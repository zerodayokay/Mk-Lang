// Why: fuzzing every target on every gate run costs hours; the fuzz step
// fuzzes only what a change can affect, each target alone, and remembers
// what last passed.
//
// What: fuzzSteps, fuzzTargets (Go targets by go test -list), fuzzPool,
// fuzzOne and its command fuzzArgv, changedFiles, affected, packageDigests and inputDigest, and the
// fuzz-state.json record (fuzzState) behind -since last-fuzz.
//
// How: a package's inputs digest is the SHA-256 of its own files combined
// with those of every module package it imports with its tests; a target
// whose digest matches its last passing run is skipped. fuzzPool runs the
// WSL-lock targets alone first, then the rest fuzzJobs at a time, never two
// socket targets at once.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fuzzTarget is one Fuzz function, or with mk a Mk test block that calls
// testing.Fuzz (fuzzmk.go): pkg its package directory, name the test's.
type fuzzTarget struct {
	pkg, name string
	mk        bool
}

// fuzzState is fuzz-state.json: for each fuzz package, the digest of its
// inputs (its own files and those of every module package it imports with
// its tests) when its targets last passed a fuzzed gate.
type fuzzState struct {
	Updated time.Time         `json:"updated"`
	Inputs  map[string]string `json:"inputs"`
}

// fuzzSteps run every selected fuzz target alone, and record the digests.
func (g *gate) fuzzSteps() {
	if g.skip["fuzz"] {
		g.skipStep("fuzz", "-skip fuzz")
		return
	}
	sel := g.newStep("fuzz selection")
	targets, err := g.fuzzTargets(sel)
	if err != nil {
		fail(sel, err.Error())
		return
	}
	mkTargets, err := mkFuzzTargets(g.root, g.selected)
	if err != nil {
		fail(sel, "Mk fuzz targets: "+err.Error())
		return
	}
	targets = append(targets, mkTargets...)
	var pkgs, goPkgs, mkPkgs []string
	for _, t := range targets {
		if !slices.Contains(pkgs, t.pkg) {
			pkgs = append(pkgs, t.pkg)
			if t.mk {
				mkPkgs = append(mkPkgs, t.pkg)
			} else {
				goPkgs = append(goPkgs, t.pkg)
			}
		}
	}
	digests, err := g.packageDigests()
	if err != nil {
		fail(sel, "package digests: "+err.Error())
		return
	}
	inputs := map[string]string{}
	for _, p := range pkgs {
		digest := g.inputDigest
		if slices.Contains(mkPkgs, p) {
			digest = g.mkInputDigest
		}
		if inputs[p], err = digest(p, digests); err != nil {
			fail(sel, err.Error())
			return
		}
	}
	g.rep.PackageDigests, g.rep.FuzzInputs = digests, inputs
	g.fuzzFound = []string{}
	for _, t := range targets {
		g.fuzzFound = append(g.fuzzFound, t.pkg+" "+t.name)
	}
	state := g.readFuzzState()
	chosen := targets
	if g.tier == "quick" {
		var affected map[string]bool
		var why string
		if g.since == "last-fuzz" {
			affected = map[string]bool{}
			for _, p := range pkgs {
				if state.Inputs[p] != inputs[p] {
					affected[p] = true
				}
			}
			why = fmt.Sprintf("-since last-fuzz: %d of %d fuzz packages have inputs that differ from %s (recorded %s)",
				len(affected), len(pkgs), g.statePath(), state.Updated.Format(time.RFC3339))
		} else if affected, why, err = g.affected(goPkgs); err != nil {
			fail(sel, err.Error())
			return
		} else if len(mkPkgs) > 0 {
			// Mk targets: a changed file under the package, or anything
			// ./cmd/mk depends on (the compiler and std).
			cmdMk, _, err := g.affected([]string{"./cmd/mk"})
			if err != nil {
				fail(sel, err.Error())
				return
			}
			changed, err := g.changedFiles(g.since)
			if err != nil {
				fail(sel, err.Error())
				return
			}
			for p := range mkAffected(mkPkgs, changed, cmdMk["./cmd/mk"]) {
				affected[p] = true
			}
		}
		sel.Notes = append(sel.Notes, why)
		chosen = nil
		for _, t := range targets {
			if affected[t.pkg] {
				chosen = append(chosen, t)
			}
		}
	}
	for _, t := range targets {
		mark := "not selected"
		if slices.Contains(chosen, t) {
			mark = "selected"
		}
		sel.Notes = append(sel.Notes, t.pkg+" "+t.name+": "+mark)
	}
	sel.Reason = fmt.Sprintf("%d of %d targets selected (%s tier, -since %s)", len(chosen), len(targets), g.tier, g.since)
	fmt.Printf("   %s\n", sel.Reason)
	passed := map[string]bool{}
	if g.fuzzPassed == nil {
		g.fuzzPassed = map[string]bool{}
	}
	record := func(t fuzzTarget, ok bool) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if _, seen := passed[t.pkg]; !seen {
			passed[t.pkg] = true
		}
		g.fuzzPassed[t.pkg+" "+t.name] = ok
		if !ok {
			passed[t.pkg] = false
		}
	}
	g.fuzzPool(chosen, record)
	if state.Inputs == nil {
		state.Inputs = map[string]string{}
	}
	for p, ok := range passed {
		if ok {
			state.Inputs[p] = inputs[p]
		}
	}
	state.Updated = time.Now()
	if err := g.writeFuzzState(state); err != nil {
		sel.Notes = append(sel.Notes, "fuzz state not written: "+err.Error())
	}
}

