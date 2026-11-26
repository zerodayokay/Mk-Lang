// Why: the native step's per-pack work (code generation, clang, import
// libraries) is where a build spends its time, so each part must run in
// parallel and skip what the cache already holds.
//
// What: emitPacks, compileOpts and compilePacks (codegen and clang per
// pack), stagedHits and copyNewFile (how a stored object reaches the link),
// importLib, neededImportLibs, windowsLibs and writeImportLibs (the Windows
// import libraries and the lazy-binding object), and linkKey with nativeOptions (the key of the
// content-addressed executable).
//
// How: packs run at most Jobs at a time with results in slots, so every
// list is in pack order. A pack whose pre-emission key or object key hits
// is neither generated nor compiled; a stored object is linked in place,
// except where lld's in-process LTO makes the object paths matter.

package driver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"mk/internal/cache"
	"mk/internal/codegen"
	"mk/internal/diag"
	"mk/internal/mono"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// packIR is emitPacks' result: per pack, then the support pack, the .ll
// path ("" when the object key hit, so no .ll was written), the .ll byte
// size (for LPT scheduling in compilePacks), the object key and, on a hit,
// the stored object.
type packIR struct {
	lls   []string
	sizes []int
	keys  []source.Hash
	hits  []string
}

// emitPacks generates each pack and the support pack in parallel, at most
// Jobs at a time; results in slots, so the lists are in pack order, then the
// support pack. Each pack's object key (objKey: the build ID, the toolchain,
// the compile options and the IR) is computed from the IR in memory, and a
// pack whose object is in st writes no .ll: writing and reading back every
// pack's IR is a file write and read per pack, which dominates a
// one-function edit of a large package. Under MK_CACHE_VERIFY every .ll is
// written, since every pack is compiled. Before that, a pack whose
// pre-emission key (prekey.go) maps to a stored object is not generated: its
// object key is the memo's. With llDir (mk build --emit-ll, made by
// prepareLLDir) or OnLL no pre-emission key is formed, so every pack's IR is
// generated and written there, hit or miss, as the bytes OnLL sees; that
// copy is in no key. A codegen ICE is E0000.
func (w *world) emitPacks(u *codegen.Unit, cfg codegen.Config, work, llDir string, tools *toolchain.Tools, st *cache.Store) (packIR, []*diag.Diagnostic) {
	n := len(u.Packs)
	out := packIR{lls: make([]string, n+1), sizes: make([]int, n+1), keys: make([]source.Hash, n+1), hits: make([]string, n+1)}
	errs := make([]*diag.Diagnostic, n+1)
	env := cgEnv{w}
	pks := w.prekeysFor(u, tools, st, llDir) // nil emits every pack
	faults := w.parallel(n+1, func(i int) string { return "codegen " + packFile(i, n) }, func(i int) {
		name := packFile(i, n)
		// A pack whose pre-emission key maps to a stored object is not
		// generated; its object key is the memo's, the key its IR had. The
		// support pack reads the whole unit and is always generated. Under
		// MK_CACHE_VERIFY every pack is generated and the memo is checked
		// against its IR below.
		var pre source.Hash
		hasPre := false
		if pks != nil && i < n {
			pre, hasPre = pks.key(u, i, cfg, w.packJob(cfg, "", "", i, n))
		}
		lookup := !w.cfg.CacheVerify && !w.cfg.TestHooks.EmitMissObjects
		if hasPre && lookup {
			if obj, hit := memoGet(st, pre); hit {
				if p, found, _ := st.FilePath(cache.KindObj, obj); found {
					out.keys[i], out.hits[i] = obj, p
					w.packEvent(name, firstKey(u, i), pre, true, obj, false)
					return
				}
			}
		}
		var b bytes.Buffer
		var err error
		if i == n {
			err = codegen.EmitSupport(u, env, cfg, &b)
		} else {
			var rep codegen.Report
			rep, err = codegen.EmitPack(u, i, env, cfg, &b)
			w.packs.noteStale(rep.Stale)
		}
		if err != nil {
			var cie *codegen.InternalError
			if errors.As(err, &cie) {
				errs[i] = diag.FromInternal(cie.IE)
			} else {
				errs[i] = diag.FromInternal(&source.InternalError{Where: "codegen " + name, What: err.Error()})
			}
			return
		}
		if h := w.cfg.TestHooks.OnLL; h != nil {
			h(name, b.String())
		}
		if llDir != "" {
			if err := os.WriteFile(filepath.Join(llDir, name+".ll"), b.Bytes(), 0o644); err != nil {
				errs[i] = diag.Toolchain("fs", "write "+name+".ll", pathErrText(err), "cannot write "+name+".ll into the --emit-ll directory "+llDir)
				return
			}
		}
		ll := filepath.Join(work, name+".ll")
		out.keys[i] = objKey(w.buildID(), tools, w.packJob(cfg, ll, "", i, n), b.Bytes())
		if hasPre {
			if w.cfg.CacheVerify {
				w.packs.verified(memoVerify(st, pre, out.keys[i]))
			}
			if old, ok := memoGet(st, pre); !ok || old != out.keys[i] {
				memoPut(st, pre, out.keys[i])
			}
		}
		w.packEvent(name, firstKey(u, i), pre, hasPre, out.keys[i], true)
		if st != nil && lookup {
			if p, ok, _ := st.FilePath(cache.KindObj, out.keys[i]); ok {
				out.hits[i] = p
				return
			}
		}
		if err := os.WriteFile(ll, b.Bytes(), 0o644); err != nil {
			errs[i] = diag.Toolchain("fs", "write "+name+".ll", pathErrText(err), "cannot write "+name+".ll")
			return
		}
		out.lls[i] = ll
		out.sizes[i] = b.Len()
	})
	return out, append(collect(errs), faults...)
}

