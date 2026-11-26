// Why: each MIR kind and field lower emits needs a validator rule that
// accepts it and refuses its misuse.
// What: tests of joins inside cleanup copies, spawns through &Scope, AggNone,
// must edges, const arguments, static refs, argument types, the quote builtin,
// the fields loop shape and the place of a consume.
// How: each test builds a small body from the fixture and expects it to
// validate, or expects the named invariant (M7, M9...) to fail.

package mir

import (
	"strings"
	"testing"

	"mk/internal/source"
	"mk/internal/types"
)

// copyBlocks turns Show's defer copy on the success edge into three blocks
// with an internal join: bb2 switches to bb5 or bb6, both go to bb7, which
// returns. It returns the join block.
func copyBlocks(f *fixture, b *Body) BlockID {
	c := local(b, types.Bool)
	x, y, j := b.AddBlock(OriginDefer, 0), b.AddBlock(OriginDefer, 0), b.AddBlock(OriginDefer, 0)
	b.Blocks[2].Term = Terminator{Kind: TermSwitch, On: Copy(LocalPlace(c)), Cases: b.AddCases(SwitchCase{Value: 1, Target: x}), Default: y}
	b.Blocks[x].Term = Terminator{Kind: TermGoto, Target: j}
	b.Blocks[y].Term = Terminator{Kind: TermGoto, Target: j}
	b.Blocks[j].Term = Terminator{Kind: TermReturn, Exit: ExitNormal}
	return j
}

// TestM7CopyWithInternalJoin is invariant M7: blocks inside one copy may
// join, but no edge from the body or from another copy enters a copy except
// at its entry.
func TestM7CopyWithInternalJoin(t *testing.T) {
	f := newFixture()
	b := f.showBody()
	copyBlocks(f, b)
	wantOK(t, f, b, "a defer copy with an internal join")
	expectViolation(t, "M7", func(f *fixture, b *Body) {
		j := copyBlocks(f, b)
		b.Blocks[3].Term.Target = j // the error path jumps into the success copy
	})
	expectViolation(t, "M7", func(f *fixture, b *Body) {
		j := copyBlocks(f, b)
		b.Blocks[4].Term = Terminator{Kind: TermGoto, Target: j} // another copy of defer 0
	})
	expectViolation(t, "M7", func(f *fixture, b *Body) { b.Blocks[3].Term.Target = 2 })
}

// spawnInto makes Show's call a spawn into local s.
func spawnInto(b *Body, s LocalID) {
	t := &b.Blocks[0].Term
	t.Spawn, t.HasErr = s, false
}

// TestM9SpawnThroughScopeRef is invariant M9: a spawn goes into a &Scope
// local, never a reference to another type or a plain value.
func TestM9SpawnThroughScopeRef(t *testing.T) {
	f := newFixture()
	m := f.items.Get(f.load).Module
	scope := f.items.AddItem(&types.Item{Module: m, Name: f.in.Intern("Scope"), Kind: types.KindStruct, Key: types.ItemKey("main", "Scope")})
	other := f.items.AddItem(&types.Item{Module: m, Name: f.in.Intern("Other"), Kind: types.KindStruct, Key: types.ItemKey("main", "Other")})
	b := f.showBody()
	s := local(b, f.tab.Ref(f.tab.Named(scope, nil)))
	spawnInto(b, s)
	env := f.env()
	env.Known = &types.KnownItems{Scope: scope}
	if err := b.Validate(env); err != nil {
		t.Fatalf("spawn through &Scope rejected: %v", err)
	}
	b = f.showBody()
	spawnInto(b, local(b, f.tab.Ref(f.tab.Named(other, nil))))
	if err := b.Validate(env); err == nil || !strings.Contains(err.Error(), "M9:") {
		t.Fatalf("spawn through &Other: %v", err)
	}
	expectViolation(t, "M9", func(f *fixture, b *Body) { spawnInto(b, local(b, types.I64)) })
}

// TestNoneAndVoidOk: AggNone has no operands and builds ?T; AggOk with no
// operand is the success of E!void.
func TestNoneAndVoidOk(t *testing.T) {
	f := newFixture()
	b := f.showBody()
	assign(b, LocalPlace(local(b, f.tab.Optional(types.I32))), Rvalue{Kind: RvAggregate, Agg: AggNone})
	ev := f.tab.ErrorUnion(f.sigs[f.load].Errors, types.Void)
	assign(b, LocalPlace(local(b, ev)), Rvalue{Kind: RvAggregate, Agg: AggOk})
	wantOK(t, f, b, "none and ok() of E!void")
	expectViolation(t, "M4", func(f *fixture, b *Body) {
		assign(b, LocalPlace(local(b, f.tab.Optional(types.I32))), Rvalue{Kind: RvAggregate, Agg: AggNone, Ops: b.AddOperands(Imm(types.I32, 1))})
	})
	expectViolation(t, "M4", func(f *fixture, b *Body) {
		assign(b, LocalPlace(local(b, types.I32)), Rvalue{Kind: RvAggregate, Agg: AggNone})
	})
	expectViolation(t, "M4", func(f *fixture, b *Body) {
		assign(b, LocalPlace(local(b, f.sigs[f.show].ReturnType(f.tab))), Rvalue{Kind: RvAggregate, Agg: AggOk})
	})
}

