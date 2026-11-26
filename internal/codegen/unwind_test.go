// Why: debuggers, profilers and the fault walk unwind through the support
// pack's hand-written routines, which have no compiler-made unwind data.
// What: TestSupportUnwind (every support routine is covered by .pdata
// and .xdata on COFF and .eh_frame on ELF, with the rules its pushes need),
// TestSupportWalk (a run-time walk through mk_call_system and mk_switch
// to the task's entry) and TestSupportAttrs.
// How: a small .eh_frame parser and unwinder (parseEhFrame, rowAt, ehWalk)
// and a Win64 decoder; on Windows the walk uses RtlVirtualUnwind, on Linux
// the harness dumps its registers and stacks for this file's unwinder.

package codegen

import (
	"bytes"
	"debug/elf"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"mk/internal/types"
)

// DWARF register numbers of x86_64 (System V psABI figure 3.36).
const (
	dwRBX = 3
	dwRBP = 6
	dwRSP = 7
	dwRIP = 16
)

var dwRegs = map[string]int{"rax": 0, "rdx": 1, "rcx": 2, "rbx": 3, "rsi": 4, "rdi": 5, "rbp": 6, "rsp": 7,
	"r8": 8, "r9": 9, "r10": 10, "r11": 11, "r12": 12, "r13": 13, "r14": 14, "r15": 15}

// cfaRow is the unwind rule at one address: CFA = reg + off; saved holds
// the CFA-relative slot of each register saved; undef is a return address
// marked undefined (the outermost frame).
type cfaRow struct {
	reg   int
	off   int64
	saved map[int]int64
	undef bool
}

func (r cfaRow) clone() cfaRow {
	c := r
	c.saved = map[int]int64{}
	for k, v := range r.saved {
		c.saved[k] = v
	}
	return c
}

// fde is one parsed frame description: [lo, hi) and its rows by address.
type fde struct {
	lo, hi uint64
	init   []byte // the CIE's initial instructions
	prog   []byte
	caf    uint64 // code alignment
	daf    int64  // data alignment
}

func uleb(b []byte, i *int) uint64 {
	var v uint64
	var s uint
	for {
		c := b[*i]
		*i++
		v |= uint64(c&0x7f) << s
		s += 7
		if c < 0x80 {
			return v
		}
	}
}

func sleb(b []byte, i *int) int64 {
	var v int64
	var s uint
	for {
		c := b[*i]
		*i++
		v |= int64(c&0x7f) << s
		s += 7
		if c < 0x80 {
			if s < 64 && c&0x40 != 0 {
				v |= -1 << s
			}
			return v
		}
	}
}

// parseEhFrame reads .eh_frame's FDEs. begin resolves an FDE's pc_begin
// field at byte offset off of the section (with its raw value): in an
// object through its relocation, in an executable pc-relative.
func parseEhFrame(t *testing.T, b []byte, begin func(off int, raw int32) uint64) []fde {
	t.Helper()
	type cie struct {
		caf  uint64
		daf  int64
		init []byte
		enc  byte
	}
	cies := map[int]cie{}
	var out []fde
	for i := 0; i+4 <= len(b); {
		start := i
		n := int(binary.LittleEndian.Uint32(b[i:]))
		if n == 0 {
			break
		}
		i += 4
		end := i + n
		id := binary.LittleEndian.Uint32(b[i:])
		if id == 0 {
			j := i + 4
			j++ // version
			aug := ""
			for b[j] != 0 {
				aug += string(b[j])
				j++
			}
			j++
			c := cie{}
			c.caf = uleb(b, &j)
			c.daf = sleb(b, &j)
			j++ // return address register
			if strings.HasPrefix(aug, "z") {
				al := int(uleb(b, &j))
				k := j
				for _, a := range aug[1:] {
					switch a {
					case 'R':
						c.enc = b[k]
						k++
					case 'L':
						k++
					case 'P':
						t.Fatalf("a personality in the support pack's CIE")
					}
				}
				j += al
			}
			c.init = b[j:end]
			cies[start] = c
		} else {
			c, ok := cies[i-int(id)]
			if !ok {
				t.Fatalf(".eh_frame: an FDE at %d names no CIE", start)
			}
			if c.enc != 0x1b {
				t.Fatalf(".eh_frame: FDE encoding %#x, want pcrel sdata4", c.enc)
			}
			j := i + 4
			lo := begin(j, int32(binary.LittleEndian.Uint32(b[j:])))
			size := uint64(binary.LittleEndian.Uint32(b[j+4:]))
			j += 8
			al := int(uleb(b, &j))
			j += al
			out = append(out, fde{lo: lo, hi: lo + size, init: c.init, prog: b[j:end], caf: c.caf, daf: c.daf})
		}
		i = end
	}
	return out
}

