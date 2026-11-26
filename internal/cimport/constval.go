// Why: C headers define constants not only as #define macros, but also as
// static const variables (such as 64-bit Vulkan flags) and INITGUID
// DEFINE_GUID / static const UUID struct constants; binding them as Mk consts
// avoids linker dependencies and gives safe compile-time values.
//
// What: emitConstVar, evalConstInit, evalScalar and evalRecordInit, which
// evaluate top-level const VarDecl AST initializers from Clang's JSON AST.
//
// How: integer, bool, float, enum and transparent-struct initializers are
// evaluated recursively over the decoded AST nodes (literals, casts, unary and
// binary operators, InitListExpr with array_filler, and DeclRefExpr to earlier
// enumerators or const variables) using target integer widths and signedness.

package cimport

import (
	"math"
	"strconv"
	"strings"
)

// constScalar is one evaluated scalar C constant.
type constScalar struct {
	u       uint64
	signed  bool
	isFloat bool
	f       float64
}

// emitConstVar emits a top-level const VarDecl with an initializer as an Mk
// const, returning false if its type or initializer cannot be bound as one.
func (e *emitter) emitConstVar(d *decl) bool {
	if d.vt == nil || d.initExpr == nil || d.name == "" {
		return false
	}
	rt := e.resolve(d.vt)
	if rt == nil {
		return false
	}
	switch rt.kind {
	case tBuiltin, tEnum:
	case tRecord:
		if rt.union {
			return false
		}
		rd := e.tagDecl(rt)
		if rd == nil || e.structFields[rd] == nil {
			return false
		}
	default:
		return false
	}
	mkTyp, why := e.mkType(d.vt, d.imp)
	if why != "" || mkTyp == "void" || strings.ContainsAny(mkTyp, "*[(") {
		return false
	}
	lit, sc, hasScalar, ok := e.evalConstInit(d.initExpr, d.vt, d.imp)
	if !ok {
		return false
	}
	e.stats[d.imp].Decls++
	e.stats[d.imp].Direct++
	mkName := e.scopes[d.imp].fresh(pascal(d.name), "Const")
	w := &e.mods[d.imp].items
	w.WriteString("\n// C: const " + d.name + "\n")
	w.WriteString("export const " + mkName + ": " + mkTyp + " = " + lit + "\n")
	if hasScalar {
		if d.id != "" {
			e.constVals[d.id] = sc
		}
		if d.extern {
			e.constValsByName[d.name] = sc
		}
	}
	return true
}

// evalConstInit evaluates AST initializer node n for C type targetType into an
// Mk constant expression string.
func (e *emitter) evalConstInit(n *jnode, targetType *ctype, imp int) (lit string, sc constScalar, hasScalar, ok bool) {
	if n == nil || targetType == nil {
		return "", constScalar{}, false, false
	}
	rt := e.resolve(targetType)
	if rt == nil {
		return "", constScalar{}, false, false
	}
	switch rt.kind {
	case tBuiltin, tEnum:
		v, ok := e.evalScalar(n, 0)
		if !ok {
			return "", constScalar{}, false, false
		}
		mkTyp, why := e.mkType(targetType, imp)
		if why != "" || mkTyp == "void" {
			return "", constScalar{}, false, false
		}
		primTyp := e.scalarPrimMk(rt)
		if primTyp == "" {
			return "", constScalar{}, false, false
		}
		lit, ok := formatScalarMk(v, primTyp)
		if !ok {
			return "", constScalar{}, false, false
		}
		return lit, v, true, true
	case tArray:
		if rt.n < 0 {
			return "", constScalar{}, false, false
		}
		if n.Kind == "ImplicitValueInitExpr" {
			z := &jnode{Kind: "ImplicitValueInitExpr"}
			elemLit, _, _, ok := e.evalConstInit(z, rt.elem, imp)
			if !ok {
				return "", constScalar{}, false, false
			}
			elems := make([]string, rt.n)
			for i := range elems {
				elems[i] = elemLit
			}
			return "[" + strings.Join(elems, ", ") + "]", constScalar{}, false, true
		}
		if n.Kind != "InitListExpr" {
			return "", constScalar{}, false, false
		}
		elems := make([]string, rt.n)
		if fillers := n.fillerNodes(); len(fillers) > 0 {
			filler := &fillers[0]
			explicit := fillers[1:]
			for i := int64(0); i < rt.n; i++ {
				src := filler
				if int(i) < len(explicit) {
					src = &explicit[i]
				}
				el, _, _, ok := e.evalConstInit(src, rt.elem, imp)
				if !ok {
					return "", constScalar{}, false, false
				}
				elems[i] = el
			}
		} else {
			kids := n.children()
			z := &jnode{Kind: "ImplicitValueInitExpr"}
			for i := int64(0); i < rt.n; i++ {
				src := z
				if int(i) < len(kids) {
					src = &kids[i]
				}
				el, _, _, ok := e.evalConstInit(src, rt.elem, imp)
				if !ok {
					return "", constScalar{}, false, false
				}
				elems[i] = el
			}
		}
		return "[" + strings.Join(elems, ", ") + "]", constScalar{}, false, true
	case tRecord:
		if rt.union {
			return "", constScalar{}, false, false
		}
		rd := e.tagDecl(rt)
		if rd == nil {
			return "", constScalar{}, false, false
		}
		s, ok := e.evalRecordInit(n, rd, imp)
		return s, constScalar{}, false, ok
	}
	return "", constScalar{}, false, false
}