// TestMustPanicAndNewError: a must edge panics with the error value; the
// new-error mark is only on a fail return.
func TestMustPanicAndNewError(t *testing.T) {
	f := newFixture()
	b := f.showBody()
	b.Blocks[0].Term.Err.Kind = EdgeMust
	b.Blocks[3].Stmts = nil
	b.Blocks[3].Term = Terminator{Kind: TermPanic, Panic: types.PanicMust, HasMsg: true, Msg: Move(LocalPlace(3))}
	b.Blocks[4].Term = Terminator{Kind: TermReturn, Exit: ExitFail, NewError: true}
	wantOK(t, f, b, "a must edge panicking with its error")
	expectViolation(t, "M4", func(f *fixture, b *Body) {
		b.Blocks[3].Term = Terminator{Kind: TermPanic, Panic: types.PanicAssert, HasMsg: true, Msg: Move(LocalPlace(3))}
	})
	expectViolation(t, "M6", func(f *fixture, b *Body) { b.Blocks[2].Term.NewError = true })
	expectViolation(t, "M6", func(f *fixture, b *Body) { b.Blocks[1].Term.NewError = true })
}

// TestConstArgValue: a const parameter used as a value has its declared
// type.
func TestConstArgValue(t *testing.T) {
	setup := func(f *fixture, b *Body, ty types.TypeID) {
		f.sigs[f.show].Generics = []types.GenericParam{{Const: true, Name: f.in.Intern("N"), Type: types.Int}}
		b.GenericCount = 1
		assign(b, LocalPlace(local(b, ty)), Rvalue{Kind: RvConstArg, Type: ty, Len: types.ConstArg{Kind: types.CAParam, Param: 0}})
	}
	f := newFixture()
	b := f.showBody()
	setup(f, b, types.Int)
	wantOK(t, f, b, "N read as int")
	if !strings.Contains((&Printer{Types: f.tab, Items: f.items}).Print(b), "ConstArg[int]") {
		t.Fatal("printer does not spell RvConstArg")
	}
	expectViolation(t, "M4", func(f *fixture, b *Body) { setup(f, b, types.I32) })
	expectViolation(t, "M2", func(f *fixture, b *Body) {
		assign(b, LocalPlace(local(b, types.Int)), Rvalue{Kind: RvConstArg, Type: types.Int, Len: types.ConstArg{Kind: types.CAParam}})
	})
}

// TestStaticRefToConst: &C of a const item is RvGlobalRef into &T.
func TestStaticRefToConst(t *testing.T) {
	add := func(f *fixture) types.ItemID {
		return f.items.AddItem(&types.Item{Module: f.items.Get(f.load).Module, Name: f.in.Intern("C"), Kind: types.KindConst,
			Key: types.ItemKey("main", "C"), Decl: &types.ConstDecl{Type: types.I32}})
	}
	f := newFixture()
	b := f.showBody()
	c := add(f)
	assign(b, LocalPlace(local(b, f.tab.Ref(types.I32))), Rvalue{Kind: RvGlobalRef, Item: c})
	assign(b, LocalPlace(local(b, types.I32)), Rvalue{Kind: RvConstRef, Item: c})
	wantOK(t, f, b, "&C and C")
	expectViolation(t, "M4", func(f *fixture, b *Body) {
		assign(b, LocalPlace(local(b, types.I32)), Rvalue{Kind: RvGlobalRef, Item: add(f)})
	})
}

// TestArgumentTypes: argument types equal the callee's parameters, and a
// generic callee gets all its generic arguments.
func TestArgumentTypes(t *testing.T) {
	expectViolation(t, "M5", func(f *fixture, b *Body) {
		b.Args[0].Operand = Copy(LocalPlace(local(b, types.I32)))
	})
	expectViolation(t, "M5", func(f *fixture, b *Body) {
		f.sigs[f.load].Generics = []types.GenericParam{{Name: f.in.Intern("T")}}
	})
}

// TestCopyOfWholeNonPlainLocal: with facts, a whole non-plain local is
// read only by Move; a field of it, or a reborrow argument, may be a Copy.
func TestCopyOfWholeNonPlainLocal(t *testing.T) {
	f := newFixture()
	b := f.showBody()
	box := types.Str // stands for a unique type in this test
	s := local(b, box)
	env := f.env()
	env.Facts = func(t types.TypeID) (types.Facts, bool) {
		if t == box {
			return types.Facts{Kind: types.Unique}, true
		}
		return types.Facts{Kind: types.Plain}, true
	}
	assign(b, LocalPlace(local(b, box)), use(Move(LocalPlace(s))))
	if err := b.Validate(env); err != nil {
		t.Fatalf("move rejected: %v", err)
	}
	assign(b, LocalPlace(local(b, box)), use(Copy(LocalPlace(s))))
	if err := b.Validate(env); err == nil || !strings.Contains(err.Error(), "M10:") {
		t.Fatalf("copy of a whole unique local: %v", err)
	}
}

