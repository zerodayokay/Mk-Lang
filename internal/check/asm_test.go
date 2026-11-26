// Why: inline asm, naked functions, align(n), the section clause and extern
// var each have type and safety rules check enforces.
// What: tests of asm operands, options, holes, sym and const, naked
// bodies, align layout, section and extern var tests, each accepting and
// rejecting.
// How: checks small function bodies through simdCases and the harness and
// compares the diagnostic codes and positions.

package check

import (
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/types"
)

// checkBodyIn is checkBody with module m added under o (o.unsafe lists it in
// unsafe_modules).
func checkBodyIn(t *testing.T, w *world, o opt, want string, src string) *built {
	t.Helper()
	b := w.add("m", o, src)
	var ds []*diag.Diagnostic
	for _, d := range append(b.diags(), b.bodyDiags(t)...) {
		if d.Code != diag.E0208 {
			ds = append(ds, d)
		}
	}
	if got := codes(w, ds); got != want {
		t.Fatalf("diagnostics %q, want %q\n%s", got, want, dump(w, ds))
	}
	return b
}

// asmCase wraps one asm statement in a function with locals of every kind.
func asmCase(stmt string) string {
	return "var Global: u8 = 0\n\nG() {\n}\n\nF(xs: []u8, r: &int, p: *u8, f: f64) {\n\tvar a: u64 = 0\n\tvar b: u8 = 0\n\tvar c := false\n\tvar s: str = \"\"\n\tunsafe \"test\" {\n\t\t" + stmt + "\n\t}\n}\n"
}

func TestAsmOperandTypes(t *testing.T) {
	for _, tc := range []struct{ name, stmt, want string }{
		{"integers floats pointers", `asm("{a} {b} {p} {f}", inout(reg) a, in(reg_byte) b, in(reg) p, in(xmm_reg) f)`, ""},
		{"bool in", `asm("{c}", in(reg_byte) c)`, ""},
		{"bool out", `asm("setc {c}", c: out(reg_byte) c)`, "E1104@12:36"},
		{"slice in", `asm("{x}", x: in(reg) xs)`, "E1104@12:25"},
		{"reference in", `asm("{x}", x: in(reg) r)`, "E1104@12:25"},
		{"str in", `asm("{x}", x: in(reg) s)`, "E1104@12:25"},
		{"discarded output", `asm("rdtsc", out("eax") _, out("edx") _)`, ""},
		{"not a place", `asm("{x}", x: out(reg) a + 1)`, "E0301@12:26"},
		{"labels", `asm("mov {t:r}, {t:e}", t: out(reg) a)`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkBody(t, newWorld(t, true), tc.want, asmCase(tc.stmt))
		})
	}
}

// TestE1104_MaskOutput: a bool or a simd.Mask is refused as an asm output
// or inout (E1104); a Mask as an input is accepted.
func TestE1104_MaskOutput(t *testing.T) {
	simdCases(t, []bodyCase{
		{"mask inout", "Ones(z: simd.Mask[u8, 16]): simd.Mask[u8, 16] {\n\tvar m := z\n\tunsafe \"r\" {\n\t\tasm(\"pcmpeqb {m}, {m}\", m: inout(xmm_reg) m)\n\t}\n\treturn m\n}\n", "E1104@6:45"},
		{"mask in", "F(m: simd.Mask[u8, 16]) {\n\tunsafe \"r\" {\n\t\tasm(\"pmovmskb eax, {m}\", m: in(xmm_reg) m, out(\"eax\") _)\n\t}\n}\n", ""},
		{"vec inout", "F(v: simd.Vec[u8, 16]): simd.Vec[u8, 16] {\n\tvar w := v\n\tunsafe \"r\" {\n\t\tasm(\"pcmpeqb {w}, {w}\", w: inout(xmm_reg) w)\n\t}\n\treturn w\n}\n", ""},
	})
}

func TestAsmGenericOperandE1104(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"type parameter", "F[T: Plain](x: T) {\n\tunsafe \"r\" {\n\t\tasm(\"{x}\", x: in(reg) x)\n\t}\n}\n", "E1104@3:25"},
		{"pointer to T", "F[T](p: *T) {\n\tunsafe \"r\" {\n\t\tasm(\"{p}\", p: in(reg) p)\n\t}\n}\n", "E1104@3:25"},
		{"concrete in a generic function", "F[T](n: u64) {\n\tunsafe \"r\" {\n\t\tasm(\"{n}\", n: in(reg) n)\n\t}\n}\n", ""},
	})
}

