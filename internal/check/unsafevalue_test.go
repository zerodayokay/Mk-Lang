// Why: a call through a fn value is a call the unsafe call rule never sees,
// so naming an unsafe-only function as a value outside `unsafe` must be
// refused too.
// What: TestUnsafeOnlyNamedAsValue (str.FromC, std.raw, std.mmio,
// fs.Map), TestBodilessExternNamedAsValue and
// TestQualifiedExternNamedAsValue.
// How: checks each name as a value outside and inside `unsafe` (E1101 at the
// name outside, clean inside) against a stub of std.raw.

package check

import (
	"os"
	"testing"
)

// rawStub is the std.raw surface the cases need: ReadUnaligned and
// WriteUnaligned are unsafe, Ctz64 and the carry intrinsics (AddCarry64,
// SubBorrow64) are safe.
const rawStub = "export ReadUnaligned[T: Plain](p: *T): T\n\nexport WriteUnaligned[T: Plain](p: *T, x: T)\n\nexport Ctz64(x: u64): int\n\n" +
	"export AddCarry64(x: u64, y: u64, c: u64): (sum: u64, carry: u64)\n\nexport SubBorrow64(x: u64, y: u64, b: u64): (diff: u64, borrow: u64)\n"

func TestUnsafeOnlyNamedAsValue(t *testing.T) {
	t.Run("str.FromC", func(t *testing.T) {
		runM4Cases(t, []bodyCase{
			{"named outside unsafe", "F(p: *u8): str {\n\tg := str.FromC\n\treturn g(p)\n}\n", "E1101@2:7"},
			{"named in unsafe", "F(p: *u8): str {\n\tunsafe \"p is a C string\" {\n\t\tg := str.FromC\n\t\treturn g(p)\n\t}\n}\n", ""},
		})
	})
	for _, tc := range []struct{ name, src, want, op string }{
		{"raw unsafe named", "F(p: *u32): u32 {\n\tg: fn(p: *u32): u32 from p = raw.ReadUnaligned[u32]\n\tunsafe \"p is valid\" {\n\t\treturn g(p)\n\t}\n}\n", "E1101@4:31", "raw.ReadUnaligned"},
		{"raw unsafe named in unsafe", "F(p: *u32): u32 {\n\tunsafe \"p is valid\" {\n\t\tg: fn(p: *u32): u32 from p = raw.ReadUnaligned[u32]\n\t\treturn g(p)\n\t}\n}\n", "", ""},
		{"raw unsafe as an argument", "Apply(m: fn(p: *u32): u32 from p, p: *u32): u32 {\n\treturn m(p)\n}\n\nF(p: *u32): u32 {\n\treturn Apply(raw.ReadUnaligned[u32], p)\n}\n", "E1101@8:15", "raw.ReadUnaligned"},
		{"raw unsafe store named", "F(p: *u32) {\n\tg: fn(p: *u32, x: u32) = raw.WriteUnaligned[u32]\n\tunsafe \"p is valid\" {\n\t\tg(p, 1)\n\t}\n}\n", "E1101@4:27", "raw.WriteUnaligned"},
		{"raw unsafe store named in unsafe", "F(p: *u32) {\n\tunsafe \"p is valid\" {\n\t\tg: fn(p: *u32, x: u32) = raw.WriteUnaligned[u32]\n\t\tg(p, 1)\n\t}\n}\n", "", ""},
		{"raw safe named", "F(x: u64): int {\n\tg := raw.Ctz64\n\treturn g(x)\n}\n", "", ""},
		// The carry intrinsics are safe, called or named.
		{"carry called", "F(x: u64, y: u64): u64 {\n\ts := raw.AddCarry64(x, y, 0)\n\td := raw.SubBorrow64(x, y, s.carry)\n\treturn d.diff +% d.borrow\n}\n", "", ""},
		{"carry named", "F(x: u64): u64 {\n\tg := raw.AddCarry64\n\treturn g(x, x, 1).sum\n}\n", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			w.add("std.raw", opt{std: true}, rawStub).wantClean(t)
			b := checkBody(t, w, tc.want, "import std.raw\n\n"+tc.src)
			for _, d := range b.bodyDiags(t) {
				if fact(d, "operation") != tc.op {
					t.Errorf("%s operation %q, want %q", d.Code, fact(d, "operation"), tc.op)
				}
			}
		})
	}
	mmio, err := os.ReadFile("../../std/mmio/mmio.mk")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []bodyCase{
		{"mmio named", "F(p: *u32): u32 {\n\tg: fn(p: *u32): u32 = mmio.Load[u32]\n\tunsafe \"p is mapped\" {\n\t\treturn g(p)\n\t}\n}\n", "E1101@4:24"},
		{"mmio method named", "F(r: &mmio.Reg[u32]): u32 {\n\tg := mmio.Reg[u32].Read\n\tunsafe \"r is mapped\" {\n\t\treturn g(r)\n\t}\n}\n", "E1101@4:7"},
		// A method of a value is no value at all (E0609), so a bound
		// receiver cannot carry an unsafe-only call out of unsafe.
		{"mmio method of a value", "F(r: &mmio.Reg[u32]): u32 {\n\tg := r.Read\n\tunsafe \"r is mapped\" {\n\t\treturn g(r)\n\t}\n}\n", "E0609@4:9"},
		{"mmio named in unsafe", "F(p: *u32): u32 {\n\tunsafe \"p is mapped\" {\n\t\tg: fn(p: *u32): u32 = mmio.Load[u32]\n\t\treturn g(p)\n\t}\n}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true)
			w.add("std.mmio", opt{std: true}, string(mmio)).wantClean(t)
			checkBody(t, w, tc.want, "import std.mmio\n\n"+tc.src)
		})
	}
}