// compileOpts is the clang optimization of pack i of n: dev -O0, release and
// checked (Profile.Optimized) -O3 -flto=thin (bitcode objects). The support
// pack (index n) is native code at -O2 in every profile; O3 is the nearer
// level toolchain has.
func compileOpts(prof types.Profile, i, n int) (toolchain.OptLevel, bool) {
	switch {
	case i == n:
		return toolchain.O3, false
	case prof.Optimized():
		return toolchain.O3, true
	}
	return toolchain.O0, false
}

// packJob is the compile job of pack i of n (n is the support pack):
// the triple and CPU of cfg and the profile's -O level and -flto.
func (w *world) packJob(cfg codegen.Config, in, out string, i, n int) toolchain.CompileJob {
	job := toolchain.CompileJob{Triple: cfg.Triple, Input: in, Output: out, CPU: cfg.CPU}
	job.Opt, job.LTO = compileOpts(w.cfg.Profile, i, n)
	job.Opt, job.LTO = w.profileCompile(job.Opt, job.LTO) // profile.go: --profile size
	if w.nativeObjects {
		job.LTO = false // a static library holds native objects (libout.go)
	}
	if job.LTO {
		// A build-time experiment: toolchain's compileArgs and
		// CompileKey decide what of it reaches a pack compile (fatlto,
		// and an unroll=<N> threshold); the support pack (no LTO) never
		// takes one.
		job.Tune = w.cfg.Tune
	}
	if i == n {
		// The support pack is a single compile with its own -O3 non-LTO options;
		// probing clang -### for one file costs more than invoking the driver.
		job.DirectDriver = true
	}
	return job
}