// TestQuoteBuiltin: quote { } takes a []u8 template constant first.
func TestQuoteBuiltin(t *testing.T) {
	quote := func(f *fixture, b *Body, tmpl Operand) {
		b.Blocks[1].Term = Terminator{Kind: TermCall, Callee: Callee{Kind: CallBuiltin, Builtin: BuiltinQuote},
			Args: b.AddArgs(Arg{Operand: tmpl, Mode: ArgCopy}), Dest: LocalPlace(local(b, types.Int)), Target: 2, Spawn: NoLocal}
	}
	f := newFixture()
	b := f.showBody()
	tmpl := f.consts.Intern(types.ConstValue{Type: f.tab.Slice(types.U8), Align: 8, Bytes: make([]byte, 16)})
	quote(f, b, Const(f.tab.Slice(types.U8), tmpl))
	wantOK(t, f, b, "quote with a template")
	if !strings.Contains((&Printer{Types: f.tab, Items: f.items}).Print(b), "builtin Quote") {
		t.Fatal("printer does not spell the quote builtin")
	}
	expectViolation(t, "M5", func(f *fixture, b *Body) { quote(f, b, Imm(types.Int, 0)) })
}

// TestFieldsLoopShape: a generic fields loop lives in a generic body, names
// one of its generics, its blocks are exactly [Header, Exit), and its latch
// is Header+2, ending in goto Exit.
//
//	bb0: goto bb1; bb1 (header): goto bb2; bb2 (body): goto bb3;
//	bb3 (latch): goto bb4; bb4 (exit): return
func TestFieldsLoopShape(t *testing.T) {
	f := newFixture()
	rec := f.tab.Record([]types.RecordField{{Name: f.in.Intern("a"), Type: types.I32}})
	build := func() *Body {
		b := NewBody(types.NoItem, source.Span{File: 1, Start: 0, End: 10}, types.Void)
		sp := b.AddSpan(source.Span{File: 1, Start: 2, End: 8})
		b.GenericCount = 1
		for i := 0; i < 5; i++ {
			b.AddBlock(OriginBody, NoDefer)
		}
		for i := 0; i < 4; i++ {
			b.Blocks[i].Term = Terminator{Kind: TermGoto, Target: BlockID(i + 1), Span: sp}
		}
		b.Blocks[4].Term = Terminator{Kind: TermReturn, Exit: ExitNormal, Span: sp}
		b.Loops = append(b.Loops, LoopInfo{Header: 1, Exit: 4, Holder: NoLocal, FieldsOf: rec})
		return b
	}
	for _, c := range []struct {
		name string
		brk  func(b *Body)
		want string // "" accepts
	}{
		{"header, body, latch", func(*Body) {}, ""},
		{"hidden generic out of range", func(b *Body) { b.Loops[0].FieldsParam = 1 }, "M9: loop 0: hidden generic"},
		{"a range block the header does not reach", func(b *Body) { b.Blocks[2].Term.Target = 4 }, "are exactly"},
		{"no latch below the exit", func(b *Body) { b.Loops[0].Exit = 3 }, "has no latch bb3"},
		{"latch does not goto the exit", func(b *Body) {
			b.Blocks[3].Term = Terminator{Kind: TermReturn, Exit: ExitNormal}
		}, "latch bb3 does not goto bb4"},
		{"an instance body", func(b *Body) { b.GenericCount = 0 }, "M2:"},
	} {
		b := build()
		c.brk(b)
		err := b.Validate(&Env{Types: f.tab})
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: rejected: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
}

// TestM4Consume is invariant M4 for StConsume: its place is a struct with
// no fields, never a struct with fields or a scalar.
func TestM4Consume(t *testing.T) {
	consume := func(f *fixture, b *Body, decl *types.StructDecl, ty types.TypeID) {
		if decl != nil {
			m := f.items.Get(f.show).Module
			it := f.items.AddItem(&types.Item{Module: m, Name: f.in.Intern("E"), Kind: types.KindStruct, Key: types.ItemKey("main", "E"), Decl: decl})
			ty = f.tab.Named(it, nil)
		}
		l := local(b, ty)
		b.Blocks[1].Stmts = append(b.Blocks[1].Stmts, Statement{Kind: StConsume, Place: LocalPlace(l)})
	}
	f := newFixture()
	b := f.showBody()
	consume(f, b, &types.StructDecl{}, types.NoType)
	wantOK(t, f, b, "a consume of a struct with no fields")
	expectViolation(t, "M4", func(f *fixture, b *Body) {
		consume(f, b, &types.StructDecl{Fields: []types.FieldDecl{{Type: types.I32}}}, types.NoType)
	})
	expectViolation(t, "M4", func(f *fixture, b *Body) { consume(f, b, nil, types.I32) })
}
