// Why: structured concurrency and unsafe code are where safety depends on
// what crosses a boundary: what a task may receive, where a Scope value may
// go, and which operations only unsafe code may perform.
//
// What: scopeStmt, goExpr, taskResult, scopeEscapes and sendable (E0301,
// E0801 to E0804, E1001, E1002, E1003 for `scope` and `s.go`); rawDeref
// (E1103), unsafeCall, unsafeOnly, unsafeValue and packedBorrow (E1101);
// variadicArg (the types an argument after `...` may have).
//
// How: every task argument, the receiver (at the type self receives) and
// the fn value must be Send: E1002 for a &C or []C with C not Share, else
// E1001. Unsafe-only items are named by module and name, so a call and a
// value use of one carry the same obligation.

package check

import (
	"strconv"
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// isScope reports whether t is a Scope or Scope[E] value (Known.Scope).
func (u *unit) isScope(t types.TypeID) bool {
	ty := u.tab.Get(t)
	return u.c.known.Scope != types.NoItem && ty.Kind == types.KNamed && ty.Item == u.c.known.Scope
}

// scopeUnknown reports whether t is a Scope[E] whose E is Poison (an error
// set that did not resolve): neither family rule may judge it.
func (u *unit) scopeUnknown(t types.TypeID) bool {
	ty := u.tab.Get(t)
	return len(ty.Args) == 1 && ty.Args[0].Type == types.Poison
}

// scopeStmt is `scope s := e { }`: e must be a Scope; s has its
// type; the body is checked with s as the scope binding. `scope` over
// Scope[E] must be `try scope` (E0802), `try scope` over Scope is E0804,
// and a `try scope`'s set must be the function's (E0801).
func (u *unit) scopeStmt(s *ast.ScopeStmt) bool {
	t := types.Poison
	if s.Value != nil {
		saved := u.scopeInit
		u.scopeInit = s.Value
		t = u.expr(s.Value, types.NoType)
		u.scopeInit = saved
		if t != types.Poison && !u.isScope(t) {
			u.err(diag.E0301, s.Value.Span(), "`scope` needs a `Scope`, found `"+u.str(t)+"`").Text("expected", "Scope").Text("found", u.str(t))
			t = types.Poison
		}
	}
	if t != types.Poison && !u.scopeUnknown(t) {
		es, isErr := u.scopeErrs(t)
		sp := s.Span()
		switch {
		case isErr && !s.Try:
			u.err(diag.E0802, s.Value.Span(), "a `scope` over `"+u.str(t)+"` can fail: write `try scope`").Text("type", u.str(t)).
				Fix(diag.Fix{Class: diag.Compiles, Edits: []diag.Edit{{Span: source.Span{File: sp.File, Start: sp.Start, End: sp.Start}, Text: "try "}}, Why: "the scope's first task error leaves through `try`"})
		case !isErr && s.Try:
			kw := keywordSpan(u.fx, sp.Start, ast.TokTry)
			next := keywordSpan(u.fx, kw.End, ast.TokScope)
			u.err(diag.E0804, kw, "`try scope` needs a `Scope[E]`, found `"+u.str(t)+"`").Text("found", u.str(t)).
				Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: between(kw, next), Text: ""}}, Why: "the scope's tasks cannot fail"})
		case isErr && s.Try:
			u.setCovers(sp, "try", es)
		}
	}
	u.bindName(s.Name, t)
	if s.Name != nil {
		if id := u.declLocal[s.Name.ID()]; id != 0 {
			u.scopes = append(u.scopes, scopeCtx{local: id, deferAt: len(u.loops), inDefer: u.deferAt})
			defer func() { u.scopes = u.scopes[:len(u.scopes)-1] }()
		}
	}
	return u.block(s.Body)
}

// scopeErrs is the error set of a Scope[E], and whether t has one.
func (u *unit) scopeErrs(t types.TypeID) (types.ErrorSetID, bool) {
	ty := u.tab.Get(t)
	if len(ty.Args) == 1 && !ty.Args[0].IsConst {
		if a := u.tab.Get(ty.Args[0].Type); a.Kind == types.KErrorSet {
			return a.Errors, true
		}
	}
	return types.EmptyErrors, false
}

