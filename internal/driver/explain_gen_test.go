// Why: mk explain shows, for each error code, a real example that the
// compiler rejects; hand-picked examples would go stale.
// What: TestExplainTable generates explain_table.go from doc/errors.md, the
// verdict blocks of doc/**/*.md and tests/conformance; the other tests check
// that the checked-in table is current and covers the catalogue.
// How: regenerate with `go test ./internal/driver -run TestExplainTable -update`.

package driver

import (
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"mk/internal/diag"
)

const explainTablePath = "explain_table.go"

// designOrder is the preference among design documents for an example: the
// reference, the memory model, the cookbook, the catalogue, then the rest
// in byte order.
var designOrder = []string{"reference.md", "memory.md", "cookbook.md", "errors.md"}

// genBlock is one fenced mk block.
type genBlock struct {
	at      string // doc/<path>.md:<line of the verdict>, doc/std/fs.md:12
	section string // the ## heading it sits under
	line    int
	verdict string   // ok, parse, reject
	codes   []string // for reject
	text    string
}

// verdictOf reads "// verdict: reject E0501 E0502" into its word and codes.
func verdictOf(first string) (string, []string) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(first), "// verdict: ")
	if !ok {
		return "", nil
	}
	f := strings.Fields(strings.ReplaceAll(rest, ",", " "))
	if len(f) == 0 {
		return "", nil
	}
	return f[0], f[1:]
}

func designGenBlocks(t *testing.T, root string) []genBlock {
	t.Helper()
	dir := filepath.Join(root, "doc")
	paths, err := markdownFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	rank := func(p string) int {
		for i, d := range designOrder {
			if p == d {
				return i
			}
		}
		return len(designOrder)
	}
	sort.SliceStable(paths, func(i, j int) bool {
		if rank(paths[i]) != rank(paths[j]) {
			return rank(paths[i]) < rank(paths[j])
		}
		return paths[i] < paths[j]
	})
	var out []genBlock
	for _, p := range paths {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
		section := ""
		for i := 0; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "## ") {
				section = lines[i]
			}
			if strings.TrimSpace(lines[i]) != "```mk" {
				continue
			}
			start := i + 1
			var body []string
			for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "```"; i++ {
				body = append(body, lines[i])
			}
			if len(body) == 0 {
				continue
			}
			v, codes := verdictOf(body[0])
			out = append(out, genBlock{at: "doc/" + p + ":" + strconv.Itoa(start+1), section: p + section,
				line: start, verdict: v, codes: codes, text: strings.Join(body, "\n") + "\n"})
		}
	}
	return out
}

// conformanceExample finds a reject example for code under tests/conformance:
// codes/<code>.mk, then codes/<code>_*.mk, then a rules/<ID>_reject_*.mk
// whose verdict names the code (with its <ID>_accept_*.mk as the accepting
// example).
func conformanceExample(t *testing.T, root, code string) (rej, acc ExplainExample) {
	t.Helper()
	dir := filepath.Join(root, "tests", "conformance")
	read := func(p string) (ExplainExample, []string, string) {
		raw, err := os.ReadFile(p)
		if err != nil {
			return ExplainExample{}, nil, ""
		}
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		first, _, _ := strings.Cut(text, "\n")
		v, codes := verdictOf(first)
		rel, _ := filepath.Rel(root, p)
		return ExplainExample{At: filepath.ToSlash(rel) + ":1", Text: text}, codes, v
	}
	has := func(codes []string) bool {
		for _, c := range codes {
			if c == code {
				return true
			}
		}
		return false
	}
	cands := []string{filepath.Join(dir, "codes", code+".mk")}
	more, _ := filepath.Glob(filepath.Join(dir, "codes", code+"_*.mk"))
	sort.Strings(more)
	cands = append(cands, more...)
	for _, p := range cands {
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			continue
		}
		if ex, codes, v := read(p); v == "reject" && has(codes) {
			return ex, ExplainExample{}
		}
	}
	// A multi-file unit: codes/<code>/ (an import or a module error), its
	// .mk files in byte order, each under a "// <file>" line.
	if fi, err := os.Stat(filepath.Join(dir, "codes", code)); err == nil && fi.IsDir() {
		var files []string
		_ = filepath.WalkDir(filepath.Join(dir, "codes", code), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && (d.Name() == "gen" || d.Name() == ".mk-cache") {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(p, ".mk") {
				files = append(files, p)
			}
			return nil
		})
		sort.Strings(files)
		var b strings.Builder
		found := false
		for _, p := range files {
			ex, codes, _ := read(p)
			found = found || has(codes)
			rel, _ := filepath.Rel(filepath.Join(dir, "codes", code), p)
			b.WriteString("// " + filepath.ToSlash(rel) + "\n" + ex.Text)
		}
		if found {
			rel, _ := filepath.Rel(root, filepath.Join(dir, "codes", code))
			return ExplainExample{At: filepath.ToSlash(rel) + "/", Text: b.String()}, ExplainExample{}
		}
	}
	var rules []string
	_ = filepath.WalkDir(filepath.Join(dir, "rules"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(filepath.Base(p), "_reject_") && strings.HasSuffix(p, ".mk") {
			rules = append(rules, p)
		}
		return nil
	})
	sort.Strings(rules)
	for _, p := range rules {
		ex, codes, v := read(p)
		if v != "reject" || !has(codes) {
			continue
		}
		id, _, _ := strings.Cut(filepath.Base(p), "_reject_")
		accs, _ := filepath.Glob(filepath.Join(filepath.Dir(p), id+"_accept_*.mk"))
		sort.Strings(accs)
		for _, a := range accs {
			if ax, _, av := read(a); av == "ok" {
				return ex, ax
			}
		}
		return ex, ExplainExample{}
	}
	return ExplainExample{}, ExplainExample{}
}