// rowAt runs f's program up to address pc and returns the rule there.
func (f fde) rowAt(t *testing.T, pc uint64) cfaRow {
	t.Helper()
	row := cfaRow{saved: map[int]int64{}}
	var initRow cfaRow
	var stack []cfaRow
	loc := f.lo
	run := func(p []byte, isInit bool) bool {
		for i := 0; i < len(p); {
			op := p[i]
			i++
			adv := func(d uint64) bool {
				loc += d * f.caf
				return loc > pc
			}
			switch op >> 6 {
			case 1:
				if adv(uint64(op & 0x3f)) {
					return true
				}
				continue
			case 2:
				row.saved[int(op&0x3f)] = int64(uleb(p, &i)) * f.daf
				continue
			case 3:
				if v, ok := initRow.saved[int(op&0x3f)]; ok {
					row.saved[int(op&0x3f)] = v
				} else {
					delete(row.saved, int(op&0x3f))
				}
				continue
			}
			switch op {
			case 0x00:
			case 0x02:
				d := uint64(p[i])
				i++
				if adv(d) {
					return true
				}
			case 0x03:
				d := uint64(binary.LittleEndian.Uint16(p[i:]))
				i += 2
				if adv(d) {
					return true
				}
			case 0x04:
				d := uint64(binary.LittleEndian.Uint32(p[i:]))
				i += 4
				if adv(d) {
					return true
				}
			case 0x05:
				r := int(uleb(p, &i))
				row.saved[r] = int64(uleb(p, &i)) * f.daf
			case 0x07:
				r := int(uleb(p, &i))
				if r == dwRIP {
					row.undef = true
				}
			case 0x08:
				r := int(uleb(p, &i))
				delete(row.saved, r)
			case 0x0a:
				stack = append(stack, row.clone())
			case 0x0b:
				row = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			case 0x0c:
				row.reg = int(uleb(p, &i))
				row.off = int64(uleb(p, &i))
			case 0x0d:
				row.reg = int(uleb(p, &i))
			case 0x0e:
				row.off = int64(uleb(p, &i))
			default:
				t.Fatalf(".eh_frame: CFA op %#x not read by this test", op)
			}
		}
		if isInit {
			initRow = row.clone()
		}
		return false
	}
	run(f.init, true)
	run(f.prog, false)
	return row
}

func fdeFor(fdes []fde, pc uint64) (fde, bool) {
	for _, f := range fdes {
		if pc >= f.lo && pc < f.hi {
			return f, true
		}
	}
	return fde{}, false
}

// asmRoutines are the support pack's module-assembly routines, in order.
func asmRoutines(ll string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`module asm "\\09\.globl ([A-Za-z0-9_.]+)"`).FindAllStringSubmatch(ll, -1) {
		out = append(out, m[1])
	}
	return out
}

// disasm is each function's instructions in obj: name -> (offset, text).
type insn struct {
	off  uint64
	text string
}

func disasm(t *testing.T, bin, obj string) map[string][]insn {
	t.Helper()
	full := runTool(t, tool(bin, "llvm-objdump"), "-d", "--no-show-raw-insn", "--print-imm-hex", obj)
	res := map[string][]insn{}
	cur := ""
	head := regexp.MustCompile(`^[0-9a-f]+ <(.+)>:$`)
	line := regexp.MustCompile(`^\s*([0-9a-f]+):\s+(.*)$`)
	for _, l := range strings.Split(full, "\n") {
		l = strings.TrimRight(l, "\r")
		if m := head.FindStringSubmatch(l); m != nil {
			cur = m[1]
			continue
		}
		if m := line.FindStringSubmatch(l); m != nil && cur != "" {
			off, _ := strconv.ParseUint(m[1], 16, 64)
			res[cur] = append(res[cur], insn{off, strings.Join(strings.Fields(m[2]), " ")})
		}
	}
	return res
}

// simulate checks rows against the instructions of one System V routine:
// before each instruction CFA must be the entry stack pointer plus 8, and
// each callee-saved register pushed must be recorded at its slot.
func simulate(t *testing.T, name string, ins []insn, row func(uint64) cfaRow) {
	t.Helper()
	delta := int64(0)
	fp := map[int]int64{}
	pushed := map[int]int64{}
	imm := regexp.MustCompile(`\$0x([0-9a-f]+), %rsp$`)
	for _, in := range ins {
		r := row(in.off)
		if !r.undef {
			want := int64(0)
			switch r.reg {
			case dwRSP:
				want = delta + 8
			case dwRBX:
				d, ok := fp[dwRBX]
				if !ok {
					t.Errorf("%s+%#x %q: CFA on rbx, which holds no frame", name, in.off, in.text)
				}
				want = d + 8
			default:
				t.Errorf("%s+%#x: CFA on register %d", name, in.off, r.reg)
			}
			if r.off != want {
				t.Errorf("%s+%#x %q: CFA offset %d, want %d", name, in.off, in.text, r.off, want)
			}
			for reg, d := range pushed {
				if got, ok := r.saved[reg]; !ok || got != -(d+8) {
					t.Errorf("%s+%#x %q: register %d saved at CFA%+d (%v), want CFA%+d", name, in.off, in.text, reg, got, ok, -(d + 8))
				}
			}
		}
		f := strings.Fields(in.text)
		if len(f) == 0 || r.undef {
			continue
		}
		switch {
		case f[0] == "pushq":
			delta += 8
			if reg, ok := dwRegs[strings.TrimPrefix(f[1], "%")]; ok && (reg == dwRBX || reg == dwRBP || reg >= 12) {
				pushed[reg] = delta
			}
		case f[0] == "popq":
			delete(pushed, dwRegs[strings.TrimPrefix(f[1], "%")])
			delta -= 8
		case f[0] == "subq" && imm.MatchString(in.text):
			v, _ := strconv.ParseInt(imm.FindStringSubmatch(in.text)[1], 16, 64)
			delta += v
		case f[0] == "addq" && imm.MatchString(in.text):
			v, _ := strconv.ParseInt(imm.FindStringSubmatch(in.text)[1], 16, 64)
			delta -= v
		case in.text == "movq %rsp, %rbx":
			fp[dwRBX] = delta
		case in.text == "movq %rbx, %rsp":
			delta = fp[dwRBX]
		}
	}
}

