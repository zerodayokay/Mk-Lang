// Why: a package's users need its public surface on one readable page, and
// std's module pages embed the same text, so mk doc renders the API from
// the checked declarations rather than from hand-kept lists.
//
// What: Session.Doc builds a DocPackage (modules, then their exported items
// with declaration, loans, errors, fields or variants and /// text);
// WriteMarkdown, WriteEmbed and WriteHTML render it. DocBlocksSource,
// DocStdFiles and DocTestPrelude feed mk doc --check, which compiles the doc text's fenced
// mk blocks in cmd/mk (the driver cannot import internal/corpus).
//
// How: the items come from the query index mk query answers from, filtered
// to exported names and sorted by name. Spelling is the source's wherever
// the source has it (doc_spell.go), except a constant's or a var's
// initializer that names a private item or field, which no program outside
// the module could write, and is left off. An item whose declaration needs
// a type of a std-only module (std.rt, std.sys, std.task, std.target,
// std.simd.kernel) is left out, as no program outside std can use it, and
// those modules are never documented; std.raw, which a package with unsafe
// modules may import, is. Only std modules carry a stability line.

package driver

import (
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mk/internal/types"
	"mk/std"
)

// DocOptions selects what mk doc documents.
type DocOptions struct {
	// Std, when set, documents std's public modules whose path is Std or
	// starts with Std + "." ("std" for all, the prelude's names in
	// std.core included) instead of the package; the session's root is
	// then only a place to check from (cmd/mk gives it an empty file). A
	// std-only module is documented only when Std names it and the session
	// browses std (DocStdBrowse).
	Std string
}

// DocStdBrowse reports whether mk doc --std mod needs a session with
// Config.BrowseStd: mod is std.raw, the one std-only module a package (one
// with unsafe modules) may import, whose items a normal session does not
// index. The other std-only modules are never documented.
func DocStdBrowse(mod string) bool {
	return mod == "std.raw"
}

// DocPackage is the documented surface, modules in path order.
type DocPackage struct {
	Title   string      `json:"title"`
	Modules []DocModule `json:"modules"`
}

// DocModule is one module's exported items, sorted by name.
type DocModule struct {
	Path  string    `json:"path"`
	Class string    `json:"class"`
	Why   string    `json:"why,omitempty"` // a non-Stable module's reason
	Std   bool      `json:"std,omitempty"` // a std module, which alone has a stability class
	Items []DocItem `json:"items"`
}

// DocItem is one exported item.
type DocItem struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Decl is the declaration as the source writes it: a function's head
	// with its clauses, a type's head, a constant with its initializer.
	Decl []string `json:"decl"`
	// Flows are the loans of a function's result and parameters, as
	// sentences.
	Flows []string `json:"flows,omitempty"`
	// ErrorSet is a function's error set as its head writes it
	// (Error|OutOfMemory); Errors its members, each with its payload.
	ErrorSet string   `json:"errorSet,omitempty"`
	Errors   []string `json:"errors,omitempty"`
	// Members are a struct's exported fields or an enum's variants, in
	// declaration order; MembersKind is "Fields" or "Variants".
	Members     []DocMember `json:"members,omitempty"`
	MembersKind string      `json:"membersKind,omitempty"`
	// Doc is the /// text, the markers taken off; Deprecated the text after
	// "Deprecated:" when a line carries the mark.
	Doc        []string `json:"doc,omitempty"`
	Deprecated string   `json:"deprecated,omitempty"`
}

// DocMember is one field or variant: its declaration without export, and
// its /// text.
type DocMember struct {
	Decl string   `json:"decl"`
	Doc  []string `json:"doc,omitempty"`
}

