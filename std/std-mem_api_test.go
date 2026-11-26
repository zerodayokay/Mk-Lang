// Why: std's public surface must match the language reference line for line,
// and package std reaches no compiler package, so the check works on text.
// What: TestAPIMatchesReference, TestAPIRouting,
// TestArgumentOrder and TestNoUnlistedPublicNames.
// How: parses the reference's Library surface block, routes each line to its
// module and wants it declared there word for word (fs.Error is Error inside
// std.fs); types compare as text, and mk check of std resolves them.

package std

import (
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// apiItem is one declaration of the reference's library surface block: a one-line
// declaration (a signature, an error, a type alias) or a block (struct,
// enum, interface) with its body lines.
type apiItem struct {
	mod    string   // routed module, "std.core", "std.fs", ...
	text   string   // the line as written in the reference
	body   []string // block body lines, trimmed; nil for one-line items
	block  bool
	inSect string // the section module the line sits in, "" outside
}

// referenceAPIBlock returns the lines of the verdict-parse block under the
// reference's Library surface heading.
func referenceAPIBlock(t *testing.T) []string {
	t.Helper()
	sec := referenceSection(t, "## 18. Library surface")
	i := strings.Index(sec, "```mk\n// verdict: parse\n")
	if i < 0 {
		t.Fatal("the reference has no verdict-parse block")
	}
	rest := sec[i+len("```mk\n// verdict: parse\n"):]
	j := strings.Index(rest, "\n```")
	if j < 0 {
		t.Fatal("the reference's verdict-parse block is not closed")
	}
	return strings.Split(rest[:j], "\n")
}

// moduleTable is the reference's module map table: module -> names.
func moduleTable(t *testing.T) map[string][]string {
	t.Helper()
	sec := referenceSection(t, "## 17. Module map")
	row := regexp.MustCompile("(?m)^\\| `(std\\.[a-z0-9.]+)`(?:, `(std\\.[a-z0-9.]+)`)? \\| ([^|]*)\\|")
	tab := map[string][]string{}
	name := regexp.MustCompile("`([^`]*)`")
	for _, m := range row.FindAllStringSubmatch(sec, -1) {
		var names []string
		for _, n := range name.FindAllStringSubmatch(m[3], -1) {
			names = append(names, strings.Fields(n[1])...)
		}
		// "derive.Eq" in the std.dyn, std.derive row is std.derive's Eq.
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
	return tab
}

// The module may be nested (std.db.sqlite); its lines then qualify with
// its last component (sqlite.Open), as user code does.
var sectionComment = regexp.MustCompile(`^// (std\.[a-z][a-z0-9]*(?:\.[a-z][a-z0-9]*)*)\b`) // digits after the first letter: std.x509

// modShort is the name a module's lines qualify with: the last component
// of its path (std.fs: fs, std.db.sqlite: sqlite).
func modShort(mod string) string {
	return mod[strings.LastIndex(mod, ".")+1:]
}

// itemHead is the declared name of a one-line item: "Box[T].New" gives
// receiver "Box", name "New"; "fs.Rename" gives receiver "fs"; "Print[A...]"
// gives receiver "", name "Print"; "error Os(..)" gives "Os".
func itemHead(text string) (recv, name string) {
	s := text
	// A const's name ends at its type's colon, so
	// `const Relaxed: Order = Order(v: 0)` is the item Relaxed.
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
	// Drop generic brackets, keeping the dots outside them.
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

// parseReferenceAPI splits the library surface block into items and routes
// each: inside a "// std.x" section, a name resolves in the
// section's module when the section declares it, when the module map lists it under a
// module, when it is the receiver of a section line and not a prelude name,
// or when the line is the module's own free function (x.Name); everything
// else resolves in the prelude module std.core.
func parseReferenceAPI(t *testing.T) []apiItem {
	t.Helper()
	lines := referenceAPIBlock(t)
	tab := moduleTable(t)
	prelude := map[string]bool{}
	for _, n := range PreludeNames {
		prelude[n] = true
	}
	listed := map[string]string{} // name -> module of the module map, for names in a built module
	mods := make([]string, 0, len(tab))
	for mod := range tab {
		mods = append(mods, mod)
	}
	sort.Strings(mods) // the same choice on every run (DF-fetch-04; tools/dogfood/refapi.go keeps the same rule)
	for _, mod := range mods {
		for _, n := range tab[mod] {
			if _, ok := listed[n]; !ok {
				listed[n] = mod
			}
		}
	}
	var items []apiItem
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
		it := apiItem{text: l, inSect: sect}
		if strings.HasSuffix(l, " {") {
			it.block = true
			for i++; i < len(lines) && lines[i] != "}"; i++ {
				if s := strings.TrimSpace(lines[i]); s != "" {
					it.body = append(it.body, s)
				}
			}
		}
		items = append(items, it)
	}
	// Names the sections declare, per section. A block whose name is a
	// prelude name (struct Limits, enum Os after "// std.net") is the
	// prelude's: the reference lists the prelude's declarations after the last section
	// comment. An error or type line is always its section's (fs.Os).
	declared := map[string]bool{} // section + " " + name
	for _, it := range items {
		if it.inSect == "" {
			continue
		}
		if it.block {
			if n := blockName(it.text); !prelude[n] {
				declared[it.inSect+" "+n] = true
			}
		} else if strings.HasPrefix(it.text, "error ") || strings.HasPrefix(it.text, "type ") {
			_, n := itemHead(it.text)
			declared[it.inSect+" "+n] = true
		}
	}
	for k := range items {
		it := &items[k]
		it.mod = "std.core"
		if it.inSect == "" {
			continue
		}
		short := modShort(it.inSect)
		var name string
		if it.block {
			name = blockName(it.text)
			if prelude[name] {
				continue
			}
		} else if strings.HasPrefix(it.text, "error ") || strings.HasPrefix(it.text, "type ") {
			it.mod = it.inSect
			continue
		} else {
			recv, n := itemHead(it.text)
			switch {
			case recv == short:
				it.mod = it.inSect
				continue
			case recv != "":
				name = recv
			default:
				name = n
			}
		}
		switch {
		case declared[it.inSect+" "+name]:
			it.mod = it.inSect
		case slices.Contains(tab[it.inSect], name) && !prelude[name]:
			// A name the module map lists under this section's own module (std.net,
			// std.websocket and std.tls each list a Conn) is that
			// module's; listed below keeps one module per name, the first
			// found in a map, so it cannot decide a shared name.
			it.mod = it.inSect
		case listed[name] != "" && !prelude[name]:
			it.mod = listed[name]
		case !prelude[name]:
			it.mod = it.inSect
		}
	}
	return items
}

// TestAPIRouting: the router alone, on the reference as on disk.
func TestAPIRouting(t *testing.T) {
	items := parseReferenceAPI(t)
	want := map[string]string{
		"AtomicInt.New(n: i64): AtomicInt":                     "std.atomic",
		"error Os(code: i64)":                                  "std.fs", // the first one, std.fs's
		"fs.Rename(io: Io, src: str, dst: str): fs.Error!void": "std.fs",
		"Duration.Nanos(self): i64":                            "std.time",
		"Mutex[T].New(x: T): Mutex[T]":                         "std.core",
		"Shared[T].Ref(&self): &T":                             "std.sync",
		"enum Os {":                                            "std.core",
		"enum Signal {":                                        "std.proc",
		"Listener.Port(&self): int":                            "std.net",
		"math.Sqrt(x: f64): f64":                               "std.math",
		"Io.Sleep(self, d: time.Duration): Canceled!void":      "std.core",
		"Cell[T].Load(&self): T":                               "std.core",
	}
	seen := map[string]bool{}
	for _, it := range items {
		if m, ok := want[it.text]; ok && !seen[it.text] {
			seen[it.text] = true
			if it.mod != m {
				t.Errorf("%q routes to %s, want %s", it.text, it.mod, m)
			}
		}
	}
	for l := range want {
		if !seen[l] {
			t.Errorf("the reference has no line %q", l)
		}
	}
	for _, it := range items {
		if it.mod == "" {
			t.Errorf("%q routes nowhere", it.text)
		}
	}
	// The second error Os is std.net's, the third std.proc's.
	var oss []string
	for _, it := range items {
		if it.text == "error Os(code: i64)" {
			oss = append(oss, it.mod)
		}
	}
	// The fourth is std.dl's, the fifth std.vm's.
	if !slices.Equal(oss, []string{"std.fs", "std.net", "std.proc", "std.dl", "std.vm"}) {
		t.Errorf("error Os routes to %v, want [std.fs std.net std.proc std.dl std.vm]", oss)
	}
}

// stdDecl is one export of a std module's source.
type stdDecl struct {
	text string   // normalized: no export mark, no body brace, one space between words
	body []string // a block's body lines, comments dropped, trimmed
	file string
}

var lineComment = regexp.MustCompile(`\s*//.*$`)

// moduleDir is the directory of a std module's source. std.linux is
// target-selected (std/pkg.mk): its x86_64-linux source is linux/linux, and
// linux/other is the empty placeholder of every other target.
func moduleDir(mod string) string {
	if mod == "std.linux" {
		return "linux/linux"
	}
	return strings.ReplaceAll(strings.TrimPrefix(mod, "std."), ".", "/")
}

// moduleDecls reads every non-test .mk file of the module's directory and
// returns its top-level exports. A signature that continues over lines
// (unbalanced parentheses) is joined.
func moduleDecls(t *testing.T, mod string) []stdDecl {
	t.Helper()
	dir := moduleDir(mod)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []stdDecl
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".mk") || strings.HasPrefix(n, "tests_") {
			continue
		}
		file := path.Join(dir, n)
		lines := strings.Split(readText(t, file), "\n")
		for i := 0; i < len(lines); i++ {
			l := lines[i]
			if !strings.HasPrefix(l, "export ") {
				continue
			}
			s := lineComment.ReplaceAllString(l, "")
			for strings.Count(s, "(") > strings.Count(s, ")") && i+1 < len(lines) {
				i++
				s += " " + strings.TrimSpace(lineComment.ReplaceAllString(lines[i], ""))
			}
			s = strings.Join(strings.Fields(strings.TrimPrefix(s, "export ")), " ")
			// The reference marks an open set with a comment, std with the word
			// open, so the word is not part of the line compared.
			s = strings.TrimPrefix(s, "open ")
			s = strings.ReplaceAll(s, "( ", "(")
			s = strings.ReplaceAll(s, ", )", ")")
			s = strings.ReplaceAll(s, ",)", ")")
			d := stdDecl{file: file}
			switch {
			case strings.HasSuffix(s, " {}"):
				d.text = strings.TrimSuffix(s, " {}")
			case strings.HasSuffix(s, " {") && blockKind(s):
				d.text = strings.TrimSuffix(s, " {")
				for i++; i < len(lines) && lines[i] != "}"; i++ {
					b := strings.TrimSpace(lineComment.ReplaceAllString(lines[i], ""))
					// An interface method that names a std.core type is std's
					// own (Selectable's Watch and Unwatch take a
					// *core.SelPark); no module outside std can name it, so the reference
					// lists only the public methods.
					if strings.Contains(s, "interface") && strings.Contains(b, "core.") {
						continue
					}
					if b != "" {
						d.body = append(d.body, b)
					}
				}
			case strings.HasSuffix(s, " {"):
				d.text = strings.TrimSuffix(s, " {")
			default:
				d.text = s
			}
			out = append(out, d)
		}
	}
	return out
}

