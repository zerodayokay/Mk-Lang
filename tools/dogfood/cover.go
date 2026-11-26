// Why: the dogfoods are how the library surface is proven in use, so some
// tool must show that every library line of doc/reference.md is referenced
// by a dogfood and, when it declares a function, executed by a case on both
// hosted targets, or else listed in EXEMPT.md with a reason.
//
// What: the cover command (harness.cover): readExempt, collect, tableRuns,
// staticRefs and parseUses gather the evidence, judgeLines gives each
// LineCov its verdict, and writeCoverage writes Dogfoods/COVERAGE.md.
//
// How: execution comes from the cases of --release --pgo-gen builds (a
// record whose Counts[0] is above zero), per target; references from mk
// query uses --json, which also covers what is compiled inline. Only a
// site in the dogfood's own source counts (not gen/ output, not std
// reaching a line on its own). A referenced function no binary instruments
// has no body and counts as covered by reference, when the reference is in
// linked code and every bin of that dogfood wrote its profile table (a bin
// no case runs writes it from one `<bin> --help` run, never as execution).

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Exemption is one EXEMPT.md entry.
type Exemption struct {
	ID     string // Item.ID
	Kind   string // execution, reference or both
	Reason string
	Line   int
}

var exemptEntry = regexp.MustCompile("^- `(.+)` \\((execution|reference|both)\\): (.+)$")

// readExempt parses EXEMPT.md: "## <section>" headings (prelude or
// std.x, as the library surface's comments name them) and under each
// entries "- `<library line as written>` (execution|reference|both):
// <reason>. Covered instead: <where>." An entry that names no library line, has
// no "Covered instead:", or repeats a line is an error.
func readExempt(text string, items []Item) ([]Exemption, []string) {
	byID := map[string]bool{}
	for k := range items {
		byID[items[k].ID()] = true
	}
	var out []Exemption
	var bad []string
	seen := map[string]bool{}
	sect := ""
	inFence := false
	for i, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(l, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if s, ok := strings.CutPrefix(l, "## "); ok {
			sect = strings.TrimSpace(s)
			continue
		}
		if !strings.HasPrefix(l, "- `") {
			continue
		}
		m := exemptEntry.FindStringSubmatch(l)
		if m == nil {
			bad = append(bad, fmt.Sprintf("line %d: want - `<section 18 line>` (execution|reference|both): <reason>. Covered instead: <where>.", i+1))
			continue
		}
		id := sect + " | " + m[1]
		switch {
		case !byID[id]:
			bad = append(bad, fmt.Sprintf("line %d: %s is not a section 18 line (under ## %s)", i+1, m[1], sect))
		case !strings.Contains(m[3], "Covered instead:"):
			bad = append(bad, fmt.Sprintf("line %d: %s: the reason does not say where it is covered instead (\"Covered instead:\")", i+1, m[1]))
		case seen[id]:
			bad = append(bad, fmt.Sprintf("line %d: %s is exempt twice", i+1, m[1]))
		default:
			seen[id] = true
			out = append(out, Exemption{ID: id, Kind: m[2], Reason: m[3], Line: i + 1})
		}
	}
	return out, bad
}

// LineCov is one library line's coverage.
type LineCov struct {
	Item    *Item
	Owners  []string // dogfoods whose README lists it
	Refs    []string // dogfoods whose own source references it (mk query uses)
	GenRefs []string // dogfoods whose generated code (gen/) alone references it
	// CodeRefs are the dogfoods that reference it from code a binary links:
	// a site, own or generated, outside test blocks and tests_ files.
	CodeRefs       []string
	Exec           []string // "dogfood/case (target)" with a non-zero entry count
	ExecOn         []string // the targets some case executed it on
	ExecRestricted bool     // some executing case has a .targets file
	Present        bool     // some binary instrumented its function
	Exempt         *Exemption
	Verdict        string
	Covered        bool
}

