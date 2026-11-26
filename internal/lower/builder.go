// Why: every body is lowered by one builder, which owns the state the
// lowering of each construct shares: the MIR body being built, the scope
// stack, the temporary frames and the loop and cleanup context.
//
// What: builder and newBuilder; scope, tempFrame, loopCtx and deferRec; the
// emit helpers (emit, assign, term, gotoBlock, switchOn, newTemp,
// hiddenLocal) and scope and frame management (openScope, closeScope,
// pushFrame, popFrame, declare).
//
// How: IDs are handed out in creation order and every statement carries a
// span. A temporary lives in the frame of the statement that made it and
// ends when that frame is popped; header temporaries of a loop end on every
// edge that leaves an iteration.

package lower

import (
	"mk/internal/ast"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// scopeKind is what a lexical scope is.
type scopeKind uint8

const (
	scopePlain scopeKind = iota
	scopeFn
	scopeLoopBody
	scopeElseHandler // the handler of an else on an E!T call or value
	scopeOptHandler  // the handler of an else on a ?T value
	scopeStmtScope   // the block of scope s := e
	scopeCleanup     // a defer or onerr body
	scopeTypeTestYes // the yes branch of if T is X
)

type scope struct {
	kind      scopeKind
	locals    []mir.LocalID
	cleanups  []mir.DeferID
	tempDepth int
	scopeVar  mir.LocalID
	// tryScope marks the block of a try scope; tryAt is the statement's
	// span, the site of its error edge.
	tryScope bool
	tryAt    source.Span
	narrowed []narrowRec
	// ttParam and ttType are a typeTestYes scope's T and X.
	ttParam uint32
	ttType  types.TypeID
	// remap is the narrowing in force for code of this scope: the
	// enclosing one, or for a typeTestYes scope the map with its own
	// narrowed locals. A cleanup copy run on an exit edge lowers under its
	// own scope's remap, so a defer outside if T is X reads x, not x'
	// (only the branch's uses are narrowed).
	remap map[mir.LocalID]mir.LocalID
}

type narrowRec struct {
	orig, narrowed mir.LocalID
	writeBack      bool
}

type tempFrame struct {
	temps []mir.LocalID
	span  source.Span
}

type loopCtx struct {
	id    mir.LoopID
	label source.Symbol
	cont  mir.BlockID
	exit  mir.BlockID
	depth int // len(scopes) outside the loop body
	// hdr are header temporaries that live through the body and end on
	// every edge leaving an iteration (for cond, for x := e).
	hdr []mir.LocalID
	// broken is set when a break targets the loop, continued when a
	// continue does.
	broken, continued bool
}

type deferRec struct {
	node     *ast.DeferStmt
	consumes mir.LocalID
}

type builder struct {
	// armDest maps each arm block of the value constructs being lowered to
	// its destination; armStmts reads it.
	armDest map[*ast.Block]mir.Place
	// dropped is the expression an expression statement is lowering, whose
	// value is dropped: `o.Must()` and `o else { }` on a ?T move a payload
	// that is not plain into a T temporary only then (okTemp), so a bound
	// value takes no extra move.
	dropped ast.Expr
	// valueDests counts the value constructs being lowered into each local
	// (a binding's own local): typeTest does not narrow a destination
	// that no arm has assigned yet.
	valueDests map[mir.LocalID]int
	cfg        *Config
	in         Input
	b          *mir.Body
	tab        *types.Table
	kind       BodyKind
	sig        *types.Signature
	cur        mir.BlockID
	// origin and deferID are given to new blocks: a cleanup copy's blocks
	// carry the copy's origin.
	origin  mir.BlockOrigin
	deferID mir.DeferID

	locals   []mir.LocalID // per ast.NodeID - nodeBase of a binding Ident
	nodeBase ast.NodeID
	spans    map[source.Span]mir.SpanID
	scopes   []scope
	temps    []tempFrame
	loops    []loopCtx
	defers   []deferRec
	borrowed []bool
	// callDest, when set, is the destination of the next emitCall (the
	// LocalScope local of a scope statement).
	callDest mir.LocalID
	// tail, when set, makes the next emitCall a TermTailCall (become);
	// emitCall clears it before any nested call is lowered.
	tail bool
	// placeMut is set while the place of an assignment target or a mut
	// borrow is lowered: an implementer index then calls ItemsMut. It holds
	// on the place's spine only (root, field, deref, the indexed base): an
	// index expression, a slice bound or a value base is a read, so
	// subExpr and tempOf clear it.
	placeMut bool
	// remap maps a local to its narrowed copy inside an if T is X branch.
	remap map[mir.LocalID]mir.LocalID
	// counters are the enclosing range loops whose counter indexes with no
	// bounds check.
	counters []counterCtx
	// laterInPlace are the index expressions and slice bounds of the place
	// being built that run after its base (ys[i][Bump(mut i)]: Bump(mut i)
	// while ys[i] is lowered). An index local that one of them may write is
	// read into a slot (indexLocal). They are in reverse
	// evaluation order: an outer level's come before an inner one's, and a
	// slice's hi before its lo.
	laterInPlace []ast.Expr
	// preEval holds the later index expressions and slice bounds that ran
	// before an inner implementer's Items or ItemsMut borrow
	// (evalLaterInPlace), by node: their own level uses the local instead
	// of lowering them again.
	preEval map[ast.NodeID]mir.LocalID
	// trace caches traceInfo (trace.go): nil until the first error return.
	trace *traceHooks
	// env is a capturing literal's hidden environment parameter,
	// NoLocal in every other body.
	env mir.LocalID
}

func newBuilder(cfg *Config, in Input) *builder {
	sig := in.Sig()
	root := in.Root()
	bl := &builder{cfg: cfg, in: in, tab: cfg.Types, kind: in.Kind(), sig: sig,
		cur: mir.NoBlock, origin: mir.OriginBody, deferID: mir.NoDefer, callDest: mir.NoLocal,
		spans: map[source.Span]mir.SpanID{}, env: mir.NoLocal}
	lo, n := in.Nodes()
	bl.nodeBase = lo
	bl.locals = make([]mir.LocalID, n)
	for i := range bl.locals {
		bl.locals[i] = mir.NoLocal
	}
	if sig.Result == types.Poison || sig.Result == types.NoType {
		source.ICEf(root.Span(), "lower: a signature whose result is Poison with no diagnostic (check gap)")
	}
	for _, p := range sig.Params {
		if p.Type == types.Poison || p.Type == types.NoType {
			source.ICEf(p.Span, "lower: a parameter of Poison type with no diagnostic (check gap)")
		}
	}
	bl.b = mir.NewBody(in.Item(), root.Span(), sig.ReturnType(cfg.Types))
	bl.spans[root.Span()] = 0
	for _, p := range sig.Params {
		l := bl.b.AddParam(p.Type, p.Name, bl.span(p.Span))
		bl.b.Locals[l].Var = p.Var // a var parameter may be assigned and borrowed mut
	}
	if env := in.EnvRef(); env != types.NoType {
		// A capturing literal's hidden environment parameter, after the
		// declared ones; never var.
		bl.env = bl.b.AddParam(env, cfg.Interner.Intern("env"), bl.span(root.Span()))
	}
	return bl
}

// capturePlace is capture i of a capturing literal's body: (*env).x_i when
// direct (a reborrowed mut T or mut []T), else *((*env).x_i).
func (bl *builder) capturePlace(i uint32, direct bool, at source.Span) mir.Place {
	if bl.env == mir.NoLocal {
		source.ICEf(at, "lower: a capture in a body with no environment parameter")
	}
	rec := bl.deref(bl.b.Place(bl.env), at)
	ft := bl.fieldType(bl.b.PlaceType(rec), i)
	if ft == types.NoType {
		source.ICEf(at, "lower: capture %d past the environment record", i)
	}
	fld := bl.field(rec, i, ft)
	if direct {
		return fld
	}
	return bl.deref(fld, at)
}

// span returns the SpanID of s, deduplicated in first-use order.
func (bl *builder) span(s source.Span) mir.SpanID {
	if id, ok := bl.spans[s]; ok {
		return id
	}
	id := bl.b.AddSpan(s)
	bl.spans[s] = id
	return id
}

func (bl *builder) newBlock() mir.BlockID { return bl.b.AddBlock(bl.origin, bl.deferID) }

// open reports whether there is a current block to append to.
func (bl *builder) open() bool { return bl.cur != mir.NoBlock }

func (bl *builder) emit(st mir.Statement) {
	if bl.cur == mir.NoBlock {
		return // dead code after a diverging point
	}
	blk := &bl.b.Blocks[bl.cur]
	blk.Stmts = append(blk.Stmts, st)
}

// assign emits p = rv. A struct construction is followed by its
// invariant checks on the destination (right after the StAssign of
// every RvAggregate{AggStruct}, whatever the destination).
func (bl *builder) assign(p mir.Place, rv mir.Rvalue, at source.Span) {
	if rv.Kind == mir.RvRef || rv.Kind == mir.RvSlice {
		bl.markBorrowed(rv.Place.Local)
	}
	bl.emit(mir.Statement{Kind: mir.StAssign, Place: p, Rvalue: rv, Span: bl.span(at)})
	if rv.Kind == mir.RvAggregate && rv.Agg == mir.AggStruct {
		bl.invariantCheck(rv.Type, p, at)
	}
}

func (bl *builder) markBorrowed(l mir.LocalID) {
	for int(l) >= len(bl.borrowed) {
		bl.borrowed = append(bl.borrowed, false)
	}
	bl.borrowed[l] = true
}

func (bl *builder) scopeEnd(l mir.LocalID, at source.Span) {
	bl.emit(mir.Statement{Kind: mir.StScopeEnd, Local: l, Span: bl.span(at)})
}

// term ends the current block.
func (bl *builder) term(t mir.Terminator, at source.Span) {
	if bl.cur == mir.NoBlock {
		return
	}
	t.Span = bl.span(at)
	bl.b.Blocks[bl.cur].Term = t
	bl.cur = mir.NoBlock
}

func (bl *builder) gotoBlock(target mir.BlockID, at source.Span) {
	bl.term(mir.Terminator{Kind: mir.TermGoto, Target: target}, at)
}

// switchOn ends the block with a switch on op.
func (bl *builder) switchOn(op mir.Operand, cases []mir.SwitchCase, def mir.BlockID, at source.Span) {
	bl.term(mir.Terminator{Kind: mir.TermSwitch, On: op, Cases: bl.b.AddCases(cases...), Default: def}, at)
}

// boolSwitch branches on a bool operand: false to f, true to t (one shape
// everywhere: test the false case, default to true).
func (bl *builder) boolSwitch(op mir.Operand, t, f mir.BlockID, at source.Span) {
	bl.switchOn(op, []mir.SwitchCase{{Value: 0, Target: f}}, t, at)
}

// newTemp makes a temporary of type t in the innermost frame.
func (bl *builder) newTemp(t types.TypeID, at source.Span) mir.LocalID {
	if t == types.NoType || t == types.Poison {
		source.ICEf(at, "lower: a temporary of no type (check left %v)", t)
	}
	l := bl.b.AddLocal(mir.Local{Type: t, Kind: mir.LocalTemp, Loop: mir.NoLoop, Span: bl.span(at)})
	if n := len(bl.temps); n > 0 {
		bl.temps[n-1].temps = append(bl.temps[n-1].temps, l)
	}
	return l
}

// hiddenLocal is a temporary in no frame (the caller ends it).
func (bl *builder) hiddenLocal(t types.TypeID, kind mir.LocalKind, at source.Span) mir.LocalID {
	return bl.b.AddLocal(mir.Local{Type: t, Kind: kind, Loop: mir.NoLoop, Span: bl.span(at)})
}

func (bl *builder) pushFrame(at source.Span) { bl.temps = append(bl.temps, tempFrame{span: at}) }

// popFrame ends the innermost frame's temporaries, in reverse creation
// order, when control reaches the frame's end.
func (bl *builder) popFrame() {
	f := bl.temps[len(bl.temps)-1]
	bl.temps = bl.temps[:len(bl.temps)-1]
	if bl.open() {
		for i := len(f.temps) - 1; i >= 0; i-- {
			bl.scopeEnd(f.temps[i], f.span)
		}
	}
}

// dropFrame discards the innermost frame without ending its temporaries
// (the caller ended them on every edge).
func (bl *builder) dropFrame() []mir.LocalID {
	f := bl.temps[len(bl.temps)-1]
	bl.temps = bl.temps[:len(bl.temps)-1]
	return f.temps
}

func (bl *builder) openScope(k scopeKind) {
	bl.scopes = append(bl.scopes, scope{kind: k, tempDepth: len(bl.temps), scopeVar: mir.NoLocal, remap: bl.remap})
}

// closeScope is the fall-through exit of the innermost scope.
func (bl *builder) closeScope(at source.Span) {
	if bl.open() {
		bl.exitScopes(len(bl.scopes)-1, exitFall, at)
	}
	bl.scopes = bl.scopes[:len(bl.scopes)-1]
}

// declare makes the local a binding Ident declares and registers it in the
// innermost scope.
func (bl *builder) declare(id *ast.Ident) mir.LocalID {
	bd := bl.in.Binding(id.ID())
	if !bd.OK {
		source.ICEf(id.Span(), "lower: binding %s has no local", bl.cfg.Interner.Str(id.Name))
	}
	return bl.declareAs(id, bd.Type, bd.Var)
}

func (bl *builder) declareAs(id *ast.Ident, t types.TypeID, isVar bool) mir.LocalID {
	if t == types.NoType || t == types.Poison {
		source.ICEf(id.Span(), "lower: binding %s of no type (check gap)", bl.cfg.Interner.Str(id.Name))
	}
	l := bl.b.AddLocal(mir.Local{Type: t, Kind: mir.LocalUser, Name: id.Name, Var: isVar, Loop: mir.NoLoop, Span: bl.span(id.Span())})
	bl.bindLocal(id, l)
	s := &bl.scopes[len(bl.scopes)-1]
	s.locals = append(s.locals, l)
	return l
}

// bindLocal records l as the MIR local of the binding id.
func (bl *builder) bindLocal(id *ast.Ident, l mir.LocalID) {
	i := int(id.ID()) - int(bl.nodeBase)
	if i < 0 || i >= len(bl.locals) {
		source.ICEf(id.Span(), "lower: binding node %d outside the item's range [%d, %d)", id.ID(), bl.nodeBase, int(bl.nodeBase)+len(bl.locals))
	}
	bl.locals[i] = l
}

// localOf is the MIR local of a binding node.
func (bl *builder) localOf(binding ast.NodeID, at source.Span) mir.LocalID {
	if i := int(binding) - int(bl.nodeBase); i >= 0 && i < len(bl.locals) && bl.locals[i] != mir.NoLocal {
		return bl.locals[i]
	}
	source.ICEf(at, "lower: use of a binding before its declaration")
	return mir.NoLocal
}

// facts are the ownership facts of t.
func (bl *builder) facts(t types.TypeID, at source.Span) types.Facts {
	if t == types.Poison {
		// A Poison type reached here without passing typeOf: a field or
		// payload declared with a type check left Poison silently (a
		// struct field `[!0]u8`, FuzzLower d1f8f93246ace338).
		source.ICEf(at, "lower: a value of Poison type with no diagnostic (check gap)")
	}
	f, ok := bl.in.Facts(t)
	if !ok {
		source.ICEf(at, "lower: no facts for %s", bl.tab.TypeString(t, bl.sig.Generics))
	}
	return f
}

func (bl *builder) plain(t types.TypeID, at source.Span) bool {
	switch bl.tab.Kind(t) {
	case types.KVoid, types.KNever, types.KBool, types.KChar, types.KInt, types.KIntPtr, types.KFloat, types.KStr, types.KRef, types.KSlice, types.KRawPtr, types.KFn:
		return true
	}
	return bl.facts(t, at).Kind == types.Plain
}

// typeOf is the checked type of an expression; NoType or Poison is an ICE.
func (bl *builder) typeOf(e ast.Node) types.TypeID {
	t := bl.in.Type(e.ID())
	if t == types.Poison {
		source.ICEf(e.Span(), "lower: %T is Poison with no diagnostic (check gap)", e)
	}
	if t == types.NoType {
		source.ICEf(e.Span(), "lower: %T has no type (check gap)", e)
	}
	return t
}

func use(o mir.Operand) mir.Rvalue { return mir.Rvalue{Kind: mir.RvUse, A: o} }
