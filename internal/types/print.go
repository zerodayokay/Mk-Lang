// Why: diagnostics (facts.type, facts.signature) and goldens show types, and
// a type shown must read as Mk source and never depend on an ID.
//
// What: Table.TypeString, ResultString and ConstArgString, with the writers
// they share (writeType, writeOperand, writeErrors, writeResult,
// writeConstArg).
//
// How: a recursive writer over the type table: named types by their item's
// name, type parameters by name, const arguments by value, and parentheses
// where a prefix operator meets "!" or "|", so the text parses back to the
// same type. A result that cannot fail and is an error union has no
// source spelling but an alias or a type parameter, so it prints as the
// union marked " (cannot fail)".

package types

import (
	"strconv"
	"strings"
)

// ResultString spells a function's or fn type's result: E!T for one that
// can fail; for one that cannot fail whose result is an error union (a
// result type parameter bound to one, or an alias of one), the union
// followed by " (cannot fail)", since E!T written as a result fails and
// the two must not print alike. "" is a void result that cannot fail.
func (t *Table) ResultString(result TypeID, errs ErrorSetID, gs []GenericParam) string {
	if result == Void && errs == EmptyErrors {
		return ""
	}
	var b strings.Builder
	t.writeResult(&b, result, errs, gs)
	return b.String()
}

// writeResult is ResultString's writer.
func (t *Table) writeResult(b *strings.Builder, result TypeID, errs ErrorSetID, gs []GenericParam) {
	if errs != EmptyErrors {
		t.writeErrors(b, errs, gs)
		b.WriteByte('!')
		t.writeType(b, result, gs)
		return
	}
	t.writeType(b, result, gs)
	if ty, ok := t.types.Get(uint32(result)); ok && result != NoType && ty.Kind == KErrorUnion {
		b.WriteString(" (cannot fail)")
	}
}

// TypeString spells t as Mk source: i32, &T, mut []T, [4]u8, ?T, A|B!T,
// (a: i32, b: str), fn(&i32): bool, *u8, List[int]. A KNamed type prints its
// item's name; a KParam prints its name from generics when given (else $i).
// The output depends on names and values only, never on IDs, except KInfer
// ("?n"): a CAValue argument is spelled by its value (ConstString), and an
// error union under a prefix operator is parenthesized, ?(E!T), and so is an
// error-set value of several errors, ?(A|B), so the text parses back to
// the same type ("!" and "|" bind looser than every prefix).
func (t *Table) TypeString(id TypeID, generics []GenericParam) string {
	var b strings.Builder
	t.writeType(&b, id, generics)
	return b.String()
}