func TestAsmOutsideUnsafe(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"pause", "Pause() {\n\tasm(\"pause\", options(nomem, nostack, preserves_flags))\n}\n", "E1101@2:2"},
		{"in unsafe", "Pause() {\n\tunsafe \"pause touches nothing\" {\n\t\tasm(\"pause\", options(nomem, nostack, preserves_flags))\n\t}\n}\n", ""},
	})
}

func TestClobberAbiOnlyC(t *testing.T) {
	for _, tc := range []struct{ name, stmt, want string }{
		{"C", `asm("call {g}", g: sym G, clobber_abi("C"))`, ""},
		{"sysv64", `asm("call {g}", g: sym G, clobber_abi("sysv64"))`, "E1104@12:41"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkBody(t, newWorld(t, true), tc.want, asmCase(tc.stmt))
		})
	}
}

func TestAsmOptions(t *testing.T) {
	for _, tc := range []struct{ name, stmt, want string }{
		{"all known", `asm("lea {a}, [rip]", a: out(reg) a, options(pure, nomem, nostack, preserves_flags, att))`, ""},
		{"unknown", `asm("nop", options(volatile))`, "E1104@12:22"},
		{"twice", `asm("nop", options(nomem, nomem))`, "E1104@12:29"},
		{"nomem and readonly", `asm("nop", options(nomem, readonly))`, "E1104@12:14"},
		{"pure without nomem", `asm("{a}", a: out(reg) a, options(pure))`, "E1104@12:29"},
		{"pure without output", `asm("nop", options(pure, nomem))`, "E1104@12:14"},
		{"noreturn with output", `asm("ud2", a: out(reg) a, options(noreturn))`, "E1104@12:29"},
		{"noreturn", `asm("ud2", options(noreturn))`, ""},
		{"raw reads no hole", `asm("{nothing}", options(raw))`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkBody(t, newWorld(t, true), tc.want, asmCase(tc.stmt))
		})
	}
}

func TestAsmHoles(t *testing.T) {
	for _, tc := range []struct{ name, stmt, want string }{
		{"label and local", `asm("add {a}, {x}", inout(reg) a, x: in(reg) p)`, ""},
		{"braces", `asm("vaddps {a}{{k1}}, {a}", inout(reg) a)`, ""},
		{"unknown hole", `asm("{q}", inout(reg) a)`, "E1104@12:7"},
		{"unclosed", `asm("{a", inout(reg) a)`, "E1104@12:7"},
		{"lone brace", `asm("a}", inout(reg) a)`, "E1104@12:7"},
		{"bad modifier", `asm("{a:k}", inout(reg) a)`, "E1104@12:7"},
		{"label twice", `asm("{t}", t: inout(reg) a, t: in(reg) p)`, "E1104@12:31"},
		{"two operands pass a", `asm("{a}", in(reg) a, lateout(reg) a)`, "E1104@12:7"},
		{"reserved register", `asm("nop", in("rbx") a)`, "E1104@12:17"},
		{"reserved part", `asm("nop", in("BPL") b)`, "E1104@12:17"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkBody(t, newWorld(t, true), tc.want, asmCase(tc.stmt))
		})
	}
}