// CoverSummary is the cover report's totals.
type CoverSummary struct {
	Lines        int      `json:"lines"`
	Covered      int      `json:"covered"`
	Exempt       int      `json:"exempt"`
	Gaps         int      `json:"gaps"`
	StaticRefs   string   `json:"static_refs"` // "mk query uses" or why there are none
	ExemptErrors []string `json:"exempt_errors,omitempty"`
	// RefErrors are the dogfoods mk query uses failed for; any fails cover,
	// since its references then fall back to execution.
	RefErrors []string `json:"ref_errors,omitempty"`
	// SkippedProfiles are the profiles cover could not read and went on
	// without: the dogfood, the case, the file and why.
	SkippedProfiles []string `json:"skipped_profiles,omitempty"`
	File            string   `json:"file"`
}

// coverage is the state of one cover run.
type coverage struct {
	skipped []string // CoverSummary.SkippedProfiles
	lines   []*LineCov
	byFunc  map[FuncID][]*LineCov
	byRecv  map[string][]*LineCov // "Recv:Name" of methods, for core aliases
	// byDecl indexes the lines that declare no function (struct, enum,
	// interface, error, type, const) by "module.Name", as mk query uses
	// names them.
	byDecl map[string][]*LineCov
	refsOK bool
	// hosted are the hosted targets cover executed on; a function line
	// must be executed on each.
	hosted []string
	// profiled are the dogfoods every bin of which wrote its profile table
	// under --pgo-gen, so that a function absent from their profiles has no
	// instrumented body.
	profiled map[string]bool
}

func newCoverage(items []Item) *coverage {
	c := &coverage{byFunc: map[FuncID][]*LineCov{}, byRecv: map[string][]*LineCov{}, byDecl: map[string][]*LineCov{}, profiled: map[string]bool{}}
	for k := range items {
		it := &items[k]
		lc := &LineCov{Item: it}
		c.lines = append(c.lines, lc)
		if !it.Executable() {
			name := blockName(it.Text)
			if !it.Block {
				_, name = itemHead(it.Text)
			}
			c.byDecl[it.Mod+"."+name] = append(c.byDecl[it.Mod+"."+name], lc)
			continue
		}
		if f, ok := funcOfItem(it); ok {
			c.byFunc[f] = append(c.byFunc[f], lc)
			if f.Recv != "" && f.Mod != "std.core" {
				c.byRecv[f.Recv+":"+f.Name] = append(c.byRecv[f.Recv+":"+f.Name], lc)
			}
		}
	}
	return c
}

// linesOf are the lines a symbol's function implements: the exact
// function, and for a std.core method the lines of a module that declares
// the type as an alias of the core type (time.Duration's methods are
// std.core's).
func (c *coverage) linesOf(key string) []*LineCov {
	var out []*LineCov
	for _, f := range funcsOfKey(key) {
		out = append(out, c.byFunc[f]...)
		if f.Mod == "std.core" && f.Recv != "" {
			out = append(out, c.byRecv[f.Recv+":"+f.Name]...)
		}
	}
	return out
}

func addOnce(l []string, s string) []string {
	if contains(l, s) {
		return l
	}
	return append(l, s)
}

