// Why: the arena and heap test blocks in std.core (core/tests_arena.mk,
// core/tests_arena_fuzz.mk, core/tests_heap.mk) must judge leaks and
// panics, and be seen to.
// What: runs those blocks with mk test --std=std.core, and planted bugs
// (a leak, a Reset that frees nothing, an OwnerCheck that never panics).
// How: the counting heap fails a leaking block with exit 102; "[exit N: text]"
// and "[env K=V ...]" name tags follow internal/driver's stdtests_test.go.

package std

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// memTestFiles are the arena and heap test-block files in std.core.
var memTestFiles = []string{"core/tests_arena.mk", "core/tests_arena_fuzz.mk", "core/tests_heap.mk"}

// memLeakBlocks plant the one leak a user program can write with these
// types (an accepted cost): a Box moved into an arena, which Arena.Free
// does not release. The twin without the Box must pass, so the leak verdict
// is about the Box, not about the arena's own chunks.
const memLeakBlocks = `test "arena alone" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    _ = try ar.Slice[u8](3_000_000)
}

test "box left in an arena" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    b := try Box[int].New(sys.heap.Alloc(), 1)
    _ = ar.Put(b) else {
        b.Free(sys.heap.Alloc())
        return
    }
}
`

// mkTestPackage writes src as the one file of a package and runs mk test
// over today's std, with fixtures written over it, and extra flags. It
// skips when mk has no test runner yet or no toolchain.
func mkTestPackage(t *testing.T, fixtures map[string]string, src string, flags ...string) (string, int) {
	t.Helper()
	root := t.TempDir()
	stdDir := copyStd(t, root, fixtures)
	pkg := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "main.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"test"}, flags...)
	args = append(args, ".")
	out, status := mkCmd(t, pkg, stdDir, args...)
	if strings.Contains(out, "no usable LLVM") {
		t.Skipf("needs the LLVM toolchain: %s", out)
	}
	return out, status
}

// testNames lists the names of the test blocks in src, in order.
func testNames(src string) []string {
	var names []string
	for _, line := range strings.Split(src, "\n") {
		if rest, ok := strings.CutPrefix(line, `test "`); ok {
			if i := strings.IndexByte(rest, '"'); i >= 0 {
				names = append(names, rest[:i])
			}
		}
	}
	return names
}

// stdTestLine is one result line of mk test: PASS or FAIL, the module, the
// name, and for a failure its reason.
var stdTestLine = regexp.MustCompile(`^(PASS|FAIL) (\S+) "(.*)"(?:: (.*))?$`)

// memExpect is a block's expected end, "[exit N: text]" at the end of its
// name; memEnv its environment, "[env K=V ...]".
var (
	memExpect = regexp.MustCompile(`\[exit ([0-9]+): ([^\]]*)\]$`)
	memEnv    = regexp.MustCompile(`\[env ([^\]]*)\]`)
)

// stdTestResult is one block's outcome as mk test printed it.
type stdTestResult struct {
	pass   bool
	reason string // "panic (exit 101)", "leak (exit 102)", ...
	output string // the indented lines after a FAIL line
}

// parseStdTest reads mk test's result lines for module mod.
func parseStdTest(out, mod string) map[string]stdTestResult {
	res := map[string]stdTestResult{}
	var last string
	for _, line := range strings.Split(out, "\n") {
		if m := stdTestLine.FindStringSubmatch(line); m != nil {
			last = ""
			if m[2] != mod {
				continue
			}
			last = m[3]
			res[last] = stdTestResult{pass: m[1] == "PASS", reason: m[4]}
			continue
		}
		if last != "" && strings.HasPrefix(line, "    ") {
			r := res[last]
			r.output += line + "\n"
			res[last] = r
		}
	}
	return res
}

