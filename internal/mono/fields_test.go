// Why: the fields-loop unroll has paths that TestMonoFieldsUnroll's
// straight-line Dump does not reach.
// What: TestFieldsBreakContinue, TestFieldsNoField and
// TestFieldsInnerLoop and TestFieldsConsume: break and continue inside the
// range, a range with no field, a non-fields loop and a consume in the range.
// How: instantiates hand-built bodies over small records and checks each
// copy's blocks, LoopInfo and holder local.

package mono

import (
	"fmt"
	"testing"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

func (w *world) record(names ...string) types.TypeID {
	var fs []types.RecordField
	for _, n := range names {
		fs = append(fs, types.RecordField{Name: w.in.Intern(n), Type: types.Int})
	}
	return w.tab.Record(fs)
}

func (w *world) instantiate(t *testing.T, item types.ItemID, a types.TypeID) *mir.Body {
	t.Helper()
	var ib *mir.Body
	if ie := source.Guard("t", source.Span{}, func() { ib = Instantiate(w, item, []types.GenericArg{ty(a)}, Options{Validate: true}) }); ie != nil {
		t.Fatalf("instance at %s: %s", w.tab.TypeString(a, nil), ie)
	}
	return ib
}

// breakContinueDump is Dump[T](x: T) with `for f in fields(&x)`, whose body
// takes &f, then `if n == 2 { continue }`, `if n == 3 { break }` (one switch
// on n), then reads the field's name and reaches the latch:
//
//	bb0 entry -> bb1; bb1 header -> bb2; bb2 body: switch n {2: bb3, 3: bb5} else bb4;
//	bb3 latch -> bb5; bb4 name -> bb3; bb5 exit: return.
func breakContinueDump(w *world) (types.ItemID, *mir.Body) {
	tab := w.tab
	p0, p1 := tab.Param(0), tab.Param(1)
	dump := w.fn("Dump", 1, []types.TypeID{p0}, types.Void)
	gb := mir.NewBody(dump, w.span(), types.Void)
	gb.GenericCount = 2
	gb.AddParam(p0, w.in.Intern("x"), 0)
	n := gb.AddLocal(mir.Local{Type: types.Int, Kind: mir.LocalUser, Loop: mir.NoLoop})
	value := gb.AddLocal(mir.Local{Type: tab.Ref(p1), Kind: mir.LocalUser, Loop: mir.NoLoop})
	name := gb.AddLocal(mir.Local{Type: types.Str, Kind: mir.LocalUser, Loop: mir.NoLoop})
	bb := blocks(gb, 6)
	gb.Loops = append(gb.Loops, mir.LoopInfo{Header: bb[1], Exit: bb[5], Holder: mir.NoLocal, FieldsOf: p0, FieldsParam: 1})
	w.stmt(gb, bb[0], gb.Place(n), mir.Rvalue{Kind: mir.RvUse, A: mir.Imm(types.Int, 0)})
	w.gotoTerm(gb, bb[0], bb[1])
	w.gotoTerm(gb, bb[1], bb[2])
	fp := gb.Place(1, mir.Projection{Kind: mir.ProjField, FieldLoop: true, A: 0, Type: p1})
	w.stmt(gb, bb[2], gb.Place(value), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: fp, Type: tab.Ref(p1)})
	gb.Blocks[bb[2]].Term = mir.Terminator{Kind: mir.TermSwitch, Span: gb.AddSpan(w.span()), On: mir.Copy(gb.Place(n)),
		Cases: gb.AddCases(mir.SwitchCase{Value: 2, Target: bb[3]}, mir.SwitchCase{Value: 3, Target: bb[5]}), Default: bb[4]}
	w.gotoTerm(gb, bb[3], bb[5])
	w.stmt(gb, bb[4], gb.Place(name), mir.Rvalue{Kind: mir.RvFieldName, Field: mir.FieldRef{Loop: true, Index: 0}, Type: types.Str})
	w.gotoTerm(gb, bb[4], bb[3])
	w.retTerm(gb, bb[5])
	w.bodies[dump] = gb
	return dump, gb
}