// catalogueTriggers reads the trigger column of 04e's table by code.
func catalogueTriggers(t *testing.T, root string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "doc", "errors.md"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, l := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(l, "| E") {
			continue
		}
		cells := strings.Split(strings.Trim(l, "|"), " | ")
		if len(cells) < 4 {
			continue
		}
		out[strings.TrimSpace(cells[0])] = strings.TrimSpace(cells[2])
	}
	return out
}

// generateExplainTable builds explain_table.go's text from the tree at root.
func generateExplainTable(t *testing.T, root string) []byte {
	t.Helper()
	blocks := designGenBlocks(t, root)
	triggers := catalogueTriggers(t, root)
	var b strings.Builder
	b.WriteString("// Code generated by explain_gen_test.go (go test ./internal/driver -run TestExplainTable -update); DO NOT EDIT.\n")
	b.WriteString("// The table of mk explain: one row per code of doc/errors.md, with\n")
	b.WriteString("// its examples from doc/ or tests/conformance.\n\n")
	b.WriteString("package driver\n\nvar explainTable = []explainRow{\n")
	q := strconv.Quote
	ex := func(e ExplainExample) string {
		if e.At == "" {
			return "ExplainExample{}"
		}
		return "ExplainExample{At: " + q(e.At) + ", Text: " + q(e.Text) + "}"
	}
	for _, row := range diag.Catalogue() {
		code := row.Code.String()
		var rej, acc ExplainExample
		if conformanceFirst[code] {
			if rej, acc = conformanceExample(t, root, code); rej.At != "" {
				b.WriteString("\t{code: " + q(code) + ", trigger: " + q(triggers[code]) + ",\n\t\treject: " + ex(rej) + ",\n\t\taccept: " + ex(acc) + "},\n")
				continue
			}
		}
		// A block whose std imports this tree's std has is preferred, so
		// mk explain never shows one that waits on a module not yet built
		// (E0603's blocks once imported a std.tls not yet built); then a
		// block with this code alone; then the first.
		best := -1
		for _, need := range []bool{true, false} {
			for i, bl := range blocks {
				if bl.verdict != "reject" || !contains(bl.codes, code) || need && !stdImportsExist(root, bl.text) {
					continue
				}
				if best < 0 || (len(bl.codes) == 1 && len(blocks[best].codes) > 1) {
					best = i
				}
				if len(bl.codes) == 1 {
					break
				}
			}
			if best >= 0 {
				break
			}
		}
		if best >= 0 {
			r := blocks[best]
			rej = ExplainExample{At: r.at, Text: r.text}
			dist := -1
			for _, bl := range blocks {
				if bl.verdict != "ok" || bl.section != r.section || !stdImportsExist(root, bl.text) {
					continue
				}
				d := bl.line - r.line
				if d < 0 {
					d = -d
				}
				if dist < 0 || d < dist {
					dist, acc = d, ExplainExample{At: bl.at, Text: bl.text}
				}
			}
		} else {
			rej, acc = conformanceExample(t, root, code)
		}
		b.WriteString("\t{code: " + q(code) + ", trigger: " + q(triggers[code]) + ",\n\t\treject: " + ex(rej) + ",\n\t\taccept: " + ex(acc) + "},\n")
	}
	b.WriteString("}\n")
	out, err := format.Source([]byte(b.String()))
	if err != nil {
		t.Fatalf("generated table does not gofmt: %v", err)
	}
	return out
}

