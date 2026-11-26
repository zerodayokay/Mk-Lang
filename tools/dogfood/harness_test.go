// Why: most of the harness is parsing and judging, which must be right
// before any program runs.
// What: tests of case files, the .mkprof reader and symbol
// reading, README ownership, EXEMPT.md, the verdicts and COVERAGE.md.
// How: pure functions on in-memory inputs and doc/reference.md as on disk;
// no toolchain runs.

package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for n, s := range files {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCaseFilesMostSpecificWins: a case's files resolve per target and
// profile, target.profile before target before profile before none.
func TestCaseFilesMostSpecificWins(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"a.args":                     "one\ntwo words\n",
		"a.out":                      "plain\n",
		"a.x86_64-linux.out":         "linux\n",
		"a.release.out":              "release\n",
		"a.x86_64-linux.release.out": "linux release\n",
		"a.code":                     "3\n",
		"a.env":                      "K=v=w\n",
		"data.txt":                   "a fixture\n",
		"b.in":                       "input\n",
		"b.targets":                  "x86_64-windows\n",
		"s.sim":                      "# seeds\nseeds 1..3\nseed 9\n",
	})
	cases, err := loadCases(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cases {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{"a", "b", "s"}) {
		t.Fatalf("cases %q, want a b s (data.txt is a fixture)", names)
	}
	for _, tc := range []struct{ target, profile, out string }{
		{targetWindows, "dev", "plain\n"},
		{targetWindows, "release", "release\n"},
		{targetLinux, "dev", "linux\n"},
		{targetLinux, "release", "linux release\n"},
	} {
		s, err := cases[0].resolve(tc.target, tc.profile)
		if err != nil {
			t.Fatal(err)
		}
		if string(s.Out) != tc.out || s.Code != 3 || !slices.Equal(s.Args, []string{"one", "two words"}) || !slices.Equal(s.Env, []string{"K=v=w"}) {
			t.Errorf("%s %s: %+v", tc.target, tc.profile, s)
		}
	}
	s, _ := cases[1].resolve(targetLinux, "dev")
	if s.Skip == "" {
		t.Error("b.targets names x86_64-windows only, but b runs on Linux")
	}
	s, _ = cases[1].resolve(targetWindows, "dev")
	if s.Skip != "" || !strings.HasSuffix(s.Stdin, "b.in") {
		t.Errorf("b on Windows: %+v", s)
	}
	s, err = cases[2].resolve(targetWindows, "dev")
	if err != nil || !slices.Equal(s.Seeds, []uint64{1, 2, 3, 9}) || s.SimTest {
		t.Errorf("s.sim: %+v %v", s, err)
	}
}

// TestCaseFileErrors: a bad qualifier, a bad case name and a bad
// .sim are errors, never a silently skipped case.
func TestCaseFileErrors(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"qualifier": {"a.x86_64-macos.out": ""},
		"name":      {"a b.out": ""},
	} {
		dir := t.TempDir()
		write(t, dir, files)
		if _, err := loadCases(dir); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	for _, sim := range []string{"", "seeds 3..1\n", "seed x\n", "mode fast\n", "seeds 1..200000\n"} {
		if _, _, err := parseSim([]byte(sim)); err == nil {
			t.Errorf(".sim %q: no error", sim)
		}
	}
	if _, test, err := parseSim([]byte("seed 1\nmode test\n")); err != nil || !test {
		t.Errorf("mode test: %v %v", test, err)
	}
}

// TestNormalizePaths: the run's paths come back as $WORK and $CASES
// in either slash direction, and $SEED is the run's seed in the expected
// output.
func TestNormalizePaths(t *testing.T) {
	x := Subst{Cases: `D:\src\mk\Dogfoods\x\cases`, Work: `V:\w\x\case`, Seed: "7"}
	got := x.normalize([]byte(`open D:\src\mk\Dogfoods\x\cases/in.txt; wrote V:/w/x/case/out` + "\n"))
	if want := "open $CASES/in.txt; wrote $WORK/out\n"; string(got) != want {
		t.Errorf("normalize: %q, want %q", got, want)
	}
	if got := x.want([]byte("seed $SEED at $CASES\n")); string(got) != "seed 7 at $CASES\n" {
		t.Errorf("want: %q", got)
	}
	if got := x.expand("$CASES/a $WORK $SEED"); got != `D:\src\mk\Dogfoods\x\cases/a V:\w\x\case 7` {
		t.Errorf("expand: %q", got)
	}
	if d := diff("stdout", []byte("a\nb\n"), []byte("a\nc\n")); !strings.Contains(d, "line 2") {
		t.Errorf("diff: %q", d)
	}
	if d := diff("stdout", []byte("a"), []byte("a")); d != "" {
		t.Errorf("equal outputs differ: %q", d)
	}
}

