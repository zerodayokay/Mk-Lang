// Why: mk lsp needs exact navigation (declarations, uses, types at a
// position) without checking the package again on every request.
//
// What: the Session methods mk lsp calls: Items, Lookup, LookupExact,
// UsesOf, TypeAt and QueryChecked, with ItemInfo and the lspMemo that holds
// what they compute between Checks.
//
// How: everything is read from the session's last Check. Items and Lookup
// read the item table; UsesOf reads resolve's side tables and check's Uses;
// TypeAt reads check's Types. check keeps no unit's typed body after a
// Check (stages.go lowers it and drops it, and a unit reused from the
// function cache was never checked), so the typed body of a unit is
// checked again on first use here: check.Checker.Check over the module's
// check index, one unit, memoized until the next Check. That is a stage-6
// check of one body, never a Session.Check: no file is read, no module is
// scheduled, nothing is stored.
//
// A module loaded whole from the module cache has no syntax and no
// resolution in the session, so TypeAt answers false in its files and
// UsesOf finds no use in them; Config.KeepSyntax (mk lsp sets it) keeps
// every package module out of that path.

package driver

import (
	"cmp"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"mk/internal/ast"
	checker "mk/internal/check"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// ItemInfo is one declaration: an item, or a field or variant of one.
// Name is the declared name alone ("Sum"); Owner is the display name of the
// type it belongs to ("Point", "str"), "" for a top-level item, so Owner +
// "." + Name is the spelling Lookup and Query take. Kind is the item kind
// as `mk query find` prints it (struct, enum, error, interface, alias,
// const, global, func, test), or "field" or "variant". [Def, End) is the
// declared name's span (types.Item.Span: the name, or the keyword of an
// item without one), not the whole declaration; both are the zero Position
// for an item with no source (the synthesized Target).
type ItemInfo struct {
	Name, Kind, Module string
	Exported           bool
	Def, End           source.Position
	Owner              string
}

// lspMemo is what the answers computed over one world (one Check).
type lspMemo struct {
	w      *world
	qx     *queryIndex
	nodes  map[*ast.File][]ast.Node
	bodies map[types.ItemID]*checker.Body
	early  map[types.ItemID]*checker.Body
	uses   map[types.ItemID][]source.Position
}

// memo is the memo of the last Check, nil before any.
func (s *Session) memo() *lspMemo {
	if s.w == nil || s.p == nil {
		return nil
	}
	if s.lsp == nil || s.lsp.w != s.w {
		s.lsp = &lspMemo{w: s.w, nodes: map[*ast.File][]ast.Node{}, bodies: map[types.ItemID]*checker.Body{},
			uses: map[types.ItemID][]source.Position{}}
	}
	return s.lsp
}

// index is the query index of the last Check, nil when the check did not
// reach the semantics.
func (s *Session) index() *queryIndex {
	mm := s.memo()
	if mm == nil || s.w.sem5 == nil || s.stdB == nil {
		return nil
	}
	if mm.qx == nil {
		mm.qx = newQueryIndexOf(s, true) // with dependency items
	}
	return mm.qx
}

// fileAt is the session's file at path as Result.Sources spells it; on
// Windows a path that differs only in case or separators matches too.
func (s *Session) fileAt(path string) (*source.File, bool) {
	if s.w == nil {
		return nil, false
	}
	if f, ok := s.w.sm.ByPath(path); ok {
		return f, true
	}
	if runtime.GOOS != "windows" {
		return nil, false
	}
	want := filepath.ToSlash(filepath.Clean(path))
	for _, f := range s.w.sm.Files() {
		if strings.EqualFold(f.Path, want) {
			return f, true
		}
	}
	return nil, false
}

// allMods are the modules of every build of the last Check: std, the
// dependencies, the C imports and the package.
func (s *Session) allMods() []*modNode {
	var out []*modNode
	for _, b := range s.p.builds {
		out = append(out, b.mods...)
	}
	return out
}

// pkgMods are the package's own modules.
func (s *Session) pkgMods() []*modNode {
	if s.pkgB != nil {
		return s.pkgB.mods
	}
	return s.nonStdMods()
}

// Items are the declarations of the file at path (as Result.Sources spells
// it) in source order: its items, methods with their Owner, and the fields
// and variants of its types. Impls, gen lines, fn literals, contract
// predicates and invariants are not listed. Nil before a Check, for a file
// the session does not hold, or when the check stopped before the items
// existed (a parse error).
func (s *Session) Items(path string) []ItemInfo {
	if s.memo() == nil {
		return nil
	}
	f, ok := s.fileAt(path)
	if !ok {
		return nil
	}
	type at struct {
		off  uint32
		info ItemInfo
	}
	var rows []at
	for _, m := range s.allMods() {
		if m.mod == nil {
			continue
		}
		for _, id := range m.mod.Items {
			it := s.w.items.Get(id)
			if it.Span.File != f.ID || !listedItem(s.w, it) {
				continue
			}
			rows = append(rows, at{it.Span.Start, s.itemInfo(it)})
			owner := s.w.in.Str(it.Name)
			mod := m.path
			addField := func(fd types.FieldDecl, kind, own string) {
				if fd.Span.File != f.ID {
					return
				}
				rows = append(rows, at{fd.Span.Start, ItemInfo{Name: s.w.in.Str(fd.Name), Kind: kind, Module: mod,
					Exported: fd.Exported, Def: s.pos(fd.Span.Start, fd.Span.File), End: s.pos(fd.Span.End, fd.Span.File), Owner: own}})
			}
			switch d := it.Decl.(type) {
			case *types.StructDecl:
				for _, fd := range d.Fields {
					addField(fd, "field", owner)
				}
			case *types.ErrorDecl:
				for _, fd := range d.Fields {
					addField(fd, "field", owner)
				}
			case *types.EnumDecl:
				for _, v := range d.Variants {
					addField(types.FieldDecl{Name: v.Name, Span: v.Span, Exported: it.Exported}, "variant", owner)
					for _, fd := range v.Fields {
						addField(fd, "field", owner+"."+s.w.in.Str(v.Name))
					}
				}
			}
		}
	}
	slices.SortStableFunc(rows, func(a, b at) int { return cmp.Compare(a.off, b.off) })
	out := make([]ItemInfo, len(rows))
	for i, r := range rows {
		out[i] = r.info
	}
	return out
}

// listedItem reports whether Items lists it: a named declaration of the
// source, never an impl, a gen line, a fn literal, a contract predicate or
// an invariant (whose owners are functions and structs and whose names are
// not the source's).
func listedItem(w *world, it *types.Item) bool {
	switch it.Kind {
	case types.KindImpl, types.KindGen, types.KindLiteral, types.KindNone:
		return false
	}
	if it.Span.File == source.NoFile || it.Origin != nil {
		return false
	}
	if it.Owner != types.NoItem {
		switch o := w.items.Get(it.Owner); o.Kind {
		case types.KindImpl, types.KindStruct, types.KindEnum, types.KindError, types.KindInterface, types.KindAlias:
			// A method, of a type or of an impl; an invariant has no name.
			return it.Kind == types.KindFunc && w.in.Str(it.Name) != ""
		default:
			return false // a contract predicate of a function
		}
	}
	return true
}

// pos is offset off of file id as a Position.
func (s *Session) pos(off uint32, id source.FileID) source.Position {
	if id == source.NoFile {
		return source.Position{}
	}
	return s.w.sm.File(id).Position(off)
}

// itemInfo is it's row.
func (s *Session) itemInfo(it *types.Item) ItemInfo {
	name := s.w.in.Str(it.Name)
	if name == "" && it.Kind == types.KindTest {
		name = it.Key.String()
	}
	mod := ""
	if m := s.w.items.Module(it.Module); m != nil {
		mod = m.Path
	}
	return ItemInfo{Name: name, Kind: it.Kind.String(), Module: mod, Exported: it.Exported,
		Def: s.pos(it.Span.Start, it.Span.File), End: s.pos(it.Span.End, it.Span.File), Owner: s.ownerName(it)}
}

// ownerName is the display name of the type a method belongs to: its
// owner type, an impl's type, or a primitive; "" for a top-level item.
func (s *Session) ownerName(it *types.Item) string {
	if it.OwnerPrim != types.NoType {
		return s.w.tab.TypeString(it.OwnerPrim, nil)
	}
	if it.Owner == types.NoItem {
		return ""
	}
	o := s.w.items.Get(it.Owner)
	if d, ok := o.Decl.(*types.ImplDecl); ok && o.Kind == types.KindImpl {
		return s.w.tab.TypeString(d.For, d.Generics)
	}
	return s.w.in.Str(o.Name)
}

// Lookup is `mk query`'s resolution of a name (queryIndex.lookup: Map.Get,
// fs.ReadText, std.fs.ReadText, then case-folded; the package before std)
// with its declaration, std items included. False before a Check, when the
// check stopped before the semantics, or when nothing has the name.
func (s *Session) Lookup(name string) (ItemInfo, bool) {
	qx := s.index()
	if qx == nil {
		return ItemInfo{}, false
	}
	e, ok := qx.lookup(name)
	if !ok {
		return ItemInfo{}, false
	}
	return s.itemInfo(s.w.items.Get(e.id)), true
}

// LookupExact is Lookup without the case-folded pass: an editor's
// subject is the text at the cursor, so `out.Len` (a local's method) is
// never std.tls's `Out.Len`.
func (s *Session) LookupExact(name string) (ItemInfo, bool) {
	qx := s.index()
	if qx == nil {
		return ItemInfo{}, false
	}
	e, ok := qx.match(name, false)
	if !ok {
		return ItemInfo{}, false
	}
	return s.itemInfo(s.w.items.Get(e.id)), true
}

// UsesOf are the resolved use sites of the item Lookup(name) finds, in the
// package's own modules (and std's under Config.BrowseStd), sorted by path
// and position; the declaration is not one. Each position is the start of
// the name at the use (the last segment that names the item: fs.ReadText's
// ReadText). A name resolve binds (a type, a function, a const, a
// module-qualified name) is read from resolve's side tables; a method, which
// check resolves (x.Sum(), Point.New()), from the Uses of every checked
// body. Nil when the name is unknown or before a Check.
func (s *Session) UsesOf(name string) []source.Position {
	qx := s.index()
	if qx == nil {
		return nil
	}
	e, ok := qx.lookup(name)
	if !ok {
		return nil
	}
	mm := s.memo()
	if out, ok := mm.uses[e.id]; ok {
		return out
	}
	it := s.w.items.Get(e.id)
	seen := map[source.Pos]bool{}
	var found []source.Pos
	add := func(sp source.Span) {
		p := sp.StartPos()
		if sp.File != source.NoFile && !seen[p] && sp != it.Span {
			seen[p] = true
			found = append(found, p)
		}
	}
	owned := it.Owner != types.NoItem || it.OwnerPrim != types.NoType
	mods := s.pkgMods()
	if s.cfg.BrowseStd && s.stdB != nil {
		// std is the code being read: its uses count.
		mods = append(append([]*modNode(nil), mods...), s.stdB.mods...)
	}
	for _, m := range mods {
		if m.info == nil {
			continue
		}
		for _, fi := range m.info.Files {
			nodes := mm.nodesOf(fi.File)
			for nid, r := range fi.Refs {
				if r.Kind != resolve.RefItem || types.ItemID(r.A) != e.id || nid >= len(nodes) || nodes[nid] == nil {
					continue
				}
				if sp, ok := useSpan(nodes[nid], it.Name, int(r.Segs)); ok {
					add(sp)
				}
			}
		}
		if !owned || m.cmod == nil {
			continue
		}
		for _, id := range s.unitsOf(m) {
			b := s.body(m, id)
			if b == nil || b.File == nil {
				continue
			}
			nodes := mm.nodesOf(b.File)
			for i, u := range b.Uses {
				nid := int(b.Base) + i
				if nid >= len(nodes) || nodes[nid] == nil {
					continue
				}
				switch {
				case (u.Kind == checker.UseMethod || u.Kind == checker.UseItem) && u.Item == e.id:
				case u.Kind == checker.UseBuiltin && s.builtinIs(b, nodes[nid], it):
				default:
					continue
				}
				if sp, ok := useSpan(nodes[nid], it.Name, 0); ok {
					add(sp)
				}
			}
		}
	}
	out := make([]source.Position, len(found))
	for i, p := range found {
		out[i] = s.w.sm.Position(p)
	}
	slices.SortFunc(out, func(a, b source.Position) int {
		if c := cmp.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Line, b.Line); c != 0 {
			return c
		}
		return cmp.Compare(a.Col, b.Col)
	})
	mm.uses[e.id] = out
	return out
}