// TestFieldsConsume checks that each loop copy consumes its own instance
// of a fieldless struct whose type depends on the current field.
func TestFieldsConsume(t *testing.T) {
	w := newWorld()
	dump, gb := breakContinueDump(w)
	tok := w.items.AddItem(&types.Item{Module: w.mod, Key: types.ItemKey("m", "Token"), Name: w.in.Intern("Token"),
		Kind: types.KindStruct, Decl: &types.StructDecl{Linear: true, Generics: []types.GenericParam{{Name: w.in.Intern("T")}}}})
	local := gb.AddLocal(mir.Local{Type: w.tab.Named(tok, []types.GenericArg{ty(w.tab.Param(1))}), Kind: mir.LocalTemp, Loop: mir.NoLoop})
	body := gb.Loops[0].Header + 1
	w.stmt(gb, body, gb.Place(local), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: tok})
	gb.Blocks[body].Stmts = append(gb.Blocks[body].Stmts, mir.Statement{Kind: mir.StConsume, Place: gb.Place(local), Span: gb.AddSpan(w.span())})

	record := w.tab.Record([]types.RecordField{{Name: w.in.Intern("a"), Type: types.Int}, {Name: w.in.Intern("b"), Type: types.Bool}})
	ib := w.instantiate(t, dump, record)
	want := []types.TypeID{w.tab.Named(tok, []types.GenericArg{ty(types.Int)}), w.tab.Named(tok, []types.GenericArg{ty(types.Bool)})}
	var got []types.TypeID
	for _, blk := range ib.Blocks {
		for i, st := range blk.Stmts {
			if st.Kind == mir.StConsume {
				if i == 0 || blk.Stmts[i-1].Place != st.Place {
					t.Fatalf("loop copy consumes %v instead of its assignment's destination", st.Place)
				}
				got = append(got, ib.PlaceType(st.Place))
			}
		}
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("consumed types %v, want %v", got, want)
	}
}

// copies finds the unrolled copies of breakContinueDump: per copy, the
// switch block and the field index its &f projects.
func copies(t *testing.T, w *world, ib *mir.Body) (switches []mir.BlockID, fields []uint32) {
	t.Helper()
	for bi, blk := range ib.Blocks {
		for _, st := range blk.Stmts {
			if st.Rvalue.Kind == mir.RvRef {
				pj := ib.Proj(st.Rvalue.Place)
				if len(pj) != 1 || pj[0].FieldLoop {
					t.Fatalf("field projection %v", pj)
				}
				fields = append(fields, pj[0].A)
			}
		}
		if blk.Term.Kind == mir.TermSwitch {
			switches = append(switches, mir.BlockID(bi))
		}
	}
	return switches, fields
}

// TestFieldsBreakContinue: over (a, b, c, d) the range is copied four
// times in field order; in each copy `continue` reaches that copy's latch,
// which goes to the next copy's header (the last one's to Exit), `break`
// goes to Exit, and the fall-through path reaches the same latch.
func TestFieldsBreakContinue(t *testing.T) {
	w := newWorld()
	dump, gb := breakContinueDump(w)
	ib := w.instantiate(t, dump, w.record("a", "b", "c", "d"))
	pr := (&mir.Printer{Types: w.tab, Items: w.items}).Print(ib)
	switches, fields := copies(t, w, ib)
	if fmt.Sprint(fields) != "[0 1 2 3]" || len(switches) != 4 {
		t.Fatalf("fields %v, %d switches:\n%s", fields, len(switches), pr)
	}
	var exit mir.BlockID = mir.NoBlock
	for bi, blk := range ib.Blocks {
		if blk.Term.Kind == mir.TermReturn {
			if exit != mir.NoBlock {
				t.Fatalf("two return blocks:\n%s", pr)
			}
			exit = mir.BlockID(bi)
		}
	}
	// header of copy c: the block whose goto leads to switch c.
	header := func(c int) mir.BlockID {
		for bi, blk := range ib.Blocks {
			if blk.Term.Kind == mir.TermGoto && blk.Term.Target == switches[c] {
				return mir.BlockID(bi)
			}
		}
		t.Fatalf("no header for copy %d:\n%s", c, pr)
		return mir.NoBlock
	}
	for c, s := range switches {
		term := ib.Blocks[s].Term
		cs := ib.Cases[term.Cases.Start:term.Cases.End()]
		if len(cs) != 2 || cs[0].Value != 2 || cs[1].Value != 3 {
			t.Fatalf("copy %d cases %v", c, cs)
		}
		latch := cs[0].Target
		next := exit
		if c+1 < len(switches) {
			next = header(c + 1)
		}
		if lt := ib.Blocks[latch].Term; lt.Kind != mir.TermGoto || lt.Target != next {
			t.Fatalf("copy %d: continue reaches bb%d, which goes to bb%d, want bb%d:\n%s", c, latch, lt.Target, next, pr)
		}
		if cs[1].Target != exit {
			t.Fatalf("copy %d: break goes to bb%d, want the exit bb%d:\n%s", c, cs[1].Target, exit, pr)
		}
		if ft := ib.Blocks[term.Default].Term; ft.Kind != mir.TermGoto || ft.Target != latch {
			t.Fatalf("copy %d: the body's end goes to bb%d, want its latch bb%d:\n%s", c, ft.Target, latch, pr)
		}
		// Each copy reads its own field's name.
		st := ib.Blocks[term.Default].Stmts
		if len(st) != 1 || st[0].Rvalue.Kind != mir.RvUse || w.tab.ConstString(st[0].Rvalue.A.Const) != fmt.Sprintf("%q", string(rune('a'+c))) {
			t.Fatalf("copy %d: name statement %v:\n%s", c, st, pr)
		}
	}
	if len(ib.Loops) != 1 || ib.Loops[0].FieldsOf != types.NoType {
		t.Fatalf("loops after the unroll: %+v", ib.Loops)
	}
	if gb.Loops[0].FieldsOf == types.NoType || len(gb.Blocks) != 6 {
		t.Fatal("the generic body was modified")
	}
}

