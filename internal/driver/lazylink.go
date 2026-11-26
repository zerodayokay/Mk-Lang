// Why: a lazily bound DLL (pkg.Link kind "lazy", or std's
// sys/windows/<dll>.lazy.def lists) is never in the import table; its
// functions are called through slots the build emits, so the driver must
// know which names bind lazily before collection (the resolver is then
// collected) and before codegen (dl.Present folds for the others), and
// must build the slots' object for the link.
//
// What: lazyLib and lazyBinds (the build's predicate: which name binds
// lazily, to which DLL), world.lazyBinds, lazyMonoOptions, lazyUse and
// takeLazy (the names a link binds lazily), and lazyObject (the
// lazy-binding object, codegen.EmitLazy compiled as the support pack is).
//
// How: a name resolves as for a hard import, in this order: std's eager
// .def lists; the graph's dynamic and lazy links in manifest order; std's
// .lazy.def lists; the system set. So a name a package links "dynamic"
// stays a hard import, std's lazy lists change no package's build, and a
// name binds lazily only where a lazy list is the first to supply it. A
// user link loads with LOAD_LIBRARY_SEARCH_APPLICATION_DIR |
// LOAD_LIBRARY_SEARCH_SYSTEM32 (0xA00), the search a hard import gets
// under the executable's DependentLoadFlags (links_libc.go), so a .dll mk
// copies beside the executable is found; std's lists load from System32
// alone (0x800), as std's hand-written loads did. Two lists of one DLL
// share one library record, the first one's. Off x86_64-windows nothing
// binds lazily (validateLinks refuses the kind).

package driver

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"mk/internal/cache"
	"mk/internal/codegen"
	"mk/internal/diag"
	"mk/internal/mono"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// The LoadLibraryExW flags of a lazy library: LOAD_LIBRARY_SEARCH_SYSTEM32
// for std's lists, with LOAD_LIBRARY_SEARCH_APPLICATION_DIR for a link's.
const (
	lazySearchSystem      uint32 = 0x800
	lazySearchApplication uint32 = 0xA00
)

// lazyStdSuffix names std's lazy lists: sys/windows/<dll>.lazy.def.
const lazyStdSuffix = ".lazy.def"

// lazyRuntimeKeys are the std functions a lazy stub reaches by symbol:
// the resolver and dl.Present's entry (std/sys/windows/lazy.mk), and the
// system-stack runner the resolver calls (std/task/sysrun.mk).
var lazyRuntimeKeys = []types.Key{
	types.ItemKey("std.sys", "MkLazyResolve"),
	types.ItemKey("std.sys", "MkLazyPresent"),
	types.ItemKey("std.task", "MkSystemRun"),
}

// isLazyRuntime reports whether k is one of lazyRuntimeKeys.
func isLazyRuntime(k types.Key) bool {
	for _, l := range lazyRuntimeKeys {
		if l.Compare(k) == 0 {
			return true
		}
	}
	return false
}

// lazyLib is one lazily bound DLL: the loader's file name, its flags, and
// the .dll a link copies beside the executable ("" for none).
type lazyLib struct {
	file  string
	flags uint32
	copy  string
}

// lazyBinds is the build's lazy predicate: each name that binds lazily, to
// its library; libs in the order they were met.
type lazyBinds struct {
	of   map[string]*lazyLib
	libs []*lazyLib
}

// syms is the set of lazily bound names, nil for none.
func (b *lazyBinds) syms() map[string]bool {
	if b == nil {
		return nil
	}
	out := make(map[string]bool, len(b.of))
	for s := range b.of {
		out[s] = true
	}
	return out
}

// lazyBinds is the predicate of the world's target, computed once; nil
// when nothing binds lazily. A link whose names cannot be read supplies
// none here: linkPlanFor reports it.
func (w *world) lazyBinds() *lazyBinds {
	w.lazyOnce.Do(func() { w.lazy = w.computeLazy() })
	return w.lazy
}

func (w *world) computeLazy() *lazyBinds {
	if w.cfg.Target.OS != types.OSWindows || w.cfg.Target.Arch != types.ArchX64 {
		return nil
	}
	t := w.stdTree()
	ents, err := fs.ReadDir(t.fsys, "sys/windows")
	if err != nil {
		return nil
	}
	eager := map[string]bool{}
	type stdList struct {
		dll   string
		names []string
	}
	var lazyLists []stdList
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || path.Ext(n) != ".def" {
			continue
		}
		raw, err := t.readFile("sys/windows/" + n)
		if err != nil {
			continue
		}
		names, err := toolchain.ParseDefList(string(raw))
		if err != nil {
			continue
		}
		if dll, ok := strings.CutSuffix(n, lazyStdSuffix); ok {
			lazyLists = append(lazyLists, stdList{dll: dll + ".dll", names: names})
			continue
		}
		for _, s := range names {
			eager[s] = true
		}
	}
	b := &lazyBinds{of: map[string]*lazyLib{}}
	byFile := map[string]*lazyLib{}
	libFor := func(file string, flags uint32) *lazyLib {
		k := strings.ToLower(file)
		if l := byFile[k]; l != nil {
			return l
		}
		l := &lazyLib{file: file, flags: flags}
		byFile[k] = l
		b.libs = append(b.libs, l)
		return l
	}
	claimed := map[string]bool{} // supplied by an earlier link, dynamic or lazy
	for _, l := range linksFor(append(append([]pkgLink(nil), w.links...), testPkgLinks()...), w.cfg.Target.Name) {
		if l.Kind == linkStatic {
			continue
		}
		names, _, d := dllNames(l)
		if d != nil {
			continue
		}
		var lib *lazyLib
		if l.Kind == linkLazy {
			lib = libFor(l.Name, lazySearchApplication)
			if strings.EqualFold(path.Ext(l.Path), ".dll") && lib.copy == "" {
				lib.copy, _ = filepath.Abs(l.file())
			}
		}
		for _, s := range names {
			if eager[s] || claimed[s] {
				continue
			}
			claimed[s] = true
			if lib != nil {
				b.of[s] = lib
			}
		}
	}
	for _, sl := range lazyLists {
		lib := libFor(sl.dll, lazySearchSystem)
		for _, s := range sl.names {
			if !eager[s] && !claimed[s] && b.of[s] == nil {
				b.of[s] = lib
			}
		}
	}
	if len(b.of) == 0 {
		return nil
	}
	return b
}

