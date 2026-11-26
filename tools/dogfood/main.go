// Why: the dogfoods are real programs that test Mk as a user meets it; they
// need one runner that builds them on every target, checks their cases and
// measures which library lines they use.
//
// What: the harness type, the list, run and cover commands and their flags,
// setup, buildMk and the exec-jobs child's entry.
//
// How: everything runs as a process (mk, QEMU, the WSL lock's script); mk
// is built once per build ID and kept under <work>/dogfood, and each run
// works in a scratch directory of its own.

// Command dogfood is the harness over Dogfoods/ (its contract is
// Dogfoods/README.md):
//
//	go run ./tools/dogfood list  [-dir D] [-only a,b]
//	go run ./tools/dogfood run   [-only a,b] [-target t,...] [-profile dev,release] [-changed]
//	                             [-dir D] [-work W] [-mk path] [-json out.json]
//	go run ./tools/dogfood cover [-only a,b] [-target t,...] [-dir D] [-work W] [-mk path]
//	                             [-coverage out.md] [-exempt EXEMPT.md] [-json out.json]
//
// list prints each dogfood with its line count and the library lines it
// owns, and exits 1 when a README lists a line the library surface of
// doc/reference.md does not have.
// run builds each dogfood with mk for each target and profile and runs its
// cases one at a time: Windows on the host, Linux through wsl.exe under
// tools/wsl-lock.ps1 (the harness starts itself as exec-jobs there), a
// freestanding dogfood in QEMU (or its ELF checked when QEMU is absent);
// it writes <work>/dogfood-last.json and exits 1 unless every run passed
// (a run passes when some case ran and none failed, within 30 s a case and
// 3 min a suite) and every dogfood meets the minimum line count.
// -changed runs only the dogfoods whose files, std modules or mk changed
// since their last pass in a whole-folder run (<work>/dogfood-state/).
// Each run keeps its executables, case directories, profiles and job
// files in a scratch directory of its own. cover builds with --release
// --pgo-gen, runs the cases on Windows (and Linux with -target), merges
// the profiles with mk profile merge, maps each record with a non-zero
// entry count to its std function, adds the static references of mk query
// uses --json, writes Dogfoods/COVERAGE.md, and exits 1 when a library
// line is neither covered nor exempt. A line counts as executed when some
// case ran it on every hosted target cover ran on, except a line of a
// hardware-bound module (std.gpu, std.window and their submodules): a case
// limited by a .targets file running it on its own target is enough.
//
// The harness imports nothing of the compiler (internal/archtest checks): it
// runs mk, QEMU and the WSL lock's script as processes. It builds mk from
// ./cmd/mk into <work>/dogfood/mk-<build ID>.exe unless -mk names one.
// -work defaults to the work root tools/gate uses ($MK_WORK, else mk-work in
// the user's cache directory; CONTRIBUTING.md).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"mk/tools/machinelock"
)

// harness is one run's settings and state.
type harness struct {
	root     string
	dir      string // Dogfoods
	work     string
	mk       string
	jsonOut  string
	pwsh     string
	targets  []string
	profiles []string
	only     []string
	// partial names a run that does not stand for the whole folder (-only,
	// or a cover that leaves out its default target): its report and coverage
	// file go under <work>/dogfood/only, named by it.
	partial string
	out     io.Writer
	items   []Item
	rep     *Report
	locked  bool              // the machine lock is held (by the gate)
	why     map[string]string // dogfood -> why -changed selected it
	passed  map[string]string // stateKey -> digest, runs that passed
	digests map[string]string
	wslDown string
	// mkID is the compiler's identity (compilerID, or -mk's build ID), in every digest, so
	// a dogfood that passed with another compiler runs again (-changed).
	mkID string
	// scratch is this run's own directory under <work>/dogfood/runs: the
	// executables, the case working directories, the profiles and the job
	// files, so harnesses that run at once on one -work never share one.
	scratch string
	// caseLimit and suiteLimit are the 30 seconds a case (all its processes
	// and seeds) and 3 minutes a suite per target and profile; minLines and
	// minLinesFree are the minimum line counts, 800 and 400 freestanding.
	caseLimit, suiteLimit  time.Duration
	minLines, minLinesFree int
	// lockedRunner runs a dogfood's Linux jobs under the WSL lock
	// (execLocked); tests replace it so that no test starts wsl.exe.
	lockedRunner func(d *Dogfood, jobs []Job) (*JobResults, error)
	// bench are the bench baselines read from and written to the state
	// files: stateKey -> the best time seen.
	bench map[string]*BenchBase
	// ptyrun is ptyrun's executable per target, built at the first
	// terminal case ("!" and the error when its build failed).
	ptyrun map[string]string
}

