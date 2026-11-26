// Why: mk doc's output is what users read as a package's API; a wrong
// spelling or a missing item misleads them.
// What: tests of Session.Doc's surface, its Markdown, embedded and HTML
// forms, the source mk doc --check extracts blocks from, and DocPreludeFor.
// How: a small package with every kind of item, and std.fs and std.testing.

package driver

import (
	"slices"
	"strings"
	"testing"
)

const docSource = "/// Doubles x.\n" +
	"export Double(x: int): int {\n    return 2 * x\n}\n\n" +
	"/// Deprecated: the name is unclear; use Double.\n" +
	"export Twice(x: int): int {\n    return Double(x)\n}\n\n" +
	"/// A point <with> markup & more.\n" +
	"export struct Point {\n    /// Across.\n    export x: int\n    y: int\n}\n\n" +
	"/// A shade.\nexport enum Shade {\n    /// No light.\n    Dark\n    Light\n}\n\n" +
	"/// The origin.\nexport const Origin: Point = Point(x: 0, y: 0)\n\n" +
	"/// A one-line struct.\nexport struct Pair { export a: int }\n\n" +
	"export struct Open {\n    export a: int\n}\n\n" +
	"export const Start: Open = Open(a: Limit)\n\nexport const Limit: int = 4\n\n" +
	"const Secret: int = 1\n\nexport const Leak: int = Secret\n\n" +
	"export type Spot = Point\n\nexport const ViaAlias: Spot = Spot(x: 0, y: 0)\n\n" +
	"export struct Cell[T] {\n    export v: T\n}\n\n" +
	"export Cell[T].New(v: T): Cell[T] {\n    return Cell[T](v: v)\n}\n\n" +
	"export var Count: Cell[int] = Cell[int].New(0)\n\n" +
	"export error Bad\n\nexport error Worse(code: i64)\n\nexport type Failure = Bad | Worse\n\n" +
	"/// Borrows.\nexport First(a: &Point): &int {\n    return &a.x\n}\n\n" +
	"export Commented(a: int): int { // the note\n    if a > 0 {\n        return a\n    }\n    return 0\n}\n\n" +
	"/// Fails either way.\nexport Either(x: int): Failure!void {\n    if x > 0 {\n        fail Bad\n    }\n    fail Worse(code: 1)\n}\n\n" +
	"/// Fails.\nexport Fails(): Bad!void {\n    fail Bad\n}\n\n" +
	"export Written(x: int): Worse|Bad!void {\n    if x > 0 {\n        fail Bad\n    }\n    fail Worse(code: 1)\n}\n\n" +
	"Hidden(): int {\n    return 1\n}\n\n" +
	"export Main() {\n    _ = Hidden()\n}\n"

