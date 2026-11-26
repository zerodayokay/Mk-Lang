// Why: test programs must be built and run like a user would, with their
// output compared byte for byte, on Windows and, through WSL, on Linux.
//
// What: Runner with Build (once per program, target and profile) and Run (one
// case), RunResult, BuildError, Expected, and DropBacktrace.
//
// How: each case runs in a fresh directory holding the executable and the
// case's files, with its arguments, standard input and timeout; stdout,
// stderr and the exit status are compared byte for byte. A Linux executable
// on Windows runs through wsl.exe under timeout -s KILL inside the VM, and a
// WSL run that times out is retried once, since the client's start alone can
// be slow on a loaded machine.

package corpus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mk/internal/driver"
)

// Runner builds and runs program cases. Its Driver configuration is the
// base of every build (toolchain, cache directory, test hooks); Root,
// Target and Profile are set per build.
type Runner struct {
	root string
	cfg  driver.Config
	dir  string // executables, by (program, target, profile)

	// WSLSlots, when set, bounds the runs through wsl.exe in flight: a run
	// takes a slot before its timeout starts and gives it back after.
	// Runners of one test binary share one channel, because many wsl.exe
	// clients at once stall WSL past a case's timeout.
	WSLSlots chan struct{}

	// RunSlots, when set, bounds non-WSL child runs in flight: a run takes
	// a slot before its timeout starts and gives it back after, limiting
	// concurrent first-run executable scans and CPU contention across
	// parallel tests.
	RunSlots chan struct{}

	mu    sync.Mutex
	built map[string]*builtProgram
}

type builtProgram struct {
	once sync.Once
	exe  string
	err  error
}

// BuildError is a program that did not build: its report.
type BuildError struct {
	Program, Target, Profile string
	Result                   driver.Result
}

func (e *BuildError) Error() string {
	return fmt.Sprintf("build %s for %s %s:\n%s", e.Program, e.Target, e.Profile, e.Result.Report.Text(e.Result.Sources))
}

// RunResult is what a run produced, compared byte for byte with the case.
type RunResult struct {
	Stdout, Stderr []byte
	Exit           int
	TimedOut       bool
}

// NewRunner is a runner for the repository at root that writes executables
// under dir.
func NewRunner(root string, cfg driver.Config, dir string) *Runner {
	return &Runner{root: root, cfg: cfg, dir: dir, built: map[string]*builtProgram{}}
}

// Build builds program (repository-relative) for target and profile once
// and returns the executable's path. The error is a *BuildError for a
// report with diagnostics, else a usage or I/O error.
func (r *Runner) Build(program, target, profile string) (string, error) {
	key := program + "\x00" + target + "\x00" + profile
	r.mu.Lock()
	b := r.built[key]
	if b == nil {
		b = &builtProgram{}
		r.built[key] = b
	}
	r.mu.Unlock()
	b.once.Do(func() { b.exe, b.err = r.build(program, target, profile) })
	return b.exe, b.err
}

