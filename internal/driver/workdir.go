// Why: a native build writes work files, executables and toolchain probe
// results; where they go decides whether the cache's GC can clean up after a
// killed mk and whether a warm build starts clang at all.
//
// What: workParent and binDir (the directories), markExe and countExe,
// world.probe and buildCPU (cached clang probes and --cpu resolution),
// cpuFloor (the x86-64-v2 floor of hosted x86_64) and moveFile.
//
// How: work directories live under the store's tmp (the OS temporary
// directory with --no-cache), executables under the store's bin, and the
// probe and an explicit --cpu are cached in memory and in the store by
// (toolchain identity, triple). moveFile renames, or copies when the
// destination is on another volume.

package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mk/internal/cache"
	"mk/internal/diag"
	"mk/internal/iface"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// workParent is the directory a native build's work directory is made in:
// the store's tmp (cache.Store.TmpDir, beside the store's own writes in
// progress), whose GC removes a work directory that a crashed or killed mk
// left once its newest file is older than cache.Grace; or the OS temporary
// directory when the build has no store (--no-cache, or a store that did not
// open). Under the store, the linked temporary and its destination under bin
// share a volume, so placing it is a rename.
func workParent(st *cache.Store) (string, error) {
	if st == nil {
		return os.TempDir(), nil
	}
	d := st.TmpDir()
	return d, os.MkdirAll(d, 0o755)
}

// binDir is where the executables named by their link key go: the store's
// Dir(cache.BinDir), so they count toward the budget and GC ages them like
// every entry; with no store, the same path under the cache directory.
func (w *world) binDir(st *cache.Store, cacheDir string) string {
	if st != nil {
		if d, err := st.Dir(cache.BinDir); err == nil {
			return d
		}
	}
	return cache.DirPath(cacheDir, cache.BinDir)
}

// markExe records a use of the executable p found under bin, so GC keeps
// it as recently used (cache.Store.Mark); nothing without a store.
func markExe(st *cache.Store, p string) {
	if st != nil {
		st.Mark(p)
	}
}

// countExe counts the executable p just placed under bin toward the
// store's GC estimate (cache.Store.Account).
func countExe(st *cache.Store, p string) {
	if st == nil {
		return
	}
	if fi, err := os.Stat(p); err == nil {
		st.Account(fi.Size())
	}
}

// probeKey keys the probe cache: one clang probe per toolchain and triple
// in a world (toolchain.Probe's contract).
type probeKey struct {
	tools  source.Hash
	triple string
}

// probe is toolchain.Probe cached by (Tools.Identity, triple): for the
// world's lifetime in memory, so a corpus session that builds many units
// runs clang once per target, and across processes in the store under
// cache.KindLayout, so a warm native command starts no probe process at all.
// st is nil with --no-cache. The lock is held across the probe, so
// concurrent first callers wait for one run. A failed probe is not cached; a
// stored entry that does not decode is a miss.
func (w *world) probe(ctx context.Context, tools *toolchain.Tools, st *cache.Store, triple string) (toolchain.TargetFacts, error) {
	k := probeKey{tools.Identity(), triple}
	w.probeMu.Lock()
	defer w.probeMu.Unlock()
	if f, ok := w.probes[k]; ok {
		return f, nil
	}
	key := layoutKey(tools, triple)
	if st != nil {
		if b, ok, _ := st.Get(cache.KindLayout, key); ok {
			if f, ok := decodeFacts(b); ok {
				w.rememberProbe(k, f)
				return f, nil
			}
		}
	}
	f, err := tools.Probe(ctx, triple)
	if err != nil {
		return f, err
	}
	w.rememberProbe(k, f)
	w.probeRuns++
	if st != nil {
		_ = st.Put(cache.KindLayout, key, encodeFacts(f)) // the cache is an optimization
	}
	return f, nil
}

// rememberProbe keeps f for the world; probeMu is held.
func (w *world) rememberProbe(k probeKey, f toolchain.TargetFacts) {
	if w.probes == nil {
		w.probes = map[probeKey]toolchain.TargetFacts{}
	}
	w.probes[k] = f
}