// cover is the cover command.
func (h *harness) cover(dogs []*Dogfood, covPath, exemptPath string) int {
	h.rep = h.newReport("cover")
	c := newCoverage(h.items)
	for _, t := range h.targets {
		if t != targetFree {
			c.hosted = append(c.hosted, t)
		}
	}
	byID := map[string]*LineCov{}
	for _, lc := range c.lines {
		byID[lc.Item.ID()] = lc
	}
	sum := &CoverSummary{File: covPath}
	if b, err := os.ReadFile(exemptPath); err == nil {
		ex, bad := readExempt(string(b), h.items)
		sum.ExemptErrors = bad
		for k := range ex {
			byID[ex[k].ID].Exempt = &ex[k]
		}
	} else if !os.IsNotExist(err) {
		h.say("dogfood cover: %v", err)
		return 2
	}
	for _, d := range dogs {
		for _, id := range d.Owned {
			if lc := byID[id]; lc != nil {
				lc.Owners = addOnce(lc.Owners, d.Name)
			}
		}
	}
	c.refsOK = true // until mk query uses fails for some dogfood; with none, nothing references a line
	sum.StaticRefs = "mk query uses --json"
	if len(dogs) == 0 {
		sum.StaticRefs = "none: the dogfood directory holds no dogfood yet"
	}
	for _, d := range dogs {
		refs, err := h.staticRefs(d)
		if err == errUsesMissing {
			c.refsOK = false
			sum.StaticRefs = "not available: this mk has no `query uses`; a line counts as referenced when a case executes it, and cover fails"
			sum.RefErrors = append(sum.RefErrors, "mk has no query uses")
			break
		}
		if err != nil {
			h.rep.Notes = append(h.rep.Notes, d.Name+": mk query uses: "+err.Error())
			c.refsOK = false
			sum.StaticRefs = "incomplete: mk query uses failed (see the errors below); a line counts as referenced when a case executes it, and cover fails"
			sum.RefErrors = append(sum.RefErrors, d.Name+": "+err.Error())
			continue
		}
		c.addRefs(d.Name, refs, testSite(d.Dir))
	}
	// Execution: the hosted dogfoods' cases, built --release --pgo-gen.
	dr := map[string]*DogReport{}
	for _, d := range dogs {
		if d.Free {
			h.rep.Notes = append(h.rep.Notes, d.Name+": freestanding, so no profile is written; its lines count by reference, and EXEMPT.md names its execution")
			continue
		}
		cases, err := loadCases(filepath.Join(d.Dir, "cases"))
		rep := &DogReport{Name: d.Name, Lines: d.Lines}
		dr[d.Name] = rep
		h.rep.Dogfoods = append(h.rep.Dogfoods, rep)
		if err != nil {
			rep.Runs = append(rep.Runs, &RunReport{Status: "fail", Reason: err.Error()})
			continue
		}
		var linux []*RunReport
		for _, t := range c.hosted {
			if !contains(d.Targets(), t) {
				continue
			}
			rr := &RunReport{Target: t, Profile: "release"}
			rep.Runs = append(rep.Runs, rr)
			if t == targetWindows && runtime.GOOS != "windows" {
				rr.Status, rr.Reason = "skip", "not a Windows host"
				continue
			}
			h.say("%s %s release --pgo-gen", d.Name, t)
			if !h.buildRun(d, rr, true) {
				continue
			}
			rr.profiles = filepath.Join(h.scratch, "prof", d.Name, t)
			if err := os.MkdirAll(rr.profiles, 0o755); err != nil {
				rr.Status, rr.Reason = "fail", err.Error()
				continue
			}
			if t == targetLinux && runtime.GOOS == "windows" {
				linux = append(linux, rr)
				continue
			}
			h.runCases(d, cases, rr)
		}
		if len(linux) > 0 {
			h.runLinux(d, cases, linux)
		}
		tabled := map[string]bool{} // bins whose profile table cover read
		for _, rr := range rep.Runs {
			h.finishRun(d, rr)
			if rr.profiles == "" {
				continue
			}
			bins, err := h.collect(c, d, rr, cases)
			if err != nil {
				rr.Status, rr.Reason = "fail", "profiles: "+err.Error()
				continue
			}
			for _, b := range bins {
				tabled[b] = true
			}
		}
		h.tableRuns(c, d, rep.Runs, tabled)
		var missing []string
		for _, b := range d.Bins {
			if !tabled[b] {
				missing = append(missing, b)
			}
		}
		if len(missing) == 0 {
			c.profiled[d.Name] = true
		} else {
			h.rep.Notes = append(h.rep.Notes, fmt.Sprintf("%s: bin %s wrote no profile table (no case runs it to its end, and `--help` wrote none), so no line it references counts as compiled inline", d.Name, strings.Join(missing, ", ")))
		}
	}
	sum.SkippedProfiles = c.skipped
	h.judgeLines(c, sum)
	if err := writeCoverage(covPath, c, sum, h); err != nil {
		h.say("dogfood cover: %v", err)
		return 2
	}
	h.rep.Coverage = sum
	h.say("coverage: %d lines, %d covered, %d exempt, %d gaps; %s", sum.Lines, sum.Covered, sum.Exempt, sum.Gaps, covPath)
	for _, e := range sum.ExemptErrors {
		h.say("EXEMPT.md: %s", e)
	}
	for _, e := range sum.RefErrors {
		h.say("mk query uses: %s", e)
	}
	return h.finish()
}