// TestFieldsNoField: over () and over a struct with no exported field,
// the edges into the header go to Exit; no block of the range keeps code
// (only the loop's header survives, as an unreachable anchor), no local
// keeps the hidden parameter, and the body validates.
func TestFieldsNoField(t *testing.T) {
	w := newWorld()
	dump, _ := breakContinueDump(w)
	priv := w.items.AddItem(&types.Item{Module: w.mod, Key: types.ItemKey("m", "Priv"), Name: w.in.Intern("Priv"), Kind: types.KindStruct,
		Decl: &types.StructDecl{Fields: []types.FieldDecl{{Name: w.in.Intern("x"), Type: types.Int}}}})
	for _, a := range []types.TypeID{w.tab.Record(nil), w.tab.Named(priv, nil)} {
		ib := w.instantiate(t, dump, a)
		pr := (&mir.Printer{Types: w.tab, Items: w.items}).Print(ib)
		if e := ib.Blocks[mir.EntryBlock].Term; e.Kind != mir.TermGoto || ib.Blocks[e.Target].Term.Kind != mir.TermReturn {
			t.Fatalf("%s: the entry does not go to the exit:\n%s", w.tab.TypeString(a, nil), pr)
		}
		for bi, blk := range ib.Blocks {
			switch blk.Term.Kind {
			case mir.TermGoto, mir.TermReturn, mir.TermUnreachable:
			default:
				t.Fatalf("%s: bb%d of the dead range survives:\n%s", w.tab.TypeString(a, nil), bi, pr)
			}
			for _, st := range blk.Stmts {
				if st.Rvalue.Kind == mir.RvRef || st.Rvalue.Kind == mir.RvFieldName {
					t.Fatalf("%s: bb%d keeps a statement of the range:\n%s", w.tab.TypeString(a, nil), bi, pr)
				}
			}
		}
		if len(ib.Blocks) > 3 {
			t.Fatalf("%s: want entry, the header anchor and exit, got %d blocks:\n%s", w.tab.TypeString(a, nil), len(ib.Blocks), pr)
		}
		for i, l := range ib.Locals {
			if !closed(w.tab, l.Type) {
				t.Fatalf("%s: local _%d keeps %s", w.tab.TypeString(a, nil), i, w.tab.TypeString(l.Type, nil))
			}
		}
	}
}

