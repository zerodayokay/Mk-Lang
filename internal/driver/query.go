// Why: an agent or an editor asks about one item of a package (its
// signature, members, flows, errors, layout) and needs a short, exact answer
// for the cost of one process start.
//
// What: QueryNames, QueryAnswer (Text, JSON), Session.Query,
// Result.ParseFailed, the queryIndex
// that maps written names to items, the not-found suggestions, the module
// map and the find synonyms.
//
// How: a query is one Check of the package (a warm run is a module-cache hit
// on the interface store, frontcache.go), then an answer from the checked
// world. Each answer is capped near 200 tokens, enforced as QueryCapBytes at
// the usual 4 bytes per token of code.

package driver

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"mk/internal/diag"
	"mk/internal/types"
)

// QueryCapBytes is the answer budget: 200 tokens at 4 bytes per token.
const QueryCapBytes = 800

// QueryNames are the queries mk query answers, in the order doc/tools.md
// lists them, with the number of arguments each takes.
var QueryNames = []struct {
	Name string
	Args int
	Help string
}{
	{"members", 1, "members T: fields and methods with signatures"},
	{"find", 1, "find word: names matching a word or a synonym"},
	{"sig", 1, "sig Name: full signature with flows, error set and contracts"},
	{"import", 1, "import Name: the import line for an exported name"},
	{"modules", 0, "modules: the module map"},
	{"kind", 1, "kind T: plain, unique or linear, and why"},
	{"shape", 1, "shape T: size, alignment, ABI class, kind, carrier, opaque, Send, Share"},
	{"why-live", 2, "why-live F line: which holder keeps a loan live"},
	{"flows", 1, "flows F: automatic and declared sources"},
	{"errors", 1, "errors F: the error set"},
	{"callers", 1, "callers F: direct callers"},
	{"allocs", 1, "allocs F: a path to an allocator, or none"},
	{"panics", 1, "panics F: panic sites reachable from F"},
	{"stack", 1, "stack F: the stack bound, or the recursion that prevents one"},
	{"uses", 0, "uses: every std item the package references, with its sites (never capped)"},
	{"perimeter", 0, "perimeter: each package's local names, source, version and unsafe code (never capped)"},
}

// QueryAnswer is one answer: one fact per line. Truncated counts the lines
// the cap cut.
type QueryAnswer struct {
	Query     string   `json:"query"`
	Subject   string   `json:"subject"`
	Found     bool     `json:"found"`
	Answer    []string `json:"answer"`
	Truncated int      `json:"truncated"`
	// Partial is true when the package did not check as far as the types
	// phase (a parse or resolve error, or an internal error): the answer
	// comes from declarations alone, so signatures, shapes and bodies of the
	// package's items may be missing. The diagnostics are in the Result.
	Partial bool `json:"partial,omitempty"`
	// StdOnly is true when the package does not parse and the answer comes
	// from std's names alone (mk query's find and members).
	StdOnly bool `json:"std_only,omitempty"`
	// Uses is mk query uses's listing, nil for every other query; the
	// JSON holds "uses" whenever the query is uses, empty or not.
	Uses *[]UseEntry `json:"uses,omitempty"`
	// Perimeter is mk query perimeter's listing (deps_grants.go).
	Perimeter []PerimeterEntry `json:"perimeter,omitempty"`
}

// Text is the answer as lines, then "... N more" when the cap cut some.
func (a *QueryAnswer) Text() string {
	var b strings.Builder
	for _, l := range a.Answer {
		b.WriteString(l + "\n")
	}
	if a.Truncated > 0 {
		b.WriteString("... " + itoaQ(a.Truncated) + " more (capped at 200 tokens)\n")
	}
	if a.Partial {
		b.WriteString("(partial: the package does not check as far as types; run mk check)\n")
	}
	if a.StdOnly {
		b.WriteString("(the package does not parse: these are std's names alone; run mk check)\n")
	}
	return b.String()
}

// JSON is the answer as one object and a newline.
func (a *QueryAnswer) JSON() []byte {
	doc := *a
	if doc.Answer == nil {
		doc.Answer = []string{}
	}
	// No HTML escaping: an answer's "->" and "<" stay readable.
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(doc) // the object and a newline
	return b.Bytes()
}