// newHarness is a harness with the default limits: 30 s a case, 3 min a
// suite, at least 800 lines of Mk (400 for a freestanding dogfood).
func newHarness(out io.Writer) *harness {
	return &harness{out: out, why: map[string]string{}, passed: map[string]string{}, digests: map[string]string{},
		bench: map[string]*BenchBase{}, caseLimit: 30 * time.Second, suiteLimit: 3 * time.Minute, minLines: 800, minLinesFree: 400}
}

// minFor is the minimum line count for d.
func (h *harness) minFor(d *Dogfood) int {
	if d.Free {
		return h.minLinesFree
	}
	return h.minLines
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "exec-jobs":
		return execJobs(args[1:])
	case "list", "run", "cover":
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "dogfood: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
	if err := machinelock.CheckEnv(); err != nil {
		fmt.Fprintf(stderr, "dogfood: %v\n", err)
		return 2
	}
	cmd := args[0]
	h := newHarness(stdout)
	fs := flag.NewFlagSet("dogfood "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	only := fs.String("only", "", "comma-separated dogfood names (the report and COVERAGE.md then go under <work>/dogfood/only)")
	fs.StringVar(&h.dir, "dir", "", "the Dogfoods directory (default <root>/Dogfoods)")
	fs.StringVar(&h.work, "work", machinelock.WorkRoot(), "directory for mk, the builds, the case directories and the reports: $"+machinelock.WorkEnv+", else mk-work in the user's cache directory, as tools/gate's")
	fs.StringVar(&h.mk, "mk", "", "the mk executable (default: built from ./cmd/mk into <work>/dogfood)")
	fs.StringVar(&h.jsonOut, "json", "", "report file (default <work>/dogfood-last.json, cover: <work>/dogfood-cover.json)")
	target := fs.String("target", "", "comma-separated targets (run default: "+strings.Join(allTargets, ",")+"; cover default: "+strings.Join(coverTargets(), ",")+", without which it writes under <work>/dogfood/only; a function line must run on every hosted target given)")
	profile := fs.String("profile", "dev,release", "comma-separated profiles (run only; cover builds release)")
	changed := fs.Bool("changed", false, "run only the dogfoods changed since their last pass (run only)")
	coverage := fs.String("coverage", "", "coverage file to write (cover only; default <dir>/COVERAGE.md)")
	exempt := fs.String("exempt", "", "exemption file (cover only; default <dir>/EXEMPT.md)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "dogfood %s: unexpected arguments %q\n", cmd, fs.Args())
		return 2
	}
	h.only = splitList(*only)
	if err := h.setup(cmd, *target, *profile); err != nil {
		fmt.Fprintf(stderr, "dogfood %s: %v\n", cmd, err)
		return 2
	}
	dogs, err := discover(h.dir, h.only, h.items)
	if err != nil {
		fmt.Fprintf(stderr, "dogfood %s: %v\n", cmd, err)
		return 2
	}
	switch cmd {
	case "list":
		return h.list(dogs)
	case "run":
		h.rep = h.newReport("run")
		if *changed {
			dogs = h.selectChanged(dogs)
		}
		return h.runCommand("run", dogs)
	default:
		cov := *coverage
		switch {
		case cov != "":
		case h.partial != "":
			// Dogfoods/COVERAGE.md is the whole folder's on both hosted
			// targets; an -only cover, or one on fewer targets,
			// writes beside its report.
			cov = filepath.Join(h.work, "dogfood", "only", h.partial+"-COVERAGE.md")
		default:
			cov = filepath.Join(h.dir, "COVERAGE.md")
		}
		ex := *exempt
		if ex == "" {
			ex = filepath.Join(h.dir, "EXEMPT.md")
		}
		return h.cover(dogs, cov, ex)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: go run ./tools/dogfood list|run|cover [flags]   (go run ./tools/dogfood <command> -h for the flags)")
	fmt.Fprintln(w, "  list   each dogfood, its line count and the section 18 lines it owns")
	fmt.Fprintln(w, "  run    build each dogfood, run its cases on each target and profile, write dogfood-last.json")
	fmt.Fprintln(w, "  cover  run the cases of --pgo-gen builds and write Dogfoods/COVERAGE.md; exit 1 on a line neither covered nor exempt")
}

// partialName names a run that does not stand for the whole folder, or
// is "" for one that does: the -only names, and for a cover without one
// of its default targets, the targets it ran on.
func partialName(cmd string, only, targets []string) string {
	p := strings.Join(only, ",")
	if cmd != "cover" {
		return p
	}
	for _, t := range coverTargets() {
		if !contains(targets, t) {
			if p != "" {
				p += "-"
			}
			return p + strings.Join(targets, ",")
		}
	}
	return p
}

// coverTargets are cover's default targets: Windows on a Windows host,
// and Linux elsewhere: cover runs on the host target alone by default
// (run checks every case on both hosted targets). A cover given -target
// x86_64-windows,x86_64-linux requires execution on both.
func coverTargets() []string {
	if runtime.GOOS == "windows" {
		return []string{targetWindows}
	}
	return []string{targetLinux}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// setup finds the module root and the reference's library surface, checks the
// targets and profiles, and, for run and cover, makes mk.
func (h *harness) setup(cmd, target, profile string) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	for d := wd; ; d = filepath.Dir(d) {
		if b, err := os.ReadFile(filepath.Join(d, "go.mod")); err == nil && strings.HasPrefix(string(b), "module mk") {
			h.root = d
			break
		}
		if filepath.Dir(d) == d {
			return fmt.Errorf("no go.mod of module mk above %s", wd)
		}
	}
	if h.dir == "" {
		h.dir = filepath.Join(h.root, "Dogfoods")
	}
	if h.dir, err = filepath.Abs(h.dir); err != nil {
		return err
	}
	if h.work, err = filepath.Abs(h.work); err != nil {
		return err
	}
	ref, err := os.ReadFile(filepath.Join(h.root, "doc", "reference.md"))
	if err != nil {
		return err
	}
	if h.items, err = parseReferenceAPI(string(ref)); err != nil {
		return err
	}
	h.targets = splitList(target)
	if len(h.targets) == 0 {
		h.targets = allTargets
		if cmd == "cover" {
			h.targets = coverTargets()
		}
	}
	for _, t := range h.targets {
		if !contains(allTargets, t) {
			return fmt.Errorf("-target %q: want some of %s", t, strings.Join(allTargets, ","))
		}
	}
	h.profiles = splitList(profile)
	for _, p := range h.profiles {
		if !contains(profiles, p) {
			return fmt.Errorf("-profile %q: want dev, release or both", p)
		}
	}
	if cmd == "cover" {
		h.profiles = []string{"release"}
	}
	h.partial = partialName(cmd, h.only, h.targets)
	if h.jsonOut == "" {
		// A run of a few dogfoods (-only) does not replace the
		// report of a whole run.
		name := "dogfood-last.json"
		if cmd == "cover" {
			name = "dogfood-cover.json"
		}
		if h.partial != "" {
			name = filepath.Join("dogfood", "only", h.partial+"-"+name)
		}
		h.jsonOut = filepath.Join(h.work, name)
	}
	h.locked = os.Getenv("MK_MACHINE_LOCKED") == "1"
	if p, err := exec.LookPath("pwsh"); err == nil {
		h.pwsh = p
	}
	if cmd == "list" {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(h.work, "dogfood"), 0o755); err != nil {
		return err
	}
	if h.mk == "" {
		if err = h.buildMk(); err == nil {
			h.mkID, err = compilerID(h.root)
		}
	} else if h.mk, err = filepath.Abs(h.mk); err == nil {
		h.mkID = mkIdentity(h.mk)
	}
	if err != nil {
		return err
	}
	return h.newScratch()
}

// compilerID is the identity of an mk built from the tree, for the
// digest: the SHA-256 of the compiler's Go sources (cmd/ and internal/,
// without tests) and go.mod. mk's own build ID would not do: it hashes the
// executable, which embeds std (each dogfood's digest follows the std
// modules it reaches instead) and the commit (a commit alone changes no
// compiler).
func compilerID(root string) (string, error) {
	var files []string
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	files = append(files, filepath.Join(root, "go.mod"))
	sort.Strings(files)
	sum := sha256.New()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(root, f)
		x := sha256.Sum256(b)
		fmt.Fprintf(sum, "%s\x00%x\n", filepath.ToSlash(rel), x)
	}
	return "src " + hex.EncodeToString(sum.Sum(nil)), nil
}

// mkIdentity is the identity of an mk named by -mk, for the digest: its
// build ID from mk version, or the SHA-256 of the file when it prints
// none.
func mkIdentity(mk string) string {
	if out, err := exec.Command(mk, "version").Output(); err == nil {
		if id := buildID(string(out)); id != "" {
			return id
		}
	}
	b, err := os.ReadFile(mk)
	if err != nil {
		return "unreadable " + mk
	}
	x := sha256.Sum256(b)
	return hex.EncodeToString(x[:])
}

// scratchAge is how old another run's scratch directory must be before a
// run removes it: no run lasts that long, so its owner is gone.
const scratchAge = 48 * time.Hour

// newScratch makes this run's scratch directory and removes the ones runs
// that ended long ago left behind (a failed run keeps its own).
func (h *harness) newScratch() error {
	runs := filepath.Join(h.work, "dogfood", "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		return err
	}
	if ents, err := os.ReadDir(runs); err == nil {
		for _, e := range ents {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > scratchAge {
				os.RemoveAll(filepath.Join(runs, e.Name()))
			}
		}
	}
	d, err := os.MkdirTemp(runs, "run-")
	h.scratch = d
	return err
}

// buildMk builds ./cmd/mk and keeps it as <work>/dogfood/mk-<build ID>,
// so harnesses that run at once (the gate and others) never replace an mk
// another one is running: a build lands under a name of its own, and is
// renamed to its build ID's name only when no file has that name yet.
func (h *harness) buildMk() error {
	goExe, err := exec.LookPath("go")
	if err != nil {
		goExe = filepath.Join(runtime.GOROOT(), "bin", "go"+exeSuffix())
	}
	dir := filepath.Join(h.work, "dogfood")
	tmp := filepath.Join(dir, fmt.Sprintf("mk-build-%d%s", os.Getpid(), exeSuffix()))
	c := exec.Command(goExe, "build", "-o", tmp, "./cmd/mk")
	c.Dir = h.root
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("go build ./cmd/mk: %v\n%s", err, out)
	}
	out, err := exec.Command(tmp, "version").Output()
	id := buildID(string(out))
	if err != nil || id == "" {
		os.Remove(tmp)
		return fmt.Errorf("mk version: %v %q", err, out)
	}
	h.mk = filepath.Join(dir, "mk-"+id+exeSuffix())
	if _, err := os.Stat(h.mk); err == nil {
		os.Remove(tmp)
		return nil
	}
	if err := os.Rename(tmp, h.mk); err != nil {
		if _, serr := os.Stat(h.mk); serr == nil { // another harness renamed the same build first
			os.Remove(tmp)
			return nil
		}
		return err
	}
	return nil
}

