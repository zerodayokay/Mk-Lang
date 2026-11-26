// Why: std's sources are read by users; a comment or name that records how
// the code was built (decision numbers, plan files, work units, milestones,
// sessions, review ids) tells them nothing about the code.
// What: TestStdNoProcessResidue and TestStdNoProcessResidue_Rules.
// How: regular expressions over every Mk file, file name and directory name
// under std/, with fixed lists of the work-unit ids and process words that
// old comments used.

// Every Mk file under std/: no comment, test name, identifier, file name or
// directory name carries a decision number, a plan or status path, a
// work-unit id, a milestone, a session or a review id. History belongs to
// git; a comment states an invariant, a reference or a reason.
//
// What counts as process residue, in a comment or a test name:
//   - a decision, requirement or review id: a word of the shape PREFIX-N
//     (D-123, D8-30, DD-5, R10-45, MKH-146, CN-14, SRW-RR-2), unless its
//     first part names an external standard (UTF-8, SHA-256, RFC-, P-256)
//     or one of the test families that harnesses still select by name
//     (residueFamilies: RT-F9-..., TERM-8);
//   - a plan, design or status path (plan/19-std.md, design/, status/x.md),
//     a numbered plan file (14-borrow.md), decisions.md, STATUS.md,
//     HANDOFF.md or the build workflow file;
//   - a work-unit (lane) id of residueLanes, in any case (std-io, STD-IO),
//     not inside a path or a longer hyphenated name
//     (tests/bench-lanes/std-net3 and RT8-Call-CopySelf stay);
//   - a lane named in prose: "this lane", "that lane's", "the runtime lane's"
//     (a SIMD or hash lane is "lane 1", "the lane of", "the low lanes", which
//     stay);
//   - a test name that starts with a lane's prefix: an upper-case part and a
//     hyphen ("MISS-Path ...") whose part, at least four letters, begins the
//     last part of a lane id (std-missing, std-sync2);
//   - a milestone (M1 to M10, M5.2) or "milestone N";
//   - a session: "session N", "rerun N", "relay N", or one named by whose
//     or which it was ("owner's session", "this rerun", "an earlier relay");
//   - history: "it did until <id> changed it" (a rule's past belongs to git).
//
// In an identifier: a squashed lane id ("rtio2"), or a word part review,
// rerun, session or milestone. In a file or directory name: one of those, or
// a part that is m<N>, f<N>, s<N> or r<N> ("io_m6", "tests_f13").
//
// Go test files are not scanned: their names carry a prefix on purpose. Nor
// are testdata directories (named by their Go tests) and dot directories
// such as .mk-cache, whose names are hashes a tool chose. Each top-level std
// directory is a subtest, and the Mk files directly under std/ (pkg.mk) are
// the subtest "toplevel", so one directory can be checked alone.
// TestStdNoProcessResidue_Rules pins each rule on what it must and must not
// report.

