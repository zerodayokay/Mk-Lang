// Why: mk build runs the instance-limit walk beside the native stages to
// save time, and must still report exactly what the synchronous walk would.
// What: TestPerfMemory_DeferredWalk and TestPerfMemory_BuildWalkMarkers,
// over walktail.go.
// How: builds runaway and clean programs cold and warm, and compares the
// report, the written executable and the walk's markers.

package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestPerfMemory_DeferredWalk: a runaway chain under a root Main does not
// reach is E0708 under mk build exactly as under mk check, cold and warm,
// and no executable is written; a clean program builds, and its timings
// still name the walk.
func TestPerfMemory_DeferredWalk(t *testing.T) {
	needTools(t)
	bad := map[string]string{"m.mk": nest + "\nexport Start(): int {\n    return Nest(1, 3)\n}\n\nexport Main(sys: &System) {\n}\n"}
	good := map[string]string{"m.mk": "One[T: Plain](x: T): int {\n    return 1\n}\n\nexport Main(sys: &System): int {\n    return One(true) + One(2)\n}\n"}

	dir := writeTree(t, bad)
	want := lines(check(t, Config{Root: dir, NoCache: true}))
	if len(want) != 1 || !strings.HasSuffix(want[0], ":E0708") {
		t.Fatalf("mk check: %v, want one E0708", want)
	}
	cache := t.TempDir()
	for _, run := range []string{"no cache", "cold", "warm"} {
		out := filepath.Join(t.TempDir(), "m.exe")
		s, err := NewSession(Config{Root: dir, NoCache: run == "no cache", CacheDir: cache})
		if err != nil {
			t.Fatal(err)
		}
		r, paths, err := s.Build(out)
		if err != nil {
			t.Fatal(err)
		}
		if got := lines(r); !slices.Equal(got, want) || len(paths) != 0 {
			t.Errorf("%s: mk build %v paths %v, want %v and no executable\n%s", run, got, paths, want, r.Report.Text(r.Sources))
		}
		if _, err := os.Stat(out); err == nil {
			t.Errorf("%s: an executable was written after E0708", run)
		}
	}

	gdir := writeTree(t, good)
	for _, run := range []string{"cold", "warm"} {
		if run == "warm" {
			// A comment-only edit bypasses the whole-build fingerprint fast
			// path while keeping every front-cache anchor and walk marker valid,
			// so the warm deferred-walk path is genuinely exercised.
			if err := os.WriteFile(filepath.Join(gdir, "m.mk"), []byte(good["m.mk"]+"// warm\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		var tm bytes.Buffer
		out := filepath.Join(t.TempDir(), "m.exe")
		s, err := NewSession(Config{Root: gdir, CacheDir: cache, Timings: &tm})
		if err != nil {
			t.Fatal(err)
		}
		r, paths, err := s.Build(out)
		if err != nil || len(r.Report.Diags) > 0 || len(paths) != 1 {
			t.Fatalf("%s: build err %v paths %v\n%s", run, err, paths, r.Report.Text(r.Sources))
		}
		if _, err := os.Stat(out); err != nil {
			t.Errorf("%s: no executable: %v", run, err)
		}
		_ = r.WriteTimings(&tm)
		if !strings.Contains(tm.String(), "stage instance-limit walk ") {
			t.Errorf("%s: timings name no walk:\n%s", run, tm.String())
		}
	}
}

// TestPerfMemory_BuildWalkMarkers: a cold mk build, whose stores write
// their files beside the native stages (deferWrites), still marks the walk
// of every package module: the walk joins the writes before it reads the
// stores' interface keys (walktail.go startWalk). A second build of the
// same tree finds the markers. The writes are delayed, so a walk that did
// not wait for them would mark nothing (mutation-checked).
func TestPerfMemory_BuildWalkMarkers(t *testing.T) {
	needTools(t)
	dir := writeTree(t, incPkg("x + 1"))
	cfg := Config{Root: dir, CacheDir: t.TempDir(), TestHooks: TestHooks{StoreWritesDelay: 300 * time.Millisecond}}
	for _, run := range []string{"cold", "warm"} {
		s, err := NewSession(cfg)
		if err != nil {
			t.Fatal(err)
		}
		r, paths, err := s.Build(filepath.Join(t.TempDir(), "bin"))
		if err != nil || len(r.Report.Diags) > 0 || len(paths) != 1 {
			t.Fatalf("%s: build err %v paths %v\n%s", run, err, paths, r.Report.Text(r.Sources))
		}
		for _, name := range []string{"lib", "app"} {
			m := s.pkgB.byName[name]
			if m == nil {
				t.Fatalf("no module %s", name)
			}
			if !s.w.front.walkedClean(m) {
				t.Errorf("%s: %s has no walk marker after a clean build", run, name)
			}
		}
	}
}
