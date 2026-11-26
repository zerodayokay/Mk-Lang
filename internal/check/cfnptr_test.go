// Why: C-import COM-style vtable structs emit methods whose vtable pointer
// safe code could forge (e.g. mem.Zeroed), so calling or naming them outside
// unsafe must be E1101, decided from the struct's type alone; and
// raw.FnFromAddr must accept ?fn types and name F in E0701.
//
// What: TestE1101_ComVtableMethod and TestE0701_FnFromAddrOptional.
//
// How: checks C-import modules with COM-shaped and non-COM structs inside and
// outside unsafe, and checks raw.FnFromAddr with fn, ?fn and non-fn types.

package check

import (
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/types"
)

func TestE1101_ComVtableMethod(t *testing.T) {
	t.Parallel()
	const cSrc = `export struct Vtbl {
    export ping: ?fn(*Obj): i32
    export pong: ?fn(*Obj, i32)
}

export struct Obj {
    export lp_vtbl: *Vtbl
}

// Decided from the struct's type alone: empty body still needs unsafe.
export Obj.Ping(&self): i32 {
    return 0
}

export Obj.Pong(&self, x: i32) {
}

export struct PlainVtbl {
    export f: ?fn(i32): i32
}

export struct NonCom {
    export p: *PlainVtbl
}

export NonCom.Safe(&self): i32 {
    return 1
}
`
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "method call outside unsafe",
			src:  "import cmod\nimport std.raw\n\nF(): i32 {\n\td := cmod.Obj(lp_vtbl: raw.Null[cmod.Vtbl]())\n\treturn d.Ping()\n}\n",
			want: "E1101@6:9",
		},
		{
			name: "static method call outside unsafe",
			src:  "import cmod\nimport std.raw\n\nF() {\n\td := cmod.Obj(lp_vtbl: raw.Null[cmod.Vtbl]())\n\tcmod.Obj.Pong(&d, 1)\n}\n",
			want: "E1101@6:2",
		},
		{
			name: "method named as value outside unsafe",
			src:  "import cmod\n\nF() {\n\tg := cmod.Obj.Pong\n\t_ = g\n}\n",
			want: "E1101@4:7",
		},
		{
			name: "method call inside unsafe allowed",
			src:  "import cmod\nimport std.raw\n\nF(): i32 {\n\td := cmod.Obj(lp_vtbl: raw.Null[cmod.Vtbl]())\n\tunsafe \"d is valid\" {\n\t\treturn d.Ping()\n\t}\n}\n",
			want: "",
		},
		{
			name: "non-COM C-import struct method allowed outside unsafe",
			src:  "import cmod\nimport std.raw\n\nF(): i32 {\n\tx := cmod.NonCom(p: raw.Null[cmod.PlainVtbl]())\n\treturn x.Safe()\n}\n",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			w.add("std.raw", opt{std: true}, "export Null[T](): *T\n").wantClean(t)
			cm := w.add("cmod", opt{unsafe: true}, cSrc)
			cm.wantClean(t)
			w.cfg.CImport = func(id types.ModuleID) bool { return id == cm.id }
			checkBodyIn(t, w, opt{unsafe: true}, tc.want, tc.src)
		})
	}
}

func TestE0701_FnFromAddrOptional(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.add("std.raw", opt{std: true}, "export FnFromAddr[F](addr: uint): F\n").wantClean(t)
	okSrc := `import std.raw

type OptF = ?fn(i32): i32

F() {
    unsafe "test" {
        a := raw.FnFromAddr[fn(i32): i32](0)
        b := raw.FnFromAddr[OptF](0)
        _ = a
        _ = b
    }
}
`
	b := w.add("ok", opt{unsafe: true}, okSrc)
	b.wantClean(t)
	if ds := b.bodyDiags(t); len(ds) != 0 {
		t.Fatalf("unexpected diagnostics: %s", dump(w, ds))
	}

	badSrc := `import std.raw

F() {
    unsafe "test" {
        _ = raw.FnFromAddr[int](0)
    }
}
`
	b2 := w.add("bad", opt{unsafe: true}, badSrc)
	b2.wantClean(t)
	ds := b2.bodyDiags(t)
	if len(ds) != 1 || ds[0].Code != diag.E0701 || !strings.Contains(ds[0].Message, "`F`") {
		t.Fatalf("want E0701 naming `F`, got:\n%s", dump(w, ds))
	}
}
