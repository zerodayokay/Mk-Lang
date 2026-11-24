// Why: findColdGuards (term.go) attaches cold branch weights only to
// nested-loop versioning guard fallbacks, avoiding register-pressure bloat
// in 2D fast nests without hurting 1D loop 0-trip exits.
// What: TestFindColdGuardsNestedOnly.

package codegen

import (
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

func TestFindColdGuardsNestedOnly(t *testing.T) {
	// Build a minimal function with a guard chain targeting a cloned fast header
	// vs slow header. When nested == true, slow encloses an inner loop.
	build := func(nested bool) (*testEnv, *mir.Body) {
		e := newTestEnv()
		fn := e.fn("F", []types.TypeID{types.Int}, types.Int, 0)
		f := e.body(fn)
		b := f.b
		n := mir.LocalID(1)
		k := b.AddLocal(mir.Local{Type: types.Int, Kind: mir.LocalTemp, Loop: mir.NoLoop})
		t1 := b.AddLocal(mir.Local{Type: types.Bool, Kind: mir.LocalTemp, Loop: mir.NoLoop})
		gt := b.AddLocal(mir.Local{Type: types.Bool, Kind: mir.LocalTemp, Loop: mir.NoLoop})
		sp := f.at(2, 1)

		addBlk := func() mir.BlockID { return b.AddBlock(mir.OriginBody, mir.NoDefer) }
		// b0: pre -> guard b1
		b1 := addBlk() // guard: switch gt [0: slow, _: b2]
		b2 := addBlk() // end: goto fast
		slow := addBlk()
		slowBody := addBlk()
		var slowInnerH, slowInnerLatch mir.BlockID
		if nested {
			slowInnerH = addBlk()
			slowInnerLatch = addBlk()
		}
		slowLatch := addBlk()
		fast := addBlk()
		fastBody := addBlk()
		fastLatch := addBlk()
		exit := addBlk()

		b.Blocks[0].Stmts = append(b.Blocks[0].Stmts,
			mir.Statement{Kind: mir.StAssign, Place: mir.LocalPlace(k), Rvalue: use(mir.Imm(types.Int, 0)), Span: sp})
		b.Blocks[0].Term = mir.Terminator{Kind: mir.TermGoto, Target: b1, Span: sp}

		b.Blocks[b1].Stmts = append(b.Blocks[b1].Stmts,
			mir.Statement{Kind: mir.StAssign, Place: mir.LocalPlace(gt), Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Lt, A: mir.Copy(mir.LocalPlace(k)), B: mir.Copy(mir.LocalPlace(n)), Type: types.Bool}, Span: sp})
		b.Blocks[b1].Term = mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(mir.LocalPlace(gt)), Cases: b.AddCases(mir.SwitchCase{Value: 0, Target: slow}), Default: b2, Span: sp}
		b.Blocks[b2].Term = mir.Terminator{Kind: mir.TermGoto, Target: fast, Span: sp}

		hdrRv := mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Lt, A: mir.Copy(mir.LocalPlace(k)), B: mir.Copy(mir.LocalPlace(n)), Type: types.Bool}
		b.Blocks[slow].Stmts = []mir.Statement{{Kind: mir.StAssign, Place: mir.LocalPlace(t1), Rvalue: hdrRv, Span: sp}}
		b.Blocks[slow].Term = mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(mir.LocalPlace(t1)), Cases: b.AddCases(mir.SwitchCase{Value: 0, Target: exit}), Default: slowBody, Span: sp}
		if nested {
			b.Blocks[slowBody].Term = mir.Terminator{Kind: mir.TermGoto, Target: slowInnerH, Span: sp}
			b.Blocks[slowInnerH].Stmts = []mir.Statement{{Kind: mir.StAssign, Place: mir.LocalPlace(t1), Rvalue: hdrRv, Span: sp}}
			b.Blocks[slowInnerH].Term = mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(mir.LocalPlace(t1)), Cases: b.AddCases(mir.SwitchCase{Value: 0, Target: slowLatch}), Default: slowInnerLatch, Span: sp}
			b.Blocks[slowInnerLatch].Term = mir.Terminator{Kind: mir.TermGoto, Target: slowInnerH, Span: sp}
		} else {
			b.Blocks[slowBody].Term = mir.Terminator{Kind: mir.TermGoto, Target: slowLatch, Span: sp}
		}
		b.Blocks[slowLatch].Term = mir.Terminator{Kind: mir.TermGoto, Target: slow, Span: sp}

		b.Blocks[fast].Stmts = []mir.Statement{{Kind: mir.StAssign, Place: mir.LocalPlace(t1), Rvalue: hdrRv, Span: sp}}
		b.Blocks[fast].Term = mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(mir.LocalPlace(t1)), Cases: b.AddCases(mir.SwitchCase{Value: 0, Target: exit}), Default: fastBody, Span: sp}
		b.Blocks[fastBody].Term = mir.Terminator{Kind: mir.TermGoto, Target: fastLatch, Span: sp}
		b.Blocks[fastLatch].Term = mir.Terminator{Kind: mir.TermGoto, Target: fast, Span: sp}

		b.Blocks[exit].Stmts = []mir.Statement{{Kind: mir.StAssign, Place: mir.LocalPlace(0), Rvalue: use(mir.Imm(types.Int, 0)), Span: sp}}
		b.Blocks[exit].Term = mir.Terminator{Kind: mir.TermReturn, Span: sp}
		return e, b
	}

	e1, b1 := build(false)
	if g := findColdGuards(b1); len(g) != 0 {
		t.Fatalf("1D loop guard should not be marked cold, got %v", g)
	}
	ll1 := emit(t, e1, unitOf(e1, []*mir.Body{b1}), types.X86_64Linux, types.Release)
	if strings.Contains(fnText(ll1, "mk.main.F"), "!prof") {
		t.Fatalf("1D loop guard unexpectedly emitted !prof:\n%s", fnText(ll1, "mk.main.F"))
	}

	e2, b2 := build(true)
	if g := findColdGuards(b2); len(g) != 1 {
		t.Fatalf("nested loop guard should be marked cold (1 block), got %v", g)
	}
	ll2 := emit(t, e2, unitOf(e2, []*mir.Body{b2}), types.X86_64Linux, types.Release)
	if !strings.Contains(fnText(ll2, "mk.main.F"), "!prof") {
		t.Fatalf("nested loop guard missing !prof:\n%s", fnText(ll2, "mk.main.F"))
	}
}
