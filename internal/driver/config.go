// Why: every command (check, build, run, test, query, the LSP) decides the
// same kinds of things: the target, the profile, the cache, the flags; one
// type carries all of them into a Session.
//
// What: Config, TestHooks (fault injection and observation points for
// tests), PGOMode, UsageError, normalize (the configuration a Session
// keeps), and what cmd/mk reads (HostTarget, DefaultTextCap, TargetNamed,
// ProfileRelease, ProfileChecked).
//
// How: the zero Config is a dev build for the host; normalize fills every
// default once (environment variables, absolute paths, the baked tuning),
// so later stages read a complete value.

package driver

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// Config is everything a command decides. The zero value is a dev build for
// the host target over the working directory, with every CPU, the cache on,
// the embedded std and no diagnostic cap.
type Config struct {
	Root    string        // a .mk file, a directory, or "" for the working directory
	Target  types.Target  // zero: the host's (x86_64-windows or x86_64-linux)
	Profile types.Profile // Dev, TestProfile, Release or Checked
	CPU     string
	PGO     PGOMode // PGOOff, PGODefault, PGOGen, PGOUse
	PGOFile string
	// Debug is --debug: full debug info (variables and types) in dev and
	// release builds; off by default, so a plain build's objects and link
	// are unchanged. native.go reads it where it builds codegen.Config (and
	// adds it to the compile and link jobs, whose options are in the object
	// and link keys: objKey hashes the IR and the compile job, linkKey
	// nativeOptions).
	Debug       bool
	Bin         string        // --bin; "" means every bin (build) or the only bin (run)
	TestTimeout time.Duration // --timeout for mk test children; 0 disables
	// StdTests (mk test --std=<module>) tests std's own test blocks instead
	// of a package's: those of the std modules whose path is StdTests or
	// starts with StdTests + "." ("std" selects every std module). Root must
	// be "": the package is the test runner alone, its root the working
	// directory (so the default cache is ./.mk-cache).
	StdTests string
	Jobs     int // worker bound; <= 0 means runtime.NumCPU()
	NoCache  bool
	// Offline is --offline or MK_OFFLINE=1: a url dependency comes from the
	// cache or vendor/ only, never the network.
	Offline bool
	// Locked is --locked (CI): a version with no pkg.sum line is E0202
	// sum-missing, and pkg.sum is never written.
	Locked bool
	// GenView writes the gen/ view of each package module with generated
	// output, <module dir>/gen/<target>/gen.mk, and reports a hand-edited
	// one as E0904 (genview.go). cmd/mk sets it for check, build, run and test; it is off
	// for every other caller (corpus sessions, tests), so a library session
	// never writes into the tree it reads.
	GenView bool
	// ReleaseFront drops each missed module's front-end output (ASTs,
	// resolve's Info, check's node index, the read sets) once no later stage
	// reads it (release.go), so a large check fits its memory budget. Set
	// it in a session that answers no syntax query after its check (lspapi's
	// UsesOf and TypeAt); GenView implies it, KeepSyntax overrides it.
	ReleaseFront bool
	// AllTargets is mk check --all-targets: translates C imports across
	// cTargets and verifies declared pkg.Select modules compile and export
	// matching Breaking APIs across every declared target OS (E0213).
	AllTargets bool
	// CacheVerify is MK_CACHE_VERIFY=1: every cache hit is also recomputed
	// and compared byte for byte with the stored entry; a difference is
	// E0000 where "cache verify". normalize sets it from the environment
	// when it is false.
	CacheVerify bool
	// Tune is the pass-pipeline and LTO experiment of a release build
	// (toolchain.Tuning). normalize sets it from the binary's baked value
	// (toolchain.Baked) when it is the zero Tuning, which every default
	// binary's is; tests set it directly. A non-zero Tune is hashed into the
	// link key, and what of it reaches a pack compile (fatlto, an unroll
	// threshold; CompileJob.CompileKey) into the object and pack keys.
	Tune     toolchain.Tuning
	CacheDir string // "" means MK_CACHE, else <package root>/.mk-cache; made absolute by normalize
	// CacheBudget is the store's eviction cap in bytes (cache.Options.Budget);
	// 0 means cache.DefaultBudget. Tests set a small one.
	CacheBudget int64
	StdDir      string           // "" means MK_STD, else the embedded std
	Tools       *toolchain.Tools // nil: discovered lazily, only by native commands
	// LLVMDir is --llvm: the directory holding the LLVM tools, tried first
	// by toolchain discovery; "" when not given.
	LLVMDir   string
	Timings   io.Writer   // nil: no timings
	MaxDiags  int         // diag.NoCap (0) or a cap
	BuildID   source.Hash // zero: BuildID(); tests override it
	TestHooks TestHooks   // tests only, but for mk build's three hidden benchmark flags
	// ExtraObjects are already compiled objects (COFF for x86_64-windows,
	// ELF for x86_64-linux) linked into every executable of the build,
	// after the packs and the support pack, in this order. Their bytes are
	// in the link key, so a changed object relinks. mk build --object sets
	// it (tools/bench's codegen table links a C reference kernel this way),
	// and tests that link a foreign function. normalize makes each
	// path absolute.
	ExtraObjects []string
	// ReadFile reads one source file by its absolute OS path; nil means
	// os.ReadFile. Tests inject read failures. Embedded std files are
	// never read through it. It is called from many goroutines at once.
	ReadFile func(path string) ([]byte, error)
	// KeepSyntax: no module outside std and the C imports is loaded whole
	// from the module cache (function results are still reused), so after
	// Check the session holds every package module's syntax, resolution and
	// check index, and TypeAt and UsesOf (lspapi.go) answer in every file.
	// mk lsp sets it; it changes no output.
	KeepSyntax bool
	// KeepSyntaxFor is KeepSyntax for some modules only: a package module
	// with a file for which it reports true (its SourceMap path,
	// package-relative and '/'-separated) is loaded whole, and every other
	// module may be a cache hit. mk lsp sets it to its open files. Nil:
	// none. It is called from many goroutines at once.
	KeepSyntaxFor func(path string) bool
	// BrowseStd: std is the code being read (mk lsp's check of a std
	// tree). The query index (Lookup, QueryChecked) holds every std
	// item, the unexported ones and the std-internal modules' included, and
	// UsesOf walks std's modules as well as the package's. It changes no
	// output of a check.
	BrowseStd bool
	// LazyStd: Check loads only the std modules the package reaches and
	// none of std's test files (stdlazy.go). mk check sets it; build, run
	// and test always load std lazily. Other callers (mk lsp, the Query
	// APIs) leave it false, so every later question answers from the one
	// Check without loading std again. It changes no diagnostic.
	LazyStd bool
	// Overlay holds files added to, or replacing, the package tree, keyed
	// by package-relative '/'-separated path (in single-file mode, relative
	// to the file's directory, and only that directory's files join the
	// module). They are listed with their directory's .mk files and read
	// from memory, never from or to disk. mk test --prop adds its scratch
	// file this way (prop.go), and so does mk repl.
	Overlay map[string][]byte
	// Size is mk build --profile size:
	// a release build whose packs, the support pack included, compile at
	// -Oz (profile.go). Symbols is --release --symbols:
	// release optimization, with the link keeping the symbols and the
	// debug information (a PDB on Windows, DWARF on Linux) while codegen
	// emits the release line tables only, so no object changes. Both
	// reach the keys through the compile job's level (objKey, prekey) and
	// the link job's Debug (the link key).
	Size, Symbols bool
	// FramePointers is --frame-pointers=on or MK_FRAME_POINTERS=on: every Mk
	// function keeps its frame pointer (codegen's Config.FramePointers,
	// fpsize.go). With neither, cmd/mk sets it from the build's target (its
	// framePointersDefault: off for x86_64-windows, on for the rest); the
	// zero Config other callers build with has it off.
	FramePointers bool
	// Cover builds with a counter per basic block (codegen's Config.Cover,
	// cover.go); mk test --cover sets it.
	Cover bool
}

