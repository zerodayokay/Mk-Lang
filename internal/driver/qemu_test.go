// Why: the freestanding target is only proven by a kernel that boots: the
// image layout, the boot path and the no-FPU code generation all have to
// hold at once.
// What: TestQemuKernel (dev: long mode, cpuid, serial, debug exit) and
// TestQemuKernelEssentials (release: the linker symbol, the Mk page
// tables and the APIC register, the IDT and int3, a cpu "sse2" function
// under general-regs-only, a HeapList on free.Heap() over the kernel's own
// MkPageAlloc, and a c_sources file compiled for the target).
// How: builds tests/freestanding/kernel with --target=x86_64-freestanding,
// checks the ELF with debug/elf, boots it with qemu-system-x86_64 -kernel
// through its PVH note (skipped when QEMU is absent; never through WSL),
// and disassembles it to find no x87, MMX, SSE or AVX register outside the
// sse2 function.

package driver

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"mk/internal/types"
)

// kernelOK is the code the kernel writes to isa-debug-exit after "mk kernel
// ok" (src/main.mk OkCode); QEMU exits with (code << 1) | 1, so 33.
const kernelOK = 0x10

// kernelBase is where link.ld places .text.boot and the PVH note's entry.
const kernelBase = 0x100000

// buildKernel builds tests/freestanding/kernel for x86_64-freestanding at
// prof and returns the ELF's path.
func buildKernel(t *testing.T, prof types.Profile) string {
	t.Helper()
	tools := needTools(t)
	root, err := filepath.Abs(filepath.Join("..", "..", "tests", "freestanding", "kernel"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(Config{Root: root, Target: types.X86_64Freestanding, Profile: prof, Tools: tools, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(filepath.Join(t.TempDir(), "kernel"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build the kernel at %v: %d paths\n%s", prof, len(paths), r.Report.Text(r.Sources))
	}
	return paths[0]
}

// checkKernelELF checks what 4.12 asks of the image without running it,
// and returns its symbols by name.
func checkKernelELF(t *testing.T, path string, prof types.Profile) map[string]elf.Symbol {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Type != elf.ET_EXEC || f.Machine != elf.EM_X86_64 || f.Class != elf.ELFCLASS64 {
		t.Errorf("type %v machine %v class %v, want ET_EXEC EM_X86_64 ELFCLASS64", f.Type, f.Machine, f.Class)
	}
	if f.Section(".dynamic") != nil || f.Section(".interp") != nil {
		t.Error("a freestanding image has a .dynamic or .interp section")
	}
	var notes []*elf.Prog
	for _, p := range f.Progs {
		switch p.Type {
		case elf.PT_INTERP, elf.PT_DYNAMIC:
			t.Errorf("program header %v in a freestanding image", p.Type)
		case elf.PT_NOTE:
			notes = append(notes, p)
		}
	}
	// The PVH note: name "Xen", type 18 (XEN_ELFNOTE_PHYS32_ENTRY), a 4-byte
	// entry address, in a PT_NOTE segment.
	var entry uint32
	found := false
	for _, p := range notes {
		data := make([]byte, p.Filesz)
		if _, err := p.ReadAt(data, 0); err != nil {
			t.Fatal(err)
		}
		for len(data) >= 12 {
			namesz := binary.LittleEndian.Uint32(data[0:])
			descsz := binary.LittleEndian.Uint32(data[4:])
			typ := binary.LittleEndian.Uint32(data[8:])
			nameEnd := 12 + int(namesz+3)&^3
			descEnd := nameEnd + int(descsz+3)&^3
			if descEnd > len(data) {
				break
			}
			if typ == 18 && namesz == 4 && bytes.Equal(data[12:16], []byte("Xen\x00")) && descsz == 4 {
				entry, found = binary.LittleEndian.Uint32(data[nameEnd:]), true
			}
			data = data[descEnd:]
		}
	}
	if !found {
		t.Errorf("no PVH note (Xen, type 18) in the %d PT_NOTE segments", len(notes))
	}
	// Whether lld types .note.Xen SHT_NOTE from its name is checked here:
	// it does, as LLVM names it in the object.
	if s := f.Section(".note.Xen"); s == nil || s.Type != elf.SHT_NOTE {
		t.Errorf(".note.Xen missing or not SHT_NOTE: %v", s)
	}
	boot := f.Section(".text.boot")
	if boot == nil || boot.Addr != kernelBase {
		t.Errorf(".text.boot missing or not at link.ld's %#x: %v", kernelBase, boot)
	}
	if found && entry != kernelBase {
		t.Errorf("PVH entry %#x, want %#x", entry, kernelBase)
	}
	syms, err := f.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]elf.Symbol{}
	for _, s := range syms {
		byName[s.Name] = s
	}
	if s, ok := byName["MkEntry"]; !ok || s.Value != kernelBase || f.Entry != kernelBase {
		t.Errorf("MkEntry %#x (found %t), ELF entry %#x, want both %#x", s.Value, ok, f.Entry, kernelBase)
	}
	// At release ThinLTO internalizes and inlines the hook into std.free's,
	// so only dev keeps the symbol.
	if s, ok := byName["MkPanicHook"]; prof == types.Dev && (!ok || elf.ST_BIND(s.Info) != elf.STB_GLOBAL) {
		t.Errorf("the kernel's MkPanicHook is not a global definition (found %t)", ok)
	}
	if s, ok := byName["MkPageAlloc"]; prof == types.Dev && (!ok || elf.ST_BIND(s.Info) != elf.STB_GLOBAL) {
		t.Errorf("the kernel's MkPageAlloc did not replace the weak default (found %t)", ok)
	}
	if _, ok := byName["KernelEnd"]; !ok {
		t.Error("no KernelEnd symbol (link.ld defines it)")
	}
	return byName
}

// sseFns are the functions of the kernel allowed an x87, MMX, SSE or AVX
// register: its one cpu "sse2" function (src/fpu.mk).
var sseFns = map[string]bool{"mk.src.Scaled": true}

// fpRegs matches an x87, MMX, SSE or AVX register in llvm-objdump's AT&T
// output.
var fpRegs = regexp.MustCompile(`%(?:[xyz]mm[0-9]+\b|mm[0-7]\b|st\([0-7]\)|st\b)`)

// code32Fns are the kernel's 32-bit functions: MkEntry, the PVH entry's
// .code32 template (src/boot.mk). They are read in 32-bit mode: read as
// 64-bit code their bytes decode as other instructions, and the far jump's
// target, Entry64's address, reads as movups wherever the layout gives it a
// 0x0f byte.
var code32Fns = map[string]bool{"MkEntry": true}

// checkGeneralRegsOnly disassembles the general-regs-only kernel and fails
// on every instruction outside sseFns that names an x87, MMX, SSE or AVX
// register: the support pack's memcpy, memmove and memset and every helper
// codegen emits are compiled without them (a release memcpy once came
// out vectorized with movups). code32Fns are read in a second, 32-bit
// listing of their own.
func checkGeneralRegsOnly(t *testing.T, path string) {
	t.Helper()
	tools := needTools(t)
	exe := "llvm-objdump"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	objdump := filepath.Join(filepath.Dir(tools.Clang), exe)
	if _, err := os.Stat(objdump); err != nil {
		t.Logf("no %s next to clang: the general-regs-only disassembly check is not run", objdump)
		return
	}
	// scan checks one listing, skipping the functions skip names, and
	// returns how many functions it lists.
	scan := func(skip map[string]bool, args ...string) int {
		out, err := exec.Command(objdump, append(append([]string{"-d", "--no-show-raw-insn"}, args...), path)...).CombinedOutput()
		if err != nil {
			t.Fatalf("llvm-objdump: %v\n%s", err, out)
		}
		fn, fns := "", 0
		for _, line := range strings.Split(string(out), "\n") {
			if i := strings.Index(line, " <"); i >= 0 && strings.HasSuffix(line, ">:") {
				fn, fns = line[i+2:len(line)-2], fns+1
				continue
			}
			if fpRegs.MatchString(line) && !sseFns[fn] && !skip[fn] {
				t.Errorf("general-regs-only kernel: %s uses an x87, MMX, SSE or AVX register: %s", fn, strings.TrimSpace(line))
			}
		}
		return fns
	}
	if scan(code32Fns) == 0 {
		t.Errorf("llvm-objdump listed no function of %s", path)
	}
	for fn := range code32Fns {
		if scan(nil, "--triple=i386-unknown-none-elf", "--disassemble-symbols="+fn) != 1 {
			t.Errorf("llvm-objdump did not list the 32-bit function %s of %s", fn, path)
		}
	}
}

// qemuPath finds qemu-system-x86_64 on PATH or in QEMU's Windows install
// directory, or "".
func qemuPath() string {
	if p, err := exec.LookPath("qemu-system-x86_64"); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		p := filepath.Join(os.Getenv("ProgramFiles"), "qemu", "qemu-system-x86_64.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// bootKernel boots path under QEMU (qemu-system-x86_64 -kernel, with the
// serial port written to a file) and returns the serial lines and QEMU's
// exit status, or skips when QEMU is absent.
func bootKernel(t *testing.T, path string) ([]string, int) {
	t.Helper()
	q := qemuPath()
	if q == "" {
		t.Skip("qemu-system-x86_64 is not on PATH or under Program Files\\qemu: the boot is not run (the ELF was checked)")
	}
	serial := filepath.Join(t.TempDir(), "serial.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, q, "-kernel", path, "-display", "none", "-monitor", "none", "-no-reboot",
		"-serial", "file:"+serial, "-device", "isa-debug-exit,iobase=0xf4,iosize=0x04", "-m", "128M")
	out, err := cmd.CombinedOutput()
	status := 0
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		t.Fatalf("QEMU did not exit within 30 s\n%s", out)
	case errors.As(err, &ee):
		status = ee.ExitCode()
	case err != nil:
		t.Fatalf("run %s: %v\n%s", q, err, out)
	}
	text, err := os.ReadFile(serial)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(strings.ReplaceAll(string(text), "\r\n", "\n"), "\n"), "\n"), status
}

// lineWith returns the serial line that starts with prefix, without it.
func lineWith(t *testing.T, lines []string, prefix string) string {
	t.Helper()
	for _, l := range lines {
		if rest, ok := strings.CutPrefix(l, prefix); ok {
			return rest
		}
	}
	t.Errorf("no serial line %q in:\n%s", prefix, strings.Join(lines, "\n"))
	return ""
}

// TestQemuKernel: the dev kernel is a static PVH ELF; booted, it enters
// long mode in its naked entries, reads cpuid, writes COM1 with out and
// ends through isa-debug-exit with the expected status.
func TestQemuKernel(t *testing.T) {
	t.Parallel()
	elfPath := buildKernel(t, types.Dev)
	checkKernelELF(t, elfPath, types.Dev)
	checkGeneralRegsOnly(t, elfPath)
	lines, status := bootKernel(t, elfPath)
	if len(lines) == 0 || lines[0] != "mk kernel: long mode" {
		t.Errorf("first line, want %q:\n%s", "mk kernel: long mode", strings.Join(lines, "\n"))
	}
	if v := lineWith(t, lines, "cpuid vendor "); len(v) < 12 || !strings.Contains(v[12:], " max leaf 0x") {
		t.Errorf("cpuid line %q: want a 12-byte vendor and the max leaf", v)
	}
	lineWith(t, lines, "cpu sse2 yes")
	if want := (kernelOK << 1) | 1; status != want || lines[len(lines)-1] != "mk kernel ok" {
		t.Errorf("status %d, last line %q; want %d and %q:\n%s", status, lines[len(lines)-1], want, "mk kernel ok", strings.Join(lines, "\n"))
	}
}

// TestQemuKernelEssentials: the release kernel prints KernelEnd's
// address (the ELF's symbol), the APIC version read through its own
// align(4096) page tables with mmio.Load, one int3 hit counted by the naked
// stub's handler, and the cpu "sse2" function's result, then exits OK.
func TestQemuKernelEssentials(t *testing.T) {
	t.Parallel()
	elfPath := buildKernel(t, types.Release)
	syms := checkKernelELF(t, elfPath, types.Release)
	checkGeneralRegsOnly(t, elfPath)
	for name, align := range map[string]uint64{"mk.src.Pml4": 4096, "mk.src.Pdpt": 4096, "mk.src.PdLow": 4096, "mk.src.PdApic": 4096, "mk.src.Fx": 16} {
		if s, ok := syms[name]; !ok || s.Value%align != 0 {
			t.Errorf("%s at %#x (found %t), want a multiple of %d", name, s.Value, ok, align)
		}
	}
	lines, status := bootKernel(t, elfPath)
	end := lineWith(t, lines, "kernel end 0x")
	if got, err := strconv.ParseUint(end, 16, 64); err != nil || got != syms["KernelEnd"].Value {
		t.Errorf("kernel end %q, want the KernelEnd symbol %#x", end, syms["KernelEnd"].Value)
	}
	if v, err := strconv.ParseUint(lineWith(t, lines, "apic version 0x"), 16, 32); err != nil || v == 0 {
		t.Errorf("apic version %d (%v): want a nonzero version byte", v, err)
	}
	for _, want := range []string{"int3 hits 1", "sse2 scaled 42", "c sum 889", "heap sum 499500", "mk kernel ok"} {
		lineWith(t, lines, want)
	}
	if want := (kernelOK << 1) | 1; status != want {
		t.Errorf("status %d, want %d:\n%s", status, want, strings.Join(lines, "\n"))
	}
}
