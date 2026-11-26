// Why: mk test --show-output must print a passing test's output and leave
// the default report as it is; mk bench does not take it.
// What: TestShowOutputFlag.
// How: runs mk test on a package of one passing test that prints, without
// the flag, with it, with it and --run selecting nothing, and with --json;
// then mk bench --show-output, a usage error.

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestShowOutputFlag(t *testing.T) {
	dir := t.TempDir()
	src := "test \"measures\" (sys: &System) {\n    sys.Print(\"measured: 42 units\\n\") else {}\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	mkTest := func(args ...string) (int, string, string) {
		var so, se bytes.Buffer
		st := run(append(append([]string{"test"}, args...), dir), &so, &se)
		return st, so.String(), se.String()
	}
	if st, so, se := mkTest(); st != 0 || so != "PASS main \"measures\"\n1 passed, 0 failed\n" {
		t.Errorf("default: status %d stdout %q stderr %q", st, so, se)
	}
	if st, so, se := mkTest("--show-output"); st != 0 || so != "PASS main \"measures\"\n    measured: 42 units\n1 passed, 0 failed\n" {
		t.Errorf("--show-output: status %d stdout %q stderr %q (red before: flag unknown)", st, so, se)
	}
	if st, so, se := mkTest("--show-output", "--run", "nothing"); st != 0 || so != "0 passed, 0 failed\n" {
		t.Errorf("--show-output --run nothing: status %d stdout %q stderr %q", st, so, se)
	}
	st, so, se := mkTest("--show-output", "--json")
	var doc struct {
		Tests []struct {
			Result, Output string
		}
	}
	if err := json.Unmarshal([]byte(so), &doc); st != 0 || err != nil || len(doc.Tests) != 1 || doc.Tests[0].Output != "measured: 42 units\n" {
		t.Errorf("--show-output --json: status %d err %v stdout %q stderr %q", st, err, so, se)
	}
	var bo, be bytes.Buffer
	if st := run([]string{"bench", "--show-output", dir}, &bo, &be); st != 2 {
		t.Errorf("mk bench --show-output: status %d, stderr %q; want a usage error", st, be.String())
	}
}
