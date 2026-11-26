// Why: stage 6 types every expression of a body, pushing an expected type
// down where one is known and reporting a mismatch once, at the
// expression that causes it.
//
// What: unit (the checking context of one stage-6 unit), newUnit, expr and
// exprInner (the bidirectional checker), assignable and widens, mismatchExpr
// (E0301; reborrowHint and plainText for a `mut T` met by a `&T`),
// literal, name (a bare name's meaning), needsBound (E0703).
//
// How: expr checks e against want and records e's own type; a mismatch is
// E0301 at e. The only implicit conversion is widening an error value to a
// superset. Poison is assignable both ways, so one error does not cascade.

package check

import (
	"math"
	"strconv"
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// unit is the checking context of one stage-6 unit: its Body, its
// generic environment and the resolve locals its names index.
type unit struct {
	m   *Module
	c   *Checker
	tab *types.Table
	b   *Body
	// targetFolded marks the nodes whose bool constant comes from a Target
	// comparison (targetfold.go); only those feed &&, || and !
	// folds, so other constant conditions lower as they did.
	targetFolded map[ast.NodeID]bool
	// litVals holds the value of each integer operator over constants the
	// body has folded (litfold.go), for the operator around it.
	litVals map[ast.NodeID]fval
	it      *types.Item
	fx      *fileIndex
	x       *tctx
	env     *Env
	sig     *types.Signature
	// fmtA is 1 + the generic slot of the record parameter A when the unit
	// is a function of the Print shape, or a fn literal inside one, whose
	// Fields[Format] env drops; fmtF is the local of that
	// function's const format f (0 in a literal, or for none).
	fmtA int32
	fmtF LocalID
	// result is what a return value must be: Sig.Result, or NoType where
	// there is no signature (a const or global initializer).
	result types.TypeID
	rb     *resolve.Body
	// declLocal maps a binding's declaring Ident to its local.
	declLocal map[ast.NodeID]LocalID
	// orWhole holds the expressions of the body that stand whole (a
	// binding's, assignment's, return's or `_ =`'s value, an argument, a
	// parenthesised expression), built at the first removed `.Or`.
	orWhole map[ast.Expr]bool
	// loops are the enclosing for loops; deferAt is len(loops) at the start
	// of the enclosing defer or onerr body, -1 outside one.
	loops   []loopCtx
	deferAt int
	// dropAt is the expression of the statement being checked whose value
	// is dropped (an expression statement, a discard): an else there needs
	// no value. discardAt is dropAt when the statement is a discard
	// (`_ = e`), a value position.
	dropAt    ast.Expr
	discardAt ast.Expr
	// returnAt is the operand of the `return` being checked, and punAt the
	// name of the record shorthand field being checked (reborrowHint).
	returnAt ast.Expr
	punAt    ast.Expr
	// defers caches bodyDefers: 0 not yet looked, 1 no, 2 yes.
	defers uint8
	// loopPat is set while a for head's destructured variable is checked:
	// its E0305 offers no `..`, which the loop head refuses.
	loopPat bool
	// narrow is the generic arguments of the current `if T is X` branch
	// (identity but for the narrowed parameters), nil outside one.
	narrow []types.GenericArg
	// unsafeDepth counts the enclosing unsafe blocks; scopes are the
	// enclosing scope bindings.
	unsafeDepth int
	scopes      []scopeCtx
	// inContract is set while a requires, ensures or invariant predicate is
	// checked.
	inContract bool
	// scopeInit is the initializer of the scope statement being checked,
	// the one place a call may produce a Scope value.
	scopeInit ast.Expr
	// remap maps resolve's locals to their fresh copies inside a concrete
	// fields-loop copy; copyAt is that copy's index in Body.Unrolled (-1
	// outside one); unrolled counts the nodes checked in copies.
	remap    map[LocalID]LocalID
	copyAt   int32
	unrolled int
	// scratch holds the tables fields-loop copies are checked into, one per
	// nesting depth; copyDepth is the current depth (fields.go).
	scratch   []*scratch
	copyDepth int
	// targetOne is the `Target` name under the field access being checked
	// (`Target.os`), which reads that one field; any other use of `Target`
	// reads them all (universe.go).
	targetOne ast.NodeID
	// cFnStore is the field an `=` assigns, whose function pointer is
	// written, not read out of C (cfnptr.go).
	cFnStore ast.NodeID
	// constLit marks the FnLit nodes written as a const argument of a call
	// that is not s.go's whose parameter is of fn type;
	// constNotFn those whose const parameter is of any other type (a type
	// parameter's), where a literal may not capture and a const fn
	// parameter may not be passed on; goCalls the calls under s.go; capBase
	// the first capture local of a capturing literal's unit.
	constLit   Bits
	constNotFn Bits
	goCalls    map[ast.NodeID]bool
	// goRecv is, for a method call under s.go, the type its receiver
	// passes to self (callee.recvPassed): goExpr judges that one
	// crossing.
	goRecv      map[ast.NodeID]types.TypeID
	capBase     LocalID
	capOrigins  []captureOrigin
	hasCaptures bool
	eager       bool
	// naming is the fn value whose requires valueRequires judges, nil
	// elsewhere: a restate demand there names no callee.
	naming ast.Node
}

// newUnit prepares the unit of item it: the Body over its node range, its
// signature's generics and its parameters as the first locals.
func (m *Module) newUnit(it *types.Item) *unit {
	return m.newUnitBase(it)
}

// newUnitWithCaptures prepares a capturing literal unit checked eagerly from
// its enclosing unit's literalCaptures with origins already typed.
func (m *Module) newUnitWithCaptures(it *types.Item, ii *resolve.ItemInfo, origins []captureOrigin) *unit {
	u := m.newUnitBase(it)
	u.eager = true
	u.initCaptures(ii, origins)
	if len(u.b.Locals) > 0 {
		u.b.Locals[0].Type = u.result
	}
	return u
}

// newUnitWithFallbackCaptures prepares a capturing literal unit whose
// enclosing unit did not produce an environment (Poison captures).
func (m *Module) newUnitWithFallbackCaptures(it *types.Item, ii *resolve.ItemInfo, rec *captureRec) *unit {
	u := m.newUnitBase(it)
	u.waitCaptures(ii, rec)
	if len(u.b.Locals) > 0 {
		u.b.Locals[0].Type = u.result
	}
	return u
}

func (m *Module) newUnitBase(it *types.Item) *unit {
	c := m.c
	n, fx := m.nodeOf(it.ID)
	b := newBody(it.ID, fx, n)
	u := &unit{m: m, c: c, tab: c.cfg.Types, b: b, it: it, fx: fx, result: types.NoType, declLocal: map[ast.NodeID]LocalID{}, deferAt: -1, copyAt: -1}
	var gens []types.GenericParam
	if fd, ok := it.Decl.(*types.FuncDecl); ok {
		b.Sig = &fd.Sig
		u.sig = &fd.Sig
		u.result = fd.Sig.Result
		gens = fd.Sig.Generics
		gens = c.withSliceImplicit(it, gens)
		if len(gens) > 0 {
			b.Generics = gens
		}
	}
	u.env = c.NewEnv(gens)
	u.env.view = m
	u.x = &tctx{m: m, fx: fx, item: it, gens: gens, rec: recorder{rs: &b.Reads}, sink: &b.Diags, self: types.Poison}
	u.x.unitEnv = func() *Env { return u.env }
	u.x.poisoned = b.poisonRead
	if _, ok := n.(*ast.FnLit); ok {
		u.literalNarrow()
	}
	u.printShapeEnv(it)
	if u.sig != nil && u.sig.Receiver != types.NoReceiver && len(u.sig.Params) > 0 {
		self := u.sig.Params[0].Type
		if k := u.tab.Kind(self); k == types.KRef || k == types.KMut {
			self = u.tab.Get(self).Elem
		}
		u.x.self = self
	}
	if ii := m.byItem[it.ID]; ii != nil && ii.Body != nil {
		u.rb = ii.Body
		b.Locals = make([]Local, len(ii.Body.Locals))
		for i := 1; i < len(ii.Body.Locals); i++ {
			rl := ii.Body.Locals[i]
			l := Local{Name: rl.Name, Type: types.NoType, Var: rl.Var, Node: rl.Decl}
			if (rl.Kind == resolve.LocalParam || rl.Kind == resolve.LocalSelf) && u.sig != nil && i <= len(u.sig.Params) {
				p := u.sig.Params[i-1]
				l.Type, l.Param, l.Span = p.Type, types.ParamIndex(i), p.Span
			}
			b.Locals[i] = l
			if rl.Decl != ast.NoNode {
				u.declLocal[rl.Decl] = LocalID(i)
			}
		}
	}
	if _, ok := b.Node.(*ast.Contract); ok && u.sig != nil {
		u.predicateLocals()
	}
	if len(b.Locals) > 0 {
		b.Locals[0].Type = u.result
	}
	return u
}

// err reports a diagnostic of the unit.
func (u *unit) err(code diag.Code, at source.Span, msg string) *diag.Diagnostic {
	d := diag.New(code, at, msg)
	u.b.Diags = append(u.b.Diags, d)
	return d
}

func (u *unit) str(t types.TypeID) string { return u.tab.TypeString(t, u.x.gens) }

func (u *unit) ref(n ast.Node) resolve.Ref { return u.x.ref(n) }

// setType records a node's type.
func (u *unit) setType(n ast.Node, t types.TypeID) { u.b.Types[u.b.slot(n.ID())] = t }

// expr checks e against the expected type want (NoType for none) and
// records its type. A mismatch is E0301 at e; the recorded type is
// e's own.
func (u *unit) expr(e ast.Expr, want types.TypeID) types.TypeID {
	if e == nil {
		return types.Poison
	}
	t := u.exprInner(e, want)
	if t == types.NoType {
		t = types.Poison
	}
	u.setType(e, t)
	// An E!T value is never stored or passed (E0802): arguments,
	// construction fields and some(..) (fallibleArgs), array and record
	// elements, a repeat value.
	switch x := e.(type) {
	case *ast.CallExpr:
		u.fallibleArgs(x)
	case *ast.ArrayLit:
		for _, el := range x.Elems {
			u.storedFallible(el)
		}
	case *ast.RecordLit:
		for _, f := range x.Fields {
			if f != nil {
				u.storedFallible(f.Value)
			}
		}
	case *ast.RepeatLit:
		u.storedFallible(x.Value)
	}
	if ce, ok := e.(*ast.CallExpr); ok && ast.Expr(ce) != u.scopeInit && t != types.Poison && u.isScope(t) {
		u.scopeE1003(e) // E1003: a Scope made anywhere but a scope initializer
	}
	if want != types.NoType && !u.assignable(want, t) && u.widens(want, t) {
		*u.b.adjust(e.ID()) = Adjust{Kind: AdjWiden, To: want}
		return t
	}
	if want != types.NoType && !u.assignable(want, t) && u.fnConvert(e, want, t) {
		return t
	}
	if want != types.NoType && !u.assignable(want, t) {
		if ty := u.tab.Get(t); ty.Kind == types.KErrorUnion && u.assignable(want, ty.Elem) {
			u.unhandled(e, t)
		} else {
			u.mismatchExpr(e, want, t)
		}
	}
	return t
}

// assignable is the one relation an expected type imposes: equal types,
// Poison on either side, never into anything. Error-set widening is widens'.
func (u *unit) assignable(want, got types.TypeID) bool {
	return want == got || want == types.Poison || got == types.Poison || got == types.Never
}

// widens reports whether an error value of type got widens to want, the
// one implicit conversion: an error set into a superset, E!T
// into E'!T with E a subset of E'.
func (u *unit) widens(want, got types.TypeID) bool {
	w, g := u.tab.Get(want), u.tab.Get(got)
	switch {
	case w.Kind == types.KErrorSet && g.Kind == types.KErrorSet:
		return u.tab.Subset(g.Errors, w.Errors)
	case w.Kind == types.KErrorUnion && g.Kind == types.KErrorUnion:
		return w.Elem == g.Elem && u.tab.Subset(g.Errors, w.Errors)
	}
	return false
}

func (u *unit) mismatch(at source.Span, want, got types.TypeID) *diag.Diagnostic {
	return u.err(diag.E0301, at, "expected `"+u.str(want)+"`, found `"+u.str(got)+"`").
		Text("expected", u.str(want)).Text("found", u.str(got))
}

// mismatchExpr is E0301 for expression e. Where a `mut U` meets a `&U` it
// names the shared reborrow (reborrowHint). Where a `mut []T` meets a `[]T`
// it offers `e[..]`, the shared view no conversion gives, parenthesised
// unless e is a name, a field, an index, a call or already parenthesised.
// Compiles: a shared re-slice can meet another loan of the call, so mk
// check's recompile settles it.
func (u *unit) mismatchExpr(e ast.Expr, want, got types.TypeID) {
	d := u.mismatch(e.Span(), want, got)
	w, g := u.tab.Get(want), u.tab.Get(got)
	if w.Kind == types.KRef && g.Kind == types.KMut && w.Elem == g.Elem {
		u.reborrowHint(d, e)
		return
	}
	if w.Kind != types.KSlice || g.Kind != types.KMutSlice || w.Elem != g.Elem {
		return
	}
	sp := e.Span()
	if sp.File == source.NoFile || sp.End <= sp.Start {
		return
	}
	end := source.Span{File: sp.File, Start: sp.End, End: sp.End}
	edits := []diag.Edit{{Span: end, Text: "[..]"}}
	switch e.(type) {
	case *ast.Ident, *ast.FieldExpr, *ast.IndexExpr, *ast.CallExpr, *ast.ParenExpr:
	default:
		edits = []diag.Edit{{Span: source.Span{File: sp.File, Start: sp.Start, End: sp.Start}, Text: "("}, {Span: end, Text: ")[..]"}}
	}
	d.Fix(diag.Fix{Class: diag.Compiles, Edits: edits, Why: "a `mut []T` is passed where a `[]T` is expected as its shared view `xs[..]`"})
}

// reborrowHint completes E0301 for a `mut T` where a `&T` is expected, which
// no conversion bridges: a try line names the shared reborrow `&*e`, or `&x`
// for a loan `mut x`, and a fix writes it. The fix is a prefix on a name, a
// field, an index, a call, a `try` or a `*`; `name: &*` on a record
// shorthand field; `&*(e)` around an `else` or any other form. A
// parenthesised expression gets the try line alone, since the expression
// inside it carries the fix. The fix is proven only where no loan can meet
// the new shared one (reborrowProven); elsewhere it is compiles.
func (u *unit) reborrowHint(d *diag.Diagnostic, e ast.Expr) {
	sp := e.Span()
	class := diag.Compiles
	if u.reborrowProven(e) {
		class = diag.Proven
	}
	ok := sp.File != source.NoFile && sp.End > sp.Start
	if x, isLoan := e.(*ast.UnaryExpr); isLoan && x.Op == ast.TokMut {
		t, plain := u.plainText(x.X)
		if plain {
			d.Alt("write `&" + t + "`, a shared loan, for `mut " + t + "`")
		} else {
			d.Alt("write `&` for `mut`, a shared loan")
			t = "x"
		}
		xs := x.X.Span()
		if ok && xs.File == sp.File && sp.Start < xs.Start {
			d.Fix(diag.Fix{Class: class, Edits: []diag.Edit{{Span: source.Span{File: sp.File, Start: sp.Start, End: xs.Start}, Text: "&"}},
				Why: "a shared loan `&" + t + "` where a `&T` is expected, for the exclusive `mut " + t + "`"})
		}
		return
	}
	at := source.Span{File: sp.File, Start: sp.Start, End: sp.Start}
	end := source.Span{File: sp.File, Start: sp.End, End: sp.End}
	t, plain := u.plainText(e)
	var edits []diag.Edit
	switch x := e.(type) {
	case *ast.Ident, *ast.FieldExpr, *ast.IndexExpr, *ast.CallExpr:
		edits = []diag.Edit{{Span: at, Text: "&*"}}
		if e == u.punAt {
			edits[0].Text = t + ": &*" // the shorthand `(n)` is `(n: n)`
			t = t + ": &*" + t
		}
	case *ast.ParenExpr:
	case *ast.UnaryExpr:
		if x.Op == ast.TokTry || x.Op == ast.TokStar {
			edits = []diag.Edit{{Span: at, Text: "&*"}}
		} else {
			edits = []diag.Edit{{Span: at, Text: "&*("}, {Span: end, Text: ")"}}
		}
	default:
		edits = []diag.Edit{{Span: at, Text: "&*("}, {Span: end, Text: ")"}}
	}
	if el, isElse := e.(*ast.ElseExpr); isElse {
		if o, p := u.plainText(el.X); p {
			d.Alt("write `&*(" + o + " else { .. })`, a shared reborrow of the `mut` reference")
		} else {
			d.Alt("reborrow the `mut` reference shared: write `&*(` before it and `)` after it")
		}
	} else if plain && e == u.punAt {
		d.Alt("write `" + t + "`, a shared reborrow of the `mut` reference")
	} else if plain {
		d.Alt("write `&*" + t + "`, a shared reborrow of the `mut` reference")
	} else if len(edits) == 2 {
		d.Alt("reborrow the `mut` reference shared: write `&*(` before it and `)` after it")
	} else {
		d.Alt("reborrow the `mut` reference shared: write `&*` before it")
	}
	if len(edits) == 0 || !ok {
		return
	}
	shown := "`&*` before it"
	if plain && e == u.punAt {
		shown = "`" + t + "`"
	} else if plain {
		shown = "`&*" + t + "`"
	} else if len(edits) == 2 {
		shown = "`&*(` and `)` around it"
	}
	d.Fix(diag.Fix{Class: class, Edits: edits, Why: "a `mut T` where a `&T` is expected is reborrowed shared: " + shown})
}

// reborrowProven reports whether the reborrow of e is certain to check where
// the mismatch did: e is the whole operand of a `return`, so nothing after it
// in the statement uses the reference; the body has no `defer` or `onerr`,
// which would run after the result exists; and e reads through a parameter
// or is a call or `try` result, so the reborrow points at no local's own
// storage. A loan `mut x` is never proven: x may be a local.
func (u *unit) reborrowProven(e ast.Expr) bool {
	if e != u.returnAt || u.bodyDefers() {
		return false
	}
	var rooted func(e ast.Expr) bool
	rooted = func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.CallExpr:
			return true
		case *ast.UnaryExpr:
			return x.Op == ast.TokTry || (x.Op == ast.TokStar && rooted(x.X))
		case *ast.FieldExpr:
			return rooted(x.X)
		case *ast.IndexExpr:
			return rooted(x.X)
		case *ast.Ident:
			r := u.ref(x)
			if r.Kind == resolve.RefParam {
				return types.ItemID(r.B) == u.it.ID // a parameter of this unit
			}
			if r.Kind != resolve.RefLocal {
				return false
			}
			id := u.local(LocalID(r.A))
			return int(id) < len(u.b.Locals) && u.b.Locals[id].Param != 0
		}
		return false
	}
	return rooted(e)
}