func TestAsmSymAndConst(t *testing.T) {
	for _, tc := range []struct{ name, stmt, want string }{
		{"function", `asm("call {g}", g: sym G)`, ""},
		{"global", `asm("lea {a}, [rip + {v}]", a: out(reg) a, v: sym Global)`, ""},
		{"local", `asm("{v}", v: sym a)`, "E1104@12:21"},
		{"const literal", `asm("int {n}", n: const 3)`, ""},
		{"const arithmetic", `asm("int {n}", n: const 1 + 2)`, ""},
		{"const sizeof", `asm("add {a}, {n}", inout(reg) a, n: const SizeOf[u64]())`, ""},
		{"const local", `asm("int {n}", n: const a)`, "E1104@12:27"},
		{"const float", `asm("int {n}", n: const 1.5)`, "E1104@12:27"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkBody(t, newWorld(t, true), tc.want, asmCase(tc.stmt))
		})
	}
	runBodyCases(t, []bodyCase{
		{"generic function", "Id[T](x: T): T {\n\treturn x\n}\n\nF() {\n\tunsafe \"r\" {\n\t\tasm(\"call {g}\", g: sym Id)\n\t}\n}\n", "E1104@7:26"},
		{"const item", "const N: int = 4\n\nF() {\n\tunsafe \"r\" {\n\t\tasm(\"int {n}\", n: const N)\n\t}\n}\n", ""},
		{"sym of a const", "const N: int = 4\n\nF() {\n\tunsafe \"r\" {\n\t\tasm(\"{n}\", n: sym N)\n\t}\n}\n", "E1104@5:21"},
		// A const parameter, generic or by value, is a constant
		// per instance; a plain parameter and an expression over a const
		// parameter are not.
		{"const generic parameter", "F[const N: int]() {\n\tunsafe \"r\" {\n\t\tasm(\"int {n}\", n: const N)\n\t}\n}\n", ""},
		{"const value parameter", "F(const k: u8) {\n\tunsafe \"r\" {\n\t\tasm(\"int {k}\", k: const k)\n\t}\n}\n", ""},
		{"plain parameter", "F(k: u8) {\n\tunsafe \"r\" {\n\t\tasm(\"int {k}\", k: const k)\n\t}\n}\n", "E1104@3:27"},
		{"bool const parameter", "F(const k: bool) {\n\tunsafe \"r\" {\n\t\tasm(\"int {k}\", k: const k)\n\t}\n}\n", "E1104@3:27"},
		{"arithmetic over N", "F[const N: int]() {\n\tunsafe \"r\" {\n\t\tasm(\"int {n}\", n: const N + 1)\n\t}\n}\n", "E1104@3:27"},
	})
}

func TestNakedBody(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"entry", "Main2() {\n}\n\nexport extern naked Entry() {\n\tunsafe \"the loader jumps here\" {\n\t\tasm(\"call {k}\\nhlt\", k: sym Main2)\n\t}\n}\n", ""},
		{"options att raw", "naked Halt() {\n\tunsafe \"r\" {\n\t\tasm(\"hlt\", options(att, raw))\n\t}\n}\n", ""},
		{"return", "export extern naked Halt() {\n\treturn\n}\n", "E1105@2:2"},
		{"two statements", "naked Halt() {\n\tunsafe \"r\" {\n\t\tasm(\"hlt\")\n\t}\n\treturn\n}\n", "E1105@5:2"},
		{"empty", "naked Halt() {\n}\n", "E1105@1:14"},
		{"no asm", "naked Halt() {\n\tunsafe \"r\" {\n\t\tx := 1\n\t}\n}\n", "E1105@3:3"},
		{"in operand", "naked Halt() {\n\tunsafe \"r\" {\n\t\tasm(\"{x}\", x: in(reg) 1)\n\t}\n}\n", "E1105@3:14"},
		{"nomem", "naked Halt() {\n\tunsafe \"r\" {\n\t\tasm(\"hlt\", options(nomem))\n\t}\n}\n", "E1105@3:22"},
		{"param used", "naked Halt(n: int) {\n\tunsafe \"r\" {\n\t\tasm(\"int {k}\", k: const n)\n\t}\n}\n", "E1105@3:27"},
		{"type parameter", "naked Halt[T]() {\n\tunsafe \"r\" {\n\t\tasm(\"hlt\")\n\t}\n}\n", "E1105@1:11"},
		{"cpu clause", "naked Halt() cpu \"avx2\" {\n\tunsafe \"r\" {\n\t\tasm(\"hlt\")\n\t}\n}\n", "E1105@1:18"},
		{"contract", "naked Halt(n: int) requires n > 0 {\n\tunsafe \"r\" {\n\t\tasm(\"hlt\")\n\t}\n}\n", "E1105@1:20"},
		{"result with no return", "naked Halt(): int {\n\tunsafe \"r\" {\n\t\tasm(\"hlt\")\n\t}\n}\n", ""},
		{"called outside unsafe", "naked Halt() {\n\tunsafe \"r\" {\n\t\tasm(\"hlt\")\n\t}\n}\n\nF() {\n\tHalt()\n}\n", "E1101@8:2"},
		{"called in unsafe", "naked Halt() {\n\tunsafe \"r\" {\n\t\tasm(\"hlt\")\n\t}\n}\n\nF() {\n\tunsafe \"stops the CPU\" {\n\t\tHalt()\n\t}\n}\n", ""},
		{"named outside unsafe", "naked Halt() {\n\tunsafe \"r\" {\n\t\tasm(\"hlt\")\n\t}\n}\n\nF(): fn() {\n\treturn Halt\n}\n", "E1101@8:9"},
	})
}