// compilePacks compiles each .ll of lls with one clang process, at most Jobs
// at a time; the objects are in the order of lls. With a store, a pack whose
// object key (objKey) hits is not compiled: the linker reads the stored
// object in place, except for a link that runs LTO in lld's own process
// (stagedHits); a compiled object enters the store. clang failing on IR the
// compiler wrote is E0000, with clang's stderr in what; a clang that cannot
// start is E0001; a job that toolchain refuses before starting clang is the
// driver's bug, E0000.
func (w *world) compilePacks(ctx context.Context, tools *toolchain.Tools, st *cache.Store, ir packIR, cfg codegen.Config, work string) ([]string, []*diag.Diagnostic) {
	n := len(ir.lls) - 1
	objs := make([]string, n+1)
	errs := make([]*diag.Diagnostic, n+1)
	stage := w.stagedHits(cfg.Triple)
	place := linkOrCopyFile
	if h := w.cfg.TestHooks.PlaceObject; h != nil {
		place = h
	}
	// unplaced is each pack whose stored object could not be placed:
	// they share one cause (the store emptied under the build, `mk cache
	// clean` in another process), so one diagnostic reports them, the
	// lowest pack's (one program once gave 75 E0001s for one cause).
	unplaced := make([]error, n+1)
	// hitAt is the path the link reads a stored object by: the store's
	// own, or under stage the work path a compiled one has. False when
	// it could not be placed there (unplaced).
	hitAt := func(i int, name, stored string) bool {
		objs[i] = stored
		if stage {
			dst := filepath.Join(work, name+".o")
			if err := place(stored, dst); err != nil {
				unplaced[i] = err
				return false
			}
			objs[i] = dst
		}
		w.packs.note(name, true)
		return true
	}
	// Schedule packs largest .ll first (LPT) so giant packs like main start at
	// t=0 instead of forming a single-core tail. Results still write to slot i,
	// so object order and outputs are identical.
	order := make([]int, n+1)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		sa, sb := ir.sizes[ia], ir.sizes[ib]
		if sa != sb {
			return sa > sb
		}
		return ia < ib
	})
	faults := w.parallel(n+1, func(k int) string { return "clang " + packFile(order[k], n) }, func(k int) {
		i := order[k]
		name := packFile(i, n)
		obj := filepath.Join(work, name+".o")
		if ir.hits[i] != "" {
			// emitPacks found the object and wrote no .ll.
			hitAt(i, name, ir.hits[i])
			return
		}
		job := w.packJob(cfg, ir.lls[i], obj, i, n)
		key := ir.keys[i]
		hit := "" // the stored object, when the key hit under MK_CACHE_VERIFY
		if st != nil {
			if p, ok, _ := st.FilePath(cache.KindObj, key); ok {
				if !w.cfg.CacheVerify {
					if hitAt(i, name, p) {
						return
					}
					if _, err := os.Lstat(obj); err == nil {
						return // a file there is never written through (copyNewFile)
					}
					// Its .ll is here: compile it, as a miss is.
					unplaced[i] = nil
					objs[i] = ""
				} else {
					hit = p
				}
			}
		}
		err := tools.Compile(ctx, job)
		var te *toolchain.ToolError
		switch {
		case err == nil && hit != "":
			// MK_CACHE_VERIFY: the hit was recompiled; the recomputed object
			// is used whatever the comparison says.
			objs[i] = job.Output
			w.packs.note(name, true)
			w.packs.verified(verifyFile(st, cache.KindObj, key, hit, job.Output))
		case err == nil:
			objs[i] = job.Output
			w.packs.note(name, false)
			if st != nil {
				objs[i] = storeFile(st, cache.KindObj, key, job.Output)
			}
		case errors.As(err, &te) && te.Started():
			errs[i] = diag.FromInternal(&source.InternalError{Where: "clang " + name,
				What: "clang rejected the IR the compiler wrote: " + firstLines(scrubWork(te.Stderr, work), 20)})
		case te != nil:
			errs[i] = toolErrIn(err, "clang", "clang could not run", work)
		default:
			errs[i] = diag.FromInternal(&source.InternalError{Where: "clang " + name,
				What: "toolchain refused the compile job: " + scrubWork(err.Error(), work)})
		}
	})
	if d := unplacedDiag(unplaced, n); d != nil {
		errs = append(errs, d)
	}
	return objs, append(collect(errs), faults...)
}

// unplacedDiag is the one E0001 for the packs whose stored object could not
// be placed in the work directory (compilePacks), the lowest pack's, with
// how many more there are; nil for none.
func unplacedDiag(unplaced []error, n int) *diag.Diagnostic {
	first, more := -1, 0
	for i, err := range unplaced {
		switch {
		case err == nil:
		case first < 0:
			first = i
		default:
			more++
		}
	}
	if first < 0 {
		return nil
	}
	name := packFile(first, n)
	msg := "cannot place the stored object of " + name + " in the work directory"
	if more > 0 {
		msg += fmt.Sprintf(", nor those of %d more packs", more)
	}
	return diag.Toolchain("fs", "link "+name+".o", pathErrText(unplaced[first]), msg)
}

