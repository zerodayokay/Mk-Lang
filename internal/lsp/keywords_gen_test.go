// Why: the keyword hovers must follow the reference as it changes.
// What: generateKeywordTable, TestKeywordTable (which also writes the std
// surface tables), TestKeywordTableCoversTheConstructList,
// TestKeywordTablePinnedRows and TestUniverseDocsQuoteTheReference.
// How: reads the reference's and memory model's table rows and construct
// paragraphs, scores each row for each term and keeps the best one; a term
// in pinnedRows takes the row named there instead.
// Regenerate: go test ./internal/lsp -run TestKeywordTable -update

package lsp

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite keywords_table.go and the std surface tables")

// hoverTerms are the words and operators that get a rule hover: the
// highlighter's keywords and flow words, the reference's other construct
// words, and the operator classes of the reference.
func hoverTerms() []string {
	var out []string
	for _, m := range []map[string]bool{keywords, flowWords} {
		for w := range m {
			out = append(out, w)
		}
	}
	out = append(out, "some", "none", "fields", "naked", "packed", "plain", "align", "open", "cpu", "section", "Scope")
	// asm operand words, which mean something only inside asm(...).
	for w := range asmWords {
		out = append(out, w)
	}
	// Primitive types (every token that names something has a
	// hover); the universe names' rules are universeDocs, quoted by hand.
	for w := range primTypes {
		out = append(out, w)
	}
	out = append(out, operatorTerms...)
	sort.Strings(out)
	return out
}

// mdRow is one table row of a design file.
type mdRow struct {
	section string
	cells   []string
	// sub is the `###` heading the row sits under, or "".
	sub string
}

var (
	codeSpan = regexp.MustCompile("`([^`]*)`")
	wordRe   = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	heading  = regexp.MustCompile(`^## (\d+)\. (.*)$`)
	// pointerRef is a parenthesised link, pointerSentence a sentence that
	// only points elsewhere ("See [..](..).", or a numbered section link).
	pointerRef      = regexp.MustCompile(` ?\(\[[^\]]*\]\([^)]*\)\)`)
	pointerSentence = regexp.MustCompile(` ?(See \[[^\]]*\]\([^)]*\)|\[[Ss]ection \d+\]\([^)]*\)[^.]*)\.`)
	mdLink          = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// readRows reads the table rows of a design file outside code fences,
// skipping header rows; label names the file in each row's section.
func readRows(t *testing.T, path, label string) []mdRow {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	var out []mdRow
	sec, sub := label, ""
	fence := false
	for i, l := range lines {
		if strings.HasPrefix(l, "```") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if m := heading.FindStringSubmatch(l); m != nil {
			sec, sub = label+": "+m[2], ""
			continue
		}
		if t, ok := strings.CutPrefix(l, "### "); ok {
			sub = t
			continue
		}
		if strings.HasPrefix(l, "`") {
			// A paragraph that opens with a construct states its rule.
			out = append(out, mdRow{section: sec, cells: []string{l}, sub: sub})
			continue
		}
		if !strings.HasPrefix(l, "| ") || strings.HasPrefix(l, "|---") {
			continue
		}
		if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "|---") {
			continue // a header row
		}
		out = append(out, mdRow{section: sec, cells: splitRow(l), sub: sub})
	}
	return out
}