package std

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// residueLanes are the work-unit ids that old comments and names used. Only
// ids with a hyphen or a digit are listed, so a plain word ("check",
// "repl") never matches.
var residueLanes = []string{
	"bench-core", "bench-final", "bench-std", "bench8", "compiler-defects7",
	"compiler-defects8", "conformance7", "conformance8", "core-new", "core8",
	"design7", "design8", "df-chat", "df-close", "df-close8", "df-imgproc",
	"df-kernel", "df-kvd", "df-lox", "df-mgrep", "df-notes", "df-orderbook",
	"df-pack", "df-procman", "df-raft", "df-textkit", "df8-bigcalc", "df8-chat",
	"df8-cron", "df8-fetch", "df8-fm", "df8-kvd", "df8-orderbook", "df8-pack",
	"df8-textkit", "df8-tracer", "dg7r1-else", "dg7r5-warmgen", "diag7",
	"dogfood-harness", "evidence10", "ffi8", "flows8", "flows8-classify",
	"harness8", "lang-sys", "lang7", "m7-borrow-swap", "m7-check-gaps",
	"m7-d455-btree", "m7-f1-allocator", "perf-back", "perf-checks",
	"perf-codegen", "perf-codegen2", "perf-front", "perf-memory", "perf-mid",
	"perf-runtime", "perf-size", "pilot7", "pkg10", "r1-release", "release10",
	"rt-io", "rt-io2", "rt-sched", "rt10", "rt8", "rw-coll", "rw-core",
	"rw-data", "rw-io", "rw-rt", "simd8", "soundness-lang", "soundness7",
	"spec-borrow", "spec-check", "spec-codegen", "spec-driver", "spec-eval-gen",
	"spec-incremental", "spec-lang", "spec-lang7", "spec-lower", "spec-resolve",
	"spec-runtime", "spec-soundness", "spec-std", "spec-syntax", "spec-sys",
	"spec-testing", "std-big", "std-cli", "std-coll", "std-coll2",
	"std-compress", "std-compress2", "std-crypto", "std-crypto2", "std-csv",
	"std-defects7", "std-defects8", "std-dyn", "std-encoding", "std-fs2",
	"std-hash", "std-hash2", "std-hashmod", "std-http", "std-http2",
	"std-internal-review", "std-io", "std-json", "std-log", "std-math2",
	"std-mem", "std-mem2", "std-missing", "std-net2", "std-net3", "std-print",
	"std-proc2", "std-proc3", "std-regex", "std-rewrite", "std-sim", "std-simd",
	"std-sqlite", "std-sync", "std-sync2", "std-term", "std-text", "std-text2",
	"std-text3", "std-time2", "std-tls", "std-url", "std-vector", "std-ws",
	"std-x509", "std10", "tc10", "wire8",
}

// residueExternal are the first segments of external standard names written
// with a hyphen and a number.
var residueExternal = map[string]bool{
	"UTF": true, "SHA": true, "CRC": true, "FNV": true, "XXH3": true, "XXH": true,
	"ECMA": true, "CVE": true, "HMAC": true, "SipHash": true, "ChaCha20": true,
	"XChaCha20": true, "Poly1305": true, "AES": true, "RFC": true, "ISO": true,
	"IEEE": true, "IPv4": true, "IPv6": true, "BLAKE3": true, "Ed25519": true,
	"X25519": true, "P": true, "MD": true, "Latin": true, "UCS": true,
	"Windows": true, "ASCII": true, "PCG": true, "ARM": true, "LLVM": true,
	"BLAKE2": true, "UAX": true, "Unicode": true, "RISC": true, "IA": true,
	"Base": true, "PKCS": true, "DEFLATE": true, "HTTP": true, "TLS": true,
	"Adler": true, "RSASSA": true, "Apache": true, "BSD": true,
	"Toom": true, "BLAKE2b": true, "WTF": true, "RSA": true, "TEST": true,
	"AVX": true,
}

// residueGenerated are generated Mk files whose text their generator
// outside std writes; fix the generator, then drop the file from this list.
var residueGenerated = map[string]bool{
	"math/tests_intrinsics_vec.mk": true, // internal/eval's TestNativeVectors
}

// residueFamilies are the first segments of test-name families that Go
// harnesses and tools select by name (RT-F9-HeapClassCount, TERM-8): a
// comment may quote such a test.
var residueFamilies = map[string]bool{
	"RT": true, "TERM": true,
}

