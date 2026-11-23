// Why: a timed run must time the program the sources describe, and must
// refuse a column whose program prints the wrong answer.
//
// What: the build half of `tools/bench`: every column of a row compiled into
// the work directory, the build stamp, runOnce, and the checksum check (the
// runner rejects a run whose checksum differs).
//
// How: a column is skipped when the stamp of its command, its toolchain and
// its sources is unchanged. Before a checksum is recorded, a majority of
// the columns must agree on one.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var lookPath = exec.LookPath

// built is one column's executable.
type built struct {
	Row    *row
	Col    column
	Exe    string
	Cmd    buildCmd
	Err    error
	Output string // the compiler's output when it failed
}

// exeName is where column c of row r is built.
func exeName(work, tag string, r *row, c column) string {
	name := strings.ReplaceAll(r.id(), "/", "_")
	return filepath.Join(work, "bin", tag, name, c.ID+".exe")
}

// stamp hashes the command line, the toolchain's version text (cmd.Deps),
// every source file the column reads (a crate's Cargo.toml and Cargo.lock
// among them), every file of a Rust column's crates tree, the mk
// executable for the Mk columns and every file of the std tree an Mk
// build's MK_STD names, so an unchanged column is not rebuilt and a changed
// one always is (a compiler upgraded in place, a rustup proxy whose
// bytes never change, or a std or a vendored crate edited at the same path
// all change the stamp).
func stamp(r *row, c column, cmd buildCmd) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%q\n", cmd.Args)
	fmt.Fprintf(h, "%q\n", cmd.Env)
	fmt.Fprintf(h, "%q\n", cmd.Deps)
	if len(cmd.Unset) > 0 {
		fmt.Fprintf(h, "unset %q\n", cmd.Unset)
	}
	var files []string
	walk := func(root string, mkPkg bool) error {
		return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && mkPkg && p != root && d.Name() == "gen" {
				// mk build writes the views of a package's gen lines
				// into gen/<target>/ (git-ignored, never read by the
				// compiler), so they are output, not source: a dogfood
				// row (app.go) would otherwise rebuild after its first
				// build.
				return filepath.SkipDir
			}
			if !d.IsDir() {
				files = append(files, p)
			}
			return nil
		})
	}
	if err := walk(filepath.Join(r.Dir, r.Sources[c.Lang]), isMkLang(c.Lang)); err != nil {
		return "", err
	}
	files = append(files, vendorFiles(r, c.Lang)...)
	files = append(files, cmd.Inputs...)
	if c.Lang == "rust" && r.Crates != "" && cargoDir(r) != "" {
		// Every file of the row's crates tree: a vendored file
		// edited in place, with its .cargo-checksum.json entry, is a
		// changed build.
		dir, err := cratesDir(r)
		if err != nil {
			return "", err
		}
		if err := walk(dir, false); err != nil {
			return "", fmt.Errorf("crates: %w", err)
		}
	}
	if std, ok := envValue(cmd.Env, "MK_STD"); ok && std != "" {
		if err := walk(std, false); err != nil {
			return "", fmt.Errorf("MK_STD: %w", err)
		}
	}
	if isMkLang(c.Lang) {
		// The mk executable, or the prebuilt program a Linux run copies.
		if cmd.Args[0] == "cp" {
			files = append(files, cmd.Args[1])
		} else {
			files = append(files, cmd.Args[0])
		}
	}
	slices.Sort(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s %d\n", filepath.ToSlash(f), len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// buildAll builds every column of rows (the check-budget columns only when
// only selects them), one at a time (the compilers are
// parallel themselves, and the machine is shared), and returns them in row
// then column order. A failed build is reported in its built.Err.
func buildAll(t *toolchain, tag string, rows []*row, only func(column) bool, log func(string)) []*built {
	var out []*built
	for _, r := range rows {
		for _, c := range allRowColumns(r) {
			if !wantColumn(c, only) {
				continue
			}
			b := &built{Row: r, Col: c, Exe: exeName(t.Work, tag, r, c)}
			b.Cmd, b.Err = t.command(r, c, b.Exe)
			if b.Err == nil {
				b.Output, b.Err = buildOne(b, log)
			}
			if b.Err != nil {
				log(fmt.Sprintf("build %s %s: %v", r.id(), c.ID, b.Err))
			}
			out = append(out, b)
		}
	}
	return out
}

// wantColumn is buildAll's selection: a check-budget column only when only
// selects it, any other column unless only refuses it.
func wantColumn(c column, only func(column) bool) bool {
	if only == nil {
		return !c.Budget
	}
	return only(c)
}

func buildOne(b *built, log func(string)) (string, error) {
	st, err := stamp(b.Row, b.Col, b.Cmd)
	if err != nil {
		return "", err
	}
	stampFile := b.Exe + ".stamp"
	if old, err := os.ReadFile(stampFile); err == nil && string(old) == st {
		if _, err := os.Stat(b.Exe); err == nil {
			return "", nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(b.Exe), 0o755); err != nil {
		return "", err
	}
	os.Remove(stampFile)
	if b.Cmd.Product != "" {
		// A cargo build into its column's CARGO_TARGET_DIR: cargo would
		// take a directory-source package at an unchanged version as
		// fresh and reuse its old compiled form, so a build whose stamp
		// changed (or is missing) starts from an empty target directory.
		if target, ok := envValue(b.Cmd.Env, "CARGO_TARGET_DIR"); ok && target != "" {
			if err := os.RemoveAll(target); err != nil {
				return "", fmt.Errorf("clearing the build's CARGO_TARGET_DIR: %w", err)
			}
		}
	}
	if b.Cmd.Prep != nil {
		if err := b.Cmd.Prep(); err != nil {
			return "", err
		}
	}
	start := time.Now()
	cmd := exec.Command(b.Cmd.Args[0], b.Cmd.Args[1:]...)
	cmd.Dir = b.Cmd.Dir
	cmd.Env = append(buildEnv(os.Environ(), b.Cmd.Unset), b.Cmd.Env...)
	outb, err := cmd.CombinedOutput()
	if err != nil {
		return string(outb), fmt.Errorf("%v: %s", err, firstLines(string(outb), 20))
	}
	if b.Cmd.Product != "" {
		// A build that names its own output (cargo, dotnet): copied to the
		// column's executable (plus any managed runtime sidecar files).
		if err := copyVendorFile(b.Cmd.Product, b.Exe); err != nil {
			return string(outb), fmt.Errorf("copying the build's product: %w", err)
		}
		if len(b.Cmd.Sidecars) > 0 {
			if err := copySidecars(b.Cmd.Product, b.Exe, b.Cmd.Sidecars); err != nil {
				return string(outb), err
			}
		}
	}
	log(fmt.Sprintf("built %s %s in %.1fs", b.Row.id(), b.Col.ID, time.Since(start).Seconds()))
	return string(outb), os.WriteFile(stampFile, []byte(st), 0o644)
}

// buildEnv is base without the variables unset names; an entry ending in
// "*" names every variable with that prefix.
func buildEnv(base, unset []string) []string {
	if len(unset) == 0 {
		return base
	}
	return slices.DeleteFunc(slices.Clone(base), func(kv string) bool {
		return slices.ContainsFunc(unset, func(k string) bool {
			if p, ok := strings.CutSuffix(k, "*"); ok {
				return envKeyHasPrefix(kv, p) // "CARGO_PROFILE_*"
			}
			return envKeyIs(kv, k)
		})
	})
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = append(lines[:n], fmt.Sprintf("... (%d more lines)", len(lines)-n))
	}
	return strings.Join(lines, "\n")
}

// runOnce runs exe with args and returns its trimmed standard output and its
// wall time. A nonzero exit is an error carrying standard error.
func runOnce(exe string, args []string) (string, time.Duration, error) {
	return runOnceEnv(exe, args, runEnv(os.Environ())) // GOGC, GOMAXPROCS and the runtime knobs pinned
}

// runOnceEnv is runOnce with the program's whole environment given (rowEnv).
func runOnceEnv(exe string, args, env []string) (string, time.Duration, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	d := time.Since(start)
	if err != nil {
		return "", d, fmt.Errorf("%s: %v: %s", filepath.Base(exe), err, firstLines(stderr.String(), 10))
	}
	return strings.TrimSpace(stdout.String()), d, nil
}

// checkResult is one column's verify run.
type checkResult struct {
	B     *built
	Sum   string
	Time  time.Duration
	Err   error
	Wrong bool // the checksum differs from the row's or from the other columns'
	// An app row's facts of the run (app.go): the program's peak memory
	// and the size of the file it wrote.
	Peak     memPeak
	OutBytes int64
	// Output check "then"'s cross-check (crossThen): the other columns'
	// files this column's Then run read, and how many of those runs
	// printed the checksum.
	CrossRuns, CrossOK int
}

// verify runs every built column once with args and checks that each prints
// the expected checksum (want), or, when want is "", that all agree. It
// reports whether the row passed and the checksum the columns printed.
func verify(bs []*built, args []string, want string) ([]checkResult, string, bool) {
	var res []checkResult
	counts := map[string]int{}
	for _, b := range bs {
		cr := checkResult{B: b}
		switch {
		case b.Err != nil:
			cr.Err = b.Err
		case b.Row.Table == "app":
			cr = checkApp1(b, args)
		default:
			cr.Sum, cr.Time, cr.Err = runOnceEnv(b.Exe, args, cellEnv(b.Row, b.Col))
		}
		if cr.Err == nil {
			counts[cr.Sum]++
		}
		res = append(res, cr)
	}
	agreed := want
	if agreed == "" {
		// Before a checksum is recorded, a majority of the columns must
		// agree; a plurality or a sort order picks no winner.
		agreed, _ = majority(counts, len(res))
	}
	// An app row whose output check is then: every column reads every
	// other's file (app.go); a failed pair becomes a column's error.
	crossThen(res, agreed)
	ok := true
	for i := range res {
		if res[i].Err != nil {
			ok = false
			continue
		}
		if res[i].Sum != agreed {
			res[i].Wrong = true
			ok = false
		}
	}
	return res, agreed, ok
}
