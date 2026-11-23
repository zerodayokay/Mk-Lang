// Why: a capturing const fn literal may mutate enclosing var locals and
// reborrow captured mut references, so the checker must decide per capture
// whether the environment holds &T, mut T or a reborrowed mut T / mut []T,
// while rejecting non-var mutations, contract mutations and non-Plain moves.
//
// What: CaptureMode (CapShared, CapMut, CapReborrow), captureOrigin,
// envTypeOf, judgeCaptureUses, judgeCapturePath, captureMutErr,
// isCaptureReborrowArg and unrolled capture inspection.
//
// How: after typing a capturing literal's body, judgeCaptureUses inspects
// every capture occurrence (in the body and any concrete fields-loop copies)
// and every nested literal's LitEnv, tracks deref steps along each maximal
// place path to distinguish reborrows from binding writes and shared-ref
// writes, reports E0508 or E0609 where disallowed, and finalizes the
// literal's CaptureMode slice and EnvRef.

package check

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// CaptureMode is how a capturing fn literal holds one captured variable in
// its environment record.
type CaptureMode uint8

const (
	// CapShared captures x: T as &T in the environment record; inside the
	// literal, x is *((*env).x).
	CapShared CaptureMode = iota
	// CapMut captures a var x: T as mut T in the environment record; inside
	// the literal, x is *((*env).x).
	CapMut
	// CapReborrow captures a mut U or mut []U by reborrowing mut *x or
	// mut x[..] directly as a mut U or mut []U field of the environment
	// record; inside the literal, x is (*env).x.
	CapReborrow
)

// captureOrigin describes one captured variable as the enclosing unit sees it.
type captureOrigin struct {
	typ       types.TypeID
	isVar     bool
	reborrow  bool // typ is KMut or KMutSlice (and not from a contract/invariant)
	contract  bool // from a requires/ensures predicate or invariant (&P / &Self)
	declSpan  source.Span
	canAddVar bool // inserting `var ` at declSpan is a proven fix
	isValSelf bool // by-value self receiver (no `var self`)
}

// envTypeOf builds the hidden environment parameter type for caps with modes:
// &R when every capture is CapShared, else mut R with &T_i for CapShared,
// mut T_i for CapMut and T_i (mut U or mut []U) for CapReborrow.
func envTypeOf(tab *types.Table, caps []resolve.Capture, origins []captureOrigin, modes []CaptureMode) types.TypeID {
	fields := make([]types.RecordField, len(caps))
	hasMut := false
	for i, c := range caps {
		t := origins[i].typ
		var ft types.TypeID
		switch modes[i] {
		case CapMut:
			ft = tab.Mut(t)
			hasMut = true
		case CapReborrow:
			ft = t
			hasMut = true
		default:
			ft = tab.Ref(t)
		}
		fields[i] = types.RecordField{Name: c.Name, Type: ft}
	}
	rec := tab.Record(fields)
	if hasMut {
		return tab.Mut(rec)
	}
	return tab.Ref(rec)
}

// capNeed is the strongest mutation requirement observed for one capture.
type capNeed uint8

const (
	needNone capNeed = iota
	needReborrow
	needVar
)

// capView reads per-node tables from either the unit's body or one concrete
// fields-loop copy.
type capView struct {
	u    *unit
	copy *Copy // nil for the unit's main tables
}

func (v capView) typeOf(id ast.NodeID) types.TypeID {
	if v.copy != nil {
		if i := int(id) - int(v.copy.Base); i >= 0 && i < len(v.copy.Types) {
			return v.copy.Types[i]
		}
	}
	return v.u.b.TypeOf(id)
}

func (v capView) useOf(id ast.NodeID) Use {
	if v.copy != nil {
		if i := int(id) - int(v.copy.Base); i >= 0 && i < len(v.copy.Uses) {
			return v.copy.Uses[i]
		}
	}
	return v.u.useOf(id)
}

func (v capView) adjustOf(id ast.NodeID) Adjust {
	if v.copy != nil {
		if i := int(id) - int(v.copy.Base); i >= 0 && i < len(v.copy.Adjust) {
			return v.copy.Adjust[i]
		}
	}
	if a := v.u.b.Adjust; a != nil {
		if i := int(id) - int(v.u.b.Base); i >= 0 && i < len(a) {
			return a[i]
		}
	}
	return Adjust{}
}

