// Why: inline assembly and naked functions are the one place a body hands
// text to the assembler; every operand, option, hole and register must be
// judged before lower pastes the template into LLVM IR.
//
// What: asmStmt, asmValue, asmType, asmConst and asmSym (asm statements),
// AsmOptionBits, AsmLiteral and AsmTemplate (for lower), nakedDecl,
// nakedBody and nakedValue (naked functions). Errors: E1101, E1104 (type,
// const, sym, generic, options, abi, hole, reserved) and E1105.
//
// How: each operand is typed by direction against a fixed table of register
// types; holes are rewritten to operand indexes once, here, so lower reads a
// canonical template. Register names and classes are checked later, by mono.

package check

import (
	"strconv"
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// AsmOption bits, in mir.AsmOptions' order.
const (
	asmPure uint16 = 1 << iota
	asmNomem
	asmReadonly
	asmPreservesFlags
	asmNostack
	asmNoreturn
	asmAtt
	asmRaw
)

// asmOptionBits maps options(...) names to their bits.
var asmOptionBits = map[string]uint16{
	"pure": asmPure, "nomem": asmNomem, "readonly": asmReadonly, "preserves_flags": asmPreservesFlags,
	"nostack": asmNostack, "noreturn": asmNoreturn, "att": asmAtt, "raw": asmRaw,
}

// AsmOptionBits is the options of s as mir.AsmOptions bits (lower's).
func AsmOptionBits(in *source.Interner, s *ast.AsmStmt) uint16 {
	var o uint16
	for _, op := range s.Operands {
		if op.Kind == ast.AsmOptions {
			for _, n := range op.Options {
				o |= asmOptionBits[in.Str(n.Name)]
			}
		}
	}
	return o
}

// reservedRegs are rbx, rbp, rsp and rip with their parts: LLVM may
// hold the base and frame pointers there.
var reservedRegs = map[string]bool{
	"rbx": true, "ebx": true, "bx": true, "bl": true, "bh": true,
	"rbp": true, "ebp": true, "bp": true, "bpl": true,
	"rsp": true, "esp": true, "sp": true, "spl": true,
	"rip": true, "eip": true, "ip": true,
}

// asmMods are the template modifiers a hole may carry ({name:mod}):
// x86_64's r, e, x, l, y and z, and aarch64's w and x (a general
// register's 32- and 64-bit names) and b, h, s, d and q (a vector
// register's scalar names).
var asmMods = map[string]bool{"": true, "r": true, "e": true, "x": true, "l": true, "y": true, "z": true,
	"w": true, "b": true, "h": true, "s": true, "d": true, "q": true}

// nakedFn reports whether the unit is a naked function's body.
func (u *unit) nakedFn() bool {
	fd, ok := u.it.Decl.(*types.FuncDecl)
	return ok && fd.Naked
}

// asmErr is one E1104 with its reason.
func (u *unit) asmErr(at source.Span, reason, msg string) *diag.Diagnostic {
	return u.err(diag.E1104, at, msg).Text("reason", reason)
}

// asmStmt checks an asm statement and reports whether it diverges:
// with options(noreturn), and always in a naked function.
func (u *unit) asmStmt(s *ast.AsmStmt) bool {
	if u.unsafeDepth == 0 {
		u.err(diag.E1101, s.Span(), "asm needs an `unsafe \"reason\" { }` block").Text("operation", "asm")
	}
	naked := u.nakedFn()
	in := u.c.cfg.Interner
	var opts uint16
	var optsAt source.Span
	outputs := 0
	labels := map[string]bool{}
	for _, op := range s.Operands {
		if op.Label != nil {
			name := in.Str(op.Label.Name)
			if labels[name] {
				u.asmErr(op.Label.Span(), "hole", "the label `"+name+"` is used twice").Text("operand", name)
			}
			labels[name] = true
		}
		switch op.Kind {
		case ast.AsmIn, ast.AsmOut, ast.AsmLateOut, ast.AsmInOut, ast.AsmInLateOut:
			if naked {
				u.err(diag.E1105, op.Span(), "a naked function's asm takes only const and sym operands").Text("reason", "operand")
			}
			if lit, ok := op.Reg.(*ast.BasicLit); ok {
				if r, ok := litString(lit.Kind, lit.Raw); ok && reservedRegs[strings.ToLower(string(r))] {
					u.asmErr(lit.Span(), "reserved", "`"+string(r)+"` is reserved: LLVM may hold the base or frame pointer there").Text("register", string(r))
				}
			}
			if op.Kind != ast.AsmIn && !op.Discard {
				outputs++
			}
			u.asmValue(op)
		case ast.AsmConst:
			u.asmConst(op, naked)
		case ast.AsmSym:
			u.asmSym(op)
		case ast.AsmClobberABI:
			if naked {
				u.err(diag.E1105, op.Span(), "a naked function's asm takes only const and sym operands").Text("reason", "operand")
			}
			if r, ok := litString(op.ABI.Kind, op.ABI.Raw); !ok || string(r) != "C" {
				u.asmErr(op.ABI.Span(), "abi", "clobber_abi names \"C\", the target's C ABI").Text("abi", string(r))
			}
		case ast.AsmOptions:
			optsAt = op.Span()
			for _, n := range op.Options {
				name := in.Str(n.Name)
				bit, ok := asmOptionBits[name]
				switch {
				case !ok:
					u.asmErr(n.Span(), "options", "`"+name+"` is no asm option").Text("option", name)
					continue
				case opts&bit != 0:
					u.asmErr(n.Span(), "options", "the option `"+name+"` is written twice").Text("option", name)
				case naked && bit != asmAtt && bit != asmRaw:
					u.err(diag.E1105, n.Span(), "a naked function's asm takes only the options att and raw").Text("reason", "operand")
				}
				opts |= bit
			}
		}
	}
	if optsAt.IsEmpty() {
		optsAt = s.Span()
	}
	switch {
	case opts&asmNomem != 0 && opts&asmReadonly != 0:
		u.asmErr(optsAt, "options", "nomem and readonly conflict").Text("option", "nomem, readonly")
	case opts&asmPure != 0 && opts&(asmNomem|asmReadonly) == 0:
		u.asmErr(optsAt, "options", "pure needs nomem or readonly").Text("option", "pure")
	case opts&asmPure != 0 && outputs == 0:
		u.asmErr(optsAt, "options", "a pure asm needs an output, or it does nothing").Text("option", "pure")
	case opts&asmNoreturn != 0 && outputs > 0:
		u.asmErr(optsAt, "options", "a noreturn asm writes no output").Text("option", "noreturn")
	}
	if opts&asmRaw == 0 {
		if _, bad, name := AsmTemplate(in, u.b, s); bad != "" {
			u.asmErr(s.Template.Span(), "hole", bad).Text("operand", name)
		}
	}
	return opts&asmNoreturn != 0 || naked
}

// asmValue types a direction operand's value: an in value,
// or the place an out, lateout, inout or inlateout operand writes.
func (u *unit) asmValue(op *ast.AsmOperand) {
	if op.Discard || op.Value == nil {
		return
	}
	if op.Kind == ast.AsmIn {
		u.asmType(op, u.expr(op.Value, types.NoType), true)
		return
	}
	t := u.operand(op.Value, types.NoType)
	if t != types.Poison && !u.isPlace(op.Value) {
		u.err(diag.E0301, op.Value.Span(), "an asm output is a place: a local, a field, an element or a dereference").
			Text("reason", "not a place").Text("expected", "a place").Text("found", "an expression")
		return
	}
	u.externVarWrite(op.Value, "write")
	u.asmType(op, t, false)
}

// asmType is the table of operand types: integers, int, uint, f32, f64, raw
// pointers and Vec, and bool or Mask for in only; a type naming a
// type parameter is E1104 generic.
func (u *unit) asmType(op *ast.AsmOperand, t types.TypeID, in bool) {
	if t == types.Poison {
		return
	}
	at := op.Value.Span()
	if u.mentionsParam(t, 0) {
		u.asmErr(at, "generic", "an asm operand's type names a type parameter: `"+u.str(t)+"`").Text("type", u.str(t))
		return
	}
	ty := u.tab.Get(t)
	switch ty.Kind {
	case types.KInt, types.KIntPtr, types.KFloat, types.KRawPtr:
		return
	case types.KBool:
		if in {
			return
		}
		u.asmErr(at, "type", "no bool comes out of asm: an output could hold a byte that is no bool").Text("type", "bool").
			Alt("take a `u8` out and compare it")
		return
	}
	if v, ok := u.vecOf(t); ok {
		if !v.mask || in {
			return
		}
		u.asmErr(at, "type", "no Mask comes out of asm: a lane could be neither all zeros nor all ones").Text("type", u.str(t)).
			Alt("take a `Vec` out and compare it")
		return
	}
	d := u.asmErr(at, "type", "asm takes integers, floats, raw pointers and vectors, not `"+u.str(t)+"`").Text("type", u.str(t))
	switch {
	case ty.IsReference():
		d.Alt("pass `raw.Of(r)`, a raw pointer")
	case ty.IsSliceLike():
		d.Alt("pass `raw.Data(xs)`, a raw pointer to the first element")
	}
}

// mentionsParam reports whether t names a type parameter or a const
// parameter.
func (u *unit) mentionsParam(t types.TypeID, depth int) bool {
	if depth > 16 {
		return false
	}
	ty := u.tab.Get(t)
	switch ty.Kind {
	case types.KParam, types.KSelf:
		return true
	case types.KRawPtr, types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KOptional:
		return u.mentionsParam(ty.Elem, depth+1)
	case types.KArray:
		return ty.Len.Kind == types.CAParam || u.mentionsParam(ty.Elem, depth+1)
	case types.KNamed:
		for _, a := range ty.Args {
			if a.IsConst && a.Const.Kind == types.CAParam || !a.IsConst && u.mentionsParam(a.Type, depth+1) {
				return true
			}
		}
	}
	return false
}

// asmConst checks a const operand: an integer constant check folds (a
// literal, a const, SizeOf) or a const parameter; E1104 const otherwise.
// In a naked function a parameter named there is E1105 param.
func (u *unit) asmConst(op *ast.AsmOperand, naked bool) {
	if op.Value == nil {
		return
	}
	t := u.expr(op.Value, types.NoType)
	if naked && u.namesParam(op.Value) {
		u.err(diag.E1105, op.Value.Span(), "a naked function's parameters are declared for the ABI and never used by name").Text("reason", "param")
		return
	}
	if t == types.Poison {
		return
	}
	if u.isInt(t) && u.nodeConst(op.Value.ID()) == types.NoConst {
		// A named const, or operators over named consts: the request path of
		// a const argument, so lower reads the value from Consts.
		if v, ok, _ := u.constFolder().fold(op.Value); ok && !v.isBool {
			if id, ok := u.c.internFolded(v, t); ok {
				u.b.setConst(op.Value.ID(), id)
			}
		}
	}
	if u.isInt(t) && u.constParamRef(op.Value) {
		// A const parameter is a constant per instance:
		// mono pastes its value into the template (instance.go asmConsts).
		return
	}
	if !u.isInt(t) || u.nodeConst(op.Value.ID()) == types.NoConst {
		u.asmErr(op.Value.Span(), "const", "a const operand is an integer constant").Text("type", u.str(t))
	}
}

// constParamRef reports whether e names a const parameter of the unit: a
// generic `const N: int`, or a const value parameter `const k: int`.
func (u *unit) constParamRef(e ast.Expr) bool {
	id, ok := unparenExpr(e).(*ast.Ident)
	if !ok || u.b.Sig == nil {
		return false
	}
	use := u.useOf(id.ID())
	switch use.Kind {
	case UseLocal:
		return use.Local >= 1 && int(use.Local) <= len(u.b.Sig.Params) && u.b.Sig.Params[use.Local-1].Const
	case UseNone:
		for _, g := range u.b.Sig.Generics {
			if g.Const && g.Name == id.Name {
				return true
			}
		}
	}
	return false
}

// namesParam reports whether e mentions a parameter of the unit by name.
func (u *unit) namesParam(e ast.Expr) bool {
	found := false
	var walk func(n ast.Node)
	walk = func(n ast.Node) {
		if found {
			return
		}
		if id, ok := n.(*ast.Ident); ok {
			if use := u.useOf(id.ID()); use.Kind == UseLocal && u.b.Sig != nil && use.Local >= 1 && int(use.Local) <= len(u.b.Sig.Params) {
				found = true
			}
		}
		ast.EachChild(n, walk)
	}
	walk(e)
	return found
}

// asmSym checks a sym operand: a non-generic function (with a body, a
// bodiless extern or naked), a var or an extern var, recorded as the path's
// Use (UseItem or UseGlobal); anything else is E1104 sym.
func (u *unit) asmSym(op *ast.AsmOperand) {
	if op.Sym == nil {
		return
	}
	r := u.ref(op.Sym)
	if r.Kind == resolve.RefPoison {
		return
	}
	if r.Kind == resolve.RefItem {
		it := u.c.cfg.Items.Get(types.ItemID(r.A))
		switch d := u.c.declOfItem(it).(type) {
		case *types.FuncDecl:
			if len(d.Sig.Generics) == 0 {
				*u.b.use(op.Sym.ID()) = Use{Kind: UseItem, Item: it.ID}
				u.x.rec.signature(it.Key)
				return
			}
		case *types.GlobalDecl:
			*u.b.use(op.Sym.ID()) = Use{Kind: UseGlobal, Item: it.ID}
			u.x.rec.signature(it.Key)
			return
		}
	}
	u.asmErr(op.Sym.Span(), "sym", "sym names a non-generic function, a var or an extern var").Text("operand", u.c.cfg.Interner.Str(op.Sym.Names[len(op.Sym.Names)-1].Name))
}

// AsmLiteral is the template's text as written, escapes decoded: the
// template of an options(raw) asm, used verbatim.
func AsmLiteral(s *ast.AsmStmt) string {
	raw, _ := litString(s.Template.Kind, s.Template.Raw)
	return string(raw)
}

// AsmTemplate is the canonical template: each hole
// {name} or {name:mod} becomes {i} or {i:mod}, i the operand's index in
// s.Operands; {{ and }} are kept. A hole names an operand's label, or the
// local an operand passes as its bare value. bad is the first fault
// ("" when none) and name the hole it is at; check reports it as E1104 hole,
// and lower, which runs only on clean bodies, reads text. b may be nil (no
// local can then be named).
func AsmTemplate(in *source.Interner, b *Body, s *ast.AsmStmt) (text, bad, name string) {
	raw, ok := litString(s.Template.Kind, s.Template.Raw)
	if !ok {
		return "", "", ""
	}
	find := func(name string) (int, string) {
		hit, why := -1, "no operand is labelled or passes `"+name+"`"
		for i, op := range s.Operands {
			if op.Label != nil && in.Str(op.Label.Name) == name {
				return i, ""
			}
		}
		for i, op := range s.Operands {
			id, isID := unparenExpr(op.Value).(*ast.Ident)
			if op.Label != nil || !isID || in.Str(id.Name) != name {
				continue
			}
			if b != nil {
				if k := int(id.ID()) - int(b.Base); b.Uses == nil || k < 0 || k >= len(b.Uses) || b.Uses[k].Kind != UseLocal {
					continue
				}
			}
			if hit >= 0 {
				return -1, "two operands pass `" + name + "`: give one a label"
			}
			hit, why = i, ""
		}
		return hit, why
	}
	var sb strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '{' && i+1 < len(raw) && raw[i+1] == '{':
			sb.WriteString("{{")
			i++
		case c == '}' && i+1 < len(raw) && raw[i+1] == '}':
			sb.WriteString("}}")
			i++
		case c == '{':
			j := strings.IndexByte(string(raw[i+1:]), '}')
			if j < 0 {
				return "", "a hole is not closed: write `{{` for a brace", ""
			}
			hole := string(raw[i+1 : i+1+j])
			name, mod, _ := strings.Cut(hole, ":")
			if !asmMods[mod] {
				return "", "`" + mod + "` is no template modifier (r, e, x, l, y, z on x86_64; w, x, b, h, s, d, q on aarch64)", name
			}
			k, why := find(name)
			if k < 0 {
				return "", why, name
			}
			sb.WriteString("{" + strconv.Itoa(k))
			if mod != "" {
				sb.WriteString(":" + mod)
			}
			sb.WriteByte('}')
			i += j + 1
		case c == '}':
			return "", "a lone `}` in the template: write `}}`", ""
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String(), "", ""
}

// nakedDecl judges a naked function's signature: no type
// parameters, contract or cpu clause (E1105 signature; a cpu clause is E1105
// only) and a body (E1105 body).
func (m *Module) nakedDecl(it *types.Item, x *tctx, s *ast.Signature, naked bool) {
	fd, ok := it.Decl.(*types.FuncDecl)
	if !naked || !ok || s == nil {
		return
	}
	fd.Naked = true
	sig := func(at source.Span, what string) {
		x.errorf(diag.E1105, at, "a naked function takes no "+what).Text("reason", "signature")
	}
	if len(fd.Sig.Generics) > 0 {
		at := s.Span()
		if s.TypeParams != nil {
			at = s.TypeParams.Span()
		}
		sig(at, "type parameters")
	}
	if s.Cpu != nil {
		sig(s.Cpu.Span(), "cpu clause")
		fd.Sig.CPU = nil
	}
	for _, c := range s.Contracts {
		sig(c.Span(), "contract")
	}
	if fd.Bodiless {
		x.errorf(diag.E1105, s.Span(), "a naked function has a body of one unsafe block holding one asm").Text("reason", "body")
	}
}

// nakedBody is E1105 body: a naked body is exactly one unsafe block
// holding exactly one asm statement; the first statement that breaks it is
// reported.
func (u *unit) nakedBody(b *ast.Block) {
	if b == nil {
		return
	}
	bad := func(at source.Span) {
		u.err(diag.E1105, at, "a naked function's body is exactly one unsafe block holding one asm").Text("reason", "body")
	}
	if len(b.Stmts) == 0 {
		bad(b.Span())
		return
	}
	us, ok := b.Stmts[0].(*ast.UnsafeStmt)
	if !ok {
		bad(b.Stmts[0].Span())
		return
	}
	if len(b.Stmts) > 1 {
		bad(b.Stmts[1].Span())
		return
	}
	if us.Body == nil || len(us.Body.Stmts) == 0 {
		bad(us.Span())
		return
	}
	if _, ok := us.Body.Stmts[0].(*ast.AsmStmt); !ok {
		bad(us.Body.Stmts[0].Span())
		return
	}
	if len(us.Body.Stmts) > 1 {
		bad(us.Body.Stmts[1].Span())
	}
}

// nakedValue is E1101 naked-value: naming a naked function as a value
// outside unsafe ("Calling it, or naming it as a value, needs unsafe").
func (u *unit) nakedValue(at ast.Node, d *types.FuncDecl) {
	if u.unsafeDepth > 0 || !d.Naked {
		return
	}
	u.err(diag.E1101, at.Span(), "naming a naked function as a value needs an `unsafe \"reason\" { }` block").Text("operation", "naked-value")
}