// scopeCtx is one enclosing scope binding and whether it was entered inside
// a cleanup body.
type scopeCtx struct {
	local   LocalID
	deferAt int
	inDefer int
}

// scopeEscapes is E1003 for a Scope value anywhere but its scope binding:
// bound with :=, returned, passed by value, stored in a field. A
// call that makes one is reported at the call (expr), not again here.
func (u *unit) scopeEscapes(e ast.Expr, t types.TypeID) bool {
	if t == types.Poison || !u.isScope(t) {
		return false
	}
	if _, ok := e.(*ast.CallExpr); !ok {
		u.scopeE1003(e)
	}
	return true
}

// scopeE1003 reports E1003 at e.
func (u *unit) scopeE1003(e ast.Expr) {
	u.err(diag.E1003, e.Span(), "a `Scope` lives only in `scope s := .. { }`").Alt("scope s := " + "io.Scope() { .. }")
}

// taskResult is the rule on what a task returns: under
// Scope, void or never (E0301); under Scope[E], E'!void with E' within E
// (E0801), and a plain void is E0301.
func (u *unit) taskResult(call *ast.CallExpr, scope types.TypeID) {
	ct := u.b.TypeOf(call.ID())
	if ct == types.Poison || ct == types.NoType || ct == types.Never || u.scopeUnknown(scope) {
		return
	}
	es, isErr := u.scopeErrs(scope)
	ty := u.tab.Get(ct)
	switch {
	case !isErr && ct != types.Void:
		u.err(diag.E0301, call.Span(), "a task under `Scope` returns `void`, found `"+u.str(ct)+"`").Text("expected", "void").Text("found", u.str(ct)).
			Alt("send the result through a `mut` argument or a `Chan`")
	case isErr && (ty.Kind != types.KErrorUnion || ty.Elem != types.Void):
		want := u.str(u.tab.ErrorUnion(es, types.Void))
		u.err(diag.E0301, call.Span(), "a task under `"+u.str(scope)+"` returns `"+want+"`, found `"+u.str(ct)+"`").Text("expected", want).Text("found", u.str(ct))
	case isErr:
		if missing := u.missingErrors(ty.Errors, es); len(missing) > 0 {
			sp := call.Span()
			u.err(diag.E0801, sp, "the scope's error set does not include "+strings.Join(missing, ", ")).Fact("missing", diag.TextList(missing...)).
				Alt("add them to the scope's error set").Alt("handle them inside the task")
		}
	}
}

