// Why: monotone while-loop bounds versioning (rangemono.go) hoists bounds and
// step-overflow checks out of strided ascending, descending, and two-pointer
// converging loops; any unsound guard would read out of bounds or miss an
// overflow trap on edge inputs.
//
// What: TestMonoVersion_Agrees, covering all three monotone loop kinds
// (monoAsc, monoDesc, monoMeet), early exits, zero-iteration cases, bounds
// and step-overflow trap equivalence (exact panic code & site), and negative
// cases (step-before-use, negative step, starting below Ne stop) that must
// preserve original trap semantics.

package lower

import (
	"strings"
	"testing"
)

func TestMonoVersion_Agrees(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		wantBnd   int    // expected versioned bounds loops (-1 to skip)
		wantPanic string // non-empty if the program must panic with this substring
	}{
		{
			name: "asc-strided-sieve-ok",
			src: pvV + `
Mark(flags: mut []u8, n: int, step: int) {
	var j := step * step
	for j <= n {
		flags[j] = 0
		j += step
	}
}
H(): int {
	var a: [20]u8 = [1; 20]
	Mark(mut a[0..V(20)], V(19), V(3))
	return int(a[9]) +% int(a[12]) +% int(a[10])
}
`,
			wantBnd: 1,
		},
		{
			name: "asc-strided-oob-traps-exact",
			src: pvV + `
Mark(flags: mut []u8, n: int, step: int) {
	var j := step * step
	for j <= n {
		flags[j] = 0
		j += step
	}
}
H(): int {
	var a: [10]u8 = [1; 10]
	Mark(mut a[0..V(10)], V(12), V(3))
	return int(a[0])
}
`,
			wantBnd:   1,
			wantPanic: "panic 2",
		},
		{
			name: "asc-strided-step-overflow-traps-exact",
			src: pvV + `
H(): int {
	var a: [8]int = [1; 8]
	s := a[0..V(8)]
	var j := V(1)
	hi := V(5)
	step := V(9223372036854775807)
	var sum := 0
	for j <= hi {
		sum = sum +% s[j]
		j += step
	}
	return sum
}
`,
			wantBnd:   1,
			wantPanic: "panic 1",
		},
		{
			name: "asc-negative-step-falls-back-and-traps",
			src: pvV + `
H(): int {
	var a: [8]int = [1; 8]
	s := a[0..V(8)]
	var j := V(2)
	hi := V(5)
	step := V(-1)
	var sum := 0
	for j <= hi {
		sum = sum +% s[j]
		j += step
	}
	return sum
}
`,
			wantBnd:   1,
			wantPanic: "panic 2",
		},
		{
			name: "asc-step-before-use-must-not-version",
			src: pvV + `
H(): int {
	var a: [6]int = [1; 6]
	s := a[0..V(6)]
	var j := V(0)
	var sum := 0
	for j < V(6) {
		j += 1
		sum = sum +% s[j]
	}
	return sum
}
`,
			wantBnd:   0,
			wantPanic: "panic 2",
		},
		{
			name: "desc-ne-fannkuch-count-ok",
			src: pvV + `
H(): int {
	var count: [8]int = [0; 8]
	var r := V(6)
	for r != 1 {
		count[r - 1] = r
		r -= 1
	}
	return count[1] +% count[5]
}
`,
			wantBnd: 1,
		},
		{
			name: "desc-ne-zero-iters",
			src: pvV + `
H(): int {
	var count: [8]int = [7; 8]
	var r := V(1)
	for r != 1 {
		count[r - 1] = r
		r -= 1
	}
	return count[0]
}
`,
			wantBnd: 1,
		},
		{
			name: "desc-ne-below-stop-traps-exact",
			// Starting at r = 0 with `r != 1`: guard fails (`0 >= 1` false),
			// slow loop runs count[-1] and traps with bounds panic.
			src: pvV + `
H(): int {
	var count: [8]int = [0; 8]
	var r := V(0)
	for r != 1 {
		count[r - 1] = r
		r -= 1
	}
	return count[0]
}
`,
			wantBnd:   1,
			wantPanic: "panic 2",
		},
		{
			name: "desc-early-exit-trim-ok",
			src: pvV + `
Trim(buf: []u8, vs0: int, ve0: int): int {
	var vs := vs0
	var ve := ve0
	for vs < ve && (buf[vs] == 32 || buf[vs] == 9) {
		vs += 1
	}
	for ve > vs && (buf[ve - 1] == 32 || buf[ve - 1] == 9) {
		ve -= 1
	}
	return ve -% vs
}
H(): int {
	var b: [8]u8 = [32, 9, 65, 66, 67, 32, 9, 32]
	return Trim(b[0..V(8)], V(0), V(8))
}
`,
			wantBnd: 2,
		},
		{
			name: "meet-two-pointer-flip-ok",
			src: pvV + `
Flip(p: mut []int, k: int) {
	var i := 0
	var j := k
	for i < j {
		t := p[i]
		p[i] = p[j]
		p[j] = t
		i += 1
		j -= 1
	}
}
H(): int {
	var a: [8]int = [10, 20, 30, 40, 50, 60, 70, 80]
	Flip(mut a[0..V(8)], V(5))
	return a[0] *% 100 +% a[5]
}
`,
			wantBnd: 1,
		},
		{
			name: "meet-two-pointer-oob-traps-exact",
			src: pvV + `
Flip(p: mut []int, k: int) {
	var i := 0
	var j := k
	for i < j {
		t := p[i]
		p[i] = p[j]
		p[j] = t
		i += 1
		j -= 1
	}
}
H(): int {
	var a: [4]int = [1, 2, 3, 4]
	Flip(mut a[0..V(4)], V(4))
	return a[0]
}
`,
			wantBnd:   1,
			wantPanic: "panic 2",
		},
		{
			name: "meet-le-single-elem-ok",
			src: pvV + `
H(): int {
	var a: [5]int = [1, 2, 3, 4, 5]
	s := a[0..V(5)]
	var i := V(0)
	var j := V(4)
	var sum := 0
	for i <= j {
		sum = sum +% s[i] +% s[j]
		i += 1
		j -= 1
	}
	return sum
}
`,
			wantBnd: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			off := pcEval(t, tc.src, func(c *Config) { c.NoRanges = true })
			on, l := pvEval(t, tc.src, VersionOptions{})
			if on != off {
				t.Fatalf("eval mismatch:\n  on:  %q\n  off: %q", on, off)
			}
			if tc.wantPanic != "" && !strings.Contains(on, tc.wantPanic) {
				t.Fatalf("expected panic %q, got %q", tc.wantPanic, on)
			}
			if tc.wantBnd >= 0 && l.vBounds != tc.wantBnd {
				t.Errorf("versioned bounds loops = %d, want %d", l.vBounds, tc.wantBnd)
			}
		})
	}
}