// implRe matches the head of an impl block: impl[params] Iface for Type {.
var implRe = regexp.MustCompile(`^impl(\[[^\]]*\])? [A-Za-z_][A-Za-z0-9_.]*(\[[^\]]*\])? for (.+) \{$`)

// implMethodDecls reads the methods of the module's impl blocks as the reference
// lines they satisfy: impl Format for Path { Format(&self, w: mut Writer): WriteError!void }
// is the line Path.Format(&self, w: mut Writer): WriteError!void. Only
// TestAPIMatchesReference uses them, to find a line; an impl method is never an unlisted
// public name (TestNoUnlistedPublicNames), since impls are visible wherever their type is.
func implMethodDecls(t *testing.T, mod string) []stdDecl {
	t.Helper()
	dir := moduleDir(mod)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []stdDecl
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".mk") || strings.HasPrefix(n, "tests_") {
			continue
		}
		file := path.Join(dir, n)
		lines := strings.Split(readText(t, file), "\n")
		for i := 0; i < len(lines); i++ {
			m := implRe.FindStringSubmatch(strings.TrimSpace(lineComment.ReplaceAllString(lines[i], "")))
			if m == nil || strings.HasPrefix(lines[i], " ") {
				continue
			}
			typ := strings.TrimSpace(m[3])
			for i++; i < len(lines) && lines[i] != "}"; i++ {
				l := lineComment.ReplaceAllString(lines[i], "")
				if !strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "     ") {
					continue
				}
				h := strings.TrimSpace(l)
				if h == "" || !strings.Contains(h, "(") || h[0] < 'A' || h[0] > 'Z' {
					continue
				}
				for strings.Count(h, "(") > strings.Count(h, ")") && i+1 < len(lines) {
					i++
					h += " " + strings.TrimSpace(lineComment.ReplaceAllString(lines[i], ""))
				}
				h = strings.TrimSuffix(strings.TrimSpace(h), "{")
				h = strings.Join(strings.Fields(h), " ")
				h = strings.ReplaceAll(h, "( ", "(")
				h = strings.ReplaceAll(h, ", )", ")")
				h = strings.ReplaceAll(h, ",)", ")")
				out = append(out, stdDecl{text: typ + "." + h, file: file})
			}
		}
	}
	return out
}