// evalRecordInit formats an InitListExpr or ImplicitValueInitExpr for
// transparent struct rd.
func (e *emitter) evalRecordInit(n *jnode, rd *decl, imp int) (string, bool) {
	sfs := e.structFields[rd]
	if sfs == nil || len(sfs) != len(rd.fields) {
		return "", false
	}
	recQual := e.qual(rd, imp)
	var kids []jnode
	if n.Kind == "InitListExpr" {
		kids = n.children()
		if len(kids) != len(rd.fields) {
			return "", false
		}
	} else if n.Kind != "ImplicitValueInitExpr" {
		return "", false
	}
	z := &jnode{Kind: "ImplicitValueInitExpr"}
	parts := make([]string, len(rd.fields))
	for i := range rd.fields {
		f := &rd.fields[i]
		src := z
		if kids != nil {
			src = &kids[i]
		}
		var flit string
		var ok bool
		if f.anon != nil && f.name == "" {
			flit, ok = e.evalRecordInit(src, f.anon, imp)
		} else {
			flit, _, _, ok = e.evalConstInit(src, f.t, imp)
		}
		if !ok {
			return "", false
		}
		parts[i] = sfs[i].name + ": " + flit
	}
	return recQual + "(" + strings.Join(parts, ", ") + ")", true
}

