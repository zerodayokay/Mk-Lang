// Why: std.mem's public API is fixed by the reference's std.mem lines, and
// the loans those lines promise keep allocations from aliasing.
// What: TestSTDMEM_APIMatchesReference and the loan tests of Fixed (its
// buffer, exclusively), Reset and Counting (its inner allocator).
// How: parseReferenceAPI reads the reference; mk check runs small programs.

package std

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// memPending are the reference's std.mem lines another module declares, by
// their receiver: Pool[T] and its Handle (std/mem/pool.mk). A
// pending line std/mem does not declare yet is logged; once it is declared
// it is judged as every other line, and the test says to drop the entry.
var memPending = map[string]string{"Pool": "std.core"}

// memSection is the reference's std.mem items, as parseReferenceAPI routes
// them (every item of the "// std.mem" section lands in std.mem).
func memSection(t *testing.T) []apiItem {
	t.Helper()
	var out []apiItem
	for _, it := range parseReferenceAPI(t) {
		if it.inSect == "std.mem" {
			if it.mod != "std.mem" {
				t.Errorf("std.mem line %q routes to %s", it.text, it.mod)
			}
			out = append(out, it)
		}
	}
	return out
}

// memTypeNames are the types std/mem may export: the capitalized names of
// the reference's std.mem lines that are not prelude names or type parameters
// (Fixed, Counting, Pool, Handle).
func memTypeNames(items []apiItem) map[string]bool {
	prelude := map[string]bool{}
	for _, n := range PreludeNames {
		prelude[n] = true
	}
	names := map[string]bool{}
	ident := regexp.MustCompile(`\b[A-Z][A-Za-z0-9]*\b`)
	for _, it := range items {
		for _, n := range ident.FindAllString(it.text, -1) {
			if !prelude[n] && len(n) > 1 {
				names[n] = true
			}
		}
	}
	return names
}

// TestSTDMEM_APIMatchesReference: every line of the reference's std.mem
// section is declared in std/mem exactly as written (Pool's lines, while
// pending, are logged), and std/mem exports nothing else: no function
// without a line (TestNoUnlistedPublicNames's module list does not cover std.mem), no type
// the lines do not name, no exported field. A renamed Counting.Peak fails it
// (planted by hand).
func TestSTDMEM_APIMatchesReference(t *testing.T) {
	items := memSection(t)
	if len(items) < 9 {
		t.Fatalf("the reference has %d std.mem lines, want at least the 9 of Fixed, Counting and Null", len(items))
	}
	decls := moduleDecls(t, "std.mem")
	if len(decls) == 0 {
		t.Fatal("std/mem declares nothing")
	}
	want := map[string]string{} // normalized line -> receiver
	for _, it := range items {
		recv, _ := itemHead(it.text)
		if k := strings.IndexByte(recv, '['); k >= 0 {
			recv = recv[:k]
		}
		line := normalizeFor("std.mem", it.text, nil)
		if it.block {
			line = strings.TrimSuffix(line, " {")
		}
		want[line] = recv
	}
	types := memTypeNames(items)
	have := map[string]bool{}
	for _, d := range decls {
		have[d.text] = true
		if blockKind(d.text) {
			name := blockName(d.text)
			if !types[name] {
				t.Errorf("std/mem exports type %s, which no std.mem line of the reference names (%s)", name, d.file)
			}
			for _, b := range d.body {
				if strings.HasPrefix(b, "export ") {
					t.Errorf("std/mem: %s has an exported field %q, which the reference does not list", name, b)
				}
			}
			continue
		}
		if _, ok := want[d.text]; !ok {
			t.Errorf("std/mem exports %q, which is no std.mem line of the reference (%s)", d.text, d.file)
		}
	}
	for w, recv := range want {
		lane, pending := memPending[recv]
		switch {
		case !have[w] && pending:
			t.Logf("pending (%s): %s", lane, w)
		case !have[w]:
			t.Errorf("std.mem line of the reference not declared in std/mem: %s", w)
		case pending:
			t.Logf("now declared, drop %s from memPending: %s", recv, w)
		}
	}
}

