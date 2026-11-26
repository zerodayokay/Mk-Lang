// Why: the std surface table must follow the reference as it changes.
// What: generateSurfaceTable and its readers (surfaceKey, surfaceEntry).
// How: reads each signature line of the library chapter and the prose sentences
// that name it; splits the table into part files to stay under the file size
// limit. TestKeywordTable writes it:
// go test ./internal/lsp -run TestKeywordTable -update

package lsp

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var surfaceTypeHeads = [...]string{"struct ", "mut view struct ", "enum ", "error ", "interface ", "type "}

// surfaceKey reads the item a library line declares. It returns the key
// (generic arguments dropped) and whether the line declares an item.
func surfaceKey(l string) (string, bool) {
	if l == "" || l[0] == ' ' || l[0] == '\t' || l[0] == '}' || strings.HasPrefix(l, "//") {
		return "", false
	}
	for _, w := range surfaceTypeHeads {
		if rest, ok := strings.CutPrefix(l, w); ok {
			n := identPrefix(rest)
			return n, n != ""
		}
	}
	var segs []string
	i := 0
	for {
		n := identPrefix(l[i:])
		if n == "" {
			return "", false
		}
		segs = append(segs, n)
		i += len(n)
		if i < len(l) && l[i] == '[' {
			i = skipBrackets(l, i)
		}
		if i < len(l) && l[i] == '.' {
			i++
			continue
		}
		break
	}
	if i >= len(l) || l[i] != '(' {
		return "", false
	}
	return strings.Join(segs, "."), true
}

func identPrefix(s string) string {
	n := 0
	for n < len(s) && (isIdentByte(s[n]) && (n > 0 || isIdentStart(s[n]))) {
		n++
	}
	return s[:n]
}

// skipBrackets is the index after the bracket closing the one at s[i].
func skipBrackets(s string, i int) int {
	d := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '[':
			d++
		case ']':
			d--
			if d == 0 {
				return i + 1
			}
		}
	}
	return i
}

var (
	boldLead   = regexp.MustCompile(`^\s*\*\*[^*]*\*\*\s*`)
	moduleHead = regexp.MustCompile(`^// (std(?:\.[a-z][a-z0-9]*)+)`)
	moduleLead = regexp.MustCompile("^\\*\\*`(std(?:\\.[a-z][a-z0-9]*)+)`\\*\\*")
	sentence   = regexp.MustCompile("(?:[^.!?`]|`[^`]*`|[.!?][^\\s])*[.!?](?:\\s|$)")
)

// surfaceEntry is one item of the library chapter as the generator reads it.
type surfaceEntry struct {
	key, module string
	lines       []string
	notes       []string
}

