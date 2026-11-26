// Why: a quote may splice a float or a fn value, which need their own
// spellings: a float must read back to the same bits, and a fn value must
// name its function wherever the output lands.
//
// What: spliceValue (the float and fn-value cases of splice), floatToks,
// fnToksOf and argToks.
//
// How: a float splices as the shortest decimal literal that reads back to
// the same bits (strconv's shortest formatting), with a minus token for a
// negative value. NaN and the infinities have no literal and fail the run.
// A fn value splices as a pinned reference to its function: qualified by
// its module, or by its type for an inherent function, with any generic
// arguments.

package gen

import (
	"math"
	"strconv"
	"strings"

	"mk/internal/ast"
	"mk/internal/eval"
	"mk/internal/types"
)

// spliceValue is the hole's tokens for a float or fn operand; ok is false for
// any other type.
func (r *run) spliceValue(m *eval.Machine, v eval.Value, t *types.Type) ([]tok, bool, *types.EvalError) {
	switch t.Kind {
	case types.KFloat:
		bits := int(t.Bits)
		var f float64
		if bits == 32 {
			f = float64(math.Float32frombits(uint32(m.Uint(v))))
		} else {
			f = math.Float64frombits(m.Uint(v))
		}
		ts, ok := floatToks(f, bits)
		if !ok {
			return nil, true, fail("gen: splice of %s, which no float literal spells", strconv.FormatFloat(f, 'g', -1, bits))
		}
		return ts, true, r.chargeToks(m, len(ts))
	case types.KFn:
		fn, err := m.Fn(v)
		if err != nil {
			return nil, true, err
		}
		ts, ferr := r.fnToksOf(fn)
		if ferr != nil {
			return nil, true, ferr
		}
		if err := r.spellFailed(); err != nil {
			return nil, true, err // a type argument naming such a fn type
		}
		return ts, true, r.chargeToks(m, len(ts))
	}
	return nil, false, nil
}

// floatToks spells f as the shortest decimal literal that parses back, at
// bits, to the same value: a minus token for a negative value (-0.0
// included), and a literal with digits on both sides of its point or an
// exponent. NaN and the infinities have no literal: false.
func floatToks(f float64, bits int) ([]tok, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, false
	}
	var ts []tok
	if math.Signbit(f) {
		ts = append(ts, tok{kind: ast.TokMinus})
		f = -f
	}
	s := strings.Replace(strconv.FormatFloat(f, 'g', -1, bits), "e+", "e", 1)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return append(ts, tok{kind: ast.TokFloat, text: s}), true
}

// fnToksOf spells the function a fn value names as a pinned reference: a
// function of another module qualified by its module, an inherent function
// by its type (`Point.Len`, `List[int].Len`), a generic instance with its
// arguments. A lifted fn literal or an impl method has no name to spell.
func (r *run) fnToksOf(fn types.FnRef) ([]tok, *types.EvalError) {
	it := r.c.Items.Get(fn.Item)
	switch {
	case it.Owner == types.NoItem && it.OwnerPrim == types.NoType:
		return append(r.itemToks(fn.Item), r.argToks(fn.Args)...), nil
	case it.Owner != types.NoItem && isTypeItem(r.c.Items.Get(it.Owner).Kind):
		n := min(len(ownerGenerics(r.c.Items.Get(it.Owner))), len(fn.Args))
		ts := append(r.itemToks(it.Owner), r.argToks(fn.Args[:n])...)
		ts = append(ts, tok{kind: ast.TokDot}, tok{kind: ast.TokIdent, text: r.sym(it.Name), origin: OriginSpliced})
		return append(ts, r.argToks(fn.Args[n:])...), nil
	}
	return nil, fail("gen: splice of a fn value of %s, which has no name to spell", it.Key)
}

// argToks spells generic arguments in brackets, nothing for none.
func (r *run) argToks(args []types.GenericArg) []tok {
	if len(args) == 0 {
		return nil
	}
	ts := []tok{{kind: ast.TokLBracket}}
	for i, a := range args {
		if i > 0 {
			ts = append(ts, tok{kind: ast.TokComma})
		}
		if a.IsConst {
			ts = append(ts, r.constArgToks(a.Const)...)
		} else {
			ts = append(ts, r.typeToks(a.Type)...)
		}
	}
	return append(ts, tok{kind: ast.TokRBracket})
}

// ownerGenerics are a type item's own generic parameters.
func ownerGenerics(it *types.Item) []types.GenericParam {
	switch d := it.Decl.(type) {
	case *types.StructDecl:
		return d.Generics
	case *types.EnumDecl:
		return d.Generics
	case *types.InterfaceDecl:
		return d.Generics
	}
	return nil
}

func isTypeItem(k types.ItemKind) bool {
	switch k {
	case types.KindStruct, types.KindEnum, types.KindError, types.KindInterface:
		return true
	}
	return false
}
