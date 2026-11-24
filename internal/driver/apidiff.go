// Why: a release must not change a public fact (a signature, a flow, an
// error set, a const value) without saying so; mk apidiff A B is the tool
// that finds every such change between two package roots.
//
// What: APIDiff and its result, APIDiffResult (APIChange per difference,
// Severity, ExitStatus, Text), and the fact spellers: apiFacts, implFacts,
// stdPkgFacts and the std alias helpers stdAliasTarget and stdReexport.
//
// How: each root is checked in its own session and every exported item's
// facts are spelled as strings keyed module.Name (never IDs, which differ
// between the two sessions); the two maps are compared key by key and the
// rules of apidiff_rules.go relax a change's severity where a promise allows.

package driver

import (
	"sort"
	"strings"

	"mk/internal/diag"
	"mk/internal/types"
)

// Severity is a change's class: breaking, recompile, reported, compatible.
type Severity string

const (
	Breaking   Severity = "breaking"
	Recompile  Severity = "recompile"
	Reported   Severity = "reported"
	Compatible Severity = "compatible"
)

// APIChange is one difference.
type APIChange struct {
	Severity Severity `json:"severity"`
	Key      string   `json:"key"`  // module.Name
	What     string   `json:"what"` // signature, flows, fn flows, errors, kind, carrier, send, share, size, abi, fields, value, added, removed
	Old      string   `json:"old"`
	New      string   `json:"new"`
}

// APIDiffResult is the differences, by key then what.
type APIDiffResult struct {
	Changes []APIChange `json:"changes"`
	// A and B are the two checks, for their diagnostics when one did not
	// reach the types phase (then Changes is empty).
	A, B Result `json:"-"`
	// Incomplete is true when a root did not check far enough to have its
	// public facts: a diagnostic before the types phase (a parse or resolve
	// error, or a directory program with no Main) or an internal error.
	// Nothing is compared then, since half-checked signatures would make
	// every change look like none.
	Incomplete bool `json:"incomplete,omitempty"`
}

// ExitStatus is 1 when a change is breaking or a root is incomplete, else 0.
func (r *APIDiffResult) ExitStatus() int {
	if r.Incomplete {
		return 1
	}
	for _, c := range r.Changes {
		if c.Severity == Breaking {
			return 1
		}
	}
	return 0
}

// Text is one line per change.
func (r *APIDiffResult) Text() string {
	var b strings.Builder
	for _, c := range r.Changes {
		b.WriteString(string(c.Severity) + "  " + c.Key + "  " + c.What)
		switch {
		case c.Old != "" && c.New != "":
			b.WriteString(": " + c.Old + "  ->  " + c.New)
		case c.New != "":
			b.WriteString(": " + c.New)
		case c.Old != "":
			b.WriteString(": " + c.Old)
		}
		b.WriteByte('\n')
	}
	switch {
	case r.Incomplete:
		b.WriteString("not compared: a root does not check far enough (its diagnostics are on stderr)\n")
	case len(r.Changes) == 0:
		b.WriteString("no API change\n")
	}
	return b.String()
}

// apiFact is one public fact of one item, by its what.
type apiFact struct {
	what, value string
	sev         Severity
}

// APIDiff checks a and b (two package roots) and compares their exported
// items. A config that does not make a session is a *UsageError.
func APIDiff(a, b Config) (*APIDiffResult, error) {
	return apiDiff(a, b, false)
}

