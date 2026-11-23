// Why: tools/bench is the only source of published benchmark numbers, so its
// row discovery, build commands, statistics and rendering need tests of
// their own.
// What: TestMain (the test binary as a stand-in program) and the core tests,
// from TestSplitmixReference to TestLinuxColumnFacts, with the helpers
// writeFile, fakeTree, discoverTree and skipWithoutEvidence.
// How: fake repository trees in temporary directories, and the test binary
// itself as every timed program (BENCH_HELPER, BENCH_HELPER_APP).

package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for a benchmark program: with
// BENCH_HELPER set it prints that value and exits.
func TestMain(m *testing.M) {
	// The Linux peak spawner (mem_linux.go) first: it runs with the
	// program's environment, BENCH_HELPER_APP included, and must not become
	// the helper itself.
	if len(os.Args) > 1 && os.Args[1] == spawnArg {
		os.Exit(spawnMain(os.Args[2:]))
	}
	// A shim copy (shim.go) of the test binary forwards as main's does, so
	// TestIRViewRow's mk build reaches the real LLVM tools.
	if tool := shimTool(os.Args[0]); tool != "" {
		os.Exit(runShim(tool, os.Args[0], os.Args[1:]))
	}
	if v := os.Getenv("BENCH_HELPER"); v != "" {
		fmt.Println(v)
		os.Exit(0)
	}
	// BENCH_HELPER_APP=<mode> is an app row's program (appHelper,
	// findings_app_test.go).
	if mode := os.Getenv("BENCH_HELPER_APP"); mode != "" {
		os.Exit(appHelper(mode, os.Args[1:]))
	}
	// A copy made by helperExe prints the file beside it.
	if exe, err := os.Executable(); err == nil {
		if b, err := os.ReadFile(exe + ".out"); err == nil {
			fmt.Println(string(b))
			os.Exit(0)
		}
	}
	// BENCH_HELPER_ENV=<name> prints the value of that variable as the
	// program sees it.
	if name := os.Getenv("BENCH_HELPER_ENV"); name != "" {
		v, ok := os.LookupEnv(name)
		if !ok {
			v = "(unset)"
		}
		fmt.Println(v)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// discoverTree is discover over the repository's rows, skipping the test
// when a row cannot load only because the evidence directory, which lives
// outside the repository, is absent (a fresh checkout).
func discoverTree(t *testing.T, root string) ([]*row, error) {
	t.Helper()
	rows, err := discover(root)
	skipWithoutEvidence(t, root, err)
	return rows, err
}

// skipWithoutEvidence skips the test when err comes from a file of the
// evidence directory and that directory is absent.
func skipWithoutEvidence(t *testing.T, root string, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "the evidence directory is outside the repository") {
		if _, serr := os.Stat(evidenceDir(root)); serr != nil {
			t.Skipf("no evidence directory at %s (set MK_EVIDENCE): %v", evidenceDir(root), err)
		}
	}
}

func TestSplitmixReference(t *testing.T) {
	g := splitmix{0}
	want := []uint64{0xe220a8397b1dcdaf, 0x6e789e6aa1b965f4, 0x06c45d188009454f}
	for i, w := range want {
		if got := g.next(); got != w {
			t.Fatalf("output %d: %#x, want %#x", i, got, w)
		}
	}
}

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeTree is a repository root with module mk, one same row and one lane row.
func fakeTree(t *testing.T) string {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module mk\n\ngo 1.27\n")
	writeFile(t, filepath.Join(root, "tests/bench/same/b/b.c"), "// b\n")
	writeFile(t, filepath.Join(root, "tests/bench/same/b/bench.json"),
		`{"table":"same","overflow":"never","args":["1"],"sources":{"c":"b.c"}}`)
	writeFile(t, filepath.Join(root, "tests/bench/same/a/a.c"), "// a\n")
	writeFile(t, filepath.Join(root, "tests/bench/same/a/mk/a.mk"), "")
	writeFile(t, filepath.Join(root, "tests/bench/same/a/bench.json"),
		`{"name":"a","table":"same","overflow":"wraps","args":["1"],"sources":{"c":"a.c","mk":"mk"}}`)
	writeFile(t, filepath.Join(root, "tests/bench-lanes/std-hash/deep/x/x.c"), "// x\n")
	writeFile(t, filepath.Join(root, "tests/bench-lanes/std-hash/deep/x/bench.json"),
		`{"name":"x","table":"same","args":["1"],"sources":{"c":"x.c"}}`)
	return root
}

func TestDiscoverRows(t *testing.T) {
	root := fakeTree(t)
	rows, err := discoverTree(t, root)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.id())
	}
	if want := []string{"a", "b", "std-hash/x"}; !slices.Equal(ids, want) {
		t.Fatalf("ids %v, want %v", ids, want)
	}
	if rows[2].Lane != "std-hash" {
		t.Fatalf("lane %q", rows[2].Lane)
	}
	if got, err := selectRows(rows, "", []string{"x"}); err != nil || len(got) != 1 || got[0].id() != "std-hash/x" {
		t.Fatalf("select by name: %v %v", got, err)
	}
	if _, err := selectRows(rows, "", []string{"nope"}); err == nil {
		t.Fatal("unknown row selected")
	}
	r, _ := repoRoot(filepath.Join(root, "tests", "bench", "same"))
	if filepath.Clean(r) != filepath.Clean(root) {
		t.Fatalf("repoRoot %q, want %q", r, root)
	}
}