// TestFieldsInnerLoop: a non-fields loop with a holder inside the
// range (`for f in fields(&x) { for y in ... { } }`) gets one LoopInfo per
// copy, each with distinct Header, Exit and holder local, its header and
// exit inside its own copy, and its holder loan made once (mir.Validate).
//
//	bb0 -> bb1 header -> bb2 body: holder = &x (loop 1 holder) -> bb4;
//	bb3 latch -> bb7; bb4 inner header: switch false {1: bb5} else bb6;
//	bb5 -> bb4; bb6 inner exit -> bb3; bb7 exit: return.
func TestFieldsInnerLoop(t *testing.T) {
	w := newWorld()
	tab := w.tab
	p0, p1 := tab.Param(0), tab.Param(1)
	dump := w.fn("Dump", 1, []types.TypeID{p0}, types.Void)
	gb := mir.NewBody(dump, w.span(), types.Void)
	gb.GenericCount = 2
	gb.AddParam(p0, w.in.Intern("x"), 0)
	value := gb.AddLocal(mir.Local{Type: tab.Ref(p1), Kind: mir.LocalUser, Loop: mir.NoLoop})
	holder := gb.AddLocal(mir.Local{Type: tab.Ref(p1), Kind: mir.LocalLoopHolder, Loop: 1})
	bb := blocks(gb, 8)
	gb.Loops = append(gb.Loops,
		mir.LoopInfo{Header: bb[1], Exit: bb[7], Holder: mir.NoLocal, FieldsOf: p0, FieldsParam: 1},
		mir.LoopInfo{Header: bb[4], Exit: bb[6], Holder: holder})
	w.gotoTerm(gb, bb[0], bb[1])
	w.gotoTerm(gb, bb[1], bb[2])
	fp := gb.Place(1, mir.Projection{Kind: mir.ProjField, FieldLoop: true, A: 0, Type: p1})
	w.stmt(gb, bb[2], gb.Place(value), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: fp, Type: tab.Ref(p1)})
	fp2 := gb.Place(1, mir.Projection{Kind: mir.ProjField, FieldLoop: true, A: 0, Type: p1})
	w.stmt(gb, bb[2], gb.Place(holder), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: fp2, Type: tab.Ref(p1),
		Origin: mir.OriginLoopHolder, Loop: 1})
	w.gotoTerm(gb, bb[2], bb[4])
	w.gotoTerm(gb, bb[3], bb[7])
	gb.Blocks[bb[4]].Term = mir.Terminator{Kind: mir.TermSwitch, Span: gb.AddSpan(w.span()), On: mir.Imm(types.Bool, 0),
		Cases: gb.AddCases(mir.SwitchCase{Value: 1, Target: bb[5]}), Default: bb[6]}
	w.gotoTerm(gb, bb[5], bb[4])
	w.gotoTerm(gb, bb[6], bb[3])
	w.retTerm(gb, bb[7])
	w.bodies[dump] = gb

	ib := w.instantiate(t, dump, w.record("p", "q", "r"))
	pr := (&mir.Printer{Types: tab, Items: w.items}).Print(ib)
	var inner []mir.LoopInfo
	for _, lp := range ib.Loops {
		if lp.Holder != mir.NoLocal {
			inner = append(inner, lp)
		}
	}
	if len(inner) != 3 {
		t.Fatalf("want 3 inner loops, got %d: %+v\n%s", len(inner), ib.Loops, pr)
	}
	hs, es, ls := map[mir.BlockID]bool{}, map[mir.BlockID]bool{}, map[mir.LocalID]bool{}
	for i, lp := range inner {
		hs[lp.Header], es[lp.Exit], ls[lp.Holder] = true, true, true
		if ib.Locals[lp.Holder].Kind != mir.LocalLoopHolder || ib.Locals[lp.Holder].Type != tab.Ref(types.Int) {
			t.Fatalf("inner loop %d: holder _%d is %v %s", i, lp.Holder, ib.Locals[lp.Holder].Kind, tab.TypeString(ib.Locals[lp.Holder].Type, nil))
		}
		// The header's loop edge stays inside the copy: its case goes to a
		// block that comes back to it.
		h := ib.Blocks[lp.Header].Term
		if h.Kind != mir.TermSwitch || h.Default != lp.Exit {
			t.Fatalf("inner loop %d: header bb%d is %v, default bb%d, want exit bb%d:\n%s", i, lp.Header, h.Kind, h.Default, lp.Exit, pr)
		}
		if back := ib.Blocks[ib.Cases[h.Cases.Start].Target].Term; back.Kind != mir.TermGoto || back.Target != lp.Header {
			t.Fatalf("inner loop %d: its body does not come back to its header:\n%s", i, pr)
		}
	}
	if len(hs) != 3 || len(es) != 3 || len(ls) != 3 {
		t.Fatalf("inner loops share headers, exits or holders: %+v\n%s", inner, pr)
	}
	if len(gb.Loops) != 2 || gb.Loops[1].Holder != holder {
		t.Fatal("the generic body was modified")
	}
}
