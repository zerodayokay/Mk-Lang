// Why: every subcommand works on the same set of rows, so the rows are
// read, checked and selected in one place.
//
// What: row (one bench.json and everything it may state), concSpec,
// variant, loadRow, discover, selectRows, brokenRow and strayProfiles.
//
// How: rows are read from bench.json files under tests/bench/<table>/ and
// tests/bench-lanes/<lane-id>/, expanded per variant and sorted by table and
// id. A row under tests/bench-lanes that does not load is kept as a broken
// stand-in; a stray PGO profile under the benchmark trees is refused.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// row is one bench.json. Sources maps a language ("mk", "c", "rust", "zig",
// "go") to its source, relative to the row directory; "mk" names the package
// root (usually "."). A language with no source has a Skip entry saying why
// it is not in the row.
type row struct {
	Name      string            `json:"name"`
	Table     string            `json:"table"` // "same", "codegen", or a table bench-std adds
	Algorithm string            `json:"algorithm"`
	Overflow  string            `json:"overflow"` // "never" (checked types) or "wraps" (wrapping operators by design)
	Args      []string          `json:"args"`     // publish size: the fastest entry runs at least 1 s
	Quick     []string          `json:"quick"`    // verify and compare size
	Checksum  string            `json:"checksum"` // expected output for Args; "" until the first verified run
	QuickSum  string            `json:"quick_checksum"`
	Sources   map[string]string `json:"sources"`
	Skip      map[string]string `json:"skip"`
	Data      *dataSpec         `json:"data,omitempty"`
	// Vendor names third-party C files outside the repository that the Mk
	// package and the C column build from (vendor.go).
	Vendor *vendorSpec `json:"vendor,omitempty"`
	Notes  []string    `json:"notes"`
	// Counts names the operation counters of the row's counting build,
	// in the order its "counts" line prints them. A row with none says why in
	// NoCounts.
	Counts   []string `json:"counts,omitempty"`
	NoCounts string   `json:"no_counts,omitempty"`
	// Cpu is the CPU target of every column ("" is x86-64-v2, the contract's
	// one target; the SIMD table adds x86-64-v3). See cpuFlags.
	Cpu string `json:"cpu,omitempty"`
	// Bytes is the input bytes one run at Args processes, for rows reported
	// in GB/s (the SIMD table); 0 when the row reports time only.
	Bytes int64 `json:"bytes,omitempty"`
	// Baseline is Args with the kernel's repetitions set to 0, for a row
	// whose program builds its input inside the timed run (the SIMD table's
	// fill and first-touch page faults): publish times it beside every
	// cell, and RESULTS.md reports the kernel as the cell's times minus the
	// baseline's median, so a compiler's fill loop is not ranked as its
	// SIMD layer and GB/s divides the kernel's bytes by the kernel's time.
	Baseline []string `json:"baseline,omitempty"`
	// Concurrency is set on a row about concurrency (the rules: "Execution
	// is single-threaded unless the row is about concurrency"): its
	// programs get the CPU set and thread count stated there instead of
	// the one-CPU pin, and each language's model is labelled.
	Concurrency *concSpec `json:"concurrency,omitempty"`
	// Std names the std functions an idiomatic row prices, a list because
	// a row may price more than one (fmt's Print and str.ParseInt):
	// ["std.core.Slice.Sort"]. The list is the line of the std bars'
	// summary the row is counted in. Exception is the key of a stated
	// exception to the 2x std bar, printed beside an over verdict: one of
	// stdExceptions, on a row whose std is exactly the function it excepts
	// (stdbars.go). Both are
	// optional and the idiomatic table's only.
	Std       []string `json:"std,omitempty"`
	Exception string   `json:"exception,omitempty"`
	// Variants expands one bench.json into one row per variant (the SIMD
	// table's L1, L3 and DRAM sizes at v2 and v3), named
	// "<name>-<variant>", sharing the sources.
	Variants []variant `json:"variants,omitempty"`
	// Dispatch marks a SIMD row about run-time CPU dispatch (the labelled
	// dispatch row, tests/bench/simd/indexdispatch): every column is built at
	// x86-64-v2 and picks its widest form at run time. It maps each language
	// with a source to how it dispatches (the row's notes say the same for
	// RESULTS.md). The SIMD table's bans then let C's
	// __attribute__((target("..."))) and Rust's #[target_feature] functions
	// through for this row only (rowViolations, TestDispatchRow).
	Dispatch map[string]string `json:"dispatch,omitempty"`
	// Dispatching is the expanded variant's Dispatching (variant): the Mk
	// cells of this fixed-target row that choose their vector form at run
	// time through std, each labelled in the row's notes.
	Dispatching map[string]string `json:"-"`

	// The app table's fields (app.go; README "Applications"). loadRow refuses
	// every one of them on the other tables.
	//
	// Kinds gives each language with a source its cell kind: "std" (the
	// language's standard library only), "eco" (pinned libraries, named in
	// Libs) or "hand" (the program writes a facility itself, described in
	// Hand). An Mk cell is always "std".
	Kinds map[string]string `json:"kinds,omitempty"`
	Libs  map[string][]lib  `json:"libs,omitempty"`
	Hand  map[string]string `json:"hand,omitempty"`
	// Labels gives a language a label printed beside each of its cells,
	// such as a hardware path its library leaves out at the row's target
	// (app/archive's Zig).
	Labels map[string]string `json:"labels,omitempty"`
	// Env is added to the environment of every program of the row, after
	// runEnv (a single-threaded row's MK_CARRIERS=1, for example).
	Env map[string]string `json:"env,omitempty"`
	// Package names the Mk side as a package elsewhere in the repository
	// (a dogfood, used unchanged); its commit is recorded with the row.
	Package *pkgSpec `json:"package,omitempty"`
	// Digest makes the checksum the SHA-256 of the program's standard
	// output, normalised (normalise).
	Digest bool `json:"digest,omitempty"`
	// Output is a file the program writes, fresh before every run, and how
	// it is checked.
	Output *outputSpec `json:"output,omitempty"`
	// Crates names a directory under the evidence directory, relative and
	// slash-separated, that holds the crates of the row's Rust crate in a
	// `cargo vendor --versioned-dirs` layout, their crates.io archives in
	// archives/ that pin it (cratespin.go), and the .cargo/config.toml
	// that replaces crates-io with it: cargo reads the crates from there,
	// by directory source replacement, instead of from the user's
	// registry (cargo.go).
	Crates string `json:"crates,omitempty"`

	Dir string `json:"-"` // absolute directory of the row
	// Base is the bench.json's own name for a row expanded from a variant
	// (its sources' header names it), else "".
	Base string `json:"-"`
	Lane string `json:"-"` // the lane id for a tests/bench-lanes row, else ""
	// Broken is why a tests/bench-lanes row did not load ("" when it did).
	// A row there may be half-written while it is worked on, so a broken row is
	// set aside instead of failing every subcommand; it is listed, and
	// naming it is an error. A row under tests/bench never loads broken.
	Broken string `json:"-"`

	// runArgs, runQuick and runBaseline are Args, Quick and Baseline with
	// "{data}" replaced by the data file's absolute path; prepareRows sets
	// them before any run. runThen is Output.Then the same way. "{out}"
	// stays: each column has its own output path (cellArgs).
	runArgs, runQuick, runBaseline, runThen []string
	// dataPath is the absolute path of the row's data file or tree ("" for
	// none), set by prepareRows.
	dataPath string
	// pkgPath is what {pkg} became (pkgDir), set by prepareRows: the digest
	// writes it back as $PKG (normalise).
	pkgPath string
}