// profileBin is the bin whose process wrote a profile file: std/rt
// names it "<executable without .exe>-<pid>.mkprof".
func profileBin(path string) string {
	n := strings.TrimSuffix(filepath.Base(path), ".mkprof")
	if i := strings.LastIndexByte(n, '-'); i > 0 {
		return n[:i]
	}
	return n
}

// collect merges each case's profiles with mk profile merge (all the
// processes and seeds of the case), merges the cases into the dogfood's
// profile, and marks the lines each case executed. It returns the bins
// that wrote a readable profile, whose tables are therefore known.
//
// A process killed while it wrote its profile leaves it empty or cut:
// such a file is skipped with a note naming the case,
// the file and why, and so is a case whose profiles mk profile merge
// still refuses; every other case is collected.
func (h *harness) collect(c *coverage, d *Dogfood, rr *RunReport, cases []*Case) ([]string, error) {
	restricted := map[string]bool{}
	for _, cs := range cases {
		if cs.Has("targets") {
			restricted[cs.Name] = true
		}
	}
	files, err := filepath.Glob(filepath.Join(rr.profiles, "*", "*.mkprof"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	where := d.Name + " " + rr.Target
	// Each case wrote into its own directory: <profiles>/<case>/.
	byCase := map[string][]string{}
	var names, bins []string
	for _, f := range files {
		cs := filepath.Base(filepath.Dir(f))
		if why := unreadableProfile(f); why != "" {
			c.skip(h, fmt.Sprintf("%s: case %s: profile %s skipped: %s", where, cs, f, why))
			continue
		}
		if byCase[cs] == nil {
			names = append(names, cs)
		}
		byCase[cs] = append(byCase[cs], f)
	}
	sort.Strings(names)
	var merged []string
	for _, cs := range names {
		out := filepath.Join(rr.profiles, cs+".mkprof")
		if err := h.merge(byCase[cs], out); err != nil {
			c.skip(h, fmt.Sprintf("%s: case %s: its profiles skipped: %v", where, cs, err))
			continue
		}
		b, err := os.ReadFile(out)
		if err != nil {
			return nil, err
		}
		recs, err := readProfile(b)
		if err != nil {
			c.skip(h, fmt.Sprintf("%s: case %s: merged profile %s skipped: %v", where, cs, out, err))
			continue
		}
		merged = append(merged, out)
		for _, f := range byCase[cs] { // a table cover read
			bins = addOnce(bins, profileBin(f))
		}
		tag := d.Name + "/" + cs + " (" + strings.TrimPrefix(rr.Target, "x86_64-") + ")"
		for _, r := range recs {
			for _, lc := range c.linesOf(r.Key) {
				lc.Present = true
				if r.Entry > 0 {
					lc.Exec = addOnce(lc.Exec, tag)
					lc.ExecOn = addOnce(lc.ExecOn, rr.Target)
					if restricted[cs] {
						lc.ExecRestricted = true
					}
				}
			}
		}
	}
	if len(merged) == 0 {
		return bins, nil
	}
	// The dogfood's profile is merged in the scratch directory and renamed
	// into <work>/dogfood/prof, so a harness running at once never reads a
	// torn one.
	tmp := filepath.Join(rr.profiles, "dogfood.merged.mkprof") // a case name holds no dot
	if err := h.merge(merged, tmp); err != nil {
		return nil, err
	}
	dst := filepath.Join(h.work, "dogfood", "prof", d.Name+"-"+rr.Target+".mkprof")
	b, err := os.ReadFile(tmp)
	if err == nil {
		err = writeAtomic(dst, b)
	}
	return bins, err
}

// skip records a profile cover went on without, in the report's notes
// and in COVERAGE.md.
func (c *coverage) skip(h *harness, note string) {
	c.skipped = append(c.skipped, note)
	h.rep.Notes = append(h.rep.Notes, note)
}

// unreadableProfile says why the profile at path cannot be merged ("" when
// it can): it cannot be read, it is empty, or it is not a whole
// version-1 profile.
func unreadableProfile(path string) string {
	b, err := os.ReadFile(path)
	switch {
	case err != nil:
		return err.Error()
	case len(b) == 0:
		return "empty (0 bytes): its process was stopped before it wrote the profile"
	}
	if _, err := readProfile(b); err != nil {
		return fmt.Sprintf("%v (%d bytes): its process was stopped while it wrote the profile, or the file is not one", err, len(b))
	}
	return ""
}

// tableRuns reads the profile table of each host-built bin of d that no
// case's process wrote a profile for (a client no case runs, or one whose
// every case panics): the bin runs once with --help, which every dogfood
// answers, under its own MK_PROFILE_DIR. Only the table
// is read (Present): nothing it runs counts as executed by a case.
func (h *harness) tableRuns(c *coverage, d *Dogfood, runs []*RunReport, tabled map[string]bool) {
	for _, rr := range runs {
		if rr.profiles == "" || (rr.Target == targetLinux && runtime.GOOS == "windows") {
			continue
		}
		for _, bin := range d.Bins {
			exe := rr.exes[bin]
			if tabled[bin] || exe == "" {
				continue
			}
			dir := filepath.Join(rr.profiles+"-table", bin)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				continue
			}
			env := append(h.childEnv(), "MK_PROFILE_DIR="+dir)
			runProgram([]string{exe, "--help"}, dir, env, "", h.caseLimit)
			files, _ := filepath.Glob(filepath.Join(dir, "*.mkprof"))
			for _, f := range files {
				b, err := os.ReadFile(f)
				if err != nil {
					continue
				}
				recs, err := readProfile(b)
				if err != nil {
					continue
				}
				tabled[bin] = true
				for _, r := range recs {
					for _, lc := range c.linesOf(r.Key) {
						lc.Present = true
					}
				}
			}
		}
	}
}

// merge runs mk profile merge over files into out, 64 inputs at a time so
// a thousand seeds stay under the command-line limit.
func (h *harness) merge(files []string, out string) error {
	for len(files) > 64 {
		var next []string
		for i := 0; i < len(files); i += 64 {
			part := files[i:min(i+64, len(files))]
			o := fmt.Sprintf("%s.part%d", out, i/64)
			if err := h.merge(part, o); err != nil {
				return err
			}
			next = append(next, o)
		}
		files = next
	}
	args := append([]string{"profile", "merge", "-o", out}, files...)
	cmd := exec.Command(h.mk, args...)
	cmd.Env = h.childEnv()
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mk profile merge: %v: %s", err, bytes.TrimSpace(b))
	}
	return nil
}