// goExpr is `x.go F(args)`: x the scope binding or a &Scope (E0301
// otherwise), the call checked as an ordinary call, then every argument,
// the method receiver and the fn value Send (E1002 for a &C or []C with C
// not Share, else E1001). In a defer or onerr of the scope's own block it is
// E1003.
func (u *unit) goExpr(e *ast.GoExpr) types.TypeID {
	st := u.expr(e.Scope, types.NoType)
	if v, _ := u.deref(st); st != types.Poison && !u.isScope(v) {
		u.err(diag.E0301, e.Scope.Span(), "`.go` needs a scope, found `"+u.str(st)+"`").Text("expected", "Scope").Text("found", u.str(st))
	} else if st != types.Poison && !u.spawnTarget(e.Scope, st) {
		// A Scope value that is not the binding (a `Scope` or `mut Scope`
		// parameter), or a `&&Scope`: lower's spawn takes the binding or a
		// `&Scope` only, so this was its ICE (corpus FuzzCheck
		// c8cfb8114d6ab0ba, `A(s:Scope,x:mut i32){s.go Bump(x)}`).
		u.err(diag.E0301, e.Scope.Span(), "`.go` needs the scope binding or a `&Scope`, found `"+u.str(st)+"`").
			Text("expected", "&Scope").Text("found", u.str(st)).Alt("take `s: &Scope` and pass `&s`")
	}
	if id, ok := e.Scope.(*ast.Ident); ok && u.inDefer() {
		if us := u.b.Uses; us != nil {
			if use := us[u.b.slot(id.ID())]; use.Kind == UseLocal {
				for _, sc := range u.scopes {
					if sc.local == use.Local && sc.inDefer < 0 {
						u.err(diag.E1003, e.Span(), "`s.go` in a cleanup of the scope's own block would start a task the scope cannot join")
					}
				}
			}
		}
	}
	call, ok := e.Call.(*ast.CallExpr)
	if !ok {
		if e.Call != nil {
			u.expr(e.Call, types.Poison)
		}
		return types.Void
	}
	// A literal in an s.go call is no const argument.
	if u.goCalls == nil {
		u.goCalls = map[ast.NodeID]bool{}
	}
	u.goCalls[call.ID()] = true
	u.expr(call, types.NoType)
	if v, _ := u.deref(st); st != types.Poison && u.isScope(v) {
		u.taskResult(call, v)
	}
	var parts []ast.Expr
	if fe, ok := methodField(call.Fun); ok {
		// The receiver of a method call crosses too, a method of a type
		// parameter's bound included, at the type self
		// receives (goRecvType).
		if use := u.b.Uses; use != nil {
			if k := use[u.b.slot(call.ID())].Kind; k == UseMethod || k == UseIfaceMethod {
				u.sendable(fe.X, u.goRecvType(call, fe.X))
			}
		}
	}
	if use := u.b.Uses; use != nil && use[u.b.slot(call.ID())].Kind == UseFnValueCall {
		parts = append(parts, call.Fun)
	}
	for _, a := range call.Args {
		if a != nil && a.Value != nil {
			parts = append(parts, a.Value)
		}
	}
	for _, p := range parts {
		u.sendable(p, u.b.TypeOf(p.ID()))
	}
	return types.Void
}

// goRecvType is the type that crosses to the task for receiver x of method
// call c under s.go. Every `&self` or `mut self` receiver passes &V or
// mut V (receiver records AdjRefShared or AdjRefMut for it, with Deref
// the layers removed first), not x's own type: a local value, a field
// place such as h.ctr with h: &Holder, and a reference too, which is
// reborrowed (k: mut Ctr calling a `&self` method passes &Ctr). A Send V
// can hold a Cell, and two tasks would share it. So every such receiver is
// judged at callee.recvPassed, and a test of Deref here would reopen the
// hole.
// k: &Ctr calling a `mut self` method is judged at mut Ctr, which is Send
// when Ctr is, so check accepts it; borrow rejects it (E0507).
// Any other receiver keeps x's type: a slice passed to a Slice method, a
// value that self moves, and, by choice, a by-value self copied
// through a reference (AdjCopyThrough), judged at &T rather than at the
// T self receives. That is conservative for a Send type that is not
// Share (a false E1002), and lets through only a Share, Plain type that
// is not Send, which needs an unsafe impl.
func (u *unit) goRecvType(c *ast.CallExpr, x ast.Expr) types.TypeID {
	t := u.b.TypeOf(x.ID())
	if a := u.b.Adjust; a != nil {
		if i := int(x.ID()) - int(u.b.Base); i >= 0 && i < len(a) {
			if k := a[i].Kind; k == AdjRefShared || k == AdjRefMut {
				if p, ok := u.goRecv[c.ID()]; ok && p != types.NoType && p != types.Poison {
					return p
				}
			}
		}
	}
	return t
}

// spawnTarget reports whether x, of type t (a Scope under any references),
// is what `.go` may start a task on: the scope binding itself, or a
// `&Scope` value.
func (u *unit) spawnTarget(x ast.Expr, t types.TypeID) bool {
	ty := u.tab.Get(t)
	if ty.Kind == types.KRef {
		return u.isScope(ty.Elem)
	}
	if !u.isScope(t) {
		return false
	}
	id, ok := x.(*ast.Ident)
	if !ok || u.b.Uses == nil {
		return false
	}
	use := u.b.Uses[u.b.slot(id.ID())]
	if use.Kind != UseLocal {
		return false
	}
	for _, sc := range u.scopes {
		if sc.local == use.Local {
			return true
		}
	}
	return false
}