// splitRow splits a table row on its unescaped pipes.
func splitRow(l string) []string {
	l = strings.TrimSpace(l)
	l = strings.TrimPrefix(strings.TrimSuffix(l, "|"), "|")
	var cells []string
	var cur strings.Builder
	inCode := false
	for i := 0; i < len(l); i++ {
		switch {
		case l[i] == '\\' && i+1 < len(l) && l[i+1] == '|':
			cur.WriteByte('|')
			i++
		case l[i] == '`':
			inCode = !inCode
			cur.WriteByte('`')
		case l[i] == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(l[i])
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

// mentions reports whether a code span names term: as its first word or
// operator (first), or anywhere in it; infix is set for an operator
// between two operands, as in `p = e`.
func mentions(span, term string) (first, any, infix bool) {
	if isIdentStart(term[0]) {
		ws := wordRe.FindAllString(span, -1)
		for i, w := range ws {
			if w == term {
				return i == 0, true, false
			}
		}
		return false, false, false
	}
	fs := strings.Fields(span)
	for i, f := range fs {
		if f == term || i == 0 && strings.HasPrefix(f, term) && !strings.ContainsAny(f[len(term):], "=<>&|%+-*/") && len(f) > len(term) && isIdentStart(f[len(term)]) {
			return i == 0, true, i == 1 && len(fs) == 3
		}
	}
	return false, false, false
}

// score ranks a row for a term: 8 when the row's first cell opens with a
// code span that starts with it, 6 when a later cell does, 5 when the
// first cell opens with the operator between two operands (`p = e`), 4
// when the first cell's code names it, 3 when the first cell's words name
// it ("CPU features" for cpu), 1 when any other code names it.
func score(r mdRow, term string) int {
	if len(r.cells) == 1 && strings.HasPrefix(r.sub, "`"+term+"`") && strings.HasPrefix(r.cells[0], "`"+term+"` ") {
		// The opening of the term's own subsection (### `match`) defines it.
		return 9
	}
	best := 0
	for ci, cell := range r.cells {
		lead := strings.HasPrefix(strings.TrimSpace(cell), "`")
		for k, m := range codeSpan.FindAllStringSubmatch(cell, -1) {
			first, any, infix := mentions(m[1], term)
			switch {
			case ci == 0 && k == 0 && first && lead:
				return 8
			case k == 0 && first && lead:
				best = max(best, 6)
			case ci == 0 && k == 0 && infix && lead && len(r.cells) > 1:
				best = max(best, 5)
			case ci == 0 && any && len(r.cells) > 1:
				best = max(best, 4)
			case any:
				best = max(best, 1)
			}
		}
		if ci == 0 && len(r.cells) > 1 && isIdentStart(term[0]) {
			for _, w := range wordRe.FindAllString(codeSpan.ReplaceAllString(cell, ""), -1) {
				if strings.EqualFold(w, term) || strings.EqualFold(w, term+"s") {
					best = max(best, 3)
				}
			}
		}
	}
	return best
}

// rowText is a row as a hover paragraph, cut near 500 bytes at a sentence.
func rowText(r mdRow) string {
	text := r.cells[0]
	if len(r.cells) > 1 {
		text += ": " + strings.Join(r.cells[1:], " | ")
	}
	// A hover has no anchors to follow: a pointer elsewhere goes, and a
	// link in a sentence keeps its text.
	text = pointerRef.ReplaceAllString(text, "")
	text = strings.TrimSpace(pointerSentence.ReplaceAllString(text, ""))
	text = mdLink.ReplaceAllString(text, "$1")
	const limit = 500
	if len(text) > limit {
		cut := strings.LastIndex(text[:limit], ". ")
		if cut < limit/2 {
			cut = limit
			for cut > 0 && text[cut]&0xC0 == 0x80 {
				cut--
			}
			return text[:cut] + " ..."
		}
		return text[:cut+1] + " ..."
	}
	return text
}

// best is the row that scores highest for term. For a keyword, among
// table rows that lead with it, one that states one construct beats a
// list of several (`struct Name { f: T }` over "`struct`, `enum`, `error`").
func best(rows []mdRow, term string) (mdRow, int) {
	var pick mdRow
	top := 0
	for _, r := range rows {
		s := score(r, term)
		if s > top || s == top && s == 8 && keywords[term] && leadsBetter(r, pick) {
			pick, top = r, s
		}
	}
	return pick, top
}

// leadsBetter reports whether table row a's first cell names fewer
// constructs than table row b's.
func leadsBetter(a, b mdRow) bool {
	if len(a.cells) < 2 || len(b.cells) < 2 {
		return false
	}
	return len(codeSpan.FindAllString(a.cells[0], -1)) < len(codeSpan.FindAllString(b.cells[0], -1))
}

// pinnedRows names, for a term the scores place on the wrong row, the
// opening of the reference row that states its rule. Scoring alone picks a
// row that only mentions the term: a C type mapping cell for i32, a
// "habits from other languages" row for go and none.
var pinnedRows = map[string]string{
	"!":     "Logic. `!` also writes a result type",
	"else":  "`e else { ... }` handles a failure",
	"i32":   "`i8 i16 i32 i64 u8 u16 u32 u64`",
	"i64":   "`i8 i16 i32 i64 u8 u16 u32 u64`",
	"uint":  "`int uint`",
	"f64":   "`f32 f64`",
	"mut":   "`&T`, `mut T`",
	"Scope": "`scope s := io.Scope() { ... }`",
	"go":    "`s.go F(args)`",
	"none":  "`some(x)`, `none`",
}

// pinned is the first row whose first cell opens with text. A reference
// edit that removes the row fails the generator.
func pinned(t *testing.T, rows []mdRow, term, text string) mdRow {
	for _, r := range rows {
		if strings.HasPrefix(r.cells[0], text) {
			return r
		}
	}
	t.Fatalf("no reference row for %q opens with %q: update pinnedRows", term, text)
	return mdRow{}
}

// loanTerms also get the memory model's row.
var loanTerms = map[string]bool{"from": true, "into": true, "ends": true, "view": true, "plain": true, "linear": true, "mut": true, "&": true}

func generateKeywordTable(t *testing.T) []byte {
	ref := readRows(t, "../../doc/reference.md", "reference")
	mm := readRows(t, "../../doc/memory.md", "memory model")
	var b bytes.Buffer
	b.WriteString("// Code generated by TestKeywordTable from doc/reference.md and\n")
	b.WriteString("// doc/memory.md; DO NOT EDIT. Regenerate with\n")
	b.WriteString("// go test ./internal/lsp -run TestKeywordTable -update\n\npackage lsp\n\n")
	b.WriteString("var keywordDocs = map[string][]keywordDoc{\n")
	for _, term := range hoverTerms() {
		var docs []keywordDoc
		rows := ref
		if asmWords[term] {
			// An asm word's rule is the asm section's (`out` is not the "Reading out" rule).
			rows = nil
			for _, r := range ref {
				if strings.HasSuffix(r.section, ": Unsafe and C") {
					rows = append(rows, r)
				}
			}
		}
		r, s := best(rows, term)
		if text, ok := pinnedRows[term]; ok {
			r, s = pinned(t, rows, term, text), 8
		}
		m, ms := best(mm, term)
		if !loanTerms[term] {
			ms = 0
		}
		// A passing mention in the reference gives way to the memory
		// model's own row.
		if s > 1 || s == 1 && ms == 0 {
			docs = append(docs, keywordDoc{r.section, rowText(r)})
		}
		if ms > 0 {
			docs = append(docs, keywordDoc{m.section, rowText(m)})
		}
		if len(docs) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\t%q: {\n", term)
		for _, d := range docs {
			fmt.Fprintf(&b, "\t\t{Section: %q, Text: %q},\n", d.Section, d.Text)
		}
		b.WriteString("\t},\n")
	}
	b.WriteString("}\n")
	out, err := format.Source(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestKeywordTable checks, or with -update writes, both tables generated
// from doc/ and std/: the rule hovers (keywords_table.go) and the std surface
// (stdsurface_table.go and its parts, stdsurface_gen_test.go). A part file
// the generator no longer writes is stale too, and -update removes it.
func TestKeywordTable(t *testing.T) {
	files := generateSurfaceTable(t)
	files["keywords_table.go"] = generateKeywordTable(t)
	old, _ := filepath.Glob("stdsurface_table*.go")
	for _, name := range old {
		if _, ok := files[name]; ok {
			continue
		}
		if *update {
			must(t, os.Remove(name))
		} else {
			t.Errorf("%s is not generated any more: go test ./internal/lsp -run TestKeywordTable -update", name)
		}
	}
	for name, want := range files {
		if *update {
			must(t, os.WriteFile(name, want, 0o644))
			continue
		}
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
			t.Errorf("%s is stale: go test ./internal/lsp -run TestKeywordTable -update", name)
		}
	}
}

// TestKeywordTableCoversTheConstructList: every construct of a fixed list
// of the words and operators a reader meets first has a hover.
func TestKeywordTableCoversTheConstructList(t *testing.T) {
	var missing []string
	for _, w := range strings.Fields("from into ends view linear unsafe asm gen quote scope try else fail onerr defer const requires ensures cpu section + * & == && .. ?") {
		if len(keywordDocs[w]) == 0 {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		t.Errorf("no rule hover for %v", missing)
	}
}

// TestKeywordTablePinnedRows: each pinned term's first hover is the row
// pinnedRows names, not a row that only mentions the term.
func TestKeywordTablePinnedRows(t *testing.T) {
	for term, text := range pinnedRows {
		docs := keywordDocs[term]
		if len(docs) == 0 || !strings.HasPrefix(docs[0].Text, text) {
			got := ""
			if len(docs) > 0 {
				got = docs[0].Text
			}
			t.Errorf("%s: first hover %q, want it to open with %q", term, got, text)
		}
	}
}

// TestUniverseDocsQuoteTheReference: every hand-written universe rule is
// the reference's own words: each part between ": " and " | " (the
// generated rows' joints) is in the reference as it is.
func TestUniverseDocsQuoteTheReference(t *testing.T) {
	raw, err := os.ReadFile("../../doc/reference.md")
	must(t, err)
	ref := string(raw)
	docs := []keywordDoc{universeLine}
	for _, d := range universeDocs {
		docs = append(docs, d)
	}
	for _, d := range docs {
		for _, cell := range strings.Split(d.Text, " | ") {
			for _, part := range strings.Split(cell, ": ") {
				if !strings.Contains(ref, strings.TrimSpace(part)) {
					t.Errorf("%q (%s) is not in doc/reference.md", part, d.Section)
				}
			}
		}
	}
}