func (r *Runner) build(program, target, profile string) (string, error) {
	cfg := r.cfg
	t, names, ok := driver.TargetNamed(target)
	if !ok {
		return "", fmt.Errorf("target %q: want one of %s", target, strings.Join(names, ", "))
	}
	cfg.Target = t
	switch profile {
	case "dev":
	case "release":
		cfg.Profile = driver.ProfileRelease
	default:
		return "", fmt.Errorf("profile %q: want dev or release", profile)
	}
	cfg.Root = filepath.Join(r.root, filepath.FromSlash(program))
	s, err := driver.NewSession(cfg)
	if err != nil {
		return "", err
	}
	name := strings.TrimSuffix(filepath.Base(program), ".mk")
	if strings.HasSuffix(target, "-windows") {
		name += ".exe"
	}
	dir := filepath.Join(r.dir, strings.ReplaceAll(filepath.ToSlash(program), "/", "_"), target, profile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	res, paths, err := s.Build(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	if len(res.Report.Diags) > 0 || len(paths) != 1 {
		return "", &BuildError{Program: program, Target: target, Profile: profile, Result: res}
	}
	return paths[0], nil
}

// Run builds c's program for target and profile (once) and runs case c in
// a fresh run directory under the runner's directory: the executable and
// c.Files are copied in, c.Args are passed, c.Stdin is standard input, and
// a run over c.Timeout is killed (TimedOut, Exit -1).
//
// A run through wsl.exe is bounded inside the Linux VM too:
// runArgv starts it under timeout -s KILL with c.Timeout, so a hung Linux
// program is killed there, with its process group, and does not outlive its
// leaf. Killing wsl.exe ends only the Windows client and leaves the Linux
// process running (an orphaned ./spawn_set was once found still running
// after 5 h 45 min). The Windows side waits wslGrace longer,
// so the VM's kill comes first and wsl.exe exits by itself; its own deadline
// is only for a client that does not answer.
//
// A run through wsl.exe that times out is run once more, in a fresh run
// directory and with the same timeout: on a
// loaded machine starting the WSL client alone can take seconds, so one slow
// start must not fail a case, while a program that really hangs times out
// both times and still fails. The first attempt's process is dead by then,
// so the two never run side by side.
func (r *Runner) Run(c ProgramCase, target, profile string) (RunResult, error) {
	exe, err := r.Build(c.Program, target, profile)
	if err != nil {
		return RunResult{}, err
	}
	res, wsl, err := r.runOnce(c, target, exe)
	if err == nil && res.TimedOut && wsl {
		res, _, err = r.runOnce(c, target, exe)
	}
	return res, err
}

// wslGrace is how much longer than a case's timeout the Windows side waits
// for wsl.exe, whose Linux process timeout -s KILL ends at the timeout.
const wslGrace = 15 * time.Second

// wslKilled reports whether a wsl.exe run's exit status is timeout's own
// after it killed the program, and only once the run lasted its limit, so a
// program that exits with such a status by itself is not taken for a
// timeout. GNU timeout -s KILL sends KILL to its whole process group, itself
// included, and wsl.exe then exits 9 (observed with WSL Ubuntu on Windows
// 11); 124 and 137 (128 + SIGKILL) are timeout's other
// documented statuses.
func wslKilled(exit int, ran, limit time.Duration) bool {
	return (exit == 9 || exit == 124 || exit == 137) && ran >= limit
}

// runOnce is one attempt of Run; wsl reports whether it ran through wsl.exe.
func (r *Runner) runOnce(c ProgramCase, target, exe string) (RunResult, bool, error) {
	run, err := os.MkdirTemp(r.dir, "run-")
	if err != nil {
		return RunResult{}, false, err
	}
	defer os.RemoveAll(run)
	local := filepath.Join(run, filepath.Base(exe))
	if err := copyFile(exe, local, 0o755); err != nil {
		return RunResult{}, false, err
	}
	for _, f := range c.Files {
		b, err := readCommitted(filepath.Join(r.root, filepath.FromSlash(f)))
		if err == nil {
			err = os.WriteFile(filepath.Join(run, filepath.Base(f)), b, 0o644)
		}
		if err != nil {
			return RunResult{}, false, err
		}
	}
	var stdin []byte
	if c.Stdin != "" {
		if stdin, err = readCommitted(filepath.Join(r.root, filepath.FromSlash(c.Stdin))); err != nil {
			return RunResult{}, false, err
		}
	}
	argv, err := runArgv(target, run, local, c.Args, c.Timeout)
	if err != nil {
		return RunResult{}, false, err
	}
	wsl := argv[0] == "wsl.exe"
	if wsl {
		if r.WSLSlots != nil {
			r.WSLSlots <- struct{}{}
			defer func() { <-r.WSLSlots }()
		}
	} else if r.RunSlots != nil {
		r.RunSlots <- struct{}{}
		defer func() { <-r.RunSlots }()
	}
	limit := c.Timeout
	if wsl {
		limit += wslGrace
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	var out, errOut bytes.Buffer
	var start time.Time
	for try := 0; ; try++ {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = run
		// A killed client may leave its pipes open in a grandchild; Wait must
		// not hang on them.
		cmd.WaitDelay = 5 * time.Second
		out.Reset()
		errOut.Reset()
		cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &out, &errOut
		start = time.Now()
		err = cmd.Run()
		// On Linux, exec of the copy fails with ETXTBSY while a process
		// forked by another goroutine still holds the copy's write
		// descriptor between its fork and its exec. That window is short:
		// try again.
		if !errors.Is(err, syscall.ETXTBSY) || try == 20 {
			break
		}
		time.Sleep(time.Duration(try+1) * 10 * time.Millisecond)
	}
	ran := time.Since(start)
	res := RunResult{Stdout: out.Bytes(), Stderr: DropBacktrace(errOut.Bytes())}
	var ee *exec.ExitError
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.Exit, res.TimedOut = -1, true
	case err == nil:
	case errors.As(err, &ee):
		res.Exit = shellExit(ee.ProcessState)
		if wsl && wslKilled(res.Exit, ran, c.Timeout) {
			res.Exit, res.TimedOut = -1, true
		}
	default:
		return res, wsl, err
	}
	return res, wsl, nil
}

// runArgv is the command line that runs the executable exe, inside the
// run directory run, for target on this host: exe itself (an absolute path)
// for the host's OS, and wsl.exe --cd <run> -- timeout -s KILL <s> ./<exe>
// for x86_64-linux on Windows (--cd takes an absolute Windows path).
// limit, when above zero, bounds the Linux run inside the VM (whole seconds,
// rounded up): timeout runs the program in its own process
// group and kills the group, so nothing it started outlives it either. Any
// other pairing is a "no runner" error.
func runArgv(target, run, exe string, args []string, limit time.Duration) ([]string, error) {
	host := "x86_64-" + runtime.GOOS
	switch {
	case runtime.GOARCH == "amd64" && target == host:
		return append([]string{exe}, args...), nil
	case runtime.GOOS == "windows" && target == "x86_64-linux":
		if _, err := exec.LookPath("wsl.exe"); err != nil {
			return nil, fmt.Errorf("no runner for %s on this host: %v", target, err)
		}
		argv := []string{"wsl.exe", "--cd", run, "--"}
		if limit > 0 {
			secs := int64((limit + time.Second - 1) / time.Second)
			argv = append(argv, "timeout", "-s", "KILL", strconv.FormatInt(secs, 10))
		}
		return append(append(argv, "./"+filepath.Base(exe)), args...), nil
	}
	return nil, fmt.Errorf("no runner for %s on %s/%s", target, runtime.GOOS, runtime.GOARCH)
}

// shellExit is a finished process's exit status as a shell reports it,
// and as wsl.exe relays it: 128 + the signal number for a process a signal
// killed (139 for SIGSEGV), the exit code otherwise. Go's ExitCode reports
// -1 for a signal.
func shellExit(ps *os.ProcessState) int {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

// ErrNoRunner reports whether err is runArgv's "no runner" error.
func ErrNoRunner(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "no runner for ")
}

func copyFile(from, to string, mode os.FileMode) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, mode)
}