func TestDocSurface(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": docSource})
	s, err := NewSession(Config{Root: root, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	dp, res, err := s.Doc(DocOptions{})
	if err != nil || dp == nil {
		t.Fatalf("doc: %v\n%s", err, res.Report.Text(res.Sources))
	}
	if len(dp.Modules) != 1 || dp.Modules[0].Path != "main" || dp.Modules[0].Class != ClassStable {
		t.Fatalf("modules %+v", dp.Modules)
	}
	var names []string
	for _, it := range dp.Modules[0].Items {
		names = append(names, it.Name)
		switch it.Name {
		case "Twice":
			if it.Deprecated != "the name is unclear; use Double." {
				t.Errorf("Twice's mark %q", it.Deprecated)
			}
		case "Fails":
			if len(it.Errors) != 1 || !strings.HasPrefix(it.Errors[0], "Bad") {
				t.Errorf("Fails's errors %q", it.Errors)
			}
		case "Point":
			if len(it.Members) != 1 || it.Members[0].Decl != "x: int" || strings.Join(it.Members[0].Doc, " ") != "Across." || it.MembersKind != "Fields" {
				t.Errorf("Point's fields: %+v (want x alone, without export, with its text)", it.Members)
			}
		case "Shade":
			if len(it.Members) != 2 || it.Members[0].Decl != "Dark" || strings.Join(it.Members[0].Doc, " ") != "No light." || it.MembersKind != "Variants" {
				t.Errorf("Shade's variants: %+v", it.Members)
			}
		case "Origin":
			// Point's y is private: no program outside main can write
			// the literal, so it is not shown.
			if strings.Join(it.Decl, "\n") != "export const Origin: Point" {
				t.Errorf("a constant whose initializer names a private field: %q", it.Decl)
			}
		case "Start":
			if strings.Join(it.Decl, "\n") != "export const Start: Open = Open(a: Limit)" {
				t.Errorf("a constant prints its initializer as written, not its bytes: %q", it.Decl)
			}
		case "Limit":
			if strings.Join(it.Decl, "\n") != "export const Limit: int = 4" {
				t.Errorf("a scalar constant: %q", it.Decl)
			}
		case "Leak":
			if strings.Join(it.Decl, "\n") != "export const Leak: int" {
				t.Errorf("a constant whose initializer names a private item: %q", it.Decl)
			}
		case "ViaAlias":
			// Spot is Point by another name: y is still private.
			if strings.Join(it.Decl, "\n") != "export const ViaAlias: Spot" {
				t.Errorf("a literal through an alias of a struct with a private field: %q", it.Decl)
			}
		case "Count":
			if strings.Join(it.Decl, "\n") != "export var Count: Cell[int] = Cell[int].New(0)" {
				t.Errorf("a method of a generic type in an initializer: %q", it.Decl)
			}
		case "Pair":
			if len(it.Members) != 1 || len(it.Members[0].Doc) != 0 {
				t.Errorf("a field on its type's line took the type's text: %+v", it.Members)
			}
		case "Written":
			if it.ErrorSet != "Worse|Bad" || strings.Join(it.Errors, ", ") != "Worse(code: i64), Bad" {
				t.Errorf("Written's errors in the written order: set %q, members %q", it.ErrorSet, it.Errors)
			}
		case "Worse":
			if strings.Join(it.Decl, "\n") != "export error Worse(code: i64)" {
				t.Errorf("an error keeps its payload: %q", it.Decl)
			}
		case "Either":
			if it.ErrorSet != "Failure" || strings.Join(it.Errors, ", ") != "Bad, Worse(code: i64)" {
				t.Errorf("Either's errors: set %q, members %q", it.ErrorSet, it.Errors)
			}
		case "First":
			if strings.Join(it.Flows, " ") != "The result borrows from a." {
				t.Errorf("First's flows in words: %q", it.Flows)
			}
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("items not sorted: %q", names)
	}
	for _, it := range dp.Modules[0].Items {
		if it.Name == "Either" && (len(it.Decl) != 1 || it.Decl[0] != "export Either(x: int): Failure!void") {
			t.Errorf("Either's head names its set: %q", it.Decl)
		}
		if it.Name == "Commented" && (len(it.Decl) != 1 || it.Decl[0] != "export Commented(a: int): int") {
			t.Errorf("a comment after the opening brace leaks the body: %q", it.Decl)
		}
	}
	if got := strings.Join(names, " "); strings.Contains(got, "Hidden") || !strings.Contains(got, "Double") || !strings.Contains(got, "Point") {
		t.Errorf("items %s", got)
	}
	var md, h strings.Builder
	if err := dp.WriteMarkdown(&md); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteHTML(&h); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"## Module `main`", "### `Twice`", "- Errors: Worse(code: i64), Bad\n", "**Deprecated:** the name is unclear; use Double.", "export Double(x: int): int",
		"- Errors: Bad\n", "- Errors: Failure (Bad, Worse(code: i64))", "- Flows: The result borrows from a.", "Fields:\n\n- `x: int`: Across.\n"} {
		if !strings.Contains(md.String(), w) {
			t.Errorf("Markdown lacks %q:\n%s", w, md.String())
		}
	}
	if strings.Contains(md.String(), "Stability:") || strings.Contains(h.String(), "Stability:") {
		t.Errorf("a package's own module has a stability line:\n%s", md.String())
	}
	if strings.Count(md.String(), "\n# ") != 0 || !strings.HasPrefix(md.String(), "# ") {
		t.Errorf("the Markdown page has not exactly one H1:\n%s", md.String())
	}
	var em strings.Builder
	if err := dp.WriteEmbed(&em); err != nil {
		t.Fatal(err)
	}
	if got := em.String(); !strings.HasPrefix(got, "### `") || strings.Contains(got, "\n# ") || strings.Contains(got, "## Module") {
		t.Errorf("the embedded form has a title or a module heading:\n%s", got)
	}
	for _, w := range []string{"<h2 id=\"main\">Module main</h2>", "A point &lt;with&gt; markup &amp; more.", "<p class=\"dep\"><b>Deprecated:</b>", "<li><code>x: int</code>: Across.</li>", "<p>Errors: Failure (Bad, Worse(code: i64))</p>"} {
		if !strings.Contains(h.String(), w) {
			t.Errorf("HTML lacks %q", w)
		}
	}
}

func TestDocStdModule(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": "export Main() {\n}\n"})
	s, err := NewSession(Config{Root: root, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	dp, _, err := s.Doc(DocOptions{Std: "std.testing"})
	if err != nil || dp == nil || len(dp.Modules) != 1 || dp.Modules[0].Path != "std.testing" {
		t.Fatalf("std.testing: %+v %v", dp, err)
	}
	var names []string
	for _, it := range dp.Modules[0].Items {
		names = append(names, it.Name)
	}
	if strings.Join(names, " ") != "Bench BenchSys Fuzz" {
		t.Errorf("std.testing's surface %q, want Bench, BenchSys and Fuzz only", names)
	}
}

// TestDocStdFs: std.fs's page spells another module's type as a program
// writes it, leaves out File.Adopt (it needs a std.rt type), and names
// Absolute's error set as written with each member once.
func TestDocStdFs(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": "export Main() {\n}\n"})
	s, err := NewSession(Config{Root: root, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	dp, _, err := s.Doc(DocOptions{Std: "std.fs"})
	if err != nil || dp == nil || len(dp.Modules) != 1 {
		t.Fatalf("std.fs: %+v %v", dp, err)
	}
	items := map[string]DocItem{}
	for _, it := range dp.Modules[0].Items {
		items[it.Name] = it
	}
	if _, ok := items["File.Adopt"]; ok {
		t.Errorf("File.Adopt is listed, though it needs a std.rt type")
	}
	if it := items["Info"]; !slices.ContainsFunc(it.Members, func(m DocMember) bool { return m.Decl == "modified: time.Time" }) {
		t.Errorf("Info's fields: %+v, want modified: time.Time", it.Members)
	}
	if it := items["Os"]; strings.Join(it.Decl, "\n") != "export error Os(code: i64)" {
		t.Errorf("Os: %q", it.Decl)
	}
	// Options has a private field, so no program outside std.fs can write
	// its literal: the constant shows its type alone.
	if it := items["Append"]; strings.Join(it.Decl, "\n") != "export const Append: Options" {
		t.Errorf("Append: %q", it.Decl)
	}
	abs := items["Absolute"]
	if abs.ErrorSet != "Error|OutOfMemory" || !slices.Contains(abs.Errors, "Os(code: i64)") || !slices.Contains(abs.Errors, "OutOfMemory") {
		t.Errorf("Absolute's errors: set %q, members %q", abs.ErrorSet, abs.Errors)
	}
	seen := map[string]bool{}
	for _, m := range abs.Errors {
		if seen[m] || strings.Contains(m, " ") && !strings.Contains(m, "(") {
			t.Errorf("Absolute's members: %q repeated or spaced", m)
		}
		seen[m] = true
	}
	if strings.Join(abs.Flows, " ") != "The result borrows from a." {
		t.Errorf("Absolute's flows: %q", abs.Flows)
	}
}

// TestDocGenericReceiver: a method of a generic type keeps its head as
// written, Atomic[T].Load, with its clauses.
func TestDocGenericReceiver(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": "export Main() {\n}\n"})
	s, err := NewSession(Config{Root: root, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	dp, _, err := s.Doc(DocOptions{Std: "std.atomic"})
	if err != nil || dp == nil || len(dp.Modules) != 1 {
		t.Fatalf("std.atomic: %+v %v", dp, err)
	}
	found := false
	for _, it := range dp.Modules[0].Items {
		if it.Name == "Atomic.Load" {
			found = true
			if !strings.HasPrefix(strings.Join(it.Decl, " "), "export Atomic[T].Load(") {
				t.Errorf("Atomic.Load's head: %q", it.Decl)
			}
		}
	}
	if !found {
		t.Error("Atomic.Load is not listed")
	}
}

// TestDocStdInternal: a std-only module other than std.raw is never
// documented, even when --std names it.
func TestDocStdInternal(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": "export Main() {\n}\n"})
	s, err := NewSession(Config{Root: root, CacheDir: t.TempDir(), BrowseStd: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, mod := range []string{"std.rt", "std.sys", "std.simd.kernel"} {
		if DocStdBrowse(mod) {
			t.Errorf("DocStdBrowse(%s)", mod)
		}
		dp, res, err := s.Doc(DocOptions{Std: mod})
		if err != nil || dp == nil {
			t.Fatalf("%s: %v\n%s", mod, err, res.Report.Text(res.Sources))
		}
		if len(dp.Modules) != 0 {
			t.Errorf("%s documents %d modules, want none", mod, len(dp.Modules))
		}
	}
}

// TestDocEmbedSubmodule: the embedded form of a module with a public
// submodule puts the submodule under a ### heading of its own with its
// items at ####, and no constant shows an initializer that names a private
// table.
func TestDocEmbedSubmodule(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": "export Main() {\n}\n"})
	s, err := NewSession(Config{Root: root, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	dp, _, err := s.Doc(DocOptions{Std: "std.encoding"})
	if err != nil || dp == nil || len(dp.Modules) < 2 {
		t.Fatalf("std.encoding: %+v %v", dp, err)
	}
	var b strings.Builder
	if err := dp.WriteEmbed(&b); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.HasPrefix(got, "Stability: ") || !strings.Contains(got, "\n### Module `std.encoding.cjk`\n") || !strings.Contains(got, "\n#### `") {
		t.Errorf("embedded std.encoding:\n%s", got)
	}
	for _, m := range dp.Modules {
		for _, it := range m.Items {
			if d := strings.Join(it.Decl, " "); strings.HasPrefix(d, "export const ") && (strings.Contains(d, "NoTable") || strings.Contains(d, "Big5Name")) {
				t.Errorf("%s.%s shows a private item: %q", m.Path, it.Name, it.Decl)
			}
		}
	}
}

func TestDocBlocksSource(t *testing.T) {
	src := "// not doc\n/// Text.\n    /// ```mk\n///// x\n///\nexport F() {}\n"
	want := "\nText.\n```mk\n// x\n\n\n"
	if got := DocBlocksSource(src); got != want {
		t.Errorf("DocBlocksSource = %q, want %q", got, want)
	}
}

// TestDocPreludeFor: a block's own declarations replace the prelude's of
// that name, and a method a block adds to a prelude type keeps the type.
func TestDocPreludeFor(t *testing.T) {
	prelude, err := DocTestPrelude()
	if err != nil {
		t.Fatal(err)
	}
	got := DocPreludeFor(prelude, "// verdict: ok\nPoint.Sum(p: &Point): i32 {\n    return p.x + p.y\n}\n")
	if !strings.Contains(got, "struct Point {") || !strings.Contains(got, "    x: i32") {
		t.Errorf("a method on Point dropped the prelude's Point:\n%s", got)
	}
	got = DocPreludeFor(prelude, "// verdict: ok\nTake(x: int): int {\n    return x\n}\n")
	if strings.Contains(got, "Take[T]") || !strings.Contains(got, "Use[T]") {
		t.Errorf("a block's own Take:\n%s", got)
	}
}

// TestDocStdCoreTypes: over all of std, no declaration line spells a
// std.core type with `core.`: a prelude type prints bare, a type a public module re-exports by an alias prints the alias
// (std.runtime's IoTable), and an item that needs any other std.core type
// is left out (Secure.Of and Secure's call field take core.ErasedCall);
// an interface keeps the methods a program can name (Selectable's Ready)
// and names the others in one comment.
func TestDocStdCoreTypes(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": "export Main() {\n}\n"})
	s, err := NewSession(Config{Root: root, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	dp, _, err := s.Doc(DocOptions{Std: "std"})
	if err != nil || dp == nil {
		t.Fatalf("std: %+v %v", dp, err)
	}
	items := map[string]DocItem{}
	for _, m := range dp.Modules {
		for _, it := range m.Items {
			items[m.Path+" "+it.Name] = it
			lines := slices.Clone(it.Decl)
			for _, mb := range it.Members {
				lines = append(lines, mb.Decl)
			}
			for _, l := range lines {
				if strings.Contains(" "+l, " core.") || strings.Contains(l, "(core.") || strings.Contains(l, "&core.") || strings.Contains(l, "*core.") || strings.Contains(l, "[core.") {
					t.Errorf("%s %s spells a std.core type: %q", m.Path, it.Name, l)
				}
			}
		}
	}
	if _, ok := items["std.net Secure.Of"]; ok {
		t.Error("Secure.Of is listed, though it takes a core.ErasedCall")
	}
	if it, ok := items["std.net Secure"]; !ok || slices.ContainsFunc(it.Members, func(m DocMember) bool { return strings.HasPrefix(m.Decl, "call") }) {
		t.Errorf("Secure: %+v, want it listed without its call field", it)
	}
	if it := items["std.runtime ForeignIo"]; !strings.Contains(strings.Join(it.Decl, " "), "table: &IoTable") {
		t.Errorf("std.runtime ForeignIo: %q, want &IoTable", it.Decl)
	}
	sel := strings.Join(items["std.sync Selectable"].Decl, "\n")
	if !strings.Contains(sel, "Ready(&self): bool") || strings.Contains(sel, "Watch(") || !strings.Contains(sel, "// std-internal methods: Watch, Unwatch and Pass") {
		t.Errorf("std.sync Selectable:\n%s", sel)
	}
}