// bodyDefers reports whether the unit's body holds a `defer` or `onerr`
// anywhere, nested literals included.
func (u *unit) bodyDefers() bool {
	if u.defers == 0 {
		u.defers = 1
		if u.b.Node != nil {
			ast.Inspect(u.b.Node, func(n ast.Node) bool {
				if _, ok := n.(*ast.DeferStmt); ok {
					u.defers = 2
				}
				return u.defers != 2
			})
		}
	}
	return u.defers == 2
}

// plainText spells a name or a chain of fields on a name, the forms a try
// line quotes; anything else is not spelled.
func (u *unit) plainText(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		return u.c.cfg.Interner.Str(x.Name), true
	case *ast.FieldExpr:
		if s, ok := u.plainText(x.X); ok {
			return s + "." + u.c.cfg.Interner.Str(x.Name.Name), true
		}
	}
	return "", false
}

// exprInner dispatches on the expression form.
func (u *unit) exprInner(e ast.Expr, want types.TypeID) types.TypeID {
	switch e := e.(type) {
	case *ast.IfStmt, *ast.MatchStmt:
		// A value construct: an if or match used as an expression.
		return u.valueConstruct(e, want)
	case *ast.BasicLit:
		return u.literal(e, false, want)
	case *ast.Ident:
		return u.name(e, want)
	case *ast.ParenExpr:
		t := u.expr(e.X, want)
		if u.isTargetFolded(e.X) {
			// `if (Target.arch == Arch.X64)`: lower reads the condition's
			// own node.
			if x, ok := u.boolConstOf(e.X); ok {
				u.setTargetConst(e, x)
			}
		}
		return t
	case *ast.UnaryExpr:
		t := u.unary(e, want)
		if e.Op == ast.TokMinus || e.Op == ast.TokTilde {
			u.litFold(e, t)
		}
		return t
	case *ast.BinaryExpr:
		t := u.binary(e, want)
		u.litFold(e, t)
		return t
	case *ast.CallExpr:
		return u.call(e, want)
	case *ast.FieldExpr:
		return u.fieldExpr(e, want)
	case *ast.RecordLit:
		return u.recordLit(e, want)
	case *ast.ArrayLit:
		return u.arrayLit(e, want)
	case *ast.RepeatLit:
		return u.repeatLit(e, want)
	case *ast.FnLit:
		return u.fnLit(e, want)
	case *ast.IndexExpr:
		return u.index(e, want)
	case *ast.SliceExpr:
		return u.slice(e)
	case *ast.ElseExpr:
		return u.elseExpr(e)
	case *ast.GoExpr:
		return u.goExpr(e)
	case *ast.QuoteExpr:
		return u.quoteExpr(e)
	}
	// Any other form (BadExpr after a parse error) is Poison, which no rule
	// reports on.
	return types.Poison
}