func TestDiscoverRejectsBadRows(t *testing.T) {
	cases := map[string]string{
		"unknown field":    `{"table":"same","sources":{"c":"a.c"},"bogus":1}`,
		"no table":         `{"sources":{"c":"a.c"}}`,
		"bad overflow":     `{"table":"same","overflow":"sometimes","sources":{"c":"a.c"}}`,
		"unknown language": `{"table":"same","sources":{"cobol":"a.c"}}`,
		"missing source":   `{"table":"same","sources":{"c":"missing.c"}}`,
		"no sources":       `{"table":"same"}`,
	}
	for name, js := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "tests/bench/same/a/a.c"), "")
			writeFile(t, filepath.Join(root, "tests/bench/same/a/bench.json"), js)
			if _, err := discover(root); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// TestDiscoverSetsAsideBrokenLaneRows: a lane row with a missing source is
// listed with its reason, left out of an unnamed selection and refused when
// named, while the other rows still load.
func TestDiscoverSetsAsideBrokenLaneRows(t *testing.T) {
	root := fakeTree(t)
	writeFile(t, filepath.Join(root, "tests/bench-lanes/std-hashmod/sip13/bench.json"),
		`{"name":"hash-sip13","table":"idiomatic","args":["1"],"sources":{"mk":"mk","rust":"r.rs"}}`)
	rows, err := discoverTree(t, root)
	if err != nil {
		t.Fatal(err)
	}
	bad := brokenRows(rows)
	if len(bad) != 1 || bad[0].id() != "std-hashmod/hash-sip13" || bad[0].Table != "idiomatic" || !strings.Contains(bad[0].Broken, "source of") {
		t.Fatalf("broken rows %+v", bad)
	}
	all, err := selectRows(rows, "", nil)
	if err != nil || len(all) != 3 {
		t.Fatalf("unnamed selection %d rows, %v (want the 3 good ones)", len(all), err)
	}
	if _, err := selectRows(rows, "", []string{"hash-sip13"}); err == nil || !strings.Contains(err.Error(), "does not load") {
		t.Fatalf("named broken row: %v", err)
	}
	var out, errb strings.Builder
	t.Chdir(root)
	t.Setenv("MK_BENCH_WORK", t.TempDir()) // list makes -work: keep it out of the real work root
	if code := run([]string{"list"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "BROKEN") {
		t.Fatalf("list exit %d:\n%s\n%s", code, out.String(), errb.String())
	}
}

func TestDiscoverRejectsDuplicateIDs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"tests/bench/same/a", "tests/bench-lanes/l/a"} {
		writeFile(t, filepath.Join(root, d, "a.c"), "")
	}
	writeFile(t, filepath.Join(root, "tests/bench/same/a/bench.json"), `{"table":"same","sources":{"c":"a.c"}}`)
	writeFile(t, filepath.Join(root, "tests/bench/same/b/b.c"), "")
	writeFile(t, filepath.Join(root, "tests/bench/same/b/bench.json"), `{"name":"a","table":"same","sources":{"c":"b.c"}}`)
	if _, err := discover(root); err == nil || !strings.Contains(err.Error(), "two rows") {
		t.Fatalf("duplicate accepted: %v", err)
	}
}