func (g *gate) statePath() string { return filepath.Join(g.work, "fuzz-state.json") }

func (g *gate) readFuzzState() fuzzState {
	var s fuzzState
	if b, err := os.ReadFile(g.statePath()); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (g *gate) writeFuzzState(s fuzzState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := g.statePath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, g.statePath())
}

// fuzzTargets lists the Fuzz functions of the packages with a
// "func Fuzz" in a test file, by go test -list.
func (g *gate) fuzzTargets(s *Step) ([]fuzzTarget, error) {
	var pkgs []string
	err := filepath.WalkDir(g.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); path != g.root && (strings.HasPrefix(n, ".") || n == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte("\nfunc Fuzz")) {
			rel, _ := filepath.Rel(g.root, filepath.Dir(path))
			if p := normPkg(rel); !slices.Contains(pkgs, p) && g.selected(p) {
				pkgs = append(pkgs, p)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(pkgs)
	var out []fuzzTarget
	for _, p := range pkgs {
		lines, err := g.goOut("test", "-list", "^Fuzz", p)
		if err != nil {
			return nil, fmt.Errorf("go test -list ^Fuzz %s: %v", p, err)
		}
		for _, l := range lines {
			if strings.HasPrefix(l, "Fuzz") && !strings.ContainsAny(l, " \t") {
				out = append(out, fuzzTarget{pkg: p, name: l})
			}
		}
	}
	s.Command = "go test -list ^Fuzz <each package with a Fuzz function>"
	return out, nil
}

// outZ runs argv in the root and returns its NUL-separated output fields.
func (g *gate) outZ(argv []string) ([]string, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = g.root
	cmd.Env = g.env(spec{})
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %v: %s", strings.Join(argv, " "), err, strings.TrimSpace(e.String()))
	}
	var f []string
	for _, s := range strings.Split(o.String(), "\x00") {
		if s != "" {
			f = append(f, s)
		}
	}
	return f, nil
}

// changedFiles are the files (slash paths from the root) that differ from
// rev in the working tree, committed or not, and every untracked file that
// is not ignored: git diff --name-only <rev> and git status --porcelain -uall.
func (g *gate) changedFiles(rev string) ([]string, error) {
	diff, err := g.outZ([]string{"git", "diff", "--name-only", "-z", rev})
	if err != nil {
		return nil, err
	}
	st, err := g.outZ([]string{"git", "status", "--porcelain", "-uall", "-z"})
	if err != nil {
		return nil, err
	}
	var out []string
	add := func(f string) {
		if f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	for _, f := range diff {
		add(f)
	}
	// Porcelain -z: "XY path"; a rename or copy is followed by its source.
	for i := 0; i < len(st); i++ {
		e := st[i]
		if len(e) < 4 {
			continue
		}
		add(e[3:])
		if e[0] == 'R' || e[0] == 'C' || e[1] == 'R' || e[1] == 'C' {
			if i+1 < len(st) {
				i++
				add(st[i])
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// pkgDirs maps each package directory of the module (absolute, cleaned)
// to its "./dir" name.
func (g *gate) pkgDirs() (map[string]string, error) {
	dirs, err := g.goOut("list", "-f", "{{.Dir}}", "./...")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, d := range dirs {
		d = filepath.Clean(d)
		rel, err := filepath.Rel(g.root, d)
		if err != nil {
			return nil, err
		}
		m[d] = normPkg(rel)
	}
	return m, nil
}

// owner is the package that file f (a slash path from the root) belongs
// to: the nearest directory above it that is a Go package, so
// std/core/x.mk belongs to std, which embeds it, and internal/x/testdata/y
// to internal/x. "" when no package holds it.
func (g *gate) owner(f string, dirs map[string]string) string {
	for d := filepath.Dir(filepath.Join(g.root, filepath.FromSlash(f))); len(d) >= len(g.root); d = filepath.Dir(d) {
		if p, ok := dirs[d]; ok {
			return p
		}
		if d == filepath.Dir(d) {
			break
		}
	}
	return ""
}

// affected is the set of pkgs whose own files, or those of a module
// package they import with their tests, changed since -since.
func (g *gate) affected(pkgs []string) (map[string]bool, string, error) {
	changed, err := g.changedFiles(g.since)
	if err != nil {
		return nil, "", err
	}
	dirs, err := g.pkgDirs()
	if err != nil {
		return nil, "", err
	}
	owners := map[string]bool{}
	all := false
	for _, f := range changed {
		if f == "go.mod" || f == "go.sum" {
			all = true
		}
		if p := g.owner(f, dirs); p != "" {
			owners[p] = true
		}
	}
	why := fmt.Sprintf("%d files changed since %s (git diff --name-only, git status --porcelain -uall), in %d packages", len(changed), g.since, len(owners))
	out := map[string]bool{}
	for _, p := range pkgs {
		deps, err := g.deps(p)
		if err != nil {
			return nil, "", err
		}
		if all || slices.ContainsFunc(deps, func(d string) bool { return owners[d] }) {
			out[p] = true
		}
	}
	if all {
		why += "; go.mod or go.sum changed, so every target is affected"
	}
	return out, why, nil
}

// deps are p and the module packages it imports with its tests, as "./dir".
func (g *gate) deps(p string) ([]string, error) {
	ds, err := g.goOut("list", "-deps", "-test", "-f", "{{if not .Standard}}{{.Dir}}{{end}}", p)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range ds {
		if d == "" {
			continue
		}
		rel, err := filepath.Rel(g.root, filepath.Clean(d))
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if n := normPkg(rel); !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// packageDigests is, for each package of the module, the SHA-256 of its
// files, tracked or untracked and not ignored, each file belonging to its
// owner: the .go files, testdata, and the embedded sources below it.
func (g *gate) packageDigests() (map[string]string, error) {
	tracked, err := g.outZ([]string{"git", "ls-files", "-z"})
	if err != nil {
		return nil, err
	}
	untracked, err := g.outZ([]string{"git", "ls-files", "-z", "--others", "--exclude-standard"})
	if err != nil {
		return nil, err
	}
	dirs, err := g.pkgDirs()
	if err != nil {
		return nil, err
	}
	files := map[string][]string{}
	for _, f := range append(tracked, untracked...) {
		if p := g.owner(f, dirs); p != "" && !slices.Contains(files[p], f) {
			files[p] = append(files[p], f)
		}
	}
	out := map[string]string{}
	for _, p := range dirs {
		list := files[p]
		sort.Strings(list)
		h := sha256.New()
		for _, f := range list {
			b, err := os.ReadFile(filepath.Join(g.root, filepath.FromSlash(f)))
			sum := "deleted"
			if err == nil {
				s := sha256.Sum256(b)
				sum = hex.EncodeToString(s[:])
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			fmt.Fprintf(h, "%s\x00%s\n", f, sum)
		}
		out[p] = hex.EncodeToString(h.Sum(nil))
	}
	return out, nil
}

// inputDigest combines the digests of p and of every module package it
// imports with its tests.
func (g *gate) inputDigest(p string, digests map[string]string) (string, error) {
	deps, err := g.deps(p)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, d := range deps {
		fmt.Fprintf(h, "%s\x00%s\n", d, digests[d])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var (
	fuzzExecs   = regexp.MustCompile(`execs: (\d+)`)
	fuzzCrasher = regexp.MustCompile(`Failing input written to (\S+)`)
)

// fuzzOne runs one fuzz target alone for -fuzztime, as
// go test -run=^$ -fuzz=^Name$ -fuzztime=D ./pkg, and reports whether it
// passed.
// fuzzPool runs the chosen targets: one after another at -fuzzjobs 1;
// otherwise the WSL-lock targets alone first, then the rest fuzzJobs at a
// time, each with -parallel NumCPU/fuzzJobs, the socket targets
// (fuzzOneAtATime) never two at once.
func (g *gate) fuzzPool(chosen []fuzzTarget, record func(fuzzTarget, bool)) {
	if g.fuzzJobs <= 1 {
		for _, t := range chosen {
			record(t, g.fuzzOne(t, 0, ""))
		}
		return
	}
	var rest []fuzzTarget
	for _, t := range chosen {
		if fuzzUnderLock[t.pkg+" "+t.name] {
			record(t, g.fuzzOne(t, 0, ""))
		} else {
			rest = append(rest, t)
		}
	}
	par := max(1, runtime.NumCPU()/g.fuzzJobs)
	var sockets sync.Mutex
	work := make(chan fuzzTarget)
	var wg sync.WaitGroup
	for range g.fuzzJobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range work {
				trip := filepath.Join(g.work, "tripwire-"+fileName(t.name)+".log")
				if fuzzOneAtATime[t.pkg+" "+t.name] {
					sockets.Lock()
					record(t, g.fuzzOne(t, par, trip))
					sockets.Unlock()
				} else {
					record(t, g.fuzzOne(t, par, trip))
				}
			}
		}()
	}
	for _, t := range rest {
		work <- t
	}
	close(work)
	wg.Wait()
}

// fuzzArgv is the go test command that fuzzes t for the tier's time: -timeout
// covers the whole binary, seed corpus and fuzzing alike; par above 0 sets
// -parallel; a target of fuzzMinimizeTime gets -fuzzminimizetime.
func (g *gate) fuzzArgv(t fuzzTarget, par int) []string {
	argv := []string{g.goExe, "test", "-run=^$", "-fuzz=^" + t.name + "$", "-fuzztime=" + g.fuzzTime.String(),
		"-timeout=" + (g.fuzzTime + 30*time.Minute).String(), t.pkg}
	if d, ok := fuzzMinimizeTime[t.pkg+" "+t.name]; ok {
		argv = slices.Insert(argv, 4, "-fuzzminimizetime="+d)
	}
	if par > 0 {
		argv = slices.Insert(argv, 4, "-parallel="+strconv.Itoa(par))
	}
	return argv
}

// fuzzOne fuzzes t for the tier's time; par above 0 sets go test's
// -parallel and trip the step's own tripwire log (targets run at once).
func (g *gate) fuzzOne(t fuzzTarget, par int, trip string) bool {
	if t.mk {
		return g.fuzzOneMk(t)
	}
	name := "fuzz " + t.pkg + " " + t.name
	lock := fuzzUnderLock[t.pkg+" "+t.name]
	if lock && (runtime.GOOS != "windows" || g.pwsh == "" || g.skip["linux"] || !g.wslAnswers()) {
		g.skipStep(name, "starts wsl.exe, and the Linux pass cannot run here")
		return false
	}
	s := g.newStep(name)
	r := &FuzzResult{Package: t.pkg, Target: t.name, FuzzTime: g.fuzzTime.String(), Parallel: par}
	s.Fuzz = r
	set := []string{"MK_GATE=1"}
	if lock {
		set = append(set, "MK_LINUX=1")
	}
	argv := g.fuzzArgv(t, par)
	prefix := "   "
	if par > 0 {
		prefix = "   [" + t.name + "] "
	}
	var all []string
	progress := 0
	code := g.exec(s, spec{argv: argv, set: set, lock: lock, tripwire: !lock, tripLog: trip}, func(l string) {
		if m := fuzzExecs.FindStringSubmatch(l); m != nil {
			r.Execs, _ = strconv.ParseInt(m[1], 10, 64)
			progress++
			if progress%20 != 1 {
				return // one progress line a minute is enough on the console
			}
		}
		if m := fuzzCrasher.FindStringSubmatch(l); m != nil {
			r.Crasher = filepath.Join(g.root, filepath.FromSlash(strings.TrimPrefix(t.pkg, "./")), m[1])
		}
		if f := strings.TrimSpace(l); strings.HasPrefix(f, "--- FAIL: ") {
			s.Failed = append(s.Failed, t.pkg+" "+strings.Fields(f)[2])
		}
		fmt.Printf("%s%s\n", prefix, l)
		all = append(all, l)
		if len(all) > 400 {
			all = all[len(all)-200:]
		}
	})
	s.Output = tail(all, 60)
	switch {
	case r.Crasher != "":
		fail(s, "crasher written to "+r.Crasher)
	case code != 0:
		fail(s, fmt.Sprintf("go test -fuzz exit status %d", code))
	}
	return s.Status == "pass"
}
