// Why: Session.Build turns a clean check into executables or libraries;
// this is where the compiler hands its IR to clang and the linker, and
// where the cache must let an unchanged build skip both.
//
// What: Session.Build and its native step: bins, notBuilt, the toolchain
// (tools, startTools, versionLine), nativeJob and world.native (instances,
// packs to .ll, clang per pack, import libraries from the runtime's name
// lists, the link), the link key and its hit path, verifyBin, linkJob, and
// the mapping of tool failures to E0001 (toolErr, toolErrIn, linkErr).
//
// How: every pack's object key and the link key are formed before clang
// runs, so a hit copies the stored executable and runs neither clang nor
// the link. A work directory of this process holds every intermediate file,
// named by pack index, and the link writes a temporary file that is renamed
// into place.

package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mk/internal/cache"
	"mk/internal/codegen"
	"mk/internal/diag"
	"mk/internal/mono"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// stackReserve is the COFF stack reservation of every executable (8 MiB):
// the initial thread runs the scheduler and the system stack calls.
const stackReserve = 8 << 20

// binTarget is one executable of a build: its name and its root module.
type binTarget struct {
	name string
	mod  *modNode
	// cpu and script are the bin's pkg.Bin cpu and link_script:
	// script as an absolute path, "" for none (free.go). subsystem is its
	// subsystem: "", "console" or, on x86_64-windows, "windows".
	cpu, script, subsystem string
	// kind and abi are its pkg.Bin kind and abi: "" builds an
	// executable, "dynamic" or "static" a library (libout.go).
	kind, abi string
}

// bins lists the build's executables in name order: the bins
// of pkg.mk in package mode, else the one implicit bin. Config.Bin selects
// one; an unknown name is a usage error listing the names.
func (s *Session) bins() ([]binTarget, error) {
	var out []binTarget
	switch {
	case s.root.kind != rootPackage:
		if len(s.pkgB.mods) > 0 {
			out = append(out, binTarget{name: s.root.name, mod: s.pkgB.mods[0]})
		}
	case s.pkgMan != nil:
		for _, bin := range s.pkgMan.Bins {
			for _, m := range s.pkgB.mods {
				if m.dir == bin.Root {
					bt := binTarget{name: bin.Name, mod: m, cpu: bin.CPU, subsystem: bin.Subsystem, kind: bin.Kind, abi: bin.ABI}
					if bin.LinkScript != "" {
						bt.script = filepath.Join(s.root.dir, filepath.FromSlash(bin.LinkScript))
					}
					out = append(out, bt)
				}
			}
		}
	}
	if s.cfg.Bin == "" {
		return out, nil
	}
	var names []string
	for _, b := range out {
		if b.name == s.cfg.Bin {
			return []binTarget{b}, nil
		}
		names = append(names, b.name)
	}
	return nil, usagef("no bin named %s; the package has %s", s.cfg.Bin, strings.Join(names, ", "))
}

// notBuilt is nil when mk can build an executable for t (t.Built), else
// E0202 reason not-built: a checked-only target's layouts and ABI are
// unit-tested, but its system layer or link is not built. mk check
// type-checks for it.
func notBuilt(t types.Target) *diag.Diagnostic {
	if t.Built {
		return nil
	}
	alt := "build for x86_64-windows, x86_64-linux or aarch64-linux"
	if t.OS == types.OSNutshell {
		alt = "build x86_64-nutshell"
	}
	return diag.New(diag.E0202, source.Span{}, t.Name+" is checked only: mk check type-checks for it, but mk build, run and test are not built for it").
		Text("target", t.Name).Text("reason", "not-built").Alt(alt)
}

// Build runs Check and, when the report is clean, stages 8 to 10 for every
// bin (or Config.Bin, or the file), writing executables to out (a file
// when the build has one bin, else a directory; "" means ./<bin>[.exe] in
// the working directory). Paths are returned in bin order. A usage error
// (an unknown --bin) is an E0001-free report
// with the error in err.
func (s *Session) Build(out string) (Result, []string, error) {
	s.forBuild = true
	s.startTools()
	defer s.joinTools()
	defer s.joinNativePrep()
	if r, paths, ok := s.tryFastBuild(out); ok {
		return r, paths, nil
	}
	s.deferWalk = true // walktail.go; Check, Run and Test on this session walk synchronously
	defer func() { s.deferWalk = false }()
	r := s.runCheck()
	defer s.w.maybeGC() // once, after the executables are written
	p := s.p
	defer p.joinWalk() // never outlives the build
	// joined is the check's result once a deferred walk is through, and
	// whether the walk reported anything: then the report is a synchronous
	// walk's.
	joined := func() (Result, bool) {
		if p.tail == nil {
			return r, p.walkBad
		}
		bad := p.joinWalk()
		return p.result(s.w, s.cfg.MaxDiags), bad
	}
	if len(r.Report.Diags) > 0 {
		r, _ = joined()
		return s.verifiedReport(r), nil, nil
	}
	bins, err := s.bins()
	if err != nil {
		if wr, bad := joined(); bad {
			return wr, nil, nil
		}
		return r, nil, err
	}
	if d := notBuilt(s.cfg.Target); d != nil {
		if wr, bad := joined(); bad {
			return wr, nil, nil
		}
		s.p.diags = append(s.p.diags, d)
		return s.p.result(s.w, s.cfg.MaxDiags), nil, nil
	}
	var paths []string
	var ds []*diag.Diagnostic
	fastOuts := make([]fastBinOut, len(bins))
	for bi, bin := range bins {
		dest := out
		if dest == "" || len(bins) > 1 || outIsDir(out) {
			dest = filepath.Join(out, bin.name+exeSuffix(s.cfg.Target))
		}
		// With the cache on, the link key also names a copy of the
		// executable under <cache>/bin, so a build whose key hits copies
		// it out and runs neither clang nor the link. MK_CACHE_VERIFY
		// links every time and compares the stored copy (native). A bin
		// with a linker script links every time: the key hashes the
		// script's bytes, not the files it names (INCLUDE, INPUT, GROUP,
		// SEARCH_DIR), which lld reads by path.
		if _, lib := libKind(bin.kind); lib {
			out, bds := s.buildLibrary(bin, out, len(bins) > 1)
			ds = append(ds, bds...)
			if out != "" {
				paths = append(paths, out)
			}
			continue
		}
		keyed := ""
		if !s.cfg.NoCache && bin.script == "" {
			keyed = bin.name + exeSuffix(s.cfg.Target)
		}
		fastOuts[bi].name = bin.name
		exe, bds := s.nativeStored(bin, func(tmp string) (string, []*diag.Diagnostic) {
			if p.joinWalk() {
				return "", nil // no executable after an E0708 (walktail.go)
			}
			return placeExe(tmp, dest)
		}, keyed, &fastOuts[bi])
		ds = append(ds, bds...)
		if exe != "" {
			paths = append(paths, exe)
		}
	}
	if _, bad := joined(); bad {
		return p.result(s.w, s.cfg.MaxDiags), nil, nil // native's diagnostics are dropped (walktail.go)
	}
	if len(ds) > 0 {
		s.p.diags = append(s.p.diags, ds...)
		r = s.p.result(s.w, s.cfg.MaxDiags)
		return r, nil, nil
	}
	r = s.p.result(s.w, s.cfg.MaxDiags)
	r.Reached = StageNative
	s.recordFastBuild(bins, fastOuts, r)
	return r, paths, nil
}