// mkTestStd runs mk test --std=std.core over a copy of std with fixtures
// written over it, with extra flags and environment; it skips without a
// toolchain.
func mkTestStd(t *testing.T, fixtures map[string]string, env []string, flags ...string) (string, int) {
	t.Helper()
	root := t.TempDir()
	stdDir := copyStd(t, root, fixtures)
	ctx, cancel := context.WithTimeout(context.Background(), mkTimeout)
	defer cancel()
	args := append([]string{"test", "--std=std.core"}, flags...)
	cmd := exec.CommandContext(ctx, mkExe(t), args...)
	cmd.Dir = root
	cmd.Env = append(append(os.Environ(), "MK_STD="+stdDir), env...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := strings.ReplaceAll(buf.String(), "\r\n", "\n")
	if ctx.Err() != nil {
		t.Fatalf("mk %s: killed after %s\n%s", strings.Join(args, " "), mkTimeout, out)
	}
	status := 0
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "no usable LLVM") {
		t.Skipf("needs the LLVM toolchain: %s", out)
	}
	return out, status
}

// diagFiles lists the std files that mk's diagnostics in out point at, in
// order of first appearance.
func diagFiles(out string) []string {
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if !diagLine.MatchString(line) {
			continue
		}
		f, _, _ := strings.Cut(line, ":")
		if !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	return files
}

// judgeMemBlock fails t unless block name ended as its name asks.
func judgeMemBlock(t *testing.T, name string, r stdTestResult, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("%q: no result line", name)
		return
	}
	if m := memExpect.FindStringSubmatch(name); m != nil {
		want := "(exit " + m[1] + ")"
		if r.pass || !strings.HasSuffix(r.reason, want) || !strings.Contains(r.output, m[2]) {
			t.Errorf("%q: pass %v, %q, want a failure %s with %q in\n%s", name, r.pass, r.reason, want, m[2], r.output)
		}
		return
	}
	if !r.pass {
		t.Errorf("%q: %s\n%s", name, r.reason, r.output)
	}
}

// TestStdMem_MkTestBlocks: every block in memTestFiles ends as its name asks,
// at dev and at release; a block with an [env] tag runs again alone with
// that environment (MK_CARRIERS=1: the same tasks on one carrier).
func TestStdMem_MkTestBlocks(t *testing.T) {
	t.Parallel()
	var names []string
	for _, f := range memTestFiles {
		names = append(names, testNames(readText(t, f))...)
	}
	if len(names) < 30 {
		t.Fatalf("found %d test blocks in %v, want at least 30", len(names), memTestFiles)
	}
	panics := 0
	for _, prof := range [][]string{nil, {"--release"}} {
		out, _ := mkTestStd(t, nil, nil, prof...)
		res := parseStdTest(out, "std.core")
		if len(res) == 0 {
			// No block ran: std.core did not build. Name the files the
			// diagnostics are in, so a failure in another module's
			// tests_*.mk is not read as one of memTestFiles.
			t.Fatalf("mk test --std=std.core %v: no result lines; diagnostics in %v\n%s", prof, diagFiles(out), out)
		}
		for _, n := range names {
			r, ok := res[n]
			judgeMemBlock(t, n, r, ok)
			if prof == nil && memExpect.MatchString(n) {
				panics++
			}
			m := memEnv.FindStringSubmatch(n)
			if m == nil {
				continue
			}
			one, _ := mkTestStd(t, nil, strings.Fields(m[1]), append(prof, "-run", n)...)
			r1, ok1 := parseStdTest(one, "std.core")[n]
			judgeMemBlock(t, n+" "+strconv.Quote(m[1]), r1, ok1)
		}
	}
	if panics < 8 {
		t.Errorf("%d blocks expect a panic, want at least 8 (the owner check and the linear frees)", panics)
	}
}