// literal types a literal, neg when a `-` directly before it was
// folded in. The value goes into Body.Consts.
func (u *unit) literal(e *ast.BasicLit, neg bool, want types.TypeID) types.TypeID {
	tab := u.tab
	switch e.Kind {
	case ast.TokInt:
		v, ok := parseIntLit(e.Raw)
		t := types.Int
		if want != types.NoType && want != types.Poison {
			wt := tab.Get(want)
			switch {
			case wt.IsInteger():
				t = want
			case wt.Kind == types.KParam:
				u.needsBound(e.Span(), want, "an integer literal")
				return types.Poison
			}
		}
		ty := tab.Get(t)
		bits := uint(ty.Bits)
		if ty.Kind == types.KIntPtr {
			bits = 64 // int and uint are 64-bit on every supported target
		}
		lit := e.Raw
		if neg {
			lit = "-" + lit
		}
		if !ok || !intFits(v, neg, bits, ty.Signed) {
			u.err(diag.E0303, e.Span(), "`"+lit+"` does not fit `"+u.str(t)+"`").Text("value", lit).Text("type", u.str(t))
			return t
		}
		x := v
		if neg {
			x = -v
		}
		u.b.setConst(e.ID(), u.c.scalarConst(t, int(bits/8), x))
		return t
	case ast.TokFloat:
		t := types.F64
		if want == types.F32 {
			t = types.F32
		} else if want != types.NoType && want != types.Poison && tab.Kind(want) == types.KParam {
			u.needsBound(e.Span(), want, "a float literal")
			return types.Poison
		}
		raw := strings.ReplaceAll(e.Raw, "_", "")
		if neg {
			raw = "-" + raw
		}
		if t == types.F32 {
			// The parser rejects only f64 overflow; only check knows the
			// literal is an f32 (E0303).
			f, err := strconv.ParseFloat(raw, 32)
			if err != nil || math.IsInf(f, 0) {
				lit := e.Raw
				if neg {
					lit = "-" + lit
				}
				u.err(diag.E0303, e.Span(), "`"+lit+"` does not fit `f32`").Text("value", lit).Text("type", "f32")
				return t
			}
			u.b.setConst(e.ID(), u.c.scalarConst(t, 4, uint64(math.Float32bits(float32(f)))))
		} else if f, err := strconv.ParseFloat(raw, 64); err == nil {
			u.b.setConst(e.ID(), u.c.scalarConst(t, 8, math.Float64bits(f)))
		}
		return t
	case ast.TokChar:
		if r, ok := litChar(e.Raw); ok {
			u.b.setConst(e.ID(), u.c.scalarConst(types.Char, 4, uint64(r)))
		}
		return types.Char
	case ast.TokByte:
		if v, ok := litChar(e.Raw); ok {
			u.b.setConst(e.ID(), u.c.scalarConst(types.U8, 1, uint64(v)))
		}
		return types.U8
	case ast.TokStr, ast.TokRawStr, ast.TokMultiStr:
		if bs, ok := litString(e.Kind, e.Raw); ok {
			u.b.setConst(e.ID(), u.c.strConst(bs))
		}
		return types.Str
	case ast.TokByteStr:
		t := tab.Slice(types.U8)
		if bs, ok := litString(e.Kind, e.Raw); ok {
			u.b.setConst(e.ID(), u.c.bytesConst(t, bs))
		}
		return t
	}
	return types.Poison
}