// PGOMode says how a release build uses a profile.
type PGOMode uint8

const (
	PGODefault PGOMode = iota // default.mkprof in the package root if present
	PGOOff
	PGOGen
	PGOUse
)

// TestHooks let tests provoke what no correct program does.
type TestHooks struct {
	// StoreWritesDelay delays every store's file writes (storeWrites), so
	// a test sees what reads them before they end (walktail.go). It
	// changes no output, so the module cache stays on.
	StoreWritesDelay time.Duration
	// PanicIn names the unit that raises an ICE: a module path (its stage-3
	// work) or an item key (its stage-6 check, 21-testing
	// TestE0000_Snapshot).
	PanicIn string
	// UntypeConst names a const (an item key) whose type is cleared after
	// its module's stage 5, as a check gap would leave it: no diagnostic
	// anywhere, so lower's E0000 at a read of it is not accounted for and
	// must be reported (TestUnaccountedHeldReported). The
	// module cache is off while it is set (startFront).
	UntypeConst string
	// MaxFileSize replaces source.MaxFileSize when nonzero, so the
	// file-too-large E0101 is testable without a 4 GiB file.
	MaxFileSize uint64
	// ICEAt names a sequential driver step that raises an ICE when it
	// runs: "manifest" (the literal reader), "list", "read" (the read
	// step) or "link" (the import step). Tests check that the step's Guard
	// turns it into E0000.
	ICEAt string
	// WrapFS wraps the fs.FS through which disk and embedded trees are
	// listed, so a listing failure is testable on any OS.
	WrapFS func(fs.FS) fs.FS
	// OnStage, when set, is called at the start (begin) and end of stages
	// 5 and 6 of each module, with no worker slot held
	// (TestScheduleByDependencies). It may block.
	OnStage func(module string, st Stage, begin bool)
	// CheckPastNames runs stages 5 and 6 even when stage 3 reported, so a
	// test can compare the bytes with and without them
	// (TestPhaseRuleSameBytes).
	CheckPastNames bool
	// SlowFunc replaces --timings' 10 ms per-function threshold when
	// nonzero; a negative value lists every function, since the clock may
	// read 0 for a small body (TestTimingsSlowFunctions).
	SlowFunc time.Duration
	// OnLink, when set, sees every link job just before the linker runs
	// (TestReleaseLinkThinLTO, TestRunSkipsLinkOnHit).
	OnLink func(toolchain.LinkJob)
	// NoRunner makes the runner lookup report that this host cannot run
	// the target's executables (TestCorpusUnitRunNoRunner).
	NoRunner bool
	// OnLL, when set, receives each pack's .ll text (name, text) as
	// codegen wrote it, from codegen's workers (TestPGOChangesBranchWeights).
	// Set, it makes codegen run for every pack, as --emit-ll does.
	OnLL func(name, ll string)
	// OnPack, when set, sees each pack of a native build as emitPacks
	// decided it, from its workers: no IR change, no key change.
	OnPack func(PackEvent)
	// LLDir is mk build's hidden --emit-ll (benchmark and tooling only):
	// each bin's pack IR, the text OnLL sees, goes to
	// <LLDir>/<bin>/<pack>.ll (p0000.ll, ..., support.ll), and earlier pack
	// files there are removed first. Codegen runs for every pack when it is
	// set, so the dump is whole on an object-cache hit too. It is in no key
	// and changes no object or executable byte. normalize makes it absolute.
	LLDir string
	// CacheNow, when set, is the store's clock (cache.Options.Now), so a
	// test can age entries past cache.Grace (TestCacheStaysUnderBudget).
	CacheNow func() time.Time
	// NoOverflowChecks and NoBoundsChecks build without those checks
	// (codegen.Config.TestNoOverflowChecks, TestNoBoundsChecks), to price
	// them on tests/bench.
	// The objects differ from a checked build's, so their keys do too.
	// mk build's hidden --no-bounds-checks and --no-overflow-checks set
	// them (benchmark only).
	NoOverflowChecks bool
	NoBoundsChecks   bool
	// HTTPClient replaces the client that fetches url dependencies (a test
	// server with its own certificate).
	HTTPClient *http.Client
	// CImportWhole gives the front end each C binding whole instead of the
	// items its importers name (cimport.PruneAll), so a test can check the
	// whole of a large header's binding (TestCImport_WindowsHMeasure).
	CImportWhole bool
	// PlaceObject, when set, replaces the hard link or copy that places a
	// stored pack object in the work directory for a staged link
	// (stagedHits), so a test can make it fail as a store emptied during
	// the build does (one E0001 for all).
	PlaceObject func(src, dst string) error
	// EmitMissObjects makes emitPacks look no pack object up in the store,
	// so every pack writes its .ll and its object hits only at the compile
	// step, as when the store gains it between the two (compilePacks's
	// recompile of a hit it cannot place).
	EmitMissObjects bool
	// OnVersionRun, when set, is called each time toolchain discovery
	// runs `clang --version` as a process, with clang's path (a compile
	// harness counts the processes a build starts). It changes
	// nothing the build computes.
	OnVersionRun func(clang string)
	// NoTrapForm builds an x86_64-windows release executable with every
	// failing check in the call form (trapform.go), so a test compares the
	// two forms' output. The packs differ, so their keys do too.
	NoTrapForm bool
}