var (
	residueIDShape   = regexp.MustCompile(`\b[A-Z][A-Za-z0-9]*(?:-[A-Za-z0-9]+)*-[A-Z]{0,4}[0-9]+[a-z]?\b`)
	residueMilestone = regexp.MustCompile(`\bM[0-9]{1,2}(?:\.[0-9]+)*\b|\b[Mm]ilestone [0-9]`)
	residueSession   = regexp.MustCompile(`(?i)\b(?:session|rerun|relay) [0-9]+\b|\b(?:owner's|agent's|this|that|last|next|previous|an earlier|a later) (?:session|rerun|relay)\b`)
	residueLaneProse = regexp.MustCompile(`(?i)\b(?:this|that|another) lane\b(?:'s)?|\bthe [a-z][a-z0-9-]* lane's\b`)
	residueHistory   = regexp.MustCompile(`(?i)\bdid until\b|\buntil [A-Z][A-Za-z0-9]*-[0-9]+ (?:changed|fixed|removed)\b`)
	residueTestLead  = regexp.MustCompile(`^([A-Z][A-Z0-9]+)-[A-Za-z]`)
	residueDocPath   = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])((?:plan|design|status)/[A-Za-z0-9_.-]*|[0-9][0-9]-[a-z0-9-]+\.md|decisions\.md|STATUS\.md|HANDOFF\.md|mk-build\.js)`)
	residueTestName  = regexp.MustCompile(`^\s*test\s+"((?:\\.|[^"\\])*)"`)
	residueIdent     = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\b`)
	residueIdentWord = regexp.MustCompile(`(?i)review|rerun|session|milestone`)
	residueNamePart  = regexp.MustCompile(`^(?:m[0-9]+|f[0-9]+|s[0-9]+|r[0-9]+|review|rerun|session|milestone|lane)$`)
)

type residueRules struct {
	lanes     *regexp.Regexp
	squashed  []string
	laneTails []string // the last part of each hyphenated lane id, four letters or more
}

func loadResidueRules() residueRules {
	list := append([]string(nil), residueLanes...)
	var squashed, tails []string
	for _, id := range list {
		if k := strings.LastIndexByte(id, '-'); k >= 0 && len(id)-k-1 >= 4 {
			tails = append(tails, id[k+1:])
		}
		if s := strings.ReplaceAll(id, "-", ""); strings.Contains(id, "-") && strings.ContainsAny(s, "0123456789") {
			squashed = append(squashed, s)
		}
	}
	// Longest first, so rt-io2 wins over rt-io.
	sort.Slice(list, func(i, j int) bool {
		if len(list[i]) != len(list[j]) {
			return len(list[i]) > len(list[j])
		}
		return list[i] < list[j]
	})
	sort.Strings(squashed)
	sort.Strings(tails)
	quoted := make([]string, len(list))
	for i, id := range list {
		quoted[i] = regexp.QuoteMeta(id)
	}
	// Any case (STD-IO, Rt-Sched). A hyphen after the id makes it part of a
	// longer name (RT8-Call-CopySelf is a test family); a '/' on either side
	// is a path.
	lanes := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_/-])(` + strings.Join(quoted, "|") + `)(?:$|[^A-Za-z0-9_/-])`)
	return residueRules{lanes: lanes, squashed: squashed, laneTails: tails}
}

// textResidue returns the process residue in s, a comment or a test name.
func (r residueRules) textResidue(s string) []string {
	var out []string
	for _, m := range r.lanes.FindAllStringSubmatch(s, -1) {
		out = append(out, "lane id "+m[1])
	}
	for _, m := range residueMilestone.FindAllString(s, -1) {
		if strings.Count(m, ".") < 2 { // M3.5.0 is a POSIX TZ date rule
			out = append(out, "milestone "+m)
		}
	}
	for _, m := range residueSession.FindAllString(s, -1) {
		out = append(out, "session "+m)
	}
	for _, m := range residueLaneProse.FindAllString(s, -1) {
		out = append(out, "lane "+m)
	}
	for _, m := range residueHistory.FindAllString(s, -1) {
		out = append(out, "history clause "+m)
	}
	for _, m := range residueDocPath.FindAllStringSubmatch(s, -1) {
		out = append(out, "process file "+m[1])
	}
	for _, id := range residueIDShape.FindAllString(s, -1) {
		first := id[:strings.IndexByte(id, '-')]
		if !residueExternal[first] && !residueFamilies[first] {
			out = append(out, "decision, requirement or review id "+id)
		}
	}
	return out
}

// splitComment returns the code and the comment of a line, ignoring "//"
// inside a string or a character literal.
func splitComment(line string) (code, comment string) {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '/' && i+1 < len(line) && line[i+1] == '/':
			return line[:i], line[i:]
		}
	}
	return line, ""
}

// stripStrings blanks string and character literals out of code.
func stripStrings(code string) string {
	b := []byte(code)
	var quote byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case quote != 0:
			if c == '\\' && i+1 < len(b) {
				b[i], b[i+1] = ' ', ' '
				i++
			} else if c == quote {
				quote = 0
			} else {
				b[i] = ' '
			}
		case c == '"' || c == '\'':
			quote = c
		}
	}
	return string(b)
}

