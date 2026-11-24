// Why: a signature's flows (which parameter a result borrows from, and in
// what mode) are a public fact borrow relies on.
// What: TestSources, TestE0601_SourceRequired, TestModes,
// TestE0607_FromOwnBytes, TestStoreClauseShape, TestE0608_FlowMismatch.
// How: checks hand-built signatures against the memory model's flow rules.

package check

import (
	"fmt"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// prm is one parameter of a hand-built signature.
type prm struct {
	name string
	t    types.TypeID
}

// sig builds a signature; a parameter named "self" makes it a method.
func (h *hand) sig(exported bool, result types.TypeID, gens int, ps ...prm) *types.Signature {
	s := &types.Signature{Result: result, Exported: exported, Span: source.Span{Start: 1000}}
	for i := 0; i < gens; i++ {
		s.Generics = append(s.Generics, types.GenericParam{Name: h.in.Intern(fmt.Sprintf("T%d", i))})
	}
	for i, p := range ps {
		if i == 0 && p.name == "self" {
			s.Receiver = types.ByMut
		}
		s.Params = append(s.Params, types.Param{Name: h.in.Intern(p.name), Type: p.t})
	}
	return s
}

// flows runs signatureFlows on a hand-built signature.
func (h *hand) flows(s *types.Signature, cl *flowClauses) (types.Flows, []*diag.Diagnostic) {
	fn := flowFn{sig: s, mod: h.mod, env: h.c.NewEnv(s.Generics), at: s.Span}
	return h.c.signatureFlows(fn, cl)
}

// from builds a `from` clause "p" or "p.f.g" against the signature.
func (h *hand) from(s *types.Signature, srcs ...string) *flowClauses {
	cl := &flowClauses{hasFrom: true}
	for n, src := range srcs {
		parts := strings.Split(src, ".")
		fc := fromClause{param: s.ParamByName(h.in.Intern(parts[0])), at: source.Span{Start: uint32(2000 + 100*n)}}
		for i, f := range parts[1:] {
			fc.steps = append(fc.steps, stepRef{name: h.in.Intern(f), at: source.Span{Start: uint32(2000 + 100*n + 10*(i+1))}})
		}
		cl.from = append(cl.from, fc)
	}
	return cl
}

// srcString renders sources as "H(a) B(self.text)".
func (h *hand) srcString(s *types.Signature, fl types.Flows) string {
	var out []string
	for _, x := range fl.Sources {
		mode := "H"
		if x.Behind {
			mode = "B"
		}
		name := h.in.Str(s.Param(x.Param).Name)
		for _, st := range x.Path {
			name += fmt.Sprintf(".%d", st.Field)
		}
		out = append(out, mode+"("+name+")")
	}
	return strings.Join(out, " ")
}

// named field struct helper: fields given as name:type pairs, with export.
func (h *hand) record(name, kw string, fields ...types.FieldDecl) types.ItemID {
	d := &types.StructDecl{View: contains(kw, "view"), Linear: contains(kw, "linear")}
	d.Fields = fields
	return h.item(name, types.KindStruct, d)
}

func (h *hand) fd(name string, t types.TypeID, exported bool) types.FieldDecl {
	return types.FieldDecl{Name: h.in.Intern(name), Type: t, Exported: exported}
}

func codesOf(ds []*diag.Diagnostic) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Code.String())
	}
	return strings.Join(out, " ")
}

func TestSources(t *testing.T) {
	h := newHand(t)
	tab := h.tab
	list := h.strct("List", 1, "view", tab.RawPtr(tab.Param(0)), types.Int)
	rows := []struct {
		name string
		s    *types.Signature
		cl   *flowClauses
		want string
	}{
		{"First", h.sig(false, tab.Ref(types.I32), 0, prm{"xs", tab.Slice(types.I32)}), nil, "H(xs)"},
		{"At", h.sig(false, tab.Ref(types.I32), 0, prm{"xs", tab.Slice(types.I32)}, prm{"i", types.I32}), nil, "H(xs)"},
		{"non-carrier result", h.sig(false, types.Int, 0, prm{"a", tab.Ref(types.Int)}), nil, ""},
		{"Pop", h.sig(false, tab.Optional(tab.Param(0)), 1, prm{"self", tab.Mut(h.named(list, tab.Param(0)))}), nil, "B(self)"},
		{"by-value generic", h.sig(false, tab.Param(0), 1, prm{"x", tab.Param(0)}, prm{"n", types.Int}), nil, "H(x)"},
		{"&Alloc never supplies a str", h.sig(false, types.Str, 0, prm{"a", tab.Ref(h.named(h.c.known.Alloc))}, prm{"s", types.Str}), nil, "H(s)"},
		{"no candidate", h.sig(false, tab.Ref(types.Int), 0, prm{"s", types.Str}), nil, ""},
		{"Clone(&self): Self", h.sig(false, tab.SelfType(), 0, prm{"self", tab.Ref(tab.SelfType())}), nil, "B(self)"},
		{"fn never supplies", h.sig(false, types.Str, 0, prm{"f", tab.Fn(types.FnType{Result: types.Str})}, prm{"s", types.Str}), nil, "H(s)"},
	}
	longest := h.sig(false, types.Str, 0, prm{"a", types.Str}, prm{"b", types.Str})
	rows = append(rows, struct {
		name string
		s    *types.Signature
		cl   *flowClauses
		want string
	}{"Longest", longest, h.from(longest, "a", "b"), "H(a) H(b)"})
	for _, r := range rows {
		cl := r.cl
		if cl == nil {
			cl = &flowClauses{}
		}
		fl, ds := h.flows(r.s, cl)
		if len(ds) > 0 {
			t.Errorf("%s: unexpected %s", r.name, codesOf(ds))
		}
		if got := h.srcString(r.s, fl); got != r.want {
			t.Errorf("%s: sources %q, want %q", r.name, got, r.want)
		}
		if fl.Declared != (r.cl != nil) {
			t.Errorf("%s: Declared %v", r.name, fl.Declared)
		}
	}
}