// capLines keeps the lines that fit QueryCapBytes, always the first.
func (a *QueryAnswer) capLines(lines []string) {
	n := 0
	for i, l := range lines {
		n += len(l) + 1
		if i > 0 && n > QueryCapBytes {
			a.Answer, a.Truncated = lines[:i], len(lines)-i
			return
		}
	}
	a.Answer = lines
}

// Query runs one query over the package (Check first, so a query costs one
// process start and a warm query is a cache hit). An unknown query or a
// wrong argument count is a *UsageError. When the package does not reach
// the types phase the answer is nil and the Result holds why.
func (s *Session) Query(q string, args []string) (*QueryAnswer, Result, error) {
	n := -1
	for _, d := range QueryNames {
		if d.Name == q {
			n = d.Args
		}
	}
	if n < 0 {
		return nil, Result{}, usagef("unknown query %q", q)
	}
	if len(args) != n {
		return nil, Result{}, usagef("query %s takes %d argument(s), got %d", q, n, len(args))
	}
	if q == "uses" || q == "callers" {
		// Both read the package's use sites, which a module loaded whole
		// from the module cache does not keep (lspapi.go).
		s.cfg.KeepSyntax = true
	}
	s.fullStd = true
	res := s.Check()
	if q == "perimeter" { // from the manifest step alone, a missing grant included
		return perimeterAnswer(s), res, nil
	}
	if s.w == nil || s.w.sem5 == nil || s.stdB == nil {
		return nil, res, nil
	}
	qx := newQueryIndexOf(s, true)
	return answerQuery(qx, q, args, !checkedToTypes(res)), res, nil
}

// ParseFailed reports whether the check stopped at a parse error, where
// mk query answers std's names alone.
func (r Result) ParseFailed() bool {
	return r.Report.HasPhase && r.Report.Phase == diag.PhaseParse
}

// qEntry is one queryable item.
type qEntry struct {
	id     types.ItemID
	name   string // the display name: Name, Type.Name or str.Len
	mod    string // module path
	std    bool
	module *modNode
}

// queryIndex is the visible items of the package and std: every item of the
// package's modules, std's exported items outside std-internal modules, and
// the prelude's names and their methods; under Config.BrowseStd every std item.
type queryIndex struct {
	s       *Session
	w       *world
	entries []qEntry                    // in module, then key order
	calls   map[types.ItemID][]callSite // callsOf's memo (query_body.go)
	// local maps a dependency's canonical id to the root's local name for
	// it, for modPath; nil without dependency items.
	local map[string]string
}

// modPath spells module path p as the root imports it: a dependency's
// canonical id, the path's first segment, becomes the root's local name.
// Only a module path is rewritten, never text the source holds (a
// signature, a contract, a doc comment), whose words are local already.
func (qx *queryIndex) modPath(p string) string {
	first, rest, ok := strings.Cut(p, ".")
	name := qx.local[first]
	switch {
	case name == "":
		return p
	case !ok:
		return name
	}
	return name + "." + rest
}

func newQueryIndex(s *Session) *queryIndex { return newQueryIndexOf(s, false) }