// apiDiff compares a and b: the package's exported items, or with std the
// exported items of std's public modules (APIDiffStd).
func apiDiff(a, b Config, std bool) (*APIDiffResult, error) {
	fa, ma, ra, err := apiFacts(a, std)
	if err != nil {
		return nil, err
	}
	fb, mb, rb, err := apiFacts(b, std)
	if err != nil {
		return nil, err
	}
	opens := openSets(ma, mb)
	closed := closedGrowth(ma, mb, opens)
	out := &APIDiffResult{A: ra, B: rb}
	if fa == nil || fb == nil {
		out.Incomplete = true
		return out, nil
	}
	keys := map[string]bool{}
	for k := range fa {
		keys[k] = true
	}
	for k := range fb {
		keys[k] = true
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		old, inA := fa[k]
		nw, inB := fb[k]
		switch {
		case !inA && openTwin(mb[k], fa, ma):
			// Error sets are structural, so an open alias whose members all
			// exist in the older release opens every set that holds them
			// all, a dependent's own spelling included: its exhaustive
			// matches outside the package now need `_`.
			sev := Breaking
			if experimental(mb[k].mod) {
				sev = Reported // an Experimental module, as for a removed item
			}
			out.Changes = append(out.Changes, APIChange{Severity: sev, Key: k, What: "added, open over existing members", New: mb[k].shown(nw[0])})
		case !inA:
			out.Changes = append(out.Changes, APIChange{Severity: Compatible, Key: k, What: "added", New: mb[k].shown(nw[0])})
		case !inB:
			sev := Breaking
			if experimental(ma[k].mod) {
				sev = Reported // an Experimental module's change is reported
			}
			out.Changes = append(out.Changes, APIChange{Severity: sev, Key: k, What: "removed", Old: ma[k].shown(old[0])})
		default:
			byWhat := map[string]apiFact{}
			for _, f := range nw {
				byWhat[f.what] = f
			}
			for _, f := range old {
				g, ok := byWhat[f.what]
				if !ok && f.what == "open" {
					// The item changed kind, so the fact is gone, not the
					// word: the declaration's line already says it.
					continue
				}
				if !ok || g.value != f.value {
					sev, what := relaxed(f, g.value, ma[k], mb[k], opens[k], closed[k])
					out.Changes = append(out.Changes, APIChange{Severity: sev, Key: k, What: what, Old: ma[k].shown(f), New: mb[k].shown(g)})
				}
			}
		}
	}
	return out, nil
}

// apiFacts checks one root and spells the public facts of its exported
// items by module.Name, with what the comparison's rules read of each
// (apidiff_rules.go): the package's items, or with std std's.
func apiFacts(cfg Config, std bool) (map[string][]apiFact, map[string]apiMeta, Result, error) {
	cfg.GenView = false
	s, err := NewSession(cfg)
	if err != nil {
		return nil, nil, Result{}, err
	}
	s.fullStd = std
	res := s.Check()
	if s.w == nil || s.w.sem5 == nil || s.stdB == nil || !checkedToTypes(res) {
		return nil, nil, res, nil
	}
	out, meta := sessionAPIFacts(s, std)
	return out, meta, res, nil
}