func TestE0601_SourceRequired(t *testing.T) {
	h := newHand(t)
	longer := h.sig(false, types.Str, 0, prm{"a", types.Str}, prm{"b", types.Str})
	fl, ds := h.flows(longer, &flowClauses{})
	if codesOf(ds) != "E0601" || fact(ds[0], "candidates") != "[a b]" || !fl.Ambiguous || h.srcString(longer, fl) != "H(a) H(b)" {
		t.Fatalf("Longer: %s %v %+v", codesOf(ds), ds, fl)
	}
	if ds[0].At != longer.Span {
		t.Errorf("Longer: anchor %v, want the signature", ds[0].At)
	}
	// Case (b): an exported function whose answer a private field changes.
	holder := h.record("Holder", "", h.fd("name", types.Str, false), h.fd("n", types.Int, true))
	get := h.sig(true, types.Str, 0, prm{"h", h.tab.Ref(h.named(holder))})
	_, ds = h.flows(get, &flowClauses{})
	if codesOf(ds) != "E0601" || fact(ds[0], "private_field") != "name" || fact(ds[0], "candidates") != "[h]" {
		t.Fatalf("exported Get: %s %s", codesOf(ds), dumpFacts(ds))
	}
	// The same function private: no diagnostic, B(h) (the str field is a
	// shared reference, as in Lexer).
	get.Exported = false
	fl, ds = h.flows(get, &flowClauses{})
	if len(ds) > 0 || h.srcString(get, fl) != "B(h)" {
		t.Errorf("private Get: %s %s", codesOf(ds), h.srcString(get, fl))
	}
	// A clause removes the E0601.
	_, ds = h.flows(longer, h.from(longer, "a"))
	if len(ds) > 0 {
		t.Errorf("Longer from a: %s", codesOf(ds))
	}
}

func dumpFacts(ds []*diag.Diagnostic) string {
	var b strings.Builder
	for _, d := range ds {
		for _, f := range d.Facts {
			fmt.Fprintf(&b, "%s=%s ", f.Key, factString(f.Value))
		}
	}
	return b.String()
}

func TestModes(t *testing.T) {
	h := newHand(t)
	tab := h.tab
	lexer := h.record("Lexer", "", h.fd("src", types.Str, false), h.fd("pos", types.Int, false))
	buf := h.record("Buf", "", h.fd("data", tab.Array(h.c.intArg(16), types.U8), false), h.fd("pos", types.Int, false))
	slot := h.record("Slot", "", h.fd("m", tab.Mut(types.I32), false))
	rows := []struct {
		name string
		s    *types.Signature
		want string
	}{
		{"Lexer.Next", h.sig(false, tab.Optional(types.Str), 0, prm{"self", tab.Mut(h.named(lexer))}), "B(self)"},
		{"Buf.Peek", h.sig(false, tab.Slice(types.U8), 0, prm{"self", tab.Mut(h.named(buf))}), "H(self)"},
		{"Peek(&Slot)", h.sig(false, tab.Ref(types.I32), 0, prm{"s", tab.Ref(h.named(slot))}), "H(s)"},
		{"by value", h.sig(false, types.Str, 0, prm{"l", h.named(lexer)}), "H(l)"},
		{"mut result", h.sig(false, tab.Optional(tab.Mut(types.I32)), 0, prm{"s", tab.Ref(h.named(slot))}), "H(s)"},
	}
	for _, r := range rows {
		fl, ds := h.flows(r.s, &flowClauses{})
		if len(ds) > 0 || h.srcString(r.s, fl) != r.want {
			t.Errorf("%s: %q %s, want %q", r.name, h.srcString(r.s, fl), codesOf(ds), r.want)
		}
	}
}