// conformanceFirst are the codes whose design blocks show a rarer reason
// than the common one, so mk explain shows tests/conformance's example
// first: E0306's design block is an open set, while the common E0306 is a
// missing variant of the program's own enum. E0406's design block is a
// `defer` pending at a `become`, while the common E0406 is a move of a
// value a `defer` uses.
var conformanceFirst = map[string]bool{"E0306": true, "E0406": true}

// stdImportsExist reports whether every std module a block imports has its
// directory in the tree's std.
func stdImportsExist(root, text string) bool {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		rest, ok := strings.CutPrefix(l, "import ")
		if !ok {
			continue
		}
		if _, path, aliased := strings.Cut(rest, " = "); aliased {
			rest = path
		}
		mod, ok := strings.CutPrefix(strings.TrimSpace(rest), "std.")
		if !ok {
			continue
		}
		if st, err := os.Stat(filepath.Join(root, "std", filepath.FromSlash(strings.ReplaceAll(mod, ".", "/")))); err != nil || !st.IsDir() {
			return false
		}
	}
	return true
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// TestExplainTable rewrites explain_table.go under -update.
func TestExplainTable(t *testing.T) {
	if !*update {
		t.Skip("run with -update to regenerate explain_table.go")
	}
	if err := os.WriteFile(explainTablePath, generateExplainTable(t, filepath.Join("..", "..")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestExplainTableMatchesDesign: the checked-in table is what doc/ and
// tests/conformance generate now. It is red after an edit of doc/ until a
// -update run regenerates the table.
func TestExplainTableMatchesDesign(t *testing.T) {
	if *update {
		t.Skip("regenerating")
	}
	want := generateExplainTable(t, filepath.Join("..", ".."))
	got, err := os.ReadFile(explainTablePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is stale: go test ./internal/driver -run TestExplainTable -update", explainTablePath)
	}
}

// awaitingEmitter are catalogue codes that no compiler phase emits yet,
// each with where the emitter is to come from. A row is deleted here, and
// in internal/corpus's codeExempt (presence_test.go), when the emitter and
// the code's conformance unit land.
var awaitingEmitter = map[diag.Code]string{}

// codeExemptPath is internal/corpus's test file that holds codeExempt, a
// test variable of a package above this one, so it is read as text.
var codeExemptPath = filepath.Join("..", "corpus", "presence_test.go")

// TestExplainEveryCatalogueCode: every catalogue code answers with its name,
// phase, trigger and fix class; most have a rejecting example; a string that
// is no code does not answer.
func TestExplainEveryCatalogueCode(t *testing.T) {
	exempt, err := os.ReadFile(codeExemptPath)
	if err != nil {
		t.Fatal(err)
	}
	withRej, withAcc, n := 0, 0, 0
	var none []string
	for _, row := range diag.Catalogue() {
		e, ok := Explain(strings.ToLower(row.Code.String()))
		if !ok || e.Name != row.Name || e.FixClass != row.FixClass || e.Trigger == "" || e.Phase == "" {
			t.Errorf("%v: %+v", row.Code, e)
			continue
		}
		if from, wait := awaitingEmitter[row.Code]; wait {
			// A code with no emitter has no example: it stays out of the
			// bar, and its row is deleted when an example appears.
			if e.Reject.At != "" || e.Accept.At != "" {
				t.Errorf("%v has an example: delete its awaitingEmitter row (%s) and its codeExempt row", row.Code, from)
			}
			if !strings.Contains(string(exempt), "diag."+row.Code.String()+":") {
				t.Errorf("%v is in awaitingEmitter (%s) but not in %s's codeExempt", row.Code, from, codeExemptPath)
			}
			continue
		}
		n++
		if e.Reject.At != "" {
			withRej++
		} else {
			none = append(none, e.Code)
		}
		if e.Accept.At != "" {
			withAcc++
		}
		if !strings.HasPrefix(e.Text(), e.Code+" "+e.Name) || len(e.JSON()) < 10 {
			t.Errorf("%v: rendering", row.Code)
		}
	}
	if withRej*10 < n*9 {
		t.Errorf("only %d of %d codes have a rejecting example", withRej, n)
	}
	t.Logf("%d codes: %d with a rejecting example, %d with an accepting one; none: %v", n, withRej, withAcc, none)
	for _, s := range []string{"", "E9999", "E05010", "x"} {
		if _, ok := Explain(s); ok {
			t.Errorf("Explain(%q) answered", s)
		}
	}
}
