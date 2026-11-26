// Why: the harness must read the library surface of doc/reference.md (the
// "Library surface" chapter's block, one declaration a line) exactly as std's
// own API test does, or coverage would judge different lines than std ships.
//
// What: Item (one library line, its module and its ID), parseReferenceAPI,
// referenceAPIBlock, preludeNames, moduleTable and the helpers modShort,
// itemHead and blockName.
//
// How: a copy of std/std-mem_api_test.go's parseReferenceAPI and its
// helpers, working on the text of doc/reference.md instead of a testing.T.
// tools/dogfood may import nothing of the module but tools/machinelock
// (internal/archtest checks), and the original lives in a std _test.go
// file, so the two are kept equal by hand. The prelude names come from the
// module map's "Prelude, no import needed" line, as
// TestPreludeNamesMatchReference checks std's PreludeNames.

package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Item is one declaration of the library surface's block (a library line),
// as TestAPIMatchesReference's apiItem: a one-line declaration or a block with its body.
type Item struct {
	Mod    string   // routed module: "std.core", "std.fs", ...
	Text   string   // the line as written in the reference
	Body   []string // block body lines, trimmed
	Block  bool
	InSect string // the "// std.x" section the line sits in, "" before the first
}

// ID names the line for COVERAGE.md, EXEMPT.md and a dogfood's README:
// its section ("prelude" before the first section comment) and its text.
// The same text in two sections (error Os) is two lines.
func (it *Item) ID() string { return sectName(it.InSect) + " | " + it.Text }

// sectName is the section as COVERAGE.md and EXEMPT.md head it.
func sectName(s string) string {
	if s == "" {
		return "prelude"
	}
	return s
}

// Executable reports whether the line declares a function, which a case
// can execute: not a block, an error, a type alias or a const.
func (it *Item) Executable() bool {
	if it.Block {
		return false
	}
	for _, kw := range []string{"error ", "type ", "const "} {
		if strings.HasPrefix(it.Text, kw) {
			return false
		}
	}
	return true
}

// referenceSection returns the text of ref from the heading that starts
// with head up to the next "## " heading.
func referenceSection(ref, head string) (string, error) {
	i := strings.Index(ref, "\n"+head)
	if i < 0 {
		return "", fmt.Errorf("doc/reference.md has no heading %q", head)
	}
	rest := ref[i+1:]
	if j := strings.Index(rest[len(head):], "\n## "); j >= 0 {
		rest = rest[:len(head)+j+1]
	}
	return rest, nil
}

// referenceAPIBlock returns the lines of the library surface's verdict-parse
// block.
func referenceAPIBlock(ref string) ([]string, error) {
	sec, err := referenceSection(ref, "## 18. Library surface")
	if err != nil {
		return nil, err
	}
	const open = "```mk\n// verdict: parse\n"
	i := strings.Index(sec, open)
	if i < 0 {
		return nil, errors.New("reference section 18 has no verdict-parse block")
	}
	rest := sec[i+len(open):]
	j := strings.Index(rest, "\n```")
	if j < 0 {
		return nil, errors.New("reference section 18's block is not closed")
	}
	return strings.Split(rest[:j], "\n"), nil
}

// preludeNames is the module map's prelude list.
func preludeNames(ref string) ([]string, error) {
	sec, err := referenceSection(ref, "## 17. Module map")
	if err != nil {
		return nil, err
	}
	m := regexp.MustCompile("Prelude, no import needed: `([^`]*)`").FindStringSubmatch(sec)
	if m == nil {
		return nil, errors.New("reference section 17 has no prelude list")
	}
	return strings.Fields(m[1]), nil
}

// moduleTable is the module map's import table: module -> names.
func moduleTable(ref string) (map[string][]string, error) {
	sec, err := referenceSection(ref, "## 17. Module map")
	if err != nil {
		return nil, err
	}
	row := regexp.MustCompile("(?m)^\\| `(std\\.[a-z][a-z0-9.]*)`(?:, `(std\\.[a-z][a-z0-9.]*)`)? \\| ([^|]*)\\|")
	tab := map[string][]string{}
	name := regexp.MustCompile("`([^`]*)`")
	for _, m := range row.FindAllStringSubmatch(sec, -1) {
		var names []string
		for _, n := range name.FindAllStringSubmatch(m[3], -1) {
			names = append(names, strings.Fields(n[1])...)
		}
		for _, mod := range []string{m[1], m[2]} {
			if mod == "" {
				continue
			}
			short := modShort(mod) + "."
			for _, n := range names {
				if k := strings.IndexByte(n, '.'); k < 0 {
					tab[mod] = append(tab[mod], n)
				} else if strings.HasPrefix(n, short) {
					tab[mod] = append(tab[mod], n[len(short):])
				}
			}
		}
	}
	return tab, nil
}

// A component of a module's name may hold digits after its first letter
// (std.x509).
var sectionComment = regexp.MustCompile(`^// (std\.[a-z][a-z0-9]*(?:\.[a-z][a-z0-9]*)*)\b`)

