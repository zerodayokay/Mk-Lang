// Why: HeapList[T].Extend and List[T].Extend must copy their new elements
// with one Slice.CopyFrom (one raw.Move), not an element loop with a check on
// each element.
// What: TestExtend_OneCopyFrom, with extendProgram, extendBodies and
// insnCycle.
// How: reads each Extend's body for one CopyFrom and no loop, then
// disassembles a dev build for x86_64-linux and checks every Extend instance
// has no cycle.

package std

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// extendProgram extends a HeapList and a List of bytes, of words and of a
// plain struct, so a dev build has three instances of each Extend.
const extendProgram = `struct P {
    k: u32
    v: u16
}

export Main(sys: &System): OutOfMemory!void {
    a := sys.heap.Alloc()
    var b := HeapList[u8].New(a)
    defer b.Free(a)
    try b.Extend(a, "hello".AsBytes())
    var w := HeapList[u64].New(a)
    defer w.Free(a)
    xs: [3]u64 = [1, 2, 3]
    try w.Extend(a, xs[..])
    var s := HeapList[P].New(a)
    defer s.Free(a)
    ps: [2]P = [P(k: 1, v: 2), P(k: 3, v: 4)]
    try s.Extend(a, ps[..])
    var lb := List[u8].New(sys.arena)
    try lb.Extend("hello".AsBytes())
    var lw := List[u64].New(sys.arena)
    try lw.Extend(xs[..])
    var ls := List[P].New(sys.arena)
    try ls.Extend(ps[..])
}
`

// extendBodies are the Extend methods the test reads and disassembles: the
// file and head of each, and its symbols' prefix in an ELF.
var extendBodies = []struct{ file, head, sym string }{
	{"coll_heap.mk", "export HeapList[T].Extend(", "mk.std.core.HeapList:Extend"},
	{"coll_list.mk", "export List[T].Extend(", "mk.std.core.List:Extend"},
}

// extendLoopRe matches a for statement in Mk text.
var extendLoopRe = regexp.MustCompile(`(?m)^\s*for\s`)

// insnCycle reports whether fn's instructions (in address order) hold a
// cycle: an edge goes to the next instruction unless the instruction is an
// unconditional jump, a return, a trap or a call of a panic (none returns),
// and a jump also goes to its target inside fn. A dev build lays a try's
// join below its call, so a backward jump alone is not a loop.
func insnCycle(ins []ctInsn) bool {
	at := map[uint64]int{}
	for i, x := range ins {
		at[x.addr] = i
	}
	next := func(i int) []int {
		x := ins[i]
		var out []int
		ends := x.op == "jmp" || x.op == "jmpq" || strings.HasPrefix(x.op, "ret") || x.op == "ud2" ||
			(strings.HasPrefix(x.op, "call") && strings.Contains(x.args, "Panic"))
		if !ends && i+1 < len(ins) {
			out = append(out, i+1)
		}
		if strings.HasPrefix(x.op, "j") {
			if f := strings.Fields(x.args); len(f) > 0 {
				if to, err := strconv.ParseUint(strings.TrimPrefix(f[0], "0x"), 16, 64); err == nil {
					if j, ok := at[to]; ok {
						out = append(out, j)
					}
				}
			}
		}
		return out
	}
	state := make([]int, len(ins)) // 0 unseen, 1 on the path, 2 done
	var visit func(i int) bool
	visit = func(i int) bool {
		state[i] = 1
		for _, j := range next(i) {
			if state[j] == 1 || (state[j] == 0 && visit(j)) {
				return true
			}
		}
		state[i] = 2
		return false
	}
	return len(ins) > 0 && visit(0)
}

// TestExtend_OneCopyFrom: each Extend's body holds one CopyFrom of xs and
// no loop; and in a dev build for x86_64-linux (the ELF keeps the names)
// every Extend instance calls Slice.CopyFrom and has no cycle, while every
// CopyFrom instance calls memmove (TestCore_BytePathsCallSimd checks it has
// no back edge).
func TestExtend_OneCopyFrom(t *testing.T) {
	t.Parallel()
	for _, e := range extendBodies {
		body := core11Body(t, e.file, e.head)
		if !strings.Contains(body, ".CopyFrom(xs)") || extendLoopRe.MatchString(body) {
			t.Errorf("std/core/%s: Extend is not one CopyFrom of xs with no loop\n%s", e.file, body)
		}
	}
	objdump := cryptoObjdump()
	if objdump == "" {
		t.Skip("needs llvm-objdump (LLVM 21)")
	}
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(extendProgram), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "main.elf")
	out, st := mkCmd(t, root, stdDir, "build", "--target", "x86_64-linux", "-o", exe, "main.mk")
	if st != 0 {
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		t.Fatalf("mk build --target x86_64-linux: exit %d\n%s", st, out)
	}
	dis, err := exec.Command(objdump, "-d", "--no-show-raw-insn", exe).Output()
	if err != nil {
		t.Fatalf("%s -d: %v", objdump, err)
	}
	byFn := map[string][]ctInsn{}
	var order []string
	for _, x := range ctParse(string(dis)) {
		if _, ok := byFn[x.fn]; !ok {
			order = append(order, x.fn)
		}
		byFn[x.fn] = append(byFn[x.fn], x)
	}
	calls := func(fn, callee string) bool {
		return slices.ContainsFunc(byFn[fn], func(x ctInsn) bool {
			return strings.HasPrefix(x.op, "call") && strings.Contains(x.args, callee)
		})
	}
	extends := make([]int, len(extendBodies))
	copies := 0
	for _, fn := range order {
		if strings.HasPrefix(fn, "mk.std.core.Slice:CopyFrom") {
			copies++
			if !calls(fn, "<memmove>") {
				t.Errorf("%s calls no memmove", fn)
			}
		}
		for i, e := range extendBodies {
			if !strings.HasPrefix(fn, e.sym) {
				continue
			}
			extends[i]++
			if !calls(fn, "<mk.std.core.Slice:CopyFrom") {
				t.Errorf("%s calls no Slice.CopyFrom", fn)
			}
			if insnCycle(byFn[fn]) {
				t.Errorf("%s has a loop", fn)
			}
		}
	}
	for i, e := range extendBodies {
		if extends[i] < 3 {
			t.Errorf("%d %s instances, want those of u8, u64 and P", extends[i], e.sym)
		}
	}
	if copies < 3 {
		t.Errorf("%d CopyFrom instances, want at least Extend's three", copies)
	}
}