// buildID is the id on mk version's "mk build <id> (...)" line, whichever
// line it is ("mk 1.1.0 commit ..." comes first in mk 1.1 and later), or "".
func buildID(version string) string {
	for _, l := range strings.Split(version, "\n") {
		if f := strings.Fields(l); len(f) >= 3 && f[0] == "mk" && f[1] == "build" {
			return f[2]
		}
	}
	return ""
}

// linesProblem is the line-count verdict on d (at least minLines of Mk,
// minLinesFree for a freestanding dogfood), or "".
func (h *harness) linesProblem(d *Dogfood) string {
	if m := h.minFor(d); d.Lines < m {
		return fmt.Sprintf("too small: %d lines of Mk, under the minimum of %d", d.Lines, m)
	}
	return ""
}

// list prints each dogfood: name, lines, targets, imports and the owned
// lines; exit 1 when a dogfood is under the minimum line count or a README
// lists a line the library surface does not have.
func (h *harness) list(dogs []*Dogfood) int {
	status := 0
	if len(dogs) == 0 {
		fmt.Fprintf(h.out, "no dogfood under %s\n", h.dir)
	}
	for _, d := range dogs {
		warn := ""
		if p := h.linesProblem(d); p != "" {
			warn = " (" + p + ")"
			status = 1
		}
		fmt.Fprintf(h.out, "%s: %d lines%s; bins %s; targets %s; owns %d section 18 lines\n",
			d.Name, d.Lines, warn, strings.Join(d.Bins, " "), strings.Join(d.Targets(), " "), len(d.Owned))
		if len(d.Imports) > 0 {
			fmt.Fprintf(h.out, "  imports %s\n", strings.Join(d.Imports, " "))
		}
		for _, id := range d.Owned {
			fmt.Fprintf(h.out, "  %s\n", id)
		}
		for _, b := range d.OwnedErr {
			fmt.Fprintf(h.out, "  NOT IN SECTION 18: %s\n", b)
			status = 1
		}
	}
	return status
}