// stagedHits reports whether a stored pack object is linked under the work
// path a compiled one has (<work>/<pack>.o, a hard link or a copy) rather
// than in place: for every release link whose LTO runs inside lld, that
// is every ELF one (hosted, Interp and freestanding) and a COFF one under
// the fatlto experiment. ld.lld's in-process ThinLTO output depends on
// the ascending order of its object paths against the link order: store
// names, or cold names in reverse, gave output different from --no-cache,
// while names that sort in link order gave the same output in every layout
// tried. Store paths are hashes, so a link that read them could differ
// from --no-cache wherever lld's ThinLTO cache did not hold the backends'
// output: in a warm build whose objects all hit, with that cache dropped
// or pruned, and in a later bin of a multi-bin build that links packs an
// earlier bin stored, cold builds included. The cold names share one
// directory and sort in link order, so a link line names its objects as
// a --no-cache one does, and only the random work directory differs, as
// it already did between two cold builds. A default COFF release link
// stages its bitcode in thinLink (m0000.o, ...), and a dev link has no
// LTO, so both read a hit at its store path. The same staging also keeps
// a warm release ELF bin with some packs hit and some compiled equal to
// a cold one (TestReleaseWarmMixEqualsCold), and the link
// keeps the bytes if GC removes the entry during the build.
func (w *world) stagedHits(triple string) bool {
	if !w.cfg.Profile.Optimized() {
		return false
	}
	f := toolchain.FormatOf(triple)
	return f == toolchain.ELF || f == toolchain.MachO || w.cfg.Tune.FatLTO
}

// linkOrCopyFile makes dst a hard link to src, or a copy where a link is
// not possible (another volume, or NTFS's 1024 names of one file).
func linkOrCopyFile(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyNewFile(src, dst)
}

// copyNewFile copies src to dst, which it creates (O_CREATE|O_EXCL, as the
// store's own copy does): a dst that already exists, a hard link to a
// stored object above all, is an error and is never written through
// (a shared stored object must never change). A partial copy is removed.
func copyNewFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
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

// collect is the non-nil diagnostics of slots, in slot order.
func collect(slots []*diag.Diagnostic) []*diag.Diagnostic {
	var ds []*diag.Diagnostic
	for _, d := range slots {
		if d != nil {
			ds = append(ds, d)
		}
	}
	return ds
}

// importLib is one Windows import library a link needs: the DLL's base
// name and the exported names of its std/sys/windows/<dll>.def. file is a
// named link's exact DLL file name, "" for <dll>.dll.
type importLib struct {
	dll   string
	file  string
	names []string
}

// fileName is the DLL file name the import library names.
func (l importLib) fileName() string {
	if l.file != "" {
		return l.file
	}
	return l.dll + ".dll"
}

