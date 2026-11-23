// Why: the idiomatic, SIMD and compile-speed sets add row features (variants,
// CPU targets, keyed columns, thread counts, CPU masks) the base tables lack.
// What: TestVariantsExpand, TestVariantSkip, TestCpuFlags,
// TestKeyedColumn, TestConcurrencyEnv, TestRenderOtherTables and
// TestRowMasks.
// How: small rows, built in a temporary tree or in memory, and checks of the
// rows, commands, environment or rendering they produce.

package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestVariantsExpand: a bench.json with variants becomes one row per
// variant, named <name>-<variant>, each with its own sizes, checksums, CPU
// and bytes, sharing the sources (L1, L3 and DRAM sizes at v2 and v3).
func TestVariantsExpand(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module mk\n\ngo 1.27\n")
	writeFile(t, filepath.Join(root, "tests/bench/simd/ib/ib.c"), "// ib, C\n")
	writeFile(t, filepath.Join(root, "tests/bench/simd/ib/bench.json"), `{"name":"ib","table":"simd","sources":{"c":"ib.c"},
		"notes":["shared"],
		"variants":[
			{"name":"l1v2","args":["16384","1000"],"quick":["16384","1"],"checksum":"1","quick_checksum":"2","bytes":16384000},
			{"name":"l1v3","cpu":"x86-64-v3","args":["16384","1000"],"quick":["16384","1"],"checksum":"1","quick_checksum":"2","bytes":16384000,"notes":["v3"]}]}`)
	rows, err := discoverTree(t, root)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.id())
	}
	if want := []string{"ib-l1v2", "ib-l1v3"}; !slices.Equal(ids, want) {
		t.Fatalf("ids %v, want %v", ids, want)
	}
	v2, v3 := rows[0], rows[1]
	if v2.base() != "ib" || v2.cpu() != "x86-64-v2" || v3.cpu() != "x86-64-v3" || v3.Bytes != 16384000 || v2.Checksum != "1" {
		t.Fatalf("expanded rows wrong: %+v %+v", v2, v3)
	}
	if !slices.Equal(v3.Notes, []string{"shared", "v3"}) || !slices.Equal(v2.Notes, []string{"shared"}) {
		t.Fatalf("notes %v %v", v2.Notes, v3.Notes)
	}
	for name, js := range map[string]string{
		"skip of no source": `{"table":"simd","sources":{"c":"ib.c"},"variants":[{"name":"a","skip":{"go":"x"}}]}`,
		"skip, no reason":   `{"table":"simd","sources":{"c":"ib.c"},"variants":[{"name":"a","skip":{"c":""}}]}`,
		"bad cpu":           `{"table":"simd","cpu":"native","sources":{"c":"ib.c"}}`,
		"bad variant cpu":   `{"table":"simd","sources":{"c":"ib.c"},"variants":[{"name":"a","cpu":"x86-64-v4"}]}`,
		"repeated variant":  `{"table":"simd","sources":{"c":"ib.c"},"variants":[{"name":"a"},{"name":"a"}]}`,
		"bad variant name":  `{"table":"simd","sources":{"c":"ib.c"},"variants":[{"name":"L1"}]}`,
		"no threads":        `{"table":"idiomatic","sources":{"c":"ib.c"},"concurrency":{"mask":"0xff"}}`,
		"bad mask":          `{"table":"idiomatic","sources":{"c":"ib.c"},"concurrency":{"mask":"zz","threads":8}}`,
	} {
		writeFile(t, filepath.Join(root, "tests/bench/simd/ib/bench.json"), js)
		if _, err := discover(root); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestVariantSkip: a variant's skip takes a language's cell out of that
// variant only, with its reason, and leaves the row's other variants and
// the shared maps untouched (validutf8's scalar cells at L1 only).
func TestVariantSkip(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module mk\n\ngo 1.27\n")
	writeFile(t, filepath.Join(root, "tests/bench/simd/ib/ib.c"), "// ib, C\n")
	writeFile(t, filepath.Join(root, "tests/bench/simd/ib/testdata/ib.go"), "// ib, Go\npackage main\n")
	writeFile(t, filepath.Join(root, "tests/bench/simd/ib/bench.json"), `{"name":"ib","table":"simd","sources":{"c":"ib.c","go":"testdata/ib.go"},
		"skip":{"mk":"pending: x"},
		"variants":[
			{"name":"l1","args":["1"],"quick":["1"],"checksum":"1","quick_checksum":"1"},
			{"name":"dram","args":["2"],"quick":["1"],"checksum":"1","quick_checksum":"1","skip":{"go":"scalar: L1 only"}}]}`)
	rows, err := discoverTree(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
	l1, dram := rows[0], rows[1]
	if l1.id() != "ib-l1" {
		l1, dram = dram, l1
	}
	var ids []string
	for _, c := range rowColumns(dram) {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, []string{"c"}) || dram.Skip["go"] != "scalar: L1 only" || dram.Skip["mk"] != "pending: x" {
		t.Fatalf("dram columns %v skip %v", ids, dram.Skip)
	}
	if _, ok := l1.Sources["go"]; !ok || l1.Skip["go"] != "" || len(l1.Skip) != 1 {
		t.Fatalf("l1 changed: sources %v skip %v", l1.Sources, l1.Skip)
	}
}

// TestCpuFlags: a row's CPU target reaches every column's command in that
// language's spelling (-march, target-cpu, -mcpu with underscores,
// GOAMD64, mk --cpu), and the default stays x86-64-v2.
func TestCpuFlags(t *testing.T) {
	tc := &toolchain{Mk: "mk", Clang: "clang", Rustc: "rustc", Zig: "zig", Go: "go", Dotnet: "dotnet", Work: "W"}
	r := &row{Name: "r", Dir: "D", Cpu: "x86-64-v3", Sources: map[string]string{"mk": "mk", "c": "r.c", "rust": "r.rs", "zig": "r.zig", "go": "testdata/r.go", "cs": "r.cs"}}
	want := map[string]string{"mk": "--cpu x86-64-v3", "c": "-march=x86-64-v3", "rust": "target-cpu=x86-64-v3", "zig-fast": "-mcpu=x86_64_v3", "go": "GOAMD64=v3", "cs-aot": "IlcInstructionSet=x86-64-v3"}
	for _, c := range rowColumns(r) {
		cmd, err := tc.command(r, c, "out.exe")
		if err != nil {
			t.Fatal(err)
		}
		s := cmd.String()
		if w := want[c.ID]; w != "" && !strings.Contains(s, w) {
			t.Errorf("%s: %q lacks %q", c.ID, s, w)
		}
		if strings.Contains(s, "v2") {
			t.Errorf("%s: a v3 row still names v2: %s", c.ID, s)
		}
	}
	csCol, _ := columnByID("cs")
	v2Env := cellEnv(&row{Name: "r", Cpu: "x86-64-v2"}, csCol)
	if !slices.Contains(v2Env, "DOTNET_EnableAVX=0") || !slices.Contains(v2Env, "DOTNET_EnableAVX512=0") {
		t.Errorf("cs v2 env lacks ISA pins: %v", v2Env)
	}
	v3Env := cellEnv(r, csCol)
	if slices.Contains(v3Env, "DOTNET_EnableAVX=0") || !slices.Contains(v3Env, "DOTNET_EnableAVX512=0") {
		t.Errorf("cs v3 env wrong ISA pins: %v", v3Env)
	}
}

// TestKeyedColumn: a row with an "mk-keyed" source gets the mk-keyed
// column, built by mk from that directory (the NewKeyed peer of Rust's
// RandomState), and its cells render under Mk's toolchain.
func TestKeyedColumn(t *testing.T) {
	tc := &toolchain{Mk: "mk", Work: "W"}
	r := &row{Name: "hashmap", Dir: "D", Sources: map[string]string{"mk": "mk", "mk-keyed": "mkkeyed"}}
	var ids []string
	for _, c := range rowColumns(r) {
		ids = append(ids, c.ID)
		if c.ID == "mk-keyed" {
			cmd, err := tc.command(r, c, "o.exe")
			if err != nil || !strings.Contains(cmd.String(), filepath.Join("D", "mkkeyed")) || !strings.Contains(cmd.String(), "--release") {
				t.Fatalf("mk-keyed command %q %v", cmd.String(), err)
			}
		}
	}
	if !slices.Equal(ids, []string{"mk", "mk-keyed"}) {
		t.Fatalf("columns %v", ids)
	}
	if langKey(langOf("mk-keyed")) != "mk" {
		t.Fatal("mk-keyed is not rendered with mk's toolchain")
	}
}

// TestConcurrencyEnv: a concurrency row's programs get GOMAXPROCS and
// MK_CARRIERS set to its thread count, whatever the caller holds; any
// other row keeps GOMAXPROCS=1.
func TestConcurrencyEnv(t *testing.T) {
	t.Setenv("GOMAXPROCS", "3")
	t.Setenv("MK_CARRIERS", "5")
	r := &row{Name: "chan", Concurrency: &concSpec{Mask: "0xff", Threads: 8}}
	env := rowEnv(r)
	if v, _ := envValue(env, "GOMAXPROCS"); v != "8" {
		t.Errorf("GOMAXPROCS %q", v)
	}
	if v, _ := envValue(env, "MK_CARRIERS"); v != "8" {
		t.Errorf("MK_CARRIERS %q", v)
	}
	n := 0
	for _, kv := range env {
		if envKeyIs(kv, "GOMAXPROCS") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("GOMAXPROCS set %d times", n)
	}
	plain := rowEnv(&row{Name: "sort"})
	if v, _ := envValue(plain, "GOMAXPROCS"); v != "1" {
		t.Errorf("plain row GOMAXPROCS %q", v)
	}
	if _, ok := envValue(plain, "MK_CARRIERS"); ok {
		t.Error("plain row has MK_CARRIERS")
	}
}

// TestRenderOtherTables: an idiomatic row prints a missing language's
// reason as its cell (not "not written"), a concurrency row names each
// cell's model, and a row with bytes prints GB/s.
func TestRenderOtherTables(t *testing.T) {
	ts := []float64{2.0, 2.0, 2.0}
	res := &results{
		Runs:    []facts{{Commit: "abc"}},
		Columns: columns, Sweeps: 3, Options: hfOptions{Warmup: 3, Runs: 10},
		Rows: []rowInfo{
			{ID: "json", Table: "idiomatic", Algorithm: "Parse.", Args: []string{"1"}, Checksum: "7", Skip: map[string]string{"c": "no std JSON in C", "rust": "no std JSON in Rust (serde_json is a crate)"}},
			{ID: "chan", Table: "idiomatic", Algorithm: "Ping-pong.", Args: []string{"1"}, Checksum: "7",
				Concurrency: &concSpec{Mask: "0xff", Threads: 8, Models: map[string]string{"go": "goroutines", "rust": "OS threads"}}},
			{ID: "ib-l1v3", Table: "simd", Algorithm: "IndexByte.", Args: []string{"1"}, Checksum: "7", Cpu: "x86-64-v3", Bytes: 4e9},
		},
	}
	res.Cells = []cell{
		{Row: "json", Table: "idiomatic", Col: "go", Class: "bounds", Times: ts, Summary: summarize(ts)},
		{Row: "chan", Table: "idiomatic", Col: "go", Class: "bounds", Times: ts, Summary: summarize(ts)},
		{Row: "chan", Table: "idiomatic", Col: "rust", Class: "bounds", Times: ts, Summary: summarize(ts)},
		{Row: "ib-l1v3", Table: "simd", Col: "c", Class: "unchecked", Times: ts, Summary: summarize(ts)},
	}
	md := renderResults(res, "2026-11-08")
	// "| 2.00 |" is ib-l1v3's GB/s: 4e9 bytes over the 2.0 s median.
	for _, w := range []string{"| no std JSON in C |", "| no std JSON in Rust (serde_json is a crate) |", "Idiomatic standard library:",
		"| goroutines |", "| OS threads |", "CPU set `0xff` with 8 threads", "| 2.00 |", "CPU target `x86-64-v3`", "SIMD:"} {
		if !strings.Contains(md, w) {
			t.Errorf("RESULTS.md lacks %q:\n%s", w, md)
		}
	}
	if strings.Contains(md, "not written: no std") {
		t.Errorf("idiomatic skip rendered as \"not written\":\n%s", md)
	}
}

// TestRowMasks: publish's affinity follows the rows in any order (the
// sweeps and the noisy reruns visit them differently): a concurrency row
// gets its own mask, the next plain row gets the run's one-CPU mask back,
// a row whose mask is already set changes nothing, and a run with no mask
// (Linux, pinned from outside) never sets one.
func TestRowMasks(t *testing.T) {
	plain := &row{Name: "sort"}
	conc := &row{Name: "chan", Concurrency: &concSpec{Mask: "0x5", Threads: 2}}
	wide := &row{Name: "spawn", Concurrency: &concSpec{Mask: "0xff", Threads: 8}}
	cur := uint64(0x4) // publish sets the run's mask first
	type step struct {
		r      *row
		mask   uint64
		change bool
	}
	for i, s := range []step{{plain, 0x4, false}, {conc, 0x5, true}, {conc, 0x5, false}, {plain, 0x4, true}, {wide, 0xff, true}, {conc, 0x5, true}, {plain, 0x4, true}} {
		m, change, err := nextMask(s.r, 0x4, cur)
		if err != nil || m != s.mask || change != s.change {
			t.Fatalf("step %d (%s): mask %#x change %v err %v, want %#x %v", i, s.r.Name, m, change, err, s.mask, s.change)
		}
		if change {
			cur = m
		}
	}
	if _, change, _ := nextMask(conc, 0, 0x4); change {
		t.Fatal("a run with no mask set one")
	}
	if _, _, err := nextMask(&row{Concurrency: &concSpec{Mask: "zz", Threads: 1}}, 0x4, 0x4); err == nil {
		t.Fatal("a bad mask was accepted")
	}
}
