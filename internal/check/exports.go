// Why: an importer sees an exported declaration's types and kind; a private
// name in it would be unreachable, and an exported struct whose private
// fields make it a view or linear would change kind without its keyword.
//
// What: exportChecks, which runs privateNames (E0412: a private name in an
// exported declaration) and exportedKind (E0410: an exported struct that
// needs `view` or `linear`); exportedTypeNodes, kindWord, isAtomic.
//
// How: exportedTypeNodes lists the type nodes importers see; the kind is
// computed twice, from the exported fields and from all fields, and each
// needed keyword is demanded on its own. It runs once the facts are final.

package check

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// exportChecks runs E0412 and E0410 over the module's exported items, in
// item order. It runs after the facts are final (E0410 needs kinds).
func (m *Module) exportChecks() {
	m.forEachItem(func(it *types.Item) {
		if !it.Exported {
			return
		}
		n, fx := m.nodeOf(it.ID)
		m.privateNames(it, fx, n)
		if d, ok := it.Decl.(*types.StructDecl); ok {
			if sd, ok := n.(*ast.StructDecl); ok {
				m.exportedKind(it, fx, sd, d)
			}
		}
	})
}

// exportedTypeNodes lists the type nodes of an exported declaration that
// its importers see.
func exportedTypeNodes(n ast.Node) []ast.Node {
	var out []ast.Node
	add := func(xs ...ast.Node) {
		for _, x := range xs {
			if x != nil {
				out = append(out, x)
			}
		}
	}
	tparams := func(tps *ast.TypeParams) {
		if tps == nil {
			return
		}
		for _, tp := range tps.Params {
			if tp == nil {
				continue
			}
			for _, b := range tp.Bounds {
				add(b)
			}
			if tp.Type != nil {
				add(tp.Type)
			}
		}
	}
	sig := func(s *ast.Signature) {
		if s == nil {
			return
		}
		tparams(s.TypeParams)
		for _, p := range s.Params {
			if p != nil && p.Type != nil {
				add(p.Type)
			}
		}
		if s.Result != nil {
			add(s.Result)
		}
	}
	switch n := n.(type) {
	case *ast.StructDecl:
		tparams(n.TypeParams)
		for _, mem := range n.Members {
			if f, ok := mem.(*ast.FieldMember); ok && f.Export && f.Type != nil {
				add(f.Type)
			}
		}
	case *ast.EnumDecl:
		tparams(n.TypeParams)
		for _, v := range n.Variants {
			if v == nil {
				continue
			}
			for _, f := range v.Fields {
				if f != nil && f.Type != nil {
					add(f.Type)
				}
			}
		}
	case *ast.ErrorDecl:
		for _, f := range n.Fields {
			if f != nil && f.Type != nil {
				add(f.Type)
			}
		}
	case *ast.InterfaceDecl:
		tparams(n.TypeParams)
		for _, s := range n.Methods {
			sig(s)
		}
	case *ast.AliasDecl:
		tparams(n.TypeParams)
		if n.Type != nil {
			add(n.Type)
		}
	case *ast.ConstDecl:
		if n.Type != nil {
			add(n.Type)
		}
	case *ast.GlobalDecl:
		if n.Type != nil {
			add(n.Type)
		}
	case *ast.FuncDecl:
		sig(n.Sig)
	case *ast.ExternDecl:
		sig(n.Sig)
	}
	return out
}

// privateNames is E0412: one diagnostic per (exported item, private
// type) at the first place the private name is written. Main is exempt.
func (m *Module) privateNames(it *types.Item, fx *fileIndex, n ast.Node) {
	c := m.c
	if it.Kind == types.KindFunc && it.Name == source.SymMain && it.Owner == types.NoItem {
		return
	}
	if it.Kind == types.KindFunc && it.Owner != types.NoItem && c.cfg.Items.Get(it.Owner).Kind == types.KindImpl {
		return
	}
	seen := map[types.ItemID]bool{}
	for _, tn := range exportedTypeNodes(n) {
		ast.Inspect(tn, func(x ast.Node) bool {
			if x == nil {
				return false
			}
			id := x.ID()
			if int(id) >= len(fx.info.Refs) {
				return true
			}
			r := fx.info.Refs[id]
			if r.Kind != resolve.RefItem {
				return true
			}
			p := c.cfg.Items.Get(types.ItemID(r.A))
			if p.Module != m.ID || p.Exported || seen[p.ID] {
				return true
			}
			switch p.Kind {
			case types.KindStruct, types.KindEnum, types.KindError, types.KindInterface, types.KindAlias:
			default:
				return true
			}
			seen[p.ID] = true
			name := c.cfg.Interner.Str(p.Name)
			what := "export " + name
			if p.Kind == types.KindError {
				what = "export error " + name
			}
			m.Diags = append(m.Diags, diag.New(diag.E0412, x.Span(), "the exported `"+c.cfg.Interner.Str(it.Name)+"` names the private `"+name+"`").
				Text("type", name).Text("item", c.cfg.Interner.Str(it.Name)).Loc("declared_at", p.Span).Alt(what))
			return true
		})
	}
}

