// Why: a warm build should not parse files it has seen. Everything the
// build reads of a file before stage 3 (the import step, the catalog, the
// Main rule, the module cache's anchors) fits in a parse summary, so a
// stored summary can stand in for the parse.
//
// What: parse summaries and their per-module parse indexes: parseIndex,
// parseIndexKey, parseIndexOf, releaseParseIndexes, updateParseIndex,
// loadSummary, storeParseIndexes, the summary builders (unsafeSites,
// itemAnchors, shareTokens) and ensureParsed, the lazy parse of a module
// that missed.
//
// How: a file whose summary is stored under (build ID, module, path,
// content, parse options) is not parsed in the parse step, and is parsed
// only when its module misses its interface lookup (ensureParsed, at the
// start of stage 3). A summary is stored only for a file that parsed with
// no diagnostic, so a lazy parse reports nothing the cold parse would not
// have. MK_CACHE_VERIFY parses every file and compares the summary's
// encoding with the stored one.
//
// The summaries are stored one cache file per module, not one per file (one
// file per summary was about half the files a cold dev build wrote): a
// module's parse index is one KindManifest entry keyed by the build ID, the
// module path and the module's directory, so a target selection gets one
// index per directory (parseIndexKey), holding each summary under its own
// per-file key (ParseKey, unchanged), so a hit is still per file and still
// checked against the file's content. The parse step reads the index once
// per module and rewrites it once per module that parsed a file it could
// store, after the parse: the files of this build get the index's next
// generation, and the index keeps the entries of its last
// parseIndexGenerations generations, this one included; older ones are
// dropped. So an edit undone soon after it, or up to that many programs with
// a module of the same path in one cache (every package's root module is
// main; tools/bench's rows share one cache), hit as long as fewer than
// parseIndexGenerations rewrites of the index came in between; past that one
// misses and reparses (with one previous generation, three such programs
// reparsed on every warm check). A lost update between processes is a later
// miss too, never a wrong hit.

package driver

import (
	"fmt"
	"slices"
	"strings"

	"mk/internal/ast"
	"mk/internal/cache"
	"mk/internal/diag"
	"mk/internal/iface"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/syntax"
)

// parseSummaryKey is f's summary key: the parse options are part of the
// content, since AllowBodiless changes what a file parses to.
func (fc *frontCache) parseSummaryKey(w *world, m *modNode, f *fileNode) source.Hash {
	k, _ := fc.parseSummaryKeyOf(w, m, f)
	return k
}

// parseSummaryKeyOf is parseSummaryKey and the file's content hash
// (mk.src.v1), which loadSummary compares with the stored summary's.
func (fc *frontCache) parseSummaryKeyOf(w *world, m *modNode, f *fileNode) (source.Hash, source.Hash) {
	content := source.HashString("mk.src.v1", f.src.Text)
	c := source.NewHasher("mk.driver.parse.v1")
	c.Hash(content)
	c.Bool(f.bodiless)
	return iface.ParseKey(w.buildID(), m.path, f.src.Path, c.Sum()), content
}

// parseIndex is one module's stored parse summaries: the encodings by
// per-file key, each with the generation that last wrote it.
type parseIndex struct {
	gen     uint64
	entries map[source.Hash]parseEntry
}

type parseEntry struct {
	gen uint64
	enc []byte // iface.EncodeParseSummary
}

const parseIndexVersion = 1

// parseIndexGenerations is how many generations a parse index keeps: an
// entry written by generation g stays through the rewrite that makes
// generation g+parseIndexGenerations-1 and goes with the next one. It is
// iface.MaxCandidates' bound, a module manifest's candidates, for the same
// reason: several programs or edits in turn share one entry.
const parseIndexGenerations = 8

// parseIndexKey is the KindManifest key of module m's parse index: the build
// ID, the module path and the module's directory as the source map names it
// (std/sys/windows, gen/x86_64-linux/c/, util). A target selection gives one
// module path a directory per target (std/pkg.mk's targets: std.sys has
// four; a C import one per target), and with one index per path, checks for
// three targets in turn rewrote it each time and reparsed std.sys forever.
// Like a summary's key it is target- and profile-free (the directory is what
// a target chose, not the target), and holds no root path: a summary's key
// already hashes the file's source-map path, which is the same directory's.
func parseIndexKey(w *world, m *modNode) source.Hash {
	h := source.NewHasher("mk.driver.parseidx.v1")
	h.Hash(w.buildID())
	h.String(m.path)
	if m.tree != nil {
		h.String(m.tree.path(m.dir))
	}
	return h.Sum()
}