func TestAlignLayout(t *testing.T) {
	w := newWorld(t, true)
	b := w.add("m", opt{}, "align(64) struct Line {\n\tx: u8\n}\n\nalign(4096) struct Page {\n\texport entries: [512]u64\n}\n\nalign(2) struct Small {\n\tx: u64\n}\n\nstruct Holds {\n\tb: u8\n\tl: Line\n}\n")
	b.wantClean(t)
	for _, tc := range []struct {
		name        string
		size, align uint64
	}{{"Line", 64, 64}, {"Page", 4096, 4096}, {"Small", 8, 8}, {"Holds", 128, 64}} {
		l := w.c.Layout(w.tab.Named(b.item(tc.name), nil))
		if l.Shape.Size != tc.size || uint64(l.Shape.Align) != tc.align {
			t.Errorf("%s: size %d align %d, want %d %d", tc.name, l.Shape.Size, l.Shape.Align, tc.size, tc.align)
		}
		if d := w.items.Get(b.item(tc.name)).Decl.(*types.StructDecl); tc.name == "Line" && d.Align != 64 {
			t.Errorf("StructDecl.Align %d", d.Align)
		}
	}
	for _, tc := range []struct{ name, src, want string }{
		{"48", "align(48) struct Line {\n\tbytes: [48]u8\n}\n", "E0706@1:1"},
		{"0", "align(0) struct S {\n\tx: u8\n}\n", "E0706@1:1"},
		{"too big", "align(131072) struct S {\n\tx: u8\n}\n", "E0706@1:1"},
		{"with packed", "packed align(8) struct Both {\n\tx: u8\n}\n", "E0706@1:8"},
		{"65536", "align(65536) struct S {\n\tx: u8\n}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkBody(t, newWorld(t, true), tc.want, tc.src)
		})
	}
}

func TestSectionNeedsUnsafeModule(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
		unsafe          bool
	}{
		{"const in unsafe module", "const Header: [3]u32 = [0x1BADB002, 0, 0xE4524FFE] section \".multiboot\"\n", "", true},
		{"var and function", "var Boot: u8 = 0 section \".data.boot\"\n\nF() section \".text.boot\" {\n}\n", "", true},
		{"const outside", "const Header: u32 = 1 section \".multiboot\"\n", "E1102@1:31", false},
		{"function outside", "F() section \".text.boot\" {\n}\n", "E1102@1:13", false},
		{"space", "const Tag: u32 = 7 section \"boot data\"\n", "E0706@1:28", true},
		{"empty", "const Tag: u32 = 7 section \"\"\n", "E0706@1:28", true},
		{"too long", "const Tag: u32 = 7 section \"" + strings.Repeat("s", 65) + "\"\n", "E0706@1:28", true},
		{"64 bytes", "const Tag: u32 = 7 section \"" + strings.Repeat("s", 64) + "\"\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := checkBodyIn(t, newWorld(t, true), opt{unsafe: tc.unsafe}, tc.want, tc.src)
			if tc.name == "const in unsafe module" {
				if d := b.w.items.Get(b.item("Header")).Decl.(*types.ConstDecl); d.Section != ".multiboot" {
					t.Errorf("ConstDecl.Section %q", d.Section)
				}
			}
			if tc.name == "var and function" {
				if d := b.w.items.Get(b.item("F")).Decl.(*types.FuncDecl); d.Section != ".text.boot" {
					t.Errorf("FuncDecl.Section %q", d.Section)
				}
				if d := b.w.items.Get(b.item("Boot")).Decl.(*types.GlobalDecl); d.Section != ".data.boot" {
					t.Errorf("GlobalDecl.Section %q", d.Section)
				}
			}
		})
	}
}

