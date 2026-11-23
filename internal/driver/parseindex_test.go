// Why: one cache file per parsed file costs a file open per file on every
// warm check; the driver stores a module's parse summaries together in one
// parse index, and that index must stay correct across edits, programs
// that share module paths, targets and concurrent builds.
// What: the parse-index tests (one index per module, edits and undo, several
// programs and targets in one cache, release after parse, concurrent
// rewrites, content checks, MK_CACHE_VERIFY) and their helpers.
// How: checks small programs with checkSession, reads the stored indexes
// back with parseIndexes, and compares Trace.FilesParsed across runs.

package driver

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"mk/internal/cache"
	"mk/internal/iface"
	"mk/internal/source"
	"mk/internal/types"
)

// checkSession runs one Check of cfg and returns the session, for its
// builds' modules.
func checkSession(t *testing.T, cfg Config) (*Session, Result) {
	t.Helper()
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, s.Check()
}

// parseIndexes reads the parse index of every module of s's std and
// package builds, by module path.
func parseIndexes(t *testing.T, s *Session, dir string) map[string]*parseIndex {
	t.Helper()
	st, err := cache.Open(dir, cache.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*parseIndex{}
	for _, b := range []*build{s.stdB, s.pkgB} {
		for _, m := range b.mods {
			raw, ok, _ := st.Get(cache.KindManifest, parseIndexKey(s.w, m))
			if !ok {
				continue
			}
			x, err := decodeParseIndex(raw)
			if err != nil {
				t.Fatalf("%s: %v", m.path, err)
			}
			out[m.path] = x
		}
	}
	return out
}

// fileKeys are the parse summary keys of m's files, by path.
func fileKeys(s *Session, m *modNode) map[string]source.Hash {
	out := map[string]source.Hash{}
	for _, f := range m.files {
		if f.src != nil && !f.tooLarge {
			out[f.src.Path] = s.w.front.parseSummaryKey(s.w, m, f)
		}
	}
	return out
}

// countFiles counts the files under dir, 0 when it does not exist.
func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return filepath.SkipDir
		}
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func parseIndexTree() map[string]string {
	return map[string]string{
		"pkg.mk":   manifest(nil),
		"lib/l.mk": "export Helper(): int {\n    return 1\n}\n",
		"lib/m.mk": "export Other(): int {\n    return 2\n}\n",
		"app/a.mk": "import lib\n\nexport G(): int {\n    return lib.Helper() + lib.Other()\n}\n",
	}
}

// TestOneParseIndexPerModule: a cold check writes no per-file parse
// entry, every module's index holds a summary for each of its files, and a
// warm check parses nothing.
func TestOneParseIndexPerModule(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Root: writeTree(t, parseIndexTree()), CacheDir: dir, Target: types.X86_64Windows,
		BuildID: source.HashString("test", "d789")}
	s, cold := checkSession(t, cfg)
	clean(t, cold)
	if n := countFiles(t, filepath.Join(cache.DirPath(dir, string(cache.KindParse)))); n != 0 {
		t.Errorf("%d per-file parse entries, want 0", n)
	}
	xs := parseIndexes(t, s, dir)
	mods := 0
	for _, b := range []*build{s.stdB, s.pkgB} {
		for _, m := range b.mods {
			keys := fileKeys(s, m)
			if len(keys) == 0 {
				continue
			}
			mods++
			x := xs[m.path]
			if x == nil {
				t.Errorf("%s: no parse index", m.path)
				continue
			}
			for p, k := range keys {
				if _, ok := x.entries[k]; !ok {
					t.Errorf("%s: no summary of %s", m.path, p)
				}
			}
			if x.gen != 1 {
				t.Errorf("%s: generation %d after one cold check, want 1 (one write)", m.path, x.gen)
			}
		}
	}
	if mods < 10 || len(xs) != mods {
		t.Errorf("%d indexes for %d modules with files", len(xs), mods)
	}
	warm := frontCheck(t, cfg)
	clean(t, warm)
	if len(warm.Trace.FilesParsed) > 0 {
		t.Errorf("warm check parsed %q", warm.Trace.FilesParsed)
	}
}

