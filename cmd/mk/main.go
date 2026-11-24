// Why: one binary, mk, serves every command (build, run, check, test, fmt,
// corpus and the rest); the commands must share flags, path handling and exit
// statuses so they behave alike.
//
// What: main and run (dispatch), the command registry (commands, register),
// parseArgs, compileFlags and its config, eachMember for `./...`, and the
// built-in commands check, test, corpus and version.
//
// How: each command parses its flags, builds a driver.Config, runs a driver
// session, prints, and returns 0 (success), 1 (diagnostics or failures), 2
// (usage or I/O) or 3 (internal error). Commands in other files register
// themselves from their init functions.

// Command mk is the Mk compiler. It holds no compiler logic: every command
// builds a driver.Config, runs a driver session, prints, and returns a
// status.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"mk/internal/driver"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// command is one subcommand.
type command struct {
	name, usage string
	run         func(args []string, stdout, stderr io.Writer) int
}

// commands is the command registry. The built-in commands are listed here in
// usage order; a newer command lives in its own file cmd/mk/<name>.go and
// calls register from that file's init, so commands can be added without
// sharing a file. Go runs the init functions of a package's files in
// file-name order, so nothing here may assign commands after another file has
// registered: builtins are appended.
var commands []command

// extra holds the commands registered by other files, kept apart so that usage
// lists the built-ins first in their order and the rest by name.
var extra []command

// register adds a command from its own file's init. A duplicate name panics at
// start-up, which every test of this package would catch.
func register(c command) {
	for _, have := range append(append([]command(nil), commands...), extra...) {
		if have.name == c.name {
			panic("mk: command registered twice: " + c.name)
		}
	}
	extra = append(extra, c)
	sort.Slice(extra, func(i, j int) bool { return extra[i].name < extra[j].name })
}

// allCommands is the built-ins followed by the registered commands.
func allCommands() []command {
	return append(append([]command(nil), commands...), extra...)
}

func init() {
	commands = []command{
		{"build", "build   [flags] [path]         build the package's bins (or the file) into executables", eachMember("build", runBuild, func() *flag.FlagSet { return newNativeFlagSet("build", io.Discard, &nativeFlags{}) })},
		{"run", "run     [flags] [path] [-- args...]   build, then run the program", runRun},
		{"check", "check   [flags] [path]         check the package through stage 7; runs clang only for C imports", eachMember("check", runCheck, func() *flag.FlagSet { return newFlagSet("check", io.Discard, &compileFlags{}) })},
		{"test", "test    [flags] [path]         build the tests and run each in a child process", eachMember("test", runTest, func() *flag.FlagSet { return newTestFlagSet(io.Discard, &testFlags{}) })},
		{"corpus", "corpus  [flags]                judge every mk block of doc/**/*.md", runCorpus},
		{"cache", "cache   size | clean [path] | prune --packages <dir>...    report, empty or prune the cache", runCache},
		{"profile", "profile merge -o out.mkprof in.mkprof...", runProfile},
		{"version", "version                        mk's build ID, Go version and the toolchain it would use", runVersion},
	}
}

