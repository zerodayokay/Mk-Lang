// Why: a test that a kept assertion refuses a bad input must pass exactly
// when its body panics after testing.ExpectPanic, and no other panic may
// pass.
// What: TestMkTestExpectPanic (one build: the outcomes of a real child) and
// TestExpectedPanicReport (the report parsing, over planted outputs).
// How: mk test over a package whose tests call testing.ExpectPanic; the
// planted outputs cover what one build does not (no backtrace, CRLF, a
// partial line of standard output before the report).

package driver

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

const expectPanicSource = `import std.testing

Load(off: u64): u64 {
    Assert(off & 7 == 0)
    return off
}

test "refused" {
    testing.ExpectPanic("assert")
    _ = Load(9)
}

test "any panic" {
    testing.ExpectPanic("")
    Panic("boom")
}

test "the site" {
    testing.ExpectPanic("t.mk:4:")
    _ = Load(9)
}

test "returns" {
    testing.ExpectPanic("assert")
    _ = Load(8)
}

test "wrong message" {
    testing.ExpectPanic("overflow")
    _ = Load(9)
}

test "panic before the call" {
    _ = Load(9)
    testing.ExpectPanic("assert")
}

test "no call" {
    _ = Load(9)
}

test "the last call wins" {
    testing.ExpectPanic("overflow")
    testing.ExpectPanic("assert")
    _ = Load(9)
}

test "after a partial line" (sys: &System) {
    testing.ExpectPanic("assert")
    try sys.Print("no newline")
    _ = Load(9)
}
`

// TestMkTestExpectPanic: a test passes when it panics after
// testing.ExpectPanic with a report line that contains the wanted text;
// it fails when its body returns, when the panic does not match, and when
// it panics before the call or never calls it. A passing test's output is
// not printed.
func TestMkTestExpectPanic(t *testing.T) {
	dir := testPkg(t, map[string]string{"t.mk": expectPanicSource})
	rep := mkTest(t, Config{Root: filepath.Join(dir, "t.mk")}, TestOptions{})
	want := strings.Join([]string{
		"main refused pass",
		"main any panic pass",
		"main the site pass",
		"main returns fail",
		"main wrong message fail",
		"main panic before the call panic",
		"main no call panic",
		"main the last call wins pass",
		"main after a partial line pass",
	}, "\n")
	if got := results(rep); got != want {
		t.Fatalf("results:\n%s\nwant:\n%s\noutputs: %+v", got, want, rep.Tests)
	}
	if rep.Tests[0].Status != 101 {
		t.Errorf("refused: status %d, want 101", rep.Tests[0].Status)
	}
	var text bytes.Buffer
	if err := rep.WriteText(&text, false); err != nil {
		t.Fatal(err)
	}
	tx := text.String()
	for _, l := range []string{
		"PASS main \"refused\"\nPASS main \"any panic\"\nPASS main \"the site\"\n",
		`FAIL main "returns": returned; testing.ExpectPanic("assert") wanted a panic`,
		`FAIL main "wrong message": panic does not contain "overflow" (testing.ExpectPanic): panic: assert at t.mk:4:5`,
		`FAIL main "panic before the call": panic (exit 101)`,
		`FAIL main "no call": panic (exit 101)`,
		"PASS main \"the last call wins\"\nPASS main \"after a partial line\"\n",
		"5 passed, 4 failed\n",
	} {
		if !strings.Contains(tx, l) {
			t.Errorf("text report lacks %q:\n%s", l, tx)
		}
	}
	if strings.Contains(tx, "    panic: assert at t.mk:4:5\n    backtrace:\n      Load (t.mk)\n      test \"refused\"") {
		t.Errorf("a passing expected panic's output is printed:\n%s", tx)
	}
}

// TestExpectedPanicReport: the report line is found with and without a
// backtrace, after CRLF line ends and a partial line of standard output,
// and a frame line naming "panic: " is not taken for it.
func TestExpectedPanicReport(t *testing.T) {
	for _, c := range []struct {
		name, out    string
		status       int
		result, want string
	}{
		{"backtrace", "mkexpectpanic 6 assert\npanic: assert at a.mk:4:5\nbacktrace:\n  Load (a.mk)\n  test \"panic: x\" (a.mk)\n", 101, "pass", ""},
		{"no backtrace", "mkexpectpanic 6 assert\npanic: assert at a.mk:4:5\n", 101, "pass", ""},
		{"crlf", "mkexpectpanic 6 assert\r\npanic: assert at a.mk:4:5\r\nbacktrace:\r\n  Load (a.mk)\r\n", 101, "pass", ""},
		{"partial line", "mkexpectpanic 6 assert\nhalfpanic: assert at a.mk:4:5\n", 101, "pass", ""},
		{"frame only", "mkexpectpanic 6 assert\nbacktrace:\n  test \"panic: assert\" (a.mk)\n", 101, "fail", "no panic report after testing.ExpectPanic(\"assert\")"},
		{"no marker", "panic: assert at a.mk:4:5\n", 101, "panic", ""},
		{"a line break in the want", "mkexpectpanic 15 x\nmkexpectpanic\npanic: x\n", 101, "fail", "panic does not contain \"x\\nmkexpectpanic\" (testing.ExpectPanic): panic: x"},
		{"a marker in the want", "mkexpectpanic 20 mkexpectpanic 1 x at\npanic: assert\n", 101, "fail", "panic does not contain \"mkexpectpanic 1 x at\" (testing.ExpectPanic): panic: assert"},
		{"two markers", "mkexpectpanic 1 x\nmkexpectpanic 6 assert\npanic: assert\n", 101, "pass", ""},
		{"short marker", "mkexpectpanic 9 x\n", 101, "panic", ""},
		{"bad length", "mkexpectpanic x\npanic: assert\n", 101, "panic", ""},
		{"returned", "mkexpectpanic 0 \n", 0, "fail", "returned; testing.ExpectPanic(\"\") wanted a panic"},
		{"error return", "mkexpectpanic 0 \nerror: Bad\n", 1, "error", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			tc := TestCase{Status: c.status, Result: resultOf(c.status), Output: c.out}
			judgeExpectedPanic(&tc)
			if tc.Result != c.result || tc.Detail != c.want {
				t.Errorf("result %q detail %q; want %q %q", tc.Result, tc.Detail, c.result, c.want)
			}
		})
	}
}
