// Why: mutable captures on non-escaping const fn literals span check, lower,
// borrow, mono, eval and codegen; only end-to-end builds, native execution
// and full-pipeline rejection checks prove every stage agrees.
// What: TestMutCaptureNativeRun (native execution + compile-time const eval)
// and TestMutCaptureRejectEndToEnd (E0508, E0501, E0505, E0507, E0609).
// How: builds and runs a temporary program with embedded std through
// buildProgram/runExe, and checks reject programs through NewSession.

package driver

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"mk/internal/diag"
	"mk/internal/types"
)

func TestMutCaptureNativeRun(t *testing.T) {
	src := `Each(xs: []i32, const visit: fn(&i32)) {
    for x in xs {
        visit(&x)
    }
}

EachErr(xs: []i32, const visit: fn(&i32): OutOfMemory!void): OutOfMemory!void {
    for x in xs {
        try visit(&x)
    }
}

After(const f: fn(), x: int): int {
    f()
    return x
}

ReborrowMut(xs: []i32, dst: mut i32, buf: mut []i32) {
    var idx := 0
    Each(xs, fn(x: &i32) {
        *dst += *x
        if idx < buf.Len() {
            buf[idx] = *x * 2
        }
        idx += 1
    })
    if buf.Len() >= 2 {
        Each(xs[0..1], fn(v: &i32) {
            _ = v
            buf.Swap(0, 1)
        })
    }
}

ConstAcc(): i32 {
    var vals: [4]i32 = [3, 1, 4, 2]
    var sum: i32 = 0
    Each(vals[..], fn(x: &i32) {
        sum += *x
    })
    return sum
}

const ConstTotal: i32 = ConstAcc()

export Main(sys: &System): OutOfMemory|WriteError!void {
    // 1. Counting comparisons in Sort.
    var nums: [5]i32 = [5, 2, 4, 1, 3]
    var cmps := 0
    nums.Sort(fn(a: &i32, b: &i32): bool {
        cmps += 1
        return *a < *b
    })

    // 2. Accumulating into var sum and var List[int] with mixed shared + mut captures.
    threshold: i32 = 2
    scale: i32 = 10
    var sum := 0
    var out := List[int].New(sys.arena)
    try EachErr(nums[..], fn(x: &i32): OutOfMemory!void {
        if *x > threshold {
            sum += int(*x) * int(scale)
            try out.Push(int(*x))
        }
    })

    // 3. Reborrowing mut T and mut []T captures.
    var acc: i32 = 0
    var doubled: [5]i32 = [0; 5]
    ReborrowMut(nums[..], mut acc, mut doubled[..])

    // 4. Nested const fn mutating an outer var.
    var nested := 0
    Each(nums[0..3], fn(a: &i32) {
        Each(nums[0..2], fn(b: &i32) {
            nested += int(*a) + int(*b)
        })
    })

    // 5. Evaluation order: plain argument read before pass-3 mut capture runs.
    var order_x := 7
    before := After(fn() { order_x += 10 }, order_x)

    try sys.Print(
        "sorted={a},{b},{c},{d},{e} cmps_ok={ok} sum={sum} out={o0},{o1},{o2} acc={acc} dbl={d0},{d1} nested={nested} order={before},{after} const={ct}\n",
        (
            a: nums[0], b: nums[1], c: nums[2], d: nums[3], e: nums[4],
            ok: cmps > 0,
            sum: sum,
            o0: out[0], o1: out[1], o2: out[2],
            acc: acc,
            d0: doubled[0], d1: doubled[1],
            nested: nested,
            before: before, after: order_x,
            ct: ConstTotal,
        ),
    )
}
`
	dir := t.TempDir()
	file := filepath.Join(dir, "mutcap.mk")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	target := types.X86_64Windows
	if runtime.GOOS == "linux" {
		target = types.X86_64Linux
	}
	want := "sorted=1,2,3,4,5 cmps_ok=true sum=120 out=3,4,5 acc=15 dbl=4,2 nested=21 order=7,17 const=10\n"
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		t.Run(prof.String(), func(t *testing.T) {
			exe := buildProgram(t, file, target, prof, t.TempDir())
			stdout, stderr, code := runExe(t, target, exe, "")
			if code != 0 || stderr != "" {
				t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
			}
			if stdout != want {
				t.Fatalf("stdout = %q, want %q", stdout, want)
			}
		})
	}
}

