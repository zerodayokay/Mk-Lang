// Why: --timings shows which functions cost the most to check, so its
// per-function lines must be complete and in a stable order.
// What: TestTimingsSlowFunctions.
// How: sets the slow-function threshold negative through the test hook so
// every function is reported, and compares the lines and the report with
// and without --timings.

package driver

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// TestTimingsSlowFunctions: with --timings, each function whose check plus
// borrow time passed the threshold (10 ms; negative here, through the test hook, so
// every function passes it) gets a "func <key> <ms>ms" line after the stage
// lines, sorted by key whatever order the workers finished in; without
// --timings nothing is recorded, and the report is the same either way.
func TestTimingsSlowFunctions(t *testing.T) {
	dir := writeTree(t, map[string]string{"t.mk": "B(): int {\n    return 2\n}\n\nA(): int {\n    return B()\n}\n"})
	file := filepath.Join(dir, "t.mk")
	var buf bytes.Buffer
	r := check(t, Config{Root: file, Timings: &buf, TestHooks: TestHooks{SlowFunc: -1}})
	if len(r.Report.Diags) != 0 {
		t.Fatalf("diagnostics: %v", lines(r))
	}
	if err := r.WriteTimings(&buf); err != nil {
		t.Fatal(err)
	}
	var funcs []string
	stagesDone, all := false, 0
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		switch {
		case strings.HasPrefix(l, "stage "):
			if stagesDone {
				t.Errorf("stage line %q after a func line", l)
			}
		case strings.HasPrefix(l, "func "):
			stagesDone = true
			all++
			// A key may hold spaces (an impl's), the time never does.
			sp := strings.LastIndexByte(l, ' ')
			key := l[len("func "):max(sp, len("func "))]
			if sp <= len("func ") || !strings.HasSuffix(l, "ms") {
				t.Errorf("line %q, want func <key> <ms>ms", l)
			}
			if !strings.HasPrefix(key, "std.") && !strings.HasPrefix(key, sysViewPrefix) {
				funcs = append(funcs, key)
			}
		default:
			t.Errorf("unexpected line %q", l)
		}
	}
	if all != len(r.SlowFuncs) {
		t.Errorf("%d func lines, %d SlowFuncs", all, len(r.SlowFuncs))
	}
	if len(funcs) != 2 || funcs[0] != "main.A" || funcs[1] != "main.B" {
		t.Errorf("func lines %v, want main.A then main.B\n%s", funcs, buf.String())
	}

	quiet := check(t, Config{Root: file, TestHooks: TestHooks{SlowFunc: -1}})
	if len(quiet.SlowFuncs) != 0 || len(quiet.Timings) != 0 {
		t.Errorf("without --timings: %v %v", quiet.SlowFuncs, quiet.Timings)
	}
	if !bytes.Equal(quiet.Report.WriteJSON(quiet.Sources), r.Report.WriteJSON(r.Sources)) {
		t.Error("--timings changed the report")
	}
}