// concSpec is a concurrency row's machine setup: the affinity mask
// every program of the row inherits instead of the one-CPU mask, the thread
// count each runtime is given (GOMAXPROCS and MK_CARRIERS), and the model
// of each language's cell ("tasks", "goroutines", "OS threads"), printed
// beside its number.
type concSpec struct {
	Mask    string            `json:"mask"`    // hex affinity mask, e.g. "0xff00"
	Threads int               `json:"threads"` // GOMAXPROCS and MK_CARRIERS
	Models  map[string]string `json:"models"`  // language -> its model
}

// variant is one expansion of a row (Variants).
type variant struct {
	Name     string   `json:"name"`
	Args     []string `json:"args"`
	Quick    []string `json:"quick"`
	Checksum string   `json:"checksum"`
	QuickSum string   `json:"quick_checksum"`
	Cpu      string   `json:"cpu,omitempty"`
	Bytes    int64    `json:"bytes,omitempty"`
	Baseline []string `json:"baseline,omitempty"` // see row.Baseline
	Notes    []string `json:"notes,omitempty"`
	// Skip leaves languages out of this variant only, with the reason: a
	// language with a source in the row has no cell in the variant (the
	// SIMD table's scalar cells, measured at the L1 size only).
	Skip map[string]string `json:"skip,omitempty"`
	// Output replaces the row's output spec in this variant only (an app
	// row whose variants write different files).
	Output *outputSpec `json:"output,omitempty"`
	// Sources replaces some of the row's sources in this variant only: a
	// language that fixes its vector width at compile time from the build's
	// CPU target (C's __AVX2__, Zig's suggestVectorLength) does so through
	// the preprocessor or comptime, and Mk, which has no compile-time CPU
	// fact (Target.vector_bits never follows --cpu), through a second
	// package with the other width (mandelvec). The language must already
	// have a source in the row.
	Sources map[string]string `json:"sources,omitempty"`
	// Dispatching labels an Mk cell of this variant, in a SIMD row without a
	// dispatch field, that chooses its vector form at run time through a std
	// routine it calls: language to how. The cell's own text may still call
	// no dispatching std.simd routine; the label is for a std routine that
	// hands its work to one, such as str.From once std.core's ValidUtf8 calls
	// std.simd.ValidUtf8, and TestNoRunTimeChoiceInFixedRows fails on a
	// cell whose release build reads the CPU's features without one. expand
	// turns each into a note, so RESULTS.md prints it with the variant.
	Dispatching map[string]string `json:"dispatching,omitempty"`
}