func TestStdMem_MkTestSeesArenaLeak(t *testing.T) {
	t.Parallel()
	out, status := mkTestPackage(t, nil, memLeakBlocks)
	if status != 1 {
		t.Fatalf("exit %d, want 1\n%s", status, out)
	}
	if !strings.Contains(out, `PASS main "arena alone"`) {
		t.Errorf("the arena alone leaks or fails\n%s", out)
	}
	if !strings.Contains(out, `FAIL main "box left in an arena": leak (exit 102)`) {
		t.Errorf("the Box left in an arena is not reported as a leak\n%s", out)
	}
}

// TestStdMem_MkTestSeesResetLeak plants a Reset that drops the chunks it
// should return (the list is cut, nothing is freed): the "Reset reuses
// memory" block, which passes over today's mem_arena.mk, must then fail as a leak.
// So the blocks' "nothing live" verdict covers Reset's frees, not only
// Free's.
func TestStdMem_MkTestSeesResetLeak(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("core/mem_arena.mk")
	if err != nil {
		t.Fatal(err)
	}
	const free = "                FreeBytes(a, raw.PtrCast[Chunk, u8](c), size, ChunkSrc(h))\n"
	text := string(src)
	i := strings.Index(text, "export Arena.Reset(")
	j := -1
	if i >= 0 {
		j = strings.Index(text[i:], free)
	}
	if j < 0 {
		t.Fatal("mem_arena.mk: Reset's FreeBytes line not found; update the plant")
	}
	// size is used only by the dropped call, so the plant uses it (E0208),
	// and a with it, which costs nothing.
	planted := text[:i+j] + "                _ = size\n                _ = a\n" + text[i+j+len(free):]
	const name = "Reset reuses memory and returns the rest"
	if !strings.Contains(readText(t, "core/tests_arena.mk"), `test "`+name+`"`) {
		t.Fatalf("core/tests_arena.mk: no block %q; update the test", name)
	}
	out, _ := mkTestStd(t, map[string]string{"core/mem_arena.mk": planted}, nil, "-run", name)
	r, ok := parseStdTest(out, "std.core")[name]
	if !ok || r.pass || r.reason != "leak (exit 102)" {
		t.Errorf("the planted Reset is not reported as a leak: %+v\n%s", r, out)
	}
	// And the same block passes over today's Reset, so the leak is the plant's.
	out, _ = mkTestStd(t, nil, nil, "-run", name)
	if r := parseStdTest(out, "std.core")[name]; !r.pass {
		t.Errorf("%q fails over today's mem_arena.mk: %+v\n%s", name, r, out)
	}
}

// TestStdMem_MkTestSeesOwnerCheckRemoved plants an OwnerCheck that never
// panics: every "wrong allocator" block must then end with no panic, so the
// [exit 101: ...] verdicts are about OwnerCheck.
func TestStdMem_MkTestSeesOwnerCheckRemoved(t *testing.T) {
	t.Parallel()
	src := readText(t, "core/mem_box.mk")
	planted := strings.Replace(src, "        raw.Fail(12, op)\n", "        _ = op\n", 1)
	if planted == src {
		t.Fatal("core/mem_box.mk: OwnerCheck's raw.Fail(12, op) not found; update the plant")
	}
	out, _ := mkTestStd(t, map[string]string{"core/mem_box.mk": planted}, nil, "-run", "with the wrong allocator")
	res := parseStdTest(out, "std.core")
	if len(res) < 7 {
		t.Fatalf("-run %q selected %d blocks, want at least 7\n%s", "with the wrong allocator", len(res), out)
	}
	// With the check gone a block may still end in another panic: the
	// HeapMap and TextMap blocks grow their table in the arena, then free
	// those arrays into the heap (the defer), which is exactly what
	// OwnerCheck stops, and the heap's page count then overflows. So a
	// block either ends with no panic (a leak report is not one), or with
	// a panic that is not OwnerCheck's and says what it is.
	for name, r := range res {
		if strings.Contains(r.output, "wrong allocator") || (strings.Contains(r.reason, "exit 101") && !strings.Contains(r.output, "panic: ")) {
			t.Errorf("%q still panics with the owner check planted away: %+v", name, r)
		}
	}
}