func (t *Table) writeType(b *strings.Builder, id TypeID, gs []GenericParam) {
	if id == NoType {
		b.WriteString("<none>")
		return
	}
	ty := t.Get(id)
	switch ty.Kind {
	case KInt:
		if ty.Signed {
			b.WriteByte('i')
		} else {
			b.WriteByte('u')
		}
		b.WriteString(strconv.Itoa(int(ty.Bits)))
	case KIntPtr:
		if ty.Signed {
			b.WriteString("int")
		} else {
			b.WriteString("uint")
		}
	case KFloat:
		b.WriteString("f" + strconv.Itoa(int(ty.Bits)))
	case KBool:
		b.WriteString("bool")
	case KChar:
		b.WriteString("char")
	case KVoid:
		b.WriteString("void")
	case KNever:
		b.WriteString("never")
	case KStr:
		b.WriteString("str")
	case KRef:
		b.WriteByte('&')
		t.writeOperand(b, ty.Elem, gs, false)
	case KMut:
		b.WriteString("mut ")
		t.writeOperand(b, ty.Elem, gs, true)
	case KSlice:
		b.WriteString("[]")
		t.writeOperand(b, ty.Elem, gs, false)
	case KMutSlice:
		b.WriteString("mut []")
		t.writeOperand(b, ty.Elem, gs, false)
	case KRawPtr:
		b.WriteByte('*')
		t.writeOperand(b, ty.Elem, gs, false)
	case KArray:
		b.WriteByte('[')
		t.writeConstArg(b, ty.Len, gs)
		b.WriteByte(']')
		t.writeOperand(b, ty.Elem, gs, false)
	case KOptional:
		b.WriteByte('?')
		t.writeOperand(b, ty.Elem, gs, false)
	case KErrorUnion:
		t.writeErrors(b, ty.Errors, gs)
		b.WriteByte('!')
		t.writeType(b, ty.Elem, gs)
	case KErrorSet:
		t.writeErrors(b, ty.Errors, gs)
	case KRecord:
		b.WriteByte('(')
		for i, f := range ty.Fields {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(t.in.Str(f.Name))
			b.WriteString(": ")
			t.writeType(b, f.Type, gs)
		}
		b.WriteByte(')')
	case KFn:
		b.WriteString("fn(")
		for i, p := range ty.Fn.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			t.writeType(b, p.Type, gs)
			if p.Into != NoParam {
				b.WriteString(" into $" + strconv.Itoa(int(p.Into)))
			}
		}
		b.WriteByte(')')
		if ty.Fn.Result != Void || ty.Fn.Errors != EmptyErrors {
			b.WriteString(": ")
			t.writeResult(b, ty.Fn.Result, ty.Fn.Errors, gs)
		}
	case KNamed:
		it := t.items.Get(ty.Item)
		b.WriteString(t.in.Str(it.Name))
		if len(ty.Args) > 0 {
			b.WriteByte('[')
			for i, a := range ty.Args {
				if i > 0 {
					b.WriteString(", ")
				}
				if a.IsConst {
					t.writeConstArg(b, a.Const, gs)
				} else {
					t.writeType(b, a.Type, gs)
				}
			}
			b.WriteByte(']')
		}
	case KParam:
		if int(ty.Index) < len(gs) {
			b.WriteString(t.in.Str(gs[ty.Index].Name))
		} else {
			b.WriteString("$" + strconv.Itoa(int(ty.Index)))
		}
	case KSelf:
		b.WriteString("Self")
	case KInfer:
		b.WriteString("?" + strconv.Itoa(int(ty.Index)))
	case KPoison:
		b.WriteString("<error>")
	default:
		b.WriteString("<" + ty.Kind.String() + ">")
	}
}

// writeOperand writes the operand of a prefix type operator, in parentheses
// when it is an error union or an error-set value of more than one error (or,
// after mut, a slice: mut ([]T) is KMut of a KSlice, distinct from mut []T).
func (t *Table) writeOperand(b *strings.Builder, elem TypeID, gs []GenericParam, afterMut bool) {
	k := t.Kind(elem)
	union := k == KErrorSet && len(t.Members(t.Get(elem).Errors)) > 1
	if k == KErrorUnion || union || (afterMut && k == KSlice) {
		b.WriteByte('(')
		t.writeType(b, elem, gs)
		b.WriteByte(')')
		return
	}
	t.writeType(b, elem, gs)
}

func (t *Table) writeErrors(b *strings.Builder, es ErrorSetID, gs []GenericParam) {
	for i, m := range t.Members(es) {
		if i > 0 {
			b.WriteByte('|')
		}
		if m.IsParam {
			t.writeType(b, t.Param(m.Param), gs)
		} else {
			b.WriteString(t.in.Str(t.items.Get(m.Item).Name))
		}
	}
}

// ConstArgString spells a const argument as Mk source: a const parameter by
// its name from generics (else $i), a value by ConstString, SizeOf[T]() as
// written. The MIR printer uses it for repeat lengths and const generics.
func (t *Table) ConstArgString(c ConstArg, generics []GenericParam) string {
	var b strings.Builder
	t.writeConstArg(&b, c, generics)
	return b.String()
}

func (t *Table) writeConstArg(b *strings.Builder, c ConstArg, gs []GenericParam) {
	switch c.Kind {
	case CAParam:
		t.writeType(b, t.Param(c.Param), gs)
	case CAValue:
		t.writeConst(b, c.Value)
	case CASizeOf:
		b.WriteString("SizeOf[")
		t.writeType(b, c.Type, gs)
		b.WriteString("]()")
	default:
		b.WriteString("_")
	}
}