// sendable reports E1002 or E1001 for an argument of a task that is not
// Send.
func (u *unit) sendable(e ast.Expr, t types.TypeID) {
	if t == types.Poison || t == types.NoType {
		return
	}
	// The Send judgement reads the shape of every type item t names, as a
	// bound judgement does (satisfies), so an edit to one of them (an unsafe
	// impl Send or Share included, iface's shape part) reaches this unit.
	// part and pt.Elem below are parts of t, so this one walk covers them.
	u.c.shapeReads(u.env, t, u.x.rec)
	if u.c.FactsIn(u.env, t).Send {
		return
	}
	notSend := func(x types.TypeID) bool { return !u.c.FactsIn(u.env, x).Send }
	part, _ := u.c.byValuePart(t, notSend)
	if part == types.NoType {
		part = t
	}
	pt := u.tab.Get(part)
	if (pt.Kind == types.KRef || pt.Kind == types.KSlice) && !u.c.FactsIn(u.env, pt.Elem).Share {
		u.err(diag.E1002, e.Span(), "`"+u.str(pt.Elem)+"` is not Share, so `"+u.str(part)+"` cannot cross to another task").
			Text("type", u.str(t)).Text("part", u.str(part))
		return
	}
	u.err(diag.E1001, e.Span(), "`"+u.str(t)+"` is not Send, so it cannot cross to another task").Text("type", u.str(t)).Text("part", u.str(part))
}

// rawDeref is E1103: a raw pointer dereferenced outside unsafe.
func (u *unit) rawDeref(at ast.Node, t types.TypeID) {
	if u.unsafeDepth == 0 {
		u.err(diag.E1103, at.Span(), "dereferencing `"+u.str(t)+"` needs an `unsafe \"reason\" { }` block").Text("type", u.str(t))
	}
}

// unsafeCall is E1101 for a call outside unsafe of a bodiless
// extern, a naked function or an unsafe-only item (unsafeOnly). e is the
// call, or the source of a `for x in it` whose loop calls `Next`.
func (u *unit) unsafeCall(e ast.Node, it types.ItemID, d *types.FuncDecl) {
	u.rawNeedsSSE(e, it) // E1106, unsafe or not
	if u.unsafeDepth > 0 {
		return
	}
	if len(d.Sig.CPU) > 0 {
		u.cpuUse(e, &d.Sig, false)
	}
	op := u.unsafeOnly(it)
	switch {
	case d.Sig.Extern && d.Bodiless:
		op = "call of a bodiless extern"
	case op == "" && d.Naked:
		op = "naked-call"
	case op == "" && u.cComMethod(it):
		op = "c-vtable-method"
	}
	if op != "" {
		u.err(diag.E1101, e.Span(), unsafeCallText(op)+" needs an `unsafe \"reason\" { }` block").Text("operation", op)
	}
}

// unsafeCallText is the subject of unsafeCall's message: the operation
// facts name an item (`vm.PageSize`, `raw.Read`, `str.FromC`), which the
// text calls, not "the vm.PageSize" (the conformance tests' E1101 units).
func unsafeCallText(op string) string {
	switch op {
	case "call of a bodiless extern":
		return "the call of a bodiless extern"
	case "naked-call":
		return "calling a naked function"
	case "c-vtable-method":
		return "calling a C vtable method"
	case "mmio":
		return "calling a std.mmio function"
	}
	return "calling " + op
}