// modShort is the name a module's lines qualify with: its last component.
func modShort(mod string) string {
	return mod[strings.LastIndex(mod, ".")+1:]
}

// itemHead is the declared name of a one-line item: "Box[T].New" gives
// receiver "Box", name "New"; "fs.Rename" gives receiver "fs";
// "Print[A...]" gives receiver "", name "Print"; "error Os(..)" gives "Os".
func itemHead(text string) (recv, name string) {
	s := text
	for _, kw := range []string{"error ", "type ", "const "} {
		if strings.HasPrefix(s, kw) {
			s = s[len(kw):]
			end := strings.IndexAny(s, "( =:")
			if end < 0 {
				end = len(s)
			}
			return "", s[:end]
		}
	}
	if k := strings.IndexByte(s, '('); k >= 0 {
		s = s[:k]
	}
	var b strings.Builder
	depth := 0
	for _, c := range s {
		switch {
		case c == '[':
			depth++
		case c == ']':
			depth--
		case depth == 0:
			b.WriteRune(c)
		}
	}
	s = b.String()
	if k := strings.LastIndexByte(s, '.'); k >= 0 {
		return s[:k], s[k+1:]
	}
	return "", s
}

// blockName is the type a block header declares: "struct Cell[T: Plain] {"
// gives "Cell".
func blockName(header string) string {
	f := strings.Fields(header)
	if len(f) < 2 {
		return ""
	}
	k := 0
	for k < len(f)-1 && (f[k] == "linear" || f[k] == "mut" || f[k] == "view" || f[k] == "packed" || f[k] == "export") {
		k++
	}
	if k+1 >= len(f) {
		return ""
	}
	n := f[k+1]
	if k := strings.IndexAny(n, "[:{"); k >= 0 {
		n = n[:k]
	}
	return n
}

// parseReferenceAPI splits the library surface's block of ref (the text
// of doc/reference.md) into items and routes each as
// TestAPIMatchesReference does.
func parseReferenceAPI(ref string) ([]Item, error) {
	ref = strings.ReplaceAll(ref, "\r\n", "\n")
	lines, err := referenceAPIBlock(ref)
	if err != nil {
		return nil, err
	}
	tab, err := moduleTable(ref)
	if err != nil {
		return nil, err
	}
	names, err := preludeNames(ref)
	if err != nil {
		return nil, err
	}
	prelude := map[string]bool{}
	for _, n := range names {
		prelude[n] = true
	}
	// The first module in sorted order that lists a name, so the choice
	// is the same on every run (not a map's order).
	listed := map[string]string{}
	mods := make([]string, 0, len(tab))
	for mod := range tab {
		mods = append(mods, mod)
	}
	sort.Strings(mods)
	for _, mod := range mods {
		for _, n := range tab[mod] {
			if _, ok := listed[n]; !ok {
				listed[n] = mod
			}
		}
	}
	var items []Item
	sect := ""
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.TrimSpace(l) == "":
			continue
		case strings.HasPrefix(l, "//"):
			if m := sectionComment.FindStringSubmatch(l); m != nil {
				sect = m[1]
			}
			continue
		}
		it := Item{Text: l, InSect: sect}
		if strings.HasSuffix(l, " {") {
			it.Block = true
			for i++; i < len(lines) && lines[i] != "}"; i++ {
				if s := strings.TrimSpace(lines[i]); s != "" {
					it.Body = append(it.Body, s)
				}
			}
		}
		items = append(items, it)
	}
	declared := map[string]bool{}
	for _, it := range items {
		if it.InSect == "" {
			continue
		}
		if it.Block {
			if n := blockName(it.Text); !prelude[n] {
				declared[it.InSect+" "+n] = true
			}
		} else if strings.HasPrefix(it.Text, "error ") || strings.HasPrefix(it.Text, "type ") {
			_, n := itemHead(it.Text)
			declared[it.InSect+" "+n] = true
		}
	}
	for k := range items {
		it := &items[k]
		it.Mod = "std.core"
		if it.InSect == "" {
			continue
		}
		short := modShort(it.InSect)
		var name string
		if it.Block {
			name = blockName(it.Text)
			if prelude[name] {
				continue
			}
		} else if strings.HasPrefix(it.Text, "error ") || strings.HasPrefix(it.Text, "type ") {
			it.Mod = it.InSect
			continue
		} else {
			recv, n := itemHead(it.Text)
			switch {
			case recv == short:
				it.Mod = it.InSect
				continue
			case recv != "":
				name = recv
			default:
				name = n
			}
		}
		switch {
		case declared[it.InSect+" "+name]:
			it.Mod = it.InSect
		case slices.Contains(tab[it.InSect], name) && !prelude[name]:
			// A name the module map lists under this section's own module
			// (std.http's Client, std.tls's Conn) is that module's, as
			// std's parseReferenceAPI routes it.
			it.Mod = it.InSect
		case listed[name] != "" && !prelude[name]:
			it.Mod = listed[name]
		case !prelude[name]:
			it.Mod = it.InSect
		}
	}
	return items, nil
}