func TestStrayProfilesRefused(t *testing.T) {
	root := fakeTree(t)
	if s := strayProfiles(root); len(s) != 0 {
		t.Fatalf("clean tree reports %v", s)
	}
	writeFile(t, filepath.Join(root, "tests/bench/same/a/mk/default.mkprof"), "")
	writeFile(t, filepath.Join(root, "tests/bench-lanes/std-hash/deep/default.pgo"), "")
	if s := strayProfiles(root); len(s) != 2 {
		t.Fatalf("found %v, want both profiles", s)
	}
	var out, errb strings.Builder
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(root)
	if code := run([]string{"list"}, &out, &errb); code == 0 || !strings.Contains(errb.String(), "a PGO profile under the benchmark trees") {
		t.Fatalf("list ran with a stray profile: code %d, %s", code, errb.String())
	}
}

func TestColumnsAndClasses(t *testing.T) {
	never := &row{Overflow: "never", Sources: map[string]string{"mk": "mk", "mk-wrap": "w", "c": "a.c", "rust": "a.rs", "zig": "a.zig", "go": "a.go", "cs": "a.cs"}}
	var ids []string
	for _, c := range rowColumns(never) {
		ids = append(ids, c.ID+":"+c.class(never))
	}
	want := []string{"mk:checked", "mk-wrap:bounds", "c:unchecked", "rust:bounds", "rust-ovf:checked", "zig-fast:unchecked", "zig-safe:checked", "go:bounds", "cs:bounds", "cs-aot:bounds"}
	if !slices.Equal(ids, want) {
		t.Fatalf("columns %v\nwant %v", ids, want)
	}
	wraps := &row{Overflow: "wraps"}
	for _, id := range []string{"mk", "rust-ovf", "zig-safe"} {
		c, _ := columnByID(id)
		if got := c.class(wraps); got != "bounds" {
			t.Errorf("%s in a wraps row: class %s, want bounds", id, got)
		}
	}
	if cs := rowColumns(&row{Sources: map[string]string{"c": "a.c"}}); len(cs) != 1 || cs[0].ID != "c" {
		t.Fatalf("c-only row: %v", cs)
	}
}

// TestBuildCommandsFollowTheContract checks the build flags the benchmark
// contract (tests/bench/README.md) fixes on every column's command, for the
// Windows table on any host (TestLinuxCommands has the Linux one).
func TestBuildCommandsFollowTheContract(t *testing.T) {
	tc := &toolchain{Mk: "mk", Clang: "clang", Rustc: "rustc", Zig: "zig", Go: "go", Dotnet: "dotnet", Work: "W", CEnv: []string{"INCLUDE=i", "LIB=l"}, GOOS: "windows"}
	r := &row{Name: "r", Dir: "D", Sources: map[string]string{"mk": "mk", "mk-wrap": "mkwrap", "c": "r.c", "rust": "r.rs", "zig": "r.zig", "go": "testdata/r.go", "cs": "r.cs"}}
	want := map[string][]string{
		"mk":       {"--release", "--pgo=off", "x86-64-v2", "--no-cache"},
		"mk-wrap":  {"--release", "--pgo=off", "mkwrap"},
		"c":        {"--target=x86_64-pc-windows-msvc", "-O3", "-march=x86-64-v2", "-ffp-contract=off", "-fuse-ld=lld", "-fms-runtime-lib=static", "INCLUDE=i", "LIB=l"},
		"rust":     {"opt-level=3", "target-cpu=x86-64-v2", "codegen-units=1", "lto=fat", "panic=abort", "+crt-static"},
		"rust-ovf": {"overflow-checks=on", "lto=fat"},
		"zig-fast": {"ReleaseFast", "-mcpu=x86_64_v2", "-target", "x86_64-windows-gnu", "--cache-dir", "--global-cache-dir"},
		"zig-safe": {"ReleaseSafe", "-mcpu=x86_64_v2"},
		"go":       {"-pgo=off", "GOAMD64=v2", "GOTOOLCHAIN=local", "GOFLAGS="},
		"cs":       {"publish", "-c Release", "--self-contained false", "ServerGarbageCollector=false", "InvariantGlobalization=true", "AllowUnsafeBlocks=false"},
		"cs-aot":   {"publish", "-c Release", "PublishAot=true", "OptimizationPreference=Speed", "IlcInstructionSet=x86-64-v2", "ServerGarbageCollector=false", "InvariantGlobalization=true", "AllowUnsafeBlocks=false"},
	}
	for _, c := range rowColumns(r) {
		cmd, err := tc.command(r, c, "out.exe")
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		s := cmd.String()
		for _, w := range want[c.ID] {
			if !strings.Contains(s, w) {
				t.Errorf("%s: %q lacks %q", c.ID, s, w)
			}
		}
		if c.ID == "rust" && strings.Contains(s, "overflow-checks") {
			t.Errorf("rust column has overflow checks: %s", s)
		}
	}
	if _, err := (&toolchain{}).command(r, columns[0], "o"); err == nil {
		t.Fatal("built mk with no mk")
	}
}

