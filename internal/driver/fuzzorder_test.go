// Why: a fuzz target's output under plain mk test is read per input: each
// "fuzz input <name>" line must be followed by what the target printed for
// that input, though sys.Print and sys.PrintErr are buffered until the test
// ends and the line is not.
// What: TestFuzzOutputOrder.
// How: mk test over saved inputs of a FuzzSys target that prints to both
// streams, of one that fails on its second input after printing, and of a
// Fuzz target whose test block prints before calling Fuzz; each test's
// output lines are compared in order.

package driver

import (
	"slices"
	"strings"
	"testing"
)

const fuzzOrderSource = `import std.testing

Both(sys: &System, data: []u8) {
    n := data.Len()
    sys.Print("len {n}\n", (n)) else {}
    sys.PrintErr("err {n}\n", (n)) else {}
}

Second(sys: &System, data: []u8) {
    n := data.Len()
    sys.Print("len {n}\n", (n)) else {}
    Assert(n < 4)
}

Quiet(data: []u8) {
    Assert(data.Len() < 100)
}

test "both" (sys: &System) {
    testing.FuzzSys(sys, Both)
}

test "second" (sys: &System) {
    testing.FuzzSys(sys, Second)
}

test "plain" (sys: &System) {
    sys.Print("before\n") else {}
    testing.Fuzz(Quiet)
    sys.Print("after\n") else {}
}

Partial(sys: &System, data: []u8) {
    n := data.Len()
    if n == 3 {
        sys.Print("stdout") else {}
    } else if n == 5 {
        sys.PrintErr("stderr") else {}
    } else if n == 7 {
        var out := sys.Stdout()
        out.Str("flushed") else {}
        out.Flush() else {}
        testing.ExpectPanic("after seeds")
    }
}

test "partial" (sys: &System) {
    sys.Print("before") else {}
    testing.FuzzSys(sys, Partial)
    Panic("after seeds")
}
`

func TestFuzzOutputOrder(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": fuzzOrderSource})
	writeFiles(t, root, map[string]string{
		"testdata/fuzz/both/one":   "abc",
		"testdata/fuzz/both/two":   "hello",
		"testdata/fuzz/second/one": "abc",
		"testdata/fuzz/second/two": "hello",
		"testdata/fuzz/plain/one":  "abc",
		"testdata/fuzz/plain/two":  "hello",
		"testdata/fuzz/partial/a":  "abc",
		"testdata/fuzz/partial/b":  "hello",
		"testdata/fuzz/partial/c":  "1234567",
		"testdata/fuzz/partial/d":  "",
	})
	lines := func(out string, n int) []string {
		ls := strings.Split(out, "\n")
		if len(ls) > n {
			ls = ls[:n]
		}
		return ls
	}
	for _, c := range []struct {
		test, result string
		want         []string
	}{
		{"both", "main both pass", []string{"fuzz input one", "len 3", "err 3", "fuzz input two", "len 5", "err 5"}},
		{"second", "main second panic", []string{"fuzz input one", "len 3", "fuzz input two", "len 5", "panic: assert at main.mk:12:5"}},
		{"plain", "main plain pass", []string{"before", "fuzz input one", "fuzz input two", "after"}},
		{"partial", "main partial pass", []string{"before", "fuzz input a", "stdout", "fuzz input b", "stderr", "fuzz input c", "flushed", "fuzz input d"}},
	} {
		rep := mkTest(t, Config{Root: root}, TestOptions{Run: c.test})
		if got := results(rep); got != c.result {
			t.Fatalf("mk test %s: %s, want %s:\n%s", c.test, got, c.result, rep.Tests[0].Output)
		}
		if got := lines(rep.Tests[0].Output, len(c.want)); !slices.Equal(got, c.want) {
			t.Errorf("%s's output starts %q, want %q:\n%s", c.test, got, c.want, rep.Tests[0].Output)
		}
	}
}