// runner is toolchain.Runner for the build's target, or no runner under
// TestHooks.NoRunner.
func (c *Config) runner() ([]string, bool) {
	if c.TestHooks.NoRunner {
		return nil, false
	}
	return toolchain.Runner(c.Target.LLVMTriple)
}

// hook raises the ICE of TestHooks.ICEAt when step is the one named.
func (c *Config) hook(step string) {
	if c.TestHooks.ICEAt == step {
		source.ICEf(source.Span{}, "test hook: ICE in step %s", step)
	}
}

// UsageError is a command-line or configuration error: exit status 2, never
// a diagnostic.
type UsageError struct {
	Msg string
	// NoSources: the root is a directory with no .mk file and no pkg.mk
	// (mk query answers std's names there).
	NoSources bool
}

func (e *UsageError) Error() string { return e.Msg }

func usagef(format string, args ...any) *UsageError {
	return &UsageError{Msg: fmt.Sprintf(format, args...)}
}

// HostTarget is the build target that matches the running OS and
// architecture: x86_64-windows on Windows, aarch64-macos or x86_64-macos on
// macOS, aarch64-linux on arm64 Linux, x86_64-linux elsewhere.
func HostTarget() types.Target {
	arm := runtime.GOARCH == "arm64"
	switch {
	case runtime.GOOS == "windows":
		return types.X86_64Windows
	case runtime.GOOS == "darwin" && arm:
		return types.Aarch64Macos
	case runtime.GOOS == "darwin":
		return types.X86_64Macos
	case arm:
		return types.Aarch64Linux
	}
	return types.X86_64Linux
}

