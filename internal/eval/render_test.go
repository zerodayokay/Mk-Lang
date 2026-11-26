// Why: an evaluation error is shown to users and tools, so its facts and JSON
// form must be stable.
// What: tests of Render: the facts per code and the JSON snapshots of E0901,
// E0902, E0908 and E0909 (every error code has a snapshot).
// How: one failing request per kind, rendered and compared with testdata.

package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// constCalling0 declares const name: int = fn() in module mod.
func (w *world) constCalling0(mod, name string, fn types.ItemID, fuel uint64) types.ItemID {
	c := w.constItem(mod, name, types.Int, fuel)
	x := w.body(c, types.Int)
	b1 := x.block()
	x.call(0, fn, lp(0), b1)
	x.ret(b1)
	x.done()
	return c
}

// renderCases evaluates one failing request per kind in world w.
func renderCases(t *testing.T, w *world) map[string]struct {
	req *types.EvalRequest
	err *types.EvalError
} {
	out := map[string]struct {
		req *types.EvalRequest
		err *types.EvalError
	}{}
	add := func(name string, req *types.EvalRequest) {
		r := w.evaluator(0).Eval(req)
		if r.Err == nil {
			t.Fatalf("%s: no error", name)
		}
		out[name] = struct {
			req *types.EvalRequest
			err *types.EvalError
		}{req, r.Err}
	}
	inner := w.panicking("Inner", func(x *bb) {
		x.term(0, mir.Terminator{Kind: mir.TermPanic, Panic: types.PanicExplicit, HasMsg: true, Msg: mir.Const(types.Str, w.strConst("bad table"))})
	})
	add("e0901 panic", constReq(w, w.constCalling0("main", "P", inner, 0), types.StageBodies))
	ext := w.fn("lib", "CExt", types.Int)
	fd := w.items.Get(ext).Decl.(*types.FuncDecl)
	fd.Bodiless, fd.Sig.Extern = true, true
	add("e0901 refused", constReq(w, w.constCalling0("main", "R", ext, 0), types.StageBodies))
	spin, _ := w.spinFn("Spin")
	add("e0902", constReq(w, w.constCalling0("main", "F", spin, 500), types.StageBodies)) // Fuel 0: the const's own 500
	h := w.retConst("main", "Helper", 3)
	n := w.constItem("main", "N", types.Int, 0)
	x := w.body(n, types.Int)
	b1 := x.block()
	x.call(0, h, lp(0), b1)
	x.ret(b1)
	x.done()
	add("e0908", constReq(w, w.constPlus("main", "M", n, 1), types.StageSignatures))
	a, _ := w.cycleAB()
	add("e0909", constReq(w, a, types.StageBodies))
	return out
}

func TestRender_Facts(t *testing.T) {
	w := newWorld(t)
	cases := renderCases(t, w)
	want := map[string]struct {
		code diag.Code
		keys string
	}{
		"e0901 panic":   {diag.E0901, "panic message trace"},
		"e0901 refused": {diag.E0901, "refused trace"},
		"e0902":         {diag.E0902, "steps fuel trace"},
		"e0908":         {diag.E0908, "items trace"},
		"e0909":         {diag.E0909, "cycle"},
	}
	for name, c := range cases {
		d := Render(c.err, c.req, w.items, w.in)
		var keys []string
		for _, f := range d.Facts {
			keys = append(keys, f.Key)
		}
		if d.Code != want[name].code || strings.Join(keys, " ") != want[name].keys || d.At != c.req.Site {
			t.Errorf("%s: %v facts %v at %v, want %v %q at the site", name, d.Code, keys, d.At, want[name].code, want[name].keys)
		}
	}
	// Trace order: innermost first for E0901, outermost first for E0908.
	p := Render(cases["e0901 panic"].err, cases["e0901 panic"].req, w.items, w.in)
	if tr, _ := p.FactValue("trace"); len(tr.List) != 2 || tr.List[0].List[0].Str != "Inner" {
		t.Errorf("E0901 trace %+v, want Inner first", tr)
	}
	s := Render(cases["e0908"].err, cases["e0908"].req, w.items, w.in)
	if it, _ := s.FactValue("items"); len(it.List) != 3 || it.List[0].Str != "main.M" || it.List[2].Str != "main.Helper" {
		t.Errorf("E0908 items %+v, want [main.M main.N main.Helper]", it)
	}
	c := Render(cases["e0909"].err, cases["e0909"].req, w.items, w.in)
	if cy, _ := c.FactValue("cycle"); len(cy.List) != 2 || cy.List[0].Str != "A" || cy.List[1].Str != "B" {
		t.Errorf("E0909 cycle %+v", cy)
	}
}

func TestRender_Snapshots(t *testing.T) {
	w := newWorld(t)
	cases := renderCases(t, w)
	sm := source.NewSourceMap()
	var text strings.Builder
	for text.Len() < int(w.spanN+2)*10 {
		text.WriteString("const placeholder line for eval spans xx\n")
	}
	f := sm.Add("main.mk", []byte(text.String()))
	if f.ID != 1 {
		t.Fatalf("file ID %d, the world's spans use 1", f.ID)
	}
	for _, code := range []string{"e0901", "e0902", "e0908", "e0909"} {
		var ds []*diag.Diagnostic
		for name, c := range cases {
			if strings.HasPrefix(name, code) {
				ds = append(ds, Render(c.err, c.req, w.items, w.in))
			}
		}
		got := diag.Order(ds, sm, diag.NoCap).WriteJSON(sm)
		golden := filepath.Join("testdata", code+".golden")
		if *update {
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run with -update)", err)
		}
		if string(got) != strings.ReplaceAll(string(want), "\r\n", "\n") {
			t.Errorf("%s changed:\n got %s\nwant %s", code, got, want)
		}
	}
}

// TestRender_GlobalFuel: a var global cannot state fuel, so its E0902
// names the route that compiles instead of a fuel clause; a const keeps
// the raised fuel.
func TestRender_GlobalFuel(t *testing.T) {
	w := newWorld(t)
	g := w.item("main", "Total", types.KindGlobal, &types.GlobalDecl{Type: types.Int})
	c := w.constItem("main", "C", types.Int, 0)
	for _, tc := range []struct {
		item types.ItemID
		alt  string
	}{
		{c, "raise the limit: fuel 8000000"},
		{g, "a global cannot state fuel: do the work in a const with fuel 8000000, and initialize Total from that const"},
	} {
		err := &types.EvalError{Kind: types.EvalFuel, Steps: 4000000, Items: []types.ItemID{tc.item}, Trace: []source.Span{{}}}
		d := Render(err, constReq(w, tc.item, types.StageBodies), w.items, w.in)
		if d.Code != diag.E0902 || len(d.Alternatives) != 1 || d.Alternatives[0] != tc.alt {
			t.Errorf("%v %q, want E0902 with %q", d.Code, d.Alternatives, tc.alt)
		}
	}
}
