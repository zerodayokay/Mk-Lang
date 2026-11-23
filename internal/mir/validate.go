// Why: every stage that writes MIR (lower, mono) can break its invariants,
// and a broken body would fail far away in borrow or codegen; the
// validator catches it at the stage that wrote it.
//
// What: Env (the signatures and facts validation may read), Body.Validate,
// and the validator's checks of locals, spans, operands, statements,
// terminators, calls and environment arguments.
//
// How: one pass over the body; the first violation panics inside the
// validator with its invariant code (M1 to M13, listed in Validate's doc
// comment) and is returned as an error naming the block and statement. A spawned call has no error edge;
// exit kinds follow the onerr and E0411 rules of ExitKind.

package mir

import (
	"fmt"
	"strconv"
	"strings"

	"mk/internal/types"
)

// Env is what Validate reads besides the body. Types is required; the rest
// may be nil, and the invariants that need them are then skipped.
type Env struct {
	Types *types.Table
	// Sig is the body's own signature (M1: local 0 and the parameters).
	Sig *types.Signature
	// Callee returns the signature of a directly called function (M5, M10).
	Callee func(types.ItemID) *types.Signature
	// Facts returns the ownership facts of a concrete type (M8, M10).
	Facts func(types.TypeID) (types.Facts, bool)
	// Known names the compiler-known items; with it M9 checks that a spawn
	// through a reference names a Scope.
	Known *types.KnownItems
}

// Validate checks the invariants M1 to M13 (M13: StAsm) in one pass over
// locals, blocks, projections and loops, O(size of body). It returns the
// first violation, naming the block, statement and invariant; lower and the
// corpus turn it into an ICE at the body's span. Debug builds, go test and
// mk corpus run it on every body.
//
// The invariant codes that start every error:
//
//   - M1: local 0 is the return local, then the parameters in order, then
//     the other locals; their types agree with the signature.
//   - M2: there is an entry block, every ID and range is in bounds, every
//     kind is known, and an instance body holds nothing generic.
//   - M3: each projection step is admitted by the type before it.
//   - M4: an assignment's place has its rvalue's type.
//   - M5: calls: an error edge exactly when the callee can fail, and the
//     destination, argument and tail-call types.
//   - M6: returns: exit kinds and new-error marks.
//   - M7: cleanup blocks are per-exit copies, with no try inside one.
//   - M8: an onerr guard's value is linear and its run block an onerr copy.
//   - M9: loop holder and element loans, joins of scope locals, spawn
//     scopes and fields loops.
//   - M10: argument modes: a reborrow of a mut, a move only for a non-plain
//     type, a copy only for a plain one.
//   - M11: no inference or poison type survives the checker.
//   - M12: the span table exists and every span ID is in it.
//   - M13: inline asm (StAsm) operands, options and clobbers.
func (b *Body) Validate(env *Env) error {
	v := validator{b: b, env: env, tab: env.Types}
	return v.run()
}

type validator struct {
	b   *Body
	env *Env
	tab *types.Table
	at  string
	// curBlock is the block being checked (M9's element-loan clause), and
	// curStmt the statement's index in it (M13's noreturn rule).
	curBlock BlockID
	curStmt  int
	// tyMemo holds typeFlags per type; loopBlocks each loop's blocks.
	tyMemo     map[types.TypeID]uint8
	loopBlocks map[LoopID][]bool
}

type violation struct{ msg string }

func (v *validator) fail(inv, format string, args ...any) {
	panic(violation{fmt.Sprintf("%s: %s: %s", inv, v.at, fmt.Sprintf(format, args...))})
}

