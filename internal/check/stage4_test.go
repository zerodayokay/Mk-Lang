// Why: gen reads declarations at stage 4, before check has run, through a
// view the driver adapts into gen.World.
// What: TestGenView_SameModuleFromAST and TestGenView_ValueResult.
// How: builds the view (stage4.go, GenView) of a module stopped before
// stage 5 and reads its declarations, fields and types from the AST alone.

package check

import (
	"testing"

	"mk/internal/ast"
	"mk/internal/source"
	"mk/internal/types"
)

const genViewLib = "export struct P {\n\texport x: int\n\thidden: int\n}\n"

const genViewSrc = "import lib\n\nerror Bad\n\nenum Tag {\n\tA\n\tB(x: int)\n}\n\ntype Id = int\n\n" +
	"export struct User {\n\texport name: str\n\tage: int\n\ttags: []Tag\n\tnext: ?&User\n\tbuf: [4]u8\n\tf: fn(int): Bad!bool\n\tid: Id\n\tp: lib.P\n\tm: Missing\n\topt: ?Missing\n}\n\n" +
	"export interface Shape {\n\tArea(&self): f64\n\tGrow(mut self, k: f64): Bad!void\n}\n\nPair[T] {\n}\n"

func TestGenView_SameModuleFromAST(t *testing.T) {
	w := newWorld(t, true)
	lib := w.add("lib", opt{}, genViewLib)
	b := w.add("m", opt{stopBefore: true, parseErrors: true}, genViewSrc)
	in := &ModuleInput{Module: b.id, Files: b.files, Info: b.info}
	v := w.c.GenView(in)
	tab, sym := w.tab, w.in.Intern
	user := b.item("User")
	d, ok := v.Decl(user, b.id)
	if !ok || d.Kind != types.KindStruct || !d.Exported {
		t.Fatalf("Decl(User) = %+v, %v", d, ok)
	}
	if w.items.Get(user).Decl != nil {
		t.Fatal("GenView wrote User's Decl")
	}
	tag := tab.Named(b.item("Tag"), nil)
	bad := tab.ErrorSet([]types.ErrorMember{{Item: b.item("Bad")}})
	want := []struct {
		name string
		typ  types.TypeID
		un   string
		exp  bool
	}{
		{"name", types.Str, "", true},
		{"age", types.Int, "", false},
		{"tags", tab.Slice(tag), "", false},
		{"next", tab.Optional(tab.Ref(tab.Named(user, nil))), "", false},
		{"buf", types.NoType, "", false}, // filled below: [4]u8
		{"f", tab.Fn(types.FnType{Params: []types.FnParam{{Type: types.Int}}, Result: types.Bool, Errors: bad}), "", false},
		{"id", types.Int, "", false},
		{"p", tab.Named(lib.item("P"), nil), "", false},
		{"m", types.NoType, "Missing", false},
		{"opt", types.NoType, "Missing", false},
	}
	if len(d.Fields) != len(want) {
		t.Fatalf("%d fields, want %d: %+v", len(d.Fields), len(want), d.Fields)
	}
	for i, f := range d.Fields {
		wf := want[i]
		if wf.name == "buf" {
			if ty := tab.Get(f.Type); ty.Kind != types.KArray || ty.Elem != tab.IntType(8, false) {
				t.Errorf("buf: %s, want [4]u8", tab.TypeString(f.Type, nil))
			}
			continue
		}
		un := ""
		if f.Unresolved != 0 {
			un = w.in.Str(f.Unresolved)
		}
		if f.Name != sym(wf.name) || f.Type != wf.typ || un != wf.un || f.Exported != wf.exp {
			t.Errorf("field %d: %s %s un=%q exported=%v, want %s %s un=%q exported=%v", i, w.in.Str(f.Name), tab.TypeString(f.Type, nil), un, f.Exported,
				wf.name, tab.TypeString(wf.typ, nil), wf.un, wf.exp)
		}
	}
	if d2, _ := v.Decl(user, lib.id); len(d2.Fields) != 1 || d2.Fields[0].Name != sym("name") {
		t.Errorf("another module sees %+v, want only name", d2.Fields)
	}
	e, ok := v.Decl(b.item("Tag"), b.id)
	if !ok || len(e.Variants) != 2 || len(e.Variants[1].Fields) != 1 || e.Variants[1].Fields[0].Type != types.Int {
		t.Errorf("Decl(Tag) = %+v", e)
	}
	s, ok := v.Decl(b.item("Shape"), b.id)
	if !ok || len(s.Methods) != 2 {
		t.Fatalf("Decl(Shape) = %+v", s)
	}
	area, grow := s.Methods[0].Sig, s.Methods[1].Sig
	if area.Name != sym("Area") || area.Receiver != types.ByShared || area.Params[0].Type != tab.Ref(tab.SelfType()) || area.Result != types.F64 {
		t.Errorf("Area: %+v", area)
	}
	if grow.Receiver != types.ByMut || len(grow.Params) != 2 || grow.Params[1].Type != types.F64 || grow.Result != types.Void || grow.Errors != bad {
		t.Errorf("Grow: %+v", grow)
	}
	if _, ok := v.Decl(b.item("Id"), b.id); ok {
		t.Error("an alias has no declaration view")
	}
	// Another module's item comes from its Decl, exported fields only.
	pv, ok := v.Decl(lib.item("P"), b.id)
	if !ok || len(pv.Fields) != 1 || pv.Fields[0].Name != sym("x") || pv.Fields[0].Type != types.Int {
		t.Errorf("Decl(lib.P) = %+v", pv)
	}
	// TypeOf on a written type: User's field `tags`.
	var tagsNode ast.Node
	ast.Inspect(b.files[0], func(n ast.Node) bool {
		if f, ok := n.(*ast.FieldMember); ok && f.Name != nil && f.Name.Name == sym("tags") {
			tagsNode = f.Type
		}
		return true
	})
	if got, _ := v.TypeOf(0, tagsNode); got != tab.Slice(tag) {
		t.Errorf("TypeOf(tags) = %s", tab.TypeString(got, nil))
	}
	if got, un := v.TypeOf(7, tagsNode); got != types.NoType || un != source.Symbol(0) {
		t.Errorf("TypeOf on a missing file = %d %d", got, un)
	}
}