// TestBodilessExternNamedAsValue: a bodiless extern named as a value
// outside unsafe is E1101 at the name, since a call through the value runs
// C code no call rule sees; inside unsafe it checks clean,
// and an extern with a body is a plain Mk function, named freely.
func TestBodilessExternNamedAsValue(t *testing.T) {
	const ext = "extern Labs(x: i64): i64\n\nexport extern Twice(n: i64): i64 {\n\treturn n * 2\n}\n\n"
	runBodyCases(t, []bodyCase{
		{"named outside unsafe", ext + "F(v: i64): i64 {\n\tg := Labs\n\treturn g(v)\n}\n", "E1101@8:7"},
		{"as an argument", ext + "Apply(f: fn(x: i64): i64, v: i64): i64 {\n\treturn f(v)\n}\n\nF(v: i64): i64 {\n\treturn Apply(Labs, v)\n}\n", "E1101@12:15"},
		{"named in unsafe", ext + "F(v: i64): i64 {\n\tunsafe \"labs is total\" {\n\t\tg := Labs\n\t\treturn g(v)\n\t}\n}\n", ""},
		{"extern with a body", ext + "F(v: i64): i64 {\n\tg := Twice\n\treturn g(v)\n}\n", ""},
	})
}

// TestQualifiedExternNamedAsValue: a bodiless extern of another
// module, named by its qualified name as a value outside unsafe, is E1101
// at the name too.
func TestQualifiedExternNamedAsValue(t *testing.T) {
	for _, tc := range []bodyCase{
		{"qualified outside unsafe", "F(v: i64): i64 {\n\tg := cx.Labs\n\treturn g(v)\n}\n", "E1101@4:7"},
		{"qualified in unsafe", "F(v: i64): i64 {\n\tunsafe \"labs is total\" {\n\t\tg := cx.Labs\n\t\treturn g(v)\n\t}\n}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			w.add("std.cx", opt{std: true}, "export extern Labs(x: i64): i64\n").wantClean(t)
			checkBody(t, w, tc.want, "import std.cx\n\n"+tc.src)
		})
	}
}

func TestUnixVulkanSurfaceRequiresUnsafe(t *testing.T) {
	const windowStub = `export error Unsupported

export CreateUnixVulkanSurface(io: Io, handle: uint, instance: uint, loader: uint): Unsupported!(code: i32, surface: uint) {
	fail Unsupported
}
`
	const head = "import std.window\n\nF(io: Io, handle: uint, instance: uint, loader: uint) {\n"
	const call = "_ = window.CreateUnixVulkanSurface(io, handle, instance, loader) else {}\n"
	for _, tc := range []bodyCase{
		{"direct call", "\t" + call, "E1101@4:6"},
		{"function value", "\t_ = window.CreateUnixVulkanSurface\n", "E1101@4:6"},
		{"unsafe call and name", "\tunsafe \"the caller keeps the window and instance live\" {\n\t\t" + call +
			"\t\t_ = window.CreateUnixVulkanSurface\n\t}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true)
			w.add("std.window", opt{std: true}, windowStub).wantClean(t)
			b := checkBody(t, w, tc.want, head+tc.src+"}\n")
			for _, d := range b.bodyDiags(t) {
				if got := fact(d, "operation"); got != "window.CreateUnixVulkanSurface" {
					t.Errorf("operation %q, want window.CreateUnixVulkanSurface", got)
				}
			}
		})
	}
}