// eachMember gives a command `./...`: when its path is ./... or ends in /...
// (\ too), the command runs once per member of the workspace that directory
// belongs to (driver.WorkspaceMembers), each run's output as its own, and the
// status is the highest. Outside a workspace dir/... names the package in dir
// alone. With --json each member's document is printed in member order; mk
// build's -o is refused when there are several members. A flag value is never
// taken for the path: only the argument parseArgs would take as the path is
// rewritten (flags gives the command's flags).
func eachMember(name string, cmd func([]string, io.Writer, io.Writer) int, flags func() *flag.FlagSet) func([]string, io.Writer, io.Writer) int {
	return func(args []string, stdout, stderr io.Writer) int {
		fs := flags()
		i := memberPattern(args, fs)
		if i < 0 {
			return cmd(args, stdout, stderr)
		}
		dir := strings.TrimSuffix(strings.ReplaceAll(args[i], `\`, "/"), "...")
		if dir = strings.TrimSuffix(dir, "/"); dir == "" {
			dir = "/"
		}
		members, err := driver.WorkspaceMembers(driver.Config{Root: dir})
		if err != nil {
			fmt.Fprintf(stderr, "mk %s: %v\n", name, err)
			return 2
		}
		if len(members) == 0 {
			members = []string{dir}
		}
		// -o names one executable or one directory, which several members'
		// bins would overwrite: refused (each member builds to its default).
		if _, _, err := parseArgs(fs, args, false); err == nil && len(members) > 1 {
			if o := fs.Lookup("o"); o != nil && o.Value.String() != "" {
				fmt.Fprintf(stderr, "mk %s: -o names one output, but %s names %d members; build each member with its own -o\n", name, args[i], len(members))
				return 2
			}
		}
		status := 0
		for _, m := range members {
			a := slices.Clone(args)
			a[i] = m
			status = max(status, cmd(a, stdout, stderr))
		}
		return status
	}
}

// memberPattern is the index of the path argument when it is ./... or
// ends in /... (or \...), else -1: the first argument before "--" that is
// no flag and no separate value of one of fs's non-boolean flags.
func memberPattern(args []string, fs *flag.FlagSet) int {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return -1
		}
		if strings.HasPrefix(a, "-") {
			name := strings.TrimLeft(a, "-")
			if f := fs.Lookup(name); f != nil && !strings.Contains(name, "=") {
				if b, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !b.IsBoolFlag() {
					i++ // the value
				}
			}
			continue
		}
		if s := strings.ReplaceAll(a, `\`, "/"); s == "./..." || strings.HasSuffix(s, "/...") {
			return i
		}
		return -1 // the path, and no pattern
	}
	return -1
}

// run dispatches one command line and returns the exit status. The driver
// turns every ICE into an E0000 itself; the recover here is the last net, so
// that even a panic outside every Guard prints one line and exits 3, never a
// Go stack.
func run(args []string, stdout, stderr io.Writer) (status int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(stderr, "mk: internal compiler error:", r)
			status = 3
		}
	}()
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(stdout)
		return 0
	}
	for _, c := range allCommands() {
		if c.name == args[0] {
			return c.run(args[1:], stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "mk: unknown command %q\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: mk <command> [flags] [path]")
	fmt.Fprintln(w)
	for _, c := range allCommands() {
		fmt.Fprintln(w, "  mk "+c.usage)
	}
}

// errUsage is a command-line error already explained to the user.
var errUsage = errors.New("usage")

// parseArgs parses flags and at most one positional path in any order:
// fs.Parse stops at the first non-flag argument, so the loop takes
// that argument as the path and parses the rest again. "--" ends the flags;
// what follows it is returned as rest when allowRest, and is a usage error
// otherwise.
func parseArgs(fs *flag.FlagSet, args []string, allowRest bool) (path string, rest []string, err error) {
	for i, a := range args {
		if a == "--" {
			if !allowRest {
				return "", nil, fmt.Errorf("unexpected arguments after --")
			}
			rest = append([]string{}, args[i+1:]...)
			args = args[:i]
			break
		}
	}
	for {
		if err := fs.Parse(args); err != nil {
			return "", nil, err
		}
		left := fs.Args()
		if len(left) == 0 {
			return path, rest, nil
		}
		if path != "" {
			return "", nil, fmt.Errorf("more than one path: %s and %s", path, left[0])
		}
		path, args = left[0], left[1:]
	}
}

// compileFlags are the flags every compiling command shares.
type compileFlags struct {
	json     bool
	maxDiags int
	target   string
	llvm     string
	jobs     int
	timings  bool
	noCache  bool
	offline  bool
	locked   bool
}

func newFlagSet(name string, stderr io.Writer, cf *compileFlags) *flag.FlagSet {
	fs := flag.NewFlagSet("mk "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if cf != nil {
		fs.BoolVar(&cf.json, "json", false, "diagnostics as JSON (see doc/tools.md)")
		fs.IntVar(&cf.maxDiags, "max-diagnostics", -1, "cap on diagnostics; 0 means no cap (default: none for JSON, 50 for text)")
		fs.StringVar(&cf.target, "target", "", "target name (default: the host's)")
		fs.StringVar(&cf.llvm, "llvm", "", "directory holding the LLVM tools")
		fs.IntVar(&cf.jobs, "j", 0, "worker bound (default: the number of CPUs)")
		fs.BoolVar(&cf.timings, "timings", false, "per-stage timings on stderr")
		fs.BoolVar(&cf.noCache, "no-cache", false, "run every stage and write nothing to the cache")
		fs.BoolVar(&cf.offline, "offline", false, "never fetch: url dependencies come from the cache or vendor/ (or MK_OFFLINE=1)")
		fs.BoolVar(&cf.locked, "locked", false, "refuse a dependency version pkg.sum has no line for (E0202 sum-missing), and never write pkg.sum")
	}
	return fs
}

// config turns the shared flags into a driver.Config.
func (cf *compileFlags) config(path string, stderr io.Writer) (driver.Config, bool) {
	// GenView: the commands that check a package write its gen/ views (E0904).
	// CacheDir: the per-user cache (cache.go userCacheDir).
	cfg := driver.Config{Root: path, Jobs: cf.jobs, NoCache: cf.noCache, Offline: cf.offline, Locked: cf.locked, LLVMDir: cf.llvm, GenView: true, CacheDir: userCacheDir()}
	if cf.target != "" {
		t, names, ok := driver.TargetNamed(cf.target)
		if !ok {
			fmt.Fprintf(stderr, "mk: unknown target %q (one of %s)\n", cf.target, strings.Join(names, ", "))
			return cfg, false
		}
		cfg.Target = t
	}
	switch {
	case cf.maxDiags >= 0:
		cfg.MaxDiags = cf.maxDiags
	case cf.json:
		cfg.MaxDiags = 0 // no cap for JSON
	default:
		cfg.MaxDiags = driver.DefaultTextCap
	}
	if cf.timings {
		cfg.Timings = stderr
	}
	if !framePointersEnv(&cfg, stderr) {
		return cfg, false
	}
	return cfg, true
}

// runCheck is `mk check`: JSON to stdout, text to stderr.
func runCheck(args []string, stdout, stderr io.Writer) int {
	var cf compileFlags
	fs := newFlagSet("check", stderr, &cf)
	allTargets := fs.Bool("all-targets", false, "also translate C imports and check pkg.Select modules across every declared target (E0213)")
	path, _, err := parseArgs(fs, args, false)
	if err != nil {
		return usageErr(stderr, fs, err)
	}
	cfg, ok := cf.config(path, stderr)
	if !ok {
		return 2
	}
	cfg.AllTargets = *allTargets
	cfg.LazyStd = true
	s, err := driver.NewSession(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "mk check: %v\n", err)
		return 2
	}
	// Every compiles fix is recompiled before it is offered.
	res, err := s.CheckVerified()
	if err != nil {
		fmt.Fprintf(stderr, "mk check: %v\n", err)
		return 2
	}
	out := stderr
	if cf.json {
		out = stdout
	}
	if err := res.WriteReport(out, cf.json); err != nil {
		fmt.Fprintf(stderr, "mk check: %v\n", err)
		return 2
	}
	_ = res.WriteTimings(cfg.Timings)
	return driver.ExitStatus(res.Report)
}

// testFlags are `mk test`'s own flags.
type testFlags struct {
	compileFlags
	run     string
	timeout string
	release bool
	std     string
	sim     string // --sim SEED; "" when absent
	prop    bool

	// --fuzz <test name> and --fuzztime.
	fuzz, fuzzTime string
	// --cover and --cover-out <file>.
	cover    bool
	coverOut string
}

func newTestFlagSet(stderr io.Writer, tf *testFlags) *flag.FlagSet {
	fs := newFlagSet("test", stderr, &tf.compileFlags)
	fs.StringVar(&tf.run, "run", "", "run only the tests whose names contain this substring")
	fs.StringVar(&tf.timeout, "timeout", "60s", "per test child process; 0 disables")
	fs.BoolVar(&tf.release, "release", false, "release profile")
	fs.StringVar(&tf.std, "std", "", "test std's own test blocks of this module and the modules under it (std for all); takes no path")
	fs.StringVar(&tf.sim, "sim", "", "run every test under simulated Io with this seed (an unsigned decimal)")
	fs.BoolVar(&tf.prop, "prop", false, "run the property tester over the contracts")
	fs.StringVar(&tf.fuzz, "fuzz", "", "run the fuzz target of the test of exactly this name under the mutation loop")
	fs.StringVar(&tf.fuzzTime, "fuzztime", "10s", "with --fuzz: how long the loop runs")
	fs.BoolVar(&tf.cover, "cover", false, "count the package's blocks and print line coverage per file")
	fs.StringVar(&tf.coverOut, "cover-out", "", "with --cover: also write the coverage as lcov to this file")
	return fs
}

// runTest is `mk test`: compile diagnostics go to stderr, as text or
// with --json as the diagnostic document; the test report goes to
// stdout, as text or with --json as one JSON object. Exit 0 when every
// test passed, 1 when one failed, else the compile status.
func runTest(args []string, stdout, stderr io.Writer) int {
	var tf testFlags
	fs := newTestFlagSet(stderr, &tf)
	path, _, err := parseArgs(fs, args, false)
	if err != nil {
		return usageErr(stderr, fs, err)
	}
	cfg, ok := tf.config(path, stderr)
	if !ok {
		return 2
	}
	timeout, err := time.ParseDuration(tf.timeout)
	if err != nil || timeout < 0 {
		fmt.Fprintf(stderr, "mk test: bad --timeout %q\n", tf.timeout)
		return 2
	}
	cfg.TestTimeout = timeout
	cfg.StdTests = tf.std
	cfg.Profile = driver.ProfileTest
	if tf.release {
		cfg.Profile = driver.ProfileRelease
	}
	s, err := driver.NewSession(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "mk test: %v\n", err)
		return 2
	}
	opt := driver.TestOptions{Run: tf.run, Prop: tf.prop, Fuzz: tf.fuzz, Cover: tf.cover || tf.coverOut != ""}
	if opt.FuzzTime, err = time.ParseDuration(tf.fuzzTime); err != nil || opt.FuzzTime <= 0 {
		fmt.Fprintf(stderr, "mk test: bad --fuzztime %q\n", tf.fuzzTime)
		return 2
	}
	if tf.sim != "" {
		seed, err := strconv.ParseUint(tf.sim, 10, 64)
		if err != nil {
			fmt.Fprintf(stderr, "mk test: bad --sim %q: want an unsigned decimal seed\n", tf.sim)
			return 2
		}
		opt.Sim, opt.SimSet = seed, true
	}
	res, rep, err := s.Test(opt)
	if err != nil {
		fmt.Fprintf(stderr, "mk test: %v\n", err)
		return 2
	}
	if len(res.Report.Diags) > 0 || rep == nil {
		if err := res.WriteReport(stderr, tf.json); err != nil {
			fmt.Fprintf(stderr, "mk test: %v\n", err)
			return 2
		}
		_ = res.WriteTimings(cfg.Timings)
		return driver.ExitStatus(res.Report)
	}
	_ = res.WriteTimings(cfg.Timings)
	if tf.fuzz != "" && len(rep.Tests) == 0 {
		fmt.Fprintf(stderr, "mk test: no test named %q\n", tf.fuzz)
		return 1
	}
	if tf.json {
		err = rep.WriteJSON(stdout)
	} else {
		err = rep.WriteText(stdout, tf.timings)
	}
	if err != nil {
		fmt.Fprintf(stderr, "mk test: %v\n", err)
		return 2
	}
	if opt.Cover && len(rep.Tests) == 0 {
		fmt.Fprintln(stderr, "mk test: no test matched; no coverage written")
	}
	if rep.Cover != nil && !tf.json {
		_ = rep.Cover.WriteText(stdout)
	}
	if tf.coverOut != "" && rep.Cover != nil {
		f, err := os.Create(tf.coverOut)
		if err == nil {
			err = rep.Cover.WriteLcov(f, coverRoot(cfg.Root))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			fmt.Fprintf(stderr, "mk test: --cover-out: %v\n", err)
			return 2
		}
	}
	return rep.ExitStatus()
}

// corpusFlags are corpus.Options, plus -v. -v is accepted (scripts ask for
// one line per unit); corpus.WriteText already prints one line per unit, so
// -v changes no byte of the report (corpus.Options has no Verbose field).
type corpusFlags struct {
	root      string
	all       bool
	only      string
	noPending bool
	build     bool
	json      bool
	verbose   bool
	jobs      int
	// --llvm belongs to every command and --max-diagnostics, --timings to
	// every compiling one; they reach the corpus session's driver.Config.
	llvm     string
	maxDiags int
	timings  bool
	// target is --target: the target every unit is judged for, the
	// host's by default. A unit marked for one host only is skipped on
	// any other.
	target string
}

func newCorpusFlagSet(stderr io.Writer, c *corpusFlags) *flag.FlagSet {
	fs := flag.NewFlagSet("mk corpus", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.root, "root", "", "repository root (default: found from the working directory)")
	fs.BoolVar(&c.all, "all", false, "also judge the tests/conformance units")
	fs.StringVar(&c.only, "only", "", "a path.Match pattern over document:line")
	fs.BoolVar(&c.noPending, "no-pending", false, "an unlisted pending unit counts as failed")
	fs.BoolVar(&c.build, "build", false, "also build ok units, and link and run those with a Main")
	fs.BoolVar(&c.json, "json", false, "JSON Lines report on stdout")
	fs.BoolVar(&c.verbose, "v", false, "one report line per unit (the text report always prints them)")
	fs.IntVar(&c.jobs, "j", 0, "worker bound (default: the number of CPUs)")
	fs.StringVar(&c.llvm, "llvm", "", "directory holding the LLVM tools")
	fs.IntVar(&c.maxDiags, "max-diagnostics", -1, "cap on diagnostics; 0 means no cap (units are judged uncapped)")
	fs.BoolVar(&c.timings, "timings", false, "per-stage timings on stderr")
	fs.StringVar(&c.target, "target", "", "judge every unit for this target (default: the host's)")
	return fs
}

// driverConfig is the corpus session's driver.Config from the shared flags.
// -j bounds the session's workers too (-j is the worker bound), not
// only the corpus's unit workers. Frame pointers are mk build's
// (framePointersDefault for the host, the corpus's target, or
// MK_FRAME_POINTERS), so the corpus builds and runs what a user's build
// gives; a bad MK_FRAME_POINTERS is reported and the default kept.
func (c *corpusFlags) driverConfig(stderr io.Writer) driver.Config {
	cfg := driver.Config{LLVMDir: c.llvm, Jobs: c.jobs}
	if t, _, ok := driver.TargetNamed(c.target); ok && c.target != "" {
		cfg.Target = t
	}
	if !framePointersEnv(&cfg, stderr) {
		cfg.FramePointers = framePointersDefault(cfg.Target.Name)
	}
	if c.maxDiags > 0 {
		cfg.MaxDiags = c.maxDiags
	}
	if c.timings {
		cfg.Timings = stderr
	}
	return cfg
}

func usageErr(stderr io.Writer, fs *flag.FlagSet, err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 2
	}
	fmt.Fprintf(stderr, "%s: %v\n", fs.Name(), err)
	return 2
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mk version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	llvm := fs.String("llvm", "", "directory holding the LLVM tools")
	if path, _, err := parseArgs(fs, args, false); err != nil || path != "" {
		if err == nil {
			err = errUsage
		}
		return usageErr(stderr, fs, err)
	}
	fmt.Fprint(stdout, driver.VersionText(*llvm))
	return 0
}

// coverRoot is the package root directory for --cover-out's paths: the
// root, made absolute, or its directory when it is a file.
func coverRoot(root string) string {
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	if st, err := os.Stat(abs); err == nil && !st.IsDir() {
		return filepath.Dir(abs)
	}
	return abs
}