func TestE0607_FromOwnBytes(t *testing.T) {
	h := newHand(t)
	tab := h.tab
	list := h.strct("List", 1, "view", tab.RawPtr(tab.Param(0)), types.Int)
	words2 := h.record("Words2", "view", h.fd("text", types.Str, false), h.fd("pos", types.Int, false))
	index := h.record("Index", "", h.fd("words", h.named(list, types.Str), false))
	sink := h.record("Sink", "", h.fd("buf", tab.MutSlice(types.U8), false))
	buf := h.record("Buf", "", h.fd("data", tab.Array(h.c.intArg(16), types.U8), false))
	pair := h.record("Pair", "", h.fd("a", types.Str, false))
	type row struct {
		name   string
		s      *types.Signature
		src    string
		code   string
		reason string
		want   string
	}
	next := h.sig(false, tab.Optional(types.Str), 0, prm{"self", tab.Mut(h.named(words2))})
	first := h.sig(false, types.Str, 0, prm{"self", tab.Ref(h.named(index))})
	out := h.sig(false, tab.MutSlice(types.U8), 0, prm{"self", tab.Mut(h.named(sink))})
	peek := h.sig(false, tab.Slice(types.U8), 0, prm{"self", tab.Mut(h.named(buf))})
	byval := h.sig(false, types.Str, 0, prm{"p", h.named(pair)})
	rows := []row{
		{"Words2.Next", next, "self.text", "", "", "B(self.0)"},
		{"Index.First", first, "self.words", "", "", "B(self.0)"},
		{"Sink.Out", out, "self.buf", "E0607", "mut", ""},
		{"Buf.Peek", peek, "self.data", "E0607", "inline", ""},
		{"by-value", byval, "p.a", "E0607", "by-value", ""},
		{"unknown field", next, "self.nope", "E0203", "", ""},
		{"through a str", next, "self.text.len", "E0607", "not-a-reference", ""},
	}
	for _, r := range rows {
		fl, ds := h.flows(r.s, h.from(r.s, r.src))
		if codesOf(ds) != r.code {
			t.Errorf("%s: %s, want %q", r.name, codesOf(ds), r.code)
			continue
		}
		if r.code == "" && h.srcString(r.s, fl) != r.want {
			t.Errorf("%s: %q, want %q", r.name, h.srcString(r.s, fl), r.want)
		}
		if r.reason != "" && fact(ds[0], "reason") != r.reason {
			t.Errorf("%s: reason %q, want %q", r.name, fact(ds[0], "reason"), r.reason)
		}
		if r.code == "E0607" {
			if !fl.ClauseError {
				t.Errorf("%s: ClauseError not set", r.name)
			}
			if len(ds[0].Fixes) != 1 || ds[0].Fixes[0].Class != diag.Proven {
				t.Errorf("%s: want one proven fix", r.name)
			}
		}
	}
}

func TestStoreClauseShape(t *testing.T) {
	h := newHand(t)
	tab := h.tab
	cellRef := h.named(h.c.known.Cell, tab.Ref(types.I32))
	type row struct {
		name  string
		s     *types.Signature
		from  string
		into  string
		code  string
		phase diag.Phase
	}
	rows := []row{
		{"into mut self", h.sig(false, types.Void, 0, prm{"self", tab.Mut(types.Int)}, prm{"q", tab.Ref(types.I32)}), "q", "self", "", 0},
		{"into &Cell", h.sig(false, types.Void, 0, prm{"p", tab.Ref(cellRef)}, prm{"q", tab.Slice(types.I32)}), "q", "p", "", 0},
		{"Swap", h.sig(false, types.Void, 1, prm{"a", tab.Mut(tab.Param(0))}, prm{"b", tab.Mut(tab.Param(0))}), "a", "b", "", 0},
		{"into &T", h.sig(false, types.Void, 0, prm{"p", tab.Ref(types.I32)}, prm{"q", tab.Ref(types.I32)}), "q", "p", "E0507", diag.PhaseTypes},
		{"from i32", h.sig(false, types.Void, 0, prm{"p", tab.Mut(types.I32)}, prm{"n", types.I32}), "n", "p", "E0301", diag.PhaseTypes},
	}
	for _, r := range rows {
		cl := &flowClauses{stores: []storeClause{{from: r.s.ParamByName(h.in.Intern(r.from)), into: r.s.ParamByName(h.in.Intern(r.into)),
			at: source.Span{Start: 3000}, fromAt: source.Span{Start: 3001}}}}
		fl, ds := h.flows(r.s, cl)
		if codesOf(ds) != r.code {
			t.Errorf("%s: %s, want %q", r.name, codesOf(ds), r.code)
			continue
		}
		if r.code == "" && len(fl.Stores) != 1 {
			t.Errorf("%s: stores %v", r.name, fl.Stores)
		}
		if r.code != "" && ds[0].Phase != r.phase {
			t.Errorf("%s: phase %v, want %v", r.name, ds[0].Phase, r.phase)
		}
		if r.code == "E0507" && (fact(ds[0], "place") != "p" || fact(ds[0], "reason") != "store-target" || ds[0].At.Start != 3000) {
			t.Errorf("%s: %s at %v", r.name, dumpFacts(ds), ds[0].At)
		}
		if r.code == "E0301" && ds[0].At.Start != 3001 {
			t.Errorf("%s: anchor %v, want the source name", r.name, ds[0].At)
		}
	}
	// ends p is recorded and nothing more.
	s := h.sig(false, types.Void, 0, prm{"self", tab.Mut(types.Int)})
	fl, ds := h.flows(s, &flowClauses{ends: []types.ParamIndex{1, 1}})
	if len(ds) > 0 || len(fl.Ends) != 1 || fl.Ends[0] != 1 {
		t.Errorf("ends: %v %s", fl.Ends, codesOf(ds))
	}
}

