// Why: a requires predicate may not fold build-constant calls such as
// UsesTasks(), because UsesTasks is unset (false) on mono's first drain and
// folding it inside a predicate would drop the entry check without rebuilding
// the callee once UsesTasks settles to true.
//
// What: TestRequiresFold_DoesNotFoldUsesTasks,
// TestRequiresFold_DoesNotFoldUsesTasksFalse, and
// TestRequiresFold_NamedConstAndMultiClause.
//
// How: collects tiny std stand-ins with withKeys and mixed requires clauses
// and checks whether instantiated bodies retain or fold their checks.

package mono

import (
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

func hasCallTo(b *mir.Body, target types.ItemID) bool {
	if b == nil {
		return false
	}
	for i := range b.Blocks {
		t := &b.Blocks[i].Term
		if t.Kind == mir.TermCall && t.Callee.Kind == mir.CallDirect && t.Callee.Fn.Item == target {
			return true
		}
	}
	return false
}

func TestRequiresFold_DoesNotFoldUsesTasks(t *testing.T) {
	withKeys(t)
	src := `
struct IoTable {
    open: fn(int): int
    write: fn(int): int
    send: fn(int): int
}

UsesTasks(): bool

WaitWord(x: int): int { return x }

Guarded[const N: int](x: int): int
    requires N == 1 && !UsesTasks()
{
    return x + N
}

Main(): int {
    _ = WaitWord(1)
    return Guarded[1](5)
}
`
	sw := newSrcWorldAt(t, "m", true, src)
	in, ds := Collect(sw, []Root{{Kind: RootMain, Item: sw.item(t, "Main")}}, Options{Target: types.X86_64Windows, Validate: true})
	if len(ds) > 0 {
		sw.fail(t, "collect", ds)
	}
	if !in.UsesTasks() {
		t.Fatalf("UsesTasks: false, want true (WaitWord was called)")
	}
	g := instancesOf(in, sw.item(t, "Guarded"))
	if len(g) != 1 {
		t.Fatalf("Guarded instances %d, want 1", len(g))
	}
	predKey := types.ContractKey(types.ItemKey("m", "Guarded"), "requires", 0)
	predItem, ok := sw.items.ByKey(predKey)
	if !ok {
		t.Fatalf("missing predicate %s", predKey)
	}
	if !hasCallTo(g[0].Body, predItem) {
		t.Fatalf("Guarded[1] dropped requires check even though UsesTasks settled to true")
	}
	predInsts := instancesOf(in, predItem)
	if len(predInsts) != 1 || hasCallTo(predInsts[0].Body, sw.item(t, "UsesTasks")) {
		t.Fatalf("predicate instances %v did not fold UsesTasks once settled", predInsts)
	}
}

func TestRequiresFold_DoesNotFoldUsesTasksFalse(t *testing.T) {
	withKeys(t)
	src := `
struct IoTable {
    open: fn(int): int
    write: fn(int): int
    send: fn(int): int
}

UsesTasks(): bool

Guarded[const N: int](x: int): int
    requires N == 1 && !UsesTasks()
{
    return x + N
}

Main(): int {
    return Guarded[1](5)
}
`
	sw := newSrcWorldAt(t, "m", true, src)
	in, ds := Collect(sw, []Root{{Kind: RootMain, Item: sw.item(t, "Main")}}, Options{Target: types.X86_64Windows, Validate: true})
	if len(ds) > 0 {
		sw.fail(t, "collect", ds)
	}
	if in.UsesTasks() {
		t.Fatalf("UsesTasks: true, want false")
	}
	g := instancesOf(in, sw.item(t, "Guarded"))
	if len(g) != 1 {
		t.Fatalf("Guarded instances %d, want 1", len(g))
	}
	predKey := types.ContractKey(types.ItemKey("m", "Guarded"), "requires", 0)
	predItem, _ := sw.items.ByKey(predKey)
	if !hasCallTo(g[0].Body, predItem) {
		t.Fatalf("Guarded[1] folded a requires clause that calls UsesTasks()")
	}
}

func TestRequiresFold_NamedConstAndMultiClause(t *testing.T) {
	src := `
const Limit: int = 8

WithConst[const N: int](x: int): int
    requires N < Limit
{
    return x + N
}

Multi[const N: int](x: int): int
    requires N == 4
    requires x > 0
{
    return x + N
}

BadConst[const N: int](): int
    requires N < 4
{
    return N
}

WrapBad[U](): int {
    return BadConst[SizeOf[[2]U]()]()
}

Main(): int {
    return WithConst[4](1) + Multi[4](1) + WrapBad[[4]u8]()
}
`
	sw := newSrcWorldAt(t, "m", false, src)
	ld := sw.items.Get(sw.item(t, "Limit")).Decl.(*types.ConstDecl)
	ld.Value = intConst(sw.consts, 8)
	ld.HasValue = true
	in, ds := Collect(sw, []Root{{Kind: RootMain, Item: sw.item(t, "Main")}}, Options{Target: types.X86_64Windows, Validate: true})
	if len(ds) > 0 {
		sw.fail(t, "collect", ds)
	}
	req := func(fn string, idx int) types.ItemID {
		id, ok := sw.items.ByKey(types.ContractKey(types.ItemKey("m", fn), "requires", idx))
		if !ok {
			t.Fatalf("missing %s#requires%d", fn, idx)
		}
		return id
	}
	if wc := instancesOf(in, sw.item(t, "WithConst")); len(wc) != 1 || !hasCallTo(wc[0].Body, req("WithConst", 0)) {
		t.Fatalf("WithConst dropped clause reading named const Limit")
	}
	if m := instancesOf(in, sw.item(t, "Multi")); len(m) != 1 || hasCallTo(m[0].Body, req("Multi", 0)) || !hasCallTo(m[0].Body, req("Multi", 1)) {
		t.Fatalf("Multi[4]: want requires0 folded and requires1 kept")
	}
	if bc := instancesOf(in, sw.item(t, "BadConst")); len(bc) != 1 || !hasCallTo(bc[0].Body, req("BadConst", 0)) {
		t.Fatalf("BadConst[8] dropped false requires clause")
	}
}
