// Why: a test that passes through testing.ExpectPanic ends its process with
// a panic, so its output holds the panic's report and the line
// ExpectPanic wrote for the driver; --show-output must print what the test
// printed and the report under its PASS line, never that line.
// What: TestShowOutputExpectedPanic.
// How: mk test over a package of a test that prints, calls
// testing.ExpectPanic and panics as it expects, and one that prints and
// passes by returning; the text report is compared whole without and with
// TestReport.ShowOutput.

package driver

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestShowOutputExpectedPanic(t *testing.T) {
	src := "import std.testing\n\n" +
		"Load(off: u64): u64 {\n    Assert(off & 7 == 0)\n    return off\n}\n\n" +
		"test \"refused\" (sys: &System) {\n    sys.Print(\"checked 9\\n\") else {}\n" +
		"    testing.ExpectPanic(\"assert\")\n    _ = Load(9)\n}\n\n" +
		"test \"returns\" (sys: &System) {\n    sys.Print(\"measured 8\\n\") else {}\n    _ = Load(8)\n}\n"
	dir := testPkg(t, map[string]string{"t.mk": src})
	// FramePointers keeps the non-Windows frame chain consistent with cmd/mk's test default.
	rep := mkTest(t, Config{Root: filepath.Join(dir, "t.mk"), FramePointers: true}, TestOptions{})
	if got := results(rep); got != "main refused pass\nmain returns pass" {
		t.Fatalf("results:\n%s", got)
	}
	if rep.Tests[0].Status != 101 {
		t.Fatalf("the expected panic's status %d, want 101", rep.Tests[0].Status)
	}
	if strings.Contains(rep.Tests[0].Output, expectPanicMark) {
		t.Fatalf("ExpectPanic's line left in the captured output: %q", rep.Tests[0].Output)
	}
	write := func(show bool) string {
		rep.ShowOutput = show
		var b bytes.Buffer
		if err := rep.WriteText(&b, false); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	quiet := "PASS main \"refused\"\nPASS main \"returns\"\n2 passed, 0 failed\n"
	if got := write(false); got != quiet {
		t.Errorf("default report:\n%s\nwant:\n%s", got, quiet)
	}
	shown := "PASS main \"refused\"\n" +
		"    checked 9\n" +
		"    panic: assert at t.mk:4:5\n" +
		"    backtrace:\n" +
		"      Load (t.mk)\n" +
		"      test \"refused\" (t.mk)\n" +
		"PASS main \"returns\"\n" +
		"    measured 8\n" +
		"2 passed, 0 failed\n"
	if got := write(true); got != shown {
		t.Errorf("--show-output report:\n%s\nwant:\n%s", got, shown)
	}
}