func (r residueRules) identResidue(id string) string {
	low := strings.ToLower(id)
	for _, s := range r.squashed {
		if strings.Contains(low, s) {
			return "lane id " + s
		}
	}
	if m := residueIdentWord.FindString(id); m != "" {
		return "process word " + m
	}
	return ""
}

func (r residueRules) nameResidue(name string) string {
	low := strings.ToLower(strings.TrimSuffix(name, ".mk"))
	for _, s := range r.squashed {
		if strings.Contains(strings.ReplaceAll(low, "_", ""), s) {
			return "lane id " + s
		}
	}
	for _, part := range strings.FieldsFunc(low, func(c rune) bool { return c == '_' || c == '-' || c == '.' }) {
		if residueNamePart.MatchString(part) {
			return "process part " + part
		}
	}
	return ""
}

// testNameResidue returns the process residue in a test name: textResidue's,
// and a leading work-unit prefix ("MISS-Path ...").
func (r residueRules) testNameResidue(name string) []string {
	out := r.textResidue(name)
	if m := residueTestLead.FindStringSubmatch(name); m != nil && len(m[1]) >= 4 && !residueExternal[m[1]] {
		low := strings.ToLower(m[1])
		for _, tail := range r.laneTails {
			if strings.HasPrefix(tail, low) {
				out = append(out, "lane prefix "+m[1]+"-")
				break
			}
		}
	}
	return out
}

// scanFile reports the residue in the name and the text of one Mk file. rel
// is its path as reported, name its base name.
func (r residueRules) scanFile(p, rel, name string, report func(format string, args ...any)) error {
	if why := r.nameResidue(name); why != "" {
		report("%s: the file name carries a %s: name it for what it holds", rel, why)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	for i, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		code, comment := splitComment(line)
		for _, why := range r.textResidue(comment) {
			report("%s:%d: the comment carries a %s: state the invariant, the reference or the reason instead", rel, i+1, why)
		}
		if m := residueTestName.FindStringSubmatch(code); m != nil {
			for _, why := range r.testNameResidue(m[1]) {
				report("%s:%d: the test name carries a %s: name the behaviour it checks", rel, i+1, why)
			}
		}
		for _, id := range residueIdent.FindAllString(stripStrings(code), -1) {
			if why := r.identResidue(id); why != "" {
				report("%s:%d: the identifier %s carries a %s", rel, i+1, id, why)
			}
		}
	}
	return nil
}

// scanTop reports the residue of the Mk files directly in root (std/'s
// pkg.mk), not of its directories.
func (r residueRules) scanTop(root string, report func(format string, args ...any)) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".mk") {
			continue
		}
		if err := r.scanFile(filepath.Join(root, e.Name()), e.Name(), e.Name(), report); err != nil {
			return err
		}
	}
	return nil
}

// scanDir reports the residue under dir, a top-level directory of root:
// directory names, file names and the text of every Mk file.
func (r residueRules) scanDir(root, dir string, report func(format string, args ...any)) error {
	base := filepath.Join(root, dir)
	return filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rp, _ := filepath.Rel(root, p)
		rel := filepath.ToSlash(rp)
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir // Go fuzz corpora and goldens, named by their Go tests
			}
			if strings.HasPrefix(d.Name(), ".") && p != base {
				return filepath.SkipDir // tool state such as .mk-cache, whose names are hashes
			}
			if why := r.nameResidue(d.Name()); why != "" {
				report("%s/: the directory name carries a %s: name it for what it holds", rel, why)
			}
			return nil
		}
		if !strings.HasSuffix(p, ".mk") || residueGenerated[rel] {
			return nil
		}
		return r.scanFile(p, rel, d.Name(), report)
	})
}