// TestGenView_ValueResult: the view reads a result as stage 5 does: a
// written E!T, parenthesized or not, fails; an alias of one is the union
// held as a value, with no errors, in a fn type and in a method alike.
func TestGenView_ValueResult(t *testing.T) {
	w := newWorld(t, true)
	b := w.add("m", opt{stopBefore: true}, "error Bad\n\ntype R = Bad!int\n\n"+
		"struct S {\n\tf: fn(&R): R\n\tg: fn(): (Bad!int)\n\th: fn(): Bad!int\n}\n\n"+
		"interface I {\n\tGet(&self): R\n\tPut(&self): Bad!int\n}\n")
	v := w.c.GenView(&ModuleInput{Module: b.id, Files: b.files, Info: b.info})
	tab := w.tab
	bad := tab.ErrorSet([]types.ErrorMember{{Item: b.item("Bad")}})
	r := tab.ErrorUnion(bad, types.Int)
	fails := tab.Fn(types.FnType{Result: types.Int, Errors: bad})
	d, ok := v.Decl(b.item("S"), b.id)
	if !ok || len(d.Fields) != 3 {
		t.Fatalf("Decl(S) = %+v", d)
	}
	for i, want := range []types.TypeID{tab.Fn(types.FnType{Params: []types.FnParam{{Type: tab.Ref(r)}}, Result: r, Errors: types.EmptyErrors}), fails, fails} {
		if d.Fields[i].Type != want {
			t.Errorf("field %d: %s, want %s", i, tab.TypeString(d.Fields[i].Type, nil), tab.TypeString(want, nil))
		}
	}
	s, ok := v.Decl(b.item("I"), b.id)
	if !ok || len(s.Methods) != 2 {
		t.Fatalf("Decl(I) = %+v", s)
	}
	if get := s.Methods[0].Sig; get.Result != r || get.Errors != types.EmptyErrors {
		t.Errorf("Get: result %s errors %d, want the union as a value", tab.TypeString(get.Result, nil), get.Errors)
	}
	if put := s.Methods[1].Sig; put.Result != types.Int || put.Errors != bad {
		t.Errorf("Put: result %s errors %d, want int failing with Bad", tab.TypeString(put.Result, nil), put.Errors)
	}
}