func (v *validator) run() (err error) {
	defer func() {
		if r := recover(); r != nil {
			if vi, ok := r.(violation); ok {
				err = fmt.Errorf("mir.Validate: %s", vi.msg)
			} else {
				err = fmt.Errorf("mir.Validate: M2: %s: malformed body: %v", v.at, r)
			}
		}
	}()
	b := v.b
	// M12: spans.
	v.at = "body"
	if len(b.Spans) == 0 {
		v.fail("M12", "no span table")
	}
	// M1 and M11: locals.
	if len(b.Locals) == 0 || b.Locals[0].Kind != LocalReturn {
		v.fail("M1", "local 0 is not the return local")
	}
	if int(b.ParamCount) >= len(b.Locals) && b.ParamCount > 0 {
		v.fail("M1", "%d params but %d locals", b.ParamCount, len(b.Locals))
	}
	for i, l := range b.Locals {
		v.at = fmt.Sprintf("local _%d", i)
		v.typeOK(l.Type)
		v.span(l.Span)
		switch {
		case i == 0:
		case i <= int(b.ParamCount):
			if l.Kind != LocalParam || l.Param != types.ParamIndex(i) {
				v.fail("M1", "local %d must be parameter %d", i, i)
			}
		default:
			switch l.Kind {
			case LocalUser, LocalTemp, LocalScope:
			case LocalLoopHolder:
				v.loop(l.Loop)
			default:
				v.fail("M1", "kind %d after the parameters", l.Kind)
			}
		}
	}
	if s := v.env.Sig; s != nil {
		v.at = "signature"
		if b.Locals[0].Type != s.ReturnType(v.tab) {
			v.fail("M1", "return local has %s, signature returns %s", v.ts(b.Locals[0].Type), v.ts(s.ReturnType(v.tab)))
		}
		// Trailing hidden environment parameters follow the declared
		// ones.
		if int(b.ParamCount) < len(s.Params) {
			v.fail("M1", "%d param locals, signature has %d", b.ParamCount, len(s.Params))
		}
		for i, p := range s.Params {
			if b.Locals[i+1].Type != p.Type {
				v.fail("M1", "param %d has %s, signature says %s", i+1, v.ts(b.Locals[i+1].Type), v.ts(p.Type))
			}
		}
		for i := len(s.Params) + 1; i <= int(b.ParamCount); i++ {
			if !IsEnvType(v.tab, b.Locals[i].Type) {
				v.fail("M1", "param %d past the signature's %d has %s, not an environment type &R or mut R", i, len(s.Params), v.ts(b.Locals[i].Type))
			}
		}
	}
	// M9 part: loops.
	holders := make([]int, len(b.Loops))
	for i, lp := range b.Loops {
		v.at = fmt.Sprintf("loop %d", i)
		v.block(lp.Header)
		v.block(lp.Exit)
		v.span(lp.Span)
		if lp.Holder != NoLocal {
			v.local(lp.Holder)
		}
		if lp.FieldsOf != types.NoType {
			v.fieldsLoop(LoopID(i), lp)
		}
	}
	for i, d := range b.Defers {
		v.at = fmt.Sprintf("defer %d", i)
		v.span(d.Span)
	}
	if len(b.Blocks) == 0 {
		v.at = "body"
		v.fail("M2", "no entry block")
	}
	preds := b.PredecessorCounts()
	for bi := range b.Blocks {
		bl := &b.Blocks[bi]
		if bl.Origin != OriginBody {
			v.at = fmt.Sprintf("bb%d", bi)
			if int(bl.Defer) >= len(b.Defers) {
				v.fail("M2", "defer %d out of range", bl.Defer)
			}
			if (bl.Origin == OriginOnerr) != b.Defers[bl.Defer].Onerr {
				v.fail("M7", "block origin and defer kind disagree")
			}
		}
	}
	v.cleanupCopies(preds)
	for bi := range b.Blocks {
		bl := &b.Blocks[bi]
		v.curBlock = BlockID(bi)
		for si := range bl.Stmts {
			v.at = fmt.Sprintf("bb%d[%d]", bi, si)
			v.curStmt = si
			v.stmt(&bl.Stmts[si], holders)
		}
		v.at = fmt.Sprintf("bb%d terminator", bi)
		v.term(bl, preds)
	}
	for i, lp := range b.Loops {
		v.at = fmt.Sprintf("loop %d", i)
		if lp.Holder != NoLocal && holders[i] != 1 {
			v.fail("M9", "holder loan made %d times, want once", holders[i])
		}
	}
	return nil
}