// dispatchingNote is the note expand writes for a variant's Dispatching.
func dispatchingNote(lang, how string) string {
	return lang + ": chooses its vector form at run time, labelled: " + how
}

// cpus are the CPU targets a row may name, and each language's spelling.
var cpus = map[string]struct{ C, Rust, Zig, GoAmd64, Mk, Cs string }{
	"x86-64-v2": {"x86-64-v2", "x86-64-v2", "x86_64_v2", "v2", "x86-64-v2", "x86-64-v2"},
	"x86-64-v3": {"x86-64-v3", "x86-64-v3", "x86_64_v3", "v3", "x86-64-v3", "x86-64-v3"},
}

// cpu is the row's CPU target.
func (r *row) cpu() string {
	if r.Cpu == "" {
		return "x86-64-v2"
	}
	return r.Cpu
}

// base is the name the row's sources carry in their header.
func (r *row) base() string {
	if r.Base != "" {
		return r.Base
	}
	return r.Name
}

// expand turns a row with variants into one row per variant.
func expand(r *row) []*row {
	if len(r.Variants) == 0 {
		return []*row{r}
	}
	var out []*row
	for _, v := range r.Variants {
		x := *r
		x.Variants = nil
		x.Base = r.Name
		x.Name = r.Name + "-" + v.Name
		x.Args, x.Quick, x.Checksum, x.QuickSum = v.Args, v.Quick, v.Checksum, v.QuickSum
		if v.Cpu != "" {
			x.Cpu = v.Cpu
		}
		if v.Bytes != 0 {
			x.Bytes = v.Bytes
		}
		if len(v.Baseline) > 0 {
			x.Baseline = v.Baseline
		}
		if v.Output != nil {
			x.Output = v.Output
		}
		x.Notes = append(slices.Clone(r.Notes), v.Notes...)
		if len(v.Sources) > 0 {
			x.Sources = maps.Clone(r.Sources)
			maps.Copy(x.Sources, v.Sources)
		}
		if len(v.Skip) > 0 {
			x.Sources, x.Skip = maps.Clone(x.Sources), maps.Clone(r.Skip)
			if x.Skip == nil {
				x.Skip = map[string]string{}
			}
			for lang, why := range v.Skip {
				delete(x.Sources, lang)
				x.Skip[lang] = why
			}
		}
		if len(v.Dispatching) > 0 {
			x.Dispatching = maps.Clone(v.Dispatching)
			for _, lang := range slices.Sorted(maps.Keys(v.Dispatching)) {
				x.Notes = append(x.Notes, dispatchingNote(lang, v.Dispatching[lang]))
			}
		}
		out = append(out, &x)
	}
	return out
}

// dataArg is the placeholder in Args and Quick for the row's data file.
const dataArg = "{data}"

// pkgArg is the placeholder in an app row's arguments for the absolute
// directory of the dogfood package it names (package), so a Tier A row
// passes the dogfood's own input files (its cases/data) to the dogfood
// unchanged; checkApp refuses it on a row with no package.
const pkgArg = "{pkg}"

// pkgDir is the absolute directory of r's Mk package when the row names
// one through package, else "".
func (r *row) pkgDir() string {
	if r.Package == nil || r.Sources["mk"] == "" {
		return ""
	}
	return filepath.Clean(filepath.Join(r.Dir, r.Sources["mk"]))
}