// builtinIs reports whether a builtin use node of b is a call of the
// primitive's method it (str.Len: check resolves s.Len() to its builtin,
// not to std.core's item): the member's name is it's, and the receiver's
// type, references removed, is it's primitive.
func (s *Session) builtinIs(b *checker.Body, n ast.Node, it *types.Item) bool {
	if it.OwnerPrim == types.NoType {
		return false
	}
	if c, ok := n.(*ast.CallExpr); ok {
		n = c.Fun
	}
	fe, ok := n.(*ast.FieldExpr)
	if !ok || fe.Name == nil || fe.Name.Name != it.Name || fe.X == nil {
		return false
	}
	i := int(fe.X.ID()) - int(b.Base)
	if i < 0 || i >= len(b.Types) {
		return false
	}
	t := b.Types[i]
	for k := 0; k < 8 && t != types.NoType && t != types.Poison; k++ {
		if k := s.w.tab.Kind(t); k != types.KRef && k != types.KMut {
			break
		}
		t = s.w.tab.Get(t).Elem
	}
	return t == it.OwnerPrim
}

// useSpan is the span of the name a use node spells: the segs-th segment
// of a path when resolve says how many it covers, a field's or a call's
// name, else the last identifier inside spelling sym. False for a
// declaring node.
func useSpan(n ast.Node, sym source.Symbol, segs int) (source.Span, bool) {
	switch n := n.(type) {
	case *ast.Ident:
		return n.Span(), true
	case *ast.Path:
		if segs >= 1 && segs <= len(n.Names) {
			return n.Names[segs-1].Span(), true
		}
		return lastIdent(n, sym)
	case *ast.PathType:
		return useSpan(n.Path, sym, segs)
	case *ast.PathPattern:
		return useSpan(n.Path, sym, segs)
	case *ast.NamePattern:
		return n.Name.Span(), true
	case *ast.FieldExpr:
		return n.Name.Span(), true
	case *ast.CallExpr:
		if n.Fun != nil {
			return useSpan(n.Fun, sym, 0)
		}
	}
	if _, ok := n.(ast.Expr); ok {
		return lastIdent(n, sym)
	}
	if _, ok := n.(ast.Pattern); ok {
		return lastIdent(n, sym)
	}
	return source.Span{}, false
}