// layoutKey is the store key of a probe: iface.LayoutKey over the
// toolchain identity (which covers clang's path, size and version) and the
// triple.
func layoutKey(tools *toolchain.Tools, triple string) source.Hash {
	return iface.LayoutKey(fmt.Sprintf("%x", tools.Identity()), triple)
}

// encodeFacts is a probe's store payload: the triple, a newline, the data
// layout. Neither contains a newline (each is one quoted IR string).
func encodeFacts(f toolchain.TargetFacts) []byte {
	return []byte(f.Triple + "\n" + f.DataLayout)
}

// decodeFacts reads encodeFacts' payload; anything else is a miss.
func decodeFacts(b []byte) (toolchain.TargetFacts, bool) {
	t, l, ok := strings.Cut(string(b), "\n")
	if !ok || t == "" || l == "" || strings.Contains(l, "\n") {
		return toolchain.TargetFacts{}, false
	}
	return toolchain.TargetFacts{Triple: t, DataLayout: l}, true
}

// buildCPU is the CPU the build compiles for (--cpu): the target baseline when
// --cpu is not given, which starts no process; else toolchain.ResolveCPU's
// explicit name, once per world. "native" becomes the host CPU's name, so
// "native" never enters a key, the IR or a clang command line. Normalize
// already refused "native" for a target of another architecture than the
// host's. A name clang rejects for the triple is one E0001 with the fact flag
// "--cpu" (exit 2), never an E0000 per pack. An explicit name clang accepted is
// remembered in the store under (toolchain identity, triple, name), so a warm
// build with --cpu starts no check process; "native" is resolved in every
// process, as it depends on the host.
func (w *world) buildCPU(ctx context.Context, tools *toolchain.Tools, st *cache.Store, triple string) (string, *diag.Diagnostic) {
	want := w.cfg.CPU
	if want == "" {
		return w.cpu(), nil
	}
	w.cpuMu.Lock()
	defer w.cpuMu.Unlock()
	if w.cpuName != "" {
		return w.cpuName, nil
	}
	var key source.Hash
	if want != toolchain.CPUNative {
		key = cpuKey(tools, triple, want)
		if st != nil {
			if b, ok, _ := st.Get(cache.KindLayout, key); ok && string(b) == want {
				w.cpuName = want
				return want, nil
			}
		}
	}
	w.cpuRuns++
	// When want != "native" on hosted x86_64, ResolveCPU and cpuFloor(want) are
	// independent clang invocations. Run cpuFloor concurrently so a valid --cpu
	// pays one clang startup instead of two; if ResolveCPU fails we still return
	// ResolveCPU's diagnostic.
	var floorCh chan *diag.Diagnostic
	if want != toolchain.CPUNative && w.cfg.Target.Arch == types.ArchX64 && !isFree(w.cfg.Target) {
		floorCh = make(chan *diag.Diagnostic, 1)
		var fd *diag.Diagnostic
		go func() {
			defer func() { floorCh <- fd }()
			if ie := source.Guard("cpu floor", source.Span{}, func() {
				fd = w.cpuFloor(ctx, tools, triple, want, want)
			}); ie != nil {
				fd = diag.FromInternal(ie)
			}
		}()
	}
	name, err := tools.ResolveCPU(ctx, triple, want)
	if err != nil {
		if floorCh != nil {
			<-floorCh
		}
		var te *toolchain.ToolError
		if errors.As(err, &te) && te.Started() && !te.Cancelled && te.Signal == "" {
			msg := fmt.Sprintf("clang does not accept the CPU %q for %s (--cpu=%s)", want, w.cfg.Target.Name, want)
			return "", diag.Toolchain(toolchain.NameClang, te.Command, te.Stderr, msg).Text("flag", "--cpu").
				Alt(altCPUName).Alt(altNoCPU)
		}
		return "", toolErr(err, fmt.Sprintf("cannot resolve --cpu=%s for %s", want, w.cfg.Target.Name)).Text("flag", "--cpu")
	}
	var floorDiag *diag.Diagnostic
	if floorCh != nil {
		floorDiag = <-floorCh
		if name != want {
			floorDiag = w.cpuFloor(ctx, tools, triple, want, name)
		}
	} else {
		floorDiag = w.cpuFloor(ctx, tools, triple, want, name)
	}
	if floorDiag != nil {
		return "", floorDiag
	}
	if st != nil && want != toolchain.CPUNative {
		_ = st.Put(cache.KindLayout, key, []byte(name))
	}
	w.cpuName = name
	return name, nil
}

