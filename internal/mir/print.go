// Why: MIR golden tests and debugging need a text form of a Body that stays
// the same from run to run and machine to machine.
//
// What: Printer, with Print, PlaceString and the renderers of operands,
// rvalues, statements, terminators and callees.
//
// How: names come from the interner, items print by key and consts by
// value (const operands, repeat lengths and const generic arguments), never
// by ID, so the text does not depend on the order things were interned in.

package mir

import (
	"strconv"
	"strings"

	"mk/internal/types"
)

// Printer renders bodies. Items may be nil, in which case items print as
// item#N; the type table spells types and, through its ConstTable, consts.
type Printer struct {
	Types *types.Table
	Items *types.Items
}

// Print renders b as text: a header naming the item, the locals, then each
// block with its statements and terminator.
func (p *Printer) Print(b *Body) string {
	var s strings.Builder
	s.WriteString("body ")
	s.WriteString(p.item(b.Item))
	s.WriteString(" generics=" + strconv.Itoa(int(b.GenericCount)) + " params=" + strconv.Itoa(int(b.ParamCount)) + " {\n")
	in := p.Types.Interner()
	for i, l := range b.Locals {
		s.WriteString("  let _" + strconv.Itoa(i) + ": " + p.ty(l.Type))
		switch l.Kind {
		case LocalReturn:
			s.WriteString(" // return")
		case LocalParam:
			s.WriteString(" // param " + strconv.Itoa(int(l.Param)))
		case LocalUser:
			if l.Var {
				s.WriteString(" // var ")
			} else {
				s.WriteString(" // ")
			}
		case LocalTemp:
			s.WriteString(" // temp")
		case LocalScope:
			s.WriteString(" // scope ")
		case LocalLoopHolder:
			s.WriteString(" // holder loop" + strconv.Itoa(int(l.Loop)))
		}
		if l.Name.IsValid() && l.Kind != LocalTemp {
			if l.Kind == LocalParam {
				s.WriteByte(' ')
			}
			s.WriteString(in.Str(l.Name))
		}
		s.WriteByte('\n')
	}
	for i := range b.Blocks {
		bl := &b.Blocks[i]
		s.WriteString("  bb" + strconv.Itoa(i))
		switch bl.Origin {
		case OriginDefer:
			s.WriteString(" (defer " + strconv.Itoa(int(bl.Defer)) + ")")
		case OriginOnerr:
			s.WriteString(" (onerr " + strconv.Itoa(int(bl.Defer)) + ")")
		}
		s.WriteString(":\n")
		for _, st := range bl.Stmts {
			s.WriteString("    " + p.stmt(b, &st) + "\n")
		}
		s.WriteString("    " + p.term(b, &bl.Term) + "\n")
	}
	s.WriteString("}\n")
	return s.String()
}

func (p *Printer) item(id types.ItemID) string {
	if id == types.NoItem {
		return "<none>"
	}
	if p.Items == nil {
		return "item#" + strconv.Itoa(int(id))
	}
	return p.Items.Get(id).Key.String()
}

func (p *Printer) ty(t types.TypeID) string { return p.Types.TypeString(t, nil) }

// PlaceString renders a place: _3, (*_3).1, _2[_4], (_1 as some).0.
func (p *Printer) PlaceString(b *Body, pl Place) string {
	s := "_" + strconv.Itoa(int(pl.Local))
	for _, pr := range b.Proj(pl) {
		switch pr.Kind {
		case ProjField:
			if pr.FieldLoop {
				s += ".loop" + strconv.Itoa(int(pr.A))
			} else {
				s += "." + strconv.Itoa(int(pr.A))
			}
		case ProjDeref:
			s = "(*" + s + ")"
		case ProjIndex:
			s += "[_" + strconv.Itoa(int(pr.A)) + "]"
		case ProjDowncast:
			s = "(" + s + " as " + p.sel(pr.Sel, pr.A) + ")"
		}
	}
	return s
}

func (p *Printer) sel(sel VariantSel, a uint32) string {
	switch sel {
	case SelEnum:
		return "variant" + strconv.Itoa(int(a))
	case SelSome:
		return "some"
	case SelOk:
		return "ok"
	case SelErr:
		return "err"
	case SelCase:
		return p.item(types.ItemID(a))
	}
	return "?"
}