// lastIdent is the span of the last identifier under n spelling sym, or
// n's own span when there is none.
func lastIdent(n ast.Node, sym source.Symbol) (source.Span, bool) {
	sp := n.Span()
	ast.Inspect(n, func(c ast.Node) bool {
		if c == nil {
			return false
		}
		if id, ok := c.(*ast.Ident); ok && id.Name == sym {
			sp = id.Span()
		}
		return true
	})
	return sp, true
}

// nodesOf is f's nodes by NodeID.
func (mm *lspMemo) nodesOf(f *ast.File) []ast.Node {
	if f == nil {
		return nil
	}
	if ns, ok := mm.nodes[f]; ok {
		return ns
	}
	ns := make([]ast.Node, f.NodeCount+1)
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if id := n.ID(); int(id) < len(ns) {
			ns[id] = n
		}
		return true
	})
	mm.nodes[f] = ns
	return ns
}

// unitsOf are the units of m that have a typed body: Units, then the
// consts stage 5 checked early.
func (s *Session) unitsOf(m *modNode) []types.ItemID {
	out := m.cmod.Units()
	for _, b := range m.cmod.Early() {
		out = append(out, b.Item)
	}
	return out
}

// body is unit id's typed body: an early const's from stage 5, else one
// check.Checker.Check of the unit, memoized. A fn literal's enclosing unit
// is checked first, so the literal's capture record is closed and
// its check never waits. Nil when the check faults.
func (s *Session) body(m *modNode, id types.ItemID) *checker.Body {
	mm := s.memo()
	if b, ok := mm.bodies[id]; ok {
		return b
	}
	if mm.early == nil {
		mm.early = map[types.ItemID]*checker.Body{}
		for _, x := range s.allMods() {
			if x.cmod != nil && x.info != nil {
				for _, b := range x.cmod.Early() {
					mm.early[b.Item] = b
				}
			}
		}
	}
	if b := mm.early[id]; b != nil {
		mm.bodies[id] = b
		return b
	}
	it := s.w.items.Get(id)
	if it.Kind == types.KindLiteral && it.Owner != types.NoItem {
		s.body(m, it.Owner)
	}
	var b *checker.Body
	if ie := source.Guard("lsp check "+it.Key.String(), it.Span, func() { b = s.w.sem5.chk.Check(m.cmod, id) }); ie != nil {
		b = nil
	}
	mm.bodies[id] = b
	return b
}