// needsBound is E0703: an operation on a value of a type parameter
// that its bounds do not give.
func (u *unit) needsBound(at source.Span, t types.TypeID, op string) {
	u.err(diag.E0703, at, "`"+u.str(t)+"` has no bound that allows "+op).Text("param", u.str(t)).Text("operation", op)
}

// name types a bare name: a local, a parameter, a const,
// a global, a function used as a value, or a universe value.
func (u *unit) name(e ast.Expr, want types.TypeID) types.TypeID {
	c := u.c
	r := u.ref(e)
	switch r.Kind {
	case resolve.RefLocal:
		id := u.local(LocalID(r.A))
		if int(id) < len(u.b.Locals) {
			*u.b.use(e.ID()) = Use{Kind: UseLocal, Local: id}
			if t := u.b.Locals[id].Type; t != types.NoType {
				return u.nt(t)
			}
		}
		return types.Poison
	case resolve.RefCapture:
		// A capture is this literal unit's capture local.
		if types.ItemID(r.B) == u.it.ID && int(r.A) < len(u.b.Captures) {
			id := u.capBase + LocalID(r.A)
			*u.b.use(e.ID()) = Use{Kind: UseLocal, Local: id}
			return u.nt(u.b.Locals[id].Type)
		}
		return types.Poison
	case resolve.RefField:
		return u.invariantField(e, r)
	case resolve.RefParam, resolve.RefResult:
		if pl, ok := u.predicateParam(e, r); ok {
			return pl
		}
		if r.Kind == resolve.RefResult {
			return types.Poison
		}
		// A parameter of this unit is its local of the same number:
		// a function's from its signature, a test's sys set by run.
		id := LocalID(r.A)
		if types.ItemID(r.B) == u.it.ID && id >= 1 && int(id) < len(u.b.Locals) && u.b.Locals[id].Type != types.NoType {
			*u.b.use(e.ID()) = Use{Kind: UseLocal, Local: id}
			return u.nt(u.b.Locals[id].Type)
		}
		return types.Poison
	case resolve.RefItem:
		it := c.cfg.Items.Get(types.ItemID(r.A))
		switch d := c.declOfItem(it).(type) {
		case *types.ConstDecl:
			*u.b.use(e.ID()) = Use{Kind: UseConst, Item: it.ID}
			u.x.rec.signature(it.Key)
			t := d.Type
			if t == types.NoType && u.m.d != nil && it.Module == u.m.ID {
				// Stage 5: an untyped const is typed on demand, from another
				// const's typing or from an early initializer checked on
				// demand or in the sweep. A const whose typing is in
				// progress is a cycle (E0909) and stays NoType.
				t = u.m.typeConst(it)
			}
			if t == types.NoType {
				// A const with no type: an untyped const whose initializer
				// failed to type, or a member of a typing cycle. This body
				// reports nothing for the Poison; the driver judges lower's
				// E0000 at this read (Body.PoisonReads, accounting).
				u.b.poisonRead(it.ID, e.Span())
				return types.Poison
			}
			if d.HasValue && it.Module != u.m.ID {
				u.x.rec.value(it.Key)
				u.b.setConst(e.ID(), d.Value)
			}
			return t
		case *types.GlobalDecl:
			*u.b.use(e.ID()) = Use{Kind: UseGlobal, Item: it.ID}
			u.x.rec.signature(it.Key)
			if d.Extern {
				u.externVarRead(e, it.ID) // E1101
			}
			return d.Type
		case *types.FuncDecl:
			// A non-generic function is a fn value; a generic one
			// needs inference from the expected type.
			*u.b.use(e.ID()) = Use{Kind: UseItem, Item: it.ID}
			u.x.rec.signature(it.Key)
			u.genUse(e, &d.Sig, "fn value")
			u.cpuUse(e, &d.Sig, true) // cpu-value
			u.rawNeedsSSE(e, it.ID)   // E1106
			u.nakedValue(e, d)        // naked-value
			u.unsafeValue(e, it.ID)
			if sigHasPoison(&d.Sig) || u.constParamFnValue(e, &d.Sig) {
				return types.Poison
			}
			if len(d.Sig.Generics) > 0 {
				return u.genericFnValue(e, it, &d.Sig, nil, want)
			}
			u.valueRequires(e, &d.Sig, 0) // a clause over consts, as at a call
			return u.fnTypeOf(&d.Sig)
		case *types.ErrorDecl:
			return u.bareError(e, it, d)
		}
		switch it.Kind {
		case types.KindStruct, types.KindEnum, types.KindAlias, types.KindInterface:
			return u.notValue(e, it.Kind.String())
		}
		return types.Poison // a value item whose declaration failed reported there
	case resolve.RefTypeParam:
		// A const type parameter used as a run-time value.
		if int(r.A) < len(u.x.gens) && u.x.gens[r.A].Const {
			return u.x.gens[r.A].Type
		}
		return u.notValue(e, "type parameter")
	case resolve.RefUniverse:
		switch source.Symbol(r.A) {
		case source.SymTrue, source.SymFalse:
			v := uint64(0)
			if source.Symbol(r.A) == source.SymTrue {
				v = 1
			}
			u.b.setConst(e.ID(), u.c.scalarConst(types.Bool, 1, v))
			return types.Bool
		case source.SymNone:
			if want != types.NoType && want != types.Poison && u.tab.Kind(want) == types.KOptional {
				return want
			}
			if want == types.Poison {
				return types.Poison
			}
			u.err(diag.E0711, e.Span(), "`none` needs an expected optional type").Text("param", "T")
			return types.Poison
		case source.SymTarget:
			return u.target(e)
		case source.SymSome:
			// The constructor named without its call (`A(){some}`): not a
			// value, and never a silent Poison.
			u.err(diag.E0304, e.Span(), "`some` takes one value: write `some(x)`").
				Text("signature", "some(x: T): ?T").Int("expected", 1).Int("found", 0)
			return types.Poison
		case source.SymFields:
			return u.fieldsOutsideLoop(e, "fields")
		case source.SymOk, source.SymErr:
			return types.Poison // resolve's E0201: pattern heads only
		}
		// Assert, Panic, Cast, SizeOf, a bound name and the like named
		// without a call: never a silent Poison (lower's invariant).
		return u.notValue(e, "universe name")
	case resolve.RefPrim:
		// `int` alone is a type, not a value; `int(x)` is a conversion and
		// never reaches here (`A(){int}`).
		return u.notValue(e, "type")
	case resolve.RefSelfType:
		return u.notValue(e, "type")
	}
	return types.Poison
}