// judgeCaptureUses walks a capturing literal's unit after its statements are
// typed: every maximal place path rooted at a capture (and every nested
// literal's LitEnv) is judged by its parent context. Valid mutations choose
// CapMut (for var captures) or CapReborrow (for mut T / mut []T reborrows);
// mutating a non-var capture is E0508 (or E0609 in a contract/invariant);
// moving a non-Plain capture is E0609 move. Finally it computes Captures[i].Mode
// and EnvRef.
func (u *unit) judgeCaptureUses() {
	if !u.hasCaptures || len(u.b.Captures) == 0 {
		return
	}
	needs := make([]capNeed, len(u.b.Captures))
	reported := map[source.Span]bool{}

	type site struct {
		id    *ast.Ident
		chain []ast.Node
	}
	var sites []site
	var stack []ast.Node
	ast.Inspect(u.b.Node, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if id, ok := n.(*ast.Ident); ok {
			sites = append(sites, site{id, append([]ast.Node(nil), stack...)})
		}
		if _, ok := n.(*ast.FnLit); ok && n != u.b.Node {
			return false // a nested literal is its own unit
		}
		stack = append(stack, n)
		return true
	})

	visit := func(v capView, s site) {
		use := v.useOf(s.id.ID())
		if use.Kind != UseLocal || int(use.Local) >= len(u.b.Locals) || u.b.Locals[use.Local].Capture == 0 {
			return
		}
		ci := int(u.b.Locals[use.Local].Capture - 1)
		u.judgeCapturePath(v, s.id, ci, s.chain, needs, reported)
	}
	mainView := capView{u: u}
	for _, s := range sites {
		visit(mainView, s)
		for k := range u.b.Unrolled {
			c := &u.b.Unrolled[k]
			if i := int(s.id.ID()) - int(c.Base); i >= 0 && i < len(c.Types) {
				visit(capView{u: u, copy: c}, s)
			}
		}
	}

	// Propagate mutation needs from nested capturing literals.
	for _, env := range u.b.Envs {
		for k, src := range env.Sources {
			if src.Kind != UseLocal || int(src.Local) >= len(u.b.Locals) || u.b.Locals[src.Local].Capture == 0 {
				continue
			}
			ci := int(u.b.Locals[src.Local].Capture - 1)
			switch env.Modes[k] {
			case CapMut:
				needs[ci] = needVar
			case CapReborrow:
				if needs[ci] < needReborrow {
					needs[ci] = needReborrow
				}
			}
		}
	}

	modes := make([]CaptureMode, len(u.b.Captures))
	for i, nd := range needs {
		switch nd {
		case needVar:
			modes[i] = CapMut
		case needReborrow:
			modes[i] = CapReborrow
		default:
			modes[i] = CapShared
		}
		u.b.Captures[i].Mode = modes[i]
	}
	if ii := u.m.byItem[u.it.ID]; ii != nil && len(ii.Captures) == len(modes) {
		u.b.EnvRef = envTypeOf(u.tab, ii.Captures, u.capOrigins, modes)
	}
}

// peelDerefs peels n reference layers from t, reporting whether any layer
// crossed a shared reference (&T), a mutable reference (mut T), or a raw
// pointer (*T).
func (u *unit) peelDerefs(t types.TypeID, n uint8, crossedShared, crossedMut, crossedRaw *bool) types.TypeID {
	for ; n > 0 && t != types.NoType && t != types.Poison; n-- {
		ty := u.tab.Get(t)
		switch ty.Kind {
		case types.KRef:
			*crossedShared = true
			t = ty.Elem
		case types.KMut:
			*crossedMut = true
			t = ty.Elem
		case types.KRawPtr:
			*crossedRaw = true
			return ty.Elem
		default:
			return t
		}
	}
	return t
}