// lazyMonoOptions sets opt's lazy fields: every lazily bound name, and the
// items a lazy stub reaches (those std declares).
func (w *world) lazyMonoOptions(opt *mono.Options) {
	b := w.lazyBinds()
	if b == nil {
		return
	}
	opt.LazySyms = b.syms()
	for _, k := range lazyRuntimeKeys {
		if id, ok := w.items.ByKey(k); ok {
			opt.LazyRuntime = append(opt.LazyRuntime, id)
		}
	}
}

// lazyUse is the names one lazy library supplies to a link.
type lazyUse struct {
	lib   *lazyLib
	names []string
}

// takeLazy splits the lazily bound names out of names (the link's
// candidates: Mk externs and the shim's undefined names): it returns the
// names left, and adds the lazy ones to uses, grouped by library in the
// predicate's order, names sorted. A name an object of the link defines
// (defs) is not a candidate.
func (b *lazyBinds) takeLazy(names []string, defs map[string]bool, uses []lazyUse) ([]string, []lazyUse) {
	if b == nil {
		return names, uses
	}
	var rest []string
	byLib := map[*lazyLib][]string{}
	for _, n := range names {
		lib := b.of[n]
		if lib == nil || defs[n] || !codegen.LazySymOK(n) {
			rest = append(rest, n)
			continue
		}
		byLib[lib] = append(byLib[lib], n)
	}
	for _, lib := range b.libs {
		got := byLib[lib]
		if len(got) == 0 {
			continue
		}
		i := slices.IndexFunc(uses, func(u lazyUse) bool { return u.lib == lib })
		if i < 0 {
			uses = append(uses, lazyUse{lib: lib})
			i = len(uses) - 1
		}
		uses[i].names = slices.Compact(slices.Sorted(slices.Values(append(uses[i].names, got...))))
	}
	slices.SortStableFunc(uses, func(x, y lazyUse) int {
		return slices.Index(b.libs, x.lib) - slices.Index(b.libs, y.lib)
	})
	return rest, uses
}

// lazyObject builds the lazy-binding object of uses into work, or takes
// it from the store: codegen.EmitLazy compiled as the support pack is
// (native code, -O3, no LTO). It returns the object and its key (objKey
// over the IR), which joins the link key.
func (w *world) lazyObject(ctx context.Context, tools *toolchain.Tools, st *cache.Store, cfg codegen.Config, work string, uses []lazyUse) (string, source.Hash, []*diag.Diagnostic) {
	libs := make([]codegen.LazyLib, len(uses))
	for i, u := range uses {
		libs[i] = codegen.LazyLib{File: u.lib.file, Flags: u.lib.flags, Names: u.names}
	}
	var b bytes.Buffer
	if err := codegen.EmitLazy(libs, cfg, &b); err != nil {
		return "", source.Hash{}, []*diag.Diagnostic{diag.FromInternal(&source.InternalError{Where: "codegen lazy", What: err.Error()})}
	}
	ll := filepath.Join(work, "lazy.ll")
	job := toolchain.CompileJob{Triple: cfg.Triple, Input: ll, Output: filepath.Join(work, "lazy.o"), CPU: cfg.CPU, Opt: toolchain.O3, DirectDriver: true}
	key := objKey(w.buildID(), tools, job, b.Bytes())
	if st != nil && !w.cfg.CacheVerify {
		if p, ok, _ := st.FilePath(cache.KindObj, key); ok {
			return p, key, nil
		}
	}
	if err := os.WriteFile(ll, b.Bytes(), 0o644); err != nil {
		return "", key, []*diag.Diagnostic{diag.Toolchain("fs", "write lazy.ll", pathErrText(err), "cannot write lazy.ll")}
	}
	if err := tools.Compile(ctx, job); err != nil {
		return "", key, []*diag.Diagnostic{toolErrIn(err, "clang", "clang could not compile the lazy-binding object", work)}
	}
	obj := job.Output
	if st != nil {
		obj = storeFile(st, cache.KindObj, key, obj)
	}
	return obj, key, nil
}