// Use is one std item of mk query uses --json and its sites.
type Use struct {
	Item  string   `json:"item"`
	Sites []string `json:"sites"`
}

// staticRefs runs mk query uses --json on d and returns the std items it
// references, in the profile's symbol form without "mk." (std.fs.File:Close).
// mk query uses lists the root package's modules, all its bins, and not
// its path dependencies, so a package nested in the dogfood's directory (a
// library its pkg.mk names as a path dep) is queried too, its sites
// prefixed with its directory.
func (h *harness) staticRefs(d *Dogfood) ([]Use, error) {
	var out []Use
	for _, rel := range packageDirs(d.Dir) {
		uses, err := h.queryUses(d, filepath.Join(d.Dir, rel))
		if err != nil {
			return nil, err
		}
		for _, u := range uses {
			if rel != "." {
				for k, s := range u.Sites {
					u.Sites[k] = filepath.ToSlash(rel) + "/" + s
				}
			}
			out = append(out, u)
		}
	}
	return out, nil
}

// packageDirs are the directories under dir, relative to it, that hold a
// pkg.mk: "." first, then nested packages, outside gen/, cases/ and repro/.
func packageDirs(dir string) []string {
	out := []string{"."}
	filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || !e.IsDir() || p == dir {
			return nil
		}
		switch e.Name() {
		case "gen", "cases", "repro":
			return filepath.SkipDir
		}
		if _, err := os.Stat(filepath.Join(p, "pkg.mk")); err == nil {
			if rel, err := filepath.Rel(dir, p); err == nil {
				out = append(out, rel)
			}
		}
		return nil
	})
	return out
}