// judgeCapturePath climbs id's maximal place path through chain and records
// any required mutation mode or reports E0508 / E0609.
func (u *unit) judgeCapturePath(v capView, id *ast.Ident, ci int, chain []ast.Node, needs []capNeed, reported map[source.Span]bool) {
	var p ast.Expr = id
	crossedShared, crossedMut, crossedRaw := false, false, false
	i := len(chain) - 1
climb:
	for ; i >= 0; i-- {
		switch x := chain[i].(type) {
		case *ast.ParenExpr:
			p = x
		case *ast.FieldExpr:
			if x.X != p || v.useOf(x.ID()).Kind != UseField {
				break climb
			}
			u.peelDerefs(v.typeOf(p.ID()), v.adjustOf(p.ID()).Deref, &crossedShared, &crossedMut, &crossedRaw)
			if crossedRaw {
				return
			}
			p = x
		case *ast.IndexExpr:
			if x.X != p || len(x.Args) != 1 {
				break climb
			}
			bt := u.peelDerefs(v.typeOf(p.ID()), v.adjustOf(p.ID()).Deref, &crossedShared, &crossedMut, &crossedRaw)
			if crossedRaw {
				return
			}
			switch u.tab.Kind(bt) {
			case types.KSlice, types.KStr:
				crossedShared = true
			case types.KMutSlice:
				crossedMut = true
			}
			p = x
		case *ast.UnaryExpr:
			if x.Op != ast.TokStar || x.X != p {
				break climb
			}
			pt := v.typeOf(p.ID())
			switch u.tab.Kind(pt) {
			case types.KRawPtr:
				return // a raw pointer's target is unsafe's
			case types.KRef:
				crossedShared = true
			case types.KMut, types.KMutSlice:
				crossedMut = true
			}
			p = x
		default:
			break climb
		}
	}
	if i < 0 {
		return
	}
	var grand, greatGrand ast.Node
	if i > 0 {
		grand = chain[i-1]
	}
	if i > 1 {
		greatGrand = chain[i-2]
	}
	t := v.typeOf(p.ID())

	// recordMut handles a mutating use of p.
	recordMut := func(reason string) {
		if crossedRaw || crossedShared {
			return // raw pointer is unsafe's; shared ref is borrow's E0507
		}
		orig := u.capOrigins[ci]
		if crossedMut && orig.reborrow {
			if orig.contract {
				u.captureContractErr(id, ci, reason, reported)
				return
			}
			if needs[ci] < needReborrow {
				needs[ci] = needReborrow
			}
			return
		}
		if orig.isVar {
			needs[ci] = needVar
			return
		}
		if orig.contract {
			u.captureContractErr(id, ci, reason, reported)
			return
		}
		u.captureImmutableErr(id, ci, reason, reported)
	}

	switch x := chain[i].(type) {
	case *ast.AssignStmt:
		if x.Target == p {
			recordMut("write")
			return
		}
	case *ast.AsmOperand:
		if x.Value == p && x.Kind.IsDirection() && x.Kind != ast.AsmIn {
			recordMut("write")
			return
		}
		if x.Value == p && x.Kind == ast.AsmIn {
			// Fall through to plain/move check.
		}
	case *ast.DestructureStmt:
		return // borrow's backstop judges non-plain field moves
	case *ast.MatchStmt:
		if x.X == p {
			if u.tab.Kind(t) == types.KMut {
				crossedMut = true
				recordMut("mut-borrow")
			}
			return // bindings decide moves in borrow's backstop
		}
	case *ast.UnaryExpr:
		switch x.Op {
		case ast.TokMut:
			recordMut("mut-borrow")
			return
		case ast.TokAmp:
			return
		}
	case *ast.SliceExpr:
		if x.X == p {
			if g, ok := grand.(*ast.UnaryExpr); ok && g.Op == ast.TokMut {
				bt := u.peelDerefs(t, v.adjustOf(p.ID()).Deref, &crossedShared, &crossedMut, &crossedRaw)
				switch u.tab.Kind(bt) {
				case types.KSlice, types.KStr:
					crossedShared = true
				case types.KMutSlice:
					crossedMut = true
				}
				recordMut("mut-borrow")
			}
			return
		}
	case *ast.RefSource:
		if x.X == p {
			if x.Mode == ast.ByMutRef {
				bt := u.peelDerefs(t, v.adjustOf(p.ID()).Deref, &crossedShared, &crossedMut, &crossedRaw)
				switch u.tab.Kind(bt) {
				case types.KSlice, types.KStr:
					crossedShared = true
				case types.KMutSlice:
					crossedMut = true
				}
				recordMut("mut-borrow")
				return
			}
			// `for x in it` over an iterator evaluates `it` by value (copy or move).
			if iu := v.useOf(x.ID()); iu.Kind == UseMethod || iu.Kind == UseIfaceMethod {
				break
			}
			return
		}
	case *ast.FieldExpr:
		if x.X == p {
			var call *ast.CallExpr
			if c, ok := grand.(*ast.CallExpr); ok && c.Fun == ast.Expr(x) {
				call = c
			} else if ix, ok := grand.(*ast.IndexExpr); ok && ix.X == ast.Expr(x) {
				if c, ok := greatGrand.(*ast.CallExpr); ok && c.Fun == ast.Expr(ix) {
					call = c
				}
			}
			if call != nil {
				switch u.receiverModeIn(v, call, p) {
				case types.ByMut:
					bt := u.peelDerefs(t, v.adjustOf(p.ID()).Deref, &crossedShared, &crossedMut, &crossedRaw)
					switch u.tab.Kind(bt) {
					case types.KSlice:
						crossedShared = true
					case types.KMutSlice:
						crossedMut = true
					}
					recordMut("mut-self")
					return
				case types.ByShared:
					return
				}
				// a by-value self: a value position, below
			} else {
				return // a method named without a call (E0609 already) or a member read
			}
		}
	case *ast.CallExpr:
		if x.Fun == p {
			return // a call through a captured fn value reads it
		}
	case *ast.Arg:
		if x.Value == p {
			if u.isCaptureReborrowArg(v, id, p, t, x, grand) {
				crossedMut = true
				recordMut("mut-borrow")
				return
			}
		}
	}
	if t == types.NoType || t == types.Poison || u.c.FactsIn(u.env, t).Kind == types.Plain {
		return // a copy
	}
	if !reported[id.Span()] {
		reported[id.Span()] = true
		u.captureMove(id, ci, t)
	}
}

