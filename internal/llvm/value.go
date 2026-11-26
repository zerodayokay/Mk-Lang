// Why: IR text must be the same bytes for the same values on every run and
// every host, so constants need one exact spelling each.
//
// What: Value and its constants: integers in decimal, double as 0x and 16
// hex digits of its bits, float as the hex of the double equal to it,
// strings as c"..." with no NUL, and the references GlobalRef and FuncRef.
//
// How: results, parameters and numeric constants carry a number and print
// straight into the block's text; the rest carry their text.

package llvm

import (
	"strconv"
	"strings"
)

// valKind says how a Value's reference is printed. Results, parameters,
// integer and float constants carry a number and are printed straight into the
// block's text, so building an operand allocates nothing; the rest carry their
// text.
type valKind uint8

const (
	vkText  valKind = iota // ref is the text
	vkLocal                // %v<n>
	vkParam                // %p<n>
	vkInt                  // n in signed decimal
	vkFP                   // 0x + 16 upper-case hex digits of uint64(n)
)

// Value is an operand: a type and its printed reference (%v3, @"name", a
// constant). The zero Value is "no value" (the result of a void call).
// Values are comparable; two constants of one type are equal exactly when
// they print equally.
type Value struct {
	Ty   Type
	ref  string
	kind valKind
	n    int64
}

// Ref is the printed reference without the type: %v3, @f, 42, c"ab".
func (v Value) Ref() string {
	if v.kind == vkText {
		return v.ref
	}
	return string(appendRef(nil, v))
}

// String is the typed operand: "i32 %v3", "ptr @g".
func (v Value) String() string { return string(appendTyped(nil, v)) }

// isConst reports whether v is a constant (not an instruction result or a
// parameter).
func (v Value) isConst() bool {
	switch v.kind {
	case vkInt, vkFP:
		return true
	case vkText:
		return v.ref != "" && v.ref[0] != '%'
	}
	return false
}

// IsZero reports whether v is the zero "no value".
func (v Value) IsZero() bool { return v == Value{} }

func (m *Module) checkValue(v Value, what string) {
	if v.Ty.tab != m.types || (v.kind == vkText && v.ref == "") {
		panic("llvm: " + what + ": zero value or value from another module")
	}
}

// ConstInt is an integer constant of type t, printed as a signed decimal for
// t's width: bits is truncated to the width (for widths up to 64) and read
// as two's complement; for wider types bits is sign-extended from 64 bits.
// An i1 prints as true or false.
func (m *Module) ConstInt(t Type, bits uint64) Value {
	m.checkType(t, "ConstInt")
	w := t.Bits()
	if !t.IsInt() {
		panic("llvm: ConstInt of " + t.String())
	}
	if w == 1 {
		if bits&1 == 1 {
			return Value{Ty: t, ref: "true"}
		}
		return Value{Ty: t, ref: "false"}
	}
	return Value{Ty: t, kind: vkInt, n: signedBits(w, bits)}
}

// signedBits is bits truncated to w bits (w <= 64) and read as two's
// complement; for wider types bits is read as a signed 64-bit value.
func signedBits(w int, bits uint64) int64 {
	if w < 64 {
		return int64(bits<<(64-w)) >> (64 - w)
	}
	return int64(bits)
}

// ConstSInt is ConstInt(t, uint64(v)): a signed integer constant.
func (m *Module) ConstSInt(t Type, v int64) Value { return m.ConstInt(t, uint64(v)) }

// ConstF64 is a double constant with the given IEEE bits, printed in the
// exact hex form 0x + 16 upper-case hex digits.
func (m *Module) ConstF64(bits uint64) Value {
	return Value{Ty: m.Double(), kind: vkFP, n: int64(bits)}
}

// ConstF32 is a float constant with the given IEEE bits, printed as the hex
// of the double that equals it exactly. The widening is done on
// bits, so NaN payloads, the quiet bit, -0 and the infinities are kept.
func (m *Module) ConstF32(bits uint32) Value {
	return Value{Ty: m.Float(), kind: vkFP, n: int64(f32ToF64Bits(bits))}
}

// f32ToF64Bits widens an IEEE single to the IEEE double of equal value,
// bit for bit, without the FPU (which would quiet a signalling NaN).
func f32ToF64Bits(b uint32) uint64 {
	sign := uint64(b>>31) << 63
	exp := int((b >> 23) & 0xFF)
	man := uint64(b & 0x7FFFFF)
	switch {
	case exp == 0xFF: // infinity or NaN, payload kept
		return sign | 0x7FF<<52 | man<<29
	case exp == 0 && man == 0:
		return sign
	case exp == 0: // subnormal single: normalize
		e := -126
		for man&0x800000 == 0 {
			man <<= 1
			e--
		}
		man &= 0x7FFFFF
		return sign | uint64(e+1023)<<52 | man<<29
	}
	return sign | uint64(exp-127+1023)<<52 | man<<29
}

