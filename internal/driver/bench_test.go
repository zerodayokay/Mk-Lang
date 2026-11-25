// Why: mk bench finds benchmarks by scanning source text and calibrates n,
// and both went wrong on unusual bodies.
// What: tests of a body that ignores n, and of a call of testing.Bench
// mentioned only in a comment or a string.
// How: runs mk bench on a small package and checks the calibration bound,
// the stable zero median and which functions are selected; codeOnly, the
// text scan, is tested on its own over strings and comments.

package driver

import (
	"strings"
	"testing"
	"time"
)

const benchEdgeSource = `import std.testing

Count(n: int) {
    var s := 0
    for i in 0..n {
        s += i & 3
    }
    Assert(s >= 0)
}

Empty(n: int) {
    _ = n
}

test "empty" {
    testing.Bench(Empty)
}

test "commented" {
    // testing.Bench(Count) is only mentioned here
    x := "testing.Bench(Count)"
    Assert(x.Len() > 0)
}
`

// TestBenchEmptyBodyAndMentions: a benchmark whose body ignores n stops
// calibrating at 2^30, its zero median is stable, and it passes; a test
// that only mentions testing.Bench( is no benchmark. The failure it guards
// against: the empty body calibrates to 2^40, whose spec Decimal cuts, and
// is reported "not a benchmark"; the commented test is selected and fails.
func TestBenchEmptyBodyAndMentions(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": benchEdgeSource})
	rep := mkTest(t, Config{Root: root}, TestOptions{Bench: true, BenchLimit: 3 * time.Second})
	if len(rep.Tests) != 1 || rep.Tests[0].Name != "empty" || rep.Tests[0].Result != "pass" {
		t.Fatalf("mk bench: %+v", rep.Tests)
	}
	if b := rep.Tests[0].Bench; b == nil || b.N > 1<<30 || b.Reps < BenchBatch {
		t.Errorf("statistics %+v", b)
	}
	if !stableCI(0, 0, 0, BenchTolerance) {
		t.Error("a zero median is not stable")
	}
}

func TestBenchCodeOnly(t *testing.T) {
	src := "a(\"x // y\") // testing.Bench(\nb'\\'' c \"testing.Bench(\"\n"
	got := codeOnly(src, false)
	if len(got) != len(src) || strings.Contains(got, "testing.Bench(") || !strings.Contains(got, "a(") || !strings.Contains(got, " c ") {
		t.Errorf("codeOnly = %q", got)
	}
	if kept := codeOnly(src, true); !strings.Contains(kept, `"x // y"`) || strings.Contains(kept, "// testing") {
		t.Errorf("codeOnly(keepStrings) = %q", kept)
	}
	// A """ body is text, and a raw string ending in \ ends at
	// its quote, so the call after it is code.
	multi := "s := \"\"\"\n    testing.Bench(Empty) in text\n    \"\"\"\n"
	if got := codeOnly(multi, false); strings.Contains(got, "testing.Bench(") || strings.Count(got, "\n") != 3 {
		t.Errorf("codeOnly over a \"\"\" string = %q", got)
	}
	raw := "p := r\"C:\\dir\\\"; testing.Bench(Count)\n"
	if got := codeOnly(raw, false); !strings.Contains(got, "testing.Bench(Count)") {
		t.Errorf("codeOnly after a raw string ending in a backslash = %q", got)
	}
}

// benchSysSource is an allocating benchmark: Run builds a list in an arena
// off sys.heap on every call, which only BenchSys's System makes possible.
const benchSysSource = `import std.testing

Sum(sys: &System, n: int): OutOfMemory!int {
    var a := try Arena.New(sys.heap)
    defer a.Free()
    var xs := List[int].New(&a)
    for i in 0..n {
        try xs.Push(i)
    }
    var s := 0
    for x in xs {
        s += x
    }
    return s
}

Run(sys: &System, n: int) {
    Assert(Sum(sys, n).Must() == n * (n - 1) / 2)
}

test "alloc" (sys: &System) {
    testing.BenchSys(sys, Run)
}

test "plain" {
    Assert(true)
}
`

// TestBenchSys: a benchmark that allocates from the test block's System
// passes under plain mk test, which calls f(sys, 1), and under mk bench,
// which selects only it and times f(sys, n) at a calibrated n. The failure
// it guards against: testing.Bench takes fn(n: int), so such a benchmark
// does not compile, and a BenchSys call mk bench's text scan does not
// find is never run.
func TestBenchSys(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": benchSysSource})
	rep := mkTest(t, Config{Root: root}, TestOptions{})
	if len(rep.Tests) != 2 || rep.Tests[0].Result != "pass" || rep.Tests[1].Result != "pass" {
		t.Fatalf("mk test: %+v", rep.Tests)
	}
	rep = mkTest(t, Config{Root: root}, TestOptions{Bench: true, BenchLimit: 3 * time.Second})
	if len(rep.Tests) != 1 || rep.Tests[0].Name != "alloc" || rep.Tests[0].Result != "pass" {
		t.Fatalf("mk bench: %+v", rep.Tests)
	}
	if b := rep.Tests[0].Bench; b == nil || b.N < 1 || b.N > 1<<30 || b.Reps < BenchBatch {
		t.Errorf("statistics %+v", b)
	}
	if !benchCall.MatchString(codeOnly("testing.BenchSys(sys, Run)", false)) || benchCall.MatchString("testing.Benchmark(x)") {
		t.Error("benchCall does not tell testing.BenchSys( from other names")
	}
}