func (p *Printer) op(b *Body, o Operand) string {
	switch o.Kind {
	case OpCopy:
		return "copy " + p.PlaceString(b, o.Place)
	case OpMove:
		return "move " + p.PlaceString(b, o.Place)
	case OpConst:
		return "const " + p.ty(o.Type) + " " + p.Types.ConstString(o.Const)
	case OpImm:
		return p.ty(o.Type) + " " + strconv.FormatUint(o.Bits, 10)
	}
	return "?"
}

func (p *Printer) ops(b *Body, r Range) string {
	var parts []string
	for _, o := range b.Operands[r.Start:r.End()] {
		parts = append(parts, p.op(b, o))
	}
	return strings.Join(parts, ", ")
}

func (p *Printer) fn(b *Body, f FnRef) string {
	s := p.item(f.Item)
	if f.Args.Len > 0 {
		var parts []string
		for _, a := range b.GenericArgs[f.Args.Start:f.Args.End()] {
			parts = append(parts, p.Types.GenericArgString(a, nil))
		}
		s += "[" + strings.Join(parts, ", ") + "]"
	}
	return s
}

var binNames = [...]string{"Add", "Sub", "Mul", "Div", "Rem", "AddWrap", "SubWrap", "MulWrap", "Shl", "Shr",
	"BitAnd", "BitOr", "BitXor", "Eq", "Ne", "Lt", "Le", "Gt", "Ge", "AddNoWrap", "SubNoWrap", "MulNoWrap"}

func (p *Printer) rvalue(b *Body, r *Rvalue) string {
	borrow := func() string {
		s := "&"
		if r.Borrow == Exclusive {
			s = "mut "
		}
		switch r.Origin {
		case OriginLoopHolder:
			s += "holder(loop" + strconv.Itoa(int(r.Loop)) + ") "
		case OriginLoopElement:
			s += "elem(loop" + strconv.Itoa(int(r.Loop)) + ") "
		}
		return s
	}
	switch r.Kind {
	case RvUse:
		return p.op(b, r.A)
	case RvRef:
		return borrow() + p.PlaceString(b, r.Place)
	case RvSlice:
		lo, hi := "", ""
		if r.HasLo {
			lo = p.op(b, r.A)
		}
		if r.HasHi {
			hi = p.op(b, r.B)
		}
		return borrow() + p.PlaceString(b, r.Place) + "[" + lo + ".." + hi + "]"
	case RvUnary:
		return [...]string{"Neg", "Not", "BitNot"}[r.Un%3] + "(" + p.op(b, r.A) + ")"
	case RvBinary:
		return binNames[int(r.Bin)%len(binNames)] + "(" + p.op(b, r.A) + ", " + p.op(b, r.B) + ")"
	case RvCast:
		return [...]string{"Lossless", "Checked", "Trunc", "WidenErrors", "Narrow"}[r.Cast%5] + "[" + p.ty(r.Type) + "](" + p.op(b, r.A) + ")"
	case RvAggregate:
		var head string
		switch r.Agg {
		case AggStruct, AggError:
			head = p.item(r.Item)
		case AggEnum:
			head = p.item(r.Item) + ".variant" + strconv.Itoa(int(r.Variant))
		case AggRecord, AggArray:
			head = p.ty(r.Type)
		case AggSome:
			head = "some"
		case AggOk:
			head = "ok"
		case AggErr:
			head = "err"
		case AggNone:
			head = "none"
		}
		return head + "(" + p.ops(b, r.Ops) + ")"
	case RvRepeat:
		return "[" + p.op(b, r.A) + "; " + p.Types.ConstArgString(r.Len, nil) + "]"
	case RvLen:
		return "Len(" + p.PlaceString(b, r.Place) + ")"
	case RvDiscriminant:
		return "Discriminant(" + p.PlaceString(b, r.Place) + ")"
	case RvFnRef:
		return "fn " + p.fn(b, r.Fn)
	case RvGlobalRef:
		return "&global " + p.item(r.Item)
	case RvConstRef:
		return "const " + p.item(r.Item)
	case RvSizeOf:
		return "SizeOf[" + p.ty(r.Type) + "]"
	case RvAlignOf:
		return "AlignOf[" + p.ty(r.Type) + "]"
	case RvFieldCount:
		return "FieldCount[" + p.ty(r.Type) + "]"
	case RvTypeIs:
		return "TypeIs($" + strconv.Itoa(int(r.Param)) + ", " + p.ty(r.Type) + ")"
	case RvFieldName:
		return "FieldName(" + strconv.Itoa(int(r.Field.Index)) + ")"
	case RvConstArg:
		return "ConstArg[" + p.ty(r.Type) + "](" + p.Types.ConstArgString(r.Len, nil) + ")"
	}
	return "?"
}