// TestSupportUnwind: every support routine in module assembly, and
// every support function in IR, has an unwind entry on x86_64 Windows
// (.pdata, with .xdata codes matching its pushes) and an FDE on x86_64
// Linux (.eh_frame, whose CFA rule at each instruction matches its pushes;
// _start, mk_task_start and mk_sigreturn, and mk_clone's child path, mark
// the return address undefined, the end of a walk).
func TestSupportUnwind(t *testing.T) {
	bin := needLLVM(t)
	t.Run("x86_64-linux", func(t *testing.T) {
		ll := supportLL(t, types.X86_64Linux)
		obj := clangC(t, types.X86_64Linux, t.TempDir(), "support", ll, "-O2")
		f, err := elf.Open(obj)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		syms, _ := f.Symbols()
		eh := f.Section(".eh_frame")
		if eh == nil {
			t.Fatal("no .eh_frame")
		}
		data, _ := eh.Data()
		rel := map[uint64]uint64{}
		if rs := f.Section(".rela.eh_frame"); rs != nil {
			rb, _ := rs.Data()
			for i := 0; i+24 <= len(rb); i += 24 {
				off := binary.LittleEndian.Uint64(rb[i:])
				info := binary.LittleEndian.Uint64(rb[i+8:])
				add := int64(binary.LittleEndian.Uint64(rb[i+16:]))
				s := syms[int(info>>32)-1]
				rel[off] = uint64(s.Section)<<40 | uint64(int64(s.Value)+add) // section-qualified: FDEs of other sections never overlap
			}
		}
		fdes := parseEhFrame(t, data, func(off int, raw int32) uint64 {
			v, ok := rel[uint64(off)]
			if !ok {
				t.Fatalf(".eh_frame: no relocation at %d", off)
			}
			return v
		})
		text := map[string]elf.Symbol{}
		for _, s := range syms {
			if elf.ST_TYPE(s.Info) == elf.STT_FUNC && s.Section != elf.SHN_UNDEF && f.Sections[s.Section].Name == ".text" {
				text[s.Name] = s
			}
		}
		ins := disasm(t, bin, obj)
		routines := asmRoutines(ll)
		if len(routines) < 6 {
			t.Fatalf("support routines %v", routines)
		}
		undef := map[string]bool{symELFStart: true, symTaskStart: true, symSigreturn: true}
		for name, s := range text {
			key := uint64(s.Section)<<40 | s.Value
			fd, ok := fdeFor(fdes, key)
			if !ok || fd.lo != key || fd.hi != key+s.Size {
				t.Errorf("%s [%#x, %#x): FDE %v [%#x, %#x)", name, s.Value, s.Value+s.Size, ok, fd.lo, fd.hi)
				continue
			}
			isAsm := false
			for _, r := range routines {
				isAsm = isAsm || r == name
			}
			if !isAsm {
				continue
			}
			if undef[name] {
				if !fd.rowAt(t, key).undef {
					t.Errorf("%s: the return address is not undefined at entry", name)
				}
				continue
			}
			simulate(t, name, ins[name], func(off uint64) cfaRow { return fd.rowAt(t, uint64(s.Section)<<40|off) })
		}
		for _, r := range routines {
			if _, ok := text[r]; !ok {
				t.Errorf("routine %s: no function symbol in .text", r)
			}
		}
	})
	// The Windows support pack as every build has it, and as a build that
	// traps a call has it, with mk_trap_call (trapform.go).
	for _, name := range []string{"x86_64-windows", "x86_64-windows-trapform"} {
		t.Run(name, func(t *testing.T) { supportUnwindWin64(t, bin, name != "x86_64-windows") })
	}
}

