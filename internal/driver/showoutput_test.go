// Why: a test that measures or reports something and passes printed none of
// it, so a writer had to fail it on purpose to read its output (MKH-620).
// What: TestMkTestShowOutput and TestShowOutputExpectedPanic: the text
// report under TestReport.ShowOutput (mk test --show-output).
// How: runs a package of a passing test that prints, a passing test that
// prints nothing and a failing test that prints, and compares the whole text
// report without and with the switch; then writes a report of a pass with
// testing.ExpectPanic's shape (status 101, a printed line and the panic's
// report) both ways.

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
	rep := mkTest(t, Config{Root: filepath.Join(dir, "t.mk")}, TestOptions{})
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

func TestShowOutputExpectedPanic(t *testing.T) {
	rep := &TestReport{Passed: 1, Tests: []TestCase{{
		Module: "main", Name: "refuses", Result: "pass", Status: 101,
		Output: "checked 3 inputs\r\npanic: assert at p.mk:4:5\r\nbacktrace:\r\n  test \"refuses\" (p.mk)\r\n",
	}}}
	var b bytes.Buffer
	_ = rep.WriteText(&b, false)
	if want := "PASS main \"refuses\"\n1 passed, 0 failed\n"; b.String() != want {
		t.Errorf("default:\n%s\nwant:\n%s", b.String(), want)
	}
	rep.ShowOutput = true
	b.Reset()
	_ = rep.WriteText(&b, false)
	want := "PASS main \"refuses\"\n    checked 3 inputs\n    panic: assert at p.mk:4:5\n    backtrace:\n      test \"refuses\" (p.mk)\n1 passed, 0 failed\n"
	if b.String() != want {
		t.Errorf("--show-output:\n%s\nwant:\n%s", b.String(), want)
	}
}