func TestParseHyperfine(t *testing.T) {
	v2 := `{"schema_version":2,"results":[{"command":"x","measurements":[
		{"time_wall_clock":{"value":1.5,"unit":"second"},"exit_code":0},
		{"time_wall_clock":{"value":1500,"unit":"millisecond"},"exit_code":0}]}]}`
	ts, err := parseHyperfine([]byte(v2))
	if err != nil || !slices.Equal(ts, []float64{1.5, 1.5}) {
		t.Fatalf("schema 2: %v %v", ts, err)
	}
	ts, err = parseHyperfine([]byte(`{"results":[{"times":[0.25,0.5]}]}`))
	if err != nil || !slices.Equal(ts, []float64{0.25, 0.5}) {
		t.Fatalf("schema 1: %v %v", ts, err)
	}
	bad := `{"results":[{"measurements":[{"time_wall_clock":{"value":1,"unit":"second"},"exit_code":3}]}]}`
	if _, err := parseHyperfine([]byte(bad)); err == nil {
		t.Fatal("a failed run was accepted")
	}
	if _, err := parseHyperfine([]byte(`{"results":[]}`)); err == nil {
		t.Fatal("empty results accepted")
	}
}

func TestSummaryAndIntervals(t *testing.T) {
	s := summarize([]float64{1, 2, 3, 4, 100})
	if s.Median != 3 || s.Min != 1 || s.Max != 100 || s.MAD != 1 || s.N != 5 {
		t.Fatalf("summary %+v", s)
	}
	if median([]float64{4, 1, 3, 2}) != 2.5 {
		t.Fatal("even median")
	}
	a := []float64{2.0, 2.02, 1.98, 2.01, 1.99, 2.0, 2.03, 1.97, 2.0, 2.0}
	b := []float64{1.0, 1.01, 0.99, 1.0, 1.0, 1.02, 0.98, 1.0, 1.01, 0.99}
	ci := ratioCI(a, b)
	if !(ci.Lo <= 2 && 2 <= ci.Hi && ci.Lo > 1.9 && ci.Hi < 2.1) {
		t.Fatalf("interval %+v around 2", ci)
	}
	if ci2 := ratioCI(a, b); ci2 != ci {
		t.Fatal("the bootstrap is not deterministic")
	}
	if classify(ci, 1.10) != "over" || classify(interval{0.9, 1.05}, 1.10) != "within" || classify(interval{1.0, 1.2}, 1.10) != "inconclusive" {
		t.Fatal("classify")
	}
	p := pairedCI([]float64{0.9, 0.91, 0.89, 0.9, 0.92, 0.88})
	if !(p.Hi < 1 && p.Lo > 0.85) {
		t.Fatalf("paired interval %+v", p)
	}
	if !math.IsNaN(ratioCI(nil, b).Lo) {
		t.Fatal("empty sample gave an interval")
	}
}

func TestVerifyChecksAgreement(t *testing.T) {
	t.Setenv("BENCH_HELPER", "42")
	r := &row{Name: "r"}
	bs := []*built{{Row: r, Col: columns[0], Exe: os.Args[0]}, {Row: r, Col: columns[2], Exe: os.Args[0]}}
	if _, sum, ok := verify(bs, nil, ""); !ok || sum != "42" {
		t.Fatalf("agreement: %s %v", sum, ok)
	}
	if res, _, ok := verify(bs, nil, "43"); ok || !res[0].Wrong || !res[1].Wrong {
		t.Fatal("a wrong checksum passed")
	}
	bs = append(bs, &built{Row: r, Col: columns[3], Err: fmt.Errorf("build failed")})
	if res, _, ok := verify(bs, nil, "42"); ok || res[2].Err == nil {
		t.Fatal("a failed build passed")
	}
}