// prepareRows writes the data file of every row that reads one (from its
// spec, into tests/bench/data, unless an up-to-date file is there) and sets
// each row's run arguments.
func prepareRows(root string, rows []*row) error {
	for _, r := range rows {
		if err := checkVendorFlags(r); err != nil {
			return err
		}
		path := ""
		if r.Data != nil {
			p, err := genData(dataDir(root), *r.Data)
			if err != nil {
				return fmt.Errorf("%s: %w", r.id(), err)
			}
			path = p
		}
		pkg := r.pkgDir()
		sub := func(args []string) ([]string, error) {
			out := make([]string, len(args))
			for i, a := range args {
				if strings.Contains(a, dataArg) && path == "" {
					return nil, fmt.Errorf("%s: argument %q names %s but the row has no data spec", r.id(), a, dataArg)
				}
				if strings.Contains(a, pkgArg) && pkg == "" {
					return nil, fmt.Errorf("%s: argument %q names %s but the row names no package", r.id(), a, pkgArg)
				}
				out[i] = strings.ReplaceAll(strings.ReplaceAll(a, dataArg, path), pkgArg, pkg)
			}
			return out, nil
		}
		r.dataPath = path
		r.pkgPath = pkg
		var err1, err2, err3, err4 error
		r.runArgs, err1 = sub(r.Args)
		r.runQuick, err2 = sub(r.Quick)
		if len(r.Baseline) > 0 {
			r.runBaseline, err3 = sub(r.Baseline)
		}
		if r.Output != nil && len(r.Output.Then) > 0 {
			r.runThen, err4 = sub(r.Output.Then)
		}
		if err := errors.Join(err1, err2, err3, err4); err != nil {
			return err
		}
	}
	return nil
}

// id is the row's name in tables: "<lane>/<name>" for a lane row.
func (r *row) id() string {
	if r.Lane != "" {
		return r.Lane + "/" + r.Name
	}
	return r.Name
}