// neededImportLibs lists the Windows import libraries the executable needs
// in .def file name order: kernel32 from std/sys/windows/kernel32.def
// always, and another DLL's (ws2_32) only when an extern instance names one
// of its symbols, so an executable imports only kernel32.dll unless it uses
// that DLL. The name lists are read from the std tree this build loaded. A
// name no .def list holds, of an extern instance or undefined in a C object
// (cUndef), is looked up in the system DLLs (syslibs.go): C imports call
// Win32 through those.
//
// With a plan (links.go), the named DLLs come between std's lists and the
// system set. A name that binds lazily (lazylink.go) gets no import
// library: it is returned in the lazy uses, for the lazy-binding object,
// and a lazy link's .dll joins the plan's copies.
func (w *world) neededImportLibs(in *mono.Instances, cUndef []string, plan *linkPlan) ([]importLib, lazyLink, []*diag.Diagnostic) {
	t := w.stdTree()
	ents, err := fs.ReadDir(t.fsys, "sys/windows")
	if err != nil {
		return nil, lazyLink{}, []*diag.Diagnostic{diag.Toolchain("fs", "list std/sys/windows", err.Error(), "cannot list std/sys/windows")}
	}
	used := map[string]bool{} // extern symbols; lookup only
	for i := range in.List {
		if fd, ok := w.items.Get(in.List[i].Item).Decl.(*types.FuncDecl); ok && fd.Sig.Extern && in.List[i].Body == nil {
			used[in.List[i].Symbol] = true
		}
	}
	var lazy lazyLink
	cUndef, lazy = w.lazyBinds().split(used, cUndef, plan) // lazylink.go
	var libs []importLib
	for _, e := range ents {
		if e.IsDir() || path.Ext(e.Name()) != ".def" || strings.HasSuffix(e.Name(), lazyStdSuffix) {
			continue
		}
		dll := strings.TrimSuffix(e.Name(), ".def")
		raw, err := t.readFile("sys/windows/" + e.Name())
		if err != nil {
			return nil, lazyLink{}, []*diag.Diagnostic{diag.Toolchain("fs", "read std/sys/windows/"+e.Name(), err.Error(), "cannot read "+e.Name())}
		}
		names, err := toolchain.ParseDefList(string(raw))
		if err != nil {
			return nil, lazyLink{}, []*diag.Diagnostic{diag.Toolchain("llvm-lib", "", err.Error(), "std/sys/windows/"+e.Name()+" is not a name list")}
		}
		needed := dll == "kernel32"
		for _, n := range names {
			needed = needed || used[n]
		}
		if needed {
			libs = append(libs, importLib{dll: dll, names: names})
		}
	}
	all := append([]string(nil), cUndef...)
	for n := range used {
		all = append(all, n)
	}
	sort.Strings(all)
	if !plan.empty() {
		libs, all = plan.namedImportLibs(libs, all)
	}
	return addSystemLibs(libs, all), lazy, nil
}

// windowsLibs is a Windows bin's import libraries (neededImportLibs), with
// its extern var faults, and its lazy-binding object and that object's key
// (lazylink.go; none when no name binds lazily). Only the shim's undefined
// names reach the system DLLs: it is the C import's own code (a static
// inline calling Win32). A C source links against std.libc alone, so its
// other calls fail at link (E0001); a named link may supply them.
func (w *world) windowsLibs(ctx context.Context, tools *toolchain.Tools, st *cache.Store, cfg codegen.Config, work string, insts *mono.Instances,
	globals []codegen.Global, cobjs []string, plan *linkPlan) ([]importLib, []string, []source.Hash, []*diag.Diagnostic) {
	specs, lazy, ds := w.neededImportLibs(insts, cObjectUndefined(shimObjects(cobjs)), plan)
	if len(ds) == 0 {
		ds = w.externVarFaults(globals, cobjs, plan, specs)
	}
	if len(ds) == 0 {
		ds = lazy.faults()
	}
	if len(ds) > 0 || lazy.empty() {
		return specs, nil, nil, ds
	}
	obj, key, ds := w.lazyObject(ctx, tools, st, cfg, work, lazy)
	if len(ds) > 0 {
		return nil, nil, nil, ds
	}
	return specs, []string{obj}, []source.Hash{key}, nil
}

// compileAndImportLibs runs compilePacks (under j.timed("clang", ...)) while
// overlapping writeImportLibs on a guarded goroutine when specs is non-empty,
// hiding llvm-lib latency behind clang on cold caches.
func (w *world) compileAndImportLibs(ctx context.Context, tools *toolchain.Tools, st *cache.Store, ir packIR, cfg codegen.Config, specs []importLib, work string, timed func(string, func())) ([]string, []string, []*diag.Diagnostic) {
	var libs []string
	var lds []*diag.Diagnostic
	var libDone chan struct{}
	if len(specs) > 0 {
		libDone = make(chan struct{})
		go func() {
			defer close(libDone)
			if ie := source.Guard("import libs", source.Span{}, func() {
				libs, lds = w.writeImportLibs(ctx, tools, st, specs, work)
			}); ie != nil {
				lds = append(lds, diag.FromInternal(ie))
			}
		}()
	}
	var objs []string
	var ds []*diag.Diagnostic
	timed("clang", func() { objs, ds = w.compilePacks(ctx, tools, st, ir, cfg, work) })
	if libDone != nil {
		<-libDone
	}
	if len(ds) > 0 {
		return nil, nil, ds
	}
	if len(lds) > 0 {
		return nil, nil, lds
	}
	return objs, libs, nil
}