// supportUnwindWin64 is TestSupportUnwind's x86_64-windows check, of
// the support pack of a build with a trapped call when traps is set.
func supportUnwindWin64(t *testing.T, bin string, traps bool) {
	{
		ll := supportLL(t, types.X86_64Windows)
		if traps {
			e := newTestEnv()
			var b bytes.Buffer
			if err := EmitSupport(trapUnitOf(e), e, Config{Target: types.X86_64Windows, Profile: types.Release, TrapForm: true}, &b); err != nil {
				t.Fatal(err)
			}
			ll = b.String()
			if !strings.Contains(ll, symTrapCall+":") {
				t.Fatal("the trap unit's support pack has no mk_trap_call")
			}
		}
		obj := clangC(t, types.X86_64Windows, t.TempDir(), "support", ll, "-O2")
		f, err := pe.Open(obj)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		type at struct {
			sec int
			off uint32
		}
		where := func(sym uint32, add uint32) at {
			s := f.COFFSymbols[sym]
			return at{int(s.SectionNumber), s.Value + add}
		}
		funcs := map[at]string{}
		for i := 0; i < len(f.COFFSymbols); i++ {
			s := f.COFFSymbols[i]
			name, _ := s.FullName(f.StringTable)
			if s.SectionNumber > 0 && s.StorageClass == 2 && s.Type == 0x20 && !strings.HasPrefix(name, ".weak.") {
				funcs[at{int(s.SectionNumber), s.Value}] = name
			}
			i += int(s.NumberOfAuxSymbols)
		}
		type entry struct {
			begin, end at
			info       at
		}
		var ents []entry
		for _, sec := range f.Sections {
			if !strings.HasPrefix(sec.Name, ".pdata") {
				continue
			}
			b, _ := sec.Data()
			rs := map[uint32]uint32{}
			for _, r := range sec.Relocs {
				rs[r.VirtualAddress] = r.SymbolTableIndex
			}
			for i := 0; i+12 <= len(b); i += 12 {
				get := func(k int) at {
					return where(rs[uint32(i+k)], binary.LittleEndian.Uint32(b[i+k:]))
				}
				ents = append(ents, entry{get(0), get(4), get(8)})
			}
		}
		unwind := func(a at) []byte {
			b, _ := f.Sections[a.sec-1].Data()
			n := int(b[a.off+2])
			return b[a.off : int(a.off)+4+2*n]
		}
		got := map[string][]byte{}
		for _, e := range ents {
			if n, ok := funcs[e.begin]; ok {
				got[n] = unwind(e.info)
			}
		}
		// A function with no entry must be a leaf that never moves rsp
		// (Win64 unwinds it as one: its return address at [rsp]).
		ins := disasm(t, bin, obj)
		for _, n := range funcs {
			if _, ok := got[n]; ok {
				continue
			}
			for _, in := range ins[n] {
				if f := strings.Fields(in.text); len(f) > 0 && (f[0] == "pushq" || f[0] == "callq" || strings.HasSuffix(in.text, "%rsp") && !strings.HasPrefix(in.text, "mov")) {
					t.Errorf("%s: no .pdata entry, and it is no leaf: %q", n, in.text)
					break
				}
			}
		}
		// Each routine's codes, decoded in prologue order: push r, alloc n,
		// xmm r@off, frame r+off.
		want := map[string]string{
			symChkstk:        "push rcx; push rax",
			symChkstkAligned: "push rcx; push rax",
			symSwitch: "push rbp; push rbx; push rdi; push rsi; push r12; push r13; push r14; push r15; alloc 168; " +
				"xmm6@0; xmm7@16; xmm8@32; xmm9@48; xmm10@64; xmm11@80; xmm12@96; xmm13@112; xmm14@128; xmm15@144",
			symTaskStart:  "alloc 32",
			symCallSystem: "push rbx; push rsi; push rdi; push r12; push r13; frame rbx+0",
			// The machine frame it writes, then the shadow space.
			symTrapCall: "machframe; alloc 32",
		}
		for _, r := range asmRoutines(ll) {
			u, ok := got[r]
			if !ok {
				if r == symTrapCall {
					t.Errorf("%s: no .pdata entry", r)
				}
				continue
			}
			if d := decodeWin64(u); d != want[r] {
				t.Errorf("%s: unwind codes %q, want %q", r, d, want[r])
			}
		}
	}
}

var win64Regs = []string{"rax", "rcx", "rdx", "rbx", "rsp", "rbp", "rsi", "rdi", "r8", "r9", "r10", "r11", "r12", "r13", "r14", "r15"}