// newQueryIndexOf is newQueryIndex; with deps, the exported items of the
// root's direct dependencies join it, outside their internal modules, for
// mk query and mk lsp alone (mk apidiff and mk doc read the package's own
// surface).
func newQueryIndexOf(s *Session, deps bool) *queryIndex {
	qx := &queryIndex{s: s, w: s.w}
	prelude := map[string]bool{}
	for _, n := range preludeNames {
		prelude[n] = true
	}
	add := func(m *modNode, std, dep bool) {
		if m.mod == nil {
			return
		}
		// Config.BrowseStd: std is the code being read, so every item of it
		// is visible, an internal module's too.
		browse := std && s.cfg.BrowseStd
		internal := std && isStdInternal(m.path) && !browse
		if m.path == "std.raw" && s.pkgMan != nil && len(s.pkgMan.UnsafeModules) > 0 {
			// A package with unsafe_modules may import std.raw, so its
			// names answer.
			internal = false
		}
		core := std && m.path == preludeModule
		for _, id := range m.mod.Items {
			it := qx.w.items.Get(id)
			switch it.Kind {
			case types.KindImpl, types.KindTest, types.KindGen, types.KindLiteral, types.KindNone:
				continue
			}
			if (std && !browse || dep) && !it.Exported {
				continue
			}
			name := qx.displayName(it)
			if name == "" {
				continue
			}
			if head, _, _ := strings.Cut(name, "."); internal && !(core && (prelude[head] || it.OwnerPrim != types.NoType)) {
				continue
			}
			qx.entries = append(qx.entries, qEntry{id: id, name: name, mod: m.path, std: std, module: m})
		}
	}
	if s.pkgB != nil {
		for _, m := range s.pkgB.mods {
			add(m, false, false)
		}
		// A dependency's exported items; an answer names their modules
		// by the root's local name (modPath).
		if deps && len(s.depRoot) > 0 {
			qx.local = s.displayNames(nil)
		}
		direct := map[string]bool{} // the cids the root names
		for _, p := range s.depRoot {
			direct[p.cid+"."] = true
		}
		for _, db := range s.depBs {
			if !deps || !direct[db.prefix] {
				continue // a C import's build, or a package the root does not name
			}
			for _, m := range db.mods {
				if !slices.Contains(strings.Split(m.dir, "/"), "internal") {
					add(m, false, true)
				}
			}
		}
	} else if s.w != nil {
		// A single file or a directory session: its own build is the
		// session's non-std modules.
		for _, m := range s.nonStdMods() {
			add(m, false, false)
		}
	}
	for _, m := range s.stdB.mods {
		add(m, true, false)
	}
	qx.addAliasMethods()
	return qx
}

// addAliasMethods lists, under an exported std alias of a std.core type
// that is no prelude name (text.Capped, text.Sink, time.Duration), the
// target's exported methods, named as the alias's module spells them:
// Capped.Writer in std.text. std.core is std-internal, so its methods are
// otherwise no entry, and the alias is how a program reaches them.
func (qx *queryIndex) addAliasMethods() {
	n := len(qx.entries)
	for i := 0; i < n; i++ {
		e := qx.entries[i]
		if !e.std || qx.w.items.Get(e.id).Kind != types.KindAlias {
			continue
		}
		target := qx.aliasTarget(e.id)
		if target == types.NoItem {
			continue
		}
		t := qx.w.items.Get(target)
		name := qx.w.in.Str(t.Name)
		if t.Key != types.ItemKey(preludeModule, name) || slices.Contains(preludeNames, name) {
			continue
		}
		for j := 1; j < qx.w.items.NumItems(); j++ {
			m := qx.w.items.Get(types.ItemID(j))
			if m.Owner != target || !m.Exported || m.Kind != types.KindFunc {
				continue
			}
			qx.entries = append(qx.entries, qEntry{id: types.ItemID(j), name: e.name + "." + qx.w.in.Str(m.Name), mod: e.mod, std: true, module: e.module})
		}
	}
}

// aliasTarget is the named type an alias item stands for, or NoItem.
func (qx *queryIndex) aliasTarget(id types.ItemID) types.ItemID {
	d, ok := qx.w.items.Get(id).Decl.(*types.AliasDecl)
	if !ok || d.Type == types.NoType {
		return types.NoItem
	}
	if ty := qx.w.tab.Get(d.Type); ty.Kind == types.KNamed {
		return ty.Item
	}
	return types.NoItem
}