// packedBorrow is E1101: `&p.f` or `mut p.f` outside unsafe
// where f's natural alignment exceeds its struct's packed(n) cap.
func (u *unit) packedBorrow(e *ast.UnaryExpr, op string) {
	if u.unsafeDepth > 0 || u.b.Uses == nil {
		return
	}
	x := e.X
	for {
		p, ok := x.(*ast.ParenExpr)
		if !ok {
			break
		}
		x = p.X
	}
	fe, ok := x.(*ast.FieldExpr)
	if !ok {
		return
	}
	use := u.b.Uses[u.b.slot(fe.ID())]
	if use.Kind != UseField || use.Item == types.NoItem {
		return
	}
	d, ok := u.c.cfg.Items.Get(use.Item).Decl.(*types.StructDecl)
	if !ok || d.Packed == 0 {
		return
	}
	ft := u.b.TypeOf(fe.ID())
	if ft == types.Poison || ft == types.NoType || !u.c.closed(ft) {
		return
	}
	if a := u.c.Shape(ft).Align; uint64(a) > uint64(d.Packed) {
		name := op + " of a field of a packed struct"
		u.err(diag.E1101, e.Span(), "`"+u.str(ft)+"` needs alignment "+strconv.FormatUint(uint64(a), 10)+" but the packed struct gives it "+
			strconv.FormatUint(uint64(d.Packed), 10)+": borrowing it needs an `unsafe \"reason\" { }` block").Text("operation", name)
	}
}

// rawSafe are the std.raw functions marked safe; every
// other std.raw function needs unsafe (E1101).
var rawSafe = map[string]bool{"Of": true, "OfMut": true, "Null": true, "IsNull": true, "Same": true, "Data": true,
	"DataMut": true, "StrData": true, "Addr": true, "IsLinear": true, "Fail": true, "Fence": true, "FenceOrd": true, "Ctz64": true,
	"AddCarry64": true, "SubBorrow64": true,
	"Clz64": true, "Popcount64": true, "RotL64": true, "Mul128": true, "F64Bits": true, "F64From": true,
	"F32Bits": true, "F32From": true, "SqrtF64": true, "Select": true}

// rawUnsafe reports whether it is a free function of the std module std.raw
// that rawSafe does not mark safe (methods of raw.Atomic are all safe).
func (u *unit) rawUnsafe(it types.ItemID) bool {
	item := u.c.cfg.Items.Get(it)
	if item.Owner != types.NoItem || item.OwnerPrim != types.NoType {
		return false
	}
	mod := u.c.cfg.Items.Module(item.Module)
	if mod == nil || !mod.Std || mod.Path != "std.raw" {
		return false
	}
	return !rawSafe[u.c.cfg.Interner.Str(item.Name)]
}

// fsMap reports whether it is std.fs's free function Map, the read-only
// mapping only unsafe code may make.
func (u *unit) fsMap(it types.ItemID) bool {
	item := u.c.cfg.Items.Get(it)
	if item.Owner != types.NoItem || item.OwnerPrim != types.NoType || item.Kind != types.KindFunc {
		return false
	}
	mod := u.c.cfg.Items.Module(item.Module)
	return mod != nil && mod.Std && mod.Path == "std.fs" && u.c.cfg.Interner.Str(item.Name) == "Map"
}

// unsafeOnly names the operation when it is an item only unsafe code may
// call or name (E1101): str.FromC, an unsafe std.raw
// function, a std.mmio function, fs.Map or a runtime door
// (unsafeDoors); else it is "".
func (u *unit) unsafeOnly(it types.ItemID) string {
	switch {
	case it == types.NoItem:
		return ""
	case it == u.c.known.StrFromC:
		return "str.FromC"
	case u.rawUnsafe(it):
		return "raw." + u.c.cfg.Interner.Str(u.c.cfg.Items.Get(it).Name)
	case u.mmioItem(it):
		return "mmio"
	case u.fsMap(it):
		return "fs.Map"
	}
	return u.unsafeDoors(it)
}

