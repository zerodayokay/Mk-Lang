// Why: a fn type or method whose result is an error union held as a value
// (an alias's E!T, which cannot fail) has no written spelling: a written
// E!T result is one that fails, so spliced text would name another type.
// What: TestValueResultUnspelled.
// How: a generator splices the type of a field `fn(&R): R`, and one
// forwards Method.Signature of an interface method returning R; each run
// fails at the gen line (E0901) and emits nothing, while the same shapes
// with a failing result still spell `Bad!int`.

package gen

import (
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

func TestValueResultUnspelled(t *testing.T) {
	w := newWorld(t)
	_, badT := w.errorType("lib", "Bad", true)
	bad := w.tab.Get(badT).Errors
	r := w.tab.ErrorUnion(bad, types.Int)
	value := w.tab.Fn(types.FnType{Params: []types.FnParam{{Type: w.tab.Ref(r)}}, Result: r, Errors: types.EmptyErrors})
	failing := w.tab.Fn(types.FnType{Params: []types.FnParam{{Type: w.tab.Ref(r)}}, Result: types.Int, Errors: bad})

	field := func(n string, ft types.TypeID) *Output {
		_, s := w.structType("main", "S"+n, false, "f", ft)
		g := w.gen1("F"+n, func(x *bb, tr, gr mir.LocalID) mir.BlockID {
			fr, b := x.fieldAt(0, tr, 0)
			ty, b2 := x.api(b, "Field:Type", cp(fr))
			tyr := x.ref(ty, false)
			x.flush(b2)
			name, b3 := x.api(b2, "Type:Name", cp(tyr))
			return x.emit(b3, "type Fty = $0", cp(name))
		})
		return w.context(1).Run(w.module("main"), []Line{w.line(0, "lib.F"+n, g, typeArg(s))})
	}
	out := field("1", value)
	wantCodes(t, out, "E0901")
	if d := out.Diags[0]; !strings.Contains(d.Message, "no spelling in generated code") || out.File != nil {
		t.Fatalf("fn type with a value result: %q, output %v", d.Message, out.File != nil)
	}
	out = field("2", failing)
	wantCodes(t, out)
	if !strings.Contains(out.Text(), "type Fty = fn(&(lib.Bad!int)): lib.Bad!int") {
		t.Fatalf("failing fn type:\n%s", out.Text())
	}

	method := func(n string, result types.TypeID, errs types.ErrorSetID) *Output {
		iface := w.item("app", "Src"+n, types.KindInterface, &types.InterfaceDecl{})
		w.items.Get(iface).Exported = true
		w.views[iface] = &DeclView{Item: iface, Kind: types.KindInterface, Exported: true, Methods: []MethodView{{Sig: types.Signature{
			Name: w.in.Intern("Get"), Receiver: types.ByShared,
			Params: []types.Param{{Name: w.in.Intern("self"), Type: w.tab.Ref(w.tab.SelfType())}},
			Result: result, Errors: errs}}}}
		g := w.gen1("Fwd"+n, func(x *bb, tr, gr mir.LocalID) mir.BlockID {
			ms, b := x.api(0, "Type:Methods", cp(tr))
			mt := w.named(w.known.Method)
			idx, m := x.local(types.Int), x.local(mt)
			x.assign(b, lp(idx), use(mir.Imm(types.Int, 0)))
			x.assign(b, lp(m), use(mir.Copy(x.b.Place(ms, mir.Projection{Kind: mir.ProjIndex, A: uint32(idx), Type: mt}))))
			mr := x.ref(m, false)
			x.flush(b)
			s, b2 := x.api(b, "Method:Signature", cp(mr))
			return x.emit(b2, "S.F$0 {\n}", cp(s))
		})
		return w.context(1).Run(w.module("main"), []Line{w.line(1, "lib.Fwd"+n, g, Arg{Kind: ArgInterface, Item: iface, Span: w.span()})})
	}
	out = method("1", r, types.EmptyErrors)
	wantCodes(t, out, "E0901")
	if d := out.Diags[0]; !strings.Contains(d.Message, "the method `Get` has no spelling") {
		t.Fatalf("method with a value result: %q", d.Message)
	}
	out = method("2", types.Int, bad)
	wantCodes(t, out)
	if !strings.Contains(out.Text(), "S.F(&self): lib.Bad!int {") {
		t.Fatalf("failing method:\n%s", out.Text())
	}
}