func generateSurfaceTable(t *testing.T) map[string][]byte {
	raw, err := os.ReadFile("../../doc/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	start, end := -1, len(all)
	for i, l := range all {
		if heading.MatchString(l) && strings.HasSuffix(l, ". Library surface") {
			start = i
		} else if start >= 0 && strings.HasPrefix(l, "## ") {
			end = i
			break
		}
	}
	if start < 0 {
		t.Fatal("doc/reference.md has no Library surface chapter")
	}
	sec := heading.FindStringSubmatch(all[start])[2]
	entries := map[string]*surfaceEntry{}
	var order []string
	add := func(key, module, line string) {
		e := entries[key]
		if e == nil {
			e = &surfaceEntry{key: key, module: module}
			entries[key] = e
			order = append(order, key)
		}
		if len(e.lines) < 4 {
			e.lines = append(e.lines, line)
		}
	}
	var prose []string
	// The surface is the section's first block that is not a corpus
	// example: its verdict is parse (or it has none). Blocks with an ok or
	// reject verdict, before it or after it, are examples (design7 put an
	// ok block first).
	fence, opened, surface, done, module := false, false, false, false, ""
	for _, l := range all[start+1 : end] {
		if strings.HasPrefix(l, "```") {
			if fence && surface {
				done = true
			}
			fence = !fence
			opened, surface = fence, false
			continue
		}
		if !fence {
			if strings.TrimSpace(l) != "" {
				prose = append(prose, l)
			}
			continue
		}
		if opened {
			opened = false
			v := strings.TrimSpace(l)
			surface = !done && !strings.HasPrefix(v, "// verdict: ok") && !strings.HasPrefix(v, "// verdict: reject")
		}
		if !surface {
			continue // a corpus example
		}
		if m := moduleHead.FindStringSubmatch(l); m != nil {
			module = m[1]
			continue
		}
		key, ok := surfaceKey(l)
		if !ok {
			continue
		}
		line := strings.TrimSuffix(strings.TrimSpace(l), " {")
		bind := ""
		if module != "" {
			bind = module[strings.LastIndexByte(module, '.')+1:]
		}
		add(key, module, line)
		// An item of a module named without its binding (`File.Read`
		// under `// std.fs`) is also found as `fs.File.Read`.
		if bind != "" && !strings.HasPrefix(key, bind+".") {
			add(bind+"."+key, module, line)
		}
	}
	// The prose's sentences that speak of an item: a code span spelling
	// its key, or its last segment where its type is spelled too. A
	// paragraph that opens with a module in bold (**`std.raw`**) speaks
	// of that module's items only.
	type proseSentence struct{ text, module string }
	var sentences []proseSentence
	for _, p := range prose {
		mod := ""
		if m := moduleLead.FindStringSubmatch(p); m != nil {
			mod = m[1]
		}
		for _, s := range sentence.FindAllString(p, -1) {
			if s = strings.TrimSpace(boldLead.ReplaceAllString(s, "")); s != "" {
				sentences = append(sentences, proseSentence{s, mod})
			}
		}
	}
	for _, key := range order {
		e := entries[key]
		segs := strings.Split(key, ".")
		last := segs[len(segs)-1]
		owner := ""
		if len(segs) > 1 {
			owner = segs[len(segs)-2]
		}
		// A type, and the owner of a method, is named by a span without a
		// call: `Slice(p, n)` is std.raw's function, not the type Slice.
		isType := false
		for _, w := range surfaceTypeHeads {
			isType = isType || strings.HasPrefix(e.lines[0], w)
		}
		for _, ps := range sentences {
			if ps.module != "" && ps.module != e.module {
				continue
			}
			s := ps.text
			spans, names := map[string]bool{}, map[string]bool{}
			for _, m := range codeSpan.FindAllStringSubmatch(s, -1) {
				k := trimSpan(m[1])
				spans[k] = true
				if !strings.Contains(m[1], "(") {
					names[k] = true
				}
			}
			self := spans[key]
			if isType {
				self = names[key]
			}
			if self || owner != "" && spans[last] && names[owner] || owner != "" && spans[owner+"."+last] {
				if len(e.notes) < 3 {
					e.notes = append(e.notes, s)
				}
			}
		}
	}
	sort.Strings(order)
	// The table is split into files of surfaceChunk entries, one line each,
	// so each stays under archtest's MaxFileLines (1,200): the library chapter has
	// about 1,800 items.
	const head = "// Code generated by TestKeywordTable from the Library surface chapter\n" +
		"// of doc/reference.md; DO NOT EDIT. Regenerate with go test ./internal/lsp -run TestKeywordTable -update\n\npackage lsp\n\n"
	gofmt := func(b []byte) []byte {
		out, err := format.Source(b)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	files := map[string][]byte{}
	var parts []string
	for i := 0; i < len(order); i += surfaceChunk {
		name := fmt.Sprintf("surfaceDocs%d", len(parts)+1)
		parts = append(parts, name)
		var b bytes.Buffer
		b.WriteString(head)
		fmt.Fprintf(&b, "var %s = map[string]surfaceDoc{\n", name)
		for _, key := range order[i:min(i+surfaceChunk, len(order))] {
			e := entries[key]
			fmt.Fprintf(&b, "\t%q: {Module: %q, Lines: %#v", key, e.module, e.lines)
			if len(e.notes) > 0 {
				fmt.Fprintf(&b, ", Notes: %#v", e.notes)
			}
			b.WriteString("},\n")
		}
		b.WriteString("}\n")
		files[fmt.Sprintf("stdsurface_table%d.go", len(parts))] = gofmt(b.Bytes())
	}
	var b bytes.Buffer
	b.WriteString(head)
	fmt.Fprintf(&b, "const surfaceSection = %q\n\n", "reference: "+sec)
	fmt.Fprintf(&b, "// surfaceDocs is the table, %d items in %d files.\n", len(order), len(parts))
	fmt.Fprintf(&b, "var surfaceDocs = joinSurfaceDocs(%s)\n\n", strings.Join(parts, ", "))
	b.WriteString("func joinSurfaceDocs(parts ...map[string]surfaceDoc) map[string]surfaceDoc {\n" +
		"\tout := map[string]surfaceDoc{}\n\tfor _, p := range parts {\n\t\tfor k, v := range p {\n\t\t\tout[k] = v\n\t\t}\n\t}\n\treturn out\n}\n")
	files["stdsurface_table.go"] = gofmt(b.Bytes())
	return files
}

// surfaceChunk is the number of library items per generated file.
const surfaceChunk = 900

// trimSpan reduces a code span to the name it spells: `Dir.Next(a, io)`
// is Dir.Next, `List[T]` is List.
func trimSpan(s string) string {
	if k, ok := surfaceKey(s + "("); ok && !strings.ContainsAny(s, " ") {
		return k
	}
	if k, ok := surfaceKey(s); ok {
		return k
	}
	return s
}

func TestSurfaceKey(t *testing.T) {
	for in, want := range map[string]string{
		"str.Len(self): int":                     "str.Len",
		"Slice[T].Get(&self, i: int): ?&T":       "Slice.Get",
		"fs.ReadText(a: &Arena, io: Io, p: str)": "fs.ReadText",
		"Print[A: Fields[Format] + Plain](w: mut Writer, const f: str, args: A): WriteError!void": "Print",
		"struct Cell[T: Plain] {":  "Cell",
		"mut view struct Fixed {":  "Fixed",
		"type Error = NotFound | ": "Error",
		"    export args: []str":   "",
		"// std.fs":                "",
	} {
		got, ok := surfaceKey(in)
		if got != want || ok != (want != "") {
			t.Errorf("surfaceKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}
