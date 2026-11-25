// Why: a test that a kept assertion refuses a bad input must pass exactly
// when its body panics after testing.ExpectPanic, and no other panic may
// pass.
// What: TestMkTestExpectPanic (one build: the outcomes of a real child) and
// TestExpectedPanicReport (the report parsing, over planted outputs).
// How: mk test over a package whose tests call testing.ExpectPanic, or
// print what could pass for its line or its report; the planted outputs
// cover what one build does not (no backtrace, a backtrace with frame
// pointers off, CRLF, another run's token).

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

test "returns" (sys: &System) {
    testing.ExpectPanic("assert")
    try sys.Print("returned\n")
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

test "a printed line, no call" (sys: &System) {
    try sys.Print("mkexpectpanic 0 \n")
    _ = Load(9)
}

test "a printed report" (sys: &System) {
    testing.ExpectPanic("assert")
    try sys.Print("panic: assert")
    var a: [2]u8 = [0; 2]
    a[Trunc[int](Load(8))] = 1
}

test "a message with a line break" {
    testing.ExpectPanic("first\nsecond")
    Panic("first\nsecond")
}
`

// TestMkTestExpectPanic: a test passes when it panics after
// testing.ExpectPanic with a report that contains the wanted text; it
// fails when its body returns, when the panic does not match, when it
// panics before the call or never calls it, and when only text it printed
// matches. A passing test's output is not printed, and ExpectPanic's line
// is left out of a failing one's.
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
		"main a printed line, no call panic",
		"main a printed report fail",
		"main a message with a line break pass",
	}, "\n")
	if got := results(rep); got != want {
		t.Fatalf("results:\n%s\nwant:\n%s\noutputs: %+v", got, want, rep.Tests)
	}
	if rep.Tests[0].Status != 101 {
		t.Errorf("refused: status %d, want 101", rep.Tests[0].Status)
	}
	if o := rep.Tests[3].Output; o != "returned\n" {
		t.Errorf("returns: output %q, want only what it printed", o)
	}
	var text bytes.Buffer
	if err := rep.WriteText(&text, false); err != nil {
		t.Fatal(err)
	}
	tx := text.String()
	for _, l := range []string{
		"PASS main \"refused\"\nPASS main \"any panic\"\nPASS main \"the site\"\n",
		"FAIL main \"returns\": returned; testing.ExpectPanic(\"assert\") wanted a panic\n    returned\nFAIL",
		`FAIL main "wrong message": panic does not contain "overflow" (testing.ExpectPanic): panic: assert at t.mk:4:5`,
		`FAIL main "panic before the call": panic (exit 101)`,
		`FAIL main "no call": panic (exit 101)`,
		"PASS main \"the last call wins\"\nPASS main \"after a partial line\"\n",
		`FAIL main "a printed line, no call": panic (exit 101)`,
		`FAIL main "a printed report": panic does not contain "assert" (testing.ExpectPanic): panic: bounds at t.mk:64:5`,
		"PASS main \"a message with a line break\"\n",
		"6 passed, 6 failed\n",
	} {
		if !strings.Contains(tx, l) {
			t.Errorf("text report lacks %q:\n%s", l, tx)
		}
	}
	if strings.Contains(tx, "    panic: assert at t.mk:4:5\n    backtrace:\n      Load (t.mk)\n      test \"refused\"") {
		t.Errorf("a passing expected panic's output is printed:\n%s", tx)
	}
}

// TestExpectedPanicReport: only a line with the run's token counts, and it
// is taken out of the output; the text compared is the report's own, with
// and without a backtrace, after CRLF line ends, after text the test
// printed, and across a message's line breaks.
func TestExpectedPanicReport(t *testing.T) {
	const tok = "TOKEN"
	for _, c := range []struct {
		name, out    string
		status       int
		result, want string
		kept         string // the output after the judge; "" when unchanged
	}{
		{"backtrace", "mkexpectpanic TOKEN 6 assert\npanic: assert at a.mk:4:5\nbacktrace:\n  Load (a.mk)\n  test \"panic: x\" (a.mk)\n", 101, "pass", "",
			"panic: assert at a.mk:4:5\nbacktrace:\n  Load (a.mk)\n  test \"panic: x\" (a.mk)\n"},
		{"no backtrace", "mkexpectpanic TOKEN 6 assert\npanic: assert at a.mk:4:5\n", 101, "pass", "", "panic: assert at a.mk:4:5\n"},
		{"frame pointers off", "mkexpectpanic TOKEN 6 assert\npanic: assert at a.mk:4:5\nbacktrace: frame pointers were off\n", 101, "pass", "",
			"panic: assert at a.mk:4:5\nbacktrace: frame pointers were off\n"},
		{"frame pointers off, no match", "mkexpectpanic TOKEN 8 pointers\npanic: assert at a.mk:4:5\nbacktrace: frame pointers were off\n", 101, "fail",
			"panic does not contain \"pointers\" (testing.ExpectPanic): panic: assert at a.mk:4:5", "panic: assert at a.mk:4:5\nbacktrace: frame pointers were off\n"},
		{"crlf", "mkexpectpanic TOKEN 6 assert\r\npanic: assert at a.mk:4:5\r\nbacktrace:\r\n  Load (a.mk)\r\n", 101, "pass", "",
			"\r\npanic: assert at a.mk:4:5\r\nbacktrace:\r\n  Load (a.mk)\r\n"},
		{"partial line", "a\nmkexpectpanic TOKEN 6 assert\nhalfpanic: assert at a.mk:4:5\n", 101, "pass", "", "a\nhalfpanic: assert at a.mk:4:5\n"},
		{"printed report", "mkexpectpanic TOKEN 6 assert\npanic: assertpanic: bounds at a.mk:9:5\nbacktrace:\n", 101, "fail",
			"panic does not contain \"assert\" (testing.ExpectPanic): panic: bounds at a.mk:9:5", "panic: assertpanic: bounds at a.mk:9:5\nbacktrace:\n"},
		{"frame only", "mkexpectpanic TOKEN 6 assert\nbacktrace:\n  test \"panic: assert\" (a.mk)\n", 101, "fail",
			"no panic report after testing.ExpectPanic(\"assert\")", "backtrace:\n  test \"panic: assert\" (a.mk)\n"},
		{"a message with a line break", "mkexpectpanic TOKEN 0 \npanic: first\nsecond at a.mk:4:5\nbacktrace:\n", 101, "pass", "", "panic: first\nsecond at a.mk:4:5\nbacktrace:\n"},
		{"a want with a line break", "mkexpectpanic TOKEN 12 first\nsecond\npanic: first\nsecond at a.mk:4:5\n", 101, "pass", "", "panic: first\nsecond at a.mk:4:5\n"},
		{"no match across lines", "mkexpectpanic TOKEN 12 second\nfirst\npanic: first\nsecond at a.mk:4:5\n", 101, "fail",
			"panic does not contain \"second\\nfirst\" (testing.ExpectPanic): panic: first", "panic: first\nsecond at a.mk:4:5\n"},
		{"no token", "mkexpectpanic 0 \npanic: assert at a.mk:4:5\n", 101, "panic", "", ""},
		{"another run's token", "mkexpectpanic OTHER 0 \npanic: assert at a.mk:4:5\n", 101, "panic", "", ""},
		{"a mark in the want", "mkexpectpanic TOKEN 23 mkexpectpanic TOKEN 1 a\npanic: assert\n", 101, "fail",
			"panic does not contain \"mkexpectpanic TOKEN 1 a\" (testing.ExpectPanic): panic: assert", "panic: assert\n"},
		{"two marks", "mkexpectpanic TOKEN 1 x\nmkexpectpanic TOKEN 6 assert\npanic: assert\n", 101, "pass", "", "panic: assert\n"},
		{"short mark", "mkexpectpanic TOKEN 9 x\n", 101, "panic", "", ""},
		{"bad length", "mkexpectpanic TOKEN x\npanic: assert\n", 101, "panic", "", ""},
		{"returned", "mkexpectpanic TOKEN 0 \nout\n", 0, "fail", "returned; testing.ExpectPanic(\"\") wanted a panic", "out\n"},
		{"error return", "mkexpectpanic TOKEN 0 \nerror: Bad\n", 1, "error", "", "error: Bad\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tc := TestCase{Status: c.status, Result: resultOf(c.status), Output: c.out}
			judgeExpectedPanic(&tc, tok)
			if tc.Result != c.result || tc.Detail != c.want {
				t.Errorf("result %q detail %q; want %q %q", tc.Result, tc.Detail, c.result, c.want)
			}
			kept := c.kept
			if kept == "" {
				kept = c.out
			}
			if tc.Output != kept {
				t.Errorf("output %q; want %q", tc.Output, kept)
			}
		})
	}
	// With no token (a child the driver gave none), nothing is judged.
	tc := TestCase{Status: 101, Result: "panic", Output: "mkexpectpanic  0 \npanic: assert\n"}
	if judgeExpectedPanic(&tc, ""); tc.Result != "panic" {
		t.Errorf("no token: result %q, want panic", tc.Result)
	}
}