// memLoanCase is one function of the loan probe and the codes its marked
// line may get (nil: the function must check clean).
type memLoanCase struct {
	name string
	fn   string // the marked line ends with // <-
	want []string
}

var memLoanCases = []memLoanCase{
	{"fixed-holds-buf", `A() {
    var buf: [64]u8 = [0; 64]
    f := mem.Fixed.New(mut buf[..])
    buf[0] = 1 // <-
    _ = f.Alloc()
}`, []string{"E0502"}},
	// The Fixed's loan on buf is exclusive, so a read of buf
	// is refused too (a shared loan would refuse only the write), which is
	// what keeps a view of buf from aliasing a Box.Mut into the same bytes.
	{"fixed-excludes-read", `A2(): u8 {
    var buf: [64]u8 = [0; 64]
    f := mem.Fixed.New(mut buf[..])
    x := buf[0] // <-
    _ = f.Alloc()
    return x
}`, []string{"E0504"}},
	{"fixed-excludes-view", `A3(): int {
    var buf: [64]u8 = [0; 64]
    f := mem.Fixed.New(mut buf[..])
    s := buf[..] // <-
    _ = f.Alloc()
    return s.Len()
}`, []string{"E0504"}},
	{"fixed-excludes-read-box", `A4(): int {
    var buf: [64]u8 = [0; 64]
    f := mem.Fixed.New(mut buf[..])
    b := Box[int].New(f.Alloc(), 1) else {
        return 0
    }
    x := buf[0] // <-
    b.Free(f.Alloc())
    return Cast[int](x)
}`, []string{"E0504"}},
	{"fixed-read-after", `A5(): int {
    var buf: [64]u8 = [0; 64]
    f := mem.Fixed.New(mut buf[..])
    b := Box[int].New(f.Alloc(), 1) else {
        return 0
    }
    b.Free(f.Alloc())
    s := buf[..]
    x := buf[0]
    return s.Len() + Cast[int](x)
}`, nil},
	{"fixed-buf-after", `B() {
    var buf: [64]u8 = [0; 64]
    f := mem.Fixed.New(mut buf[..])
    _ = f.Alloc()
    buf[0] = 1
}`, nil},
	{"reset-ends-box", `C() {
    var buf: [64]u8 = [0; 64]
    var f := mem.Fixed.New(mut buf[..])
    b := Box[int].New(f.Alloc(), 1) else {
        return
    }
    f.Reset() // <-
    b.Free(f.Alloc())
}`, []string{"E0501"}},
	{"reset-ends-heaplist", `C2() {
    var buf: [64]u8 = [0; 64]
    var f := mem.Fixed.New(mut buf[..])
    var xs := HeapList[int].New(f.Alloc())
    f.Reset() // <-
    xs.Free(f.Alloc())
}`, []string{"E0501"}},
	{"reset-after-free", `D() {
    var buf: [64]u8 = [0; 64]
    var f := mem.Fixed.New(mut buf[..])
    b := Box[int].New(f.Alloc(), 1) else {
        return
    }
    b.Free(f.Alloc())
    f.Reset()
    _ = f.Alloc()
}`, nil},
	{"counting-holds-inner", `E(h: &Heap): int {
    var ar := Arena.New(h) else {
        return 0
    }
    c := mem.Counting.New(ar.Alloc())
    ar.Free() // <-
    return c.Live()
}`, []string{"E0503"}},
	{"fixed-not-share", `F(x: &mem.Fixed): int {
    return G(x) // <-
}

G[T: Share](x: &T): int {
    return 0
}`, []string{"E0701"}},
	{"counting-not-share", `H(x: &mem.Counting): int {
    return G2(x) // <-
}

G2[T: Share](x: &T): int {
    return 0
}`, []string{"E0701"}},
}

