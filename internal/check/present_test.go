// Why: dl.Present(io, f) answers only for a bodiless extern function named
// directly and refuses compile-time const evaluation cleanly (RefuseExtern),
// while deferring contracts (evaluateContract) at compile time.
//
// What: TestPresent_ArgRule and TestPresent_EvalRefusal.
//
// How: checks std.dl.Present and std.sys.LazyPresent with invalid arguments
// (E0705 present-argument) and tests both EvalConst refusal (E0901) and
// contract deferral via fakeEval.

package check

import (
	"testing"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

func TestPresent_ArgRule(t *testing.T) {
	t.Parallel()
	const sysSrc = "export struct Io { x: i32 }\nexport LazyPresent[F](io: Io, f: F): bool {\n\treturn false\n}\n"
	const dlSrc = "import std.sys\nexport Present[F](io: sys.Io, f: F): bool {\n\treturn sys.LazyPresent(io, f)\n}\n"

	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "bodiless extern valid outside unsafe",
			src:  "import std.dl\nimport std.sys\nextern Ext(): i32\nexport F(io: sys.Io): bool {\n\treturn dl.Present(io, Ext)\n}\n",
			want: "",
		},
		{
			name: "local holding extern rejected",
			src:  "import std.dl\nimport std.sys\nextern Ext(): i32\nexport F(io: sys.Io): bool {\n\tunsafe \"ok\" {\n\t\tg := Ext\n\t\treturn dl.Present(io, g)\n\t}\n}\n",
			want: "E0705@7:25",
		},
		{
			name: "defined function rejected",
			src:  "import std.dl\nimport std.sys\nBody(): i32 { return 1 }\nexport F(io: sys.Io): bool {\n\treturn dl.Present(io, Body)\n}\n",
			want: "E0705@5:24",
		},
		{
			name: "export extern with body rejected",
			src:  "import std.dl\nimport std.sys\nexport extern WithBody(): i32 { return 1 }\nexport F(io: sys.Io): bool {\n\treturn dl.Present(io, WithBody)\n}\n",
			want: "E0705@5:24",
		},
		{
			name: "call expression rejected",
			src:  "import std.dl\nimport std.sys\nextern Ext(): i32\nPick(): fn(): i32 { unsafe \"ok\" { return Ext } }\nexport F(io: sys.Io): bool {\n\treturn dl.Present(io, Pick())\n}\n",
			want: "E0705@6:24",
		},
		{
			name: "Present named as value rejected",
			src:  "import std.dl\nimport std.sys\nexport F() {\n\tg := dl.Present[fn(): i32]\n\t_ = g\n}\n",
			want: "E0705@4:7",
		},
		{
			name: "LazyPresent non-extern rejected",
			src:  "import std.sys\nBody(): i32 { return 1 }\nexport F(io: sys.Io): bool {\n\treturn sys.LazyPresent(io, Body)\n}\n",
			want: "E0705@4:29",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			w.add("std.sys", opt{std: true}, sysSrc).wantClean(t)
			w.add("std.dl", opt{std: true}, dlSrc).wantClean(t)
			b := checkBodyIn(t, w, opt{unsafe: true}, tc.want, tc.src)
			if tc.want != "" {
				ds := b.bodyDiags(t)
				if len(ds) != 1 || fact(ds[0], "reason") != "present-argument" {
					t.Fatalf("want reason=present-argument, got:\n%s", dump(w, ds))
				}
			}
		})
	}
}

func TestPresent_EvalRefusal(t *testing.T) {
	t.Parallel()
	const sysSrc = "export struct Io { x: i32 }\nexport LazyPresent[F](io: Io, f: F): bool {\n\treturn false\n}\n"
	const dlSrc = "import std.sys\nexport Present[F](io: sys.Io, f: F): bool {\n\treturn sys.LazyPresent(io, f)\n}\n"

	w := newWorld(t, true)
	w.add("std.sys", opt{std: true}, sysSrc).wantClean(t)
	w.add("std.dl", opt{std: true}, dlSrc).wantClean(t)
	fe := w.withEval()
	fe.fn = func(req *types.EvalRequest) (types.EvalResult, bool) {
		return types.EvalResult{Err: &types.EvalError{
			Kind:    types.EvalRefused,
			Refusal: types.RefuseExtern,
			Items:   []types.ItemID{req.Item},
			Trace:   []source.Span{{}},
		}}, true
	}

	// Contract using dl.Present refuses with RefuseExtern and defers cleanly.
	contractSrc := `import std.dl
import std.sys

extern Ext(): i32
Helper(io: sys.Io): bool { return dl.Present(io, Ext) }
Guarded(io: sys.Io) requires Helper(io) { }
export Caller(io: sys.Io) { Guarded(io) }
`
	_ = checkBodyIn(t, w, opt{unsafe: true}, "", contractSrc)

	// Const initializer refusing with RefuseExtern reports E0901 cleanly.
	constSrc := `Ask(): bool { return false }
const Bad: bool = Ask()
export Read(): bool { return Bad }
`
	b2 := w.add("constmod", opt{}, constSrc)
	b2.wantClean(t)
	if ds := b2.bodyDiags(t); len(ds) != 0 {
		t.Fatalf("unexpected body diags: %s", dump(w, ds))
	}
	v, ds := w.c.EvalConst(b2.m, b2.item("Bad"))
	if v != types.NoConst || len(ds) != 1 || ds[0].Code != diag.E0901 || fact(ds[0], "refused") != "extern" {
		t.Fatalf("want E0901 refused=extern, got v=%v ds=%s", v, dump(w, ds))
	}
}