// blockKind reports whether a declaration header opens a struct, enum or
// interface body (as opposed to a function body).
func blockKind(s string) bool {
	for _, f := range strings.Fields(s) {
		switch f {
		case "struct", "enum", "interface":
			return true
		case "linear", "mut", "view", "packed":
			continue
		}
		return false
	}
	return false
}

// coreAliases are the user-importable modules' aliases of std.core types:
// inside std.core, time.Duration is written Duration, and the
// generic text.Sink[S] is Sink[S].
var coreAliases = regexp.MustCompile(`(?m)^export type (\w+)(?:\[[^\]]*\])? = core\.(\w+)(?:\[[^\]]*\])?\s*$`)

// normalizeFor rewrites a reference line as the routed module writes it: the
// module's own qualifier goes (fs.Error is Error in std.fs), and in
// std.core an alias of a core type is its target (time.Duration is
// Duration).
func normalizeFor(mod, text string, aliases map[string]string) string {
	short := modShort(mod)
	text = regexp.MustCompile(`\b`+regexp.QuoteMeta(short)+`\.`).ReplaceAllString(text, "")
	if mod == "std.core" {
		for q, target := range aliases {
			text = regexp.MustCompile(`\b`+regexp.QuoteMeta(q)+`\b`).ReplaceAllString(text, target)
		}
	}
	return strings.Join(strings.Fields(text), " ")
}