// runCommand is run: build and run the cases, write the report and the
// state.
func (h *harness) runCommand(cmd string, dogs []*Dogfood) int {
	if h.rep == nil {
		h.rep = h.newReport(cmd)
	}
	if len(dogs) == 0 {
		h.rep.Notes = append(h.rep.Notes, "no dogfood selected under "+h.dir)
		h.say("no dogfood to run under %s", h.dir)
	}
	h.runAll(dogs)
	return h.finish()
}

func (h *harness) newReport(cmd string) *Report {
	r := &Report{Command: cmd, Root: h.root, Dir: h.dir, Mk: h.mk, Host: runtime.GOOS + "/" + runtime.GOARCH, Started: time.Now()}
	if out, err := exec.Command(h.mk, "version").Output(); err == nil {
		r.MkVersion = strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	}
	return r
}

// finish sets OK, writes the report and the state, prints the summary and
// returns the exit status.
func (h *harness) finish() int {
	h.rep.WallSeconds = secs(time.Since(h.rep.Started))
	h.rep.OK = true
	runFailed := false
	for _, d := range h.rep.Dogfoods {
		if len(d.Problems) > 0 {
			h.rep.OK = false
		}
		for _, r := range d.Runs {
			if r.Status == "fail" {
				h.rep.OK = false
				runFailed = true
			}
		}
	}
	if c := h.rep.Coverage; c != nil && (c.Gaps > 0 || len(c.ExemptErrors) > 0 || len(c.RefErrors) > 0) {
		h.rep.OK = false
	}
	if err := h.writeJSON(h.jsonOut, h.rep); err != nil {
		fmt.Fprintf(h.out, "dogfood: %v\n", err)
		return 2
	}
	if err := h.writeState(); err != nil {
		h.say("dogfood: state not written: %v", err)
	}
	for _, n := range h.rep.Notes {
		h.say("note: %s", n)
	}
	if h.scratch != "" {
		if runFailed {
			h.say("dogfood: the failed runs' executables and working directories are kept under %s", h.scratch)
		} else {
			os.RemoveAll(h.scratch)
		}
	}
	verdict := "PASS"
	if !h.rep.OK {
		verdict = "FAIL"
	}
	h.say("dogfood %s %s; report %s", h.rep.Command, verdict, h.jsonOut)
	if !h.rep.OK {
		return 1
	}
	return 0
}

func (h *harness) writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'))
}

// writeAtomic writes path through a temporary file of its own and a
// rename, so harnesses that run at once never leave a torn file.
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func (h *harness) say(format string, args ...any) {
	fmt.Fprintf(h.out, format+"\n", args...)
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

var errUsesMissing = errors.New("mk has no query uses")