func TestRenderResults(t *testing.T) {
	res := &results{
		Runs:    []facts{{Commit: "abc", CPU: "cpu", Versions: map[string]string{"c": "clang 21"}, Busy: []string{"go.exe x2"}}},
		Columns: columns, Sweeps: 3, Options: hfOptions{Warmup: 3, Runs: 10},
		Rows: []rowInfo{{ID: "k", Table: "same", Algorithm: "Alg.", Overflow: "never", Args: []string{"9"}, Checksum: "7",
			Skip: map[string]string{"zig": "no reason"}}},
	}
	mkT := []float64{1.2, 1.21, 1.19, 1.2}
	cT := []float64{1.0, 1.01, 0.99, 1.0}
	res.Cells = []cell{
		{Row: "k", Table: "same", Col: "mk", Class: "checked", Times: mkT, Summary: summarize(mkT)},
		{Row: "k", Table: "same", Col: "c", Class: "unchecked", Times: cT, Summary: summarize(cT)},
		{Row: "k", Table: "same", Col: "go", Class: "bounds", Excluded: "checksum 8, expected 7"},
	}
	md := renderResults(res, "2026-11-06")
	for _, w := range []string{"| k | never | 1.200 s, x1.20 | 1.000 s, x1.00 |", "excluded (checksum 8, expected 7)",
		"| k | 1.20 [", "| over |", "Median of mk / c over these rows: 1.20", "machine was shared", "Table \"codegen\""} {
		if !strings.Contains(md, w) {
			t.Errorf("RESULTS.md lacks %q:\n%s", w, md)
		}
	}
}