// corePreludeRef matches &core.T, a reference to a std.core type spelled
// with its module.
var corePreludeRef = regexp.MustCompile(`&core\.(\w+)\b`)

// preludeRef reads a std declaration's &core.T as &T when T is a prelude
// type: a std module whose own item shadows the prelude name writes the
// type as core.T, and the reference shows the caller's view, &T
// (free.Heap(): &Heap).
func preludeRef(text string) string {
	return corePreludeRef.ReplaceAllStringFunc(text, func(m string) string {
		if n := strings.TrimPrefix(m, "&core."); slices.Contains(PreludeNames[:], n) {
			return "&" + n
		}
		return m
	})
}

// coreQualified matches core.T, a std.core type spelled with its module.
var coreQualified = regexp.MustCompile(`\bcore\.(\w+)\b`)

// ownAliasRef reads a std declaration's core.T as T when the declaring
// module exports the alias `type T = core.T`: std.runtime declares
// ForeignIo's table as &core.IoTable and exports IoTable, and the reference
// shows the caller's view, &IoTable.
func ownAliasRef(mod, text string, aliases map[string]string) string {
	short := modShort(mod)
	return coreQualified.ReplaceAllStringFunc(text, func(m string) string {
		n := strings.TrimPrefix(m, "core.")
		if aliases[short+"."+n] == n {
			return n
		}
		return m
	})
}

// apiPending lists reference lines std does not declare yet, with a note on
// each. A line here that std now declares as written is logged, so it can
// be dropped; any other mismatch fails the test.
var apiPending = map[string]string{}

// userModules are the std modules user code may import (the reference's
// module map), which TestNoUnlistedPublicNames and TestArgumentOrder cover besides std.core's
// prelude members.
var userModules = []string{"std.fs", "std.net", "std.time", "std.proc", "std.sync", "std.atomic", "std.math", "std.derive", "std.coll", "std.hash", "std.mem", "std.cli", "std.log", "std.runtime", "std.vm", "std.linux"}

