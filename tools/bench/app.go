// Why: the app table (tests/bench/README.md "Applications") compares real
// programs that use std, one task per row, and needs rules the other tables
// do not have.
//
// What: the app rows' field checks (cellKinds, lib, checkAppCrates,
// checkMkPackage), the per-row environment (appEnvRefused), the output
// digest and round trip, crossThen and crossLine, cellRun, and the record of
// a program's peak memory.
//
// How: each cell is labelled by its kind (std, eco, hand); the Mk side is a
// package elsewhere in the repository (a dogfood, unchanged). Outputs are
// compared by digest or read back by every other column's Then; peak memory
// is read by mem_*.go.

package main

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

// cellKinds are the kinds an app cell may have (no tool columns).
var cellKinds = []string{"std", "eco", "hand"}

// lib is one pinned library of an eco cell: its name and exact
// version, the language it is written in, and what is inside it that the
// program's own source may not use (unsafe code, SIMD, C, assembly), or
// "none".
type lib struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Lang    string `json:"lang"`
	Inside  string `json:"inside"`
}

func (l lib) String() string {
	return fmt.Sprintf("%s %s (%s; inside: %s)", l.Name, l.Version, l.Lang, l.Inside)
}

// pkgSpec is an Mk side that is a package elsewhere in the repository.
type pkgSpec struct {
	Path string `json:"path"`          // repository-relative, slash-separated, e.g. "Dogfoods/orderbook"
	Bin  string `json:"bin,omitempty"` // the bin to build (mk build --bin) when the package has several
}

// outputSpec is a file the program writes. {out} in the arguments (and in
// Then) is the column's own path for it, under the work directory, deleted
// before every run (hyperfine --prepare when timed). Check says what the
// checksum gains from it:
//   - "digest": the SHA-256 of the file;
//   - "zlib" or "gzip": the round trip: Go's compress/zlib or compress/gzip
//     decodes the file, the result must equal the row's data file byte for
//     byte, and the checksum gains its SHA-256 (output bytes legitimately
//     differ by library, so no digest of them is compared).
//   - "then": nothing: the file is the program's own format, whose bytes
//     legitimately differ by library (an archive's DEFLATE stream), and Then,
//     which reads it back, is the check. Then is required and names
//     {out}. verify also runs each column's Then on every other column's
//     file, and each of those runs must print the checksum too (crossThen).
//
// Then, when given, is a second untimed run of the same program after each
// checked run (an archive's "open" after its "create"): its standard output
// is the checksum's stdout part instead of the first run's.
type outputSpec struct {
	File  string   `json:"file"`
	Check string   `json:"check"`
	Then  []string `json:"then,omitempty"`
}

// outArg is the placeholder for a column's output path.
const outArg = "{out}"

// appEnvRefused is what a row's env may not set: the variables runEnv pins
// or clears for every program and the Mk profiler's. A concurrency
// row's MK_CARRIERS comes from its thread count.
var appEnvRefused = []string{"GOGC", "GOMAXPROCS", "GODEBUG", "GOMEMLIMIT", "GOTRACEBACK", "MK_IO", "MK_PROFILE_DIR", "MK_INHERIT", "MK_STD", "MK_CACHE"}

// mkPkgForeign finds a pkg.mk field that links code other than Mk into the
// package (C or assembly sources, C imports, libraries), or a dependency,
// whose own c_sources, c_imports, asm_sources and unsafe modules apply to
// it and link into the program (see doc/reference.md): an
// app row's Mk cell is always kind std and is one package, so it may have
// none of them.
var mkPkgForeign = regexp.MustCompile(`\b(c_sources|c_imports|asm_sources|links|deps)\s*:\s*\[\s*[^\]\s]`)

// checkMkPackage checks the Mk package of one row (the row's own or a
// variant's, expand) of an app table: its pkg.mk has none of mkPkgForeign's
// fields.
func checkMkPackage(r *row) error {
	for lang, src := range r.Sources {
		if !isMkLang(lang) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(r.Dir, src, "pkg.mk"))
		if err != nil {
			continue
		}
		switch m := mkPkgForeign.FindSubmatch(b); {
		case m == nil:
		case string(m[1]) == "deps":
			return errors.New("the Mk package sets deps: the Mk cell is one package of Mk code under the row's checks, and a dependency's own c_sources, unsafe modules and vector code would link into it unchecked")
		default:
			return fmt.Errorf("the Mk package sets %s: the Mk cell is kind std and links no code but Mk's own", m[1])
		}
	}
	return nil
}

