// Why: a package's own C import whose headers include the platform's asks
// for the host's SDK with sdk: true; without the field nothing may change,
// and a C type the program also takes from a system view must be the
// view's, not a second copy.
//
// What: wantsSDK, Session.sysrootOf (the system header directories of a
// package's C for a target) and cImports.shareViews (the binding texts with
// the declarations a system view binds replaced by the view's items).
//
// How: sdk: true on any C import of a package gives its one translation
// unit hostSDKFor's directories (TestHooks.HostSDK in tests); none found is
// E0213 no-sysroot unless every import is vendored. Without it the
// directories are sysrootFor's, as before. shareViews runs in prune, before
// the bindings are pruned, only for a package with sdk: true whose program
// imports a system view: it opens those views and their required ones that
// are generated for the build's target (never another target's, whose C
// types may differ) and rewrites each binding with cimport.ShareWithViews.

package driver

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"mk/internal/cimport"
	"mk/internal/diag"
	"mk/internal/mono"
	"mk/internal/source"
	"mk/internal/sysview"
	"mk/internal/types"
)

// wantsSDK reports whether a C import of man says sdk: true.
func wantsSDK(man *Manifest) bool {
	if man == nil {
		return false
	}
	for _, c := range man.CImports {
		if c.SDK {
			return true
		}
	}
	return false
}

// sysrootOf is the system header directories of man's C for target:
// sysrootFor's, or with sdk: true the host's SDK (hostsdk.go).
func (s *Session) sysrootOf(target string, man *Manifest) ([]string, *diag.Diagnostic) {
	if !wantsSDK(man) {
		return sysrootFor(target, os.Getenv), nil
	}
	var h hostSDK
	if f := s.cfg.TestHooks.HostSDK; f != nil {
		h.Dirs, h.Source = f(target)
	} else {
		h = hostSDKFor(target, realHostEnv())
	}
	if len(h.Dirs) == 0 && !allVendored(man) {
		at := source.Span{}
		if len(man.CImports) > 0 {
			at = man.spanOf(man.CImports[0].Field)
		}
		return nil, diag.New(diag.E0213, at, "sdk: true: no SDK for "+target+" on this machine: set MK_SYSROOT_"+target+
			", install the SDK (mk sdk shows where mk looks), or vendor the C imports (vendor: true)").
			Text("target", target).Text("reason", "no-sysroot")
	}
	return h.Dirs, nil
}

// shareViews rewrites ci.full when the package says sdk: true and a file of
// builds imports a system view: each declaration those views bind is the
// view's item.
func (ci *cImports) shareViews(w *world, builds []*build) *diag.Diagnostic {
	if !wantsSDK(ci.man) {
		return nil
	}
	named := map[string]bool{}
	for _, b := range builds {
		if b == ci.b {
			continue
		}
		for _, m := range b.mods {
			for _, f := range m.files {
				var text string
				if f.src != nil {
					text = f.src.Text
				} else if raw, err := m.tree.readFile(f.rel); err == nil {
					text = source.Normalize(raw)
				}
				if !strings.Contains(text, "c.") {
					continue
				}
				fw := cimport.Want{}
				cimport.Uses(text, fw)
				for v := range fw {
					if _, ok := sysview.Named(v); ok && !slices.Contains(ci.names, v) {
						named[v] = true
					}
				}
			}
		}
	}
	if len(named) == 0 {
		return nil
	}
	target := w.cfg.Target.Name
	set := sysview.Set{}
	var open func(string)
	open = func(n string) {
		if set[n] != nil {
			return
		}
		row, _ := sysview.Named(n)
		at := w.sysViewArchive(row, target)
		if at != target {
			// Another target's declarations (c.windows, or c.vulkan with
			// no archive of its own, in a build for Linux): a C long or
			// wchar_t there is not this target's, so nothing is shared.
			return
		}
		data, ok := w.sysViewBytes(at, n)
		if !ok {
			return
		}
		a, err := sysview.Open(data)
		if err != nil {
			return
		}
		set[n] = a
		for _, r := range a.Meta().Requires {
			open(r)
		}
	}
	for _, n := range slices.Sorted(maps.Keys(named)) {
		open(n)
	}
	// Table order: an earlier view owns a declaration both bind.
	var order []string
	for _, r := range sysview.Table {
		if set[r.Name] != nil {
			order = append(order, r.Name)
		}
	}
	vi, err := cimport.NewViewIdentity(set, order)
	if err != nil {
		return diag.Toolchain("fs", "read", err.Error(), "a system view this mk carries is corrupt: "+err.Error())
	}
	shared, err := cimport.SharePackageWithViews(ci.names, ci.full, vi)
	if err != nil {
		d := diag.Toolchain("clang", "", err.Error(), "C imports: "+err.Error())
		d.At = ci.at
		return d
	}
	ci.full = shared
	return nil
}