// Doc checks the package and returns its documented surface; nil when the
// check did not reach the types phase (the Result says why).
func (s *Session) Doc(opt DocOptions) (*DocPackage, Result, error) {
	if opt.Std != "" {
		s.fullStd = true
	}
	res := s.Check()
	if s.w == nil || s.w.sem5 == nil || s.stdB == nil || !checkedToTypes(res) {
		return nil, res, nil
	}
	qx := newQueryIndex(s)
	hider := newDocHider(qx)
	names := newDocNames(qx)
	title := filepath.Base(s.root.dir)
	if opt.Std != "" {
		title = opt.Std
	}
	dp := &DocPackage{Title: title}
	byMod := map[string]*DocModule{}
	for _, e := range qx.entries {
		it := qx.w.items.Get(e.id)
		if !it.Exported || e.mod == testRunnerModule || e.module.sysview {
			continue
		}
		if opt.Std == "" && e.std {
			continue
		}
		if opt.Std != "" && (!e.std || (isStdInternal(e.mod) && e.mod != preludeModule && !(e.mod == opt.Std && DocStdBrowse(e.mod))) || !(opt.Std == "std" || e.mod == opt.Std || strings.HasPrefix(e.mod, opt.Std+"."))) {
			continue
		}
		item, ok := docItem(qx, hider, names, e, it)
		if !ok {
			continue
		}
		m := byMod[e.mod]
		if m == nil {
			c, why := moduleClass(e.mod)
			m = &DocModule{Path: e.mod, Class: c, Why: why, Std: e.std}
			byMod[e.mod] = m
		}
		m.Items = append(m.Items, item)
	}
	for _, m := range byMod {
		// Items by name: Type before Type.Method, then the methods.
		slices.SortStableFunc(m.Items, func(a, b DocItem) int { return strings.Compare(a.Name, b.Name) })
		dp.Modules = append(dp.Modules, *m)
	}
	slices.SortFunc(dp.Modules, func(a, b DocModule) int { return strings.Compare(a.Path, b.Path) })
	return dp, res, nil
}