// loadRow reads and checks one bench.json.
func loadRow(path, lane string) (*row, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r row
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	r.Dir = filepath.Dir(path)
	r.Lane = lane
	if r.Name == "" {
		r.Name = filepath.Base(r.Dir)
	}
	if r.Table == "" {
		return nil, fmt.Errorf("%s: no table", path)
	}
	if r.Overflow != "" && r.Overflow != "never" && r.Overflow != "wraps" {
		return nil, fmt.Errorf("%s: overflow %q is neither never nor wraps", path, r.Overflow)
	}
	if r.Vendor != nil {
		if err := resolveVendor(r.Vendor, r.Dir); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if err := checkApp(&r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if (r.Std != nil || r.Exception != "") && r.Table != "idiomatic" {
		return nil, fmt.Errorf("%s: std and exception belong to the idiomatic table's std bars", path)
	}
	if r.Std != nil && len(r.Std) == 0 {
		return nil, fmt.Errorf("%s: std is an empty list (leave it out instead)", path)
	}
	for i, n := range r.Std {
		if !stdName.MatchString(n) {
			return nil, fmt.Errorf("%s: std %q is not a std function's name (std.<module>.<Name>)", path, n)
		}
		if slices.Contains(r.Std[:i], n) {
			return nil, fmt.Errorf("%s: std names %q twice", path, n)
		}
	}
	if r.Exception != "" {
		fn, ok := stdExceptions[r.Exception]
		if !ok {
			return nil, fmt.Errorf("%s: exception %q is not one of the stated exceptions (crypto-random for std.crypto.Random, sort-stable for std.core.Slice.SortStable)", path, r.Exception)
		}
		if len(r.Std) != 1 || r.Std[0] != fn {
			return nil, fmt.Errorf("%s: exception %s excepts %s only, so the row's std is exactly [%q], not %q", path, r.Exception, fn, fn, r.Std)
		}
	}
	if len(r.Sources) == 0 {
		return nil, fmt.Errorf("%s: no sources", path)
	}
	if _, ok := cpus[r.cpu()]; !ok {
		return nil, fmt.Errorf("%s: cpu %q is not one of the contract's targets", path, r.Cpu)
	}
	if c := r.Concurrency; c != nil {
		if c.Threads < 1 || c.Mask == "" {
			return nil, fmt.Errorf("%s: concurrency needs a mask and a thread count", path)
		}
		if _, err := parseMask(c.Mask); err != nil {
			return nil, fmt.Errorf("%s: concurrency mask: %w", path, err)
		}
	}
	if err := checkBaseline(r.Baseline, r.Args, r.Bytes); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	vseen := map[string]bool{}
	for _, v := range r.Variants {
		if len(v.Baseline) > 0 {
			bytes := v.Bytes
			if bytes == 0 {
				bytes = r.Bytes
			}
			if err := checkBaseline(v.Baseline, v.Args, bytes); err != nil {
				return nil, fmt.Errorf("%s: variant %s: %w", path, v.Name, err)
			}
		}
		if v.Name == "" || strings.Trim(v.Name, "abcdefghijklmnopqrstuvwxyz0123456789") != "" || vseen[v.Name] {
			return nil, fmt.Errorf("%s: variant name %q is empty, repeated or not [a-z0-9]+", path, v.Name)
		}
		vseen[v.Name] = true
		if v.Cpu != "" {
			if _, ok := cpus[v.Cpu]; !ok {
				return nil, fmt.Errorf("%s: variant %s: cpu %q is not one of the contract's targets", path, v.Name, v.Cpu)
			}
		}
		for lang, why := range v.Skip {
			if _, ok := r.Sources[lang]; !ok || why == "" {
				return nil, fmt.Errorf("%s: variant %s: skip %q names no source of the row, or gives no reason", path, v.Name, lang)
			}
		}
		for lang, how := range v.Dispatching {
			_, skipped := v.Skip[lang]
			if _, ok := r.Sources[lang]; !ok || skipped || !isMkLang(lang) || how == "" || r.Table != "simd" || len(r.Dispatch) > 0 {
				return nil, fmt.Errorf("%s: variant %s: dispatching %q names no Mk cell of the variant in a fixed-target SIMD row, or says not how", path, v.Name, lang)
			}
		}
		for lang, src := range v.Sources {
			if _, ok := r.Sources[lang]; !ok {
				return nil, fmt.Errorf("%s: variant %s: source %q replaces no source of the row", path, v.Name, lang)
			}
			if _, err := os.Stat(filepath.Join(r.Dir, src)); err != nil {
				return nil, fmt.Errorf("%s: variant %s: source of %s: %w", path, v.Name, lang, err)
			}
		}
	}
	if err := checkAppCrates(&r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for lang, src := range r.Sources {
		if !slices.Contains(languages, lang) {
			return nil, fmt.Errorf("%s: unknown language %q", path, lang)
		}
		if _, err := os.Stat(filepath.Join(r.Dir, src)); err != nil {
			return nil, fmt.Errorf("%s: source of %s: %w", path, lang, err)
		}
	}
	return &r, nil
}

// checkBaseline refuses a baseline (row.Baseline) that is not the publish
// arguments with some changed (the repetitions set to 0), or that belongs to
// a row reported in time only.
func checkBaseline(base, args []string, bytes int64) error {
	if len(base) == 0 {
		return nil
	}
	if bytes <= 0 {
		return errors.New("baseline on a row with no bytes: only a GB/s row subtracts its fill")
	}
	if len(base) != len(args) || slices.Equal(base, args) {
		return fmt.Errorf("baseline %q is not the arguments %q with the repetitions set to 0", base, args)
	}
	return nil
}

// languages in table order.
var languages = []string{"mk", "mk-wrap", "mk-keyed", "mk-ctx", "c", "rust", "zig", "go", "cs"}

// isMkLang reports whether a source language is built by mk (the mk column
// and its wrapping, keyed and context-struct twins).
func isMkLang(lang string) bool {
	return lang == "mk" || lang == "mk-wrap" || lang == "mk-keyed" || lang == "mk-ctx"
}

// parseMask reads a hex affinity mask ("0x4").
func parseMask(s string) (uint64, error) {
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	if err != nil || v == 0 {
		return 0, fmt.Errorf("bad affinity mask %q", s)
	}
	return v, nil
}

// discover finds every row: tests/bench/<table>/<row>/bench.json, then
// tests/bench-lanes/<lane>/**/bench.json. Rows come back sorted by table,
// then id, so every listing is deterministic.
func discover(root string) ([]*row, error) {
	var rows []*row
	bench := filepath.Join(root, "tests", "bench")
	matches, _ := filepath.Glob(filepath.Join(bench, "*", "*", "bench.json"))
	for _, m := range matches {
		r, err := loadRow(m, "")
		if err != nil {
			return nil, err
		}
		rows = append(rows, expand(r)...)
	}
	lanes := filepath.Join(root, "tests", "bench-lanes")
	err := filepath.WalkDir(lanes, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == lanes {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() || d.Name() != "bench.json" {
			return nil
		}
		rel, _ := filepath.Rel(lanes, path)
		lane := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		r, err := loadRow(path, lane)
		if err != nil {
			rows = append(rows, brokenRow(path, lane, err))
			return nil
		}
		rows = append(rows, expand(r)...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	lr, err := loadLaneReview(root)
	if err != nil {
		return nil, err
	}
	applyLaneReview(lr, rows)
	slices.SortFunc(rows, func(a, b *row) int {
		if c := strings.Compare(a.Table, b.Table); c != 0 {
			return c
		}
		return strings.Compare(a.id(), b.id())
	})
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Broken != "" {
			continue
		}
		k := r.Table + " " + r.id()
		if seen[k] {
			return nil, fmt.Errorf("two rows named %s in table %s", r.id(), r.Table)
		}
		seen[k] = true
	}
	return rows, nil
}

// brokenRow is the stand-in for a lane row that did not load: its name and
// table as far as they can be read, and the reason.
func brokenRow(path, lane string, err error) *row {
	r := &row{Dir: filepath.Dir(path), Lane: lane, Broken: err.Error()}
	if b, rerr := os.ReadFile(path); rerr == nil {
		var head struct{ Name, Table string }
		if json.Unmarshal(b, &head) == nil {
			r.Name, r.Table = head.Name, head.Table
		}
	}
	if r.Name == "" {
		r.Name = filepath.Base(r.Dir)
	}
	return r
}

// brokenRows is the rows of rows that did not load.
func brokenRows(rows []*row) []*row {
	var out []*row
	for _, r := range rows {
		if r.Broken != "" {
			out = append(out, r)
		}
	}
	return out
}

// selectRows keeps the rows whose id or name is in names (all rows when names
// is empty), and only those of table when table is not "". A broken lane row
// is left out of an unnamed selection and refused when named.
func selectRows(rows []*row, table string, names []string) ([]*row, error) {
	var out []*row
	found := map[string]bool{}
	brokenNamed := map[string]*row{}
	for _, r := range rows {
		if table != "" && r.Table != table {
			continue
		}
		if len(names) > 0 && !slices.Contains(names, r.id()) && !slices.Contains(names, r.Name) {
			continue
		}
		if r.Broken != "" {
			// Refused when named by its id; a lane row that only shares
			// its bare name with the row asked for (std-simd/dot-l1v3 and
			// dot-l1v3) is left out instead.
			if len(names) > 0 && slices.Contains(names, r.id()) {
				return nil, fmt.Errorf("row %s does not load, so it cannot be run: %s", r.id(), r.Broken)
			}
			if brokenNamed[r.Name] == nil {
				brokenNamed[r.Name] = r
			}
			continue
		}
		found[r.id()], found[r.Name] = true, true
		out = append(out, r)
	}
	for _, n := range names {
		if !found[n] {
			if b := brokenNamed[n]; b != nil {
				return nil, fmt.Errorf("row %s does not load, so it cannot be run: %s", b.id(), b.Broken)
			}
			return nil, fmt.Errorf("no row %q", n)
		}
	}
	return out, nil
}

// strayProfiles is the stray-profile refusal: any default.mkprof or
// default.pgo under the benchmark trees would switch PGO on for one column
// silently.
func strayProfiles(root string) []string {
	var found []string
	for _, top := range []string{filepath.Join(root, "tests", "bench"), filepath.Join(root, "tests", "bench-lanes")} {
		filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() && (d.Name() == "default.mkprof" || d.Name() == "default.pgo") {
				found = append(found, path)
			}
			return nil
		})
	}
	return found
}

// repoRoot walks up from dir to the directory holding go.mod with module mk.
func repoRoot(dir string) (string, error) {
	for d := dir; ; {
		b, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if f := strings.Fields(string(b)); err == nil && len(f) >= 2 && f[0] == "module" && f[1] == "mk" {
			return d, nil
		}
		p := filepath.Dir(d)
		if p == d {
			return "", fmt.Errorf("no go.mod of module mk above %s", dir)
		}
		d = p
	}
}

// dataRoot, when set, holds the data files instead of tests/bench/data (the
// run inside WSL keeps them on the Linux file system).
var dataRoot string

func dataDir(root string) string {
	if dataRoot != "" {
		return dataRoot
	}
	return filepath.Join(root, "tests", "bench", "data")
}