// isCaptureReborrowArg reports whether passing capture path p of type t as
// argument a of call grand is an implicit reborrow of a whole mut T or
// mut []T capture (matching lower's isReborrow rule), rather than a move.
func (u *unit) isCaptureReborrowArg(v capView, id *ast.Ident, p ast.Expr, t types.TypeID, a *ast.Arg, grand ast.Node) bool {
	k := u.tab.Kind(t)
	if k != types.KMut && k != types.KMutSlice {
		return false
	}
	if unparenExpr(p) != id {
		return false // a field or deref is never implicitly reborrowed
	}
	if v.adjustOf(id.ID()).Kind != AdjNone {
		return false
	}
	call, ok := grand.(*ast.CallExpr)
	if !ok || (u.goCalls != nil && u.goCalls[call.ID()]) {
		return false
	}
	ai := -1
	for j, ca := range call.Args {
		if ca == a {
			ai = j
			break
		}
	}
	if ai < 0 {
		return false
	}
	cu := v.useOf(call.ID())
	switch cu.Kind {
	case UseItem, UseMethod:
		if cu.Item == types.NoItem {
			return u.fnParamReborrows(v.typeOf(call.Fun.ID()), ai, k)
		}
		d, ok := u.c.cfg.Items.Get(cu.Item).Decl.(*types.FuncDecl)
		if !ok {
			return false
		}
		pi := ai
		if cu.Kind == UseMethod && d.Sig.Receiver != types.NoReceiver && isMethodCallFun(v, call.Fun, cu.Kind) {
			pi++
		}
		if pi >= len(d.Sig.Params) {
			return false
		}
		pr := d.Sig.Params[pi]
		return pr.Into == types.NoParam && u.tab.Kind(pr.Type) == k
	case UseIfaceMethod:
		d, ok := u.c.cfg.Items.Get(cu.Item).Decl.(*types.InterfaceDecl)
		if !ok || int(cu.Index) >= len(d.Methods) {
			return false
		}
		sig := &d.Methods[cu.Index]
		pi := ai + 1
		if pi >= len(sig.Params) {
			return false
		}
		pr := sig.Params[pi]
		return pr.Into == types.NoParam && u.tab.Kind(pr.Type) == k
	case UseFnValueCall:
		return u.fnParamReborrows(v.typeOf(call.Fun.ID()), ai, k)
	}
	return false
}