func TestLinuxPath(t *testing.T) {
	for in, want := range map[string]string{`D:\src\mk\a b\c`: "/mnt/d/src/mk/a b/c", `c:/x`: "/mnt/c/x", `rel\p`: "rel/p"} {
		if got := linuxPath(in); got != want {
			t.Errorf("linuxPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// mkprof writes a version-1 profile of (key, entry) records.
func mkprof(recs ...Record) []byte {
	var b bytes.Buffer
	b.WriteString(profMagic)
	binary.Write(&b, binary.LittleEndian, uint64(len(recs)))
	for _, r := range recs {
		binary.Write(&b, binary.LittleEndian, uint32(len(r.Key)))
		b.WriteString(r.Key)
		b.Write(make([]byte, 16))
		binary.Write(&b, binary.LittleEndian, uint32(2))
		binary.Write(&b, binary.LittleEndian, r.Entry)
		binary.Write(&b, binary.LittleEndian, uint64(5))
	}
	return b.Bytes()
}

// TestReadProfile: the reader takes the version-1 .mkprof format
// and refuses a bad magic and a truncated file.
func TestReadProfile(t *testing.T) {
	in := []Record{{"mk.std.hash.Crc32", 4}, {"mk.std.core.str:Trim", 0}}
	b := mkprof(in...)
	got, err := readProfile(b)
	if err != nil || !slices.Equal(got, in) {
		t.Fatalf("readProfile: %v %v", got, err)
	}
	if _, err := readProfile(b[:len(b)-3]); err == nil {
		t.Error("a truncated profile reads")
	}
	if _, err := readProfile(append([]byte("MKPROF\x00\x02"), b[8:]...)); err == nil {
		t.Error("a version-2 magic reads")
	}
}

// TestFuncsOfKey: the symbols a --pgo-gen build writes (observed on
// mk build 4b809d32406018a1, 2026-11-11) read as the library's functions.
func TestFuncsOfKey(t *testing.T) {
	for key, want := range map[string]string{
		"mk.std.hash.Crc32":                       "std.hash.Crc32",
		"mk.std.core.str:Trim":                    "std.core.str:Trim",
		"mk.std.math.Max.4e7b4ee469f382fb":        "std.math.Max",
		"mk.std.core.List:Push.2575cf1b0717458f":  "std.core.List:Push",
		"mk.std.core.Slice:Sort.52aef6d3f69407b0": "std.core.Slice:Sort",
		"mk.std.core#impl(std.core.Slice for std.core.List):ItemsMut.2d848cd42d8c6c7e": "std.core.Slice:ItemsMut std.core.List:ItemsMut",
		"mk.std.core#impl(std.core.Format for int):Format":                             "std.core.Format:Format std.core.int:Format",
		"mk.src.Main":              "",
		"mk.fn.891dd82b3193243c":   "",
		"mk.std.core.Sort#fn2":     "",
		"mk.std.db.sqlite.Db:Exec": "std.db.sqlite.Db:Exec",
	} {
		var got []string
		for _, f := range funcsOfKey(key) {
			got = append(got, f.String())
		}
		if strings.Join(got, " ") != want {
			t.Errorf("funcsOfKey(%q) = %q, want %q", key, got, want)
		}
	}
}

// TestOwnedLines: a README's owned block takes section comments,
// lines word for word, a block with its body, and `*`; a line the library
// surface does not have is reported.
func TestOwnedLines(t *testing.T) {
	items := referenceItems(t)
	readme := "# x\n\n## Section 18 lines\n\n```text\nstr.Trim(self): str\nstruct System {\n    export args: []str\n}\n// std.math\n*\n// std.fs\nfs.Nope(): void\n```\n"
	owned, bad := ownedLines(readme, items)
	if !slices.Contains(owned, "prelude | str.Trim(self): str") || !slices.Contains(owned, "prelude | struct System {") ||
		!slices.Contains(owned, "std.math | math.Sqrt(x: f64): f64") {
		t.Errorf("owned %q", owned)
	}
	if !slices.Equal(bad, []string{"std.fs | fs.Nope(): void"}) {
		t.Errorf("bad %q", bad)
	}
}

func TestCountLines(t *testing.T) {
	src := "// a comment\nimport std.fs\n\nMain() {\n}\n\ntest \"x\" {\n    Assert(true)\n}\n"
	if n := countLines([]byte(src)); n != 4 {
		t.Errorf("countLines = %d, want 4 (blank lines and test blocks out)", n)
	}
}

// TestExempt: EXEMPT.md entries name a library line under its
// section's heading, a kind and where the line is covered instead.
func TestExempt(t *testing.T) {
	items := referenceItems(t)
	text := "# Exempt\n\n## std.free, on x86_64-freestanding only\n\n## std.free\n\n" +
		"- `free.Heap(): &Heap` (execution): freestanding only. Covered instead: Dogfoods/kernel.\n" +
		"- `free.Heap(): &Heap` (both): twice. Covered instead: nowhere.\n" +
		"- `free.Nope()` (both): no line. Covered instead: x.\n" +
		"- `str.Trim(self): str` (execution): wrong section. Covered instead: x.\n\n## prelude\n\n" +
		"- `str.Trim(self): str` (reference): no where.\n" +
		"- `str.Len(self): int` (sometimes): bad kind. Covered instead: x.\n" +
		"```\n- `inside a fence` (both): ignored. Covered instead: x.\n```\n"
	ex, bad := readExempt(text, items)
	if len(ex) != 1 || ex[0].ID != "std.free | free.Heap(): &Heap" || ex[0].Kind != "execution" {
		t.Errorf("entries %+v", ex)
	}
	if len(bad) != 5 {
		t.Errorf("errors %d, want 5: %q", len(bad), bad)
	}
}

// TestVerdicts: a line executed on both hosted targets and named by a
// dogfood's source is covered; a referenced function no binary
// instruments is covered by reference; one instrumented and never entered
// is a gap, and so is one entered on one target only; a line std executes
// on its own, or that only generated code names, is a gap while mk query
// uses answers; without it only execution counts; exemptions cover their
// half.
func TestVerdicts(t *testing.T) {
	items := referenceItems(t)
	h := &harness{items: items}
	line := func(c *coverage, text string) *LineCov {
		for _, lc := range c.lines {
			if lc.Item.Text == text {
				return lc
			}
		}
		t.Fatalf("no line %q", text)
		return nil
	}
	exec := func(c *coverage, key, target string, entry uint64) {
		for _, lc := range c.linesOf(key) {
			lc.Present = true
			if entry > 0 {
				lc.Exec = addOnce(lc.Exec, "d/case ("+target+")")
				lc.ExecOn = addOnce(lc.ExecOn, target)
			}
		}
	}
	c := newCoverage(items)
	c.refsOK = true
	c.hosted = []string{targetWindows, targetLinux}
	c.profiled["d"] = true
	for _, tg := range c.hosted {
		exec(c, "mk.std.core.str:Trim", tg, 3)
		exec(c, "mk.std.hash.Crc32", tg, 0)
		exec(c, "mk.std.core.str:Byte", tg, 9) // std's own call, no dogfood names it
	}
	exec(c, "mk.std.core.str:Find", targetWindows, 1)
	for _, text := range []string{"str.Trim(self): str", "hash.Crc32(data: []u8): u32", "str.Len(self): int", "struct System {", "str.Find(self, sub: str): ?int"} {
		line(c, text).Refs = []string{"d"}
		line(c, text).CodeRefs = []string{"d"}
	}
	line(c, "str.Has(self, sub: str): bool").GenRefs = []string{"d"}
	line(c, "free.Heap(): &Heap").Exempt = &Exemption{Kind: "execution"}
	line(c, "free.Heap(): &Heap").Refs = []string{"kernel"}
	line(c, "str.EndsWith(self, suffix: str): bool").Exempt = &Exemption{Kind: "reference"}
	sum := &CoverSummary{}
	h.judgeLines(c, sum)
	for text, want := range map[string]string{
		"str.Trim(self): str":                     "covered (executed)",
		"hash.Crc32(data: []u8): u32":             "gap: referenced, not executed by any case",
		"str.Len(self): int":                      "covered (referenced; no instrumented body: compiled inline)",
		"struct System {":                         "covered (referenced)",
		"free.Heap(): &Heap":                      "exempt (execution)",
		"str.Find(self, sub: str): ?int":          "gap: not executed on x86_64-linux",
		"str.Byte(self, i: int): u8":              "gap: executed, but no dogfood's own source references it",
		"str.Has(self, sub: str): bool":           "gap: referenced only by generated code (gen/) of d",
		"str.StartsWith(self, prefix: str): bool": "gap: no dogfood references it",
		"str.EndsWith(self, suffix: str): bool":   "gap: exempt from reference only, and not executed by any case",
	} {
		if got := line(c, text).Verdict; got != want {
			t.Errorf("%s: %q, want %q", text, got, want)
		}
	}
	if sum.Lines != len(items) || sum.Covered != 3 || sum.Exempt != 1 || sum.Gaps != len(items)-4 {
		t.Errorf("summary %+v", sum)
	}
	// Without mk query uses, execution stands for a reference.
	c2 := newCoverage(items)
	c2.hosted = []string{targetWindows}
	exec(c2, "mk.std.core.str:Byte", targetWindows, 1)
	h.judgeLines(c2, &CoverSummary{})
	if v := line(c2, "str.Len(self): int").Verdict; !strings.Contains(v, "references are unknown") {
		t.Errorf("without references: %q", v)
	}
	if v := line(c2, "str.Byte(self, i: int): u8").Verdict; v != "covered (executed)" {
		t.Errorf("without references, executed: %q", v)
	}
}

// TestParseUses: mk query uses --json as internal/driver writes it
// (TestQueryUses), and a site under gen/ is generated code.
func TestParseUses(t *testing.T) {
	// Taken from mk built at 27f93ba (2026-11-11) on a package with a gen
	// regex.Matcher line: the generator is named at the gen line, its
	// output's items at src/gen/<target>/gen.mk.
	got, err := parseUses([]byte(`{"query":"uses","subject":"","found":true,"answer":["std.fs.File:Close  src/main.mk:31:11"],"truncated":0,"uses":[{"item":"std.fs.File:Close","sites":["src/main.mk:31:11"]},{"item":"std.regex.Matcher","sites":["src/main.mk:8:11"]},{"item":"std.simd.Vec:Splat","sites":["src/gen/x86_64-windows/gen.mk:203:31","src/gen/x86_64-windows/gen.mk:204:32"]}]}`))
	if err != nil || len(got) != 3 || got[0].Item != "std.fs.File:Close" || got[2].Sites[1] != "src/gen/x86_64-windows/gen.mk:204:32" {
		t.Errorf("parseUses: %+v %v", got, err)
	}
	if got, err := parseUses([]byte(`{"query":"uses","uses":[]}`)); err != nil || len(got) != 0 {
		t.Errorf("empty: %+v %v", got, err)
	}
	if _, err := parseUses([]byte(`{"query": "uses", "answer": ["std.fs.File:Close"]}`)); err == nil {
		t.Error("an answer without a uses array parses")
	}
	for site, want := range map[string]bool{
		"src/gen/x86_64-windows/gen.mk:201:21": true,
		"src\\gen\\x86_64-linux\\gen.mk:1:1":   true,
		"gen/a.mk:1:1":                         true,
		"src/main.mk:8:11":                     false,
		"src/gen.mk:3:1":                       false,
		"src/regen/a.mk:3:1":                   false,
	} {
		if generatedSite(site) != want {
			t.Errorf("generatedSite(%q) = %v", site, !want)
		}
	}
	c := newCoverage(referenceItems(t))
	c.addRefs("d", got, nil)
	for _, lc := range c.lines {
		switch lc.Item.Text {
		case "File.Close(self, io: Io): fs.Error!void":
			if len(lc.Refs) != 1 {
				t.Errorf("File.Close: refs %q", lc.Refs)
			}
		case "regex.Matcher(g: mut Gen, t: Type, pattern: str)":
			if len(lc.Refs) != 1 {
				t.Errorf("regex.Matcher: refs %q", lc.Refs)
			}
		}
	}
}

// TestWriteCoverage: COVERAGE.md has a table per section with the
// pipes of error sets escaped.
func TestWriteCoverage(t *testing.T) {
	items := referenceItems(t)
	h := &harness{items: items, targets: []string{targetWindows}, rep: &Report{MkVersion: "mk build x"}}
	c := newCoverage(items)
	sum := &CoverSummary{StaticRefs: "test"}
	h.judgeLines(c, sum)
	p := filepath.Join(t.TempDir(), "COVERAGE.md")
	if err := writeCoverage(p, c, sum, h); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	for _, want := range []string{"# Coverage of reference section 18", "\n## prelude\n", "\n## std.fs\n", "| Line | Owned by |"} {
		if !strings.Contains(s, want) {
			t.Errorf("COVERAGE.md lacks %q", want)
		}
	}
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "| `") && strings.Count(strings.ReplaceAll(l, `\|`, ""), "|") != 6 {
			t.Errorf("row with an unescaped pipe: %s", l)
		}
	}
}

// archID is the ID of the library surface's `enum Arch {` line, whichever
// module the reference puts it under.
func archID(items []Item) string {
	for _, it := range items {
		if it.Text == "enum Arch {" {
			return it.ID()
		}
	}
	return "no enum Arch line in section 18"
}

// fsErrorID is the ID of std.fs's `type Error = ...` line, whose members
// change as the module grows.
func fsErrorID(items []Item) string {
	for _, it := range items {
		if it.Mod == "std.fs" && strings.HasPrefix(it.Text, "type Error = ") {
			return it.ID()
		}
	}
	return "no type Error line in std.fs's section 18 lines"
}

// TestStaticRefsReachEveryKind: mk query uses names reach function
// lines (methods, free functions, generic instances) and the lines that
// declare no function: a struct, an error, a type alias and a const.
func TestStaticRefsReachEveryKind(t *testing.T) {
	items := referenceItems(t)
	refs, err := parseUses([]byte(`{"uses": [
		{"item": "std.core.System", "sites": ["src/main.mk:1:1"]},
		{"item": "std.fs.Os", "sites": ["src/main.mk:2:1"]},
		{"item": "std.fs.Error", "sites": ["src/main.mk:3:1"]},
		{"item": "std.atomic.Relaxed", "sites": ["src/main.mk:4:1"]},
		{"item": "std.core.str:Trim", "sites": ["src/main.mk:5:1"]},
		{"item": "std.fs.Rename", "sites": ["src/main.mk:6:1"]},
		{"item": "std.math.Max", "sites": ["src/main.mk:7:1"]},
		{"item": "std.fs.File:Close", "sites": ["src/main.mk:8:1"]},
		{"item": "std.core.Slice:Sort", "sites": ["src/main.mk:9:1"]},
		{"item": "std.core.Duration:Nanos", "sites": ["src/main.mk:10:1"]},
		{"item": "std.core.Lines:Next", "sites": ["src/main.mk:11:1"]},
		{"item": "std.core.Arch", "sites": ["src/main.mk:12:1"]},
		{"item": "std.regex.Matcher", "sites": ["src/main.mk:13:1"]},
		{"item": "std.mmio.Load", "sites": ["src/paging.mk:66:21"]},
		{"item": "std.free.Heap", "sites": ["src/heap.mk:40:15"]},
		{"item": "std.core#impl(std.core.Slice for std.core.HeapList):Items", "sites": ["src/heap.mk:46:17"]},
		{"item": "std.simd.Vec:Splat", "sites": ["src/gen/x86_64-windows/gen.mk:203:31"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	c := newCoverage(items)
	c.addRefs("d", refs, nil)
	want := []string{
		"prelude | struct System {",
		"std.fs | error Os(code: i64)",
		fsErrorID(items),
		"std.atomic | const Relaxed: Order = Order(v: 0)",
		"prelude | str.Trim(self): str",
		"std.fs | fs.Rename(io: Io, src: str, dst: str): fs.Error!void",
		"std.math | math.Max[T: Ord + Plain](a: T, b: T): T",
		"std.fs | File.Close(self, io: Io): fs.Error!void",
		"prelude | Slice[T].Sort(mut self, const less: fn(&T, &T): bool)",
		"prelude | interface Slice[T] {", // through Slice:Sort and the impl's Items
		"std.time | Duration.Nanos(self): i64",
		"prelude | Lines.Next(mut self): ?str from self.text",
		archID(items), // the prelude's; std.net's while the reference had no comment between std.net's lines and Arch
		"std.regex | regex.Matcher(g: mut Gen, t: Type, pattern: str)",
		"std.mmio | mmio.Load[T: Plain](p: *T): T",
		"std.free | free.Heap(): &Heap",
	}
	var got []string
	for _, lc := range c.lines {
		if len(lc.Refs) > 0 {
			got = append(got, lc.Item.ID())
		}
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("no reference reached %q", w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("references reached %d lines, want %d: %q", len(got), len(want), got)
	}
	// simd.Vec:Splat is named only in generated code.
	for _, lc := range c.lines {
		if len(lc.GenRefs) > 0 && !strings.HasPrefix(lc.Item.Text, "Vec[") {
			t.Errorf("generated-only reference reached %q", lc.Item.ID())
		}
	}
}

// TestTargetsFileAndsKinds: a .targets file naming a target and a
// profile runs the case on that target in that profile only.
func TestTargetsFileAndsKinds(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"c.targets": "x86_64-linux release\n", "c.out": ""})
	cases, err := loadCases(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		target, profile string
		runs            bool
	}{{targetLinux, "release", true}, {targetLinux, "dev", false}, {targetWindows, "release", false}} {
		s, err := cases[0].resolve(tc.target, tc.profile)
		if err != nil || (s.Skip == "") != tc.runs {
			t.Errorf("%s %s: skip %q, want runs %v (%v)", tc.target, tc.profile, s.Skip, tc.runs, err)
		}
	}
}

// TestFreestandingDetection: a link_script or the target's name in
// pkg.mk makes a dogfood freestanding; a hosted pkg.mk does not.
func TestFreestandingDetection(t *testing.T) {
	for pkg, free := range map[string]bool{
		`bins: [pkg.Bin(name: "k", root: "src", link_script: "link.ld", cpu: "", subsystem: "console")]`:                 true,
		`bins: [pkg.Bin(name: "k", root: "src", link_script: "", cpu: "", subsystem: "console")] // x86_64-freestanding`: true,
		`bins: [pkg.Bin(name: "k", root: "src", link_script: "", cpu: "", subsystem: "console")]`:                        false,
	} {
		dir := t.TempDir()
		write(t, dir, map[string]string{"pkg.mk": "const Package = pkg.Package(\n    name: \"k\",\n    " + pkg + ",\n)\n"})
		d, err := loadDogfood(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Free != free || len(d.Bins) != 1 || d.Bins[0] != "k" {
			t.Errorf("%s: free %v bins %q, want free %v", pkg, d.Free, d.Bins, free)
		}
	}
}

// TestPartialName: a cover without its default target, or any -only
// run, writes beside its report and never replaces Dogfoods/COVERAGE.md.
func TestPartialName(t *testing.T) {
	def := strings.Join(coverTargets(), ",")
	other := targetLinux
	if def == targetLinux {
		other = targetWindows
	}
	for _, tc := range []struct {
		cmd     string
		only    []string
		targets []string
		want    string
	}{
		{"cover", nil, coverTargets(), ""},
		{"cover", nil, []string{targetWindows, targetLinux}, ""},
		{"cover", nil, []string{other}, other},
		{"cover", []string{"a", "b"}, []string{other}, "a,b-" + other},
		{"cover", []string{"a"}, coverTargets(), "a"},
		{"run", nil, []string{other}, ""},
		{"run", []string{"a"}, allTargets, "a"},
	} {
		if got := partialName(tc.cmd, tc.only, tc.targets); got != tc.want {
			t.Errorf("%s %v %v: %q, want %q", tc.cmd, tc.only, tc.targets, got, tc.want)
		}
	}
}

// TestBuildID: mk version's build id, with or without the release
// line mk 1.1 prints first.
func TestBuildID(t *testing.T) {
	for in, want := range map[string]string{
		"mk 1.1.0 commit 84316e2949c4+dirty\nmk build c74a6b270757db62 (go1.27.3 windows/amd64)\nLLVM 21.1.8\n": "c74a6b270757db62",
		"mk build 4b809d32406018a1 (go1.27.3 windows/amd64)\n":                                                  "4b809d32406018a1",
		"mk 1.1.0\n": "",
		"":           "",
	} {
		if got := buildID(in); got != want {
			t.Errorf("buildID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRefErrorsFailCover: a cover whose mk query uses failed for some
// dogfood fails, even with no gap, since its references fell back to
// execution.
func TestRefErrorsFailCover(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		sum  CoverSummary
		want int
	}{
		{CoverSummary{Lines: 1, Covered: 1}, 0},
		{CoverSummary{Lines: 1, Covered: 1, RefErrors: []string{"d: exit status 3"}}, 1},
	} {
		var out bytes.Buffer
		h := &harness{out: &out, work: dir, jsonOut: filepath.Join(dir, "cover.json"), rep: &Report{Command: "cover", Started: time.Now()}}
		sum := tc.sum
		h.rep.Coverage = &sum
		if got := h.finish(); got != tc.want {
			t.Errorf("%+v: exit %d, want %d\n%s", tc.sum, got, tc.want, out.String())
		}
	}
}

// TestPackageDirs: a library package nested in a dogfood is queried
// with it; generated code, cases and repros are not packages of it.
func TestPackageDirs(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"pkg.mk":            "",
		"src/main.mk":       "",
		"lib/pkg.mk":        "",
		"lib/inner/pkg.mk":  "",
		"repro/r1/pkg.mk":   "",
		"cases/data/pkg.mk": "",
		"src/gen/x/pkg.mk":  "",
		"lib/inner/a/b.mk":  "",
	})
	got := packageDirs(dir)
	want := []string{".", "lib", filepath.Join("lib", "inner")}
	if !slices.Equal(got, want) {
		t.Errorf("packageDirs: %q, want %q", got, want)
	}
}

// TestHardwareBoundCoverage: a hardware-bound module's function executed
// on one target by a .targets-restricted case counts as executed across
// hosted targets; an unrestricted execution on one target, or a
// non-hardware module, still reports the missing target.
func TestHardwareBoundCoverage(t *testing.T) {
	items := []Item{
		{Mod: "std.gpu", Text: "gpu.Open(h: &Heap, io: Io): Error!Device", InSect: "std.gpu"},
		{Mod: "std.window", Text: "window.Open(h: &Heap, io: Io): Error!Window", InSect: "std.window"},
		{Mod: "std.core", Text: "str.Trim(self): str", InSect: ""},
	}
	c := newCoverage(items)
	c.refsOK = true
	c.hosted = []string{targetWindows, targetLinux}
	for _, lc := range c.lines {
		lc.Refs = []string{"d"}
		lc.CodeRefs = []string{"d"}
		lc.Present = true
		lc.Exec = []string{"d/case (windows)"}
		lc.ExecOn = []string{targetWindows}
	}
	// std.gpu has ExecRestricted = true -> covered (executed).
	c.lines[0].ExecRestricted = true
	// std.window has ExecRestricted = false -> gap on x86_64-linux.
	// std.core has ExecRestricted = true -> still gap on x86_64-linux.
	c.lines[2].ExecRestricted = true

	(&harness{}).judgeLines(c, &CoverSummary{})
	if got := c.lines[0].Verdict; got != "covered (executed)" {
		t.Errorf("std.gpu restricted: %q, want covered (executed)", got)
	}
	if got := c.lines[1].Verdict; got != "gap: not executed on x86_64-linux" {
		t.Errorf("std.window unrestricted: %q, want gap on linux", got)
	}
	if got := c.lines[2].Verdict; got != "gap: not executed on x86_64-linux" {
		t.Errorf("std.core restricted: %q, want gap on linux", got)
	}
}