// checkAppCrates checks every Rust crate a row builds, the row's own and
// each variant's (expand): only the app table builds a crate, and each one
// passes checkCargoSource. On the app table it checks each one's Mk
// package too (checkMkPackage).
func checkAppCrates(r *row) error {
	crates := false
	for _, x := range expand(r) {
		variant := func(err error) error {
			if x != r {
				return fmt.Errorf("variant %s: %w", strings.TrimPrefix(x.Name, r.Name+"-"), err)
			}
			return err
		}
		if r.Table == "app" {
			if err := checkMkPackage(x); err != nil {
				return variant(err)
			}
		}
		if cargoDir(x) == "" {
			continue
		}
		crates = true
		if r.Table != "app" {
			return errors.New("sources.rust names a crate directory, which only the app table builds (with cargo)")
		}
		if err := checkCargoSource(x); err != nil {
			return variant(err)
		}
	}
	if r.Crates != "" {
		// Directory source replacement: the field's form is
		// checked here, the directory itself when a crate is built
		// (cratesConfig), so a missing tree costs the Rust cells only.
		if !crates {
			return errors.New("crates names the crates of a Rust crate, and the row builds none")
		}
		if err := vendorRel(r.Crates); err != nil {
			return fmt.Errorf("crates: %w", err)
		}
	}
	return nil
}