// evalScalar evaluates a scalar constant AST expression.
func (e *emitter) evalScalar(n *jnode, depth int) (constScalar, bool) {
	if n == nil || depth > 64 {
		return constScalar{}, false
	}
	bits, signed, hasType := e.jtypeWidthSign(n.Type)
	switch n.Kind {
	case "ConstantExpr":
		if vs := n.valueStr(); vs != "" {
			if u, ok := parseIntValue(vs, signed); ok {
				return fitScalar(u, bits, signed, hasType), true
			}
		}
		if kids := n.children(); len(kids) == 1 {
			return e.evalScalar(&kids[0], depth+1)
		}
	case "IntegerLiteral":
		vs := n.valueStr()
		if u, ok := parseIntValue(vs, signed); ok {
			return fitScalar(u, bits, signed, hasType), true
		}
	case "CharacterLiteral":
		vs := n.valueStr()
		if u, ok := parseIntValue(vs, true); ok {
			return fitScalar(u, bits, signed, hasType), true
		}
	case "CXXBoolLiteralExpr", "BoolLiteral":
		if n.valueStr() == "true" {
			return constScalar{u: 1, signed: false}, true
		}
		return constScalar{u: 0, signed: false}, true
	case "FloatingLiteral":
		f, err := strconv.ParseFloat(n.valueStr(), 64)
		if err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return constScalar{isFloat: true, f: f}, true
		}
	case "ImplicitValueInitExpr":
		return constScalar{signed: signed}, true
	case "ParenExpr":
		if kids := n.children(); len(kids) == 1 {
			return e.evalScalar(&kids[0], depth+1)
		}
	case "ImplicitCastExpr", "CStyleCastExpr":
		if n.CastKind == "NullToPointer" {
			return constScalar{}, true
		}
		kids := n.children()
		if len(kids) != 1 {
			return constScalar{}, false
		}
		v, ok := e.evalScalar(&kids[0], depth+1)
		if !ok {
			return constScalar{}, false
		}
		if v.isFloat {
			if hasType && !isFloatJType(n.Type, e) {
				if math.IsNaN(v.f) || math.IsInf(v.f, 0) {
					return constScalar{}, false
				}
				if signed {
					if v.f < -9223372036854775808.0 || v.f >= 9223372036854775808.0 {
						return constScalar{}, false
					}
					return fitScalar(uint64(int64(v.f)), bits, signed, true), true
				}
				if v.f < 0 || v.f >= 18446744073709551616.0 {
					return constScalar{}, false
				}
				return fitScalar(uint64(v.f), bits, signed, true), true
			}
			return v, true
		}
		if hasType && isFloatJType(n.Type, e) {
			if v.signed {
				return constScalar{isFloat: true, f: float64(int64(v.u))}, true
			}
			return constScalar{isFloat: true, f: float64(v.u)}, true
		}
		if hasType {
			return fitScalar(v.u, bits, signed, true), true
		}
		return v, true
	case "UnaryOperator":
		kids := n.children()
		if len(kids) != 1 {
			return constScalar{}, false
		}
		v, ok := e.evalScalar(&kids[0], depth+1)
		if !ok {
			return constScalar{}, false
		}
		if v.isFloat {
			switch n.Opcode {
			case "+":
				return v, true
			case "-":
				return constScalar{isFloat: true, f: -v.f}, true
			case "!":
				if v.f == 0 {
					return constScalar{u: 1, signed: true}, true
				}
				return constScalar{u: 0, signed: true}, true
			}
			return constScalar{}, false
		}
		switch n.Opcode {
		case "+":
			return fitScalar(v.u, bits, signed, hasType), true
		case "-":
			return fitScalar(uint64(-int64(v.u)), bits, signed, hasType), true
		case "~":
			return fitScalar(^v.u, bits, signed, hasType), true
		case "!":
			if v.u == 0 {
				return constScalar{u: 1, signed: true}, true
			}
			return constScalar{u: 0, signed: true}, true
		}
	case "BinaryOperator":
		kids := n.children()
		if len(kids) != 2 {
			return constScalar{}, false
		}
		a, okA := e.evalScalar(&kids[0], depth+1)
		b, okB := e.evalScalar(&kids[1], depth+1)
		if !okA || !okB || a.isFloat || b.isFloat {
			return constScalar{}, false
		}
		opSigned := signed
		if !hasType {
			opSigned = a.signed && b.signed
		}
		var out uint64
		switch n.Opcode {
		case "+":
			out = a.u + b.u
		case "-":
			out = a.u - b.u
		case "*":
			out = a.u * b.u
		case "/":
			if b.u == 0 {
				return constScalar{}, false
			}
			if opSigned {
				out = uint64(int64(a.u) / int64(b.u))
			} else {
				out = a.u / b.u
			}
		case "%":
			if b.u == 0 {
				return constScalar{}, false
			}
			if opSigned {
				out = uint64(int64(a.u) % int64(b.u))
			} else {
				out = a.u % b.u
			}
		case "<<":
			if b.u >= 64 {
				return constScalar{}, false
			}
			out = a.u << b.u
		case ">>":
			if b.u >= 64 {
				return constScalar{}, false
			}
			if a.signed {
				out = uint64(int64(a.u) >> b.u)
			} else {
				out = a.u >> b.u
			}
		case "&":
			out = a.u & b.u
		case "|":
			out = a.u | b.u
		case "^":
			out = a.u ^ b.u
		case "==":
			if a.u == b.u {
				out = 1
			}
		case "!=":
			if a.u != b.u {
				out = 1
			}
		case "<":
			if opSigned && int64(a.u) < int64(b.u) || !opSigned && a.u < b.u {
				out = 1
			}
		case "<=":
			if opSigned && int64(a.u) <= int64(b.u) || !opSigned && a.u <= b.u {
				out = 1
			}
		case ">":
			if opSigned && int64(a.u) > int64(b.u) || !opSigned && a.u > b.u {
				out = 1
			}
		case ">=":
			if opSigned && int64(a.u) >= int64(b.u) || !opSigned && a.u >= b.u {
				out = 1
			}
		case "&&":
			if a.u != 0 && b.u != 0 {
				out = 1
			}
		case "||":
			if a.u != 0 || b.u != 0 {
				out = 1
			}
		default:
			return constScalar{}, false
		}
		return fitScalar(out, bits, opSigned, hasType), true
	case "ConditionalOperator":
		kids := n.children()
		if len(kids) != 3 {
			return constScalar{}, false
		}
		cond, ok := e.evalScalar(&kids[0], depth+1)
		if !ok {
			return constScalar{}, false
		}
		if (!cond.isFloat && cond.u != 0) || (cond.isFloat && cond.f != 0) {
			return e.evalScalar(&kids[1], depth+1)
		}
		return e.evalScalar(&kids[2], depth+1)
	case "DeclRefExpr":
		if n.ReferencedDecl == nil {
			return constScalar{}, false
		}
		switch n.ReferencedDecl.Kind {
		case "EnumConstantDecl":
			if v, ok := e.u.enumVals[n.ReferencedDecl.ID]; ok {
				if !hasType {
					return constScalar{u: uint64(v), signed: true}, true
				}
				return fitScalar(uint64(v), bits, signed, true), true
			}
		case "VarDecl":
			if v, ok := e.constVals[n.ReferencedDecl.ID]; ok {
				return v, true
			}
			if v, ok := e.constValsByName[n.ReferencedDecl.Name]; ok {
				return v, true
			}
		}
	}
	return constScalar{}, false
}