// TypeAt is the display name of the type check gave the innermost
// expression or pattern of the file at path that ends at byte offset off
// (normalized text: Result.Sources' file), so `x` of `x.` and the name of
// `x :=` answer their types. False before a Check, outside every body, in a
// module loaded whole from the cache (see KeepSyntax), when no typed node
// ends at off, or when the type is unknown (an earlier error).
func (s *Session) TypeAt(path string, off uint32) (string, bool) {
	mm := s.memo()
	if mm == nil || s.w.sem5 == nil {
		return "", false
	}
	f, ok := s.fileAt(path)
	if !ok {
		return "", false
	}
	m, fi := s.moduleOfFile(f.ID)
	if m == nil || m.cmod == nil {
		return "", false
	}
	units := map[types.ItemID]bool{}
	for _, id := range s.unitsOf(m) {
		units[id] = true
	}
	// The innermost unit whose declaring node holds off.
	var best types.ItemID
	var bestSpan source.Span
	nodes := mm.nodesOf(m.info.Files[fi].File)
	for _, ii := range m.info.Items {
		if ii.File != fi || !units[ii.Item] || int(ii.Node) >= len(nodes) || nodes[ii.Node] == nil {
			continue
		}
		sp := nodes[ii.Node].Span()
		if sp.Start <= off && off <= sp.End && (best == types.NoItem || sp.End-sp.Start < bestSpan.End-bestSpan.Start) {
			best, bestSpan = ii.Item, sp
		}
	}
	if best == types.NoItem {
		return "", false
	}
	b := s.body(m, best)
	if b == nil || b.Node == nil {
		return "", false
	}
	t := types.NoType
	ast.Inspect(b.Node, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		sp := n.Span()
		if sp.Start > off || sp.End < off {
			return false // off is not under n
		}
		_, isExpr := n.(ast.Expr)
		_, isPat := n.(ast.Pattern)
		if sp.End == off && (isExpr || isPat) {
			if i := int(n.ID()) - int(b.Base); i >= 0 && i < len(b.Types) {
				x := b.Types[i]
				if l := b.Bind[i]; l != 0 && int(l) < len(b.Locals) {
					x = b.Locals[l].Type // a binding: its local's type
				}
				if x != types.NoType && x != types.Poison {
					t = x // later in preorder is deeper
				}
			}
		}
		return true
	})
	if t == types.NoType {
		return "", false
	}
	return s.w.tab.TypeString(t, b.Generics), true
}