// checkApp checks the app table's fields of a row read by loadRow: refused
// on every other table, complete on an app row. It also points the Mk
// source at Package.
func checkApp(r *row) error {
	if r.Table != "app" {
		if len(r.Kinds) > 0 || len(r.Libs) > 0 || len(r.Hand) > 0 || len(r.Labels) > 0 || len(r.Env) > 0 || r.Package != nil || r.Digest || r.Output != nil || r.Crates != "" {
			return errors.New("kinds, libs, hand, labels, env, package, digest, output and crates belong to the app table only")
		}
		for _, v := range r.Variants {
			if v.Output != nil {
				return fmt.Errorf("variant %s: output belongs to the app table only", v.Name)
			}
		}
		// A crate builds with its dependencies (cargo.go): only an app
		// row's eco cell may have them, with its lockfile checked. The
		// crates are checked by checkAppCrates once the variants are.
		return nil
	}
	if len(r.Counts) > 0 {
		return errors.New("an app row has no counting builds: its check is the output")
	}
	if len(r.Baseline) > 0 || r.Bytes > 0 {
		return errors.New("an app row has no baseline and no bytes: GB/s and the fill baseline are the SIMD table's")
	}
	if p := r.Package; p != nil {
		if _, ok := r.Sources["mk"]; ok {
			return errors.New("package and sources.mk both name the Mk side")
		}
		dir, err := resolvePackage(r.Dir, p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(r.Dir, dir)
		if err != nil {
			return err
		}
		if r.Sources == nil {
			r.Sources = map[string]string{}
		}
		r.Sources["mk"] = rel
	}
	if r.Package == nil {
		sets := [][]string{r.Args, r.Quick}
		if r.Output != nil {
			sets = append(sets, r.Output.Then)
		}
		for _, v := range r.Variants {
			sets = append(sets, v.Args, v.Quick)
			if v.Output != nil {
				sets = append(sets, v.Output.Then)
			}
		}
		for _, args := range sets {
			for _, a := range args {
				if strings.Contains(a, pkgArg) {
					return fmt.Errorf("argument %q names %s, the directory of the package, and the row names none", a, pkgArg)
				}
			}
		}
	}
	for lang := range r.Sources {
		k := r.Kinds[lang]
		if isMkLang(lang) && lang != "mk" {
			return fmt.Errorf("an app row has no %s column: its Mk side is the one mk program", lang)
		}
		if !slices.Contains(cellKinds, k) {
			return fmt.Errorf("language %s has kind %q: every cell of an app row is std, eco or hand", lang, k)
		}
		if isMkLang(lang) && k != "std" {
			return fmt.Errorf("the Mk cell %s is %q: Mk's cell uses its std only", lang, k)
		}
		// The Mk cell is always std: its package, and each variant's,
		// links no C, assembly, library or dependency (checkMkPackage,
		// run by checkAppCrates once the variants are checked).
		libs := r.Libs[lang]
		if (k == "eco") != (len(libs) > 0) {
			return fmt.Errorf("language %s: an eco cell names its libraries in libs, and only an eco cell has libs", lang)
		}
		for _, l := range libs {
			if l.Name == "" || l.Version == "" || l.Lang == "" || l.Inside == "" {
				return fmt.Errorf("language %s: library %q needs a name, a version, its language and what is inside it", lang, l.Name)
			}
		}
		if (k == "hand") != (r.Hand[lang] != "") {
			return fmt.Errorf("language %s: a hand cell says in hand what it writes itself, and only a hand cell has that", lang)
		}
	}
	for _, m := range []map[string]string{r.Kinds, r.Hand, r.Labels} {
		for lang, text := range m {
			if _, ok := r.Sources[lang]; !ok {
				return fmt.Errorf("kinds, hand or labels names %s, which has no source", lang)
			}
			if text == "" {
				return fmt.Errorf("language %s: an empty kind, hand or label", lang)
			}
		}
	}
	for lang := range r.Libs {
		if _, ok := r.Sources[lang]; !ok {
			return fmt.Errorf("libs names %s, which has no source", lang)
		}
	}
	for k := range r.Env {
		if k == "" || strings.ContainsAny(k, "= \t") {
			return fmt.Errorf("env name %q", k)
		}
		for _, x := range appEnvRefused {
			if envKeyIs(k+"=", x) {
				return fmt.Errorf("env sets %s, which tools/bench pins or clears for every program", k)
			}
		}
		if envKeyHasPrefix(k+"=", runEnvClearPrefix) {
			return fmt.Errorf("env sets %s: %s* is cleared for every program", k, runEnvClearPrefix)
		}
		if r.Concurrency != nil && envKeyIs(k+"=", "MK_CARRIERS") {
			return errors.New("env sets MK_CARRIERS on a concurrency row, whose thread count sets it")
		}
	}
	if v := r.Vendor; v != nil {
		// A vendored library is an eco cell's: never the Mk side's,
		// whose cell is always std, and only C's when C is eco and
		// names it.
		if !slices.Equal(v.For, []string{"c"}) {
			return errors.New(`vendor on an app row needs "for": ["c"]: the Mk cell is kind std and builds no vendored library`)
		}
		if r.Kinds["c"] != "eco" || len(r.Libs["c"]) == 0 {
			return errors.New("vendor gives C a library: C's cell is then eco and names it in libs")
		}
	}
	outs := []*outputSpec{r.Output}
	for _, v := range r.Variants {
		outs = append(outs, v.Output)
	}
	for _, o := range outs {
		if err := checkOutput(r, o); err != nil {
			return err
		}
	}
	// {out} only where the row (or its variant) has an output.
	argSets := map[*outputSpec][][]string{r.Output: {r.Args, r.Quick}}
	for _, v := range r.Variants {
		o := r.Output
		if v.Output != nil {
			o = v.Output
		}
		argSets[o] = append(argSets[o], v.Args, v.Quick)
	}
	for o, sets := range argSets {
		uses := false
		for _, args := range sets {
			for _, a := range args {
				uses = uses || strings.Contains(a, outArg)
			}
		}
		if o == nil && uses {
			return fmt.Errorf("an argument names %s but the row has no output", outArg)
		}
		if o != nil && !uses && !slices.ContainsFunc(o.Then, func(a string) bool { return strings.Contains(a, outArg) }) {
			return fmt.Errorf("output %s is never named: write %s in args, quick or then", o.File, outArg)
		}
	}
	return nil
}

// checkOutput checks one output spec (nil is none).
func checkOutput(r *row, o *outputSpec) error {
	if o == nil {
		return nil
	}
	if o.File == "" || filepath.Base(o.File) != o.File || strings.ContainsAny(o.File, " \t\"'&|<>^%;$`") {
		return fmt.Errorf("output file %q must be a plain file name with no spaces or shell characters", o.File)
	}
	switch o.Check {
	case "digest":
	case "zlib", "gzip":
		if r.Data == nil || r.Data.Kind == "tree" {
			return fmt.Errorf("output check %s is a round trip to the data file, and the row has no data file", o.Check)
		}
	case "then":
		if len(o.Then) == 0 {
			return errors.New("output check then compares only what the then run prints, and the output has no then")
		}
		if !slices.ContainsFunc(o.Then, func(a string) bool { return strings.Contains(a, outArg) }) {
			return fmt.Errorf("output check then: the then run must read the output, and names no %s", outArg)
		}
	default:
		return fmt.Errorf("output check %q is not digest, zlib, gzip or then", o.Check)
	}
	return nil
}

// resolvePackage is the absolute directory of an app row's Mk package: a
// repository-relative path with a pkg.mk, and a bin named when it has
// several.
func resolvePackage(rowDir string, p *pkgSpec) (string, error) {
	if p.Path == "" || filepath.IsAbs(p.Path) || strings.HasPrefix(p.Path, "/") || slices.Contains(strings.Split(p.Path, "/"), "..") || strings.Contains(p.Path, `\`) {
		return "", fmt.Errorf("package path %q must be repository-relative and slash-separated, without ..", p.Path)
	}
	root, err := repoRoot(rowDir)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, filepath.FromSlash(p.Path))
	b, err := os.ReadFile(filepath.Join(dir, "pkg.mk"))
	if err != nil {
		return "", fmt.Errorf("package %s: %w", p.Path, err)
	}
	if strings.Count(string(b), "pkg.Bin(") > 1 && p.Bin == "" {
		return "", fmt.Errorf("package %s has several bins: name one in package.bin", p.Path)
	}
	return dir, nil
}

// pkgFact is what a results file records about a row's Mk package: the
// last commit that touched it and whether the tree differs from it there.
type pkgFact struct {
	Path   string `json:"path"`
	Bin    string `json:"bin,omitempty"`
	Commit string `json:"commit"`
	Dirty  bool   `json:"dirty"`
	// Unknown says why the commit and state could not be read (git
	// failed, or no commit touched the path); Commit and Dirty then
	// claim nothing.
	Unknown string `json:"unknown,omitempty"`
}

// packageFact reads p's commit and state from git.
func packageFact(root string, p *pkgSpec) *pkgFact {
	if p == nil {
		return nil
	}
	return packageFactWith(root, p, "git")
}

// packageFactWith is packageFact with the git binary given (a test names
// one that fails).
func packageFactWith(root string, p *pkgSpec, git string) *pkgFact {
	g := func(args ...string) (string, error) {
		out, err := exec.Command(git, append([]string{"-c", "safe.directory=*", "-C", root}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	f := &pkgFact{Path: p.Path, Bin: p.Bin}
	commit, err1 := g("log", "-1", "--format=%H", "--", p.Path)
	status, err2 := g("status", "--porcelain", "--untracked-files=all", "--", p.Path)
	switch {
	case err1 != nil || err2 != nil:
		f.Unknown = "git failed: " + firstLines(fmt.Sprint(errors.Join(err1, err2)), 1)
	case commit == "":
		f.Unknown = "no commit has touched it"
		f.Dirty = status != ""
	default:
		f.Commit, f.Dirty = commit, status != ""
	}
	return f
}

// outPath is column b's output file: beside its executable, one directory
// per column, so no two columns or builds share it.
func outPath(b *built) string {
	if b.Row.Output == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(b.Exe), "out-"+b.Col.ID, b.Row.Output.File)
}

// cellArgs is args with {out} replaced by b's output path.
func cellArgs(b *built, args []string) []string {
	p := outPath(b)
	if p == "" {
		return args
	}
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, outArg, p)
	}
	return out
}

// freshOutput deletes b's output file before a run and makes its directory.
func freshOutput(b *built) error {
	p := outPath(b)
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// prepareCmd is the hyperfine --prepare command that deletes the output
// file before every timed run. hyperfine runs it through the platform
// shell even under -N (cmd.exe on Windows, sh elsewhere), so the path is
// quoted: in double quotes for cmd.exe, which still expands % there, and
// in single quotes for sh. A path holding a quote, a % or a line break is
// refused.
func prepareCmd(path, goos string) (string, error) {
	if strings.ContainsAny(path, "\"'%\r\n") {
		return "", fmt.Errorf("output path %q holds a quote, a %% or a line break; use a -work without them", path)
	}
	if goos == "windows" {
		return `if exist "` + path + `" del /f /q "` + path + `"`, nil
	}
	return "rm -f '" + path + "'", nil
}

// normalise is the text an app row's digest is taken over: CR LF read as
// LF, and the row's data path, the column's output path and the package
// directory {pkg} names, in both slash forms, written back as $DATA, $OUT
// and $PKG, so the digest does not depend on where the work, data and
// repository directories are.
func normalise(out []byte, dataPath, outFile string, pkgPath ...string) []byte {
	s := bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n"))
	paths := []struct{ path, name string }{{dataPath, "$DATA"}, {outFile, "$OUT"}}
	for _, p := range pkgPath {
		paths = append(paths, struct{ path, name string }{p, "$PKG"})
	}
	for _, p := range paths {
		if p.path == "" {
			continue
		}
		// Both slash forms on every OS (filepath.ToSlash does nothing on
		// Linux); the forms have one length, so the order does not matter.
		forms := []string{p.path, strings.ReplaceAll(p.path, `\`, "/"), strings.ReplaceAll(p.path, "/", `\`)}
		for _, f := range forms {
			s = bytes.ReplaceAll(s, []byte(f), []byte(p.name))
		}
	}
	return s
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// memPeak is a program's peak memory, read once per checked run, never
// timed and never a ratio: the peak working set (Windows) or maximum
// resident set (Linux), and on Windows the peak commit.
//
// On Linux (mem_linux.go) the reading is the program's ru_maxrss as
// its spawner read it (Maxrss), which carries the spawner's own peak, the
// floor (Floor), recorded beside it. RSS is the reading only when it is
// above the floor; at or under it RSS is 0, and the cell prints "at most"
// the reading (peakText), never the reading as the program's peak.
type memPeak struct {
	RSS     uint64 `json:"peak_rss_bytes,omitempty"`
	Commit  uint64 `json:"peak_commit_bytes,omitempty"`
	Maxrss  uint64 `json:"maxrss_bytes,omitempty"`
	Floor   uint64 `json:"spawner_floor_bytes,omitempty"`
	Spawner string `json:"spawner,omitempty"` // peakspawn, or tools/bench (the fallback)
}

// runProgram runs exe once with env and returns its whole standard output,
// its wall time and its peak memory (runWithPeak, mem_*.go). A nonzero exit
// is an error carrying standard error.
func runProgram(exe string, args, env []string) ([]byte, time.Duration, memPeak, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	peak, err := runWithPeak(cmd)
	d := time.Since(start)
	if err != nil {
		return nil, d, peak, fmt.Errorf("%s: %v: %s", filepath.Base(exe), err, firstLines(stderr.String(), 10))
	}
	return stdout.Bytes(), d, peak, nil
}

// checkApp1 is verify's check of one app column: a fresh output file, the
// run (with its peak memory), the Then run, and the checksum: the stdout
// part (its digest when the row asks for one, else the trimmed text), then
// the output part.
func checkApp1(b *built, args []string) checkResult {
	cr := checkResult{B: b}
	r := b.Row
	if cr.Err = freshOutput(b); cr.Err != nil {
		return cr
	}
	env := cellEnv(r, b.Col)
	var out []byte
	out, cr.Time, cr.Peak, cr.Err = runProgram(b.Exe, cellArgs(b, args), env)
	if cr.Err != nil {
		return cr
	}
	op := outPath(b)
	var fileBytes []byte
	if op != "" {
		if fileBytes, cr.Err = os.ReadFile(op); cr.Err != nil {
			cr.Err = fmt.Errorf("output %s: %w", r.Output.File, cr.Err)
			return cr
		}
		cr.OutBytes = int64(len(fileBytes))
	}
	if r.Output != nil && len(r.Output.Then) > 0 {
		if out, _, _, cr.Err = runProgram(b.Exe, cellArgs(b, thenArgs(r)), env); cr.Err != nil {
			cr.Err = fmt.Errorf("then %q: %w", r.Output.Then, cr.Err)
			return cr
		}
	}
	sum := stdoutSum(r, out, op)
	if r.Output != nil {
		part, err := outputSum(r.Output.Check, fileBytes, r.dataPath)
		if err != nil {
			cr.Err = err
			return cr
		}
		sum = strings.TrimSpace(sum + " " + part)
	}
	cr.Sum = sum
	return cr
}

// thenArgs is the row's Then run, with {data} replaced once prepareRows has
// run.
func thenArgs(r *row) []string {
	if r.runThen != nil {
		return r.runThen
	}
	return r.Output.Then
}

// stdoutSum is the checksum's stdout part: the digest of the normalised
// output when the row asks for one (op is the output path written back as
// $OUT), else the trimmed text.
func stdoutSum(r *row, out []byte, op string) string {
	if r.Digest {
		return "sha256:" + sha256Hex(normalise(out, r.dataPath, op, r.pkgPath))
	}
	return strings.TrimSpace(string(out))
}

// crossThen is verify's cross-check for output check "then". Each column's
// own Then run shows only that the column agrees with itself: a create and
// an open wrong in the same way pass it. So, once the columns agree on
// agreed, each passing column's Then runs on every other passing column's
// file, untimed, and must print agreed too. A failed pair is judged by the
// languages other than the reader's and the file's, since two columns of one
// language (rust and rust-ovf, zig-fast and zig-safe) build one source and
// would vote twice: a language reads a file when each of its passing columns
// prints agreed on it. When most of those languages read the file, the
// reader is wrong; when most fail on it, the file's column is; on a tie, or
// with no other language, both are marked. The verdict is that column's
// error, so publish excludes the column that is wrong and not its
// neighbours. Each reader's runs are counted in its result (crossLine). It
// reports whether every pair agreed. Columns of other rows or checks, and a
// single column, are left alone.
func crossThen(res []checkResult, agreed string) bool {
	var good []int
	for i, cr := range res {
		r := cr.B.Row
		if cr.Err == nil && !cr.Wrong && cr.Sum == agreed && r.Table == "app" && r.Output != nil && r.Output.Check == "then" && r.id() == res[0].B.Row.id() {
			good = append(good, i)
		}
	}
	if len(good) < 2 {
		return true
	}
	type miss struct {
		reader, file int
		why          string
	}
	var misses []miss
	failed := map[[2]int]bool{} // {reader, file}
	for _, f := range good {
		for _, rd := range good {
			if rd == f {
				continue
			}
			reader, file := res[rd].B, res[f].B
			out, _, _, err := runProgram(reader.Exe, cellArgs(file, thenArgs(reader.Row)), cellEnv(reader.Row, reader.Col))
			why := ""
			if err != nil {
				why = firstLines(err.Error(), 3)
			} else if sum := stdoutSum(reader.Row, out, outPath(file)); sum != agreed {
				why = "printed " + sum
			}
			res[rd].CrossRuns++
			if why != "" {
				misses = append(misses, miss{rd, f, why})
				failed[[2]int{rd, f}] = true
			} else {
				res[rd].CrossOK++
			}
		}
	}
	lang := func(i int) string { return res[i].B.Col.Lang }
	// reads says whether language l reads column f's file: each of its
	// passing columns printed agreed on it.
	reads := func(l string, f int) bool {
		for _, c := range good {
			if lang(c) == l && failed[[2]int{c, f}] {
				return false
			}
		}
		return true
	}
	errs := map[int][]error{}
	for _, m := range misses {
		readerID, fileID, name := res[m.reader].B.Col.ID, res[m.file].B.Col.ID, res[m.file].B.Row.Output.File
		judges, read := map[string]bool{}, 0
		for _, c := range good {
			if l := lang(c); l != lang(m.reader) && l != lang(m.file) && !judges[l] {
				judges[l] = true
				if reads(l, m.file) {
					read++
				}
			}
		}
		onFile := fmt.Errorf("cross-check: %s's then run does not read this column's %s: %s", readerID, name, m.why)
		onReader := fmt.Errorf("cross-check: this column's then run does not read %s's %s: %s", fileID, name, m.why)
		switch n := len(judges); {
		case 2*read > n:
			errs[m.reader] = append(errs[m.reader], onReader)
		case 2*(n-read) > n:
			errs[m.file] = append(errs[m.file], onFile)
		default:
			why := fmt.Sprintf(" (both marked: %d of %d other languages read the file)", read, n)
			errs[m.file] = append(errs[m.file], errors.New(onFile.Error()+why))
			errs[m.reader] = append(errs[m.reader], errors.New(onReader.Error()+why))
		}
	}
	for i, es := range errs {
		res[i].Err = errors.Join(es...)
	}
	return len(misses) == 0
}

// crossLine is verify's and publish's record of crossThen: how many pairs
// ran and how many printed the checksum, or "" when none ran.
func crossLine(res []checkResult) string {
	runs, ok := 0, 0
	for _, cr := range res {
		runs += cr.CrossRuns
		ok += cr.CrossOK
	}
	if runs == 0 {
		return ""
	}
	return fmt.Sprintf("cross-check: %d of %d pairs printed the checksum", ok, runs)
}

// outputSum is the output file's part of the checksum (outputSpec.Check).
func outputSum(check string, file []byte, dataPath string) (string, error) {
	switch check {
	case "digest":
		return "file sha256:" + sha256Hex(file), nil
	case "then":
		// The file's bytes differ by library; the Then run's output,
		// already the stdout part, is its check.
		return "", nil
	}
	var rd io.ReadCloser
	var err error
	br := bytes.NewReader(file)
	switch check {
	case "zlib":
		rd, err = zlib.NewReader(br)
	case "gzip":
		// Multistream: a byte after a member that does not start another
		// member is ErrHeader, and a second member decodes to more than
		// the input.
		rd, err = gzip.NewReader(br)
	default:
		return "", fmt.Errorf("output check %q", check)
	}
	if err != nil {
		return "", fmt.Errorf("round trip: Go's compress/%s does not read the output: %w", check, err)
	}
	dec, err := io.ReadAll(rd)
	if cerr := rd.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("round trip: Go's compress/%s does not read the output: %w", check, err)
	}
	if n := br.Len(); n != 0 {
		// The zlib reader stops at the Adler-32 (bytes.Reader is a
		// ByteReader, so flate reads no further): anything after it is
		// not the stream, and would count in the output's size.
		return "", fmt.Errorf("round trip: %d bytes follow the %s stream in the output", n, check)
	}
	in, err := os.ReadFile(dataPath)
	if err != nil {
		return "", fmt.Errorf("round trip: the input: %w", err)
	}
	if !bytes.Equal(dec, in) {
		return "", fmt.Errorf("round trip: the output decoded by Go's compress/%s is not the input (%d bytes against %d)", check, len(dec), len(in))
	}
	return "roundtrip sha256:" + sha256Hex(dec), nil
}

// cellRun is how one cell's program runs when it is timed: its arguments
// with {out} replaced by the column's output path, the hyperfine --prepare
// that deletes that file ("" when the row writes none), and its whole
// environment (rowEnv). publish's sweep and rerun and compare's pairs all
// take it from timedRun, so the three cannot drift apart.
type cellRun struct {
	Args    []string
	Prepare string
	Env     []string
}

// timedRun is cell b's cellRun for args. It deletes the output file and
// makes its directory, as timedPrepare does.
func timedRun(b *built, args []string) (cellRun, error) {
	prep, err := timedPrepare(b)
	if err != nil {
		return cellRun{}, err
	}
	return cellRun{Args: cellArgs(b, args), Prepare: prep, Env: cellEnv(b.Row, b.Col)}, nil
}

// appCellFacts is what publish records of an app cell from its checked
// run: its class by kind (appClass), its peak memory when the run
// succeeded and read one, and its output file's size.
func appCellFacts(c *cell, r *row, cr checkResult) {
	if r.Table != "app" {
		return
	}
	c.Class = appClass(r, cr.B.Col)
	if cr.Err == nil && (cr.Peak != memPeak{}) {
		p := cr.Peak
		c.Peak = &p
	}
	c.OutBytes = cr.OutBytes
}

// timedPrepare is the hyperfine --prepare of a timed cell ("" when the row
// writes no file).
func timedPrepare(b *built) (string, error) {
	p := outPath(b)
	if p == "" {
		return "", nil
	}
	if err := freshOutput(b); err != nil {
		return "", err
	}
	return prepareCmd(p, runtime.GOOS)
}