// ts spells a type for a violation message. A fn type with sources, stores
// or ends gets them appended (TypeString drops them), so two fn types that
// differ only in flows read differently: `fn(&Holder): str [from $1]`.
func (v *validator) ts(t types.TypeID) string {
	s := v.tab.TypeString(t, nil)
	if t == types.NoType || v.tab.Kind(t) != types.KFn {
		return s
	}
	fn := v.tab.Get(t).Fn
	var fl []string
	for _, src := range fn.Sources {
		if src.Behind {
			fl = append(fl, "B($"+strconv.Itoa(int(src.Param))+")")
		} else {
			fl = append(fl, "from $"+strconv.Itoa(int(src.Param)))
		}
	}
	for _, st := range fn.Stores {
		fl = append(fl, "*$"+strconv.Itoa(int(st.From))+" into $"+strconv.Itoa(int(st.Into)))
	}
	for _, e := range fn.Ends {
		fl = append(fl, "ends $"+strconv.Itoa(int(e)))
	}
	if len(fl) == 0 {
		return s
	}
	return s + " [" + strings.Join(fl, ", ") + "]"
}

// fits is the type relation of M4 and M5 where a value lands in a place, a
// parameter or a destination: the same type, or a fn value whose flows the
// target fn type covers (E0608). Only the top level is relaxed, as
// in check's conversion.
func (v *validator) fits(value, target types.TypeID) bool {
	return value == target || v.tab.FnCovers(value, target)
}

func (v *validator) span(s SpanID) {
	if int(s) >= len(v.b.Spans) {
		v.fail("M12", "span %d out of range %d", s, len(v.b.Spans))
	}
}

func (v *validator) local(l LocalID) {
	if int(l) >= len(v.b.Locals) {
		v.fail("M2", "local %d out of range", l)
	}
}

func (v *validator) block(id BlockID) {
	if int(id) >= len(v.b.Blocks) {
		v.fail("M2", "block %d out of range", id)
	}
}

func (v *validator) loop(id LoopID) {
	if int(id) >= len(v.b.Loops) {
		v.fail("M2", "loop %d out of range", id)
	}
}

func (v *validator) rng(r Range, n int, what string) {
	if uint64(r.Start)+uint64(r.Len) > uint64(n) {
		v.fail("M2", "%s range %d+%d out of %d", what, r.Start, r.Len, n)
	}
}

// operand checks an operand outside a call's argument list and returns its
// type. M10: a whole non-plain local is always read by Move; call
// arguments are judged by their ArgMode instead (argOperand).
func (v *validator) operand(o Operand) types.TypeID {
	t := v.argOperand(o)
	if f := v.env.Facts; f != nil && o.Kind == OpCopy && o.Place.Proj.Len == 0 {
		if facts, ok := f(t); ok && facts.Kind != types.Plain {
			v.fail("M10", "copy of _%d, whose type %s is %v", o.Place.Local, v.ts(t), facts.Kind)
		}
	}
	return t
}

// argOperand checks an operand and returns its type.
func (v *validator) argOperand(o Operand) types.TypeID {
	switch o.Kind {
	case OpCopy, OpMove:
		return v.place(o.Place)
	case OpConst, OpImm:
		v.typeOK(o.Type)
		return o.Type
	}
	v.fail("M2", "operand kind %d", o.Kind)
	return types.NoType
}

func (v *validator) stmt(st *Statement, holders []int) {
	b := v.b
	v.span(st.Span)
	switch st.Kind {
	case StAssign:
		dest := v.place(st.Place)
		got := v.rvalue(&st.Rvalue, holders, st.Place, dest)
		if !v.fits(got, dest) {
			v.fail("M4", "assigns %s to a place of %s", v.ts(got), v.ts(dest))
		}
	case StScopeEnd:
		v.local(st.Local)
	case StJoin:
		v.local(st.Local)
		if b.Locals[st.Local].Kind != LocalScope {
			v.fail("M9", "join of _%d, which is not a scope local", st.Local)
		}
	case StNop:
	case StAsm:
		v.asm(st, v.curStmt)
	default:
		v.fail("M2", "statement kind %d", st.Kind)
	}
}