// unsafeDoors names the runtime doors only unsafe code may call or name:
// every exported function and method of std.vm and
// std.linux, runtime.AddFaultFilter, runtime.ForeignIo and
// runtime.ForeignContext, mem.Custom, and
// std.core's Alloc.Bytes and Alloc.FreeBytes; else "". A call from the
// item's own module is not caught: std.vm's code and test blocks are std's,
// in unsafe_modules, and audited there.
func (u *unit) unsafeDoors(it types.ItemID) string {
	item := u.c.cfg.Items.Get(it)
	if item.Kind != types.KindFunc || !item.Exported || (u.m != nil && item.Module == u.m.ID) {
		return ""
	}
	mod := u.c.cfg.Items.Module(item.Module)
	if mod == nil || !mod.Std {
		return ""
	}
	name := u.c.cfg.Interner.Str(item.Name)
	owner := ""
	if item.Owner != types.NoItem {
		owner = u.c.cfg.Interner.Str(u.c.cfg.Items.Get(item.Owner).Name)
	} else if item.OwnerPrim != types.NoType {
		return ""
	}
	switch {
	case mod.Path == "std.vm":
		if owner != "" {
			return "vm." + owner + "." + name
		}
		return "vm." + name
	case mod.Path == "std.linux":
		return "linux." + name
	case mod.Path == "std.runtime" && owner == "" && (name == "AddFaultFilter" || name == "ForeignIo" || name == "ForeignContext"):
		return "runtime." + name
	case mod.Path == "std.mem" && owner == "" && name == "Custom":
		return "mem.Custom"
	case mod.Path == "std.core" && owner == "Alloc" && (name == "Bytes" || name == "FreeBytes"):
		return "Alloc." + name
	}
	return ""
}

// unsafeValue is E1101 for an unsafe-only item named as a value outside
// unsafe: a call through the fn value is a call that
// unsafeCall never sees, so the name carries the obligation, as a cpu
// function's and a naked function's do. A bodiless extern
// is one too: calling it needs unsafe, and a call
// through its value would run C code outside any unsafe block. The
// argument of a dl.Present call names an extern without unsafe: the
// intrinsic never calls it (present.go), and Present itself is never a
// value, unsafe or not.
func (u *unit) unsafeValue(at ast.Node, it types.ItemID) {
	if u.presentItem(it) {
		u.presentValue(at) // present.go
		return
	}
	if u.unsafeDepth > 0 || u.presentArg != nil && at == ast.Node(u.presentArg) {
		return
	}
	if op := u.unsafeOnly(it); op != "" {
		u.err(diag.E1101, at.Span(), "naming "+op+" as a value needs an `unsafe \"reason\" { }` block").Text("operation", op)
		return
	}
	if it == types.NoItem {
		return
	}
	if d, ok := u.c.cfg.Items.Get(it).Decl.(*types.FuncDecl); ok && d.Sig.Extern && d.Bodiless {
		u.err(diag.E1101, at.Span(), "naming a bodiless extern as a value needs an `unsafe \"reason\" { }` block").Text("operation", "extern-value")
		return
	}
	if u.cComMethod(it) {
		u.err(diag.E1101, at.Span(), "naming a C vtable method as a value needs an `unsafe \"reason\" { }` block").Text("operation", "c-vtable-method")
	}
}

// variadicArg is the rule for an argument after `...`: integers,
// floats, bool, char, raw pointers and fn values only (E0301).
func (u *unit) variadicArg(e ast.Expr) {
	t := u.expr(e, types.NoType)
	switch u.tab.Kind(t) {
	case types.KInt, types.KIntPtr, types.KFloat, types.KBool, types.KChar, types.KRawPtr, types.KFn, types.KPoison:
		return
	}
	u.err(diag.E0301, e.Span(), "a variadic argument is a number, bool, char, raw pointer or fn, found `"+u.str(t)+"`").
		Text("expected", "a C scalar").Text("found", u.str(t))
}

// methodField is the member FieldExpr of a method call's callee: x.M, or
// x.M[A] with explicit generic arguments (an IndexExpr over the member), as
// lower's methodField reads it. The receiver of x.M[A]() crosses to the
// task as x.M()'s does.
func methodField(fun ast.Expr) (*ast.FieldExpr, bool) {
	switch f := fun.(type) {
	case *ast.FieldExpr:
		return f, true
	case *ast.IndexExpr:
		fe, ok := f.X.(*ast.FieldExpr)
		return fe, ok
	}
	return nil, false
}
