// Why: a program may name a function of a DLL that is absent on some
// machines (an optional system component, a newer Windows); a hard import
// of it kills the process before Main, so the DLL must be loaded, and each
// function found, on its first call instead.
//
// What: LazyLib (one lazily bound DLL and the names a build uses),
// EmitLazy (the build's lazy-binding object), LazySymOK, and the
// stub's assembly win64LazyStub (mk_lazy_stub).
//
// How: per used name S the object defines S, a thunk that jumps through
// the slot __imp_S, and __imp_S itself, which starts at a per-name first
// stub; so a direct call, a fn value and a C object's dllimport call all
// reach the slot, as through an import table. The first stub loads the
// name's descriptor address into rax and jumps to mk_lazy_stub, which
// keeps the argument registers, calls std.sys's MkLazyResolve(descriptor)
// (which loads the library on the system stack, stores the address in the
// slot or panics) and jumps through the slot it returns, the arguments
// intact. The descriptor is the slot followed by its library's record and
// the name; the record holds the module handle the resolver publishes.

package codegen

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf16"

	"mk/internal/llvm"
	"mk/internal/source"
	"mk/internal/types"
)

// LazyLib is one lazily bound DLL of a build: the file name the loader
// looks up, the LoadLibraryExW flags it is loaded with, and the names the
// build uses from it, sorted.
type LazyLib struct {
	File  string
	Flags uint32
	Names []string
}

// symLazyStub is the shared resolver entry; symLazyResolve the std.sys
// function it calls (std/sys/windows/lazy.mk), by symbol.
const (
	symLazyStub    = "mk_lazy_stub"
	symLazyResolve = "MkLazyResolve"
	// SymLazyPresent is std.sys's MkLazyPresent, which dl.Present calls
	// for a lazily bound function.
	SymLazyPresent = "MkLazyPresent"
)

// LazySlot is the slot symbol of the lazily bound name s: the address the
// thunk s jumps through, and its descriptor.
func LazySlot(s string) string { return "__imp_" + s }

// LazySymOK reports whether s can be bound lazily: a C identifier, the only
// form the assembly spells without quoting.
func LazySymOK(s string) bool {
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// win64LazyStub is mk_lazy_stub(descriptor: rax), entered by a jump from a
// first stub with the callee's arguments live: rcx, rdx, r8, r9 and xmm0
// to xmm3 are kept in its frame (the whole 16 bytes of each xmm), the
// caller's shadow space and stack arguments above the return address are
// never touched, and rsp is 16-aligned at the call (8 mod 16 at entry, the
// push makes it 0). MkLazyResolve returns the slot, which then holds the
// function; the epilogue is the documented lea/pop/jmp form, jmp through
// memory (ModRM mod 00), so an unwinder recognizes it.
const win64LazyStub = `
pushq %rbp
.seh_pushreg %rbp
movq %rsp, %rbp
.seh_setframe %rbp, 0
.seh_endprologue
subq $128, %rsp
movq %rcx, 96(%rsp)
movq %rdx, 104(%rsp)
movq %r8, 112(%rsp)
movq %r9, 120(%rsp)
movaps %xmm0, 32(%rsp)
movaps %xmm1, 48(%rsp)
movaps %xmm2, 64(%rsp)
movaps %xmm3, 80(%rsp)
movq %rax, %rcx
callq ` + symLazyResolve + `
movq 96(%rsp), %rcx
movq 104(%rsp), %rdx
movq 112(%rsp), %r8
movq 120(%rsp), %r9
movaps 32(%rsp), %xmm0
movaps 48(%rsp), %xmm1
movaps 64(%rsp), %xmm2
movaps 80(%rsp), %xmm3
leaq 0(%rbp), %rsp
popq %rbp
jmpq *(%rax)
`

// EmitLazy writes the lazy-binding object of libs for cfg's target (only
// x86_64-windows has one) as a module of assembly, compiled as the support
// pack is. Per library a 40-byte record in .data: the module handle (0
// until the resolver loads it), its UTF-16 file name, its ASCII file name
// and length, and the flags (std/sys/windows/lazy.mk reads both layouts).
// Per name the 40-byte descriptor __imp_S in .data: the slot (the first
// stub until resolved), the library record, the NUL-terminated name, the
// first stub's address (the slot's unbound value) and the name's length.
// Same libs, same bytes.
func EmitLazy(libs []LazyLib, cfg Config, w io.Writer) error {
	var werr error
	err := guard("lazy bindings", source.Span{}, func() {
		if cfg.Target.OS != types.OSWindows || cfg.Target.Arch != types.ArchX64 {
			source.ICEf(source.Span{}, "codegen: lazy binding on %s", cfg.Target.Name)
		}
		s := &support{m: llvm.NewModule("mk.lazy", cfg.triple(), cfg.DataLayout), cfg: &cfg, t: cfg.Target}
		s.asmFunc(symLazyStub, win64LazyStub)
		var data, text, rdata strings.Builder
		for li, l := range libs {
			lib := fmt.Sprintf("mk.lazy.lib.%d", li)
			fmt.Fprintf(&data, "\t.p2align 3\n%s:\n\t.quad 0\n\t.quad %s.w\n\t.quad %s.a\n\t.quad %d\n\t.long %d\n\t.long 0\n",
				lib, lib, lib, len(l.File), l.Flags)
			fmt.Fprintf(&rdata, "\t.p2align 1\n%s.w:\n", lib)
			for _, u := range utf16.Encode([]rune(l.File)) {
				fmt.Fprintf(&rdata, "\t.short %d\n", u)
			}
			fmt.Fprintf(&rdata, "\t.short 0\n%s.a:\n\t.asciz %q\n", lib, l.File)
			for _, n := range l.Names {
				if !LazySymOK(n) {
					source.ICEf(source.Span{}, "codegen: lazy name %q is not a C identifier", n)
				}
				slot, first := LazySlot(n), "mk.lazy.first."+n
				fmt.Fprintf(&data, "\t.globl %s\n\t.p2align 3\n%s:\n\t.quad %s\n\t.quad %s\n\t.quad mk.lazy.name.%s\n\t.quad %s\n\t.quad %d\n",
					slot, slot, first, lib, n, first, len(n))
				fmt.Fprintf(&rdata, "mk.lazy.name.%s:\n\t.asciz %q\n", n, n)
				fmt.Fprintf(&text, "\t.globl %s\n\t.def %s;\n\t.scl 2;\n\t.type 32;\n\t.endef\n\t.p2align 4\n%s:\n\tjmpq *%s(%%rip)\n",
					n, n, n, slot)
				fmt.Fprintf(&text, "\t.def %s;\n\t.scl 3;\n\t.type 32;\n\t.endef\n%s:\n\tleaq %s(%%rip), %%rax\n\tjmp %s\n",
					first, first, slot, symLazyStub)
			}
		}
		s.asm.WriteString("\t.text\n" + text.String())
		s.asm.WriteString("\t.data\n" + data.String())
		s.asm.WriteString("\t.section .rdata,\"dr\"\n" + rdata.String())
		s.m.ModuleAsm(strings.TrimRight(s.asm.String(), "\n"))
		_, werr = s.m.WriteTo(w)
	})
	if err != nil {
		return err
	}
	return werr
}