// TestAPIMatchesReference: every line of the reference's library
// surface block is declared in its routed module exactly. Bodiless intrinsics count the same as declarations with bodies.
func TestAPIMatchesReference(t *testing.T) {
	items := parseReferenceAPI(t)
	aliases := map[string]string{} // "time.Duration" -> "Duration"
	// std.text holds two aliases and is not on userModules.
	for _, m := range append([]string{"std.text"}, userModules...) {
		for _, d := range moduleDecls(t, m) {
			if mm := coreAliases.FindStringSubmatch("export " + d.text); mm != nil {
				aliases[modShort(m)+"."+mm[1]] = mm[2]
			}
		}
	}
	decls := map[string][]stdDecl{}
	for _, it := range items {
		if _, ok := decls[it.mod]; !ok {
			decls[it.mod] = append(moduleDecls(t, it.mod), implMethodDecls(t, it.mod)...)
		}
	}
	targetDecls := moduleDecls(t, "std.target")
	var bad []string
	matched := 0
	for _, it := range items {
		mod := it.mod
		want := normalizeFor(mod, it.text, aliases)
		if it.block {
			want = strings.TrimSuffix(want, " {")
		}
		ds := decls[mod]
		// The reference lists TargetInfo, Arch and Os under std.core, as the
		// prelude offers them; std.core aliases std.target's, so
		// they are declared there.
		if mod == "std.core" {
			ds = append(slices.Clone(ds), targetDecls...)
		}
		// An alias of a core type: its methods are std.core's.
		if recv, _ := itemHead(it.text); !it.block && recv != "" {
			if target, ok := aliases[modShort(mod)+"."+recv]; ok && mod != "std.core" {
				mod = "std.core"
				ds = append(moduleDecls(t, mod), implMethodDecls(t, mod)...)
				// The alias's own name, bare in its module, is the
				// target's in std.core (time.Duration is Duration).
				bare := regexp.MustCompile(`\b`+regexp.QuoteMeta(recv)+`\b`).ReplaceAllString(it.text, target)
				want = normalizeFor(mod, bare, aliases)
			}
		}
		var found *stdDecl
		for k := range ds {
			// An alias's own line (type AllocTable = core.AllocTable) is
			// compared as written: ownAliasRef would fold its right side.
			if d := preludeRef(ds[k].text); ownAliasRef(mod, d, aliases) == want || d == want {
				found = &ds[k]
				break
			}
		}
		key := mod + " | " + it.text
		if found == nil {
			if lane, ok := apiPending[key]; ok {
				t.Logf("pending (%s): %s", lane, key)
				continue
			}
			msg := key + "\n    want: export " + want
			recv, name := itemHead(want)
			for _, d := range ds {
				r2, n2 := itemHead(d.text)
				if it.block && blockName(d.text) == blockName(want) || !it.block && r2 == recv && n2 == name {
					msg += "\n    have: export " + d.text + "   (" + d.file + ")"
				}
			}
			bad = append(bad, msg)
			continue
		}
		if _, ok := apiPending[key]; ok {
			t.Logf("now declared, drop it from apiPending: %s", key)
		}
		matched++
		if !it.block {
			continue
		}
		var wantBody []string
		for _, b := range it.body {
			wantBody = append(wantBody, normalizeFor(mod, b, aliases))
		}
		switch strings.Fields(want)[0] {
		case "enum", "interface":
			if !slices.Equal(found.body, wantBody) {
				bad = append(bad, key+"\n    body: "+strings.Join(found.body, "; ")+"\n    want: "+strings.Join(wantBody, "; "))
			}
		default: // struct: every listed field is declared; every exported field is listed.
			for _, b := range wantBody {
				if !slices.Contains(found.body, b) {
					bad = append(bad, key+"\n    field "+b+" missing ("+found.file+")")
				}
			}
			for _, b := range found.body {
				if strings.HasPrefix(b, "export ") && !slices.Contains(wantBody, b) {
					bad = append(bad, key+"\n    exported field "+b+" is not in §18 ("+found.file+")")
				}
			}
		}
	}
	t.Logf("%d of %d library-surface items declared as written", matched, len(items))
	for _, b := range bad {
		t.Error(b)
	}
}

// paramList returns the parameters of a signature's first parenthesized
// list, split at top-level commas.
func paramList(sig string) []string {
	i := strings.IndexByte(sig, '(')
	if i < 0 {
		return nil
	}
	depth := 0
	start := i + 1
	var out []string
	for j := i; j < len(sig); j++ {
		switch sig[j] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
			if depth == 0 {
				if s := strings.TrimSpace(sig[start:j]); s != "" {
					out = append(out, s)
				}
				return out
			}
		case ',':
			if depth == 1 {
				out = append(out, strings.TrimSpace(sig[start:j]))
				start = j + 1
			}
		}
	}
	return out
}