func TestExternVarAccess(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
		unsafe          bool
	}{
		{"address in unsafe", "extern var KernelEnd: u8\n\nEnd(): *u8 {\n\tunsafe \"the linker script defines KernelEnd\" {\n\t\treturn &KernelEnd\n\t}\n}\n", "", true},
		{"read in unsafe", "extern var Ticks: u64\n\nNow(): u64 {\n\tunsafe \"r\" {\n\t\treturn Ticks\n\t}\n}\n", "", true},
		{"write through the address", "extern var Ticks: u64\n\nReset() {\n\tunsafe \"r\" {\n\t\t*(&Ticks) = 0\n\t}\n}\n", "", true},
		{"address outside unsafe", "extern var KernelEnd: u8\n\nEnd(): *u8 {\n\treturn &KernelEnd\n}\n", "E1101@4:10", true},
		{"read outside unsafe", "extern var Ticks: u64\n\nNow(): u64 {\n\treturn Ticks\n}\n", "E1101@4:9", true},
		{"direct write", "extern var Ticks: u64\n\nReset() {\n\tunsafe \"r\" {\n\t\tTicks = 0\n\t}\n}\n", "E0404@5:3", true},
		{"mut", "extern var Ticks: u64\n\nReset(): mut u64 {\n\tunsafe \"r\" {\n\t\treturn mut Ticks\n\t}\n}\n", "E0404@5:14", true},
		{"outside unsafe_modules", "extern var KernelEnd: u8\n", "E1102@1:12", false},
		{"sym of an extern var", "extern var Stack: [4096]u8\n\nF() {\n\tvar a: u64 = 0\n\tunsafe \"r\" {\n\t\tasm(\"lea {a}, [rip + {s}]\", a: out(reg) a, s: sym Stack)\n\t}\n}\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkBodyIn(t, newWorld(t, true), opt{unsafe: tc.unsafe}, tc.want, tc.src)
		})
	}
}

// An extern var's type is plain (E0701 with bound Plain).
func TestExternVarPlain(t *testing.T) {
	checkBodyIn(t, newWorld(t, true), opt{unsafe: true}, "", "extern var A: [16]u64\n\nextern var B: *u8\n")
	b := checkBodyIn(t, newWorld(t, true), opt{unsafe: true}, "E0701@1:15", "extern var R: &u8\n")
	if d := b.w.items.Get(b.item("R")).Decl.(*types.GlobalDecl); !d.Extern {
		t.Error("GlobalDecl.Extern not set")
	}
}

// TestExternVarOtherModule: `&k.Ticks` of another module's
// extern var is a raw pointer, as `&Ticks` is in its own module, so Mk code
// can write a C-bound extern var through it. It gave a reference before
// (E0301 against *u64, and the write through it refused). A direct write,
// `k.Ticks = 0` or `k.Ticks += 1`, is E0404 alone, which names that way,
// as `Ticks = 0` is in its own module; it was also E0301 (not a place),
// the one a report showed.
func TestExternVarOtherModule(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"address", "import k\n\nEnd(): *u64 {\n\tunsafe \"r\" {\n\t\treturn &k.Ticks\n\t}\n}\n", ""},
		{"write through the address", "import k\n\nReset() {\n\tunsafe \"r\" {\n\t\t*(&k.Ticks) = 0\n\t}\n}\n", ""},
		{"direct write", "import k\n\nReset() {\n\tunsafe \"r\" {\n\t\tk.Ticks = 0\n\t}\n}\n", "E0404@5:3"},
		{"compound write", "import k\n\nReset() {\n\tunsafe \"r\" {\n\t\tk.Ticks += 1\n\t}\n}\n", "E0404@5:3"},
		{"mut", "import k\n\nReset(): mut u64 {\n\tunsafe \"r\" {\n\t\treturn mut k.Ticks\n\t}\n}\n", "E0404@5:14"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true)
			w.add("k", opt{unsafe: true}, "export extern var Ticks: u64\n").wantClean(t)
			b := checkBodyIn(t, w, opt{unsafe: true}, tc.want, tc.src)
			for _, d := range b.bodyDiags(t) {
				if d.Code == diag.E0404 && !strings.Contains(d.Message, "`*(&k.Ticks)`") {
					t.Errorf("E0404 names no pointer of k.Ticks: %s", d.Message)
				}
			}
		})
	}
}
