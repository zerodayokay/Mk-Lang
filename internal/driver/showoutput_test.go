// Why: the output of a test that measures or reports something and passes
// must be readable without failing the test, and the default report must
// stay as quiet as it is.
// What: TestMkTestShowOutput and TestShowOutputLines: the text report under
// TestReport.ShowOutput (mk test --show-output).
// How: runs a package of a passing test that prints, a passing test that
// prints nothing and a failing test that prints, and compares the whole text
// report without and with the switch; then writes a report of a pass whose
// output has several CRLF lines, both ways.

package driver

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestMkTestShowOutput(t *testing.T) {
	src := "test \"prints\" (sys: &System) {\n    sys.Print(\"measured: 42 units\\n\") else {}\n}\n\n" +
		"test \"quiet\" {\n    Assert(true)\n}\n\n" +
		"test \"fails\" (sys: &System) {\n    sys.Print(\"measured: 43 units\\n\") else {}\n    Assert(false)\n}\n"
	dir := testPkg(t, map[string]string{"t.mk": src})
	// FramePointers keeps the non-Windows frame chain consistent with cmd/mk's test default.
	rep := mkTest(t, Config{Root: filepath.Join(dir, "t.mk"), FramePointers: true}, TestOptions{})
	if got := results(rep); got != "main prints pass\nmain quiet pass\nmain fails panic" {
		t.Fatalf("results:\n%s", got)
	}
	if rep.Tests[0].Output != "measured: 42 units\n" {
		t.Fatalf("passing test's captured output %q", rep.Tests[0].Output)
	}
	failing := "FAIL main \"fails\": panic (exit 101)\n" +
		"    measured: 43 units\n" +
		"    panic: assert at t.mk:11:5\n" +
		"    backtrace:\n" +
		"      test \"fails\" (t.mk)\n" +
		"2 passed, 1 failed\n"
	write := func(show bool) string {
		rep.ShowOutput = show
		var b bytes.Buffer
		if err := rep.WriteText(&b, false); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	quiet := "PASS main \"prints\"\nPASS main \"quiet\"\n" + failing
	if got := write(false); got != quiet {
		t.Errorf("default report changed:\n%s\nwant:\n%s", got, quiet)
	}
	shown := "PASS main \"prints\"\n    measured: 42 units\nPASS main \"quiet\"\n" + failing
	if got := write(true); got != shown {
		t.Errorf("--show-output report:\n%s\nwant:\n%s", got, shown)
	}
}

func TestShowOutputLines(t *testing.T) {
	rep := &TestReport{Passed: 1, Tests: []TestCase{{
		Module: "main", Name: "counts", Result: "pass",
		Output: "checked 3 inputs\r\ncaught 2 of 2\r\n  in 4 ms\r\n",
	}}}
	var b bytes.Buffer
	_ = rep.WriteText(&b, false)
	if want := "PASS main \"counts\"\n1 passed, 0 failed\n"; b.String() != want {
		t.Errorf("default:\n%s\nwant:\n%s", b.String(), want)
	}
	rep.ShowOutput = true
	b.Reset()
	_ = rep.WriteText(&b, false)
	want := "PASS main \"counts\"\n    checked 3 inputs\n    caught 2 of 2\n      in 4 ms\n1 passed, 0 failed\n"
	if b.String() != want {
		t.Errorf("--show-output:\n%s\nwant:\n%s", b.String(), want)
	}
}