func (p *Printer) stmt(b *Body, st *Statement) string {
	switch st.Kind {
	case StAssign:
		return p.PlaceString(b, st.Place) + " = " + p.rvalue(b, &st.Rvalue)
	case StScopeEnd:
		return "scope_end _" + strconv.Itoa(int(st.Local))
	case StJoin:
		if st.Cancel {
			return "join _" + strconv.Itoa(int(st.Local)) + " cancel"
		}
		return "join _" + strconv.Itoa(int(st.Local))
	case StNop:
		return "nop"
	case StAsm:
		return p.asm(b, st)
	case StConsume:
		return "consume " + p.PlaceString(b, st.Place)
	}
	return "?"
}

func bb(id BlockID) string { return "bb" + strconv.Itoa(int(id)) }

func (p *Printer) term(b *Body, t *Terminator) string {
	switch t.Kind {
	case TermGoto:
		if t.ElseExit {
			return "goto " + bb(t.Target) + " else-exit"
		}
		return "goto " + bb(t.Target)
	case TermSwitch:
		var parts []string
		for _, c := range b.Cases[t.Cases.Start:t.Cases.End()] {
			parts = append(parts, strconv.FormatUint(c.Value, 10)+": "+bb(c.Target))
		}
		if t.Default != NoBlock {
			parts = append(parts, "_: "+bb(t.Default))
		}
		return "switch " + p.op(b, t.On) + " [" + strings.Join(parts, ", ") + "]"
	case TermCall:
		s := p.PlaceString(b, t.Dest) + " = call " + p.callee(b, t) + " -> " + bb(t.Target)
		if t.HasErr {
			s += " err " + [...]string{"try", "else", "match", "must"}[t.Err.Kind%4] + " _" + strconv.Itoa(int(t.Err.Error)) + " -> " + bb(t.Err.Block)
		}
		if t.Spawn != NoLocal {
			s += " spawn _" + strconv.Itoa(int(t.Spawn))
		}
		return s
	case TermTailCall:
		return "tailcall " + p.callee(b, t)
	case TermTypeTest:
		return "typetest $" + strconv.Itoa(int(t.Param)) + " is " + p.ty(t.Type) + " -> " + bb(t.Yes) + ", " + bb(t.No)
	case TermOnerrGuard:
		return "onerr_guard _" + strconv.Itoa(int(t.Value)) + " -> " + bb(t.Yes) + ", " + bb(t.No)
	case TermReturn:
		s := "return " + [...]string{"normal", "fail", "try", "else", "maybe-error"}[t.Exit%5]
		if t.NewError {
			s += " new-error"
		}
		return s
	case TermPanic:
		if t.HasMsg {
			return "panic " + t.Panic.String() + " " + p.op(b, t.Msg)
		}
		return "panic " + t.Panic.String()
	case TermUnreachable:
		return "unreachable"
	}
	return "?"
}

// callee prints a call's callee and arguments, "Load(copy _1)", for TermCall
// and TermTailCall.
func (p *Printer) callee(b *Body, t *Terminator) string {
	var callee string
	switch t.Callee.Kind {
	case CallDirect:
		callee = p.fn(b, t.Callee.Fn)
	case CallInterface:
		callee = p.item(t.Callee.Iface) + ".method" + strconv.Itoa(int(t.Callee.Method)) + "[" + p.ty(t.Callee.SelfType) + "]"
	case CallValue:
		callee = "(" + p.op(b, t.Callee.Value) + ")"
	case CallBuiltin:
		callee = "builtin " + [...]string{"?", "EvalArena", "Quote"}[min(int(t.Callee.Builtin), 2)]
	}
	var args []string
	for _, a := range b.Args[t.Args.Start:t.Args.End()] {
		m := ""
		if a.Mode == ArgReborrow {
			m = "reborrow "
		}
		args = append(args, m+p.op(b, a.Operand))
	}
	return callee + "(" + strings.Join(args, ", ") + ")"
}
