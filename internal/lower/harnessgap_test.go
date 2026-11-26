// Why: a weak harness can make the corpus test pass while skipping bodies;
// these tests keep lower's harness honest.
// What: TestHarnessCountsRejectedBodies, TestTargetFieldLowers,
// TestFactsOfPoisonIsCheckGap, TestPoisonParamIsCheckGap.
// How: a body check rejects is counted, never silently dropped; the fake
// prelude's TargetInfo carries std's fields and std.core has the driver's
// synthesized Target item, so `Target.<field>` lowers; and a Poison type
// that reaches the ownership facts is a tagged check gap.

package lower

import (
	"strings"
	"testing"

	"mk/internal/source"
	"mk/internal/types"
)

// TestHarnessCountsRejectedBodies: lowerUnitsJudged returns a body stage 6
// rejects in its rejected list, with its key and code, and lowers the rest.
// lowerUnitsWith once dropped it with no trace, so a
// verdict-ok block whose body the harness's fakes broke (PageSize, E0203 on
// a TargetInfo with no fields) left TestCorpusLowersAndValidates green.
func TestHarnessCountsRejectedBodies(t *testing.T) {
	w := newWorld(t, true)
	b := w.add("m", opt{}, "Ok(): int {\n\treturn 1\n}\nBad(): int {\n\treturn true\n}\n")
	bodies, ices, rejected := lowerUnitsJudged(w, b, false)
	if len(ices) != 0 {
		t.Fatalf("ICEs: %q", ices)
	}
	if _, ok := bodies["m.Ok"]; !ok || len(bodies) != 1 {
		t.Fatalf("want only m.Ok lowered, got %d bodies", len(bodies))
	}
	if len(rejected) != 1 || !strings.HasPrefix(rejected[0], "m.Bad at ") || !strings.Contains(rejected[0], "E0") {
		t.Fatalf("want m.Bad rejected with its code, got %q", rejected)
	}
	// lowerUnitsWith keeps its shape: the same bodies, no rejected list.
	if bs, _ := lowerUnitsWith(w, b, false); len(bs) != 1 {
		t.Fatalf("lowerUnitsWith lowered %d bodies, want 1", len(bs))
	}
}

// TestTargetFieldLowers: under the fake prelude, `Target.pointer_bytes`
// checks (TargetInfo has std's fields) and lowers to a field read of
// RvConstRef of std.core.Target (the driver's synthesized item), and
// validates. It fails if either fake loses what PageSize needs.
func TestTargetFieldLowers(t *testing.T) {
	l := lowerSrc(t, "PointerBytes(): int {\n\treturn Target.pointer_bytes\n}\nIsArch(): bool {\n\treturn Target.arch == Arch.A\n}\n")
	for _, k := range []string{"PointerBytes", "IsArch"} {
		f := l.print(k)
		if !strings.Contains(f, "std.core.Target") {
			t.Errorf("%s does not read std.core.Target:\n%s", k, f)
		}
	}
}

// TestFactsOfPoisonIsCheckGap: a Poison type that reaches the ownership
// facts without passing typeOf (a field read whose declared type check
// left Poison) raises the tagged check-gap ICE, which corpusPending sorts
// as another package's, not the opaque "no facts for <error>". FuzzLower found
// the shape (testdata/fuzz/FuzzLower/d1f8f93246ace338: `struct Buf {
// data: [!0]u8 }` and `return self.data`) while check accepted the field
// type silently; check reports E0308 there, so the seed stops before
// stage 6 and this test calls facts directly.
func TestFactsOfPoisonIsCheckGap(t *testing.T) {
	w := newWorld(t, true)
	b := w.add("m", opt{}, "F(): int {\n\treturn 1\n}\n")
	b.wantClean(t)
	cfg := w.lowerCfg()
	id := b.item("F")
	bl := newBuilder(cfg, FromCheck(cfg, w.c, w.c.Check(b.m, id)))
	ie := source.Guard("F", source.Span{}, func() { bl.facts(types.Poison, source.Span{}) })
	if ie == nil {
		t.Fatal("facts of Poison raised no ICE")
	}
	if !corpusPending(ie.What) || !strings.Contains(ie.What, "Poison type with no diagnostic") {
		t.Fatalf("not a tagged check-gap ICE: %s", ie.What)
	}
	// An ordinary type still has its facts.
	if f := bl.facts(types.Int, source.Span{}); f.Kind != types.Plain {
		t.Fatalf("int facts: %+v", f)
	}
}

// TestPoisonParamIsCheckGap: a parameter of Poison type that reaches lower
// with no diagnostic is the tagged check-gap ICE at the parameter, reported
// as E0000 with a position, never a panic.
func TestPoisonParamIsCheckGap(t *testing.T) {
	w := newWorld(t, true)
	b := w.add("m", opt{}, "F(x: int): int {\n\treturn x\n}\n")
	b.wantClean(t)
	cfg := w.lowerCfg()
	cb := w.c.Check(b.m, b.item("F"))
	if cb.Sig == nil || len(cb.Sig.Params) != 1 {
		t.Fatalf("F's body carries no signature: %+v", cb.Sig)
	}
	s := *cb.Sig
	s.Params = append([]types.Param(nil), s.Params...)
	s.Params[0].Type = types.Poison
	cb.Sig = &s
	ie := source.Guard("F", source.Span{}, func() { newBuilder(cfg, FromCheck(cfg, w.c, cb)) })
	if ie == nil {
		t.Fatal("a Poison parameter raised no ICE")
	}
	if !corpusPending(ie.What) || !strings.Contains(ie.What, "a parameter of Poison type with no diagnostic") {
		t.Fatalf("not a tagged check-gap ICE: %s", ie.What)
	}
	if ie.Span != s.Params[0].Span || ie.Span == (source.Span{}) {
		t.Fatalf("ICE at %v, want the parameter's span %v", ie.Span, s.Params[0].Span)
	}
}