// outIsDir reports whether -o out names a directory: it ends in a path
// separator or names an existing directory. The executable is then
// <out>/<bin>[.exe].
func outIsDir(out string) bool {
	if strings.HasSuffix(out, "/") || strings.HasSuffix(out, `\`) {
		return true
	}
	fi, err := os.Stat(out)
	return out != "" && err == nil && fi.IsDir()
}

// placeExe moves the linked temporary to dest: a rename, or a copy and a
// rename when dest is on another volume (moveFile). A failure (Windows
// refuses to replace a running executable) is E0001 tool: fs naming the
// path.
func placeExe(tmp, dest string) (string, []*diag.Diagnostic) {
	abs, err := filepath.Abs(dest)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(abs), 0o755)
	}
	if err == nil {
		err = moveFile(tmp, abs)
	}
	if err != nil {
		return "", []*diag.Diagnostic{diag.Toolchain("fs", "rename "+filepath.ToSlash(dest), pathErrText(err),
			"cannot write the executable "+filepath.ToSlash(dest)).Alt(altCloseExe).Alt(altOtherOut)}
	}
	return abs, nil
}

// tools is the session's toolchain: Config.Tools, or discovered once, only
// by native commands. The error is one E0001 naming the searched
// directories.
func (s *Session) tools() (*toolchain.Tools, *diag.Diagnostic) {
	if s.cfg.Tools != nil {
		return s.cfg.Tools, nil
	}
	s.startTools()
	<-s.disc.done
	if s.disc.err != nil {
		return nil, toolErr(s.disc.err, "no usable LLVM toolchain: "+s.disc.err.Error())
	}
	s.cfg.Tools = s.disc.t
	return s.disc.t, nil
}

// discovery is one toolchain.Discover run: done closes when t and err are
// set.
type discovery struct {
	once sync.Once
	done chan struct{}
	t    *toolchain.Tools
	err  error
}

// startTools starts the discovery tools waits for, once, on its own
// goroutine: a native command (build, run, test) starts it before its
// check, so its work overlaps the front end. Only native commands call it;
// its result is read only after a clean check, so a check that
// fails reports no toolchain error. joinTools waits for it, so it never
// outlives the command. With the cache on, `clang --version` runs only when
// clang's file is not the one it last ran (versionLine).
func (s *Session) startTools() {
	if s.cfg.Tools != nil {
		return
	}
	s.disc.once.Do(func() {
		s.disc.done = make(chan struct{})
		go func() {
			defer close(s.disc.done)
			if ie := source.Guard("toolchain discovery", source.Span{}, func() {
				run := toolchain.VersionOutput
				if h := s.cfg.TestHooks.OnVersionRun; h != nil {
					run = func(clang string) (string, error) {
						h(clang)
						return toolchain.VersionOutput(clang)
					}
				}
				// The world's store opens after this starts (runCheck);
				// this one is its own handle on the same directory, as
				// another process's would be.
				var st *cache.Store
				if !s.cfg.NoCache && !s.cfg.CacheVerify {
					st, _ = cache.Open(s.cacheDir(), cache.Options{Budget: s.cfg.CacheBudget, Now: s.cfg.TestHooks.CacheNow})
				}
				s.disc.t, s.disc.err = toolchain.Discover(toolchain.Options{LLVMDir: s.cfg.LLVMDir,
					VersionLine: func(clang string) (string, error) { return versionLine(st, clang, run) }})
			}); ie != nil {
				s.disc.err = fmt.Errorf("%s", ie.What)
			}
		}()
	})
}

// versionDomain is the hash domain of a stored `clang --version` output.
const versionDomain = "mk.driver.clangversion.v1"

// versionLine is `<clang> --version`'s output by run, memoized in st:
// a stat hint of clang's file (cache.Hints: its size, mtime and
// change identity, with the racy rule; doc/compiler.md) names
// the hash of the output, and a KindLayout entry, toolchain facts like the
// probe's, holds the output itself, verbatim, so Tools.Version and
// Tools.Identity are what a run gives. A replaced clang changes the stat,
// so the hint misses and the process runs again; the output then reaches
// every key through Identity. Nothing is memoized without a
// store (--no-cache), under MK_CACHE_VERIFY, or for a run that failed.
// The hint file is keyed by clang's own path, which no package root can
// be.
func versionLine(st *cache.Store, clang string, run func(string) (string, error)) (string, error) {
	if st == nil {
		return run(clang)
	}
	statAt := st.Now()        // before the stat: the racy rule's anchor
	fi, err := os.Stat(clang) // the file a symbolic link names: a link itself never hits
	if err != nil {
		return run(clang)
	}
	h := st.LoadHints(clang)
	const name = "clang --version"
	if key, ok := h.Lookup(name, fi); ok {
		if b, ok, _ := st.Get(cache.KindLayout, key); ok && source.HashBytes(versionDomain, b) == key {
			return string(b), nil
		}
	}
	out, err := run(clang)
	if err != nil {
		return out, err
	}
	key := source.HashBytes(versionDomain, []byte(out))
	if st.Put(cache.KindLayout, key, []byte(out)) == nil {
		h.Record(name, fi, statAt, key)
		_ = h.Save() // the cache is an optimization: the cost is a run
	}
	return out, nil
}

// joinTools waits for a started discovery.
func (s *Session) joinTools() {
	if s.disc.done != nil {
		<-s.disc.done
	}
}

// toolErr is the E0001 of a tool error: tool, command and stderr from a
// *toolchain.ToolError, else the error text.
func toolErr(err error, msg string) *diag.Diagnostic {
	return toolErrIn(err, "llvm", msg, "")
}

// toolErrIn is toolErr for a tool run inside the work directory work: the
// tool is the error's, else the one the caller ran (clang, lld,
// llvm-lib), and the work directory's random name is replaced by <work>
// in every fact, so it reaches no output.
func toolErrIn(err error, tool, msg, work string) *diag.Diagnostic {
	var te *toolchain.ToolError
	if errors.As(err, &te) {
		return llvmAlts(diag.Toolchain(te.Tool, scrubWork(te.Command, work), scrubWork(startErr(te), work), scrubWork(msg, work)))
	}
	var me *toolchain.MissingError
	if errors.As(err, &me) {
		return diag.Toolchain(tool, "", scrubWork(err.Error(), work), scrubWork(msg, work)).
			Alt(altInstallLLVM).Alt(altLLVMFlag)
	}
	return llvmAlts(diag.Toolchain(tool, "", scrubWork(err.Error(), work), scrubWork(msg, work)))
}

// startErr is the stderr fact of a tool error: the tool's stderr, or, for
// a tool that never started (an empty stderr), Go's start error, which
// names the cause ("The filename or extension is too long", a missing
// file): the synth link defect showed an empty stderr there.
func startErr(te *toolchain.ToolError) string {
	if te.Started() || te.Err == nil || strings.TrimSpace(te.Stderr) != "" {
		return te.Stderr
	}
	return "could not start: " + te.Err.Error()
}

// E0001's alternatives (its fix class is Alternatives). Each is an
// instruction with no path, host or version of this machine in it, so a
// snapshot of one (codes/E0001.json) is the same on every host.
const (
	altInstallLLVM = toolchain.InstallAdvice
	altLLVMFlag    = "pass --llvm=<dir> naming the bin directory of an LLVM 21 or newer"
	altRunCommand  = "run the command fact by hand to see the tool's whole output"
	altOtherLLVM   = "pass --llvm=<dir> to use another LLVM 21 or newer"
	altAddTools    = "add the missing tools to that LLVM's bin directory"
	altCloseExe    = "close the program that holds the executable open, then build again"
	altOtherOut    = "pass -o with another path"
	altCPUName     = "pass --cpu with a CPU name clang lists for the target (clang --target=<triple> --print-supported-cpus)"
	altNoCPU       = "leave out --cpu to build for the target's baseline CPU"
	altGiveBody    = "give the function a body"
	altNoCall      = "remove the calls that reach it"
)

// llvmAlts gives an E0001 of an LLVM tool its alternatives: with a command,
// rerun it by hand or use another LLVM; with none (the tool did not
// start), install one or name one.
func llvmAlts(d *diag.Diagnostic) *diag.Diagnostic {
	if f, ok := d.FactValue("command"); !ok || f.Str == "" {
		return d.Alt(altInstallLLVM).Alt(altLLVMFlag)
	}
	return d.Alt(altRunCommand).Alt(altOtherLLVM)
}

// scrubWork replaces the work directory's path, in its OS, slash and
// doubly escaped forms, by <work>.
func scrubWork(s, work string) string {
	if work == "" {
		return s
	}
	for _, w := range []string{strings.ReplaceAll(work, `\`, `\\`), work, filepath.ToSlash(work)} {
		if w != "" {
			s = strings.ReplaceAll(s, w, "<work>")
		}
	}
	return s
}

// native is world.native for one bin of the session: the linked
// temporary goes to place(tmp), or, with keyed set instead, to
// <store>/bin/<link key>/<keyed>.
func (s *Session) native(bin binTarget, place func(tmp string) (string, []*diag.Diagnostic), keyed string) (string, []*diag.Diagnostic) {
	return s.nativeStored(bin, place, keyed, nil)
}

func (s *Session) nativeStored(bin binTarget, place func(tmp string) (string, []*diag.Diagnostic), keyed string, stored *fastBinOut) (string, []*diag.Diagnostic) {
	if d := notBuilt(s.cfg.Target); d != nil {
		return "", []*diag.Diagnostic{d} // before any tool runs
	}
	tools, d := s.tools()
	if d != nil {
		return "", []*diag.Diagnostic{d}
	}
	cache := ""
	if !s.cfg.NoCache {
		cache = s.cacheDir()
	}
	s.w.display = s.displayNames(nil) // run-time reports name dependencies locally (sitedisplay.go)
	return s.w.native(nativeJob{tools: tools, name: bin.name, mainMod: bin.mod.path, pkg: s.binMods(bin.mod), timed: s.p.timed, pkgRoot: s.root.dir,
		place: place, keyed: keyed, cache: cache, gro: generalRegsOnly(s.cfg.Target, bin.cpu), script: bin.script, subsystem: bin.subsystem, stored: stored})
}

// nativeJob is one executable to build: the tools, the bin's name and
// root module, the package's own modules (for the export-extern roots), a
// timer, and where the linked temporary goes. With place and keyed both
// nil the build stops after the objects (a corpus unit built but not
// linked).
type nativeJob struct {
	tools   *toolchain.Tools
	name    string
	mainMod string
	pkg     []*modNode
	timed   func(string, func())
	place   func(tmp string) (string, []*diag.Diagnostic)
	// keyed, used instead of place, is the file name of the executable at
	// its content-addressed path, <store>/bin/<link key>/<keyed> (mk run, mk
	// test, a corpus unit's run). An executable already there has the same
	// bytes and is used as is, so a running one is never overwritten. The
	// bin directory is the store's Dir("bin"), so GC ages its executables
	// like any entry. With place set too (mk build with a store), place
	// still gets the executable: on a hit a copy of the stored one, on a
	// miss the link's output, of which a copy is then stored at the keyed
	// path.
	keyed string
	// cache is the build's cache directory, whose tmp holds the work
	// directory; "" (--no-cache) means the OS temporary directory.
	cache string
	// allFuncs adds every non-generic function of pkg as a root (a
	// corpus unit with no Main: its objects are still built).
	allFuncs bool
	// noBody, when set, receives the first no-body E0001's message instead
	// of the E0001s, and the build stops after the objects (a corpus unit).
	noBody *string
	// pkgRoot is the package root directory, where a release build looks
	// for default.mkprof; "" for a corpus unit.
	pkgRoot string
	// gro builds the bin general-regs-only (mono's E1108 and codegen's
	// target features), and script is its linker script ("" for none;
	// free.go).
	gro    bool
	script string
	// subsystem is the bin's pkg.Bin subsystem, passed to the
	// link: "windows" links /subsystem:windows, a program with no console.
	subsystem string
	// lib, when set, builds a library instead of an executable
	// (libout.go): never stored under <cache>/bin, and its packs
	// are native objects when it is static.
	lib *libJob
	// stored, when non-nil, receives the <cache>/bin paths for fastbuild.go.
	stored *fastBinOut
}

// native runs stages 8 to 10 for one bin: collection over the build roots, the
// no-body check, packs, codegen per pack and the support pack, clang per pack
// (at most Jobs at once), the import libraries, and the link into a temporary
// file that place moves to its final path. A work directory of this process
// holds every intermediate file, named by pack name, and is removed at the end.
func (w *world) native(j nativeJob) (string, []*diag.Diagnostic) {
	w.nativeObjects = j.lib != nil && j.lib.kind == toolchain.Static
	tools := j.tools
	triple := w.cfg.Target.LLVMTriple
	if miss := tools.Need(triple); len(miss) > 0 {
		return "", []*diag.Diagnostic{diag.Toolchain(miss[0], "", "", fmt.Sprintf("the LLVM in %s lacks %s, needed for %s",
			tools.Dir, strings.Join(miss, ", "), w.cfg.Target.Name)).Text("reason", "missing-tool").Alt(altAddTools).Alt(altLLVMFlag)}
	}
	ctx := context.Background()
	// The store first: the probe and an explicit --cpu are read through
	// it, and the build ID it computes is in every key, the
	// link key included.
	st := w.openStore(j.cache)
	facts, err := w.probe(ctx, tools, st, triple)
	if err != nil {
		return "", []*diag.Diagnostic{toolErr(err, "clang cannot describe target "+triple)}
	}
	cpu, d := w.buildCPU(ctx, tools, st, triple)
	if d != nil {
		return "", []*diag.Diagnostic{d}
	}
	parent, err := workParent(st)
	var work string
	if err == nil {
		work, err = os.MkdirTemp(parent, "mk-")
	}
	if err != nil {
		return "", []*diag.Diagnostic{diag.Toolchain("fs", "mkdir", err.Error(), "cannot make a work directory")}
	}
	defer os.RemoveAll(work)

	env := w.sem5.monoEnv(w)
	opt := w.monoOptions()
	// No pack key reads mono's content hash; the pre-emission keys hash each
	// pack's members in emitPacks' workers, over layouts by identity, and
	// only when a key is formed.
	opt.SkipContent = true
	if j.gro {
		opt.GeneralRegsOnly, opt.Bin = true, j.name
	}
	var insts *mono.Instances
	var ds []*diag.Diagnostic
	var ie *source.InternalError
	j.timed("instances", func() {
		// Decode cached non-std bodies reachable from this bin's modules in
		// parallel before mono.Collect runs single-threaded; otherwise edit
		// builds decode every unchanged module's bodies sequentially on one
		// goroutine.
		w.prefetchBinBodies(j)
		ie = source.Guard("instances of "+j.mainMod, source.Span{}, func() {
			insts, ds = mono.Collect(env, w.fromCRoots(w.buildRoots(j.mainMod, j.pkg, j.allFuncs, j.gro), j.lib != nil), opt)
		})
	})
	if ie != nil {
		return "", []*diag.Diagnostic{diag.FromInternal(ie)}
	}
	if len(ds) > 0 {
		return "", ds
	}
	if isFree(w.cfg.Target) {
		if fd := w.freeTaskUses(insts); len(fd) > 0 {
			return "", fd // never codegen's missing-runtime-entry ICE
		}
	}
	if nb := w.noBody(insts); len(nb) > 0 {
		if j.noBody == nil {
			return "", nb
		}
		// A corpus unit reaching a trusted bodiless prelude function is
		// built but not linked (RunSkipped).
		*j.noBody = nb[0].Message
		j.place, j.keyed = nil, ""
	}
	var packs []mono.Pack
	if ie := source.Guard("partition of "+j.mainMod, source.Span{}, func() {
		packs = w.coalescePacks(insts, mono.Partition(env, insts, opt))
	}); ie != nil {
		return "", []*diag.Diagnostic{diag.FromInternal(ie)}
	}
	unit := w.unitOf(insts, packs, j.pkg)
	// Config.Debug (--debug) becomes a codegen.Config field here, and a
	// compile and link job option.
	cfg := codegen.Config{Target: w.cfg.Target, Profile: w.cfg.Profile, CPU: cpu, DataLayout: facts.DataLayout,
		Triple: facts.Triple, RootModule: j.mainMod, GeneralRegsOnly: j.gro, Debug: w.cfg.Debug, Display: w.display, Library: j.lib != nil, LibName: libNameOf(j),
		TestNoOverflowChecks: w.cfg.TestHooks.NoOverflowChecks, TestNoBoundsChecks: w.cfg.TestHooks.NoBoundsChecks}
	cfg.FramePointers, cfg.Size, cfg.Cover = w.cfg.FramePointers, w.cfg.Size, w.cfg.Cover
	cfg.TrapForm = trapForm(w.cfg, j.lib != nil, j.gro)
	if id := w.mainEntry(j.mainMod); id != types.NoItem {
		cfg.Main = w.items.Get(id).Key
	}
	cfg.Frames = w.frameNames(j.mainMod, cfg.Main)
	if d := w.pgoConfig(&cfg, j.pkgRoot); d != nil {
		return "", []*diag.Diagnostic{d}
	}
	llDir := "" // --emit-ll (TestHooks.LLDir): this bin's dump
	if d := w.cfg.TestHooks.LLDir; d != "" {
		llDir = filepath.Join(d, j.name)
		if d := prepareLLDir(llDir); d != nil {
			return "", []*diag.Diagnostic{d}
		}
	}
	var ir packIR
	j.timed("codegen", func() { ir, ds = w.emitPacks(unit, cfg, work, llDir, tools, st) })
	if len(ds) > 0 {
		return "", ds
	}
	// The package's C import shim, c_sources and asm_sources (cimport.go)
	// follow the packs' objects, by their own content keys. They come
	// first here because their undefined names choose import libraries.
	cobjs, ckeys, cds := w.cObjects(ctx, tools, st, triple, w.cfg.Profile.Optimized(), work, cTargetFlags(w.cfg.Target, cpu, j.gro))
	if len(cds) > 0 {
		return "", cds
	}
	// The package graph's links (links.go): nil when none applies to the
	// target, so the link is exactly the one a package without links gets.
	plan, ds := w.linkPlanFor(insts, unit.Globals, cobjs)
	if len(ds) > 0 {
		return "", ds
	}
	// mono folded std.sys.LibcBuild() to opt.LibcMode, so the
	// entry the plan links must be that mode's: a static entry under a
	// true fold would call a null pthread_create.
	if libc := plan != nil && len(plan.sos) > 0; libc != opt.LibcMode {
		return "", []*diag.Diagnostic{diag.FromInternal(&source.InternalError{Where: "link plan of " + j.mainMod,
			What: fmt.Sprintf("driver: the link plan's system-libc mode is %v, mono folded LibcBuild to %v", libc, opt.LibcMode)})}
	}
	var specs []importLib
	if w.cfg.Target.OS == types.OSWindows {
		// Only the shim's undefined names reach the system DLLs: it is the
		// C import's own code (a static inline calling Win32). A C source
		// links against std.libc alone, so its other calls fail at link
		// (E0001); a named link may supply them.
		specs, ds = w.neededImportLibs(insts, cObjectUndefined(shimObjects(cobjs)), plan)
		if len(ds) == 0 {
			ds = w.externVarFaults(unit.Globals, cobjs, plan, specs)
		}
		if len(ds) > 0 {
			return "", ds
		}
	}
	// macOS: the libSystem stub the link reads (macho.go), and its key.
	var stub string
	var stubKey source.Hash
	if w.cfg.Target.OS == types.OSMacos {
		var d *diag.Diagnostic
		if stub, stubKey, d = w.macosStub(tools); d != nil {
			return "", []*diag.Diagnostic{d}
		}
	}
	linked := j.place != nil || j.keyed != ""
	job := w.linkJob(triple, filepath.Join(work, j.name+exeSuffix(w.cfg.Target)))
	job.Subsystem = j.subsystem
	if job.Format == toolchain.COFF {
		job.SectionAlign = codegen.StaticAlign(unit, cgEnv{w}, cfg) // over-page alignment on Windows
	}
	if isFree(w.cfg.Target) {
		// Freestanding: a static ELF entered at MkEntry, with the
		// bin's linker script.
		job.Free, job.Entry, job.Script = true, toolchain.FreeEntry, j.script
	}
	// Every pack's object key: the build ID, the toolchain,
	// the compile options and the pack's IR. They name the objects in the
	// store and, with the import libraries' keys and the link options,
	// the executable.
	okeys := ir.keys
	// Config.ExtraObjects follow the packs' objects, in the link and in
	// its key (by content, so an object rebuilt in place relinks).
	xkeys, d := extraObjectKeys(w.cfg.ExtraObjects)
	if d != nil {
		return "", []*diag.Diagnostic{d}
	}
	xkeys = append(xkeys, ckeys...)
	xkeys = append(xkeys, plan.keys()...)
	if stub != "" {
		xkeys = append(xkeys, stubKey)
	}
	var dest, storedPDB, key string
	// verifyHit is MK_CACHE_VERIFY over a stored link: the link runs, and
	// its output is compared with the stored copy.
	verifyHit := false
	// mk build with a store passes place and keyed both: a hit
	// copies the stored executable out, a miss links, places and stores
	// a copy. Without a store or a build ID it links and places, as
	// before, and writes nothing under bin.
	build := j.place != nil && j.keyed != ""
	if build && (st == nil || w.buildID().IsZero()) {
		build, j.keyed = false, ""
	}
	if j.keyed != "" {
		// The link key comes before clang, so a hit skips clang, the import
		// libraries and the link. It is the hash of the object keys, which
		// hold the build ID, of the import libraries' keys and of every link
		// option, the build ID again among them. The compile and link
		// command lines are code inside the mk binary (toolchain's
		// compileArgs and linkArgs), so the build ID is what changes when
		// they do; the literal argument vectors cannot be hashed, as they
		// hold the random work directory and, on COFF, the store path of an
		// import library that hit. Without a build ID (no store) there is no
		// hit: the executable is linked again.
		lkeys := append(append([]source.Hash(nil), okeys...), xkeys...)
		key = linkKey(lkeys, w.libKeys(tools, specs), nativeOptions(tools, w.buildID(), cfg, w.cfg.Profile, job))
		dir := w.binDir(st, j.cache)
		dest = filepath.Join(dir, key[:32], j.keyed)
		if pdb := pdbOf(job); pdb != "" {
			storedPDB = filepath.Join(filepath.Dir(dest), filepath.Base(pdb))
		}
		// A hit needs the executable and, for a --debug COFF link, its
		// PDB: GC ages the two files apart. Under MK_CACHE_VERIFY a
		// stored executable is compared even when its PDB went: the
		// miss path would keep it as it is.
		pdbStored := storedPDB == "" || isRegular(storedPDB)
		if isRegular(dest) && (pdbStored || w.cfg.CacheVerify) && !w.buildID().IsZero() {
			markExe(st, dest)
			if storedPDB != "" && pdbStored {
				markExe(st, storedPDB)
			}
			switch {
			case w.cfg.CacheVerify:
				// Every hit is recomputed and compared. The
				// link below runs, and verifyBin compares its output
				// with the stored copy.
				verifyHit = true
			case !build:
				if ds := plan.copyDLLs(dest, st); len(ds) > 0 {
					return "", ds
				}
				return dest, nil
			default:
				if copyOut(dest, job.Output) == nil && (storedPDB == "" || copyOut(storedPDB, pdbOf(job)) == nil) {
					w.packs.noteAll(len(okeys), true)
					if j.stored != nil {
						j.stored.storedExe, j.stored.storedPDB = dest, storedPDB
					}
					return placeLinked(j, job, plan)
				}
				// The stored file went (GC) or cannot be read: link.
				os.Remove(job.Output)
			}
		}
	}
	objs, libs, ds := w.compileAndImportLibs(ctx, tools, st, ir, cfg, specs, work, j.timed)
	if len(ds) > 0 {
		return "", ds
	}
	if !linked {
		return "", nil
	}
	if j.lib != nil {
		return w.finishLibrary(ctx, tools, st, j, job, objs, libs, cobjs, plan, work)
	}
	job.Objects, job.Libs = append(append(objs, w.cfg.ExtraObjects...), cobjs...), libs
	if stub != "" {
		tbd := filepath.Join(work, "libSystem.tbd")
		if err := os.WriteFile(tbd, []byte(stub), 0o644); err != nil {
			return "", []*diag.Diagnostic{diag.Toolchain("fs", "write", err.Error(), "cannot write the libSystem stub")}
		}
		job.Libs = []string{tbd}
	}
	if ds := plan.apply(ctx, tools, work, &job); len(ds) > 0 {
		return "", ds
	}
	if job.LTO {
		job.LTOCacheDir, job.LTOCacheSize = ltoDir(st)
	}
	if h := w.cfg.TestHooks.OnLink; h != nil {
		h(job)
	}
	var lerr error
	j.timed("link", func() { lerr = tools.Link(ctx, job) })
	if lerr != nil {
		return "", plan.linkErrWith(lerr, work)
	}
	w.packs.noteLink()
	pdb := pdbOf(job)
	if verifyHit {
		d, replaced := verifyBin(key, dest, storedPDB, job.Output, pdb, work)
		w.packs.verified(d)
		switch {
		case d == nil && j.place == nil:
			// The stored copy has the link's bytes: use it as is, as a
			// hit does (a running one is never overwritten). A PDB that
			// GC aged out goes back beside it; failing that costs only
			// a later miss.
			if storedPDB != "" && !isRegular(storedPDB) {
				placePDB(pdb, dest)
			}
			return dest, plan.copyDLLs(dest, st)
		case d != nil && j.place == nil:
			// mk run, mk test or a corpus unit's run: the stored path may
			// still hold the stale bytes (a running image could not be moved
			// aside, or another process stored them again), so the fresh
			// link runs from a private directory, never from the store, and
			// is stored only when the stale files went.
			j.place = privatePlacement(st, j.keyed)
			build = replaced
		case d != nil:
			// mk build: the fresh link is placed; it is stored only when
			// the stale files went (storeStaged keeps a file at dest).
			build = replaced
		}
		// Otherwise (mk build, the bytes agree) the output is placed and
		// whatever of the link GC aged out is stored, as on a miss.
	}
	if j.place != nil {
		// The store's copies are taken in the work directory before the
		// placement: the output path is the user's, and another process
		// may replace it before this one reads it back.
		stagedExe, stagedPDB := "", ""
		var exeHash, pdbHash source.Hash
		var exeSize, pdbSize int64
		if build {
			stagedExe = job.Output + ".store"
			var err error
			if exeHash, exeSize, err = copyOutHash(job.Output, stagedExe); err != nil {
				stagedExe = ""
			}
			if stagedExe != "" && pdb != "" {
				stagedPDB = pdb + ".store"
				if pdbHash, pdbSize, err = copyOutHash(pdb, stagedPDB); err != nil {
					stagedExe = ""
				}
			}
		}
		exe, ds := placeLinked(j, job, plan)
		if stagedExe != "" && exe != "" && len(ds) == 0 {
			// PDB first, so that the executable's presence implies the
			// PDB's; a failure costs the next build a link.
			if storedPDB == "" || storeStaged(st, stagedPDB, storedPDB) {
				if storeStaged(st, stagedExe, dest) && j.stored != nil {
					j.stored.storedExe, j.stored.exeSize, j.stored.exeHash = dest, exeSize, exeHash
					j.stored.storedPDB, j.stored.pdbSize, j.stored.pdbHash = storedPDB, pdbSize, pdbHash
				}
			}
		}
		return exe, ds
	}
	// The PDB first: a link-key hit (the executable present) then implies it.
	if pdb != "" {
		if ds := placePDB(pdb, dest); len(ds) > 0 {
			return "", ds
		}
	}
	exe, ds := placeExe(job.Output, dest)
	if len(ds) > 0 {
		// Another process placed the same link first (and may be
		// running it, so Windows refused the rename): its bytes are
		// these.
		if _, err := os.Stat(dest); err == nil {
			return dest, plan.copyDLLs(dest, st)
		}
		// GC removes a <link key> directory that its eviction left
		// empty; one that did so between placeExe's mkdir and its rename
		// costs one more try.
		exe, ds = placeExe(job.Output, dest)
	}
	if len(ds) == 0 {
		countExe(st, exe)
		ds = plan.copyDLLs(exe, st)
	}
	return exe, ds
}

// placeLinked moves the executable at job.Output, and its PDB, to where
// j.place puts it, and copies the link's DLLs beside it. place returns no
// path after an E0708 (walktail.go); then nothing else is placed.
func placeLinked(j nativeJob, job toolchain.LinkJob, plan *linkPlan) (string, []*diag.Diagnostic) {
	exe, ds := j.place(job.Output)
	if exe == "" || len(ds) > 0 {
		return exe, ds
	}
	if pdb := pdbOf(job); pdb != "" {
		if ds := placePDB(pdb, exe); len(ds) > 0 {
			return exe, ds
		}
	}
	return exe, plan.copyDLLs(exe, nil)
}

// copyOut copies the stored file src to dst in the work directory, with
// src's permissions. mk build never hands out the store's own file (no
// rename and no hard link): a later edit of the executable in place, a
// strip or a signature, must not reach the entry.
func copyOut(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
	}
	return err
}

// verifyBin is MK_CACHE_VERIFY's comparison of a stored link, the executable
// at stored and its PDB at storedPDB ("" for none; a PDB GC aged out is not
// compared), with the link just made at fresh and freshPDB: nil when the
// stored files have the link's bytes, else E0000 with where "cache verify"
// and what "bin <key>:" and the file that differs or cannot be read. On a
// difference the stored files are moved aside into aside, the build's work
// directory (on the store's volume, removed at the end, and by GC's tmp
// cleaning when a running image keeps it): a rename works on a running
// executable on Windows, where a remove does not. The executable goes first,
// so that its presence still implies the PDB's. replaced reports whether
// both paths are now free, so that the caller stores the fresh files as on a
// miss; when not, the E0000 says the stored copy was not replaced.
func verifyBin(key, stored, storedPDB, fresh, freshPDB, aside string) (d *diag.Diagnostic, replaced bool) {
	what := fileDiffers(stored, fresh, "executable")
	if what == "" && storedPDB != "" && isRegular(storedPDB) {
		what = fileDiffers(storedPDB, freshPDB, "PDB")
	}
	if what == "" {
		return nil, false
	}
	err := moveAside(stored, aside)
	if err == nil && storedPDB != "" {
		err = moveAside(storedPDB, aside)
	}
	if err != nil {
		what += "; the stored copy was not replaced: " + pathErrText(err)
	}
	return diag.FromInternal(&source.InternalError{Where: "cache verify",
		What: fmt.Sprintf("bin %s: %s", key, what)}), err == nil
}

// fileDiffers is "" when the stored file and the fresh one both read and
// hold the same bytes, else what differs: "the stored <name> differs from
// the one linked", or that one of them cannot be read.
func fileDiffers(stored, fresh, name string) string {
	x, err := os.ReadFile(stored)
	if err != nil {
		return "the stored " + name + " cannot be read: " + pathErrText(err)
	}
	y, err := os.ReadFile(fresh)
	if err != nil {
		return "the linked " + name + " cannot be read: " + pathErrText(err)
	}
	if !bytes.Equal(x, y) {
		return "the stored " + name + " differs from the one linked"
	}
	return ""
}

// moveAside renames the store's file p into dir, as stale-<name>; a file
// that is already gone is no error.
func moveAside(p, dir string) error {
	err := os.Rename(p, filepath.Join(dir, "stale-"+filepath.Base(p)))
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// privatePlacement is a placement into a fresh directory of its own under
// the store's tmp (os.TempDir without a store), as name: mk run's and the
// runners' copy of a fresh link after MK_CACHE_VERIFY found the stored one
// stale (verifyBin). GC's tmp cleaning removes the directory once it is
// older than its grace, so a running copy is never removed under the
// process that runs it.
func privatePlacement(st *cache.Store, name string) func(tmp string) (string, []*diag.Diagnostic) {
	return func(tmp string) (string, []*diag.Diagnostic) {
		parent, err := workParent(st)
		dir := ""
		if err == nil {
			dir, err = os.MkdirTemp(parent, "mk-run-")
		}
		if err != nil {
			return "", []*diag.Diagnostic{diag.Toolchain("fs", "mkdir", err.Error(), "cannot make a directory for the executable")}
		}
		return placeExe(tmp, filepath.Join(dir, name))
	}
}

// storeStaged moves staged, a copy taken in the work directory (under the
// store's tmp, so on the store's volume), to the store path dest under
// bin: a rename, so a reader sees all of it or nothing (moveFile copies
// through a temporary in dest's directory when the rename cannot be
// made). It counts it toward GC's estimate. A file already at dest
// (another process stored the same link, with the same bytes) is kept.
// It reports whether dest now holds the bytes.
func storeStaged(st *cache.Store, staged, dest string) bool {
	if isRegular(dest) {
		return true
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false
	}
	if err := moveFile(staged, dest); err != nil {
		return isRegular(dest)
	}
	countExe(st, dest)
	return true
}

// isRegular reports whether p is a regular file.
func isRegular(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// linkJob is the link of one executable to out, without its inputs: the
// format of the triple, COFF's stack reservation, and in release
// /opt:ref,icf or --gc-sections plus ThinLTO at -O3 over the bitcode packs
// with Jobs backend jobs. The ThinLTO cache directory is set by native from
// the store (ltoDir).
func (w *world) linkJob(triple, out string) toolchain.LinkJob {
	rel := w.cfg.Profile.Optimized()
	job := toolchain.LinkJob{Format: toolchain.FormatOf(triple), Output: out, Release: rel, LTO: rel, Jobs: w.cfg.Jobs, Debug: w.linkKeepsSymbols()}
	if rel {
		job.Tune = w.cfg.Tune // a build-time experiment; zero by default
	}
	// A Nutshell executable is a static ELF at a fixed address with
	// page-aligned segments and no RELRO (toolchain.NutshellLinkFlags).
	// The link key needs nothing more: its triple is in cfg.Triple.
	job.Nutshell = w.cfg.Target.OS == types.OSNutshell
	if job.Format == toolchain.COFF {
		job.StackReserve = stackReserve
	}
	if job.Format == toolchain.MachO {
		job.MachOArch = toolchain.MachOArch(triple)
	}
	return job
}

// cpu is the target-cpu of the build: --cpu, else the target baseline.
func (w *world) cpu() string {
	if w.cfg.CPU != "" {
		return w.cfg.CPU
	}
	if isFree(w.cfg.Target) {
		return freeCPU
	}
	if w.cfg.Target.Arch == types.ArchX64 {
		return "x86-64-v2"
	}
	return "generic"
}

// linkErr is E0001 for a failed link: the linker's stderr, and
// the first undefined symbol it names in the symbol fact.
func linkErr(err error, work string) *diag.Diagnostic {
	d := toolErrIn(err, "lld", "the link failed", work)
	var te *toolchain.ToolError
	if errors.As(err, &te) {
		if sym := undefinedSymbol(te.Stderr); sym != "" {
			d = toolErrIn(err, "lld", "the link failed: undefined symbol "+sym, work).Text("symbol", sym)
		}
	}
	return d
}

// undefinedSymbol finds "undefined symbol: X" in lld's output.
func undefinedSymbol(stderr string) string {
	const m = "undefined symbol: "
	i := strings.Index(stderr, m)
	if i < 0 {
		return ""
	}
	rest := stderr[i+len(m):]
	if j := strings.IndexAny(rest, "\r\n"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// firstLines cuts s to its first n lines.
func firstLines(s string, n int) string {
	lines := strings.SplitAfterN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.TrimRight(strings.Join(lines, ""), "\n")
}

// exeSuffix is ".exe" for a Windows target, else "".
func exeSuffix(t types.Target) string {
	if t.OS == types.OSWindows {
		return ".exe"
	}
	return ""
}

// packFile is the work-directory base name of pack i of n: p0000 and up,
// then "support" for the support pack (index n). Names come from the pack
// index, a function of the source tree, never from a counter.
func packFile(i, n int) string {
	if i == n {
		return "support"
	}
	return fmt.Sprintf("p%04d", i)
}

// isPackLL reports whether base is a file name emitPacks writes into an
// --emit-ll directory: support.ll or p<digits>.ll.
func isPackLL(base string) bool {
	stem, ok := strings.CutSuffix(base, ".ll")
	if !ok {
		return false
	}
	if stem == "support" {
		return true
	}
	if len(stem) < 5 || stem[0] != 'p' {
		return false
	}
	for _, c := range stem[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// prepareLLDir makes dir, one bin's --emit-ll directory, and
// removes the pack files an earlier build left there, so the directory
// holds exactly this build's packs; any other file is left alone. A
// failure is E0001 under fs.
func prepareLLDir(dir string) *diag.Diagnostic {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return diag.Toolchain("fs", "mkdir "+dir, pathErrText(err), "cannot make the --emit-ll directory "+dir)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return diag.Toolchain("fs", "read "+dir, pathErrText(err), "cannot read the --emit-ll directory "+dir)
	}
	for _, e := range ents {
		if e.Type().IsRegular() && isPackLL(e.Name()) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return diag.Toolchain("fs", "remove "+e.Name(), pathErrText(err), "cannot replace "+e.Name()+" in the --emit-ll directory "+dir)
			}
		}
	}
	return nil
}

// extraObjectKeys is the content hash of each of Config.ExtraObjects, in
// order (domain mk.driver.extraobj.v1, over the file's bytes, not its path: the
// object's place does not change the executable). A file that cannot be
// read is E0001 under fs.
func extraObjectKeys(paths []string) ([]source.Hash, *diag.Diagnostic) {
	keys := make([]source.Hash, len(paths))
	for i, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, diag.Toolchain("fs", "read "+filepath.Base(p), pathErrText(err), "cannot read the extra object "+filepath.Base(p))
		}
		h := source.NewHasher("mk.driver.extraobj.v1")
		h.Bytes(b)
		keys[i] = h.Sum()
	}
	return keys, nil
}

// pdbOf is the PDB a COFF --debug link wrote beside job.Output (toolchain's
// pdbLink: <output stem>.pdb, the base name the image records through
// /pdbaltpath:%_PDB%), or "" when the link writes none.
func pdbOf(job toolchain.LinkJob) string {
	if !job.Debug || job.Format != toolchain.COFF {
		return ""
	}
	return strings.TrimSuffix(job.Output, filepath.Ext(job.Output)) + ".pdb"
}

// placePDB moves the PDB pdb beside the executable exe under its own base
// name, the one the image records, where a debugger looks for it
// (debuginfo request 1). A failure is E0001 tool: fs, as placeExe's.
func placePDB(pdb, exe string) []*diag.Diagnostic {
	dest := filepath.Join(filepath.Dir(exe), filepath.Base(pdb))
	err := os.MkdirAll(filepath.Dir(dest), 0o755)
	if err == nil {
		err = moveFile(pdb, dest)
	}
	if err != nil {
		return []*diag.Diagnostic{diag.Toolchain("fs", "rename "+filepath.ToSlash(dest), pathErrText(err),
			"cannot write the debug info "+filepath.ToSlash(dest)).Alt(altCloseExe).Alt(altOtherOut)}
	}
	return nil
}