func TestStdNoProcessResidue(t *testing.T) {
	t.Parallel()
	rules := loadResidueRules()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("toplevel", func(t *testing.T) {
		if err := rules.scanTop(".", t.Errorf); err != nil {
			t.Fatal(err)
		}
	})
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "testdata" {
			continue
		}
		dir := e.Name()
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			if err := rules.scanDir(".", dir, t.Errorf); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestStdNoProcessResidue_Rules pins the rules: each positive is reported
// and each negative is not, and the walk reaches a top-level Mk file.
func TestStdNoProcessResidue_Rules(t *testing.T) {
	rules := loadResidueRules()
	text := []struct {
		s    string
		want bool
	}{
		{"// owner's session, llvm-nm on the Linux --debug build", true},
		{"// the runtime lane's convention, judged by internal/stdtest", true},
		{"// This lane's path makes no handle", true},
		{"// the spin this rerun removed", true},
		{"// session 2 found it", true},
		{"// (R-12, asked of rw-rt)", true},
		{"// from core-new's review (CN-14)", true},
		{"// the hash function (it did until DD-5 changed it)", true},
		{"// M7 adds it", true},
		{"// from M10 on", true},
		{"// see status/rw-core.md", true},
		{"// see ../status/rw-core.md", true},
		{"// .claude/workflows/mk-build.js", true},
		{"// moved here by STD-IO", true},
		{"// D-423's release protocol", true},
		{"// D8-30: an interrupted call is prefix", true},
		{"// DD-5's hash", true},
		{"// plan/18-runtime.md asks for it", true},
		{"// as 14-borrow.md says", true},
		{"// (MKH-146)", true},
		{"// moved here by rt8", true},
		{"// two SIMD lanes at once", false},
		{"// pair j goes to lane j % 2; v0 is lane 0's accumulator", false},
		{"// the lane of the next pair", false},
		{"// a TLS session resumes", false},
		{"// the relay outlives the thread's loop", false},
		{"// UTF-8 and SHA-256 over P-256", false},
		{"// the row in tests/bench-lanes/std-net3/ measures it", false},
		{"// RT-F9-HeapClassCount and TERM-8 check it", false},
		{"// RT8-Call-CopySelf checks it", false},
		{"// by design, the handle is closed", false},
		{"// a TZ string such as \"CET-1CEST,M3.5.0,M10.5.0/3\"", false},
	}
	for _, c := range text {
		if got := len(rules.textResidue(c.s)) > 0; got != c.want {
			t.Errorf("textResidue(%q) reports %v (%v), want %v", c.s, got, rules.textResidue(c.s), c.want)
		}
	}
	names := []struct {
		s    string
		want bool
	}{
		{"MISS-Path Base", true},
		{"SYNC2-Once init runs once", true},
		{"D-748 Adler32State: the empty message", true},
		{"a Fetch is closed (D8-30)", true},
		{"Path.Base drops trailing separators", false},
		{"UTF-8 round trip", false},
		{"RT-F8-PanicTextIsCodeName the 16 panic texts", false},
	}
	for _, c := range names {
		if got := len(rules.testNameResidue(c.s)) > 0; got != c.want {
			t.Errorf("testNameResidue(%q) reports %v, want %v", c.s, got, c.want)
		}
	}
	if rules.identResidue("MilestoneGate") == "" || rules.identResidue("Rtio2Ring") == "" || rules.identResidue("HashKey") != "" {
		t.Error("identResidue: want MilestoneGate and Rtio2Ring reported and HashKey not")
	}
	if rules.nameResidue("io_m6.mk") == "" || rules.nameResidue("tests_f13.mk") == "" || rules.nameResidue("core_fmtfloat.mk") != "" {
		t.Error("nameResidue: want io_m6.mk and tests_f13.mk reported and core_fmtfloat.mk not")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pkg.mk"), []byte("// owner's session\nconst X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "tests_m6.mk"), []byte("test \"MISS-Path Base\" () {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var top, lib []string
	if err := rules.scanTop(root, func(f string, a ...any) { top = append(top, fmt.Sprintf(f, a...)) }); err != nil {
		t.Fatal(err)
	}
	if err := rules.scanDir(root, "lib", func(f string, a ...any) { lib = append(lib, fmt.Sprintf(f, a...)) }); err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 || !strings.HasPrefix(top[0], "pkg.mk:1:") {
		t.Errorf("scanTop of a top-level pkg.mk with a session: got %q, want one report at pkg.mk:1", top)
	}
	if len(lib) != 2 {
		t.Errorf("scanDir of lib/tests_m6.mk with a MISS-Path test: got %q, want the file name and the test name", lib)
	}
}
