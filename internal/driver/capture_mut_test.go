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

    // 6. Same-arena Put + local List[&i32].New(a) beside a mut capture.
    a := sys.arena
    var arena_val: i32 = 0
    Each(nums[0..1], fn(x: &i32) {
        _ = x
        v := a.Put(i32(21)) else {
            return
        }
        var tmp := List[&i32].New(a)
        tmp.Push(&*v) else {
            return
        }
        arena_val = *tmp[0] * 2
    })

    try sys.Print(
        "sorted={a},{b},{c},{d},{e} cmps_ok={ok} sum={sum} out={o0},{o1},{o2} acc={acc} dbl={d0},{d1} nested={nested} order={before},{after} const={ct} arena={av}\n",
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
            av: arena_val,
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
	want := "sorted=1,2,3,4,5 cmps_ok=true sum=120 out=3,4,5 acc=15 dbl=4,2 nested=21 order=7,17 const=10 arena=42\n"
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
		{
			name: "Sort mut slice reborrow conflicts with mut capture of same slice",
			src:  helpers + "F(xs: mut []i32) {\n    xs.Sort(fn(a: &i32, b: &i32): bool {\n        xs[0] = 99\n        return *a < *b\n    })\n}\n",
			want: diag.E0501,
		},
		{
			name: "mut ref reborrow conflicts with mut capture of same ref",
			src:  helpers + "UseMut(r: mut i32, const f: fn()) {\n    *r = 10\n    f()\n}\n\nF(p: mut i32) {\n    UseMut(p, fn() {\n        *p += 100\n    })\n}\n",
			want: diag.E0501,
		},
		{
			name: "ArgReborrow conflicts with moved mut borrow of same ref",
			src:  helpers + "TwoMut(a: mut i32, b: mut i32) {\n    *a = 10\n    *b = 20\n}\n\nF(p: mut i32) {\n    TwoMut(p, mut *p)\n}\n",
			want: diag.E0501,
		},
		{
			name: "store shorter-lived capture loan into captured arena via Put",
			src:  helpers + "Call(const f: fn()) {\n    f()\n}\n\nF(a: &Arena) {\n    if true {\n        short: i32 = 42\n        Call(fn() {\n            _ = a.Put(&short) else {\n                return\n            }\n        })\n    }\n}\n",
			want: diag.E0609,
		},
		{
			name: "store inner sub-arena into outer captured arena",
			src:  helpers + "Call(const f: fn()) {\n    f()\n}\n\nStoreA(a1: &Arena, a2: &Arena into a1) {\n    _ = a1\n    _ = a2\n}\n\nF(a: &Arena) {\n    if true {\n        sub := Arena.Sub(a) else {\n            return\n        }\n        defer sub.Free()\n        Call(fn() {\n            StoreA(a, &sub)\n        })\n    }\n}\n",
			want: diag.E0609,
		},
		{
			name: "forwarding same const fn parameter twice in one call is E0705",
			src:  helpers + "Forward(const f: fn()) {\n    Two(f, f)\n}\n",
			want: diag.E0705,
		},
		{
			name: "store param through helper into captured var ref",
			src:  helpers + "Stash(dst: mut &i32, v: &i32 into dst) {\n    *dst = v\n}\n\nF() {\n    long: i32 = 1\n    var r: &i32 = &long\n    VisitRef(fn(x: &i32) {\n        Stash(mut r, x)\n    })\n    _ = r\n}\n",
			want: diag.E0609,
		},
		{
			name: "store inner capture loan into outer captured var ref",
			src:  helpers + "Call(const f: fn()) {\n    f()\n}\n\nF() {\n    long: i32 = 1\n    var r: &i32 = &long\n    if true {\n        short: i32 = 2\n        Call(fn() {\n            r = &short\n        })\n    }\n    _ = r\n}\n",
			want: diag.E0609,
		},
		{
			name: "store param into captured Cell",
			src:  helpers + "F() {\n    long: i32 = 1\n    c := Cell[&i32].New(&long)\n    VisitRef(fn(x: &i32) {\n        c.Store(x)\n    })\n    _ = c.Load()\n}\n",
			want: diag.E0609,
		},
		{
			name: "overwrite captured var linear local is E0402",
			src:  helpers + "Call(const f: fn()) {\n    f()\n}\n\nF() {\n    var tok := Token(id: 1)\n    Call(fn() {\n        tok = Token(id: 2)\n    })\n    tok.Close()\n}\n",
			want: diag.E0402,
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

// TestMutCaptureTaggedEval runs mutable captures, forwarding, recursion,
// reborrows of mut T and mut []T, a capturing literal inside a non-capturing
// literal, and plain self/iterator behind mut under eval's tagged
// borrow-tracking mode to prove zero aliasing or tag violations.
func TestMutCaptureTaggedEval(t *testing.T) {
	src := `Each(xs: []i32, const f: fn(&i32)) {
    for x in xs {
        f(&x)
    }
}

Forward(xs: []i32, const f: fn(&i32)) {
    Each(xs, f)
    Each(xs, f)
}

Rec(n: int, const f: fn()) {
    if n > 0 {
        f()
        Rec(n - 1, f)
    }
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
}

struct Pt {
    x: i32
}

Pt.Get(self): i32 {
    return self.x
}

struct It {
    n: i32
}

It.Next(mut self): ?i32 {
    if self.n <= 0 {
        return none
    }
    self.n -= 1
    return some(self.n)
}

Run(): i32 {
    var xs: [3]i32 = [1, 2, 3]
    var sum: i32 = 0
    Forward(xs[..], fn(x: &i32) {
        sum += *x
    })
    // sum = 12
    Rec(4, fn() {
        sum += 1
    })
    // sum = 16
    g := fn(ys: []i32): i32 {
        var acc: i32 = 0
        Each(ys, fn(x: &i32) {
            acc += *x
        })
        return acc
    }
    sum += g(xs[..])
    // sum = 22
    var pt := Pt(x: 5)
    p := mut pt
    var it := It(n: 3)
    mit := mut it
    Each(xs[0..1], fn(x: &i32) {
        _ = x
        sum += p.Get()
        for v in mit {
            sum += v
        }
    })
    // sum = 22 + 5 + (2 + 1 + 0) = 30
    var acc: i32 = 0
    var buf: [3]i32 = [0; 3]
    ReborrowMut(xs[..], mut acc, mut buf[..])
    // acc = 6, buf = [2, 4, 6] -> acc + buf[2] = 12 -> total = 42
    sum += acc + buf[2]
    return sum
}
`
	c := soundSession(t)
	if o := soundCheck(t, c, "sound/mutcap", src); len(o.Report.Diags) != 0 {
		t.Fatalf("diagnostics:\n%s", o.Report.Text(o.Sources))
	}
	res := c.RunUnit(RunSpec{Unit: "sound/mutcap", Fn: "Run", Tag: &TagOptions{Seed: 11}})
	if res.Err != nil || res.Skipped != "" || len(res.Violations) != 0 || res.Value != 42 {
		t.Fatalf("tagged run: %+v", res)
	}
}
