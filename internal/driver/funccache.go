// Why: a module that missed its interface lookup should not recheck every
// function in it. A body edit should recheck that function, an exported
// signature edit the functions that read it, and an added item the
// functions with a negative read of its name.
//
// What: per-function reuse inside a missed module: each function's prefix,
// candidates and FuncResult (funcLookup, unitReads, encodeFunc), the const
// part of the prefix (constPart, moduleConsts, contractNames) and the read
// filling done at store time (filled).
//
// How:
//   - Only plain functions, methods and impl methods are reused (KindFunc
//     with no fn literal, whose parent is none or an impl): a literal, a
//     contract predicate, an invariant, a test, a const and a global are
//     always checked, so no unit's check depends on a sibling that was
//     skipped. An impl method's anchor is its whole impl (the top-level
//     item), so an edit of the impl's header is a miss.
//   - The function prefix holds the build ID, the item key, the token hash
//     of the item's anchor and its file's imports, plus the module's
//     context (the module prefix's flags and file list), and the const
//     part (constPart): the token hashes of the same-module consts the
//     unit names, of the consts their initializers name, and of the consts
//     named by the requires and ensures clauses of the same-module
//     functions the unit names (the clause part). No impl is in it:
//     every impl read (ReadImpl, ReadNoImpl) is validated against its impl
//     set (implreads.go), and every Send and Share judgement records the
//     shape of the types it walks, whose part covers their unsafe impls, so
//     an added, changed or removed impl rechecks only the functions whose
//     reads it changes.
//   - A unit's reads are resolve's for its item, check's for its body
//     (evaluations included) and borrow's, with every zero hash filled from
//     the session at store time; a read the session cannot answer makes the
//     unit not stored on its own (its module may still be).
//   - Function results are stored only when their module is (store), under
//     their own key; the module's interface frame names the same entries.
//   - MK_CACHE_VERIFY recomputes every function: no function hit is taken.

package driver