// cc1StoreHooks returns optional KindLayout get/put callbacks for persisting
// probed `clang -cc1` argv templates across processes.
func cc1StoreHooks(st *cache.Store) (func(source.Hash) ([]byte, bool), func(source.Hash, []byte)) {
	if st == nil {
		return nil, nil
	}
	get := func(k source.Hash) ([]byte, bool) {
		b, ok, _ := st.Get(cache.KindLayout, k)
		return b, ok
	}
	put := func(k source.Hash, b []byte) {
		_ = st.Put(cache.KindLayout, k, b)
	}
	return get, put
}

// startNativePrep overlaps clang target-layout probing, explicit --cpu
// resolution, and -cc1 template probing with runCheck on mk build, once
// startTools finishes. All results are memoized so world.native reuses them
// without running extra driver probes on the critical path.
func (s *Session) startNativePrep(w *world) {
	if s.prepDone != nil {
		return
	}
	tools := s.cfg.Tools
	discDone := s.disc.done
	if tools == nil && discDone == nil {
		return
	}
	var st *cache.Store
	if !s.cfg.NoCache {
		st = w.openStore(s.cacheDir())
	}
	triple := s.cfg.Target.LLVMTriple
	hasCPU := w.cfg.CPU != ""
	done := make(chan struct{})
	s.prepDone = done
	go func() {
		defer close(done)
		_ = source.Guard("native prep", source.Span{}, func() {
			t := tools
			if t == nil {
				<-discDone
				if s.disc.err != nil {
					return
				}
				t = s.disc.t
			}
			if t == nil {
				return
			}
			ctx := context.Background()
			opt, lto := compileOpts(w.cfg.Profile, 0, 1)
			opt, lto = w.profileCompile(opt, lto)
			var tune toolchain.Tuning
			if lto {
				tune = w.cfg.Tune
			}
			warmTemplates := func(probedTriple, cpuName string) {
				var twg sync.WaitGroup
				twg.Add(1)
				go func() {
					defer twg.Done()
					_ = source.Guard("warm cc1 pack", source.Span{}, func() {
						t.WarmCC1(ctx, toolchain.CompileJob{Triple: probedTriple, Opt: opt, LTO: lto, CPU: cpuName, Tune: tune})
					})
				}()
				if lto && toolchain.FormatOf(probedTriple) == toolchain.COFF && !w.cfg.Tune.FatLTO {
					twg.Add(1)
					go func() {
						defer twg.Done()
						_ = source.Guard("warm cc1 thin", source.Span{}, func() {
							t.WarmThinCC1(ctx, w.cfg.Tune)
						})
					}()
				}
				twg.Wait()
			}
			var probedTriple string
			var cpuName string
			var probeErr error
			var cpuDiag *diag.Diagnostic
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = source.Guard("probe", source.Span{}, func() {
					var f toolchain.TargetFacts
					f, probeErr = w.probe(ctx, t, st, triple)
					probedTriple = f.Triple
				})
			}()
			if hasCPU {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_ = source.Guard("build cpu", source.Span{}, func() {
						cpuName, cpuDiag = w.buildCPU(ctx, t, st, triple)
					})
				}()
			} else {
				cpuName = w.cpu()
			}
			wg.Wait()
			if probeErr == nil && cpuDiag == nil && probedTriple != "" {
				warmTemplates(probedTriple, cpuName)
			}
		})
	}()
}