// sessionAPIFacts spells the public facts of every exported item in an
// already-checked Session s (the package's items, or with std std's).
func sessionAPIFacts(s *Session, std bool) (map[string][]apiFact, map[string]apiMeta) {
	qx := newQueryIndex(s)
	out := map[string][]apiFact{}
	meta := map[string]apiMeta{}
	defaults := qx.defaultConsts()
	for _, e := range qx.entries {
		it := qx.w.items.Get(e.id)
		// The query index already keeps of an internal std module only the
		// prelude's names and their methods (std.core), which are public.
		if e.std != std || !it.Exported {
			continue
		}
		e, it = qx.stdReexport(e, it)
		meta[e.mod+"."+e.name] = qx.apiMetaOf(e, defaults)
		var fs []apiFact
		switch d := it.Decl.(type) {
		case *types.FuncDecl:
			fs = append(fs, apiFact{"signature", qx.sigLine(e.name, &d.Sig), Breaking},
				apiFact{"flows", strings.Join(qx.flows(e), "; "), Breaking},
				apiFact{"errors", strings.Join(qx.errors(e), "; "), Breaking},
				apiFact{"fn flows", strings.Join(qx.sigFnFlows(&d.Sig, "", nil), "; "), Breaking})
			var cs []string
			for _, c := range d.Sig.Contracts {
				cs = append(cs, c.Text)
			}
			fs = append(fs, apiFact{"contracts", strings.Join(cs, "; "), Breaking})
		case *types.StructDecl, *types.EnumDecl:
			fs = append(fs, apiFact{"declaration", qx.declHead(e, false), Breaking})
			if _, ok := d.(*types.EnumDecl); ok {
				fs = append(fs, openFact(meta[e.mod+"."+e.name]))
			}
			var fields, fnf []string
			if sd, ok := d.(*types.StructDecl); ok {
				for _, f := range sd.Fields {
					if f.Exported {
						fields = append(fields, qx.fieldLine(f, sd.Generics))
					}
				}
				fnf = qx.fieldsFnFlows(sd.Fields, "", true, nil)
			} else {
				// The variants only: a method is a fact of its own
				// (Type.Name), so adding one is an addition, not a change
				// to what a match over the enum names.
				fields = qx.variantLines(d.(*types.EnumDecl))
				for _, v := range d.(*types.EnumDecl).Variants {
					fnf = qx.fieldsFnFlows(v.Fields, qx.str(v.Name)+".", false, fnf)
				}
			}
			fs = append(fs, apiFact{"fields", strings.Join(fields, "; "), Breaking},
				apiFact{"fn flows", strings.Join(fnf, "; "), Breaking})
			if !genericDecl(it.Decl) {
				t := qx.w.tab.Named(e.id, nil)
				sh := qx.w.sem5.chk.Shape(t)
				fs = append(fs, apiFact{"kind", sh.Kind.String(), Breaking},
					apiFact{"carrier", yes(sh.Carrier), Breaking},
					apiFact{"send", yes(sh.Send), Breaking},
					apiFact{"share", yes(sh.Share), Breaking},
					apiFact{"size", itoaQ(int(sh.Size)) + " align " + itoaQ(int(sh.Align)), Recompile},
					apiFact{"abi", abiString(sh.ABI), Recompile})
			}
		case *types.ConstDecl:
			fs = append(fs, apiFact{"type", qx.w.tab.TypeString(d.Type, nil), Breaking},
				apiFact{"fn flows", strings.Join(fnTypeFlows(qx.w.tab, d.Type, "type", nil), "; "), Breaking})
			if d.HasValue {
				fs = append(fs, apiFact{"value", qx.w.tab.ConstString(d.Value), Reported})
			}
		case *types.InterfaceDecl:
			// A method added, removed or changed breaks every implementer.
			fs = append(fs, apiFact{"declaration", qx.declLine(e), Breaking},
				apiFact{"methods", strings.Join(qx.members(e), "; "), Breaking})
			var fnf []string
			for i := range d.Methods {
				fnf = qx.sigFnFlows(&d.Methods[i], qx.str(d.Methods[i].Name)+".", fnf)
			}
			fs = append(fs, apiFact{"fn flows", strings.Join(fnf, "; "), Breaking})
		case *types.ErrorDecl:
			// The payload is what a match or an else e reads.
			// The payload alone: members also lists the compiler's Format.
			var pay []string
			for _, f := range d.Fields {
				pay = append(pay, qx.fieldLine(f, nil))
			}
			fs = append(fs, apiFact{"declaration", qx.declLine(e), Breaking},
				apiFact{"fields", strings.Join(pay, "; "), Breaking})
		case *types.AliasDecl:
			fs = append(fs, apiFact{"declaration", qx.declHead(e, false), Breaking}, openFact(meta[e.mod+"."+e.name]))
		default:
			fs = append(fs, apiFact{"declaration", qx.declLine(e), Breaking})
		}
		out[e.mod+"."+e.name] = fs
	}
	mods := qx.s.stdB.mods
	if !std {
		mods = qx.pkgMods()
	}
	for k, f := range qx.implFacts(mods, std) {
		out[k] = f
		meta[k] = apiMeta{mod: k[:strings.Index(k, ".impl ")]}
	}
	if std {
		stdPkgFacts(s, out, meta)
	}
	resolveRefs(meta)
	return out, meta
}

// stdAliasTarget is the declaration a std alias re-exports: for an
// exported, non-generic alias in a std module whose type is an exported
// struct or enum, with no arguments, declared in a std-internal module, the
// target's item. These are std.core's TargetInfo, Arch and Os over
// std.target's and std.time's Duration over std.core's. A user's
// code cannot import the target's module, so for it the alias is the
// type's name. apidiff compares such an alias as its target (stdReexport),
// and mk query members lists the target's members under the alias's name.
func stdAliasTarget(w *world, it *types.Item) (types.ItemID, bool) {
	ad, ok := it.Decl.(*types.AliasDecl)
	if !ok || !it.Exported || len(ad.Generics) != 0 || !w.items.Module(it.Module).Std {
		return types.NoItem, false
	}
	ty := w.tab.Get(ad.Type)
	if ty.Kind != types.KNamed || len(ty.Args) != 0 {
		return types.NoItem, false
	}
	t := w.items.Get(ty.Item)
	if !t.Exported || !isStdInternal(w.items.Module(t.Module).Path) {
		return types.NoItem, false
	}
	switch t.Decl.(type) {
	case *types.StructDecl, *types.EnumDecl:
		return ty.Item, true
	}
	return types.NoItem, false
}

// stdReexport is the entry whose facts std's exported alias e promises:
// for an alias stdAliasTarget accepts, the target item under e's own name
// and module. So moving a prelude type below std.core into such a module
// and aliasing it compares as the declaration it was, and a change
// of the target's facts (std.core's Duration growing a field) is a change
// of std.time's Duration. The alias's own declaration line is not
// compared: retargeting the alias to another declaration with the same
// facts is no change. Any other entry, a package's alias included, is
// returned as it is.
func (qx *queryIndex) stdReexport(e qEntry, it *types.Item) (qEntry, *types.Item) {
	if !e.std {
		return e, it
	}
	if t, ok := stdAliasTarget(qx.w, it); ok {
		e.id = t
		return e, qx.w.items.Get(t)
	}
	return e, it
}