// decodeWin64 renders an UNWIND_INFO's codes in prologue order.
func decodeWin64(u []byte) string {
	n := int(u[2])
	var ops []string
	for i := 0; i < n; {
		c := u[4+2*i:]
		op, info := c[1]&15, int(c[1]>>4)
		switch op {
		case 0:
			ops = append(ops, "push "+win64Regs[info])
			i++
		case 1:
			if info == 0 {
				ops = append(ops, fmt.Sprintf("alloc %d", 8*int(binary.LittleEndian.Uint16(u[4+2*i+2:]))))
				i += 2
			} else {
				ops = append(ops, fmt.Sprintf("alloc %d", binary.LittleEndian.Uint32(u[4+2*i+2:])))
				i += 3
			}
		case 2:
			ops = append(ops, fmt.Sprintf("alloc %d", 8*info+8))
			i++
		case 3:
			ops = append(ops, fmt.Sprintf("frame %s+%d", win64Regs[u[3]&15], 16*int(u[3]>>4)))
			i++
		case 8:
			ops = append(ops, fmt.Sprintf("xmm%d@%d", info, 16*int(binary.LittleEndian.Uint16(u[4+2*i+2:]))))
			i += 2
		case 10: // UWOP_PUSH_MACHFRAME (info 0: no error code)
			ops = append(ops, "machframe")
			i++
		default:
			ops = append(ops, fmt.Sprintf("op%d", op))
			i++
		}
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return strings.Join(ops, "; ")
}

// walkHarness is the walk's program, on the support pack: Start makes a
// task (InitContext on @stackT) and switches to it; the task's entry
// calls f1, which runs onSys on the system stack through mk_call_system
// (walk 1, from onSys's frame), then calls f2, which switches back; Start
// then walks the suspended task from its context (walk 2). Each frame's
// function start is printed as a tagged hex line. walkIR is the target's
// walk1 and walk2; marks prints the known functions.
const walkHarness = `
@ctxM = internal global [8 x i64] zeroinitializer, align 16
@ctxT = internal global [8 x i64] zeroinitializer, align 16
@stackT = internal global [65536 x i8] zeroinitializer, align 16
@sysStk = internal global [65536 x i8] zeroinitializer, align 16
@ss = internal global [4 x i64] zeroinitializer, align 16
@hexd = internal constant [16 x i8] c"0123456789abcdef"

declare void @mk_switch(ptr, ptr) nounwind
declare void @mk_call_system(ptr, ptr, ptr) nounwind
declare void @mk_task_start() nounwind
declare void @"mk.support.InitContext"(ptr, ptr, ptr, ptr) nounwind

define internal void @phex(i8 %tag, i64 %v) noinline nounwind uwtable {
  %buf = alloca [19 x i8], align 1
  store i8 %tag, ptr %buf, align 1
  %sp = getelementptr inbounds i8, ptr %buf, i64 1
  store i8 32, ptr %sp, align 1
  %nl = getelementptr inbounds i8, ptr %buf, i64 18
  store i8 10, ptr %nl, align 1
  %i = alloca i64, align 8
  store i64 0, ptr %i, align 8
  br label %loop
loop:
  %iv = load i64, ptr %i, align 8
  %sh0 = sub i64 15, %iv
  %sh = mul i64 %sh0, 4
  %d0 = lshr i64 %v, %sh
  %d = and i64 %d0, 15
  %cp = getelementptr inbounds i8, ptr @hexd, i64 %d
  %c = load i8, ptr %cp, align 1
  %o = add i64 %iv, 2
  %dp = getelementptr inbounds i8, ptr %buf, i64 %o
  store i8 %c, ptr %dp, align 1
  %n = add i64 %iv, 1
  store i64 %n, ptr %i, align 8
  %done = icmp eq i64 %n, 16
  br i1 %done, label %out, label %loop
out:
  call void @h_write(ptr %buf, i64 19)
  ret void
}

define internal void @onSys(ptr %a) noinline nounwind uwtable {
  call void @walk1()
  ret void
}

define internal void @f2() noinline nounwind uwtable {
  call void @mk_switch(ptr @ctxT, ptr @ctxM)
  ret void
}

define internal void @f1() noinline nounwind uwtable {
  call void @mk_call_system(ptr @onSys, ptr null, ptr @ss)
  call void @f2()
  ret void
}

define internal void @taskEntry(ptr %a) noinline nounwind uwtable {
  call void @f1()
  call void @h_exit(i32 9)
  unreachable
}

define void @"mk.std.task.Start"(ptr %sp) noreturn nounwind uwtable {
  call void @phex(i8 84, i64 ptrtoint (ptr @taskEntry to i64))
  call void @phex(i8 65, i64 ptrtoint (ptr @f1 to i64))
  call void @phex(i8 66, i64 ptrtoint (ptr @f2 to i64))
  call void @phex(i8 83, i64 ptrtoint (ptr @onSys to i64))
  call void @phex(i8 67, i64 ptrtoint (ptr @mk_call_system to i64))
  call void @phex(i8 87, i64 ptrtoint (ptr @mk_switch to i64))
  call void @phex(i8 88, i64 ptrtoint (ptr @mk_task_start to i64))
  store ptr getelementptr inbounds (i8, ptr @sysStk, i64 65536), ptr @ss, align 8
  store ptr getelementptr inbounds (i8, ptr @sysStk, i64 65536), ptr getelementptr inbounds (i8, ptr @ss, i64 8), align 8
  store ptr @sysStk, ptr getelementptr inbounds (i8, ptr @ss, i64 16), align 8
  store ptr @sysStk, ptr getelementptr inbounds (i8, ptr @ss, i64 24), align 8
  store ptr getelementptr inbounds (i8, ptr @stackT, i64 65536), ptr getelementptr inbounds (i8, ptr @ctxT, i64 8), align 8
  store ptr @stackT, ptr getelementptr inbounds (i8, ptr @ctxT, i64 16), align 8
  store ptr @stackT, ptr getelementptr inbounds (i8, ptr @ctxT, i64 24), align 8
  call void @"mk.support.InitContext"(ptr @ctxT, ptr getelementptr inbounds (i8, ptr @stackT, i64 65536), ptr @taskEntry, ptr null)
  call void @mk_switch(ptr @ctxM, ptr @ctxT)
  call void @walk2()
  call void @h_exit(i32 42)
  unreachable
}
`

// walkWin64 walks with RtlLookupFunctionEntry and RtlVirtualUnwind from a
// CONTEXT, printing each frame's function start under tag (a frame with no
// entry is a leaf: its return address is at rsp), until the task's entry
// or 32 frames. walk1 captures its own context; walk2 starts at mk_switch's
// first instruction after its prologue, with the suspended task's stack
// pointer, as a debugger reads a suspended context.
const walkWin64 = `
declare void @RtlCaptureContext(ptr) nounwind
declare ptr @RtlLookupFunctionEntry(i64, ptr, ptr) nounwind
declare ptr @RtlVirtualUnwind(i32, i64, i64, ptr, ptr, ptr, ptr, ptr) nounwind

define internal void @walkFrom(ptr %ctx, i8 %tag) noinline nounwind uwtable {
  %base = alloca i64, align 8
  %hd = alloca ptr, align 8
  %est = alloca i64, align 8
  %i = alloca i64, align 8
  store i64 0, ptr %i, align 8
  %ripp = getelementptr inbounds i8, ptr %ctx, i64 248
  %rspp = getelementptr inbounds i8, ptr %ctx, i64 152
  br label %loop
loop:
  %iv = load i64, ptr %i, align 8
  %pc = load i64, ptr %ripp, align 8
  %stop0 = icmp eq i64 %pc, 0
  %many = icmp eq i64 %iv, 32
  %stop1 = or i1 %stop0, %many
  br i1 %stop1, label %out, label %look
look:
  %e = call ptr @RtlLookupFunctionEntry(i64 %pc, ptr %base, ptr null)
  %none = icmp eq ptr %e, null
  br i1 %none, label %leaf, label %entry
leaf:
  call void @phex(i8 %tag, i64 %pc)
  %rsp = load i64, ptr %rspp, align 8
  %rap = inttoptr i64 %rsp to ptr
  %ra = load i64, ptr %rap, align 8
  store i64 %ra, ptr %ripp, align 8
  %rsp2 = add i64 %rsp, 8
  store i64 %rsp2, ptr %rspp, align 8
  br label %next
entry:
  %b = load i64, ptr %base, align 8
  %rva = load i32, ptr %e, align 4
  %rva64 = zext i32 %rva to i64
  %start = add i64 %b, %rva64
  call void @phex(i8 %tag, i64 %start)
  %task = icmp eq i64 %start, ptrtoint (ptr @taskEntry to i64)
  br i1 %task, label %out, label %unw
unw:
  %h = call ptr @RtlVirtualUnwind(i32 0, i64 %b, i64 %pc, ptr %e, ptr %ctx, ptr %hd, ptr %est, ptr null)
  br label %next
next:
  %n = add i64 %iv, 1
  store i64 %n, ptr %i, align 8
  br label %loop
out:
  ret void
}

define internal void @walk1() noinline nounwind uwtable {
  %ctx = alloca [1232 x i8], align 16
  call void @RtlCaptureContext(ptr %ctx)
  call void @walkFrom(ptr %ctx, i8 49)
  ret void
}

define internal void @walk2() noinline nounwind uwtable {
  %ctx = alloca [1232 x i8], align 16
  call void @llvm.memset.p0.i64(ptr %ctx, i8 0, i64 1232, i1 false)
  %base = alloca i64, align 8
  %e = call ptr @RtlLookupFunctionEntry(i64 ptrtoint (ptr @mk_switch to i64), ptr %base, ptr null)
  %b = load i64, ptr %base, align 8
  %uip = getelementptr inbounds i8, ptr %e, i64 8
  %uir = load i32, ptr %uip, align 4
  %ui64 = zext i32 %uir to i64
  %ui = add i64 %b, %ui64
  %psp = add i64 %ui, 1
  %pp = inttoptr i64 %psp to ptr
  %ps = load i8, ptr %pp, align 1
  %ps64 = zext i8 %ps to i64
  %pc = add i64 ptrtoint (ptr @mk_switch to i64), %ps64
  %ripp = getelementptr inbounds i8, ptr %ctx, i64 248
  store i64 %pc, ptr %ripp, align 8
  %sp = load i64, ptr @ctxT, align 8
  %rspp = getelementptr inbounds i8, ptr %ctx, i64 152
  store i64 %sp, ptr %rspp, align 8
  call void @walkFrom(ptr %ctx, i8 50)
  ret void
}

declare void @llvm.memset.p0.i64(ptr, i8, i64, i1)
`

// walkSysV dumps what a walk needs for this file's .eh_frame unwinder:
// walk1 its rip, rsp, rbx and rbp (P, Q, R, U) where it stands, walk2 the
// suspended task's stack pointer (V), then both stacks (M address, then a
// line of hex).
const walkSysV = `
define internal void @dump(ptr %p) noinline nounwind uwtable {
  %pa = ptrtoint ptr %p to i64
  call void @phex(i8 77, i64 %pa)
  %buf = alloca [8193 x i8], align 1
  %i = alloca i64, align 8
  store i64 0, ptr %i, align 8
  br label %chunk
chunk:
  %iv = load i64, ptr %i, align 8
  %j = alloca i64, align 8
  store i64 0, ptr %j, align 8
  br label %byte
byte:
  %jv = load i64, ptr %j, align 8
  %at = add i64 %iv, %jv
  %bp = getelementptr inbounds i8, ptr %p, i64 %at
  %bv = load i8, ptr %bp, align 1
  %bz = zext i8 %bv to i64
  %hi = lshr i64 %bz, 4
  %lo = and i64 %bz, 15
  %hp = getelementptr inbounds i8, ptr @hexd, i64 %hi
  %lp = getelementptr inbounds i8, ptr @hexd, i64 %lo
  %hc = load i8, ptr %hp, align 1
  %lc = load i8, ptr %lp, align 1
  %o = mul i64 %jv, 2
  %o1 = add i64 %o, 1
  %d0 = getelementptr inbounds i8, ptr %buf, i64 %o
  %d1 = getelementptr inbounds i8, ptr %buf, i64 %o1
  store i8 %hc, ptr %d0, align 1
  store i8 %lc, ptr %d1, align 1
  %jn = add i64 %jv, 1
  store i64 %jn, ptr %j, align 8
  %jdone = icmp eq i64 %jn, 4096
  br i1 %jdone, label %flush, label %byte
flush:
  call void @h_write(ptr %buf, i64 8192)
  %in = add i64 %iv, 4096
  store i64 %in, ptr %i, align 8
  %idone = icmp eq i64 %in, 65536
  br i1 %idone, label %out, label %chunk
out:
  %nl = getelementptr inbounds i8, ptr %buf, i64 0
  store i8 10, ptr %nl, align 1
  call void @h_write(ptr %nl, i64 1)
  ret void
}

define internal void @walk1() noinline nounwind uwtable {
  %r = call { i64, i64, i64, i64 } asm sideeffect "leaq 0(%rip), $0\0Amovq %rsp, $1\0Amovq %rbx, $2\0Amovq %rbp, $3", "=r,=r,=r,=r,~{memory}"()
  %rip = extractvalue { i64, i64, i64, i64 } %r, 0
  %rsp = extractvalue { i64, i64, i64, i64 } %r, 1
  %rbx = extractvalue { i64, i64, i64, i64 } %r, 2
  %rbp = extractvalue { i64, i64, i64, i64 } %r, 3
  call void @phex(i8 80, i64 %rip)
  call void @phex(i8 81, i64 %rsp)
  call void @phex(i8 82, i64 %rbx)
  call void @phex(i8 85, i64 %rbp)
  ret void
}

define internal void @walk2() noinline nounwind uwtable {
  %sp = load i64, ptr @ctxT, align 8
  call void @phex(i8 86, i64 %sp)
  call void @dump(ptr @stackT)
  call void @dump(ptr @sysStk)
  ret void
}
`

// walkOut is the harness's printed lines: tag -> values in order, and
// the hex dumps by address.
func walkOut(t *testing.T, out string) (map[byte][]uint64, map[uint64][]byte) {
	t.Helper()
	tags := map[byte][]uint64{}
	mem := map[uint64][]byte{}
	lines := strings.Split(strings.ReplaceAll(out, "\r", ""), "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if len(l) < 3 {
			continue
		}
		v, err := strconv.ParseUint(l[2:], 16, 64)
		if err != nil {
			t.Fatalf("harness line %q", l)
		}
		if l[0] == 'M' && i+1 < len(lines) {
			b, err := hex.DecodeString(lines[i+1])
			if err != nil {
				t.Fatalf("dump at %#x: %v", v, err)
			}
			mem[v] = b
			i++
			continue
		}
		tags[l[0]] = append(tags[l[0]], v)
	}
	return tags, mem
}