// docItem documents one exported entry; false when the item is left out
// because its declaration needs a type no program outside std can name.
func docItem(qx *queryIndex, hider *docHider, names *docNames, e qEntry, it *types.Item) (DocItem, bool) {
	item := DocItem{Name: e.name, Kind: it.Kind.String()}
	if sig := qx.funcSig(e.id); sig != nil {
		if hider.sig(sig) {
			return item, false
		}
		// The signature as written, so a named error set or an alias
		// keeps its name; the checker's spelling when the source has none
		// (a re-export) or is not a plain head.
		if written := docWrittenHead(qx, it); written != nil {
			item.Decl = written
		} else {
			item.Decl = append(item.Decl, qx.sigLine(e.name, sig))
			for _, c := range sig.Contracts {
				w := "requires "
				if c.Kind == types.Ensures {
					w = "ensures "
				}
				item.Decl = append(item.Decl, "    "+w+c.Text)
			}
		}
		item.Flows = docFlows(qx, sig)
		if len(qx.w.tab.Members(sig.Errors)) > 0 {
			item.Errors = docErrorMembers(qx, sig, e.mod)
			item.ErrorSet = docWrittenErrorSet(item.Decl, e.name)
			if item.ErrorSet == "" {
				item.ErrorSet = docBareSet(item.Errors)
			}
			if ordered, ok := docSetOrder(item.ErrorSet, item.Errors); ok {
				item.Errors = ordered
			}
		}
	} else {
		head, _, _ := strings.Cut(qx.declHead(e, true), "  (module ")
		item.Decl = []string{head}
		switch d := it.Decl.(type) {
		case *types.StructDecl, *types.EnumDecl:
			docMembers(qx, hider, &item, it.Decl)
		case *types.InterfaceDecl:
			if w := docWritten(qx, it.Span, "interface "+e.name, 80); w != nil && docNameEnds(w[0], "interface "+e.name) {
				item.Decl = hider.iface(d, w)
			}
		case *types.ErrorDecl:
			item.Decl = []string{"export error " + docErrorDecl(qx, e.id)}
		case *types.ConstDecl:
			if hider.typ(d.Type) {
				return item, false
			}
			if w := docWritten(qx, it.Span, "const "+e.name, 40); w != nil && docNameEnds(w[0], "const "+e.name) {
				item.Decl = names.initializer(e, w, qx.w.tab.TypeString(d.Type, nil))
			} else {
				// No source to show: the value when it is a scalar, never
				// a composite's layout bytes.
				item.Decl = []string{"export const " + e.name + ": " + qx.w.tab.TypeString(d.Type, nil)}
				if d.HasValue {
					if v := qx.w.tab.ConstString(d.Value); !strings.Contains(v, "{align ") {
						item.Decl[0] += " = " + v
					}
				}
			}
		case *types.GlobalDecl:
			if hider.typ(d.Type) {
				return item, false
			}
			if w := docWritten(qx, it.Span, "var "+e.name, 40); w != nil && docNameEnds(w[0], "var "+e.name) {
				item.Decl = names.initializer(e, w, qx.w.tab.TypeString(d.Type, nil))
			}
		case *types.AliasDecl:
			if t := qx.aliasTarget(e.id); t != types.NoItem && docAliasOfInternal(qx, e, t) {
				// An alias that makes a std-only struct or enum nameable
				// (the prelude's Arch is std.target's, std.text's Capped
				// is std.core's) is documented as that declaration under
				// the alias's name: the target's module is no name a
				// program can write.
				te := e
				te.id = t
				if h, _, _ := strings.Cut(qx.declHead(te, true), "  (module "); docMembers(qx, hider, &item, qx.w.items.Get(t).Decl) {
					item.Decl = []string{"export " + strings.TrimPrefix(h, "export ")}
					break
				}
			}
			if hider.typ(d.Type) {
				return item, false
			}
			for _, kw := range []string{"type ", "open type "} {
				if w := docWritten(qx, it.Span, kw+e.name, 40); w != nil && docNameEnds(w[0], kw+e.name) {
					item.Decl = w
					break
				}
			}
		}
	}
	if e.std {
		item.Decl = names.spell(e.mod, item.Decl)
		for i := range item.Members {
			item.Members[i].Decl = names.spell(e.mod, []string{item.Members[i].Decl})[0]
		}
	}
	for _, l := range qx.doc(it) {
		t := strings.TrimPrefix(strings.TrimPrefix(l, "///"), " ")
		if d, ok := strings.CutPrefix(t, "Deprecated:"); ok && item.Deprecated == "" {
			item.Deprecated = strings.TrimSpace(d)
		}
		item.Doc = append(item.Doc, t)
	}
	return item, true
}

// docAliasOfInternal reports whether alias entry e stands for t, a type of
// a std-only module other than e's own.
func docAliasOfInternal(qx *queryIndex, e qEntry, t types.ItemID) bool {
	p := qx.w.items.Module(qx.w.items.Get(t).Module).Path
	return e.std && p != e.mod && isStdInternal(p)
}

// docMembers fills item's fields (a struct's exported ones whose type a
// program can name) or variants from decl, and reports whether decl is a
// struct or an enum.
func docMembers(qx *queryIndex, hider *docHider, item *DocItem, decl any) bool {
	switch d := decl.(type) {
	case *types.StructDecl:
		item.MembersKind = "Fields"
		for _, f := range d.Fields {
			if f.Exported && !hider.typ(f.Type) {
				item.Members = append(item.Members, DocMember{Decl: docFieldLine(qx, f, d.Generics), Doc: docMemberDoc(qx, f.Span, qx.str(f.Name))})
			}
		}
	case *types.EnumDecl:
		item.MembersKind = "Variants"
		for _, v := range d.Variants {
			item.Members = append(item.Members, DocMember{Decl: docVariantLine(qx, v, d.Generics), Doc: docMemberDoc(qx, v.Span, qx.str(v.Name))})
		}
	default:
		return false
	}
	return true
}