// targetOS is the os name pkg.Target selections use: "windows", "linux",
// "macos", the part of the target name after its last '-'.
func targetOS(t types.Target) string {
	for i := len(t.Name) - 1; i >= 0; i-- {
		if t.Name[i] == '-' {
			return t.Name[i+1:]
		}
	}
	return t.Name
}

// stdTargetMissing is nil when std's manifest selects its system modules
// for t's OS (or selects nothing per target), else one E0202, reason
// no-std-target, naming the targets std is built for. Without it a target
// std has no std.sys half for fails inside std, with an E0202 for every std
// file that imports std.sys. A hosted target only: x86_64-freestanding forms
// layers 0 and 1 of std and std.free (stdFormed), which need no selection.
func stdTargetMissing(t types.Target, man *Manifest) *diag.Diagnostic {
	if man == nil || len(man.Targets) == 0 || t.OS == types.OSFreestanding {
		return nil
	}
	oses := map[string]bool{}
	for _, sel := range man.Targets {
		oses[sel.OS] = true
	}
	if oses[targetOS(t)] {
		return nil
	}
	// The targets mk build refuses (notBuilt) are named apart: following
	// the hint for build, run or test must not lead to E0202 not-built.
	var built, checked []string
	for _, x := range types.AllTargets() {
		switch {
		case !oses[targetOS(x)] && x.OS != types.OSFreestanding:
		case notBuilt(x) != nil:
			checked = append(checked, x.Name)
		default:
			built = append(built, x.Name)
		}
	}
	msg := "the standard library has no system layer for " + t.Name + "; mk builds for " + strings.Join(built, ", ")
	if len(checked) > 0 {
		msg += ", and checks only for " + strings.Join(checked, ", ")
	}
	return diag.New(diag.E0202, source.Span{}, msg).
		Text("target", t.Name).Text("reason", "no-std-target").Alt("pass --target with one of the targets it builds or checks for")
}

// hostArch is the architecture mk runs on, as a types.Arch.
func hostArch() (types.Arch, bool) {
	switch runtime.GOARCH {
	case "amd64":
		return types.ArchX64, true
	case "arm64":
		return types.ArchArm64, true
	case "riscv64":
		return types.ArchRiscv64, true
	}
	return 0, false
}