// fnTypeOf is the fn type of a non-generic signature, with its flows
// positionally.
func (u *unit) fnTypeOf(s *types.Signature) types.TypeID {
	ps := make([]types.FnParam, len(s.Params))
	for i, p := range s.Params {
		ps[i] = types.FnParam{Type: p.Type, Into: p.Into}
	}
	return u.tab.Fn(types.FnType{Params: ps, Result: s.Result, Errors: s.Errors, Sources: s.Flows.Sources, Fields: s.Flows.Fields, Stores: s.Flows.Stores, Ends: s.Flows.Ends})
}

// isUntyped reports whether e is a numeric literal whose type comes from
// the other operand: a literal, a negated literal, a parenthesized
// one.
func isUntyped(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.BasicLit:
		return e.Kind == ast.TokInt || e.Kind == ast.TokFloat
	case *ast.ParenExpr:
		return isUntyped(e.X)
	case *ast.UnaryExpr:
		return e.Op == ast.TokMinus && isUntyped(e.X)
	}
	return false
}

// defaultType is the type an untyped operand gets on its own: int for an
// integer literal, f64 for a float.
func defaultType(e ast.Expr) types.TypeID {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == ast.TokFloat {
			return types.F64
		}
		return types.Int
	case *ast.ParenExpr:
		return defaultType(e.X)
	case *ast.UnaryExpr:
		return defaultType(e.X)
	}
	return types.Int
}