// kindWord is a struct's (kind, carrier, loanInterior) under one choice of fields.
type kindWord struct {
	kind         types.Kind
	carrier      bool
	loanInterior bool
}

// exportedKind is E0410: the kind, carrier and loan-interior-writability of an
// exported struct from its exported fields and from all of them, each with its
// keywords; a field that mentions a type parameter counts as Plain, no carrier
// and no loan interior. `view`, `mut view` and `linear` are each required on
// their own.
func (m *Module) exportedKind(it *types.Item, fx *fileIndex, n *ast.StructDecl, d *types.StructDecl) {
	c := m.c
	// A compiler-known item takes its kind and carrier bit from its identity,
	// so its private fields never change
	// either answer: no E0410 for Io, Alloc, Heap, Cell, Mutex, Chan,
	// raw.Atomic or Scope.
	if k := c.known; it.Module != types.NoModule && c.cfg.Items.Module(it.Module).Std {
		switch it.ID {
		case k.Io, k.Alloc, k.Heap, k.Cell, k.Mutex, k.Chan, k.SpinCore, k.Atomic, k.Scope:
			return
		}
	}
	floor := kindWord{}
	if d.Linear {
		floor.kind = types.Linear
	} else if d.View {
		floor.kind = types.Unique
	}
	floor.carrier = d.View
	floor.loanInterior = d.MutView
	all, exp := floor, floor
	// unique: some field is Unique (not Linear): `view` is asked of
	// "carrier or unique" and `linear` of "linear", so a linear field
	// alone needs only `linear`.
	unique := false
	var changing []string
	for _, f := range d.Fields {
		w := kindWord{}
		if f.Type != types.Poison && c.closed(f.Type) {
			x := c.Facts(f.Type)
			w = kindWord{x.Kind, x.Carrier, x.LoanInterior}
		}
		// A private field of the std-internal `std.raw.Atomic` counts
		// as exported, so `Mutex`, `AtomicInt` and `AtomicBool` are unique
		// and not views.
		shown := f.Exported || c.isAtomic(f.Type)
		unique = unique || !shown && w.kind == types.Unique
		all.kind = max(all.kind, w.kind)
		all.carrier = all.carrier || w.carrier
		all.loanInterior = all.loanInterior || w.loanInterior
		if shown {
			exp.kind = max(exp.kind, w.kind)
			exp.carrier = exp.carrier || w.carrier
			exp.loanInterior = exp.loanInterior || w.loanInterior
		} else if w.kind != types.Plain || w.carrier || w.loanInterior {
			changing = append(changing, c.cfg.Interner.Str(f.Name))
		}
	}
	needMutView := !d.MutView && all.loanInterior && !exp.loanInterior
	needLinear := !d.Linear && all.kind == types.Linear && exp.kind != types.Linear
	var needs []string
	if needLinear {
		needs = append(needs, "linear")
	}
	if needMutView {
		needs = append(needs, "mut view")
	} else if !d.View && (all.carrier && !exp.carrier || unique && exp.kind == types.Plain) {
		needs = append(needs, "view")
	}
	if len(needs) == 0 {
		return
	}
	words := ""
	for _, w := range needs {
		words += w + " "
	}
	dg := diag.New(diag.E0410, it.Span, "the exported struct `"+c.cfg.Interner.Str(it.Name)+"` must say `"+words[:len(words)-1]+"`: its private fields make it so").
		Fact("needs", diag.TextList(needs...)).Fact("fields", diag.TextList(changing...))
	if needMutView && d.View {
		// `view` is already present: add `mut ` before it (and `linear `
		// before `view` too when both are needed).
		if at, ok := structToken(fx, n, ast.TokView); ok {
			ins := "mut "
			if needLinear {
				ins = "linear mut "
			}
			dg.Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: at, Text: ins}}, Why: "the private fields decide the struct's kind"})
		}
	} else if at, ok := structToken(fx, n, ast.TokStruct); ok {
		dg.Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: at, Text: words}}, Why: "the private fields decide the struct's kind"})
	}
	m.Diags = append(m.Diags, dg)
}

// structToken is the empty span before the first token of kind k in n.
func structToken(fx *fileIndex, n *ast.StructDecl, k ast.TokenKind) (source.Span, bool) {
	toks := fx.file.Tokens
	for i := n.Toks.First; i < n.Toks.End && int(i) < len(toks); i++ {
		if toks[i].Kind == k {
			return source.Span{File: fx.info.FileID, Start: toks[i].Start, End: toks[i].Start}, true
		}
	}
	return source.Span{}, false
}

// isAtomic reports whether t is `std.raw.Atomic[..]`
// (KnownItems.Atomic).
func (c *Checker) isAtomic(t types.TypeID) bool {
	if c.known.Atomic == types.NoItem || t == types.Poison || t == types.NoType {
		return false
	}
	ty := c.cfg.Types.Get(t)
	return ty.Kind == types.KNamed && ty.Item == c.known.Atomic
}