// moduleOfFile is the module whose resolution holds file id, and the
// file's index in it; nil when no module with syntax holds it.
func (s *Session) moduleOfFile(id source.FileID) (*modNode, int) {
	for _, m := range s.allMods() {
		if m.info == nil {
			continue
		}
		for i, fi := range m.info.Files {
			if fi.FileID == id {
				return m, i
			}
		}
	}
	return nil, 0
}

// QueryChecked is Query over the last Check, without checking again: the
// same answers, Partial by the same rule. A usage error for an unknown
// query, a wrong argument count, or no Check yet; nil when that check did
// not reach the semantics.
func (s *Session) QueryChecked(q string, args []string) (*QueryAnswer, error) {
	n := -1
	for _, d := range QueryNames {
		if d.Name == q {
			n = d.Args
		}
	}
	if n < 0 {
		return nil, usagef("unknown query %q", q)
	}
	if len(args) != n {
		return nil, usagef("query %s takes %d argument(s), got %d", q, n, len(args))
	}
	if s.memo() == nil {
		return nil, usagef("query %s: the session has not checked", q)
	}
	if !s.lastFullStd {
		s.fullStd = true
		s.Check()
	}
	qx := s.index()
	if qx == nil {
		return nil, nil
	}
	return s.localizeAnswer(answerQuery(qx, q, args, !reachedTypes(s.p.diags))), nil // local names
}