func TestE0608_FlowMismatch(t *testing.T) {
	h := newHand(t)
	tab := h.tab
	// Id(x: &i32): &i32 has H(x); fn(&i32): T declares none.
	id := h.sig(false, tab.Ref(types.I32), 0, prm{"x", tab.Ref(types.I32)})
	idFl, _ := h.flows(id, &flowClauses{})
	target := h.sig(false, tab.Param(0), 1, prm{"p0", tab.Ref(types.I32)})
	tFl, _ := h.flows(target, &flowClauses{})
	if flowsCovered(idFl, tFl, sigInto(id), sigInto(target)) {
		t.Errorf("Id as fn(&i32): T is covered")
	}
	held := types.Flows{Sources: []types.Source{{Param: 1}}}
	behind := types.Flows{Sources: []types.Source{{Param: 1, Behind: true}}}
	if !flowsCovered(behind, held, nil, nil) {
		t.Errorf("H(p) does not cover B(p)")
	}
	if flowsCovered(held, behind, nil, nil) {
		t.Errorf("B(p) covers H(p)")
	}
	store := types.Flows{Stores: []types.Store{{From: 1, Into: 2}}}
	if flowsCovered(store, types.Flows{}, nil, []types.ParamIndex{2, 0}) {
		t.Errorf("x into p covers *x into p")
	}
	if !flowsCovered(types.Flows{}, store, []types.ParamIndex{0, 0}, nil) {
		t.Errorf("no flows not covered")
	}
	if flowsCovered(types.Flows{}, types.Flows{}, []types.ParamIndex{2, 0}, []types.ParamIndex{0, 0}) {
		t.Errorf("an undeclared into is covered")
	}
}

func TestE0507_EnumReachesInterior(t *testing.T) {
	w := newWorld(t, true)
	checkCodes(t, w, "m", opt{}, "E0507@11:34", `enum CarrierEnum {
	A(c: &Cell[&i32])
	B(v: i32)
}

enum NonCarrierEnum {
	A(c: &Cell[i32])
	B(v: i32)
}

Bad(e: &NonCarrierEnum, x: &i32) *x into e {
	_ = e
	_ = x
}

Good(e: &CarrierEnum, x: &i32) *x into e {
	_ = e
	_ = x
}
`)
}

func TestPlainView_InvariantB(t *testing.T) {
	w := newWorld(t, true)
	checkCodes(t, w, "m", opt{unsafe: true}, "E0507@17:24 E0507@21:25 E0507@25:34 E0507@29:35 E0507@33:49 E0507@38:24 E0507@41:28", `plain view struct Ref[T] {
	idx: u32
}

plain view struct Span[T] {
	xs: []T
}

Ref.OKCopy(&self, x: &i32): i32 {
	return *x
}

Ref.OKMutInto(mut self, x: &i32 into self) {
	_ = x
}

Ref.BadIntoByVal(self, x: &i32 into self) {
	_ = x
}

Ref.BadIntoByRef(&self, x: &i32 into self) {
	_ = x
}

Ref.BadStoreByVal(self, q: &i32) *q into self {
	_ = q
}

Ref.BadStoreByRef(&self, q: &i32) *q into self {
	_ = q
}

BadStoreSpanCell(s: &Span[Cell[&i32]], q: &i32) *q into s {
	_ = s
	_ = q
}

Ref.BadEndsByVal(self) ends self {
}

Ref.BadEndsByMut(mut self) ends self {
}
`)
}
