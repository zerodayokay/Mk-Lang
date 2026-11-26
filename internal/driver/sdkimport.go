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
// imports a system view: it opens those views and their required ones and
// rewrites each binding with cimport.ShareWithViews.

package driver

import (
	"maps"
	"os"
	"slices"
	"strings"

	"mk/internal/cimport"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/sysview"
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
		data, ok := w.sysViewBytes(sysViewArchive(row, target), n)
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
	for i, text := range ci.full {
		shared, _, err := cimport.ShareWithViews(text, vi)
		if err != nil {
			d := diag.Toolchain("clang", "", err.Error(), "C import "+ci.names[i]+": "+err.Error())
			d.At = ci.at
			return d
		}
		ci.full[i] = shared
	}
	return nil
}