// addSDKImportLibs resolves remaining Windows extern symbols belonging to
// `sdk: true` C imports against the host SDK's import libraries. An
// ambiguous symbol is E0001 naming the candidate DLLs and the pkg.Link
// entry to add, never a silent guess.
func (w *world) addSDKImportLibs(libs []importLib, in *mono.Instances, cobjs, all []string) ([]importLib, []*diag.Diagnostic) {
	allC := w.allCImports()
	var sdkCIs []*cImports
	ciByIdx := map[string]*cImports{}
	modToCI := map[types.ModuleID]*cImports{}
	for i, ci := range allC {
		if !wantsSDK(ci.man) {
			continue
		}
		sdkCIs = append(sdkCIs, ci)
		ciByIdx["c"+strconv.Itoa(i)] = ci
		if ci.b != nil {
			for _, m := range ci.b.mods {
				modToCI[m.id] = ci
			}
		}
	}
	if len(sdkCIs) == 0 {
		return libs, nil
	}
	have := map[string]bool{}
	for _, l := range libs {
		for _, n := range l.names {
			have[n] = true
		}
	}
	need := map[string]bool{}
	for _, n := range all {
		if !have[n] {
			need[n] = true
		}
	}
	if len(need) == 0 {
		return libs, nil
	}
	symOwner := map[string]*cImports{}
	for i := range in.List {
		if in.List[i].Body != nil || !need[in.List[i].Symbol] {
			continue
		}
		it := w.items.Get(in.List[i].Item)
		fd, ok := it.Decl.(*types.FuncDecl)
		if !ok || !fd.Sig.Extern {
			continue
		}
		if ci := modToCI[it.Module]; ci != nil && symOwner[in.List[i].Symbol] == nil {
			symOwner[in.List[i].Symbol] = ci
		}
	}
	for _, o := range shimObjects(cobjs) {
		ci := ciByIdx[filepath.Base(filepath.Dir(o))]
		if ci == nil && len(sdkCIs) == 1 {
			ci = sdkCIs[0]
		}
		if ci == nil {
			continue
		}
		for _, u := range cObjectUndefined([]string{o}) {
			if need[u] && symOwner[u] == nil {
				symOwner[u] = ci
			}
		}
	}
	if len(symOwner) == 0 {
		return libs, nil
	}
	cacheIdx := map[string]*implibIndex{}
	indexFor := func(ci *cImports) *implibIndex {
		dirs := sdkLibDirs(ci.sysdir)
		if len(dirs) == 0 {
			return nil
		}
		key := strings.Join(dirs, "\x00")
		if idx, ok := cacheIdx[key]; ok {
			return idx
		}
		idx := mergeImplibDirs(dirs)
		cacheIdx[key] = idx
		return idx
	}
	extra := map[string][]string{}
	var ds []*diag.Diagnostic
	for _, sym := range slices.Sorted(maps.Keys(symOwner)) {
		ci := symOwner[sym]
		dll, _, amb := indexFor(ci).resolve(sym)
		if len(amb) > 0 {
			ds = append(ds, diag.New(diag.E0001, ci.at, fmt.Sprintf(
				"symbol %s from sdk: true C import %s is exported by multiple SDK libraries (%s): add `pkg.Link(name: %q, kind: \"dynamic\", path: \"\", target: %q)` to pkg.mk",
				sym, sdkImportLabel(ci), strings.Join(amb, ", "), amb[0], w.cfg.Target.Name)).
				Text("tool", "mk").Text("symbol", sym))
			continue
		}
		if dll != "" {
			extra[dll] = append(extra[dll], sym)
		}
	}
	if len(ds) > 0 {
		return nil, ds
	}
	return mergeImportLibs(libs, extra), nil
}

// sdkImportLabel names the C import(s) of ci for an ambiguity error.
func sdkImportLabel(ci *cImports) string {
	var names []string
	if ci.man != nil {
		for _, c := range ci.man.CImports {
			if c.SDK {
				names = append(names, "c."+c.Name)
			}
		}
	}
	if len(names) == 0 {
		return "c"
	}
	return strings.Join(names, ", ")
}