// TestSTDMEM_Loans: each probe function is checked alone (an error in one
// must not hide another's); a marked line gets one of its codes and no
// other line any code, an unmarked function checks clean.
func TestSTDMEM_Loans(t *testing.T) {
	t.Parallel()
	for _, c := range memLoanCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src := "import std.mem\n\n" + c.fn + "\n\nexport Main(sys: &System) {\n}\n"
			mark := 0
			for i, l := range strings.Split(src, "\n") {
				if strings.HasSuffix(l, "// <-") {
					mark = i + 1
				}
			}
			out, code := memCheck(t, src)
			got := diagCodes(out)
			if c.want == nil {
				if code != 0 || len(got) != 0 {
					t.Errorf("want clean, exit %d\n%s", code, out)
				}
				return
			}
			ok := false
			for _, lc := range got {
				var line int
				var dcode string
				fmt.Sscanf(lc, "%d %s", &line, &dcode)
				if line != mark {
					t.Errorf("unexpected %s at main.mk:%d\n%s", dcode, line, out)
					continue
				}
				for _, w := range c.want {
					ok = ok || dcode == w
				}
			}
			if !ok {
				t.Errorf("main.mk:%d: want one of %v, have %v\n%s", mark, c.want, got, out)
			}
		})
	}
}

// memPlantBlocks are written into the copy of std/mem beside tests_mem.mk:
// the leak report pattern must see a leak (the Assert fails, exit 101), and
// a Box left live through a Counting over sys.heap must reach the test
// heap's own leak check (exit 102), so Counting forwards to the heap it
// wraps and the clean blocks' verdicts mean something.
const memPlantBlocks = `test "plant: Live sees a leak" (sys: &System) {
    c := Counting.New(sys.heap.Alloc())
    b := try Box[int].New(c.Alloc(), 1)
    Assert(c.Live() == 0)
    b.Free(c.Alloc())
}

test "plant: the test heap sees a leak through Counting" (sys: &System) {
    c := Counting.New(sys.heap.Alloc())
    b := try Box[int].New(c.Alloc(), 1)
    unsafe "a planted leak" {
        raw.Forget(b)
    }
}
`

// TestSTDMEM_TestBlocks: mk test --std=std.mem passes every block of
// std/mem/tests_mem.mk, dev and release, and the two planted blocks fail
// with exit 101 and 102.
func TestSTDMEM_TestBlocks(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile(filepath.Join("mem", "tests_mem.mk"))
	if err != nil {
		t.Fatal(err)
	}
	names := testNames(string(src))
	if len(names) < 12 {
		t.Fatalf("tests_mem.mk has %d blocks, want at least 12", len(names))
	}
	for _, flags := range [][]string{nil, {"--release"}} {
		root := t.TempDir()
		stdDir := memCopyStd(t, root)
		plant := "import std.raw\n\n" + memPlantBlocks
		if err := os.WriteFile(filepath.Join(stdDir, "mem", "tests_zz_plant.mk"), []byte(plant), 0o644); err != nil {
			t.Fatal(err)
		}
		out, _ := mkCmd(t, root, stdDir, append([]string{"test", "--std=std.mem"}, flags...)...)
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		res := parseStdTest(out, "std.mem")
		for _, n := range names {
			if r, ok := res[n]; !ok || !r.pass {
				t.Errorf("%v: block %q: %+v\n%s", flags, n, r, out)
			}
		}
		for name, reason := range map[string]string{
			"plant: Live sees a leak":                           "exit 101",
			"plant: the test heap sees a leak through Counting": "exit 102",
		} {
			if r := res[name]; r.pass || !strings.Contains(r.reason, reason) {
				t.Errorf("%v: planted block %q: want a failure with %s, have %+v\n%s", flags, name, reason, r, out)
			}
		}
	}
}