// ehWalk unwinds from (pc, regs) through the dumped stacks with exe's
// .eh_frame, returning each frame's FDE start until the task's entry, a
// frame marked outermost, or one it cannot unwind.
func ehWalk(t *testing.T, fdes []fde, mem map[uint64][]byte, pc uint64, regs map[int]uint64, stop uint64) []uint64 {
	t.Helper()
	read := func(a uint64) (uint64, bool) {
		for base, b := range mem {
			if a >= base && a+8 <= base+uint64(len(b)) {
				return binary.LittleEndian.Uint64(b[a-base:]), true
			}
		}
		return 0, false
	}
	var out []uint64
	look := pc
	for i := 0; i < 32; i++ {
		f, ok := fdeFor(fdes, look)
		if !ok {
			out = append(out, pc)
			return out
		}
		out = append(out, f.lo)
		if f.lo == stop {
			return out
		}
		r := f.rowAt(t, look)
		if r.undef {
			return out
		}
		cfa := regs[r.reg] + uint64(r.off)
		next := map[int]uint64{dwRSP: cfa}
		for k, v := range regs {
			if k != dwRSP {
				next[k] = v
			}
		}
		for reg, off := range r.saved {
			if reg == dwRIP {
				continue
			}
			v, ok := read(uint64(int64(cfa) + off))
			if !ok {
				return out
			}
			next[reg] = v
		}
		ra, ok := read(cfa - 8)
		if !ok || ra == 0 {
			return out
		}
		regs, pc, look = next, ra, ra-1
	}
	return out
}

