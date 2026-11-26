// Why: a system view (c.windows, c.d3d12, ...) is a binding of a platform
// API that mk ships, so a build that imports one needs no SDK, no Clang and
// no header; megabytes of Mk must not be parsed per build to use a handful
// of its items.
//
// What: the data of one view as the generator hands it over and the
// archive returns it: Meta (name, target, required views, inputs and
// counts), Item (one block of the binding with its kind, C name, the names
// it defines, its dependencies and its link entry), Ref, Link, and Fold,
// the name folding a wanted name is matched with.
//
// How: an item is the importer's pruning unit made durable (internal/cimport
// prune.go's block): a run of lines that defines one or more names, with the
// items its text needs resolved to indexes at generation time, so extraction
// is a walk over indexes, never a scan of text.

package sysview

import "strings"

// FormatVersion is the archive format this mk writes and the newest it
// reads.
const FormatVersion = 1

// Meta describes one view. Its JSON encoding (fields in this order, maps
// sorted by key) is part of the archive's identity.
type Meta struct {
	Name     string   `json:"name"`     // the import name: c.<Name>
	Target   string   `json:"target"`   // the target the view was generated for
	Requires []string `json:"requires"` // the views its items refer to, in table order
	Headers  []string `json:"headers"`
	Defines  []string `json:"defines"`
	// Inputs are the pinned inputs: the SDK version or header repository
	// commit, the importer's format, the toolset headers' hash, Clang's
	// version. Never a path of the generating machine.
	Inputs map[string]string `json:"inputs"`
	// Counts are the importer's counts for the view: declarations, bound,
	// items, and the items dropped at generation by reason.
	Counts map[string]int `json:"counts"`
}

// Ref names an item: View 0 is the item's own view, View k the view
// Requires[k-1]; Item is the item's index there.
type Ref struct{ View, Item int }

// Link is a function item's link entry: the library that exports it and
// whether it binds lazily. The zero Link is none.
type Link struct {
	Lib  string
	Lazy bool
}

// Item is one block of a view.
type Item struct {
	Name string // the first name the block defines
	Kind string // func, var, const, type, struct or method
	// C is the block's first "// C: " comment, the C declaration it binds:
	// part of Text (CText), so not stored apart.
	C    string
	Defs []string
	Deps []Ref
	Text string // the block's lines, joined by "\n", with no final newline
	// Raw reports that the text refers to std.raw (accessors).
	Raw  bool
	Link Link
}

// CText is the text of the first "// C: " line of an item's text.
func CText(text string) string {
	for len(text) > 0 {
		line, rest, _ := strings.Cut(text, "\n")
		if c, ok := strings.CutPrefix(line, "// C: "); ok {
			return c
		}
		text = rest
	}
	return ""
}

// Fold is the spelling a wanted name is matched by: underscores dropped,
// ASCII letters lowered. It equals internal/cimport's fold, which prune
// matches wanted names with.
func Fold(name string) string {
	b := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b = append(b, c)
	}
	return string(b)
}
