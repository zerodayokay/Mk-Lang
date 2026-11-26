// Why: a system view may be imported in a build for any target (a std
// module with per-OS backends checks everywhere), so a view's function that
// a build for another system reaches must fail with a message that says
// why, and `mk check --all-targets` must say what a view does across
// targets.
//
// What: world.sysViewLinkHints (the E0001 of a link that misses a view's
// function) and pipeline.sysViewAllTargets (the E0213 of a multi-target
// view whose used items differ between the targets --all-targets compares).
//
// How: the functions the kept items bind are recorded at extraction; an
// undefined symbol lld reports that is one of them gets its own E0001
// naming the view and its targets. A view generated for one target reads
// the same archive on every target, so its API cannot differ and nothing is
// compared, nor is a FallbackToFirst view (vulkan) for a target whose own
// archive mk lacks; a view mk carries per target is extracted again from
// each other target's archive with the same names, and the items' APIs are
// compared as a C import's are (cimport.API).

package driver

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"mk/internal/cimport"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/sysview"
	"mk/internal/toolchain"
)

// sysViewLinkHints adds to ds, the diagnostics of a failed link, one E0001
// per undefined symbol that is a function of a system view: for a view of
// another system, that the call must stay behind that system's
// Target.os branch; else that no library this bin links exports it.
func (w *world) sysViewLinkHints(ds []*diag.Diagnostic, err error, work string) []*diag.Diagnostic {
	sv := w.sysviews
	var te *toolchain.ToolError
	if sv == nil || len(sv.funcs) == 0 || !errors.As(err, &te) {
		return ds
	}
	target := w.cfg.Target.Name
	seen := map[string]bool{}
	for _, raw := range undefinedSymbols(te.Stderr) {
		sym, _ := strings.CutPrefix(raw, dllimportPrefix)
		view, ok := sv.funcs[sym]
		if !ok || seen[sym] {
			continue
		}
		seen[sym] = true
		row, _ := sysview.Named(view)
		var msg string
		if archive := w.sysViewArchive(row, target); archive != target {
			msg = fmt.Sprintf("the link failed: %s is a function of the system view c.%s, an API of %s, and this build is for %s: call it only where Target.os is %s's, as in `if Target.os == Os.%s { }`",
				sym, view, archive, target, archive, osWord(archive))
		} else {
			msg = fmt.Sprintf("the link failed: %s, a function of the system view c.%s, is exported by no library this bin links: add a pkg.Link for its DLL", sym, view)
		}
		ds = append(ds, toolErrIn(err, "lld", msg, work).Text("symbol", sym).Text("view", "c."+view))
	}
	return ds
}

// osWord is the Os variant of a target name's system: "Windows" for
// x86_64-windows.
func osWord(target string) string {
	_, os, _ := strings.Cut(target, "-")
	if os == "" {
		return target
	}
	return strings.ToUpper(os[:1]) + os[1:]
}

// sysViewAllTargets is --all-targets' E0213 for the system views the
// program uses: for each view generated per target, the used items whose
// API differs between the session's target and another of cTargets, or
// the other target's archive missing. names are the wanted names by view;
// mods the session's extracted modules.
func (p *pipeline) sysViewAllTargets(names map[string][]string, mods []extracted) []*diag.Diagnostic {
	w := p.w
	target := w.cfg.Target.Name
	var out []*diag.Diagnostic
	for _, em := range mods {
		row, _ := sysview.Named(em.Name)
		mine := w.sysViewArchive(row, target)
		for _, t := range cTargets {
			other := w.sysViewArchive(row, t.Name)
			if t.Name == target || other == mine {
				continue // the same archive: the same declarations
			}
			set := sysview.Set{}
			var open func(string) bool
			open = func(n string) bool {
				if set[n] != nil {
					return true
				}
				data, ok := w.sysViewBytes(other, n)
				if !ok {
					return false
				}
				a, err := sysview.Open(data)
				if err != nil {
					return false
				}
				set[n] = a
				for _, r := range a.Meta().Requires {
					if !open(r) {
						return false
					}
				}
				return true
			}
			if !open(em.Name) {
				out = append(out, diag.New(diag.E0213, source.Span{}, fmt.Sprintf("--all-targets: this mk carries no system view c.%s for %s", em.Name, t.Name)).
					Text("import", "c."+em.Name).Text("target", t.Name).Text("reason", "no-sysview"))
				continue
			}
			want := map[string][]string{}
			for _, v := range slices.Sorted(maps.Keys(names)) {
				if set[v] != nil {
					want[v] = names[v]
				}
			}
			theirs, err := sysview.Extract(set, want, false)
			if err != nil {
				continue
			}
			for _, m := range theirs {
				if m.Name != em.Name {
					continue
				}
				if diff := cimport.APIDiff(cimport.API(em.Text), cimport.API(m.Text)); len(diff) > 0 {
					out = append(out, cimport.Mismatch(source.Span{}, em.Name, target, t.Name, diff))
				}
			}
		}
	}
	return out
}
