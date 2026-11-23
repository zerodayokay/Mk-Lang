// Why: accumulator tail-recursion elimination (rectre.go) rewrites self-calls
// into loops with branchless wrapping accumulation and sticky overflow flags;
// any unsoundness in non-negativity, panic ordering, panic spans, stack-frame
// aliasing, or parallel parameter updates would change program behaviour.
//
// What: TestRecTRE_Agrees (end-to-end eval equivalence on/off across signed
// and unsigned checked +, wrapping/bitwise ops, direct tail rec, mut slices,
// parameter swaps, overflow traps, panic-order preservation, and negative
// soundness cases) and TestRecTRE_RewriteShape (checks which functions rewrite
// and which are conservatively rejected).
//
// How: each test lowers with pcOpt{fold: true, version: &VersionOptions{}}
// (which validates every rewritten body and verifies the input body is
// untouched via pvVersion) and compares eval output against pcEval with
// NoRanges (versions off).

package lower

import (
	"strings"
	"testing"

	"mk/internal/mir"
)

func hasSelfCallInBody(b *mir.Body) int {
	n := 0
	for bi := range b.Blocks {
		if isDirectSelfCall(b, &b.Blocks[bi].Term) {
			n++
		}
	}
	return n
}

func TestRecTRE_Agrees(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		wantPanic string // non-empty if the program must trap with this substring
		fn        string // function to inspect for rewrite
		wantSelf  int    // expected direct self-calls remaining in fn (-1 to skip check)
		wantLoops int    // minimum b.Loops count in fn (-1 to skip check)
	}{
		{
			name: "binarytrees-check",
			src: `
struct Node {
	left: u32
	right: u32
}
Check(nodes: []Node, idx: u32): int {
	n := nodes[int(idx)]
	if n.left == 0 {
		return 1
	}
	return 1 + Check(nodes, n.left) + Check(nodes, n.right)
}
H(): int {
	var ns: [8]Node = [Node(left: 0, right: 0); 8]
	ns[1] = Node(left: 2, right: 3)
	ns[2] = Node(left: 4, right: 5)
	ns[3] = Node(left: 6, right: 7)
	return Check(ns[..], 1)
}
`,
			fn:        "Check",
			wantSelf:  1, // left call stays, right call eliminated
			wantLoops: 1,
		},
		{
			name: "signed-checked-add-branch-nonneg",
			src: pvV + `
Sum(n: int): int {
	if n <= 0 {
		return 0
	}
	return n + Sum(n - 1)
}
H(): int {
	return Sum(V(10))
}
`,
			fn:        "Sum",
			wantSelf:  0,
			wantLoops: 1,
		},
		{
			name: "signed-checked-add-overflow-i8-triple-wrap",
			// 100 + 100 + 100 in i8: wraps past 127 to -56, then wraps back to +44!
			// Sticky-OR mask must catch the intermediate overflow and trap at the exact + span.
			src: pvV + `
Sum3(n: int): i8 {
	if n <= 0 {
		return 0
	}
	return 100 + Sum3(n - 1)
}
H(): int {
	return int(Sum3(V(3)))
}
`,
			wantPanic: "panic 1",
			fn:        "Sum3",
			wantSelf:  0,
			wantLoops: 1,
		},
		{
			name: "signed-checked-add-panic-order-bounds-before-overflow",
			// First 2 steps accumulate 100 + 100 (overflowing i8), but the 3rd
			// step indexes out of bounds before reaching base return.
			// Bounds panic MUST fire before overflow panic!
			src: pvV + `
Walk(a: []i8, i: int): i8 {
	v := a[i]
	if v == 0 {
		return 0
	}
	return 100 + Walk(a, i + 1)
}
H(): int {
	var a: [2]i8 = [1, 1]
	return int(Walk(a[..], V(0)))
}
`,
			wantPanic: "panic 2",
			fn:        "Walk",
			wantSelf:  0,
			wantLoops: 1,
		},
		{
			name: "signed-negative-base-must-not-transform",
			// 100 + (100 + (-100)) == 100 in i8: does NOT overflow!
			// Must NOT rewrite to left-associative accumulation.
			src: pvV + `
NegBase(n: int): i8 {
	if n <= 0 {
		return -100
	}
	return 100 + NegBase(n - 1)
}
H(): int {
	return int(NegBase(V(2)))
}
`,
			fn:        "NegBase",
			wantSelf:  1,
			wantLoops: 0,
		},
		{
			name: "signed-negative-term-must-not-transform",
			// 100 + (100 + (-100 + 0)) == 100 in i8: does NOT overflow!
			src: pvV + `
SumArr(a: []i8, i: int): i8 {
	if i >= a.Len() {
		return 0
	}
	return a[i] + SumArr(a, i + 1)
}
H(): int {
	var a: [3]i8 = [100, 100, -100]
	return int(SumArr(a[..], V(0)))
}
`,
			fn:        "SumArr",
			wantSelf:  1,
			wantLoops: 0,
		},
		{
			name: "unsigned-checked-add-ok",
			src: pvV + `
USum(n: u8): u8 {
	if n == 0 {
		return 0
	}
	return n + USum(n - 1)
}
H(): int {
	return int(USum(Cast[u8](V(10))))
}
`,
			fn:        "USum",
			wantSelf:  0,
			wantLoops: 1,
		},
		{
			name: "unsigned-checked-add-overflow-triple-wrap",
			// 200 + 200 + 200 in u8: wraps twice; sticky carry must catch it!
			src: pvV + `
USum3(n: int): u8 {
	if n <= 0 {
		return 0
	}
	return 200 + USum3(n - 1)
}
H(): int {
	return int(USum3(V(3)))
}
`,
			wantPanic: "panic",
			fn:        "USum3",
			wantSelf:  0,
			wantLoops: 1,
		},
		{
			name: "wrapping-add-mul-bitops-with-negatives",
			src: pvV + `
WrapAdd(n: int): int {
	if n <= 0 { return -7 }
	return -3 +% WrapAdd(n - 1)
}
WrapMul(n: int): int {
	if n <= 1 { return -2 }
	return n *% WrapMul(n - 1)
}
BitOrRec(n: int): int {
	if n <= 0 { return 1 }
	return (1 << (n & 7)) | BitOrRec(n - 1)
}
BitAndRec(n: int): int {
	if n <= 0 { return 255 }
	return (255 ^ (1 << (n & 3))) & BitAndRec(n - 1)
}
BitXorRec(n: int): int {
	if n <= 0 { return 42 }
	return (n *% 17) ^ BitXorRec(n - 1)
}
H(): int {
	return WrapAdd(V(5)) +% WrapMul(V(6)) +% BitOrRec(V(5)) +% BitAndRec(V(3)) +% BitXorRec(V(7))
}
`,
			fn:        "WrapMul",
			wantSelf:  0,
			wantLoops: 1,
		},
		{
			name: "mixed-direct-and-checked-add",
			src: pvV + `
CountEven(n: int): int {
	if n <= 0 { return 0 }
	if (n & 1) == 1 {
		return CountEven(n - 1)
	}
	return 1 + CountEven(n - 1)
}
H(): int {
	return CountEven(V(11))
}
`,
			fn:        "CountEven",
			wantSelf:  0,
			wantLoops: 1,
		},
		{
			name: "direct-tail-rec-param-swap",
			src: pvV + `
SwapRec(a: int, b: int, n: int): int {
	if n <= 0 { return a *% 100 +% b }
	return SwapRec(b, a +% 1, n - 1)
}
H(): int {
	return SwapRec(V(3), V(7), V(5))
}
`,
			fn:        "SwapRec",
			wantSelf:  0,
			wantLoops: 1,
		},
		{
			name: "mut-slice-subslice-tail-rec",
			src: pvV + `
ZeroAndCount(s: mut []int): int {
	if s.Len() == 0 { return 0 }
	s[0] = 0
	return 1 + ZeroAndCount(mut s[1..])
}
H(): int {
	var a: [5]int = [1, 2, 3, 4, 5]
	c := ZeroAndCount(mut a[..])
	return c +% a[0] +% a[4]
}
`,
			fn:        "ZeroAndCount",
			wantSelf:  0,
			wantLoops: 1,
		},
		{
			name: "stack-ref-escape-must-not-transform",
			// Passing &local into self-call requires distinct stack frames;
			// must NOT transform into a loop that overwrites the same slot.
			src: pvV + `
Chain(prev: &int, n: int): int {
	if n <= 0 { return *prev }
	cur := *prev + n
	return *prev + Chain(&cur, n - 1)
}
H(): int {
	x := V(1)
	return Chain(&x, V(4))
}
`,
			fn:        "Chain",
			wantSelf:  1,
			wantLoops: 0,
		},
		{
			name: "two-checked-add-different-spans-must-not-transform",
			// Two checked-add tail sites at different lines: exact panic span
			// must match the original (site 1 vs site 2).
			src: pvV + `
TwoSpans(n: int): i8 {
	if n <= 0 { return 100 }
	if (n & 1) == 0 {
		return 50 + TwoSpans(n - 1)
	}
	return 50 + TwoSpans(n - 1)
}
H(): int {
	return int(TwoSpans(V(2)))
}
`,
			wantPanic: "panic",
			fn:        "TwoSpans",
			wantSelf:  2,
			wantLoops: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			off := pcEval(t, tc.src, nil)
			on, l := pvEval(t, tc.src, VersionOptions{})
			if on != off {
				t.Fatalf("eval mismatch:\n  on:  %q\n  off: %q", on, off)
			}
			if tc.wantPanic != "" && !strings.Contains(on, tc.wantPanic) {
				t.Fatalf("expected panic containing %q, got %q", tc.wantPanic, on)
			}
			if tc.fn != "" {
				var found *mir.Body
				for name, b := range l.bodies {
					if name == tc.fn || strings.HasSuffix(name, "."+tc.fn) {
						found = b
						break
					}
				}
				if found == nil {
					t.Fatalf("function %q not found in lowered bodies", tc.fn)
				}
				if tc.wantSelf >= 0 {
					if got := hasSelfCallInBody(found); got != tc.wantSelf {
						t.Errorf("%s: remaining direct self-calls = %d, want %d", tc.fn, got, tc.wantSelf)
					}
				}
				if tc.wantLoops >= 0 && len(found.Loops) < tc.wantLoops {
					t.Errorf("%s: len(Loops) = %d, want >= %d", tc.fn, len(found.Loops), tc.wantLoops)
				}
			}
		})
	}
}