// writeImportLibs writes each library of specs into work with llvm-lib and
// returns their paths in the same order. With a store, a library whose key
// (libKey) hits is read from the store, and a new one enters it.
func (w *world) writeImportLibs(ctx context.Context, tools *toolchain.Tools, st *cache.Store, specs []importLib, work string) ([]string, []*diag.Diagnostic) {
	var libs []string
	for _, l := range specs {
		var key source.Hash
		hit := ""
		if st != nil {
			key = libKey(tools, w.cfg.Target.Name, l)
			if p, ok, _ := st.FilePath(cache.KindLib, key); ok {
				if !w.cfg.CacheVerify {
					libs = append(libs, p)
					continue
				}
				hit = p
			}
		}
		out := filepath.Join(work, l.dll+".lib")
		if err := tools.ImportLib(ctx, l.fileName(), l.names, "x64", out); err != nil {
			return nil, []*diag.Diagnostic{toolErrIn(err, "llvm-lib", "cannot make the import library "+l.dll+".lib", work)}
		}
		if hit != "" {
			w.packs.verified(verifyFile(st, cache.KindLib, key, hit, out))
		} else if st != nil {
			out = storeFile(st, cache.KindLib, key, out)
		}
		libs = append(libs, out)
	}
	return libs, nil
}

// linkKey is the key naming the content-addressed executable of mk run, mk
// test and a corpus unit's run: the hash of the object keys in pack order
// (pack order is the sorted pack index, and the linker reads the objects in
// it), of the import libraries' keys in .def order, and of opts. An object
// key holds the build ID, so a new mk binary links again.
func linkKey(objs, libs []source.Hash, opts string) string {
	h := sha256.New()
	fmt.Fprintf(h, "objs %d\n", len(objs))
	for _, k := range objs {
		h.Write(k[:])
	}
	fmt.Fprintf(h, "libs %d\n", len(libs))
	for _, k := range libs {
		h.Write(k[:])
	}
	h.Write([]byte(opts))
	return hex.EncodeToString(h.Sum(nil))
}

// libKeys is libKey of every import library of specs, in order.
func (w *world) libKeys(tools *toolchain.Tools, specs []importLib) []source.Hash {
	keys := make([]source.Hash, len(specs))
	for i, l := range specs {
		keys[i] = libKey(tools, w.cfg.Target.Name, l)
	}
	return keys
}

// nativeOptions is everything besides the objects and the import
// libraries that decides the executable's bytes: the build ID (the
// compile and link command lines are code in the mk binary), the toolchain
// (its identity covers every tool's path, size and version), the triple
// and CPU clang compiles for, the profile (which fixes each pack's -O
// level and -flto), and the link's format, entry, /opt:ref or
// --gc-sections, ThinLTO and stack reservation. Jobs is not in it: it
// changes no byte.
func nativeOptions(tools *toolchain.Tools, build source.Hash, cfg codegen.Config, prof types.Profile, job toolchain.LinkJob) string {
	// v4: Debug, and the freestanding link's Free and its linker script's
	// bytes.
	script := ""
	if job.Script != "" {
		h := source.NewHasher("mk.driver.linkscript.v1")
		if b, err := os.ReadFile(job.Script); err == nil {
			h.Bytes(b)
		}
		script = fmt.Sprintf("%x", h.Sum())
	}
	opts := fmt.Sprintf("mk.link.v4 %x %x %s %s %s %d %q %t %t %d %t %t %s", build, tools.Identity(), cfg.Triple, cfg.CPU, prof,
		job.Format, job.Entry, job.Release, job.LTO, job.StackReserve, job.Debug, job.Free, script)
	// An experiment's LTO options and route; the zero Tuning adds nothing,
	// so every default key stays valid.
	if !job.Tune.IsZero() {
		opts += " tune=" + job.Tune.String()
	}
	// "" and "console" link the same bytes, so only "windows"
	// changes the key (and an existing console key stays valid).
	if job.Subsystem == "windows" {
		opts += " subsystem=windows"
	}
	// Only an image with static data aligned over a page has a
	// section alignment, so every other key stays valid.
	if job.SectionAlign != 0 {
		opts += fmt.Sprintf(" align=%d", job.SectionAlign)
	}
	return opts
}