// queryUses runs mk query uses --json on the package at root.
func (h *harness) queryUses(d *Dogfood, root string) ([]Use, error) {
	args := []string{"query", "uses", "--json", "--root", root}
	if d.Free {
		args = append(args, "--target", targetFree)
	}
	cmd := exec.Command(h.mk, args...)
	cmd.Dir = root
	cmd.Env = append(h.childEnv(), "MK_CACHE="+filepath.Join(h.work, "dogfood", "cache", d.Name))
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	if bytes.Contains(e.Bytes(), []byte(`unknown query "uses"`)) {
		return nil, errUsesMissing
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %v: %s", root, err, bytes.TrimSpace(e.Bytes()))
	}
	return parseUses(o.Bytes())
}

// parseUses reads mk query uses --json (internal/driver
// query_uses.go): {"query": "uses", ..., "uses": [{"item":
// "std.fs.File:Close", "sites": ["src/main.mk:12:5", ...]}, ...]}, items
// named as the profile's symbols without "mk." and without a generic
// instance's hash, "uses" present even when empty.
func parseUses(b []byte) ([]Use, error) {
	var doc struct {
		Uses *[]Use `json:"uses"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("mk query uses --json: %v", err)
	}
	if doc.Uses == nil {
		return nil, fmt.Errorf("mk query uses --json has no \"uses\" array: tools/dogfood and diag7's T13 disagree on the format")
	}
	var out []Use
	for _, u := range *doc.Uses {
		if u.Item != "" {
			out = append(out, u)
		}
	}
	return out, nil
}

// generatedSite reports whether a use site (path:line:col, the path
// relative to the package) is in a generator's output: a file under a gen/
// directory, where mk writes what a gen line emits (src/gen/<target>/gen.mk).
func generatedSite(site string) bool {
	parts := strings.FieldsFunc(site, func(r rune) bool { return r == '/' || r == '\\' })
	for _, p := range parts[:max(len(parts)-1, 0)] {
		if p == "gen" {
			return true
		}
	}
	return false
}

// testSite reports whether a use site of the dogfood at dir (path:line:col,
// the path relative to dir) is test code: a tests_ file, or a line of a
// test block (countLines's rule). A binary links none of it.
func testSite(dir string) func(string) bool {
	masks := map[string][]bool{}
	return func(site string) bool {
		rest, _, ok1 := cutLast(site, ':')
		path, ln, ok2 := cutLast(rest, ':')
		n, err := strconv.Atoi(ln)
		if !ok1 || !ok2 || err != nil {
			return false
		}
		if strings.HasPrefix(filepath.Base(filepath.FromSlash(path)), "tests_") {
			return true
		}
		m, ok := masks[path]
		if !ok {
			b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
			if err == nil {
				m = testLines(b)
			}
			masks[path] = m
		}
		return n >= 1 && n <= len(m) && m[n-1]
	}
}

func cutLast(s string, sep byte) (before, after string, ok bool) {
	i := strings.LastIndexByte(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}

// addRefs records that dogfood references the std items of uses (mk query
// uses's names: std.fs.File:Close, std.hash.Crc32, std.fs.File, std.fs.Os):
// as its own reference when some site is outside gen/, else as a reference
// of its generated code only; and as a reference from linked code when some
// site is not test code (isTest; nil: none is).
func (c *coverage) addRefs(dogfood string, uses []Use, isTest func(string) bool) {
	for _, u := range uses {
		key := u.Item
		lines := c.linesOf("mk." + key)
		if !strings.ContainsAny(key, ":#") {
			lines = append(lines, c.byDecl[stripInstance(key)]...)
		}
		// An interface's methods are lines of its block, so a call of one
		// (std.core.Slice:Sort, or an impl's method) references the block.
		for _, f := range funcsOfKey("mk." + key) {
			for _, lc := range c.byDecl[f.Mod+"."+f.Recv] {
				if f.Recv != "" && strings.HasPrefix(lc.Item.Text, "interface ") {
					lines = append(lines, lc)
				}
			}
		}
		own := len(u.Sites) == 0
		linked := len(u.Sites) == 0
		for _, s := range u.Sites {
			if !generatedSite(s) {
				own = true
			}
			if isTest == nil || !isTest(s) {
				linked = true
			}
		}
		for _, lc := range lines {
			if own {
				lc.Refs = addOnce(lc.Refs, dogfood)
			} else {
				lc.GenRefs = addOnce(lc.GenRefs, dogfood)
			}
			if linked {
				lc.CodeRefs = addOnce(lc.CodeRefs, dogfood)
			}
		}
	}
}

// refProfiled reports whether a dogfood that references lc from code its
// binaries link had every bin write its profile table under --pgo-gen, so
// that lc's function missing from those tables means it has no body.
func (c *coverage) refProfiled(lc *LineCov) bool {
	for _, d := range lc.CodeRefs {
		if c.profiled[d] {
			return true
		}
	}
	return false
}

// isHardwareBound reports whether mod is a hardware-bound std module whose
// functions may execute only on targets selected by a case's .targets file.
func isHardwareBound(mod string) bool {
	return mod == "std.gpu" || strings.HasPrefix(mod, "std.gpu.") ||
		mod == "std.window" || strings.HasPrefix(mod, "std.window.")
}

// missingTargets are the hosted targets cover ran on where no case
// executed lc. For a hardware-bound module executed by a .targets-restricted
// case, execution on its allowed target(s) satisfies every hosted target.
func (c *coverage) missingTargets(lc *LineCov) []string {
	if isHardwareBound(lc.Item.Mod) && lc.ExecRestricted && len(lc.ExecOn) > 0 {
		return nil
	}
	var out []string
	for _, t := range c.hosted {
		if !contains(lc.ExecOn, t) {
			out = append(out, t)
		}
	}
	return out
}

// judgeLines gives each line its verdict.
func (h *harness) judgeLines(c *coverage, sum *CoverSummary) {
	for _, lc := range c.lines {
		sum.Lines++
		exec := lc.Item.Executable()
		ex := ""
		if lc.Exempt != nil {
			ex = lc.Exempt.Kind
		}
		executed := len(lc.Exec) > 0
		missing := c.missingTargets(lc)
		// Without mk query uses (absent, or failed for some dogfood) an
		// executed line counts as referenced; with it, only a dogfood's
		// own source references a line.
		referenced := len(lc.Refs) > 0 || ex == "reference" || ex == "both" || (executed && !c.refsOK)
		switch {
		case ex == "both":
			lc.Verdict, lc.Covered = "exempt", true
		case !referenced && !c.refsOK:
			lc.Verdict = "gap: not executed, and references are unknown (mk query uses: see Static references above)"
		case !referenced && len(lc.GenRefs) > 0:
			lc.Verdict = "gap: referenced only by generated code (gen/) of " + strings.Join(lc.GenRefs, ", ")
		case !referenced && executed:
			lc.Verdict = "gap: executed, but no dogfood's own source references it"
		case !referenced:
			lc.Verdict = "gap: no dogfood references it"
		case !exec && ex == "reference" && len(lc.Refs) == 0:
			// Referenced only through its reference exemption.
			lc.Verdict, lc.Covered = "exempt (reference)", true
		case !exec:
			lc.Verdict, lc.Covered = "covered (referenced)", true
		case executed && len(missing) == 0 && ex == "reference" && len(lc.Refs) == 0:
			lc.Verdict, lc.Covered = "exempt (reference; executed)", true
		case executed && len(missing) == 0:
			lc.Verdict, lc.Covered = "covered (executed)", true
		case ex == "execution":
			lc.Verdict, lc.Covered = "exempt (execution)", true
		case !lc.Present && c.refProfiled(lc):
			lc.Verdict, lc.Covered = "covered (referenced; no instrumented body: compiled inline)", true
		case executed:
			lc.Verdict = "gap: not executed on " + strings.Join(missing, ", ")
		case len(lc.Refs) == 0:
			lc.Verdict = "gap: exempt from reference only, and not executed by any case"
		default:
			lc.Verdict = "gap: referenced, not executed by any case"
		}
		switch {
		case lc.Covered && strings.HasPrefix(lc.Verdict, "exempt"):
			sum.Exempt++
		case lc.Covered:
			sum.Covered++
		default:
			sum.Gaps++
		}
	}
}

// writeCoverage writes COVERAGE.md.
func writeCoverage(path string, c *coverage, sum *CoverSummary, h *harness) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Coverage of reference section 18\n\n")
	fmt.Fprintf(&b, "Written by `go run ./tools/dogfood cover` on %s, with %s, executing on %s; do not edit by hand. ",
		time.Now().Format("2006-01-02"), orDash(h.rep.MkVersion), strings.Join(h.targets, " and "))
	fmt.Fprintf(&b, "A line is covered when some dogfood's own source references it (`mk query uses`; a site in generated code under `gen/` is shown as `gen:` and does not count) and, if it declares a function, some case executes it on every hosted target named above (an entry count above zero in a `--release --pgo-gen` build), or it is in `EXEMPT.md`.\n\n")
	fmt.Fprintf(&b, "- Lines: %d. Covered: %d. Exempt: %d. Gaps: %d.\n", sum.Lines, sum.Covered, sum.Exempt, sum.Gaps)
	fmt.Fprintf(&b, "- Static references: %s.\n", sum.StaticRefs)
	for _, e := range sum.ExemptErrors {
		fmt.Fprintf(&b, "- EXEMPT.md error: %s\n", e)
	}
	for _, e := range sum.RefErrors {
		fmt.Fprintf(&b, "- mk query uses error: %s\n", strings.Join(strings.Fields(e), " "))
	}
	for _, e := range sum.SkippedProfiles {
		fmt.Fprintf(&b, "- Profile skipped (the case's other profiles and every other case still count): %s\n", e)
	}
	sect := "-"
	for _, lc := range c.lines {
		if s := sectName(lc.Item.InSect); s != sect {
			sect = s
			fmt.Fprintf(&b, "\n## %s\n\n| Line | Owned by | Referenced by | Executed by | Verdict |\n|---|---|---|---|---|\n", s)
		}
		verdict := lc.Verdict
		if lc.Exempt != nil {
			verdict += " (EXEMPT.md line " + fmt.Sprint(lc.Exempt.Line) + ")"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", cell(lc.Item.Text), list3(lc.Owners), list3(refCells(lc)), list3(lc.Exec), verdict)
	}
	return writeAtomic(path, []byte(b.String()))
}

// refCells are the Referenced by cell's entries: the dogfoods whose own
// source references the line, then "gen: d" for those whose generated code
// alone does.
func refCells(lc *LineCov) []string {
	out := append([]string{}, lc.Refs...)
	for _, d := range lc.GenRefs {
		out = append(out, "gen: "+d)
	}
	return out
}

// cell escapes a table cell's pipes.
func cell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

func list3(l []string) string {
	switch {
	case len(l) == 0:
		return "-"
	case len(l) <= 3:
		return cell(strings.Join(l, ", "))
	}
	return cell(strings.Join(l[:3], ", ")) + fmt.Sprintf(" and %d more", len(l)-3)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