// implFacts are the package's public impls: an impl is the fact a bound
// depends on, so removing one, or changing its generics or their bounds,
// breaks a caller whose argument met the bound. Each impl is its own key,
// module.impl Bound for Type, so an impl for a type of another module (one
// per interface and type constructor, in the module of either) is compared
// as well. An impl whose type or interface is a package item that is not
// exported is not public and is skipped.
func (qx *queryIndex) implFacts(mods []*modNode, std bool) map[string][]apiFact {
	out := map[string][]apiFact{}
	private := func(id types.ItemID) bool {
		if id == types.NoItem {
			return false
		}
		it := qx.w.items.Get(id)
		return !it.Exported && (std || !qx.w.items.Module(it.Module).Std)
	}
	for _, m := range mods {
		if m.mod == nil || std && isStdInternal(m.path) {
			continue
		}
		for _, id := range m.mod.Items {
			im, ok := qx.w.items.Get(id).Decl.(*types.ImplDecl)
			if !ok || im.For == types.NoType {
				continue
			}
			if ty := qx.w.tab.Get(im.For); ty.Kind == types.KNamed && private(ty.Item) {
				continue
			}
			if im.Bound.Kind != types.BoundPlain && im.Bound.Kind != types.BoundSend && im.Bound.Kind != types.BoundShare && private(im.Bound.Item) {
				continue
			}
			what := qx.boundString(im.Bound, im.Generics) + " for " + qx.w.tab.TypeString(im.For, im.Generics)
			line := "impl" + qx.genericsString(im.Generics, im.Generics) + " " + what
			if im.Unsafe {
				what, line = "unsafe "+what, "unsafe "+line
			}
			out[m.path+".impl "+what] = []apiFact{{"impl", line, Breaking}}
		}
	}
	return out
}

// checkedToTypes reports whether a check got far enough for its public facts:
// no diagnostic before the types phase and no internal error.
// pkgMods are the package's modules, or a file or directory session's own.
func (qx *queryIndex) pkgMods() []*modNode {
	if qx.s.pkgB != nil {
		return qx.s.pkgB.mods
	}
	return qx.s.nonStdMods()
}

func checkedToTypes(res Result) bool {
	for _, e := range res.Report.Diags {
		if p := e.Diag.Phase; p == diag.PhaseInternal || p < diag.PhaseTypes {
			return false
		}
	}
	return true
}

func genericDecl(d types.Decl) bool {
	switch d := d.(type) {
	case *types.StructDecl:
		return len(d.Generics) > 0
	case *types.EnumDecl:
		return len(d.Generics) > 0
	}
	return false
}

// stdPkgFacts adds std.pkg's exported items to a std comparison:
// the manifest step checks std.pkg alone, in tables of its own, so the
// query index never holds it. Its items are the types a manifest is
// written in. A manifest may leave out a field holding "", [] or false,
// which the manifest step supplies, so a std.pkg struct is read as
// an options type: a field added to one is compatible, a field removed or
// changed breaking. The facts are each item's declaration and fields.
func stdPkgFacts(s *Session, out map[string][]apiFact, meta map[string]apiMeta) {
	ms := s.ms
	if ms == nil || !ms.pkgOK() || ms.pkgMod == nil || ms.pkgMod.mod == nil {
		return
	}
	px := &queryIndex{s: s, w: ms.mw}
	for _, id := range ms.pkgMod.mod.Items {
		it := px.w.items.Get(id)
		name := px.displayName(it)
		if !it.Exported || name == "" {
			continue
		}
		switch it.Kind {
		case types.KindImpl, types.KindTest, types.KindGen, types.KindLiteral, types.KindNone:
			continue
		}
		e := qEntry{id: id, name: name, mod: pkgModule, std: true, module: ms.pkgMod}
		fs := []apiFact{{"declaration", px.declLine(e), Breaking}}
		if sd, ok := it.Decl.(*types.StructDecl); ok {
			var fields []string
			for _, f := range sd.Fields {
				if f.Exported {
					fields = append(fields, px.fieldLine(f, sd.Generics))
				}
			}
			fs = append(fs, apiFact{"fields", strings.Join(fields, "; "), Breaking})
		}
		out[pkgModule+"."+name] = fs
		_, isStruct := it.Decl.(*types.StructDecl)
		meta[pkgModule+"."+name] = apiMeta{mod: pkgModule, options: isStruct}
	}
}