// encodeParseIndex is the payload of x: the entries sorted by key.
func encodeParseIndex(x *parseIndex) []byte {
	keys := make([]source.Hash, 0, len(x.entries))
	for k := range x.entries {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b source.Hash) int { return a.Compare(b) })
	var e iface.Encoder
	e.Uvarint(parseIndexVersion)
	e.Uvarint(x.gen)
	e.Uvarint(uint64(len(keys)))
	for _, k := range keys {
		en := x.entries[k]
		e.Hash(k)
		e.Uvarint(en.gen)
		e.Bytes(en.enc)
	}
	return e.Out()
}

func decodeParseIndex(b []byte) (*parseIndex, error) {
	d := iface.NewDecoder(b)
	if v := d.Uvarint(); d.Err == nil && v != parseIndexVersion {
		d.Failf("parse index version %d", v)
	}
	x := &parseIndex{gen: d.Uvarint(), entries: map[source.Hash]parseEntry{}}
	n := d.Count(34)
	for i := 0; i < n && d.Err == nil; i++ {
		k := d.Hash()
		g := d.Uvarint()
		enc := d.Bytes()
		x.entries[k] = parseEntry{gen: g, enc: enc}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	return x, nil
}

// readParseIndex reads m's parse index from the store; an absent or
// corrupt one is empty.
func (fc *frontCache) readParseIndex(w *world, m *modNode) *parseIndex {
	if raw, ok, _ := fc.st.Get(cache.KindManifest, parseIndexKey(w, m)); ok {
		if x, err := decodeParseIndex(raw); err == nil {
			return x
		}
	}
	return &parseIndex{entries: map[source.Hash]parseEntry{}}
}

// parseIndexOf is m's parse index as the store held it when this build
// first asked (one read per module and build); empty once the parse step
// released it (releaseParseIndexes).
func (m *modNode) parseIndexOf(fc *frontCache, w *world) *parseIndex {
	m.pidxOnce.Do(func() { m.pidx = fc.readParseIndex(w, m) })
	if m.pidx == nil {
		return &parseIndex{entries: map[source.Hash]parseEntry{}}
	}
	return m.pidx
}

// releaseParseIndexes drops the parse indexes the parse step read for
// mods: nothing reads one after it (only parseAll calls loadSummary), and
// an index holds every entry's encoding, other targets' and the previous
// generation's included, which would otherwise stay alive through codegen
// and the link (release.go's rule for front-end output).
func releaseParseIndexes(mods []*modNode) {
	for _, m := range mods {
		m.pidx = nil
	}
}

// updateParseIndex rewrites m's parse index after the parse step: current are
// the summaries of m's files this build could store (hits and clean parses), by
// key. The index is read again, so a concurrent update is merged as far as it
// landed; an older entry stays while it is one of the index's last
// parseIndexGenerations generations.
func (fc *frontCache) updateParseIndex(w *world, m *modNode, current map[source.Hash][]byte) {
	old := fc.readParseIndex(w, m)
	gen := old.gen + 1
	nx := &parseIndex{gen: gen, entries: make(map[source.Hash]parseEntry, len(current))}
	for k, enc := range current {
		nx.entries[k] = parseEntry{gen: gen, enc: enc}
	}
	for k, en := range old.entries {
		if _, ok := nx.entries[k]; !ok && en.gen+parseIndexGenerations > gen {
			nx.entries[k] = en
		}
	}
	_ = fc.st.Put(cache.KindManifest, parseIndexKey(w, m), encodeParseIndex(nx))
}

// loadSummary gives f, a file of m, its stored summary from m's parse index;
// false on a miss, a corrupt entry or a summary of another file or of other
// content (content is f's mk.src.v1 hash: the index's frame checks only the
// index's own key, so the summary is checked against the file here). A
// summary that decodes but fails that check is still returned as its
// encoding, with false: under MK_CACHE_VERIFY parseAll compares it with the
// fresh parse, so a summary stored under another file's key is E0000 `cache
// verify`, not a silent miss.
func (fc *frontCache) loadSummary(key, content source.Hash, w *world, m *modNode, f *fileNode) (*iface.ParseSummary, []byte, bool) {
	en, ok := m.parseIndexOf(fc, w).entries[key]
	if !ok {
		return nil, nil, false
	}
	b := en.enc
	p, err := iface.DecodeParseSummary(b)
	if err != nil {
		return nil, nil, false
	}
	if p.ContentHash != content || p.FileAnchor.Path != f.src.Path || !p.Bind(w.sm) {
		return nil, b, false
	}
	return p, b, true
}

// summarize builds f's parse summary from its AST.
func (w *world) summarize(f *fileNode) *iface.ParseSummary {
	text := f.src.Text
	p := &iface.ParseSummary{ContentHash: source.HashString("mk.src.v1", text)}
	p.FileAnchor = iface.Anchor{File: f.src.ID, Path: f.src.Path, TokenHash: syntax.FileHash(f.ast, text)}
	// The token lists are read only by the module cache (its anchors, and
	// the summary it stores); with the cache off they are not built, and
	// nothing reads a summary's Items.
	if w.front != nil {
		for _, t := range f.ast.Tokens {
			if t.Kind.IsTrivia() || t.Kind == ast.TokEOF {
				continue
			}
			addTok(&p.FileAnchor, t)
		}
		p.Items = itemAnchors(f, &p.FileAnchor)
	}
	var pairs [][2]string
	for _, il := range resolve.ImportsOf(f.ast, w.in) {
		b := w.in.Str(il.Binding)
		pairs = append(pairs, [2]string{b, il.Path})
		p.Imports = append(p.Imports, iface.ParsedImport{Binding: b, Path: il.Path, Node: uint32(il.Node),
			Start: il.At.Start, End: il.At.End, PathStart: il.PathSpan.Start, PathEnd: il.PathSpan.End})
	}
	p.ImportsHash = iface.ImportsHash(pairs)
	p.Main = declaresMain(f.ast, w.in)
	for _, it := range f.ast.Items {
		if n := exportedName(it, w.in); n != "" {
			p.Exports = append(p.Exports, n)
		}
	}
	p.Unsafe = unsafeSites(f)
	return p
}

// addTok adds token t to anchor a. An empty token (the implicit
// terminator before a "}" on the same line, std/derive/derive.mk) shares
// its start with the next token, and an encoded anchor's starts strictly
// increase (iface readAnchor), so it is left out, as is any token that
// does not start after the last one kept; a span at its offset locates by
// the next token.
func addTok(a *iface.Anchor, t ast.Token) {
	if n := len(a.Starts); t.Len == 0 || n > 0 && t.Start <= a.Starts[n-1] {
		return
	}
	a.Starts = append(a.Starts, t.Start)
	a.Ends = append(a.Ends, t.End())
}

// shareTokens points a's token lists into fa's when fa holds the same run
// of tokens, so the item's copy can be collected.
func shareTokens(a, fa *iface.Anchor) {
	n := len(a.Starts)
	lo, ok := slices.BinarySearch(fa.Starts, a.Starts[0])
	if !ok || lo+n > len(fa.Starts) || !slices.Equal(fa.Starts[lo:lo+n], a.Starts) || !slices.Equal(fa.Ends[lo:lo+n], a.Ends) {
		return
	}
	a.Starts, a.Ends = fa.Starts[lo:lo+n:lo+n], fa.Ends[lo:lo+n:lo+n]
}

// importLines are f's import lines (resolve.ImportsOf), from its summary.
func (w *world) importLines(f *fileNode) []resolve.ImportLine {
	if f.sum == nil {
		return nil
	}
	out := make([]resolve.ImportLine, 0, len(f.sum.Imports))
	for _, im := range f.sum.Imports {
		out = append(out, resolve.ImportLine{Binding: w.in.Intern(im.Binding), Path: im.Path, Node: ast.NodeID(im.Node),
			At:       source.Span{File: f.src.ID, Start: im.Start, End: im.End},
			PathSpan: source.Span{File: f.src.ID, Start: im.PathStart, End: im.PathEnd}})
	}
	return out
}

// unsafeSites are f's unsafe blocks and unsafe impls, each at its unsafe
// keyword, in ast.Inspect order (the import step's E1102).
func unsafeSites(f *fileNode) []iface.ParsedUnsafe {
	if !slices.ContainsFunc(f.ast.Tokens, func(t ast.Token) bool { return t.Kind == ast.TokUnsafe }) {
		return nil
	}
	var out []iface.ParsedUnsafe
	ast.Inspect(f.ast, func(n ast.Node) bool {
		impl := false
		switch x := n.(type) {
		case *ast.UnsafeStmt:
		case *ast.ImplDecl:
			if !x.Unsafe {
				return true
			}
			impl = true
		default:
			return true
		}
		at := unsafeKeyword(f, n.Span())
		out = append(out, iface.ParsedUnsafe{Start: at.Start, End: at.End, Impl: impl})
		return true
	})
	return out
}

// itemAnchors gives every top-level item of f its own anchor (its
// non-trivia tokens, its token hash syntax.ItemHash), so a span inside an
// item is stored relative to that item's tokens: an edit of another item of
// the file, or a line added above, leaves it valid.
// The anchor key is "item:<path>#<head>", head being the item's first
// tokens up to its first "(", "{", "=" or ":" (its kind and name: "F",
// "export F", "struct S[T]", "impl Show for T"), with "#<n>" for the n-th
// repeat of a head in the file. The key only has to name the same item in
// two sessions; a renamed item is a new key, whose stored spans are stale.
//
// An item's token lists are a window of the file anchor's (fa) whenever
// they hold the same tokens, which is the rule: the item's non-trivia
// tokens are a contiguous run of the file's. The window is cut to its
// length, so nothing appends through it; the encoding is the same either
// way (copies of the anchors' lists would hold every token list twice).
func itemAnchors(f *fileNode, fa *iface.Anchor) []iface.ParsedItem {
	text := f.src.Text
	seen := map[string]int{}
	var out []iface.ParsedItem
	for _, it := range f.ast.Items {
		r := it.Info().Toks
		a := iface.Anchor{File: f.src.ID, Path: f.src.Path, TokenHash: syntax.ItemHash(f.ast, text, it)}
		var head []string
		stop := false
		for i := r.First; i < r.End && int(i) < len(f.ast.Tokens); i++ {
			t := f.ast.Tokens[i]
			if t.Kind.IsTrivia() || t.Kind == ast.TokEOF {
				continue
			}
			addTok(&a, t)
			if stop {
				continue
			}
			switch s := text[t.Start:t.End()]; {
			case s == "(" || s == "{" || s == "=" || s == ":" || len(head) == 24:
				stop = true
			default:
				head = append(head, s)
			}
		}
		if len(a.Starts) == 0 {
			continue
		}
		shareTokens(&a, fa)
		h := strings.Join(head, " ")
		seen[h]++
		k := "item:" + f.src.Path + "#" + h
		if n := seen[h]; n > 1 {
			k += "#" + fmt.Sprint(n)
		}
		out = append(out, iface.ParsedItem{Name: h, Key: iface.AnchorKey(k), Anchor: a})
	}
	return out
}

// parseFile parses f and builds its summary. With the cache on and no parse
// diagnostic it returns the summary's encoding for its module's parse
// index under key (a zero key: none), and put: the index must be rewritten
// for it (no stored summary, or under MK_CACHE_VERIFY a stored one that
// differs, which is reported and replaced).
func (b *build) parseFile(f *fileNode, key source.Hash, stored []byte) (ds []*diag.Diagnostic, enc []byte, put bool) {
	f.ast, ds = syntax.ParseFile(f.src, b.w.in, syntax.Options{AllowBodiless: f.bodiless})
	f.sum = b.w.summarize(f)
	fc := b.w.front
	if fc == nil || key.IsZero() || len(ds) > 0 {
		return ds, nil, false
	}
	enc = iface.EncodeParseSummary(f.sum)
	if stored != nil {
		if string(stored) == string(enc) {
			return ds, enc, false
		}
		fc.mismatchKind("parse", key, "summary of "+f.src.Path+" differs")
	}
	return ds, enc, true
}

// storeParseIndexes rewrites the parse index of every module of b that
// parsed a file it could store in the parse step (mods, keys, encs and put
// by file, as parseAll collected them; a nil enc is not stored), one
// module per worker.
func (b *build) storeParseIndexes(mods []*modNode, keys []source.Hash, encs [][]byte, put []bool) []*diag.Diagnostic {
	fc := b.w.front
	if fc == nil {
		return nil
	}
	type job struct {
		m       *modNode
		current map[source.Hash][]byte
		dirty   bool
	}
	var jobs []*job
	by := map[*modNode]*job{}
	for i, m := range mods {
		if encs[i] == nil {
			continue
		}
		j := by[m]
		if j == nil {
			j = &job{m: m, current: map[source.Hash][]byte{}}
			by[m] = j
			jobs = append(jobs, j)
		}
		j.current[keys[i]] = encs[i]
		j.dirty = j.dirty || put[i]
	}
	jobs = slices.DeleteFunc(jobs, func(j *job) bool { return !j.dirty })
	if len(jobs) == 0 {
		return nil
	}
	if fc.deferWrites {
		fc.writes("store parse indexes", func() {
			for _, j := range jobs {
				fc.updateParseIndex(b.w, j.m, j.current)
			}
		})
		return nil
	}
	return b.w.parallel(len(jobs), func(i int) string { return "store the parse index of " + jobs[i].m.path }, func(i int) {
		fc.updateParseIndex(b.w, jobs[i].m, jobs[i].current)
	})
}

// storeParseIndexesCold computes parse summary keys (joining async buildID)
// and writes the parse indexes for a cold cache build. Under fc.deferWrites
// both key computation and disk writes run in the background beside later
// stages; nothing in the current build reads parse indexes after parseAll.
func (b *build) storeParseIndexesCold(mods []*modNode, files []*fileNode, encs [][]byte, put []bool) []*diag.Diagnostic {
	fc := b.w.front
	if fc == nil {
		return nil
	}
	type item struct {
		m   *modNode
		f   *fileNode
		enc []byte
		put bool
	}
	var items []item
	anyDirty := false
	for i, m := range mods {
		if encs[i] == nil {
			continue
		}
		items = append(items, item{m: m, f: files[i], enc: encs[i], put: put[i]})
		if put[i] {
			anyDirty = true
		}
	}
	if !anyDirty {
		return nil
	}
	run := func() []*diag.Diagnostic {
		type job struct {
			m       *modNode
			current map[source.Hash][]byte
			dirty   bool
		}
		var jobs []*job
		by := map[*modNode]*job{}
		for _, it := range items {
			k := fc.parseSummaryKey(b.w, it.m, it.f)
			j := by[it.m]
			if j == nil {
				j = &job{m: it.m, current: map[source.Hash][]byte{}}
				by[it.m] = j
				jobs = append(jobs, j)
			}
			j.current[k] = it.enc
			j.dirty = j.dirty || it.put
		}
		jobs = slices.DeleteFunc(jobs, func(j *job) bool { return !j.dirty })
		if fc.deferWrites {
			for _, j := range jobs {
				fc.updateParseIndex(b.w, j.m, j.current)
			}
			return nil
		}
		return b.w.parallel(len(jobs), func(i int) string { return "store the parse index of " + jobs[i].m.path }, func(i int) {
			fc.updateParseIndex(b.w, jobs[i].m, jobs[i].current)
		})
	}
	if fc.deferWrites {
		fc.writes("store parse indexes", func() { _ = run() })
		return nil
	}
	return run()
}

// ensureParsed parses the files of m that the parse step left unparsed because
// their summaries hit (m missed its interface lookup). Called at the start of
// stage 3, on m's goroutine before it takes its worker slot: the files are
// parsed in parallel, one worker slot each (an edit build parses every file of
// the edited module but one here), and their diagnostics and the trace are
// appended in file order, as one at a time gave. A faulted parse is an E0000 of
// the module over an empty file, as parseAll's.
func (b *build) ensureParsed(m *modNode) {
	var todo []*fileNode
	for _, f := range m.files {
		if f.ast != nil || f.tooLarge || f.src == nil {
			continue
		}
		todo = append(todo, f)
	}
	if len(todo) == 0 {
		return
	}
	slots := make([][]*diag.Diagnostic, len(todo))
	faults := b.w.parallel(len(todo), func(i int) string { return "parse " + todo[i].src.Path }, func(i int) {
		f := todo[i]
		f.ast, slots[i] = syntax.ParseFile(f.src, b.w.in, syntax.Options{AllowBodiless: f.bodiless})
	})
	for i, f := range todo {
		m.diags = append(m.diags, slots[i]...)
		m.parsed = append(m.parsed, f.src.Path)
		if f.ast == nil { // the worker faulted before ParseFile returned
			f.ast = &ast.File{Base: ast.Base{NID: 1, Sp: source.Span{File: f.src.ID}}, Path: f.src.Path, NodeCount: 1}
		}
	}
	m.diags = append(m.diags, faults...)
}