func parseIntValue(s string, signed bool) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	if u, err := strconv.ParseUint(s, 10, 64); err == nil {
		return u, true
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return uint64(n), true
	}
	if !signed {
		return 0, false
	}
	return 0, false
}

func fitScalar(u uint64, bits int, signed, hasType bool) constScalar {
	if !hasType || bits <= 0 || bits > 64 {
		return constScalar{u: u, signed: signed}
	}
	if bits == 1 {
		if u != 0 {
			return constScalar{u: 1, signed: false}
		}
		return constScalar{u: 0, signed: false}
	}
	if bits < 64 {
		mask := (uint64(1) << uint(bits)) - 1
		u &= mask
		if signed && (u&(uint64(1)<<uint(bits-1))) != 0 {
			u |= ^mask
		}
	}
	return constScalar{u: u, signed: signed}
}

func (e *emitter) jtypeWidthSign(jt *jtype) (bits int, signed, ok bool) {
	if jt == nil {
		return 0, false, false
	}
	qt := jt.Desugared
	if qt == "" {
		qt = jt.QualType
	}
	t := e.resolve(parseType(qt))
	if t == nil {
		return 0, false, false
	}
	switch t.kind {
	case tBuiltin:
		m, sz, ok := e.builtin(t.name)
		if !ok || sz == 0 {
			return 0, false, false
		}
		if m == "bool" {
			return 1, false, true
		}
		return int(sz * 8), m[0] == 'i', true
	case tEnum:
		ed := e.tagDecl(t)
		if ed == nil {
			return 32, true, true
		}
		it := e.enumInt(ed)
		return typeWidthSignMk(it)
	}
	return 0, false, false
}

func isFloatJType(jt *jtype, e *emitter) bool {
	if jt == nil {
		return false
	}
	qt := jt.Desugared
	if qt == "" {
		qt = jt.QualType
	}
	t := e.resolve(parseType(qt))
	return t != nil && t.kind == tBuiltin && (t.name == "float" || t.name == "double")
}

func (e *emitter) scalarPrimMk(rt *ctype) string {
	if rt == nil {
		return ""
	}
	switch rt.kind {
	case tBuiltin:
		m, _, ok := e.builtin(rt.name)
		if ok && m != "void" {
			return m
		}
	case tEnum:
		if ed := e.tagDecl(rt); ed != nil {
			return e.enumInt(ed)
		}
		return "i32"
	}
	return ""
}

func typeWidthSignMk(mk string) (bits int, signed, ok bool) {
	switch mk {
	case "bool":
		return 1, false, true
	case "i8":
		return 8, true, true
	case "u8":
		return 8, false, true
	case "i16":
		return 16, true, true
	case "u16":
		return 16, false, true
	case "i32":
		return 32, true, true
	case "u32":
		return 32, false, true
	case "i64", "int":
		return 64, true, true
	case "u64", "uint":
		return 64, false, true
	}
	return 0, false, false
}

func formatScalarMk(v constScalar, mk string) (string, bool) {
	if v.isFloat {
		if mk != "f32" && mk != "f64" {
			return "", false
		}
		if math.IsInf(v.f, 0) || math.IsNaN(v.f) {
			return "", false
		}
		s := strconv.FormatFloat(v.f, 'f', -1, 64)
		if len(s) > 64 {
			return "", false
		}
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s, true
	}
	if mk == "f32" || mk == "f64" {
		var f float64
		if v.signed {
			f = float64(int64(v.u))
		} else {
			f = float64(v.u)
		}
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s, true
	}
	if mk == "bool" {
		if v.u == 0 {
			return "false", true
		}
		return "true", true
	}
	bits, signed, ok := typeWidthSignMk(mk)
	if !ok {
		return "", false
	}
	fit := fitScalar(v.u, bits, signed, true)
	if !signed {
		return strconv.FormatUint(fit.u, 10), true
	}
	sv := int64(fit.u)
	if sv == math.MinInt64 {
		return "-9223372036854775807 - 1", true
	}
	return strconv.FormatInt(sv, 10), true
}