// readCommitted reads a test file as it is committed: a text file (one
// with no NUL byte, git's own test) has every CR LF turned back into LF.
// The tests' files are LF only (fizz.out is exactly 58 bytes, LF only), so
// a CR before an LF in one is a checkout's
// translation (core.autocrlf=true and no .gitattributes), never
// a byte the file means. Only the test's own files are read so (expected
// output, stdin, run files), never a program's output: a program that
// prints CR LF still differs from its .out.
func readCommitted(p string) ([]byte, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return committedBytes(b), nil
}

// committedBytes is readCommitted's translation of bytes already read.
func committedBytes(b []byte) []byte {
	if bytes.IndexByte(b, 0) >= 0 || !bytes.Contains(b, []byte("\r\n")) {
		return b
	}
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

// Expected reads a case's expected stdout and stderr bytes ("" means
// empty), as committed (readCommitted).
func (r *Runner) Expected(c ProgramCase) (stdout, stderr []byte, err error) {
	read := func(p string) ([]byte, error) {
		if p == "" {
			return nil, nil
		}
		return readCommitted(filepath.Join(r.root, filepath.FromSlash(p)))
	}
	if stdout, err = read(c.Stdout); err != nil {
		return nil, nil, err
	}
	stderr, err = read(c.Stderr)
	return stdout, stderr, err
}

// DropBacktrace is a run's standard error up to a panic report's
// "backtrace:" line: the
// backtrace's frames and addresses vary with the profile, the target and
// the build, so expected stderr files hold the report's line alone.
func DropBacktrace(b []byte) []byte {
	if bytes.HasPrefix(b, []byte("backtrace:")) {
		return b[:0]
	}
	if i := bytes.Index(b, []byte("\nbacktrace:")); i >= 0 {
		return b[:i+1]
	}
	return b
}