// TestTreeRowsMeetTheContract loads the real rows and checks what can be
// checked without building: every row loads, has a README, records both
// checksums, and every baseline file opens with a header comment that names
// the row.
func TestTreeRowsMeetTheContract(t *testing.T) {
	wd, _ := os.Getwd()
	root, err := repoRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := discoverTree(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, r := range rows {
		if r.Lane != "" {
			continue
		}
		if r.Checksum == "" || r.QuickSum == "" {
			t.Errorf("%s: checksums not recorded", r.id())
		}
		if r.Table == "same" {
			if _, err := os.Stat(filepath.Join(r.Dir, "README.md")); err != nil {
				t.Errorf("%s: no README.md with the reference algorithm", r.id())
			}
			if r.Overflow == "" {
				t.Errorf("%s: no overflow class", r.id())
			}
			if (len(r.Counts) == 0) == (r.NoCounts == "") {
				t.Errorf("%s: a same-algorithm row names its counters (counts) or says why it has none (no_counts), not both or neither", r.id())
			}
		}
		if r.Table == "app" {
			// The program's own files, a dogfood's whole package included
			// (appRowViolations, findings_appcontract_test.go).
			for _, v := range appRowViolations(r) {
				t.Errorf("%s: %s", r.id(), v)
			}
			continue
		}
		for lang, src := range r.Sources {
			p := filepath.Join(r.Dir, src)
			if isMkLang(lang) {
				p = filepath.Join(p, r.base()+".mk")
			}
			b, err := os.ReadFile(p)
			if err != nil {
				t.Errorf("%s %s: %v", r.id(), lang, err)
				continue
			}
			if !strings.HasPrefix(string(b), "// "+r.base()+",") {
				t.Errorf("%s %s: %s does not open with the header comment \"// %s, ...\"", r.id(), lang, p, r.base())
			}
			if len(r.Counts) > 0 {
				mark := map[string]string{"mk": countSwitch["mk"], "zig": countSwitch["zig"], "go": countSwitch["go"], "cs": countSwitch["cs"], "c": "BENCH_COUNT", "rust": "cfg!(bench_count)"}[lang]
				if mark != "" && !strings.Contains(string(b), mark) {
					t.Errorf("%s %s: a row with counters has no counting switch %q in %s", r.id(), lang, mark, p)
				}
			}
			text := string(b)
			if isMkLang(lang) {
				// The package file declares the unsafe modules.
				pkg, err := os.ReadFile(filepath.Join(r.Dir, src, "pkg.mk"))
				if err != nil {
					t.Errorf("%s %s: %v", r.id(), lang, err)
				}
				text += "\n" + string(pkg)
			}
			if r.Table == "same" {
				for _, v := range contractViolations(lang, text) {
					t.Errorf("%s %s: uses %s, which the same-algorithm table forbids", r.id(), lang, v)
				}
				continue
			}
			for _, v := range rowViolations(r, lang, text) {
				t.Errorf("%s %s: uses %s, which the %s table forbids", r.id(), lang, v, r.Table)
			}
		}
	}
}

func TestMergeKeepsOtherRows(t *testing.T) {
	prev := results{
		Runs:   []facts{{Commit: "one"}, {Commit: "two"}},
		Builds: map[string]buildCmd{"a c": {Dir: "a"}, "b c": {Dir: "b"}},
		Rows:   []rowInfo{{ID: "a", Run: 1}, {ID: "b", Run: 0}},
		Cells:  []cell{{Row: "a", Col: "c"}, {Row: "b", Col: "c"}},
	}
	next := results{Runs: []facts{{Commit: "three"}}, Builds: map[string]buildCmd{}}
	got, err := mergeResults(prev, next, []string{"b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 2 || got.Runs[0].Commit != "two" || got.Runs[1].Commit != "three" {
		t.Fatalf("runs %+v", got.Runs)
	}
	if len(got.Rows) != 1 || got.Rows[0].ID != "a" || got.Rows[0].Run != 0 {
		t.Fatalf("rows %+v", got.Rows)
	}
	if len(got.Cells) != 1 || got.Cells[0].Row != "a" || len(got.Builds) != 1 {
		t.Fatalf("cells %+v builds %+v", got.Cells, got.Builds)
	}
}

// TestPrepareRowsSubstitutesData: "{data}" in a row's arguments becomes the
// absolute path of its generated file, the file is written from the spec and
// its stamp, and a row that names {data} without a spec is refused.
func TestPrepareRowsSubstitutesData(t *testing.T) {
	root := t.TempDir()
	r := &row{Name: "scan", Args: []string{"{data}", "3"}, Quick: []string{"{data}", "1"},
		Data: &dataSpec{Kind: "json", File: "scan.json", Bytes: 4096, Seed: 7}}
	plain := &row{Name: "plain", Args: []string{"5"}, Quick: []string{"1"}}
	if err := prepareRows(root, []*row{r, plain}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "tests", "bench", "data", "scan.json")
	if r.runArgs[0] != want || r.runArgs[1] != "3" || r.runQuick[0] != want {
		t.Fatalf("run args %q %q, want the data path %s", r.runArgs, r.runQuick, want)
	}
	if !slices.Equal(plain.runArgs, []string{"5"}) || !slices.Equal(plain.runQuick, []string{"1"}) {
		t.Fatalf("plain row args changed: %q %q", plain.runArgs, plain.runQuick)
	}
	b, err := os.ReadFile(want)
	if err != nil || len(b) < 4096 || b[0] != '[' {
		t.Fatalf("data file: %d bytes, err %v", len(b), err)
	}
	if s, _ := os.ReadFile(want + ".spec"); string(s) != "json 4096 7\n" {
		t.Fatalf("stamp %q", s)
	}
	bad := &row{Name: "bad", Args: []string{"{data}"}}
	if err := prepareRows(root, []*row{bad}); err == nil {
		t.Fatal("a {data} argument without a data spec was accepted")
	}
}

func TestParseCounts(t *testing.T) {
	r := &row{Name: "x", QuickSum: "42", Counts: []string{"cmp", "swap"}}
	got, err := parseCounts("42\r\ncounts cmp=7 swap=0", r)
	if err != nil || got != "counts cmp=7 swap=0" {
		t.Fatalf("parseCounts = %q, %v", got, err)
	}
	for _, bad := range []string{
		"42",                             // no counts line
		"41\ncounts cmp=7 swap=0",        // wrong checksum
		"42\ncounts swap=0 cmp=7",        // wrong order
		"42\ncounts cmp=7",               // missing counter
		"42\ncounts cmp=-1 swap=0",       // not unsigned
		"42\ncounts cmp=7 swap=0\nextra", // trailing line
	} {
		if _, err := parseCounts(bad, r); err == nil {
			t.Errorf("parseCounts(%q) accepted", bad)
		}
	}
}

func TestCountCopyFlipsOneSwitch(t *testing.T) {
	dir := t.TempDir()
	rowDir := filepath.Join(dir, "row")
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(rowDir, "mk", "x.mk"), "// x, Mk\nconst BenchCount: bool = false\n")
	write(filepath.Join(rowDir, "mk", "pkg.mk"), "const Package = 1\n")
	write(filepath.Join(rowDir, "testdata", "x.go"), "// x, Go\nconst benchCount = false\nconst benchCount = false\n")
	r := &row{Name: "x", Dir: rowDir, Sources: map[string]string{"mk": "mk", "go": "testdata/x.go"}}
	out := filepath.Join(dir, "copy")
	c, err := countCopy(r, "mk", out)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(out, "mk", "x.mk"))
	if c.Dir != out || !strings.Contains(string(b), "const BenchCount: bool = true") || strings.Contains(string(b), "= false") {
		t.Errorf("mk copy: dir %s, text %q", c.Dir, b)
	}
	if _, err := os.Stat(filepath.Join(out, "mk", "pkg.mk")); err != nil {
		t.Errorf("mk copy lost pkg.mk: %v", err)
	}
	if _, err := countCopy(r, "go", filepath.Join(dir, "copygo")); err == nil {
		t.Error("a source with two switch lines was accepted")
	}
}