// joinNativePrep waits for startNativePrep if one was launched.
func (s *Session) joinNativePrep() {
	if s.prepDone != nil {
		<-s.prepDone
		s.prepDone = nil
	}
}

// cpuKey is the store key of an explicit --cpu name clang accepted:
// (toolchain identity, triple, name), beside the layouts it belongs with.
func cpuKey(tools *toolchain.Tools, triple, cpu string) source.Hash {
	h := source.NewHasher("mk.cpu.v2") // v2: a stored name has met cpuFloor
	h.Hash(tools.Identity())
	h.String(triple)
	h.String(cpu)
	return h.Sum()
}

// moveFile renames src to dest. When the rename fails (dest is on another
// volume than the work directory: "cannot move the file to a different
// disk drive" on Windows, EXDEV on Linux), src is copied, with its mode, to
// a temporary file in dest's directory, which is then renamed onto dest, so
// dest is never seen half written. The error is that last rename's when the
// copy succeeded (Windows refuses to replace a running executable), else
// the copy's.
func moveFile(src, dest string) error {
	if os.Rename(src, dest) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".mk-place-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = io.Copy(tmp, in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, st.Mode().Perm())
	}
	if err == nil {
		err = os.Rename(name, dest)
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}

// v2Macros are the predefined macros clang sets for the features of
// x86-64-v2, the hosted x86_64 baseline, by the feature each names.
var v2Macros = []struct{ macro, feature string }{
	{"__SSE3__", "sse3"}, {"__SSSE3__", "ssse3"}, {"__SSE4_1__", "sse4.1"}, {"__SSE4_2__", "sse4.2"},
	{"__POPCNT__", "popcnt"}, {"__GCC_HAVE_SYNC_COMPARE_AND_SWAP_16", "cx16"}, {"__LAHF_SAHF__", "sahf"},
}

// cpuFloor keeps --cpu from going below the baseline: on a hosted x86_64 target
// a CPU without every feature of x86-64-v2 is one E0001 with the fact flag
// "--cpu", as a name clang rejects is; freestanding takes any x86-64 CPU. The
// features are read from the macros clang predefines for -march=<name> (clang
// -dM -E of an empty C file, which compiles nothing); nil when the CPU meets
// the floor.
func (w *world) cpuFloor(ctx context.Context, tools *toolchain.Tools, triple, want, name string) *diag.Diagnostic {
	if w.cfg.Target.Arch != types.ArchX64 || isFree(w.cfg.Target) {
		return nil
	}
	dir, err := os.MkdirTemp("", "mk-cpu-floor-")
	if err != nil {
		return toolErr(err, "cannot check --cpu="+want)
	}
	defer os.RemoveAll(dir)
	empty := filepath.Join(dir, "empty.c")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		return toolErr(err, "cannot check --cpu="+want)
	}
	args := []string{"--target=" + triple, "-march=" + name, "-dM", "-E", "-x", "c", empty}
	out, _, err := tools.RunClang(ctx, args, dir)
	if err != nil {
		return toolErr(err, fmt.Sprintf("cannot read the features of --cpu=%s for %s", want, w.cfg.Target.Name)).Text("flag", "--cpu")
	}
	defined := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "#define" {
			defined[f[1]] = true
		}
	}
	var missing []string
	for _, m := range v2Macros {
		if !defined[m.macro] {
			missing = append(missing, m.feature)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	cmd := strings.ReplaceAll("clang "+strings.Join(args, " "), dir, "<work>")
	msg := fmt.Sprintf("--cpu=%s is below %s's baseline x86-64-v2: it lacks %s", want, w.cfg.Target.Name, strings.Join(missing, ", "))
	return diag.Toolchain(toolchain.NameClang, cmd, "", msg).Text("flag", "--cpu").Alt(altCPUName).Alt(altNoCPU)
}
