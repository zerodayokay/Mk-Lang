// Why: versionNestedBounds (rangenest.go) hoists bounds and overflow checks
// out of 2D counted-loop nests on a non-trapping entry guard; a wrong interval
// or missing overflow test would silently drop a trap or read out of bounds.
//
// What: TestNestVersion_Agrees, verifying eval agreement (versions on vs off),
// MIR validation, and versioning counts across safe 2D kernels and trap edges.

package lower

import (
	"strings"
	"testing"
)

var nestEdge = []struct {
	name   string
	src    string
	bounds int
	panics bool
}{
	{
		name: "mat-3d-safe",
		src: pvV + `H(): int {
	var a: [16]int = [2; 16]
	var b: [16]int = [3; 16]
	var c: [16]int = [0; 16]
	sa := a[0..V(16)]
	sb := b[0..V(16)]
	sc := mut c[0..V(16)]
	n := V(4)
	for i in 0..n {
		for k in 0..n {
			aik := sa[i * n + k]
			for j in 0..n {
				sc[i * n + j] = sc[i * n + j] +% aik *% sb[k * n + j]
			}
		}
	}
	var sum := 0
	for i in 0..16 {
		sum = sum +% c[i]
	}
	return sum
}
`,
		bounds: 1,
	},
	{
		name: "mat-2d-oob-outer",
		src: pvV + `H(): int {
	var a: [3]int = [1; 3]
	var b: [16]int = [2; 16]
	sa := a[0..V(3)]
	sb := b[0..V(16)]
	n := V(4)
	var r := 0
	for k in 0..n {
		v := sa[k]
		for j in 0..n {
			r = r +% v +% sb[k * n + j]
		}
	}
	return r
}
`,
		bounds: 1,
		panics: true,
	},
	{
		name: "mat-2d-oob-inner",
		src: pvV + `H(): int {
	var a: [4]int = [1; 4]
	var b: [15]int = [2; 15]
	sa := a[0..V(4)]
	sb := b[0..V(15)]
	n := V(4)
	var r := 0
	for k in 0..n {
		v := sa[k]
		for j in 0..n {
			r = r +% v +% sb[k * n + j]
		}
	}
	return r
}
`,
		bounds: 1,
		panics: true,
	},
	{
		name: "mat-2d-mul-ovf",
		src: pvV + `H(): int {
	var b: [16]int = [2; 16]
	sb := b[0..V(16)]
	stride := V(3074457345618258603)
	nk := V(4)
	nj := V(2)
	var r := 0
	for k in 0..nk {
		for j in 0..nj {
			r = r +% sb[k * stride + j]
		}
	}
	return r
}
`,
		bounds: 1,
		panics: true,
	},
	{
		name: "mat-2d-inv-mul-ovf",
		src: pvV + `H(): int {
	var a: [8]int = [1; 8]
	sa := a[0..V(8)]
	x := V(3074457345618258603)
	y := V(4)
	n := V(2)
	var r := 0
	for k in 0..n {
		for j in 0..n {
			r = r +% sa[x * y + j]
		}
	}
	return r
}
`,
		bounds: 1,
		panics: true,
	},
	{
		name: "mat-2d-mul-big-safe-fallback",
		src: pvV + `H(): int {
	var b: [4]int = [5; 4]
	sb := b[0..V(4)]
	stride := V(3000000000)
	nk := V(1)
	nj := V(3)
	var r := 0
	for k in 0..nk {
		for j in 0..nj {
			r = r +% sb[k * stride + j]
		}
	}
	return r
}
`,
		bounds: 1,
	},
	{
		name: "mat-2d-zero-outer",
		src: pvV + `H(): int {
	var b: [2]int = [7; 2]
	sb := b[0..V(2)]
	nk := V(0)
	n := V(10)
	var r := 0
	for k in 0..nk {
		for j in 0..n {
			r = r +% sb[k * n + j]
		}
	}
	return r
}
`,
		bounds: 1,
	},
	{
		name: "mat-2d-zero-inner",
		src: pvV + `H(): int {
	var a: [4]int = [3; 4]
	var b: [1]int = [7; 1]
	sa := a[0..V(4)]
	sb := b[0..V(1)]
	nk := V(4)
	nj := V(0)
	var r := 0
	for k in 0..nk {
		r = r +% sa[k]
		for j in 0..nj {
			r = r +% sb[k * 100 + j]
		}
	}
	return r
}
`,
		bounds: 1,
	},
	{
		name: "mat-2d-neg-start",
		src: pvV + `H(): int {
	var b: [16]int = [1; 16]
	sb := b[0..V(16)]
	k0 := V(-2)
	nk := V(3)
	nj := V(4)
	var r := 0
	for k in k0..nk {
		for j in 0..nj {
			r = r +% sb[k * 4 + j]
		}
	}
	return r
}
`,
		bounds: 1,
		panics: true,
	},
	{
		name: "mat-2d-neg-offset-safe",
		src: pvV + `H(): int {
	var b: [9]int = [4; 9]
	sb := b[0..V(9)]
	lo := V(1)
	hi := V(4)
	var r := 0
	for k in lo..hi {
		for j in lo..hi {
			r = r +% sb[(k - 1) * 3 + (j - 1)]
		}
	}
	return r
}
`,
		bounds: 1,
	},
	{
		name: "mat-2d-neg-offset-oob",
		src: pvV + `H(): int {
	var b: [9]int = [4; 9]
	sb := b[0..V(9)]
	k0 := V(0)
	nk := V(4)
	nj := V(3)
	var r := 0
	for k in k0..nk {
		for j in 0..nj {
			r = r +% sb[(k - 1) * 3 + j]
		}
	}
	return r
}
`,
		bounds: 1,
		panics: true,
	},
	{
		name: "mat-2d-sub-ovf",
		src: pvV + `H(): int {
	var b: [8]int = [1; 8]
	sb := b[0..V(8)]
	lo := V(` + pvMin + `)
	nj := V(2)
	var r := 0
	for k in lo..lo + 2 {
		for j in 0..nj {
			r = r +% sb[(k - 1) + j]
		}
	}
	return r
}
`,
		bounds: 1,
		panics: true,
	},
	{
		name: "mat-2d-slice-reassigned",
		src: pvV + `H(): int {
	var a: [16]int = [1; 16]
	var s := a[0..V(16)]
	t := a[0..V(2)]
	n := V(4)
	var r := 0
	for k in 0..n {
		for j in 0..n {
			r = r +% s[k * n + j]
		}
		s = t
	}
	return r
}
`,
		bounds: 1, // nest rejects (s reassigned in outer), inner 1D versions j
		panics: true,
	},
	{
		name: "mat-2d-cond-inner-ovf",
		src: pvV + `H(): int {
	var a: [4]int = [2; 4]
	sa := a[0..V(4)]
	big := V(9223372036854775807)
	cond := V(0)
	nk := V(4)
	var r := 0
	for k in 0..nk {
		r = r +% sa[k]
		if cond != 0 {
			for j in 0..big + 1 {
				r = r +% sa[j]
			}
		}
	}
	return r
}
`,
		bounds: 1,
	},
	{
		name: "mat-2d-two-inners-transpose",
		src: pvV + `H(): int {
	var src: [12]int = [0; 12]
	var dst: [12]int = [0; 12]
	for i in 0..12 {
		src[i] = i + 1
	}
	ss := src[0..V(12)]
	sd := mut dst[0..V(12)]
	rows := V(3)
	cols := V(4)
	var r := 0
	for k in 0..rows {
		for j in 0..cols {
			sd[j * rows + k] = ss[k * cols + j]
		}
		for j2 in 0..cols {
			r = r +% ss[k * cols + j2]
		}
	}
	return r +% dst[1] +% dst[11]
}
`,
		bounds: 1,
	},
}

func TestNestVersion_Agrees(t *testing.T) {
	for _, tc := range nestEdge {
		t.Run(tc.name, func(t *testing.T) {
			off := pcEval(t, tc.src, nil)
			on, l := pvEval(t, tc.src, VersionOptions{})
			if on != off {
				t.Fatalf("versions on gave %q, off gave %q", on, off)
			}
			if l.vBounds != tc.bounds {
				t.Errorf("versioned %d bounds loops, want %d", l.vBounds, tc.bounds)
			}
			if tc.panics && !strings.Contains(on, " panic ") {
				t.Errorf("expected panic, got %q", on)
			}
			if !tc.panics && strings.Contains(on, " panic ") {
				t.Errorf("unexpected panic: %q", on)
			}
			if got, _ := pvEval(t, tc.src, VersionOptions{NoReduce: true}); got != off {
				t.Errorf("NoReduce gave %q, off gave %q", got, off)
			}
		})
	}
}