// argClass is a parameter's rank in std's argument order: 0 an allocator, arena or heap; 1 an Io; 2
// anything else; -1 the receiver.
func argClass(p string) int {
	if p == "self" || p == "&self" || p == "mut self" {
		return -1
	}
	ty := p
	if k := strings.Index(p, ": "); k >= 0 {
		ty = p[k+2:]
	}
	ty = strings.TrimSpace(strings.Split(ty, " into ")[0])
	switch ty {
	case "&Alloc", "&Arena", "&Heap":
		return 0
	case "Io":
		return 1
	}
	return 2
}

// TestArgumentOrder: in every reference signature and every exported
// function of a user-importable module, an allocator, arena or heap
// parameter precedes any Io parameter, which precedes every other
// non-receiver parameter.
func TestArgumentOrder(t *testing.T) {
	var sigs []string
	for _, it := range parseReferenceAPI(t) {
		if !it.block && !strings.HasPrefix(it.text, "error ") && !strings.HasPrefix(it.text, "type ") {
			sigs = append(sigs, it.text)
		}
	}
	for _, m := range userModules {
		for _, d := range moduleDecls(t, m) {
			if d.body == nil && strings.Contains(d.text, "(") && !strings.HasPrefix(d.text, "error ") {
				sigs = append(sigs, m+": "+d.text)
			}
		}
	}
	checked := 0
	for _, s := range sigs {
		last := -1
		for _, p := range paramList(s) {
			c := argClass(p)
			if c < 0 {
				continue
			}
			if c < last {
				t.Errorf("argument order (allocator, Io, rest): %s", s)
				break
			}
			last = c
		}
		checked++
	}
	t.Logf("%d signatures checked", checked)
}

// TestNoUnlistedPublicNames: every export of a user-importable std
// module is named in the module map for it or declared by a reference line
// routed to it, and every exported member of a prelude type in std.core is
// a reference line. std.core's other exports are std-internal.
func TestNoUnlistedPublicNames(t *testing.T) {
	items := parseReferenceAPI(t)
	tab := moduleTable(t)
	named := map[string]map[string]bool{} // module -> names (receivers, members as Recv.Name)
	add := func(mod, n string) {
		if named[mod] == nil {
			named[mod] = map[string]bool{}
		}
		named[mod][n] = true
	}
	for mod, ns := range tab {
		for _, n := range ns {
			add(mod, n)
		}
	}
	for _, it := range items {
		if it.block {
			add(it.mod, blockName(it.text))
			continue
		}
		recv, n := itemHead(normalizeFor(it.mod, it.text, nil))
		if recv == "" {
			add(it.mod, n)
		} else {
			add(it.mod, recv+"."+n)
		}
		// A union's members are its module's names (fs.NotFound).
		if strings.HasPrefix(it.text, "type ") {
			if k := strings.Index(it.text, "= "); k >= 0 {
				for _, m := range strings.Split(it.text[k+2:], "|") {
					add(it.mod, strings.TrimSpace(m))
				}
			}
		}
	}
	// The generator API's members are listed by genSurface;
	// TestGeneratorAPIDeclared pins them.
	for _, l := range strings.Split(genSurface, "\n") {
		if recv, n := itemHead(l); recv != "" && strings.Contains(l, "(") {
			add("std.core", recv+"."+n)
		}
	}
	prelude := map[string]bool{}
	for _, n := range PreludeNames {
		prelude[n] = true
	}
	for _, m := range append([]string{"std.core"}, userModules...) {
		for _, d := range moduleDecls(t, m) {
			var recv, n string
			if blockKind(d.text) {
				n = blockName(d.text)
			} else {
				recv, n = itemHead(d.text)
			}
			if m == "std.core" {
				// Only members of prelude types are public in std.core.
				if recv == "" || !prelude[recv] || named[m][recv+"."+n] {
					continue
				}
				t.Errorf("%s: %s exports %s.%s, a member of a prelude type with no line in the reference", d.file, m, recv, n)
				continue
			}
			key := n
			if recv != "" {
				key = recv + "." + n
			}
			if !named[m][key] {
				t.Errorf("%s: %s exports %s, which the reference names nowhere", d.file, m, key)
			}
		}
	}
	// Exported fields of std.core's prelude structs are the reference's.
	var names []string
	for n := range named["std.core"] {
		names = append(names, n)
	}
	sort.Strings(names)
	t.Logf("std.core: %d library-surface names", len(names))
}
