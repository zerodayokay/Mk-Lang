// Why: a FuzzSys target prints through the test's System, and
// --show-output must show what it printed for each saved input under
// plain mk test; under --fuzz the target runs thousands of times, so a
// passing loop shows its summary line only.
// What: TestShowOutputFuzzSys.
// How: mk test over a package whose FuzzSys target prints each input's
// length, with two saved inputs; the text report with TestReport.ShowOutput
// of the plain run holds each input's line followed by what the target
// printed for it, and that of a short --fuzz run holds the loop's one
// summary line and nothing the target printed.

package driver

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestShowOutputFuzzSys(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": fuzzSysSource})
	writeFiles(t, root, map[string]string{
		"testdata/fuzz/echo/a": "abc",
		"testdata/fuzz/echo/b": "hello",
	})
	text := func(rep *TestReport) string {
		rep.ShowOutput = true
		var b bytes.Buffer
		if err := rep.WriteText(&b, false); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	rep := mkTest(t, Config{Root: root}, TestOptions{Run: "echo"})
	if got := results(rep); got != "main echo pass" {
		t.Fatalf("mk test: %s", got)
	}
	got := text(rep)
	if want := "PASS main \"echo\"\n    fuzz input a\n    echo 3\n    fuzz input b\n    echo 5\n"; !strings.HasPrefix(got, want) {
		t.Errorf("--show-output of the plain run, want each input's line followed by its output:\n%s", got)
	}
	rep = mkTest(t, Config{Root: root}, TestOptions{Fuzz: "echo", FuzzTime: time.Second})
	if got := results(rep); got != "main echo pass" {
		t.Fatalf("mk test --fuzz echo: %s", got)
	}
	got = text(rep)
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 3 || lines[0] != "PASS main \"echo\"" || !strings.HasPrefix(lines[1], "    fuzz: ") ||
		!strings.HasSuffix(lines[1], ", 0 failing runs, 0 inputs saved") || lines[2] != "1 passed, 0 failed" {
		t.Errorf("--show-output of a passing --fuzz run, want the PASS line, the loop's summary and the count:\n%s", got)
	}
}