// Null is the null pointer, ptr null.
func (m *Module) Null() Value { return Value{Ty: m.Ptr(), ref: "null"} }

// Undef is undef of type t.
func (m *Module) Undef(t Type) Value { m.checkType(t, "Undef"); return Value{Ty: t, ref: "undef"} }

// Poison is poison of type t.
func (m *Module) Poison(t Type) Value { m.checkType(t, "Poison"); return Value{Ty: t, ref: "poison"} }

// ZeroInit is the all-zero value of t: 0, false, null, 0x0 for scalars and
// zeroinitializer for aggregates and vectors.
func (m *Module) ZeroInit(t Type) Value {
	m.checkType(t, "ZeroInit")
	switch t.info().kind {
	case tkInt:
		return m.ConstInt(t, 0)
	case tkPtr:
		return m.Null()
	case tkFloat, tkDouble:
		return Value{Ty: t, ref: "0x0000000000000000"}
	case tkArray, tkStruct, tkVector:
		return Value{Ty: t, ref: "zeroinitializer"}
	}
	panic("llvm: ZeroInit of " + t.String())
}

// Bytes is the [n x i8] constant c"..." holding exactly b, with \XX escapes
// and no implicit NUL; zeroinitializer when every byte is zero, so a zeroed
// table of megabytes is not three times its size in text.
func (m *Module) Bytes(b []byte) Value {
	t := m.Array(uint64(len(b)), m.I(8))
	if len(b) > 0 && allZero(b) {
		return Value{Ty: t, ref: "zeroinitializer"}
	}
	return Value{Ty: t, ref: `c"` + escape(string(b)) + `"`}
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// ConstStruct is a literal struct constant { v, ... } or <{ v, ... }> whose
// type is the struct of the values' types.
func (m *Module) ConstStruct(packed bool, vs ...Value) Value {
	tys := make([]Type, len(vs))
	for i, v := range vs {
		m.checkValue(v, "ConstStruct")
		tys[i] = v.Ty
	}
	t := m.Struct(packed, tys...)
	var sb strings.Builder
	if packed {
		sb.WriteString("<")
	}
	if len(vs) == 0 {
		sb.WriteString("{}")
	} else {
		sb.WriteString("{ ")
		for i, v := range vs {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(v.String())
		}
		sb.WriteString(" }")
	}
	if packed {
		sb.WriteString(">")
	}
	return Value{Ty: t, ref: sb.String()}
}

// ConstArray is the constant [elem v, ...] of type [len(vs) x elem]; every
// value has type elem.
func (m *Module) ConstArray(elem Type, vs ...Value) Value {
	t := m.Array(uint64(len(vs)), elem)
	if len(vs) == 0 {
		return Value{Ty: t, ref: "zeroinitializer"}
	}
	var sb strings.Builder
	sb.WriteString("[")
	for i, v := range vs {
		m.checkValue(v, "ConstArray")
		if v.Ty != elem {
			panic("llvm: ConstArray element " + v.String() + " is not " + elem.String())
		}
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(v.String())
	}
	sb.WriteString("]")
	return Value{Ty: t, ref: sb.String()}
}

// GlobalRef is the address of g, ptr @g; g belongs to m (a global of
// another Module would print a reference to an undefined symbol).
func (m *Module) GlobalRef(g *Global) Value {
	if g == nil || g.m != m {
		panic("llvm: GlobalRef of a global of another module")
	}
	return Value{Ty: m.Ptr(), ref: g.ref}
}

// FuncRef is the address of f, ptr @f; f belongs to m. Codegen
// declares a function of another pack in this pack with Declare.
func (m *Module) FuncRef(f *Function) Value {
	if f == nil || f.m != m {
		panic("llvm: FuncRef of a function of another module")
	}
	return Value{Ty: m.Ptr(), ref: f.ref}
}

// ConstGEP is base plus offset bytes as a constant expression,
// getelementptr inbounds (i8, ptr @g, i64 k); base is a pointer constant.
func (m *Module) ConstGEP(base Value, offset uint64) Value {
	m.checkValue(base, "ConstGEP")
	if !base.Ty.IsPtr() {
		panic("llvm: ConstGEP base " + base.String())
	}
	return Value{Ty: m.Ptr(), ref: "getelementptr inbounds (i8, " + base.String() + ", i64 " +
		strconv.FormatInt(int64(offset), 10) + ")"}
}

// ConstIntToPtr is the constant pointer with address a, inttoptr (i64 a to
// ptr); codegen uses it for the dangling aligned address of a zero-sized
// local. LLVM 18 to 21 all keep this constant expression.
func (m *Module) ConstIntToPtr(a uint64) Value {
	return Value{Ty: m.Ptr(), ref: "inttoptr (i64 " + strconv.FormatInt(int64(a), 10) + " to ptr)"}
}