func TestCountExclusions(t *testing.T) {
	mk, _ := columnByID("mk")
	c, _ := columnByID("c")
	res := []countResult{{B: &built{Col: mk}, Counts: "counts a=1"}, {B: &built{Col: c}, Counts: "counts a=2", Wrong: true}}
	ex := countExclusions(res, "counts a=1")
	if len(ex) != 1 || !strings.Contains(ex["c"], "differ") {
		t.Errorf("countExclusions = %v", ex)
	}
}

func TestWslPath(t *testing.T) {
	for in, want := range map[string]string{`D:\src\mk\tools\bench`: "/mnt/d/src/mk/tools/bench", `C:\x`: "/mnt/c/x"} {
		if runtime.GOOS != "windows" {
			break
		}
		if got := wslPath(in); got != want {
			t.Errorf("wslPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinuxCommands(t *testing.T) {
	r := &row{Name: "x", Dir: "/r", Sources: map[string]string{"mk": "mk", "c": "x.c"}}
	tc := &toolchain{Mk: "/mk", Clang: "/clang", Work: "/w", MkTarget: "x86_64-linux"}
	mk, _ := columnByID("mk")
	b, err := tc.command(r, mk, "/o")
	if err != nil || !slices.Contains(b.Args, "--target") || !slices.Contains(b.Args, "x86_64-linux") {
		t.Errorf("cross-built mk command = %v, %v", b.Args, err)
	}
	tc.MkPrebuilt = "/pre"
	b, err = tc.command(r, mk, "/o")
	if err != nil || b.Args[0] != "cp" || filepath.ToSlash(b.Args[1]) != "/pre/x/mk.exe" {
		t.Errorf("prebuilt mk command = %v, %v", b.Args, err)
	}
	if runtime.GOOS == "linux" {
		c, _ := columnByID("c")
		b, _ = tc.command(r, c, "/o")
		if !slices.Contains(b.Args, "-static") || !slices.Contains(b.Args, "-fuse-ld=lld") || b.Args[len(b.Args)-1] != "-lm" {
			t.Errorf("Linux C command = %v", b.Args)
		}
	}
}

func TestLinuxColumnFacts(t *testing.T) {
	lc := linuxColumnFacts(columns)
	for _, c := range lc {
		switch c.Lang {
		case "mk", "mk-wrap", "c":
			if strings.Contains(c.Linker, "lld-link") || strings.Contains(c.CRT, "UCRT") || strings.Contains(c.CRT, "kernel32") {
				t.Errorf("Linux column %s keeps a Windows fact: %+v", c.ID, c)
			}
		}
	}
	if columns[0].Linker != "lld-link" {
		t.Error("linuxColumnFacts changed the Windows column table")
	}
}