// reachedTypes is checkedToTypes over a pipeline's diagnostics.
func reachedTypes(ds []*diag.Diagnostic) bool {
	for _, d := range ds {
		if d.Phase == diag.PhaseInternal || d.Phase < diag.PhaseTypes {
			return false
		}
	}
	return true
}

// answerQuery is Query's answer over a query index: Query's dispatch and
// QueryChecked's, one switch.
func answerQuery(qx *queryIndex, q string, args []string, partial bool) *QueryAnswer {
	subject := strings.Join(args, " ")
	a := &QueryAnswer{Query: q, Subject: subject, Found: true, Partial: partial}
	var lines []string
	switch q {
	case "modules":
		lines = qx.modules()
	case "uses":
		us, ls := qx.s.stdUses()
		a.Uses = &us
		a.Answer = ls
		return a
	case "perimeter":
		return perimeterAnswer(qx.s)
	case "find":
		lines = qx.find(subject)
		a.Found = len(lines) > 0
		if !a.Found {
			lines = []string{"no name matches " + subject}
		}
	default:
		e, ok := qx.lookup(args[0])
		if !ok {
			if q == "members" {
				if ls := qx.primMembers(args[0]); len(ls) > 0 {
					lines = ls
					break
				}
			}
			if q == "sig" {
				// `E.Format`, the compiler's Format of an error,
				// which no source declares and the index does not list.
				if ls, ok := qx.providedFormatSig(args[0]); ok {
					lines = ls
					break
				}
				// A universe function no source declares.
				if l, ok := universeSigs[args[0]]; ok {
					lines = []string{l}
					break
				}
			}
			a.Found = false
			lines = qx.notFound(args[0])
			break
		}
		switch q {
		case "members":
			if t, ok := qx.typeNamed(args[0], e); ok {
				// `members Eq`: the prelude interface, with the
				// generator of that name after it.
				lines = append(qx.members(t), "also "+lastSeg(e.mod)+"."+e.name+", a function: "+qx.sigLine(e.name, qx.funcSig(e.id)))
				break
			}
			lines = qx.members(e)
		case "sig":
			lines = qx.sig(e)
		case "import":
			lines = qx.importLine(e)
		case "kind", "shape":
			lines = qx.shape(e, q == "kind")
		case "flows":
			lines = qx.flows(e)
		case "errors":
			lines = qx.errors(e)
		case "callers":
			lines = qx.callers(e)
		case "panics":
			lines = qx.panics(e)
		case "allocs":
			lines = qx.allocs(e)
		case "stack":
			lines = qx.stack(e)
		case "why-live":
			lines = qx.whyLive(e, args[1])
		}
	}
	a.capLines(lines)
	return a
}