// TestSupportWalk: a walk from code running on the system stack
// through mk_call_system, and one from a task suspended in mk_switch, each
// reach the task's entry, frame by frame.
func TestSupportWalk(t *testing.T) {
	for _, target := range runTargets {
		t.Run(target.Name, func(t *testing.T) {
			walk := walkSysV
			if target.OS == types.OSWindows {
				walk = walkWin64
			}
			exe := buildHarness(t, target, walkHarness+walk, linkOpts{kernel: []string{"RtlCaptureContext", "RtlLookupFunctionEntry", "RtlVirtualUnwind"}})
			out, code := runExe(t, target, exe, "")
			if code != 42 {
				t.Fatalf("exit status %d, want 42\n%.2000s", code, out)
			}
			tags, mem := walkOut(t, out)
			names := map[uint64]string{}
			for tag, n := range map[byte]string{'T': "taskEntry", 'A': "f1", 'B': "f2", 'S': "onSys", 'C': "mk_call_system", 'W': "mk_switch", 'X': "mk_task_start"} {
				if len(tags[tag]) != 1 {
					t.Fatalf("no address of %s in:\n%.2000s", n, out)
				}
				names[tags[tag][0]] = n
			}
			var w1, w2 []uint64
			if target.OS == types.OSWindows {
				w1, w2 = tags['1'], tags['2']
			} else {
				fdes := exeFDEs(t, exe)
				task := tags['T'][0]
				w1 = ehWalk(t, fdes, mem, tags['P'][0], map[int]uint64{dwRSP: tags['Q'][0], dwRBX: tags['R'][0], dwRBP: tags['U'][0]}, task)
				sw, ok := fdeFor(fdes, tags['W'][0])
				if !ok {
					t.Fatal("mk_switch has no FDE")
				}
				// The first address in mk_switch's body: CFA at rsp + 64.
				body := sw.lo
				for ; body < sw.hi; body++ {
					if r := sw.rowAt(t, body); r.reg == dwRSP && r.off == 64 {
						break
					}
				}
				w2 = ehWalk(t, fdes, mem, body, map[int]uint64{dwRSP: tags['V'][0]}, task)
			}
			show := func(w []uint64) string {
				var s []string
				for _, a := range w {
					if n, ok := names[a]; ok {
						s = append(s, n)
					} else {
						s = append(s, fmt.Sprintf("%#x", a))
					}
				}
				return strings.Join(s, " > ")
			}
			for _, c := range []struct {
				walk []uint64
				want string
			}{{w1, "onSys > mk_call_system > f1 > taskEntry"}, {w2, "mk_switch > f2 > f1 > taskEntry"}} {
				if got := show(c.walk); !strings.HasSuffix(got, c.want) {
					t.Errorf("walk %s, want it to end %s", got, c.want)
				}
			}
		})
	}
}