func (v *validator) genericArgs(r Range) {
	for _, a := range v.b.GenericArgs[r.Start:r.End()] {
		if a.IsConst {
			v.constArgOK(a.Const)
		} else {
			v.typeOK(a.Type)
		}
	}
}

func (v *validator) term(bl *Block, preds []uint32) {
	b, t := v.b, &bl.Term
	v.span(t.Span)
	if t.NewError && t.Kind != TermReturn {
		v.fail("M6", "new-error mark on a terminator that is not a return")
	}
	switch t.Kind {
	case TermGoto:
		v.block(t.Target)
	case TermSwitch:
		v.operand(t.On)
		v.rng(t.Cases, len(b.Cases), "case")
		for _, c := range b.Cases[t.Cases.Start:t.Cases.End()] {
			v.block(c.Target)
		}
		if t.Default != NoBlock {
			v.block(t.Default)
		}
	case TermCall:
		v.call(bl, t)
	case TermTypeTest:
		if b.GenericCount == 0 {
			v.fail("M2", "type test in an instance body")
		}
		v.typeOK(t.Type)
		v.block(t.Yes)
		v.block(t.No)
	case TermOnerrGuard:
		v.local(t.Value)
		v.block(t.Yes)
		v.block(t.No)
		if b.Blocks[t.Yes].Origin != OriginOnerr {
			v.fail("M8", "guard runs a block that is not an onerr copy")
		}
		if f := v.env.Facts; f != nil {
			if facts, ok := f(b.Locals[t.Value].Type); ok && facts.Kind != types.Linear {
				v.fail("M8", "guarded value _%d is %v, not linear", t.Value, facts.Kind)
			}
		}
	case TermReturn:
		if t.Exit > ExitMayBeError {
			v.fail("M6", "exit kind %d", t.Exit)
		}
		if t.NewError && t.Exit != ExitFail {
			v.fail("M6", "new-error mark on exit kind %d, not a fail", t.Exit)
		}
	case TermPanic:
		if t.Panic == types.PanicNone || int(t.Panic) >= types.NumPanicCodes {
			v.fail("M2", "panic code %d", t.Panic)
		}
		if t.HasMsg {
			mt := v.operand(t.Msg)
			if mt != types.Str && (t.Panic != types.PanicMust || v.tab.Kind(mt) != types.KErrorSet) {
				v.fail("M4", "panic message is %s, not str (or an error set for must)", v.ts(mt))
			}
		}
	case TermUnreachable:
	case TermTailCall:
		v.tailCall(t)
	default:
		v.fail("M2", "terminator kind %d", t.Kind)
	}
}