// TestEditParsesOnlyTheEditedFileAndUndoHits: an edit misses only the
// edited file in the parse step (its module's other files are parsed at
// stage 3 because the module misses), and the edit undone hits: the
// previous generation is kept.
func TestEditParsesOnlyTheEditedFileAndUndoHits(t *testing.T) {
	files := parseIndexTree()
	root := writeTree(t, files)
	cfg := Config{Root: root, CacheDir: t.TempDir(), Target: types.X86_64Windows, BuildID: source.HashString("test", "d789")}
	clean(t, frontCheck(t, cfg))
	path := filepath.Join(root, "lib", "l.mk")
	if err := os.WriteFile(path, []byte(strings.Replace(files["lib/l.mk"], "return 1", "return 3", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := frontCheck(t, cfg)
	clean(t, edit)
	if got := slices.Sorted(slices.Values(edit.Trace.FilesParsed)); !slices.Equal(got, []string{"lib/l.mk", "lib/m.mk"}) {
		t.Errorf("edit parsed %q, want lib's two files", got)
	}
	if err := os.WriteFile(path, []byte(files["lib/l.mk"]), 0o644); err != nil {
		t.Fatal(err)
	}
	undo := frontCheck(t, cfg)
	clean(t, undo)
	if len(undo.Trace.FilesParsed) > 0 {
		t.Errorf("undo parsed %q, want nothing", undo.Trace.FilesParsed)
	}
}

// TestSameModulePathTwoPrograms: two programs whose modules have the
// same paths and different text share one cache. Their indexes are one
// entry per path, so each rewrites the other's; the index's last
// parseIndexGenerations generations keep both warm while fewer rewrites
// than that come in between, past that one misses, and neither ever reads
// the other's summary.
func TestSameModulePathTwoPrograms(t *testing.T) {
	dir := t.TempDir()
	id := source.HashString("test", "d789")
	a := Config{Root: writeTree(t, parseIndexTree()), CacheDir: dir, Target: types.X86_64Windows, BuildID: id}
	bf := parseIndexTree()
	bf["lib/l.mk"] = "export Helper(): int {\n    return 10\n}\n"
	bf["app/a.mk"] = "import lib\n\nexport G(): int {\n    return lib.Helper() - lib.Other()\n}\n"
	b := Config{Root: writeTree(t, bf), CacheDir: dir, Target: types.X86_64Windows, BuildID: id}
	clean(t, frontCheck(t, a))
	coldB := frontCheck(t, b)
	clean(t, coldB)
	if got := slices.Sorted(slices.Values(coldB.Trace.FilesParsed)); !slices.Contains(got, "lib/l.mk") || slices.ContainsFunc(got, func(p string) bool { return strings.HasPrefix(p, "std/") }) {
		t.Errorf("b's cold check parsed %q, want lib/l.mk and no std file", got)
	}
	for _, c := range []struct {
		name string
		cfg  Config
	}{{"a", a}, {"b", b}, {"a again", a}} {
		r := frontCheck(t, c.cfg)
		clean(t, r)
		if len(r.Trace.FilesParsed) > 0 {
			t.Errorf("%s: warm check parsed %q", c.name, r.Trace.FilesParsed)
		}
	}
	// The limit: parseIndexGenerations generations. Each edit of a's lib
	// rewrites lib's index once (a check that hits every file rewrites
	// nothing). b's lib/l.mk summary, written by b's cold check, survives
	// parseIndexGenerations-1 rewrites and goes with the next, so b's next
	// check parses its lib files again: a miss, never another program's
	// summary. Both sides of the bound are checked.
	al := filepath.Join(a.Root, "lib", "l.mk")
	for i := 1; i <= parseIndexGenerations; i++ {
		v := "return " + strconv.Itoa(100+i) // b's l.mk returns 10: never its content
		if err := os.WriteFile(al, []byte(strings.Replace(parseIndexTree()["lib/l.mk"], "return 1", v, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		clean(t, frontCheck(t, a))
		if i == parseIndexGenerations-1 {
			kept := frontCheck(t, b)
			clean(t, kept)
			if len(kept.Trace.FilesParsed) > 0 {
				t.Errorf("b after %d rewrites by a parsed %q, want nothing (its summaries kept)", i, kept.Trace.FilesParsed)
			}
		}
	}
	evicted := frontCheck(t, b)
	clean(t, evicted)
	if !slices.Contains(evicted.Trace.FilesParsed, "lib/l.mk") {
		t.Errorf("b after %d rewrites by a parsed %q, want lib/l.mk among them (its summary dropped)", parseIndexGenerations, evicted.Trace.FilesParsed)
	}
	// Each answer is its own program's: a summary of the other program's
	// lib/l.mk would export the same names, so compare the checked
	// results with fresh caches.
	for _, c := range []Config{a, b} {
		warm := frontCheck(t, c)
		fresh := c
		fresh.CacheDir = t.TempDir()
		if got, want := jsonOf(t, warm), jsonOf(t, frontCheck(t, fresh)); string(got) != string(want) {
			t.Errorf("warm --json %s, cold %s", got, want)
		}
	}
}

// TestThreePrograms: three programs whose modules have the same paths
// (main, lib, app) and different text share one cache, as tools/bench's rows
// do. After each one's cold check, no warm check of any of them parses a
// file, round after round: with one previous generation kept, each cold or
// missing check dropped the oldest program's summaries, and every warm check
// reparsed.
func TestThreePrograms(t *testing.T) {
	dir := t.TempDir()
	id := source.HashString("test", "d789")
	var cfgs []Config
	for i := range 3 {
		files := parseIndexTree()
		files["lib/l.mk"] = "export Helper(): int {\n    return " + strconv.Itoa(10+i) + "\n}\n"
		files["app/a.mk"] = "import lib\n\nexport G(): int {\n    return lib.Helper() * " + strconv.Itoa(2+i) + " + lib.Other()\n}\n"
		cfgs = append(cfgs, Config{Root: writeTree(t, files), CacheDir: dir, Target: types.X86_64Windows, BuildID: id})
	}
	for _, c := range cfgs {
		clean(t, frontCheck(t, c))
	}
	for round := 1; round <= 2; round++ {
		for i, c := range cfgs {
			r := frontCheck(t, c)
			clean(t, r)
			if len(r.Trace.FilesParsed) > 0 {
				t.Errorf("round %d, program %d: a warm check parsed %q", round, i, r.Trace.FilesParsed)
			}
		}
	}
}

// TestTargetFree: a check for another target reads the summaries the
// first target's check stored. A module whose files are the same in both
// builds is not rewritten, so none of its files missed in the parse step.
func TestTargetFree(t *testing.T) {
	dir := t.TempDir()
	win := Config{Root: writeTree(t, parseIndexTree()), CacheDir: dir, Target: types.X86_64Windows,
		BuildID: source.HashString("test", "d789")}
	sw, r := checkSession(t, win)
	clean(t, r)
	before := parseIndexes(t, sw, dir)
	lin := win
	lin.Target = types.X86_64Linux
	sl, r := checkSession(t, lin)
	clean(t, r)
	after := parseIndexes(t, sl, dir)
	same := 0
	for _, b := range []*build{sl.stdB, sl.pkgB} {
		for _, m := range b.mods {
			x, y := before[m.path], after[m.path]
			if x == nil || y == nil {
				continue
			}
			keys := fileKeys(sl, m)
			all := len(keys) > 0
			for _, k := range keys {
				if _, ok := x.entries[k]; !ok {
					all = false
				}
			}
			if !all {
				continue
			}
			same++
			if y.gen != x.gen {
				t.Errorf("%s: rewritten by the linux check (generation %d to %d), though every file was stored", m.path, x.gen, y.gen)
			}
		}
	}
	if same < 10 {
		t.Errorf("only %d modules with the same files on both targets", same)
	}
}

// TestParseErrorNotStored: a file with a parse diagnostic gets no
// summary, and its module's other files keep theirs.
func TestParseErrorNotStored(t *testing.T) {
	files := parseIndexTree()
	files["lib/bad.mk"] = "export Bad(: int {\n"
	dir := t.TempDir()
	cfg := Config{Root: writeTree(t, files), CacheDir: dir, Target: types.X86_64Windows, BuildID: source.HashString("test", "d789")}
	s, r := checkSession(t, cfg)
	if len(r.Report.Diags) == 0 {
		t.Fatal("no parse diagnostic")
	}
	lib := s.pkgB.byName["lib"]
	x := parseIndexes(t, s, dir)["lib"]
	if x == nil {
		t.Fatal("no parse index for lib")
	}
	for p, k := range fileKeys(s, lib) {
		_, ok := x.entries[k]
		if want := p != "lib/bad.mk"; ok != want {
			t.Errorf("%s stored %v, want %v", p, ok, want)
		}
	}
	warm := frontCheck(t, cfg)
	if !slices.Contains(warm.Trace.FilesParsed, "lib/bad.mk") {
		t.Errorf("warm parsed %q, want lib/bad.mk among them", warm.Trace.FilesParsed)
	}
	if got, want := codes(warm), codes(r); !slices.Equal(got, want) {
		t.Errorf("warm codes %v, cold %v", got, want)
	}
}

// TestIndexRoundTrips: the index encoding decodes to itself, and a
// truncated or foreign payload is refused (an empty index on read).
func TestIndexRoundTrips(t *testing.T) {
	x := &parseIndex{gen: 7, entries: map[source.Hash]parseEntry{
		source.HashString("t", "a"): {gen: 7, enc: []byte("one")},
		source.HashString("t", "b"): {gen: 6, enc: nil},
	}}
	b := encodeParseIndex(x)
	y, err := decodeParseIndex(b)
	if err != nil {
		t.Fatal(err)
	}
	if y.gen != 7 || len(y.entries) != 2 || string(y.entries[source.HashString("t", "a")].enc) != "one" ||
		y.entries[source.HashString("t", "b")].gen != 6 {
		t.Errorf("round trip: %+v", y)
	}
	if string(encodeParseIndex(y)) != string(b) {
		t.Error("the encoding is not canonical")
	}
	for _, bad := range [][]byte{b[:len(b)-1], append(slices.Clone(b), 0), {2, 0, 0}} {
		if _, err := decodeParseIndex(bad); err == nil {
			t.Errorf("decoded %x", bad)
		}
	}
}

// ownIndexOnly: the module of s's builds whose files lie under prefix (a
// target selection's directory: std/sys/<os>/, gen/<target>/c/) has a parse
// index that holds exactly its own files' summaries. With several
// generations kept, an index shared by three targets would not miss on a
// rotation, it would hold every target's files; so the rotation tests read
// the index itself.
func ownIndexOnly(t *testing.T, s *Session, prefix, label string) {
	t.Helper()
	found := false
	builds := []*build{s.stdB, s.pkgB}
	if c := s.pkgB.cBuild; c != nil && c != s.pkgB {
		builds = append(builds, c) // the C import modules (cimport.go)
	}
	for _, b := range builds {
		for _, m := range b.mods {
			if len(m.files) == 0 || m.files[0].src == nil || !strings.HasPrefix(m.files[0].src.Path, prefix) {
				continue
			}
			found = true
			own := fileKeys(s, m)
			x := s.w.front.readParseIndex(s.w, m)
			for p, k := range own {
				if _, ok := x.entries[k]; !ok {
					t.Errorf("%s: %s's index lacks %s", label, m.path, p)
				}
			}
			if len(x.entries) != len(own) {
				t.Errorf("%s: %s's index holds %d summaries, its directory %d files: another directory shares it", label, m.path, len(x.entries), len(own))
			}
		}
	}
	if !found {
		t.Errorf("%s: no module under %s", label, prefix)
	}
}

// TestTargetRotation: one program checked for three OS targets in turn
// in one cache. std.sys is one module path with a directory per OS
// (std/pkg.mk's targets); its index is keyed by the directory too, so each
// directory's index holds its own files only, and after the first round no
// check parses a std file.
func TestTargetRotation(t *testing.T) {
	dir := t.TempDir()
	root := writeTree(t, parseIndexTree())
	id := source.HashString("test", "d789")
	targets := []types.Target{types.X86_64Windows, types.X86_64Linux, types.X86_64Nutshell}
	for round := range 3 {
		for _, tg := range targets {
			s, r := checkSession(t, Config{Root: root, CacheDir: dir, Target: tg, BuildID: id})
			clean(t, r)
			ownIndexOnly(t, s, "std/sys/", fmt.Sprintf("round %d, %s", round, tg.Name))
			if round == 0 {
				continue
			}
			var std []string
			for _, p := range r.Trace.FilesParsed {
				if strings.HasPrefix(p, "std/") {
					std = append(std, p)
				}
			}
			if len(std) > 0 {
				t.Errorf("round %d, %s: a warm check parsed %d std files, first %s", round, tg.Name, len(std), std[0])
			}
		}
	}
}

// TestIndexReleasedAfterParse: no module holds its parse index once
// the parse step is done, so the encodings do not live through the later
// stages, and a second check in the same session parses nothing.
func TestIndexReleasedAfterParse(t *testing.T) {
	cfg := Config{Root: writeTree(t, parseIndexTree()), CacheDir: t.TempDir(), Target: types.X86_64Windows, BuildID: source.HashString("test", "d789")}
	s, r := checkSession(t, cfg)
	clean(t, r)
	for i := range 2 {
		for _, b := range []*build{s.stdB, s.pkgB} {
			for _, m := range b.mods {
				if m.pidx != nil {
					t.Errorf("check %d: %s still holds its parse index", i, m.path)
				}
			}
		}
		r = s.Check()
		clean(t, r)
		if len(r.Trace.FilesParsed) > 0 {
			t.Errorf("check %d again in the session parsed %q", i+2, r.Trace.FilesParsed)
		}
	}
}

// TestUpdateMergesAConcurrentRewrite: another process rewrote a
// module's index between this build's read in the parse step and its
// rewrite; the rewrite reads the index again, so the other process's entry
// survives, beside this build's files at the new generation.
func TestUpdateMergesAConcurrentRewrite(t *testing.T) {
	cfg := Config{Root: writeTree(t, parseIndexTree()), CacheDir: t.TempDir(), Target: types.X86_64Windows, BuildID: source.HashString("test", "d789")}
	s, r := checkSession(t, cfg)
	clean(t, r)
	lib := s.pkgB.byName["lib"]
	fc := s.w.front
	x := fc.readParseIndex(s.w, lib)
	if len(x.entries) != 2 {
		t.Fatalf("lib's index holds %d entries, want 2", len(x.entries))
	}
	foreign := source.HashString("test", "foreign")
	y := &parseIndex{gen: x.gen + 1, entries: maps.Clone(x.entries)}
	y.entries[foreign] = parseEntry{gen: y.gen, enc: []byte("another process's summary")}
	if err := fc.st.Put(cache.KindManifest, parseIndexKey(s.w, lib), encodeParseIndex(y)); err != nil {
		t.Fatal(err)
	}
	current := map[source.Hash][]byte{}
	for k, e := range x.entries {
		current[k] = e.enc
	}
	fc.updateParseIndex(s.w, lib, current)
	z := fc.readParseIndex(s.w, lib)
	if z.gen != y.gen+1 {
		t.Errorf("generation %d after the rewrite, want %d", z.gen, y.gen+1)
	}
	if e, ok := z.entries[foreign]; !ok || string(e.enc) != "another process's summary" {
		t.Errorf("the concurrent rewrite's entry was lost (%v)", ok)
	}
	for k := range current {
		if z.entries[k].gen != z.gen {
			t.Errorf("this build's entry has generation %d, want %d", z.entries[k].gen, z.gen)
		}
	}
}

// TestSummaryCheckedAgainstContent: a summary of other content planted
// under a file's key is not used: loadSummary compares the stored summary's
// content hash with the file's.
func TestSummaryCheckedAgainstContent(t *testing.T) {
	files := parseIndexTree()
	root := writeTree(t, files)
	cfg := Config{Root: root, CacheDir: t.TempDir(), Target: types.X86_64Windows, BuildID: source.HashString("test", "d789")}
	s1, r := checkSession(t, cfg)
	clean(t, r)
	oldKey := fileKeys(s1, s1.pkgB.byName["lib"])["lib/l.mk"]
	oldEnc := s1.w.front.readParseIndex(s1.w, s1.pkgB.byName["lib"]).entries[oldKey].enc
	if oldEnc == nil {
		t.Fatal("no stored summary of lib/l.mk")
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "l.mk"), []byte(strings.Replace(files["lib/l.mk"], "return 1", "return 3", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, r := checkSession(t, cfg)
	clean(t, r)
	lib := s2.pkgB.byName["lib"]
	fc := s2.w.front
	var f *fileNode
	for _, x := range lib.files {
		if x.src != nil && x.src.Path == "lib/l.mk" {
			f = x
		}
	}
	key, content := fc.parseSummaryKeyOf(s2.w, lib, f)
	newEnc := fc.readParseIndex(s2.w, lib).entries[key].enc
	if newEnc == nil || key == oldKey {
		t.Fatal("no stored summary of the edited lib/l.mk under a new key")
	}
	for _, c := range []struct {
		name string
		enc  []byte
		want bool
	}{{"its own summary", newEnc, true}, {"the old content's summary", oldEnc, false}} {
		lib.pidx = &parseIndex{gen: 1, entries: map[source.Hash]parseEntry{key: {gen: 1, enc: c.enc}}}
		if _, _, ok := fc.loadSummary(key, content, s2.w, lib, f); ok != c.want {
			t.Errorf("%s under the edited file's key: loaded %v, want %v", c.name, ok, c.want)
		}
	}
}

// TestCImportRotation: a C import is the other kind of target
// selection: one module path (c.k) with a directory per target
// (gen/<target>/c/). One package checked for three targets in turn in one
// cache parses no C import file after the first round (it fails if the
// directory is hashed into the key for std modules only).
func TestCImportRotation(t *testing.T) {
	tools := needTools(t)
	dir := t.TempDir()
	root := writeTree(t, map[string]string{
		"pkg.mk": manifest(map[string]string{
			"c_imports":      `[pkg.CImport(name: "k", headers: ["k.h"], defines: [], macros: [], vendor: false)]`,
			"unsafe_modules": `["app"]`}),
		"k.h":      "int k_one(void);\nint k_add(int a, int b);\n",
		"app/a.mk": "import c.k\n\nexport A(): i32 {\n    unsafe \"k_one is plain C\" {\n        return k.k_one()\n    }\n}\n",
	})
	id := source.HashString("test", "d789-cimport")
	targets := []types.Target{types.X86_64Windows, types.X86_64Linux, types.Aarch64Linux}
	for round := range 3 {
		for _, tg := range targets {
			s, r := checkSession(t, Config{Root: root, CacheDir: dir, Target: tg, BuildID: id, Tools: tools})
			clean(t, r)
			ownIndexOnly(t, s, "gen/", fmt.Sprintf("round %d, %s", round, tg.Name))
			if round == 0 {
				continue
			}
			var gen []string
			for _, p := range r.Trace.FilesParsed {
				if strings.HasPrefix(p, "gen/") {
					gen = append(gen, p)
				}
			}
			if len(gen) > 0 {
				t.Errorf("round %d, %s: a warm check parsed the C import's files %q", round, tg.Name, gen)
			}
		}
	}
}

// TestVerifyReportsMisfiledSummary: a summary of other content stored
// under a file's key (the mis-keyed entry
// TestSummaryCheckedAgainstContent guards against) is E0000 cache
// verify under MK_CACHE_VERIFY, where "parse <key>: summary of lib/l.mk
// differs", and is replaced, so a plain check after it is clean and parses
// nothing. Without the comparison, verify would drop it without a word.
func TestVerifyReportsMisfiledSummary(t *testing.T) {
	cfg := Config{Root: writeTree(t, map[string]string{"pkg.mk": manifest(nil),
		"lib/l.mk": "export Helper(): int {\n    return 1\n}\n"}), CacheDir: t.TempDir(), Target: types.X86_64Windows,
		BuildID: source.HashString("test", "d789")}
	s, r := checkSession(t, cfg)
	clean(t, r)
	lib := s.pkgB.byName["lib"]
	key := s.w.front.parseSummaryKey(s.w, lib, lib.files[0])
	x := s.w.front.readParseIndex(s.w, lib)
	en, ok := x.entries[key]
	if !ok {
		t.Fatal("no stored summary of lib/l.mk")
	}
	p, err := iface.DecodeParseSummary(en.enc)
	if err != nil {
		t.Fatal(err)
	}
	p.ContentHash = source.HashString("mk.src.v1", "other content")
	p.Exports = append(p.Exports, "Planted")
	en.enc = iface.EncodeParseSummary(p)
	x.entries[key] = en
	if err := s.w.front.st.Put(cache.KindManifest, parseIndexKey(s.w, lib), encodeParseIndex(x)); err != nil {
		t.Fatal(err)
	}
	verify := cfg
	verify.CacheVerify = true
	rv := frontCheck(t, verify)
	if len(rv.Report.Diags) != 1 || fact(rv.Report.Diags[0].Diag, "where") != "cache verify" ||
		!strings.HasPrefix(fact(rv.Report.Diags[0].Diag, "what"), "parse ") || !strings.Contains(fact(rv.Report.Diags[0].Diag, "what"), "summary of lib/l.mk differs") {
		t.Errorf("verify check over a misfiled summary: codes %v\n%s", codes(rv), rv.Report.Text(rv.Sources))
	}
	rn := frontCheck(t, cfg)
	clean(t, rn)
	if len(rn.Trace.FilesParsed) > 0 {
		t.Errorf("plain check after the verify parsed %q, want nothing (the entry replaced)", rn.Trace.FilesParsed)
	}
}

// TestParseIndexPopulatedOnColdBuild verifies that a cold build (missing
// v1/manifest, deferWrites active) still populates parse indexes so the very
// first subsequent check parses zero files.
func TestParseIndexPopulatedOnColdBuild(t *testing.T) {
	tools := needTools(t)
	dir := writeTree(t, incPkg("x + 1"))
	cdir := t.TempDir()
	s, err := NewSession(Config{Root: dir, CacheDir: cdir, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(filepath.Join(t.TempDir(), "bin"))
	if err != nil || len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("cold build: err %v paths %v\n%s", err, paths, r.Report.Text(r.Sources))
	}
	rc := frontCheck(t, Config{Root: dir, CacheDir: cdir})
	clean(t, rc)
	if len(rc.Trace.FilesParsed) > 0 {
		t.Errorf("warm check after cold build parsed %q, want nothing", rc.Trace.FilesParsed)
	}
}