func TestMutCaptureRejectEndToEnd(t *testing.T) {
	helpers := `Each(xs: []i32, const f: fn(&i32)) {
    for x in xs {
        f(&x)
    }
}

VisitStr(const f: fn(str)) {
    var buf: [2]u8 = [97, 98]
    s := str.From(buf[..]) else {
        return
    }
    f(s)
}

VisitRef(const f: fn(&i32)) {
    x: i32 = 1
    f(&x)
}

Two(const f: fn(), const g: fn()) {
    f()
    g()
}

linear struct Token {
    id: int
}

Token.Close(self) {
}

`
	cases := []struct {
		name string
		src  string
		want diag.Code
	}{
		{
			name: "immutable local mutated in capture",
			src:  helpers + "F(xs: []i32) {\n    sum := 0\n    Each(xs, fn(x: &i32) {\n        sum += int(*x)\n    })\n    _ = sum\n}\n",
			want: diag.E0508,
		},
		{
			name: "conflicting borrow with Sort receiver",
			src:  helpers + "F(a: &Arena) {\n    var xs := List[i32].New(a)\n    xs.Sort(fn(x: &i32, y: &i32): bool {\n        xs.Push(1) else {}\n        return *x < *y\n    })\n}\n",
			want: diag.E0501,
		},
		{
			name: "two const fn args mutating same local",
			src:  helpers + "F() {\n    var n := 0\n    Two(fn() { n += 1 }, fn() { n += 2 })\n    _ = n\n}\n",
			want: diag.E0501,
		},
		{
			name: "shared capture and mut capture of same local",
			src:  helpers + "F() {\n    var n := 0\n    Two(fn() { _ = n }, fn() { n += 1 })\n    _ = n\n}\n",
			want: diag.E0501,
		},
		{
			name: "move var linear capture",
			src:  helpers + "F(xs: []i32) {\n    var tok := Token(id: 1)\n    Each(xs, fn(x: &i32) {\n        _ = x\n        tok.Close()\n    })\n    tok.Close()\n}\n",
			want: diag.E0609,
		},
		{
			name: "store param ref loan into captured option",
			src:  helpers + "F() {\n    var best: ?&i32 = none\n    VisitRef(fn(p: &i32) {\n        best = some(p)\n    })\n    _ = best\n}\n",
			want: diag.E0609,
		},
		{
			name: "store param str loan into captured List[str]",
			src:  helpers + "F(a: &Arena) {\n    var out := List[str].New(a)\n    VisitStr(fn(s: str) {\n        out.Push(s) else {}\n    })\n    _ = out.Len()\n}\n",
			want: diag.E0609,
		},
		{
			name: "store shorter-lived capture loan into outer captured List[str]",
			src:  helpers + "F(a: &Arena, xs: []i32) {\n    var out := List[str].New(a)\n    if xs.Len() > 0 {\n        var buf: [2]u8 = [97, 98]\n        s := str.From(buf[..]) else {\n            return\n        }\n        Each(xs, fn(x: &i32) {\n            _ = x\n            out.Push(s) else {}\n        })\n    }\n    _ = out.Len()\n}\n",
			want: diag.E0609,
		},
		{
			name: "store literal-local loan into captured option",
			src:  helpers + "F(xs: []i32) {\n    var best: ?&i32 = none\n    Each(xs, fn(x: &i32) {\n        _ = x\n        loc: i32 = 5\n        best = some(&loc)\n    })\n    _ = best\n}\n",
			want: diag.E0505,
		},
		{
			name: "write through captured shared ref is E0507",
			src:  helpers + "F(xs: []i32, p: &i32) {\n    Each(xs, fn(x: &i32) {\n        _ = x\n        *p = 1\n    })\n}\n",
			want: diag.E0507,
		},
		{
			name: "write through captured shared slice is E0507",
			src:  helpers + "F(xs: []i32, ys: []i32) {\n    Each(xs, fn(x: &i32) {\n        _ = x\n        ys[0] = 1\n    })\n}\n",
			want: diag.E0507,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "reject.mk")
			if err := os.WriteFile(file, []byte(tc.src), 0o644); err != nil {
				t.Fatal(err)
			}
			r := check(t, Config{Root: file})
			found := false
			for _, e := range r.Report.Diags {
				if e.Diag.Code == tc.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %v, got:\n%s", tc.want, r.Report.Text(r.Sources))
			}
		})
	}
}