// docNameEnds reports whether the written line, after export, is prefix
// followed by something that cannot continue a name: Append in `const
// Append: Options`, not in `const AppendAll`.
func docNameEnds(line, prefix string) bool {
	rest, ok := strings.CutPrefix(strings.TrimPrefix(line, "export "), prefix)
	return ok && (rest == "" || !isDocNameByte(rest[0]))
}

func isDocNameByte(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// docBareSet is a set's members without payloads, joined as an error set
// is written: A|B.
func docBareSet(members []string) string {
	var ns []string
	for _, m := range members {
		n, _, _ := strings.Cut(m, "(")
		ns = append(ns, n)
	}
	return strings.Join(ns, "|")
}

// docWrittenHead is a function item's head as its source writes it: the
// lines from the one holding the item's span to the one that opens the
// body (" {" taken off), at most 12, or the one line of a bodiless
// declaration; nil when the first line does not begin with the item's
// name (after export), so a re-export or a generated item falls back to
// the checker's spelling.
func docWrittenHead(qx *queryIndex, it *types.Item) []string {
	f := qx.w.sm.File(it.Span.File)
	if f == nil || int(it.Span.Start) > len(f.Text) {
		return nil
	}
	text := f.Text
	start := strings.LastIndexByte(text[:it.Span.Start], '\n') + 1
	lines := strings.Split(text[start:], "\n")
	name := qx.str(it.Name)
	first := docStripReceiverGenerics(strings.TrimPrefix(strings.TrimSpace(lines[0]), "export "))
	if i := strings.IndexAny(first, "(["); i < 0 || !strings.HasSuffix(first[:i], name) {
		return nil
	}
	var out []string
	for i, l := range lines {
		if i == 12 {
			return nil
		}
		// A line comment is no part of the head: `F() { // note` opens
		// the body.
		l = strings.TrimRight(codeOnly(l, true), " \t\r")
		if body, ok := strings.CutSuffix(l, "{"); ok {
			if b := strings.TrimRight(body, " "); b != "" {
				out = append(out, b)
			}
			return out
		}
		out = append(out, l)
		// A bodiless declaration: the next line is not an indented
		// continuation of this head.
		if i+1 < len(lines) && (lines[i+1] == "" || (lines[i+1][0] != ' ' && lines[i+1][0] != '\t')) {
			return out
		}
	}
	return out
}

// docStability is a std module's class as one sentence: "Stability:
// Stable.", with a non-Stable module's reason in parentheses. Empty for a
// package's own module, which declares no class.
func docStability(m DocModule) string {
	if !m.Std {
		return ""
	}
	if m.Why != "" {
		return "Stability: " + m.Class + " (" + strings.TrimSuffix(m.Why, ".") + ")."
	}
	return "Stability: " + m.Class + "."
}

// docErrorsLine is the Errors line's text: the set as written, then its
// members when the set as written does not already list them.
func docErrorsLine(it DocItem) string {
	members := strings.Join(it.Errors, ", ")
	if _, same := docSetOrder(it.ErrorSet, it.Errors); same {
		return members
	}
	return it.ErrorSet + " (" + members + ")"
}

// docSetOrder reports whether set, an error set as written (A|B), names
// exactly the members (each with its payload), and if so returns the
// members in the written order: `BadInput|OutOfMemory` gives
// [BadInput(offset: int), OutOfMemory] whatever order the checker keeps.
func docSetOrder(set string, members []string) ([]string, bool) {
	written := strings.Split(strings.ReplaceAll(set, " ", ""), "|")
	if len(written) != len(members) {
		return nil, false
	}
	byName := map[string]string{}
	for _, m := range members {
		n, _, _ := strings.Cut(m, "(")
		byName[n] = m
	}
	var out []string
	for _, n := range written {
		m, ok := byName[n]
		if !ok {
			return nil, false
		}
		delete(byName, n)
		out = append(out, m)
	}
	return out, true
}

// WriteMarkdown writes dp as one Markdown page: the title, then a section
// per module with its stability and a subsection per item.
func (dp *DocPackage) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", dp.Title)
	for _, m := range dp.Modules {
		fmt.Fprintf(&b, "\n## Module `%s`\n", m.Path)
		if st := docStability(m); st != "" {
			b.WriteString("\n" + st + "\n")
		}
		for _, it := range m.Items {
			writeItemMarkdown(&b, it, "###")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteEmbed writes dp for a page that already has its title and an API
// heading: no title, the first module's stability line and its items at
// ###, then each further module (a submodule the page also documents)
// under a ### heading of its own with its items at ####.
func (dp *DocPackage) WriteEmbed(w io.Writer) error {
	var b strings.Builder
	for i, m := range dp.Modules {
		level := "###"
		if i > 0 {
			fmt.Fprintf(&b, "\n### Module `%s`\n", m.Path)
			level = "####"
		}
		if st := docStability(m); st != "" {
			b.WriteString("\n" + st + "\n")
		}
		for _, it := range m.Items {
			writeItemMarkdown(&b, it, level)
		}
	}
	_, err := io.WriteString(w, strings.TrimPrefix(b.String(), "\n"))
	return err
}

// writeItemMarkdown writes one item under a heading of the given level:
// the deprecation mark, the declaration in an mk-less code block (the
// corpus never reads it), the Flows and Errors lines, the /// text, then
// the fields or variants with their text.
func writeItemMarkdown(b *strings.Builder, it DocItem, level string) {
	fmt.Fprintf(b, "\n%s `%s`\n\n", level, it.Name)
	if it.Deprecated != "" {
		fmt.Fprintf(b, "**Deprecated:** %s\n\n", it.Deprecated)
	}
	b.WriteString("```\n" + strings.Join(it.Decl, "\n") + "\n```\n")
	if len(it.Flows) > 0 || len(it.Errors) > 0 {
		b.WriteString("\n")
	}
	if len(it.Flows) > 0 {
		fmt.Fprintf(b, "- Flows: %s\n", strings.Join(it.Flows, " "))
	}
	if len(it.Errors) > 0 {
		fmt.Fprintf(b, "- Errors: %s\n", docErrorsLine(it))
	}
	if len(it.Doc) > 0 {
		b.WriteString("\n" + strings.Join(it.Doc, "\n") + "\n")
	}
	if len(it.Members) > 0 {
		fmt.Fprintf(b, "\n%s:\n\n", it.MembersKind)
		for _, m := range it.Members {
			fmt.Fprintf(b, "- `%s`", m.Decl)
			for i, l := range m.Doc {
				switch {
				case i == 0:
					b.WriteString(": " + l)
				case l == "":
					b.WriteString("\n")
				default:
					b.WriteString("\n  " + l)
				}
			}
			b.WriteString("\n")
		}
	}
}

// WriteHTML writes dp as one HTML page, every text escaped: the doc text
// as preformatted paragraphs, so a fenced block keeps its lines.
func (dp *DocPackage) WriteHTML(w io.Writer) error {
	e := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>%s</title>\n", e(dp.Title))
	b.WriteString("<style>body{font-family:sans-serif;max-width:60em;margin:auto;padding:0 1em}pre{background:#f4f4f4;padding:.5em;overflow-x:auto}.dep{color:#a00}.class{color:#555}</style>\n")
	fmt.Fprintf(&b, "</head><body>\n<h1>%s</h1>\n<ul>\n", e(dp.Title))
	for _, m := range dp.Modules {
		fmt.Fprintf(&b, "<li><a href=\"#%s\">%s</a></li>\n", e(m.Path), e(m.Path))
	}
	b.WriteString("</ul>\n")
	for _, m := range dp.Modules {
		fmt.Fprintf(&b, "<h2 id=\"%s\">Module %s</h2>\n", e(m.Path), e(m.Path))
		if st := docStability(m); st != "" {
			fmt.Fprintf(&b, "<p class=\"class\">%s</p>\n", e(st))
		}
		for _, it := range m.Items {
			fmt.Fprintf(&b, "<h3 id=\"%s\">%s</h3>\n", e(m.Path+"."+it.Name), e(it.Name))
			if it.Deprecated != "" {
				fmt.Fprintf(&b, "<p class=\"dep\"><b>Deprecated:</b> %s</p>\n", e(it.Deprecated))
			}
			fmt.Fprintf(&b, "<pre><code>%s</code></pre>\n", e(strings.Join(it.Decl, "\n")))
			if len(it.Flows) > 0 {
				fmt.Fprintf(&b, "<p>Flows: %s</p>\n", e(strings.Join(it.Flows, " ")))
			}
			if len(it.Errors) > 0 {
				fmt.Fprintf(&b, "<p>Errors: %s</p>\n", e(docErrorsLine(it)))
			}
			if len(it.Doc) > 0 {
				fmt.Fprintf(&b, "<pre class=\"doc\">%s</pre>\n", e(strings.Join(it.Doc, "\n")))
			}
			if len(it.Members) > 0 {
				fmt.Fprintf(&b, "<p>%s:</p>\n<ul>\n", e(it.MembersKind))
				for _, mb := range it.Members {
					fmt.Fprintf(&b, "<li><code>%s</code>", e(mb.Decl))
					if len(mb.Doc) > 0 {
						fmt.Fprintf(&b, ": %s", e(strings.Join(mb.Doc, "\n")))
					}
					b.WriteString("</li>\n")
				}
				b.WriteString("</ul>\n")
			}
		}
	}
	b.WriteString("</body></html>\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// DocBlocksSource is a file's text with every line that is not a /// doc
// line blanked and the markers of those that are taken off, so a fenced
// mk block of the doc text sits on its own source lines: the document
// mk doc --check extracts blocks from (corpus.Extract), whose line
// numbers are the file's.
func DocBlocksSource(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, l := range lines {
		t := strings.TrimLeft(l, " \t")
		if !strings.HasPrefix(t, "///") {
			lines[i] = ""
			continue
		}
		t = strings.TrimPrefix(t, "///")
		lines[i] = strings.TrimPrefix(t, " ")
	}
	return strings.Join(lines, "\n")
}

// DocFile is one source file mk doc --check reads: its path as a
// diagnostic cites it, and its text.
type DocFile struct {
	Path, Text string
}

// DocStdFiles is the source of std's modules under mod ("std" for all, else
// std.<module>), from MK_STD or the embedded std, in path order, cited as
// std/<dir>/<file>.mk. A mod no std directory holds is an error naming it,
// so mk doc reports an unknown module instead of an empty page.
func DocStdFiles(mod string) ([]DocFile, error) {
	var fsys fs.FS = std.FS
	if d := os.Getenv("MK_STD"); d != "" {
		fsys = os.DirFS(d)
	}
	root := "."
	if mod != "std" {
		if !strings.HasPrefix(mod, "std.") || len(mod) == len("std.") {
			return nil, fmt.Errorf("%s is not a standard-library module path (std, std.fs, ...)", mod)
		}
		root = strings.ReplaceAll(mod[len("std."):], ".", "/")
	}
	var out []DocFile
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != root && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
			return fs.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".mk") || p == "pkg.mk" {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		out = append(out, DocFile{Path: "std/" + p, Text: string(b)})
		return nil
	})
	if len(out) == 0 {
		return nil, fmt.Errorf("no standard-library module %s", mod)
	}
	return out, err
}

// DocTestPrelude is the test prelude std embeds (std.TestPreludePath),
// which mk doc --check gives each block as the corpus gives doc/'s.
func DocTestPrelude() (string, error) {
	b, err := fs.ReadFile(std.FS, std.TestPreludePath)
	return string(b), err
}