func (v *validator) call(bl *Block, t *Terminator) {
	b, tab := v.b, v.tab
	v.block(t.Target)
	dest := v.place(t.Dest)
	args := v.callArgs(t)
	if t.Spawn != NoLocal {
		v.local(t.Spawn)
		v.spawnScope(t.Spawn)
		if t.HasErr {
			v.fail("M5", "a spawned call has no error edge (its error goes to the scope)")
		}
	}
	if t.HasErr {
		v.block(t.Err.Block)
		v.local(t.Err.Error)
		if bl.Origin != OriginBody && t.Err.Kind == EdgeTry {
			v.fail("M7", "try inside a cleanup copy")
		}
	}
	switch t.Callee.Kind {
	case CallDirect:
		sig := v.directCallee(t, args, "call")
		if sig == nil {
			return
		}
		fallible := sig.Errors != types.EmptyErrors
		if t.Spawn == NoLocal && fallible != t.HasErr {
			v.fail("M5", "callee fallible=%v but error edge=%v", fallible, t.HasErr)
		}
		if v.genericCount(t, sig) {
			v.argTypes(args, sig.Params)
			if t.Spawn == NoLocal && !v.fits(sig.Result, dest) {
				v.fail("M5", "destination %s, callee result %s", v.ts(dest), v.ts(sig.Result))
			}
			if t.HasErr && b.Locals[t.Err.Error].Type != tab.ErrorSetType(sig.Errors) {
				v.fail("M5", "error local is %s, want %s", v.ts(b.Locals[t.Err.Error].Type), v.ts(tab.ErrorSetType(sig.Errors)))
			}
			v.reborrows(args, len(sig.Params), func(i int) types.TypeID { return sig.Params[i].Type })
		}
	case CallInterface:
		// The method's parameter count is not in Env, so environment
		// arguments past it are not told apart here; mono checks
		// the instance's call.
		if b.GenericCount == 0 {
			v.fail("M2", "interface call in an instance body")
		}
		v.typeOK(t.Callee.SelfType)
	case CallValue:
		ft := v.operand(t.Callee.Value)
		if tab.Kind(ft) != types.KFn {
			v.fail("M5", "call through %s", v.ts(ft))
		}
		fn := tab.Get(ft).Fn
		if len(args) < len(fn.Params) {
			v.fail("M5", "%d arguments, fn value has %d parameters", len(args), len(fn.Params))
		}
		v.envArgs(t, args[len(fn.Params):], len(fn.Params))
		fallible := fn.Errors != types.EmptyErrors
		if t.Spawn == NoLocal && fallible != t.HasErr {
			v.fail("M5", "fn value fallible=%v but error edge=%v", fallible, t.HasErr)
		}
		if t.Spawn == NoLocal && !v.fits(fn.Result, dest) {
			v.fail("M5", "destination %s, fn value result %s", v.ts(dest), v.ts(fn.Result))
		}
		if t.HasErr && b.Locals[t.Err.Error].Type != tab.ErrorSetType(fn.Errors) {
			v.fail("M5", "error local is %s, want %s", v.ts(b.Locals[t.Err.Error].Type), v.ts(tab.ErrorSetType(fn.Errors)))
		}
		v.reborrows(args, len(fn.Params), func(i int) types.TypeID { return fn.Params[i].Type })
	case CallBuiltin:
		switch t.Callee.Builtin {
		case BuiltinEvalArena:
		case BuiltinQuote:
			if len(args) == 0 || args[0].Operand.Kind != OpConst || v.operand(args[0].Operand) != tab.Slice(types.U8) {
				v.fail("M5", "quote without a []u8 template constant")
			}
		default:
			v.fail("M2", "builtin %d", t.Callee.Builtin)
		}
	default:
		v.fail("M2", "callee kind %d", t.Callee.Kind)
	}
}

// envArgs checks the arguments of a call past its callee's n declared
// parameters: each is an environment argument — an ArgCopy of &R or an
// ArgMove or ArgReborrow of mut R (IsEnvType). How many there are is mono's
// to check. A spawned call has none: s.go of a capturing literal is E0609.
func (v *validator) envArgs(t *Terminator, extra []Arg, n int) {
	if len(extra) > 0 && t.Spawn != NoLocal {
		v.fail("M5", "a spawned call with %d arguments past the callee's %d parameters", len(extra), n)
	}
	for i, a := range extra {
		et := v.argOperand(a.Operand)
		if !IsEnvType(v.tab, et) {
			v.fail("M5", "argument %d past the callee's %d parameters is %v %s, not an environment type &R or mut R", n+i+1, n, a.Mode, v.ts(et))
		}
		switch v.tab.Kind(et) {
		case types.KRef:
			if a.Mode != ArgCopy {
				v.fail("M5", "argument %d past the callee's %d parameters is %v %s, not a copy of &R", n+i+1, n, a.Mode, v.ts(et))
			}
		case types.KMut:
			if a.Mode != ArgMove && a.Mode != ArgReborrow {
				v.fail("M5", "argument %d past the callee's %d parameters is %v %s, not a move or reborrow of mut R", n+i+1, n, a.Mode, v.ts(et))
			}
		}
	}
}