// exeFDEs reads a linked ELF executable's .eh_frame (pc_begin pc-relative).
func exeFDEs(t *testing.T, exe string) []fde {
	t.Helper()
	f, err := elf.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := f.Section(".eh_frame")
	if s == nil {
		t.Fatal("no .eh_frame in the executable")
	}
	b, _ := s.Data()
	fdes := parseEhFrame(t, b, func(off int, raw int32) uint64 { return uint64(int64(s.Addr) + int64(off) + int64(raw)) })
	sort.Slice(fdes, func(i, j int) bool { return fdes[i].lo < fdes[j].lo })
	return fdes
}

// TestSupportAttrs: the support pack's IR functions carry
// "frame-pointer"="non-leaf" under Config.FramePointers and minsize and
// optsize under Config.Size, as Mk functions do, and none of them otherwise.
func TestSupportAttrs(t *testing.T) {
	for _, target := range []types.Target{types.X86_64Linux, types.X86_64Windows} {
		emit := func(cfg Config) string {
			var b bytes.Buffer
			if err := EmitSupport(&Unit{}, nil, cfg, &b); err != nil {
				t.Fatal(err)
			}
			return b.String()
		}
		on := fnText(emit(Config{Target: target, FramePointers: true, Size: true}), symMemcpy)
		off := fnText(emit(Config{Target: target}), symMemcpy)
		for _, want := range []string{`"frame-pointer"="non-leaf"`, "minsize", "optsize", "uwtable"} {
			if !strings.Contains(on, want) {
				t.Errorf("%s: memcpy has no %s with the settings on:\n%.300s", target.Name, want, on)
			}
			if want != "uwtable" && strings.Contains(off, want) {
				t.Errorf("%s: memcpy has %s with the settings off", target.Name, want)
			}
		}
	}
}