// nonStdMods are the modules of a session without a package build (a file
// or directory root), found through the semantics' module table.
func (s *Session) nonStdMods() []*modNode {
	var out []*modNode
	if s.w.sem5 == nil {
		return nil
	}
	for _, m := range s.w.sem5.mods {
		if !m.std {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// displayName is Name, Owner.Name or prim.Name; "" for an impl method,
// which members reaches through its impl.
func (qx *queryIndex) displayName(it *types.Item) string {
	name := qx.w.in.Str(it.Name)
	switch {
	case it.OwnerPrim != types.NoType:
		return qx.w.tab.TypeString(it.OwnerPrim, nil) + "." + name
	case it.Owner != types.NoItem:
		o := qx.w.items.Get(it.Owner)
		if o.Kind == types.KindImpl || o.Kind == types.KindFunc {
			return ""
		}
		return qx.w.in.Str(o.Name) + "." + name
	}
	return name
}

// lookup finds a subject: an exact display name (Map.Get), the same after a
// module qualifier (fs.ReadText, std.fs.ReadText), then case-folded; the
// package before std.
func (qx *queryIndex) lookup(subject string) (qEntry, bool) {
	if e, ok := qx.match(subject, false); ok {
		return e, true
	}
	return qx.match(subject, true)
}

// match is lookup's one pass: exact, or case-folded when fold is set.
func (qx *queryIndex) match(subject string, fold bool) (qEntry, bool) {
	for pass := 0; pass < 2; pass++ {
		for _, e := range qx.entries {
			if e.std != (pass == 1) {
				continue
			}
			for _, cand := range []string{e.name, e.mod + "." + e.name, lastSeg(e.mod) + "." + e.name} {
				if cand == subject || (fold && strings.EqualFold(cand, subject)) {
					return e, true
				}
			}
		}
	}
	return qEntry{}, false
}

// typeNamed finds, when the subject's lookup e is a function (std.derive's
// Eq), a type the subject names as well (the prelude interface Eq): the
// type is what `members` answers for. The package comes before std.
func (qx *queryIndex) typeNamed(subject string, e qEntry) (qEntry, bool) {
	if !e.std || qx.w.items.Get(e.id).Kind != types.KindFunc || qx.funcSig(e.id) == nil {
		return qEntry{}, false
	}
	for pass := 0; pass < 2; pass++ {
		for _, x := range qx.entries {
			if x.std != (pass == 1) {
				continue
			}
			it := qx.w.items.Get(x.id)
			if it.Kind == types.KindFunc || it.Owner != types.NoItem || it.OwnerPrim != types.NoType {
				continue
			}
			for _, cand := range []string{x.name, x.mod + "." + x.name, lastSeg(x.mod) + "." + x.name} {
				if cand == subject {
					return x, true
				}
			}
		}
	}
	return qEntry{}, false
}

func lastSeg(p string) string {
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// universeSigs are the signatures of the universe functions that the
// language reference spells and no source declares:
// `Panic(msg: str): never`.
var universeSigs = map[string]string{
	"Panic": "Panic(msg: str): never",
}

// notFound is the answer for an unknown subject: up to five nearest names
// by folded edit distance.
func (qx *queryIndex) notFound(subject string) []string {
	type cand struct {
		name string
		d    int
	}
	var cs []cand
	seen := map[string]bool{}
	low := strings.ToLower(subject)
	for _, e := range qx.entries {
		if seen[e.name] {
			continue
		}
		seen[e.name] = true
		cs = append(cs, cand{e.name, editDistance(low, strings.ToLower(e.name))})
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].d != cs[j].d {
			return cs[i].d < cs[j].d
		}
		return cs[i].name < cs[j].name
	})
	out := []string{"no item named " + subject}
	var near []string
	for i := 0; i < len(cs) && i < 5; i++ {
		near = append(near, cs[i].name)
	}
	if len(near) > 0 {
		out = append(out, "nearest: "+strings.Join(near, ", "))
	}
	return out
}

// editDistance is Levenshtein's distance over bytes.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := prev[j-1]
			if a[i-1] != b[j-1] {
				c++
			}
			cur[j] = min(c, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// modules is the module map: the package's modules, each with its count of
// exported items, then std's importable ones, then, when any is not Stable,
// their classes on a line of their own. The `std:` line holds module paths
// only: the LSP reads each of its fields as one.
func (qx *queryIndex) modules() []string {
	var out []string
	count := func(m *modNode) int {
		n := 0
		for _, id := range m.mod.Items {
			if it := qx.w.items.Get(id); it.Exported && it.Owner == types.NoItem && it.Kind != types.KindImpl {
				n++
			}
		}
		return n
	}
	var pkg []*modNode
	if qx.s.pkgB != nil {
		pkg = qx.s.pkgB.mods
	} else {
		pkg = qx.s.nonStdMods()
	}
	for _, m := range pkg {
		if m.mod != nil {
			out = append(out, m.path+" ("+itoaQ(len(m.files))+" files, "+itoaQ(count(m))+" exported)")
		}
	}
	var std, classes []string
	for _, m := range qx.s.stdB.mods {
		if m.mod == nil || isStdInternal(m.path) {
			continue
		}
		std = append(std, m.path)
	}
	slices.Sort(std)
	for _, p := range std {
		if c, _ := moduleClass(p); c != ClassStable {
			classes = append(classes, p+" "+c)
		}
	}
	out = append(out, "prelude: "+preludeModule+" (no import)")
	out = append(out, "std: "+strings.Join(std, " "))
	if len(classes) > 0 {
		out = append(out, "std classes: "+strings.Join(classes, ", "))
	}
	return out
}

// synonyms maps a word an agent from another language reaches for to the
// Mk spellings it should find (len, size, append, HashMap). Keys and values
// are lower case.
var synonyms = map[string][]string{
	"len": {"len", "count"}, "length": {"len"}, "size": {"len", "sizeof"}, "count": {"len", "count"},
	"append": {"push", "add"}, "push": {"push"}, "add": {"push", "add", "insert"}, "insert": {"insert", "set", "add"},
	"hashmap": {"map", "heapmap"}, "dict": {"map", "heapmap"}, "dictionary": {"map"}, "hashset": {"set"},
	"vec": {"list", "heaplist"}, "vector": {"list", "heaplist"}, "arraylist": {"list", "heaplist"}, "array": {"list", "slice"},
	"string": {"str", "text"}, "println": {"print"}, "printf": {"print"}, "format": {"fmt", "format", "print"},
	"contains": {"has", "contains", "find"}, "indexof": {"find", "index"}, "split": {"split", "fields", "lines"},
	"remove": {"remove", "delete", "pop"}, "delete": {"remove", "delete"}, "get": {"get", "index"},
	"sort": {"sort", "sortstable"}, "mutex": {"mutex"}, "lock": {"mutex", "lock"}, "channel": {"chan"},
	"thread": {"scope", "go"}, "spawn": {"go", "scope"}, "sleep": {"sleep"}, "open": {"open", "readtext", "readbytes"},
	"readfile": {"readtext", "readbytes"}, "writefile": {"writetext", "writebytes"}, "error": {"error", "fail"},
	"unwrap": {"must"}, "expect": {"must"}, "option": {"some", "none"}, "box": {"box"}, "free": {"free"}, "malloc": {"alloc", "heap"},
	"parse": {"parse", "parseint", "parsefloat"}, "atoi": {"parseint"}, "tostring": {"format", "fmt"},
}

// find lists the names whose last segment matches the word or one of its
// synonyms (folded; exact matches first), then the top-level names of a
// module the word names, then partial matches, with their kind and module.
func (qx *queryIndex) find(word string) []string {
	w := strings.ToLower(strings.TrimSpace(word))
	words := append([]string{w}, synonyms[w]...)
	var exact, module, part []string
	seen := map[string]bool{}
	for _, e := range qx.entries {
		last := strings.ToLower(lastSeg(e.name))
		it := qx.w.items.Get(e.id)
		line := e.name + "  " + it.Kind.String() + "  " + qx.modPath(e.mod)
		if seen[line] {
			continue
		}
		// A word that names a module, `find csv`, lists that module's
		// top-level names after the exact matches.
		if strings.ToLower(lastSeg(e.mod)) == w && it.Owner == types.NoItem && it.OwnerPrim == types.NoType {
			module = append(module, line)
			seen[line] = true
			continue
		}
		for _, x := range words {
			if last == x {
				exact = append(exact, line)
				seen[line] = true
				break
			}
			if len(x) >= 3 && strings.Contains(last, x) {
				part = append(part, line)
				seen[line] = true
				break
			}
		}
	}
	// The compiler's members of `?T` and `E!T`, which no source declares.
	for _, b := range builtinMembers {
		if slices.Contains(words, strings.ToLower(lastSeg(b))) {
			exact = append(exact, b+"  builtin  "+preludeModule)
		}
	}
	return append(append(exact, module...), part...)
}

// builtinMembers are the members the compiler gives `?T` and `E!T` (no
// source declares them), as find lists them.
var builtinMembers = []string{"?T.IsNone", "?T.IsSome", "?T.Must", "E!T.Must"}

func itoaQ(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