import (
	"cmp"
	"slices"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/eval"
	"mk/internal/iface"
	"mk/internal/mir"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// unitRec is what one unit of a module that missed did in this build: its
// reads and raw diagnostics (computed), or the function result it loaded
// (entry, reused).
type unitRec struct {
	reads  types.ReadSet
	diags  []*diag.Diagnostic // stage 6
	flow   []*diag.Diagnostic // stage 7
	reused bool
	entry  funcRef // the function result it was loaded from, when reused
}

// noteUnit records a unit's result for store, under readsMu.
func (m *modNode) noteUnit(id types.ItemID, r *unitRec) {
	m.readsMu.Lock()
	if m.units == nil {
		m.units = map[types.ItemID]*unitRec{}
	}
	m.units[id] = r
	m.readsMu.Unlock()
}

// unit returns a unit's record, nil when none.
func (m *modNode) unit(id types.ItemID) *unitRec {
	m.readsMu.Lock()
	defer m.readsMu.Unlock()
	return m.units[id]
}

// itemInfo is resolve's record of item id in m, nil when none.
func (m *modNode) itemInfo(id types.ItemID) *resolve.ItemInfo {
	m.infoOnce.Do(func() {
		m.infoBy = map[types.ItemID]*resolve.ItemInfo{}
		if m.info != nil {
			for _, ii := range m.info.Items {
				m.infoBy[ii.Item] = ii
			}
		}
	})
	return m.infoBy[id]
}

// reusable reports whether it is a unit whose result may be loaded instead
// of checked (the first deviation above).
func (m *modNode) reusable(it *types.Item, items *types.Items) bool {
	if it.Kind != types.KindFunc {
		return false
	}
	ii := m.itemInfo(it.ID)
	if ii == nil || len(ii.Literals) > 0 {
		return false
	}
	if it.Origin != nil {
		// A generated function's tokens are pinned to declarations
		// (gen's origins) that its token hash does not spell, so it is
		// never loaded on its own; its module's hit loads it (GenRef).
		return false
	}
	// resolve gives an impl's methods the impl as parent (declare.go), as
	// it gives lifted literals, contract predicates and invariants theirs.
	return ii.Parent == types.NoItem || items.Get(ii.Parent).Kind == types.KindImpl
}

// funcPrefix is the function manifest prefix of unit it of m.
func (b *build) funcPrefix(m *modNode, it *types.Item) (source.Hash, bool) {
	fc := b.w.front
	if it.Span.File == source.NoFile {
		return source.Hash{}, false
	}
	k, ok := fc.anchors.Locate(it.Span.File, it.Span.Start)
	if !ok || k.IsFile() {
		return source.Hash{}, false
	}
	a, _ := fc.anchors.Anchor(k)
	ii := m.itemInfo(it.ID)
	if ii == nil || ii.File < 0 || ii.File >= len(m.info.Files) {
		return source.Hash{}, false
	}
	var imps [][2]string
	for _, bd := range m.info.Files[ii.File].Imports {
		// The binding and the module the line resolves to: a local name
		// means what the manifest says (a package's module paths start
		// with its canonical id).
		to := bd.Path
		if d := m.imports[bd.Path]; d != nil {
			to = bd.Path + "=" + d.path
		}
		imps = append(imps, [2]string{b.w.in.Str(bd.Name), to})
	}
	c := source.NewHasher("mk.driver.funcctx.v7")
	c.Hash(fc.ctx)
	c.String(m.path)
	c.Bool(m.std)
	c.Bool(m.unsafe)
	c.Bool(m.binRoot)
	if m.libBoundary {
		c.String("library") // E1109
	}
	c.Bool(m.seesCore)
	for _, f := range m.files {
		c.String(f.src.Path)
		// The site path is in the lowered body (a trace site's file,
		// lower.Config.SitePath), so it is in the key.
		c.String(b.w.sitePath(f.src.ID))
		c.Bool(f.bodiless)
		c.Bool(f.prelude)
	}
	b.constPart(c, m, a)
	return iface.FuncPrefix(c.Sum(), it.Key, a.TokenHash, iface.ImportsHash(imps)), true
}

// funcWorld is the validation World of m's units: the module's, with the
// bodies that are ready.
func (b *build) funcWorld(m *modNode) iface.World {
	s := b.w.sem5
	return b.w.front.world(b.w, m, s.hash, func(id types.ItemID) *mir.Body {
		if e := s.body(id); e != nil && e.state == eval.BodyReady {
			return e.mir
		}
		return nil
	})
}

// lookupWorld is funcWorld for m's unit lookups during its check: the
// namespace reads go to m's shared names world. A namespace is a function
// of the item lists of m and its closure, which stage 3 fixed before the
// check, so one answer serves every unit; every other read, a body's
// above all, is answered by the unit's own world.
func (b *build) lookupWorld(m *modNode) iface.World {
	fw := b.funcWorld(m).(*frontWorld)
	m.namesMu.Lock()
	if m.names == nil {
		m.names = b.funcWorld(m).(*frontWorld).SessionWorld
	}
	fw.names = m.names
	m.namesMu.Unlock()
	return fw
}

// dropNames drops m's names world once its check is done.
func (m *modNode) dropNames() {
	m.namesMu.Lock()
	m.names = nil
	m.namesMu.Unlock()
}

// funcLookup is the per-function lookup for one unit of a module that
// missed: the first candidate whose reads hold and whose result decodes.
// It returns nil on a miss. On a hit the unit's body entry is stored and
// its diagnostics are in r.
func (b *build) funcLookup(m *modNode, it *types.Item, r *unitResult) bool {
	w := b.w
	fc := w.front
	if fc == nil || fc.verify || !m.reusable(it, b.w.items) {
		return false
	}
	p, ok := b.funcPrefix(m, it)
	if !ok {
		return false
	}
	idx := m.funcIndexOf(fc)
	man := idx.lookupCandidates(p)
	if man == nil {
		return false
	}
	world := b.lookupWorld(m)
	for _, c := range man.Candidates {
		var valid bool
		if ie := source.Guard("validate "+it.Key.String(), it.Span, func() { valid, _ = iface.Valid(world, c.Reads) }); ie != nil || !valid {
			continue
		}
		ref := funcRef{bundle: idx.refs[c.Entry], key: c.Entry}
		fb, ok := fc.result(ref)
		if !ok {
			continue
		}
		var fr *iface.FuncResult
		var err error
		if ie := source.Guard("decode "+it.Key.String(), it.Span, func() { fr, err = iface.DecodeFuncResult(fc.env, fb) }); ie != nil || err != nil {
			continue
		}
		if fr.Item != it.Key || (fr.Accept && fr.Body == nil) {
			continue
		}
		entry := &bodyEntry{state: eval.BodyPoisoned}
		if fr.Accept {
			e, err := decodeFlow(fr.Borrow)
			if err != nil {
				continue
			}
			entry = e
			entry.mir = fr.Body
		}
		rec := &unitRec{reused: true, entry: ref}
		for _, rd := range c.Reads {
			rec.reads.Add(rd)
		}
		for i := range fr.Diags {
			d := fr.Diags[i]
			if d.Phase == diag.PhaseFlow {
				rec.flow = append(rec.flow, &d)
			} else {
				rec.diags = append(rec.diags, &d)
			}
		}
		r.diags, r.flow = rec.diags, rec.flow
		w.sem5.store(it.ID, entry)
		m.noteUnit(it.ID, rec)
		return true
	}
	return false
}

// unitReads is a computed unit's reads: resolve's for its item, check's
// for its body and borrow's.
func (m *modNode) unitReads(it *types.Item, body, flow *types.ReadSet) types.ReadSet {
	var rs types.ReadSet
	if ii := m.itemInfo(it.ID); ii != nil {
		rs.Merge(&ii.Reads)
	}
	if body != nil {
		rs.Merge(body)
	}
	if flow != nil {
		rs.Merge(flow)
	}
	return rs
}

// constTok is one const of a module for constPart: its anchor's token
// hash and the identifier texts of its tokens.
type constTok struct {
	hash  source.Hash
	names []string
}

// constPart hashes into c, in name order, the token hash of every
// same-module const that unit it names, directly or through the
// initializer of a const it names. check folds a same-module const into a
// body without recording a read of its value when it folds the initializer
// itself (check/early.go constValue with HasValue false, the path foldCond
// takes for `if Flag`; only the taken branch is lowered), and the
// module's const values are written only after its units ran (stages.go
// declEvals), so no read validated at lookup could cover them. The folded
// value is a function of those initializers' tokens (a fold has no call;
// a const of another module is a recorded value read), so their token
// hashes in the prefix make a reused body exact. A name is matched by its
// identifier text: a local or field of the same name only costs a recheck.
// Before this part a reused function kept the branch of the old value
// (`const BenchCount: bool = false` flipped to true, a build from MK_CACHE
// took the false branch, dev and release;
// TestInvalidation_SameModuleConstFolded).
//
// A unit also folds the requires clauses of the same-module functions it
// calls or names as values (check foldedRequires, at the call), so the
// consts those clauses name are in its part too: every function the unit
// names gives the identifiers of its clauses (moduleConsts'
// contractNames), one hop, since a fold enters no same-module call
// (forwardreq.go methodCall). Before this a warm build kept the old
// verdict: `G(): int requires Max > 0` called by Pick, with Max edited
// from 1 to 0 in another file, was clean warm and E0706 cold
// (TestInvalidation_SameModuleClauseFolded).
func (b *build) constPart(c *source.Hasher, m *modNode, a iface.Anchor) {
	consts := b.moduleConsts(m)
	if len(consts) == 0 {
		return
	}
	seen := map[string]bool{}
	var work []string
	add := func(n string) {
		if consts[n] != nil && !seen[n] {
			seen[n] = true
			work = append(work, n)
		}
	}
	for _, n := range m.contractNames[""] {
		add(n)
	}
	for _, n := range b.anchorIdents(m, a) {
		add(n)
		for _, cn := range m.contractNames[n] {
			add(cn)
		}
	}
	for i := 0; i < len(work); i++ {
		for _, n := range consts[work[i]].names {
			if consts[n] != nil && !seen[n] {
				seen[n] = true
				work = append(work, n)
			}
		}
	}
	slices.Sort(work)
	c.Uvarint(uint64(len(work)))
	for _, n := range work {
		c.String(n)
		c.Hash(consts[n].hash)
	}
}

// moduleConsts is m's consts by name with their anchors' token hashes and
// identifiers; a const with no item anchor is left out (no unit can fold
// what has no tokens of its own). It sets m.contractNames in the same
// pass (contractNames), only when the module has a const, since constPart
// reads it only then. checkModule calls it at the start of stage 6, while
// the ASTs are held; every later call reads what that one built.
func (b *build) moduleConsts(m *modNode) map[string]*constTok {
	m.constOnce.Do(func() {
		fc := b.w.front
		m.consts = map[string]*constTok{}
		if m.mod == nil {
			return
		}
		for _, id := range m.mod.Items {
			it := b.w.items.Get(id)
			if it.Kind != types.KindConst || it.Span.File == source.NoFile {
				continue
			}
			k, ok := fc.anchors.Locate(it.Span.File, it.Span.Start)
			if !ok || k.IsFile() {
				continue
			}
			a, _ := fc.anchors.Anchor(k)
			m.consts[b.w.in.Str(it.Name)] = &constTok{hash: a.TokenHash, names: b.anchorIdents(m, a)}
		}
		if len(m.consts) > 0 {
			m.contractNames = b.contractNames(m)
		}
	})
	return m.consts
}

// contractNames maps the name of every function, method and interface
// method of m that states a requires or ensures clause to the
// identifier-shaped tokens of its clauses, the generated file's included;
// a name stated twice (methods of two types) has the union. A file with no
// AST gives every identifier of its anchors under "", which constPart adds
// for every unit: a conservative fallback (more misses, never a stale hit).
// It is not reached on the paths that check units: checkModule builds the
// map at the start of stage 6, before release drops the ASTs. It is
// reached if the first call comes after release, as it did from a store's
// encoding before that call.
// The map is a function of the module's tokens, as the prefix must be: it
// is read at lookup, before any unit of the module is checked.
func (b *build) contractNames(m *modNode) map[string][]string {
	out := map[string][]string{}
	walk := func(text string, f *ast.File) {
		ast.Inspect(f, func(n ast.Node) bool {
			sig, ok := n.(*ast.Signature)
			if !ok || len(sig.Contracts) == 0 || sig.Name == nil || sig.Name.Name == nil {
				return true
			}
			name := b.w.in.Str(sig.Name.Name.Name)
			for _, ct := range sig.Contracts {
				sp := ct.Span()
				i, _ := slices.BinarySearchFunc(f.Tokens, sp.Start, func(t ast.Token, off uint32) int { return cmp.Compare(t.Start, off) })
				for ; i < len(f.Tokens) && f.Tokens[i].End() <= sp.End; i++ {
					if t := f.Tokens[i]; !t.Kind.IsTrivia() && int(t.End()) <= len(text) && t.Len > 0 && identStart(text[t.Start]) {
						out[name] = append(out[name], text[t.Start:t.End()])
					}
				}
			}
			return true
		})
	}
	for _, f := range m.files {
		switch {
		case f.src == nil:
		case f.ast != nil:
			walk(f.src.Text, f.ast)
		case f.sum != nil:
			out[""] = append(out[""], b.anchorIdents(m, f.sum.FileAnchor)...)
			for _, it := range f.sum.Items {
				out[""] = append(out[""], b.anchorIdents(m, it.Anchor)...)
			}
		}
	}
	if m.gen != nil && m.gen.out != nil && m.gen.out.File != nil && m.gen.out.AST != nil {
		walk(m.gen.out.File.Text, m.gen.out.AST)
	}
	return out
}

// identStart reports whether r can start an identifier (or a keyword).
func identStart(r byte) bool { return r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }

// anchorIdents are the identifier-shaped token texts of anchor a.
func (b *build) anchorIdents(m *modNode, a iface.Anchor) []string {
	var text string
	for _, f := range m.files {
		if f.src.ID == a.File {
			text = f.src.Text
			break
		}
	}
	var out []string
	for i := range a.Starts {
		s, e := a.Starts[i], a.Ends[i]
		if int(e) > len(text) || s >= e {
			continue
		}
		t := text[s:e]
		if identStart(t[0]) {
			out = append(out, t)
		}
	}
	return out
}

// filled returns rs with every zero hash of a positive read filled from
// world, every impl read's hash set to its impl set (implreads.go; the
// negative ReadNoImpl too), and the target reads dropped (the target is in
// every prefix); false names the first read the session cannot answer.
func filled(world iface.World, rs []types.Read) ([]types.Read, types.Read, bool) {
	var out types.ReadSet
	for _, r := range rs {
		switch {
		case r.Kind == types.ReadTarget:
			continue
		case r.Kind == types.ReadImpl || r.Kind == types.ReadNoImpl:
			fw, ok := world.(*frontWorld)
			if !ok {
				return nil, r, false
			}
			h, ok := fw.implSet(r.Key, r.Name)
			if !ok {
				return nil, r, false
			}
			r.Hash = h
			out.Add(r)
			continue
		case r.Kind.IsNegative() || !r.Hash.IsZero() || r.Kind == types.ReadFile || r.Kind == types.ReadData:
			out.Add(r)
			continue
		}
		h, ok := world.Part(r)
		if !ok {
			return nil, r, false
		}
		r.Hash = h
		out.Add(r)
	}
	return out.Sorted(), types.Read{}, true
}

// encodeFunc is one computed unit's result under its own key: the key,
// the function prefix, the filled reads (the manifest candidate) and the
// payload; false when the unit is not stored on its own (not reusable, no
// prefix, or a read the session cannot answer). store puts the payload
// and adds the candidate (after MK_CACHE_VERIFY's comparison).
func (b *build) encodeFunc(m *modNode, it *types.Item, e *bodyEntry, rec *unitRec, world iface.World) (source.Hash, source.Hash, []types.Read, []byte, bool) {
	fc := b.w.front
	if !m.reusable(it, b.w.items) {
		return source.Hash{}, source.Hash{}, nil, nil, false
	}
	p, ok := b.funcPrefix(m, it)
	if !ok {
		return source.Hash{}, source.Hash{}, nil, nil, false
	}
	reads, _, ok := filled(world, rec.reads.Sorted())
	if !ok {
		return source.Hash{}, source.Hash{}, nil, nil, false
	}
	key := iface.FuncKey(p, reads)
	fr := &iface.FuncResult{Item: it.Key, Accept: e.state == eval.BodyReady, Body: e.mir, Borrow: encodeFlow(b.w, it, e)}
	for _, rs := range reads {
		fr.Reads.Add(rs)
	}
	for _, d := range rec.diags {
		fr.Diags = append(fr.Diags, *d)
	}
	for _, d := range rec.flow {
		fr.Diags = append(fr.Diags, *d)
	}
	return key, p, reads, iface.EncodeFuncResult(fc.env, b.w.sem5.hash, fr), true
}