func (u *unit) fnParamReborrows(fnType types.TypeID, ai int, k types.TypeKind) bool {
	ft := u.tab.Get(fnType)
	if ft.Kind != types.KFn || ft.Fn == nil || ai >= len(ft.Fn.Params) {
		return false
	}
	fp := ft.Fn.Params[ai]
	return fp.Into == types.NoParam && u.tab.Kind(fp.Type) == k
}

func isMethodCallFun(v capView, fun ast.Expr, callKind UseKind) bool {
	if ix, ok := fun.(*ast.IndexExpr); ok {
		fun = ix.X
	}
	fe, ok := fun.(*ast.FieldExpr)
	return ok && v.useOf(fe.ID()).Kind == callKind
}

// receiverModeIn is receiverMode reading through v (the main tables or a
// concrete fields-loop copy).
func (u *unit) receiverModeIn(v capView, c *ast.CallExpr, p ast.Expr) types.Receiver {
	switch v.adjustOf(p.ID()).Kind {
	case AdjRefMut:
		return types.ByMut
	case AdjRefShared:
		return types.ByShared
	case AdjCopyThrough:
		return types.ByValue
	}
	use := v.useOf(c.ID())
	if use.Item == types.NoItem {
		return types.ByShared
	}
	if d, ok := u.c.cfg.Items.Get(use.Item).Decl.(*types.FuncDecl); ok && d.Sig.Receiver != types.NoReceiver {
		return d.Sig.Receiver
	}
	return types.ByShared
}

// captureImmutableErr emits E0508 (in the flow group, matching borrow's
// E0508) when a literal mutates a captured variable that is not declared var.
func (u *unit) captureImmutableErr(id *ast.Ident, ci int, reason string, reported map[source.Span]bool) {
	if reported[id.Span()] {
		return
	}
	reported[id.Span()] = true
	orig := u.capOrigins[ci]
	n := u.c.cfg.Interner.Str(u.b.Captures[ci].Name)
	var action string
	switch reason {
	case "write":
		action = "assigns `" + n + "`"
	case "mut-self":
		action = "calls a `mut self` method on `" + n + "`"
	default:
		action = "borrows `" + n + "` `mut`"
	}
	d := u.err(diag.E0508, id.Span(), "the literal "+action+", which is not `var` and cannot be borrowed `mut`").
		Fact("captured", diag.TextList(n)).Text("reason", reason)
	if orig.declSpan.File != source.NoFile {
		d.Loc("declared_at", orig.declSpan)
		if orig.canAddVar {
			d.Fix(diag.Fix{
				Class: diag.Proven,
				Edits: []diag.Edit{{Span: source.Span{File: orig.declSpan.File, Start: orig.declSpan.Start, End: orig.declSpan.Start}, Text: "var "}},
				Why:   "declare it var so it may be assigned again or borrowed mut",
			})
		}
	}
	if orig.isValSelf {
		d.Alt("move it into a local first: `var s := self`")
	}
}

// captureContractErr emits E0609 when a literal inside a contract predicate
// or invariant attempts to mutate a captured parameter, result or self.
func (u *unit) captureContractErr(id *ast.Ident, ci int, reason string, reported map[source.Span]bool) {
	if reported[id.Span()] {
		return
	}
	reported[id.Span()] = true
	n := u.c.cfg.Interner.Str(u.b.Captures[ci].Name)
	d := u.err(diag.E0609, id.Span(), "the literal mutates `"+n+"`, which a contract or invariant reads through a shared reference").
		Fact("captured", diag.TextList(n)).Text("reason", reason).In(diag.PhaseTypes)
	captureAlts(d, false)
}