// normalize fills the defaults of a zero Config and makes paths absolute.
func (c Config) normalize() (Config, error) {
	if c.Target.Name == "" {
		c.Target = HostTarget()
	} else if _, ok := types.TargetByName(c.Target.Name); !ok {
		return c, usagef("unknown target %q", c.Target.Name)
	}
	if c.Jobs <= 0 {
		c.Jobs = runtime.NumCPU()
	}
	// --cpu: "native" names the host's CPU, which is no CPU of a target of
	// another architecture (clang would answer with the host's name for any
	// triple). The name itself is resolved, and any other name checked, by
	// clang once the toolchain is known (world.buildCPU).
	if c.CPU == toolchain.CPUNative {
		if host, ok := hostArch(); !ok || host != c.Target.Arch {
			return c, usagef("--cpu=native names this machine's CPU, which is no %s CPU; name one with --cpu=<name>", c.Target.Name)
		}
	}
	if c.MaxDiags < 0 {
		return c, usagef("--max-diagnostics must not be negative")
	}
	if c.ReadFile == nil {
		c.ReadFile = os.ReadFile
	}
	if c.StdDir == "" {
		c.StdDir = os.Getenv("MK_STD")
	}
	if c.StdDir != "" {
		abs, err := filepath.Abs(c.StdDir)
		if err != nil {
			return c, usagef("std directory %s: %v", c.StdDir, err)
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return c, usagef("std directory %s is not a directory", c.StdDir)
		}
		c.StdDir = abs
	}
	// MK_CACHE is read here only, and the cache directory is
	// made absolute, so the work directory, the .ll and .o paths under
	// the store's tmp and bin are absolute (CompileJob). ""
	// stays "": each session's default (<package root>/.mk-cache, or the
	// corpus's mk-corpus under the OS temporary directory) is absolute.
	if c.StdTests != "" {
		if c.StdTests != "std" && !strings.HasPrefix(c.StdTests, "std.") {
			return c, usagef("--std=%s: not a std module path (std, std.rt, ...)", c.StdTests)
		}
		if c.Root != "" {
			return c, usagef("--std tests std's own modules and takes no path (got %s)", c.Root)
		}
	}
	if len(c.ExtraObjects) > 0 {
		objs := make([]string, len(c.ExtraObjects))
		for i, o := range c.ExtraObjects {
			abs, err := filepath.Abs(o)
			if err != nil {
				return c, usagef("extra object %s: %v", o, err)
			}
			objs[i] = abs
		}
		c.ExtraObjects = objs
	}
	if c.TestHooks.LLDir != "" {
		abs, err := filepath.Abs(c.TestHooks.LLDir)
		if err != nil {
			return c, usagef("--emit-ll directory %s: %v", c.TestHooks.LLDir, err)
		}
		c.TestHooks.LLDir = abs
	}
	if c.CacheDir == "" {
		c.CacheDir = os.Getenv("MK_CACHE")
	}
	c.CacheVerify = c.CacheVerify || os.Getenv("MK_CACHE_VERIFY") == "1"
	if c.Tune.IsZero() {
		t, err := toolchain.Baked()
		if err != nil {
			return c, usagef("this mk binary was built with a bad experiment: %v", err)
		}
		c.Tune = t
	}
	c.Offline = c.Offline || os.Getenv("MK_OFFLINE") == "1"
	if c.CacheDir != "" {
		abs, err := filepath.Abs(c.CacheDir)
		if err != nil {
			return c, usagef("cache directory %s: %v", c.CacheDir, err)
		}
		c.CacheDir = abs
	}
	return c, nil
}

// toolchainOptions is how a native step discovers LLVM when Config.Tools is
// nil: --llvm first.
func (c Config) toolchainOptions() toolchain.Options {
	return toolchain.Options{LLVMDir: c.LLVMDir}
}

// DefaultTextCap is the diagnostic cap of text output (diag.DefaultTextCap);
// JSON output has none. cmd/mk sets Config.MaxDiags from it.
const DefaultTextCap = diag.DefaultTextCap

// TargetNamed finds a target of types.AllTargets() by name (the hosted
// ones, x86_64-freestanding, and the two Nutshell targets), and
// lists every name for a usage message.
func TargetNamed(name string) (t types.Target, names []string, ok bool) {
	for _, x := range types.AllTargets() {
		names = append(names, x.Name)
	}
	t, ok = types.TargetByName(name)
	return t, names, ok
}

// ProfileRelease is types.Release, for cmd/mk (which imports only driver):
// --release sets Config.Profile to it.
const ProfileRelease = types.Release

// ProfileChecked is types.Checked, for cmd/mk: --profile checked sets
// Config.Profile to it: release's compile and link, with contracts and the
// trace hooks kept and build.Profile Checked.
const ProfileChecked = types.Checked
