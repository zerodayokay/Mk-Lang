// Why: a rebuild should redo only the modules whose meaning changed; the
// front cache lets a module's check be skipped, and its diagnostics
// replayed, when nothing it read has changed.
//
// What: frontCache (startFront, anchorFiles, stdContext, prefix, lookup,
// load), the store side (build.store, storeEntries, storeWrites,
// unstorable, externalReads), frontWorld (the trace validator's view), the
// entry frames (frameIfaceGen, unframeIfaceGen) and encodeFlow/decodeFlow.
//
// How: a trace-validated lookup at module granularity: a candidate entry is
// found by the module's prefix and kept only if every read it recorded
// still hashes the same. Poison is never stored. In detail:
//
//   - Anchors are per item (addItemAnchors: an item's non-trivia tokens, its
//     token hash syntax.ItemHash, keyed by file and the item's head tokens,
//     not by item key, since a module hit has no resolve to name its items),
//     with a per-file anchor (every token, syntax.FileHash) for spans
//     outside items: a comment, a blank line or a line added above moves
//     nothing, and a token edit of an item makes only the records anchored
//     in it stale (a miss, never a wrong hit).
//   - The module prefix holds, for every module of the reader's transitive
//     dependency closure (not only the direct imports), its path, API hash
//     and shape hash, and a context hash (build ID, target, profile, the
//     module's own flags). Names, impls and member lists of the closure are
//     therefore covered by the prefix, and the stored external reads keep
//     only what the API and shape parts do not cover: bodies, layouts,
//     private items' signatures, shapes, fields and values, pkg.mk fields
//     and files.
//   - Function results are stored per item with the module's interface, and
//     inside a module that missed, a plain function is reused on its own
//     when its function candidate validates (funccache.go).
//   - A module is stored only when its stage 3 and stage 5 are clean, every
//     module it depends on is done clean, no evaluation it ran returned
//     poison, and no diagnostic of it is an E0000 or E0001 or carries fixes
//     (a unit with fixes is not stored, which reruns the
//     emitting unit on the next build too).

package driver

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"mk/internal/cache"
	"mk/internal/diag"
	"mk/internal/eval"
	"mk/internal/iface"
	"mk/internal/mir"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// frontCache is a world's module cache: nil when the cache is off for the
// world (--no-cache, a corpus session, a test hook that changes what a
// stage computes, or a store that does not open).
type frontCache struct {
	st          *cache.Store
	noManifests bool          // true when <cache>/v1/manifest did not exist at startFront (cold cache)
	ctx         source.Hash   // the build ID, target and profile (context), initialized in anchorFiles
	anchors     *anchorLayers // the parse summaries' and the generated files' (genref.go)
	// bodyHashes memoizes a ReadBody's hash per MIR body (genref.go): every
	// module a generator ran in reads the generator's bodies.
	bodyHashes sync.Map
	// implEntries memoizes a done module's impl entries (implreads.go).
	implEntries sync.Map
	// closureIndex memoizes a reader's closure impl index (implreads.go).
	closureIndex sync.Map
	// reaches and extReads serve the walk markers (walkcache.go): the
	// reach entries read, and each module's external reads hash.
	reaches  sync.Map
	extReads sync.Map
	// walk is the walk markers' build and contexts (walkcache.go).
	walk walkState
	// closures memoizes closureOf (genref.go).
	closures sync.Map
	env      *iface.Env

	mu        sync.Mutex
	notStored []string
	// verify is MK_CACHE_VERIFY: every validated candidate is fetched but
	// not used, the module is recomputed, and store compares the encodings
	// (frontverify.go); mismatches, under mu.
	verify      bool
	verifyDiags []*diag.Diagnostic
	// bundles are the function-result bundles read or written by this
	// frontCache, by key, under mu; nil value: absent (funcbundle.go).
	bundles map[source.Hash]map[source.Hash][]byte
	// The background tasks (lazybody.go): the prefetch of hits' function
	// results, with its semaphore, and the stores; their faults, under mu.
	preOnce sync.Once
	preSem  chan struct{}
	// storeSem bounds the stores in flight (storeSlot, genref.go).
	storeOnce sync.Once
	storeSem  chan struct{}
	preWG     sync.WaitGroup
	bgDiags   []*diag.Diagnostic
	// deferWrites: a store's file writes run on their own goroutine,
	// under writeWG, past the end of the check stage (lazybody.go
	// writes); set by a Build whose walk runs beside native (walktail.go).
	deferWrites bool
	writeWG     sync.WaitGroup
	writeDelay  time.Duration // TestHooks.StoreWritesDelay
}

// frontHit is what a module hit loaded, kept until stages 5 to 7 would
// have run (phase 1 modules decode before the semantics exist).
type frontHit struct {
	ifc    *iface.Interface
	bodies map[types.ItemID]*bodyEntry
	// lazy are the bodies' entries in the record's order, for the walk's
	// prefetch (prefetchWalked): a hit's results are decoded only when a
	// stage reads them, never all of them on every build.
	lazy []*bodyEntry
}

// startFront opens the module cache for a session's Check: every build of
// the world shares it. It is off for any test hook that changes what a
// stage computes, and with --no-cache.
func (w *world) startFront(dir string) {
	h := w.cfg.TestHooks
	if h.PanicIn != "" || h.MaxFileSize != 0 || h.ICEAt != "" || h.WrapFS != nil || h.CheckPastNames ||
		h.OnStage != nil || h.SlowFunc != 0 || h.UntypeConst != "" {
		return
	}
	st := w.openStore(dir)
	if st == nil {
		return
	}
	_, err := os.Stat(filepath.Join(dir, "v1", string(cache.KindManifest)))
	w.front = &frontCache{st: st, noManifests: err != nil, verify: w.cfg.CacheVerify, writeDelay: h.StoreWritesDelay}
}

// anchorFiles gives every parsed file of the builds its file anchor, after
// the parse step and before any module runs (encoding and decoding read them from many
// goroutines).
func (fc *frontCache) anchorFiles(w *world, builds []*build) {
	set := iface.NewAnchorSet()
	for _, b := range builds {
		for _, m := range b.mods {
			for _, f := range m.files {
				if f.sum == nil || f.src == nil {
					continue
				}
				set.Add(iface.FileAnchorKey(f.src.Path), f.sum.FileAnchor)
				for _, it := range f.sum.Items {
					set.Add(it.Key, it.Anchor)
				}
			}
		}
	}
	fc.anchors = newAnchorLayers(set)
	c := source.NewHasher("mk.driver.front.v1")
	c.Hash(w.buildID())
	c.String(w.cfg.Target.Name)
	c.Uvarint(uint64(w.cfg.Profile))
	fc.ctx = c.Sum()
	fc.ctx = fc.stdContext(w, builds)
	if w.libcBuild() {
		// A system-libc build folds std.sys.LibcBuild() to true, so
		// its walk, and every module result, is keyed apart from a static
		// build's; a static build's keys are unchanged.
		c := source.NewHasher("mk.driver.libcctx.v1")
		c.Hash(fc.ctx)
		c.Bool(true)
		fc.ctx = c.Sum()
	}
	fc.env = &iface.Env{Interner: w.in, Types: w.tab, Items: w.items, Consts: w.tab.Consts(), Sources: w.sm,
		PointerBytes: w.cfg.Target.PointerBytes, Anchors: fc.anchors}
}

// stdContext folds the content of every std source file into the context
// hash when std comes from MK_STD (Config.StdDir). An embedded std is part
// of the executable, so the build ID already changes with it; a std on
// disk is not, and a module's key names only its imports, while lowering
// calls std items no module imports (std.rt's TraceTry, TraceFail, the
// panic and check hooks: known items). Without this, an edit of such an
// item under one MK_STD kept a user module's hit, whose function results
// then named an item that no longer existed ("iface: entry refers to
// something that changed: item std.rt.TraceTry";
// TestInvalidation_StdDirEditMissesImplicitReaders). So under MK_STD any
// std edit misses every module, as an embedded std edit does. Called once,
// after the parse step and before any key is computed.
func (fc *frontCache) stdContext(w *world, builds []*build) source.Hash {
	if w.cfg.StdDir == "" {
		return fc.ctx
	}
	c := source.NewHasher("mk.driver.stdtree.v1")
	c.Hash(fc.ctx)
	for _, b := range builds {
		if !b.isStd {
			continue
		}
		for _, m := range b.mods {
			if m.sysview {
				continue // a view's text is its own module's key, not std's
			}
			for _, f := range m.files {
				if f.src == nil {
					continue
				}
				c.String(f.src.Path)
				c.Hash(f.src.Hash)
			}
		}
	}
	return c.Sum()
}

// skip records a module that is not stored, with the reason.
func (fc *frontCache) skip(m *modNode, why string) {
	fc.mu.Lock()
	fc.notStored = append(fc.notStored, m.path+": "+why)
	fc.mu.Unlock()
}

// copyNotStored returns a snapshot of the recorded not-stored reasons.
func (fc *frontCache) copyNotStored() []string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return append([]string(nil), fc.notStored...)
}

// closure is m's transitive dependency closure, in slot order of each
// build, m excluded.
func closure(m *modNode) []*modNode {
	seen := map[*modNode]bool{}
	var out []*modNode
	var walk func(x *modNode)
	walk = func(x *modNode) {
		for _, d := range append(slices.Clone(x.deps), x.after...) {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
				walk(d)
			}
		}
	}
	walk(m)
	slices.SortFunc(out, func(a, b *modNode) int { return strings.Compare(a.path, b.path) })
	return out
}

// prefix computes m's module prefix, or false when a module of its closure
// is not done clean or has no part hashes (m then misses and is not
// stored: it is computed against a failed or tainted module).
func (fc *frontCache) prefix(m *modNode) (source.Hash, bool) {
	var imps []iface.ImportHash
	for _, d := range closure(m) {
		if !d.partsOK || (d.state.Load() != modDoneClean && !d.phase1) {
			return source.Hash{}, false
		}
		imps = append(imps, iface.ImportHash{Path: d.path, API: d.api, Shape: d.shape})
	}
	c := source.NewHasher("mk.driver.modctx.v2")
	c.Hash(fc.ctx)
	c.Bool(m.std)
	c.Bool(m.unsafe)
	c.Bool(m.binRoot)
	if m.libBoundary {
		c.String("library") // E1109 judges this module's export externs
	}
	c.Bool(m.seesCore)
	// What each import line means, not only how it is spelled: a local
	// name is an alias in a manifest, so an edit to pkg.mk can
	// point `import a` at another package with no change to m's text.
	c.Hash(resolvedImports(m))
	var files []iface.FileTokens
	for _, f := range m.files {
		if f.sum == nil || f.tooLarge {
			return source.Hash{}, false
		}
		c.String(f.src.Path)
		c.Bool(f.bodiless)
		c.Bool(f.prelude)
		files = append(files, iface.FileTokens{Path: f.src.Path, Outside: f.sum.FileAnchor.TokenHash})
	}
	return iface.ModulePrefix(c.Sum(), m.path, files, imps), true
}

// world is the validation World of m's candidates. body serves the bodies
// of the closure (sem5's, or a phase-1 hit's); h is the hasher.
func (fc *frontCache) world(w *world, m *modNode, h *iface.Hasher, body func(types.ItemID) *mir.Body) iface.World {
	cl := fc.closureOf(w, m) // genref.go: memoized once the semantics exist
	sw := &iface.SessionWorld{H: h, Env: fc.env, Body: body, Target: w.cfg.Target, Reader: m.path,
		Closure: func(p string) bool { return cl.in[p] }, File: closureFileHash(m)}
	return &frontWorld{SessionWorld: sw, w: w, impls: &implState{mods: cl.mods}}
}

// fileReadDomain is gen's hash domain of a ReadFile read (internal/gen
// reflect.go fileReadDomain): the hash of the file's normalized text.
const fileReadDomain = "mk.file.v1"

// closureFileHash answers a ReadFile read (gen records one for the file of a
// struct whose `///` field docs it prints, since a comment is in no token
// hash) with the current text of that file, found by its SourceMap path
// among the files of m and its import closure, hashed as gen hashes it. A
// path outside them is not answered, so the candidate misses.
func closureFileHash(m *modNode) func(path string) (source.Hash, bool) {
	return func(path string) (source.Hash, bool) {
		for _, d := range append([]*modNode{m}, closure(m)...) {
			for _, f := range d.files {
				if f.src != nil && f.src.Path == path {
					return source.HashBytes(fileReadDomain, []byte(f.src.Text)), true
				}
			}
		}
		return source.Hash{}, false
	}
}

// frontWorld is the SessionWorld with the layout part the evaluator
// records (program.Layout and eval's errorLayoutHash): a layout read is
// answered from check's layout of the named type the read spells, found
// among the types of the session's table, and an error's from its payload
// fields, as eval hashes it. Before the semantics exist (phase 1) no
// layout is answered, so such a candidate misses. Impl reads are answered
// by their impl set (implreads.go).
type frontWorld struct {
	*iface.SessionWorld
	w     *world
	once  sync.Once
	named map[string]types.TypeID // item key + "\x00" + LayoutName(args)
	impls *implState              // ReadImpl and ReadNoImpl (implreads.go)
	// names, when set, answers the namespace reads (lookupWorld).
	names *iface.SessionWorld
}

func (fw *frontWorld) Part(r types.Read) (source.Hash, bool) {
	switch r.Kind {
	case types.ReadImpl, types.ReadNoImpl:
		return fw.implPart(r)
	case types.ReadData:
		return fw.w.dataPart(r) // datafile.go
	case types.ReadBody:
		return fw.bodyPart(r) // genref.go: memoized per body
	case types.ReadNoItem, types.ReadExports, types.ReadShape:
		// A shape part reads the closure's unsafe impls (withAsserts),
		// fixed before m's bodies are checked, as its namespaces are: the
		// shared world indexes them once per module, not once per unit.
		if fw.names != nil {
			return fw.names.Part(r)
		}
		return fw.SessionWorld.Part(r)
	case types.ReadLayout:
	default:
		return fw.SessionWorld.Part(r)
	}
	s := fw.w.sem5
	if s == nil {
		return source.Hash{}, false
	}
	id, ok := fw.w.items.ByKey(r.Key)
	// An item outside the reader and its closure is not read: its module
	// may be in its own check or decode now.
	if !ok || !fw.Sees(fw.w.items.Get(id).Module) {
		return source.Hash{}, false
	}
	var out source.Hash
	if ie := source.Guard("layout "+r.Key.String(), source.Span{}, func() {
		it := fw.w.items.Get(id)
		if d, isErr := it.Decl.(*types.ErrorDecl); isErr {
			b := binary.AppendUvarint(nil, uint64(len(d.Fields)))
			for _, f := range d.Fields {
				l := s.chk.Layout(f.Type)
				h := s.hash.Layout(&l)
				b = append(b, h[:]...)
			}
			out, ok = source.HashBytes("mk.eval.errorlayout.v1", b), true
			return
		}
		t := types.NoType
		if r.Name == "" {
			t = fw.w.tab.Named(id, nil)
		} else {
			t, ok = fw.instance(r.Key, r.Name)
			if !ok {
				return
			}
		}
		l := s.chk.Layout(t)
		out, ok = s.hash.Layout(&l), true
	}); ie != nil {
		return source.Hash{}, false
	}
	return out, ok
}

// instance finds the named type of item key whose generic arguments
// LayoutName spells name, among the closed named types of the table.
func (fw *frontWorld) instance(key types.Key, name string) (types.TypeID, bool) {
	fw.once.Do(func() { fw.named = fw.w.namedIndex() })
	t, ok := fw.named[key.String()+"\x00"+name]
	return t, ok
}

// namedIndex maps item key, a NUL byte and LayoutName(args) to each named type with
// generic arguments in the session's table.
func (w *world) namedIndex() map[string]types.TypeID {
	out := map[string]types.TypeID{}
	tab := w.tab
	for i := 1; i < tab.Len(); i++ {
		ty := tab.Get(types.TypeID(i))
		if ty.Kind != types.KNamed || len(ty.Args) == 0 {
			continue
		}
		var n string
		if source.Guard("layout name", source.Span{}, func() { n = w.sem5.hash.LayoutName(ty.Args) }) != nil {
			continue
		}
		out[w.items.Get(ty.Item).Key.String()+"\x00"+n] = types.TypeID(i)
	}
	return out
}

// typesKey is the entry, beside an interface, of the types its layout
// reads with generic arguments name (iface.EncodeTypes): a lookup interns
// them before it validates the candidate, so frontWorld finds them.
func typesKey(ifKey source.Hash) source.Hash {
	h := source.NewHasher("mk.driver.ltypes.v1")
	h.Hash(ifKey)
	return h.Sum()
}

// lookup is the module-cache lookup for one module whose closure is done:
// the trace-validated lookup and, on a hit, the decode into m's module and
// resolve's Load. Stages 5 to 7's half of a hit is loadHit. It returns
// whether m hit. Every failure is a miss.
func (fc *frontCache) lookup(w *world, m *modNode) bool {
	p, ok := fc.prefix(m)
	m.prefix, m.prefixOK = p, ok
	if !ok {
		return false
	}
	raw, ok, _ := fc.st.Get(cache.KindManifest, p)
	if !ok {
		return false
	}
	man, err := iface.DecodeManifest(raw)
	if err != nil {
		return false
	}
	var h *iface.Hasher
	var body func(types.ItemID) *mir.Body
	if w.sem5 != nil {
		h = w.sem5.hash
		body = func(id types.ItemID) *mir.Body {
			if e := w.sem5.body(id); e != nil && e.state == eval.BodyReady {
				return e.mir
			}
			return nil
		}
	} else {
		// Phase 1 (the prelude's closure, before the semantics): the
		// bodies are the closure's hits'.
		body = func(id types.ItemID) *mir.Body {
			it := w.items.Get(id)
			for _, d := range closure(m) {
				if d.id == it.Module && d.hit != nil {
					if e := d.hit.bodies[id]; e != nil && e.forced().state == eval.BodyReady {
						return e.mir
					}
				}
			}
			return nil
		}
		h = iface.NewHasher(w.tab, w.tab.Consts(), body)
	}
	for _, c := range man.Candidates {
		if w.sem5 != nil && slices.ContainsFunc(c.Reads, func(r types.Read) bool { return r.Kind == types.ReadLayout && r.Name != "" }) {
			if b, ok, _ := fc.st.Get(cache.KindIface, typesKey(c.Entry)); ok {
				_, _ = iface.DecodeTypes(fc.env, b)
			}
		}
		var valid bool
		var failed types.Read
		ie := source.Guard("validate "+m.path, source.Span{}, func() {
			valid, failed = iface.Valid(fc.world(w, m, h, body), c.Reads)
		})
		if os.Getenv("MK_FRONT_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "front %s: valid %v failed %v %s %q ice %v\n", m.path, valid, failed.Kind, failed.Key, failed.Name, ie)
		}
		if ie != nil || !valid {
			continue
		}
		if fc.verify {
			fc.fetchForVerify(m, c.Entry)
			return false
		}
		if fc.load(w, m, c.Entry) {
			m.ifKey, m.ifKeyOK = c.Entry, true
			fc.noteExtReads(m, c.Reads)
			return true
		}
	}
	return false
}

// load fetches an interface and every function result it names, then
// decodes them into m's module. A missing or undecodable payload fetched
// before the decode is a miss of the candidate; a decode that fails after
// the interface is in the session is an ICE (a compiler bug: the payloads
// passed their frames).
func (fc *frontCache) load(w *world, m *modNode, entry source.Hash) bool {
	raw, ok, _ := fc.st.Get(cache.KindIface, entry)
	if !ok {
		return false
	}
	// The function results are read first: a referrer whose payload GC
	// removed is a miss, decided before the session changes. The entry is
	// the driver's frame: the function result keys, then the interface
	// record.
	refs, rec, gen, err := unframeIfaceGen(raw)
	if err != nil {
		return false
	}
	var funcs [][]byte
	read := map[source.Hash]map[source.Hash][]byte{} // this load's bundles only
	for _, r := range refs {
		bm, ok := read[r.bundle]
		if !ok {
			if bm, ok = fc.readBundle(r.bundle); ok {
				read[r.bundle] = bm
			}
		}
		var b []byte
		if ok {
			b, ok = bm[r.key]
		}
		if !ok {
			// A bundle GC removed: drop the frame too, so the store of
			// the recomputed module writes one that names live bundles
			// (Put keeps a frame that verifies).
			_ = fc.st.RemoveFile(cache.KindIface, entry)
			return false
		}
		funcs = append(funcs, b)
	}
	// GenRef (genref.go): a module with gen output has its generated file
	// rebuilt and anchored before its interface decodes.
	if !fc.rebuildGen(w, m, gen) {
		return false
	}
	ifc, err := iface.DecodeInterfaceInto(fc.env, rec, m.mod)
	if err != nil {
		if os.Getenv("MK_FRONT_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "front %s: decode %v\n", m.path, err)
		}
		return false
	}
	// Each result's item is the interface's (store writes refs and Funcs
	// together); the result itself is decoded on first use or by the
	// walk's prefetch (lazybody.go), which checks that it names that item.
	itemOf := make(map[source.Hash]types.Key, len(ifc.Funcs))
	for _, f := range ifc.Funcs {
		itemOf[f.Entry] = f.Item
	}
	hit := &frontHit{ifc: ifc, bodies: map[types.ItemID]*bodyEntry{}}
	lazy := make([]*bodyEntry, 0, len(funcs))
	for i, b := range funcs {
		k, ok := itemOf[refs[i].key]
		if !ok {
			source.ICEf(source.Span{}, "driver: function result %d of a module hit of %s is not in its interface", i, m.path)
		}
		id, ok := w.items.ByKey(k)
		if !ok {
			source.ICEf(source.Span{}, "driver: function result %s names no item", k)
		}
		e := fc.lazyEntry(m.path, k, b)
		hit.bodies[id] = e
		lazy = append(lazy, e)
	}
	hit.lazy = lazy
	m.hit = hit
	m.api, m.shape, m.partsOK = ifc.APIHash, ifc.ShapeHash, true
	// The synthesized Target item is in no Module.Items in a cold build
	// (addTargetItem); the record lists it so it is decoded with its
	// value.
	m.mod.Items = slices.DeleteFunc(m.mod.Items, func(id types.ItemID) bool {
		return isTargetKey(w.items.Get(id).Key)
	})
	// The import lines of each file, from its parse summary (a hit's files
	// all have one: the prefix needs it), so the module's generators'
	// quotes see what a whole load binds, aliases and implicit bindings
	// included. The lines are covered by the prefix, so an edited
	// import misses before it reaches here.
	lines := make([][]resolve.ImportLine, len(m.files))
	for i, f := range m.files {
		lines[i] = w.importLines(f)
	}
	imps := make(map[string]types.ModuleID, len(m.imports))
	for p, d := range m.imports {
		if d != nil {
			imps[p] = d.id
		} else {
			imps[p] = types.NoModule // poisoned, as resolveModule passes it
		}
	}
	w.rs.LoadWith(m.id, lines, imps, m.implicit)
	return true
}

// frameIface is the KindIface payload: where each function result the
// interface names is (its bundle and key, funcbundle.go), in its order,
// then the interface record, so a hit can fetch every payload before it
// decodes anything into the session.
func frameIface(refs []funcRef, rec []byte) []byte { return frameIfaceGen(refs, rec, nil) }

// frameIfaceGen is frameIface with the stored generated file of a module
// with gen output (GenRef, genref.go's encodeGenText) after the record,
// empty for any other module.
func frameIfaceGen(refs []funcRef, rec, gen []byte) []byte {
	var e iface.Encoder
	e.Uvarint(uint64(len(refs)))
	for _, r := range refs {
		e.Hash(r.bundle)
		e.Hash(r.key)
	}
	e.Bytes(rec)
	e.Bytes(gen)
	return e.Out()
}

func unframeIface(b []byte) ([]funcRef, []byte, error) {
	refs, rec, _, err := unframeIfaceGen(b)
	return refs, rec, err
}

func unframeIfaceGen(b []byte) ([]funcRef, []byte, []byte, error) {
	d := iface.NewDecoder(b)
	n := d.Count(64)
	refs := make([]funcRef, 0, n)
	for i := 0; i < n && d.Err == nil; i++ {
		bk := d.Hash()
		refs = append(refs, funcRef{bundle: bk, key: d.Hash()})
	}
	rec := d.Bytes()
	gen := d.Bytes()
	if err := d.Done(); err != nil {
		return nil, nil, nil, err
	}
	return refs, rec, gen, nil
}

// loadHit is the stages 5 to 7 half of a module hit, once the semantics
// exist: check's Load, the bodies, the replayed diagnostics, the state.
func (b *build) loadHit(m *modNode) {
	w := b.w
	s := w.sem5
	m.cmod = s.chk.Load(m.id)
	m.cdiags = append(m.cdiags, m.cmod.Diags...)
	ids := make([]types.ItemID, 0, len(m.hit.bodies))
	for id := range m.hit.bodies {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		s.store(id, m.hit.bodies[id])
	}
	failed := false
	for i := range m.hit.ifc.Diags {
		d := m.hit.ifc.Diags[i]
		if d.Phase == diag.PhaseFlow {
			m.fdiags = append(m.fdiags, &d)
		} else {
			m.cdiags = append(m.cdiags, &d)
			failed = true
		}
	}
	m.fromCache = true
	if failed || len(m.cmod.Diags) > 0 {
		m.state.Store(modDoneFailed)
	} else {
		m.state.Store(modDoneClean)
	}
}

// store stores a module that missed: its interface and function
// results, stored under its prefix, and its part hashes for its importers.
// It runs on the module's worker after checkModule.
func (b *build) store(m *modNode) {
	w := b.w
	fc := w.front
	s := w.sem5
	items := slices.Clone(m.mod.Items)
	if s.target.item != types.NoItem && w.items.Get(s.target.item).Module == m.id {
		items = append(items, s.target.item)
	}
	var shapes []iface.ShapeRec
	if ie := source.Guard("parts "+m.path, source.Span{}, func() {
		shapes = b.shapes(items)
		m.api = s.hash.APIHash(items)
		m.shape = s.hash.ShapesHash(items, shapes)
		m.partsOK = true
	}); ie != nil {
		m.partsOK = false
	}
	if fc == nil {
		return
	}
	fc.dropBundles(m) // genref.go: m's unit lookups are done
	if !m.prefixOK {
		// Phase 1 could not compute it (an import of the prelude's closure
		// missed); every module m depends on is done now.
		m.prefix, m.prefixOK = fc.prefix(m)
	}
	// The importers need only the part hashes, so the encoding and the
	// writes run beside them (lazybody.go, background), joined at the end
	// of the stage, before the walk reads m.ifKey.
	fc.background("store "+m.path, func() {
		// At most storeSlots modules encode or wait on their writes at
		// once, so a cold build's encodings are not all in memory beside
		// the bodies (every gen module is stored).
		release := fc.storeSlot()
		b.storeEntries(m, items, shapes, release)
	})
}

// storeEntries is store's encoding and writes: the interface, its function
// results and the manifests. It reads only what m's check left, which no
// importer changes.
func (b *build) storeEntries(m *modNode, items []types.ItemID, shapes []iface.ShapeRec, release func()) {
	defer b.releaseStored(m)
	defer b.w.front.closureIndex.Delete(m) // implreads.go: m's impl reads are filled
	written := false
	defer func() {
		if !written {
			release()
		}
	}()
	w := b.w
	fc := w.front
	s := w.sem5
	if why := b.unstorable(m); why != "" {
		fc.skip(m, why)
		fc.verifyUnstored(m, why)
		return
	}
	world := b.funcWorld(m)
	reads, why := b.externalReads(m, world)
	if why != "" {
		fc.skip(m, why)
		fc.verifyUnstored(m, why)
		return
	}
	ifKey := iface.InterfaceKey(m.prefix, reads)
	var ltypes []types.TypeID
	var index map[string]types.TypeID
	for _, r := range reads {
		if r.Kind != types.ReadLayout || r.Name == "" {
			continue
		}
		if index == nil {
			index = w.namedIndex()
		}
		if t, ok := index[r.Key.String()+"\x00"+r.Name]; ok {
			ltypes = append(ltypes, t)
		} else if os.Getenv("MK_FRONT_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "front %s: layout read %s %q not in the named index\n", m.path, r.Key, r.Name)
		}
	}
	ifc := &iface.Interface{Path: m.path, Module: m.mod, Items: items, Shapes: shapes, ExternalReads: reads}
	// GenRef: the generated file's text, stored in the interface's frame,
	// and each gen line that ran naming the frame.
	var genText []byte
	if m.gen != nil && m.gen.out.File != nil && m.gen.sum != nil {
		text := m.gen.out.Text()
		genText = encodeGenText(m.gen.out.File.Path, text, m.gen.sum)
		ifc.GenText = source.HashString(genTextDomain, text)
		for _, k := range m.genRuns {
			ifc.Gens = append(ifc.Gens, iface.GenRef{Gen: k, Entry: ifKey})
		}
	}
	for _, d := range m.cdiags {
		ifc.Diags = append(ifc.Diags, *d)
	}
	for _, d := range m.fdiags {
		ifc.Diags = append(ifc.Diags, *d)
	}
	type fres struct {
		key   source.Hash
		bytes []byte
		// prefix and reads: the function manifest candidate of a unit
		// stored on its own (funccache.go), when own.
		own    bool
		prefix source.Hash
		reads  []types.Read
	}
	var outs []fres
	// refs: where each result the frame names is; a computed one's bundle
	// is filled in once every result is known (computed[i] indexes refs).
	var refs []funcRef
	var computed []int
	var keys []source.Hash
	// current: the prefixes of the module's units stored on their own,
	// computed or reused (the function index's current generation).
	var current []source.Hash
	var lt []byte
	var rec []byte
	if ie := source.Guard("store "+m.path, source.Span{}, func() {
		for _, id := range items {
			e := s.body(id)
			if e == nil {
				continue
			}
			it := w.items.Get(id)
			if u := m.unit(id); u != nil && u.reused {
				// Loaded from its own entry this build: the frame names it.
				refs = append(refs, u.entry)
				keys = append(keys, u.entry.key)
				ifc.Funcs = append(ifc.Funcs, iface.FuncRef{Item: it.Key, Entry: u.entry.key})
				if p, ok := b.funcPrefix(m, it); ok {
					current = append(current, p)
				}
				continue
			}
			if u := m.unit(id); u != nil {
				if k, p, rs, fb, ok := b.encodeFunc(m, it, e, u, world); ok {
					outs = append(outs, fres{key: k, bytes: fb, own: true, prefix: p, reads: rs})
					computed = append(computed, len(refs))
					refs = append(refs, funcRef{key: k})
					keys = append(keys, k)
					ifc.Funcs = append(ifc.Funcs, iface.FuncRef{Item: it.Key, Entry: k})
					current = append(current, p)
					continue
				}
			}
			r := &iface.FuncResult{Item: it.Key, Accept: e.state == eval.BodyReady, Body: e.mir, Borrow: encodeFlow(w, it, e)}
			fb := iface.EncodeFuncResult(fc.env, s.hash, r)
			k := funcEntryKey(ifKey, it.Key)
			outs = append(outs, fres{key: k, bytes: fb})
			computed = append(computed, len(refs))
			refs = append(refs, funcRef{key: k})
			keys = append(keys, k)
			ifc.Funcs = append(ifc.Funcs, iface.FuncRef{Item: it.Key, Entry: k})
		}
		rec = iface.EncodeInterface(fc.env, s.hash, ifc)
		if len(ltypes) > 0 {
			lt = iface.EncodeTypes(fc.env, s.hash, ltypes)
		}
	}); ie != nil {
		fc.skip(m, "encode: "+ie.What)
		return
	}
	items2 := make([]bundleItem, len(outs))
	for i, o := range outs {
		items2[i] = bundleItem{key: o.key, bytes: o.bytes}
	}
	bk := bundleKey(items2)
	for _, i := range computed {
		refs[i].bundle = bk
	}
	if m.verify != nil {
		funcs := map[source.Hash][]byte{}
		for _, o := range outs {
			funcs[o.key] = o.bytes
		}
		fc.verifyStored(m, ifKey, keys, rec, funcs)
	}
	stored := make([]storedFunc, len(outs))
	for i, o := range outs {
		stored[i] = storedFunc{key: o.key, own: o.own, prefix: o.prefix, reads: o.reads, bundle: bk}
	}
	written = true
	fc.writes("store writes "+m.path, func() {
		defer release()
		fc.storeWrites(m, ifKey, reads, stored, current, items2, lt, genText, refs, rec)
	})
}

// storedFunc is a result storeEntries computed: its key, and when it is a
// unit stored on its own, its function manifest candidate.
type storedFunc struct {
	key    source.Hash
	own    bool
	prefix source.Hash
	reads  []types.Read
	bundle source.Hash
}

// storeWrites is storeEntries' file writes, in their order: the bundle,
// the function index, the layout types, the interface frame, then the
// module manifest's candidate. It reads only the encodings and m's path
// and prefix, never what releaseStored drops, so it may run after the
// check stage (writes). m's ifKey is set once the frame is written; only
// the walk reads it (walkcache.go), after joinWrites.
func (fc *frontCache) storeWrites(m *modNode, ifKey source.Hash, reads []types.Read, outs []storedFunc, current []source.Hash, items2 []bundleItem, lt, genText []byte, refs []funcRef, rec []byte) {
	if d := fc.writeDelay; d > 0 {
		time.Sleep(d) // TestHooks.StoreWritesDelay
	}
	// One file for every result this store computed (funcbundle.go).
	if len(items2) > 0 {
		if _, ok := fc.putBundle(items2); !ok {
			return
		}
	}
	var adds []newCandidate
	for _, o := range outs {
		if o.own {
			adds = append(adds, newCandidate{prefix: o.prefix, cand: iface.Candidate{Reads: o.reads, Entry: o.key}, bundle: o.bundle})
		}
	}
	if len(current) > 0 || len(adds) > 0 {
		fc.updateFuncIndex(m, current, adds)
	}
	if lt != nil && fc.st.Put(cache.KindIface, typesKey(ifKey), lt) != nil {
		return
	}
	if fc.st.Put(cache.KindIface, ifKey, frameIfaceGen(refs, rec, genText)) != nil {
		return
	}
	m.ifKey, m.ifKeyOK = ifKey, true
	fc.noteExtReads(m, reads)
	fc.addCandidate(m.prefix, reads, ifKey)
}

// addCandidate adds (reads, entry) to the manifest under prefix, unless it
// holds that entry already. The read-modify-write is not atomic across
// processes: a lost candidate is a later miss, never a wrong hit.
func (fc *frontCache) addCandidate(prefix source.Hash, reads []types.Read, entry source.Hash) {
	man := &iface.Manifest{}
	if old, ok, _ := fc.st.Get(cache.KindManifest, prefix); ok {
		if dm, err := iface.DecodeManifest(old); err == nil {
			man = dm
		}
	}
	if slices.ContainsFunc(man.Candidates, func(c iface.Candidate) bool { return c.Entry == entry }) {
		return
	}
	man.Add(iface.Candidate{Reads: reads, Entry: entry})
	_ = fc.st.Put(cache.KindManifest, prefix, iface.EncodeManifest(man))
}

// unstorable says why a module that missed is not stored, or "".
func (b *build) unstorable(m *modNode) string {
	switch {
	case !m.prefixOK:
		return "poison: a module it depends on is not done clean"
	case !m.partsOK:
		return "no part hashes"
	case len(m.diags) > 0 || m.cmod == nil:
		return "stage 3 diagnostics"
	case len(m.cmod.Diags) > 0:
		return "stage 5 diagnostics"
	case m.evalPoison.Load():
		return "poison"
	case m.gen != nil && m.gen.out.AST != nil && (len(m.cdiags) > 0 || len(m.fdiags) > 0):
		// A hit has no gen Output to Wrap its replayed diagnostics with
		// (E0905 for one in the generated file), so a gen module with a
		// diagnostic is not stored; a clean one is (GenRef, genref.go).
		return "gen diagnostics"
	}
	m.prepMu.Lock()
	prep := len(m.prep) + len(m.prepFlow)
	m.prepMu.Unlock()
	if prep > 0 {
		return "E0000"
	}
	for _, d := range m.deps {
		if d.state.Load() != modDoneClean {
			return "poison"
		}
	}
	for _, ds := range [][]*diag.Diagnostic{m.cdiags, m.fdiags} {
		for _, d := range ds {
			switch {
			case d.Code == diag.E0000 || d.Code == diag.E0001:
				return d.Code.String()
			case len(d.Fixes) > 0:
				return "fixes"
			}
		}
	}
	return ""
}

// shapes are the shape records of the non-generic struct and enum items.
func (b *build) shapes(items []types.ItemID) []iface.ShapeRec {
	w := b.w
	var out []iface.ShapeRec
	for _, id := range items {
		it := w.items.Get(id)
		var generic bool
		switch d := it.Decl.(type) {
		case *types.StructDecl:
			generic = len(d.Generics) > 0
		case *types.EnumDecl:
			generic = len(d.Generics) > 0
		default:
			continue
		}
		if generic {
			continue
		}
		out = append(out, iface.ShapeRec{Item: id, Shape: w.sem5.chk.Shape(w.tab.Named(id, nil))})
	}
	return out
}

// externalReads are the module's reads that the prefix does not cover:
// every read of stage 5, of each unit (resolve's, its body's with the
// evaluations it ran, borrow's; funccache.go) and of each evaluation the
// module ran, minus same-module reads, minus the name, impl and member
// reads (the closure's API parts are in the prefix), minus the
// signature, shape, value and exported-fields reads of exported items
// (their API and shape parts; a ReadFields "all", which reads private
// fields too, is kept), minus Target reads (the target is in the
// context). Zero hashes (resolve, check and borrow record none) are
// filled from world; a read the session cannot answer makes the module
// not stored, and the reason names it.
func (b *build) externalReads(m *modNode, world iface.World) ([]types.Read, string) {
	w := b.w
	var rs types.ReadSet
	for _, ir := range m.cmod.Reads {
		rs.Merge(&ir.Reads)
	}
	m.readsMu.Lock()
	rs.Merge(&m.reads)
	ids := make([]types.ItemID, 0, len(m.units))
	for id := range m.units {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		rs.Merge(&m.units[id].reads)
	}
	m.readsMu.Unlock()
	var out []types.Read
	var index map[string]types.TypeID
	for _, r := range rs.Sorted() {
		switch r.Kind {
		case types.ReadExports, types.ReadImpl, types.ReadNoImpl, types.ReadNoItem, types.ReadTarget:
			continue
		case types.ReadPkg, types.ReadFile, types.ReadData:
			out = append(out, r)
			continue
		case types.ReadLayout:
			if r.Name != "" {
				if index == nil {
					index = w.namedIndex()
				}
				if t, ok := index[r.Key.String()+"\x00"+r.Name]; ok && b.ownInstance(m, t) {
					continue
				}
			}
		}
		id, ok := w.items.ByKey(r.Key)
		if ok {
			it := w.items.Get(id)
			if it.Module == m.id {
				continue
			}
			if it.Exported && r.Kind != types.ReadBody && r.Kind != types.ReadLayout && (r.Kind != types.ReadFields || r.Name != "all") {
				continue
			}
		}
		out = append(out, r)
	}
	out, bad, ok := filled(world, out)
	if !ok {
		return nil, "unanswerable read: " + bad.Kind.String() + " " + bad.Key.String() + " " + bad.Name
	}
	return out, ""
}

// ownInstance reports whether t is an instance of a generic of another
// module whose arguments name a type of m, or name no named type at all.
// Such a layout read (an evaluation of m read memory of, say,
// std.raw.Atomic[core.SpinState] or std.raw.Atomic[u32]) can never
// validate at a later lookup of the prelude: std.core is looked up before
// the semantics exist, when frontWorld answers no layout, and for an own
// argument m's items are not in the session yet either, so it does not
// decode. Kept as a read, it makes std.core miss on every build, and every
// warm check re-checks all of std.core (TestFrontWarmCheckEqualsCold and
// the TestInvalidation rows catch it). The read is covered by the prefix
// instead: the generic's module is in m's closure, whose API and shape
// hashes the prefix holds, builtin arguments have no source, and m's own
// types are m's tokens, every one of which is in the prefix too
// (FileTokens). An argument naming a private type of a third module keeps
// the read.
func (b *build) ownInstance(m *modNode, t types.TypeID) bool {
	tab := b.w.tab
	ty := tab.Get(t)
	if ty.Kind != types.KNamed || b.w.items.Get(ty.Item).Module == m.id {
		return false
	}
	own, foreign := false, false
	ok := true
	seen := map[types.TypeID]bool{}
	var walk func(types.TypeID)
	walk = func(t types.TypeID) {
		if t == types.NoType || seen[t] || !ok {
			return
		}
		seen[t] = true
		x := tab.Get(t)
		switch x.Kind {
		case types.KNamed:
			it := b.w.items.Get(x.Item)
			switch {
			case it.Module == m.id:
				own = true
			case !it.Exported:
				ok = false
			default:
				foreign = true
			}
			for _, a := range x.Args {
				walk(a.Type)
			}
		case types.KRecord:
			for _, f := range x.Fields {
				walk(f.Type)
			}
		case types.KFn:
			ok = false // not walked: keep the read
		default:
			walk(x.Elem)
		}
	}
	for _, a := range ty.Args {
		walk(a.Type)
	}
	return ok && (own || !foreign)
}

// funcEntryKey is the payload key of one function result stored with an
// interface: a function of the interface's key and the item's key, so the
// payload under a key is determined by the key.
func funcEntryKey(ifKey source.Hash, item types.Key) source.Hash {
	h := source.NewHasher("mk.driver.fres.v1")
	h.Hash(ifKey)
	h.String(item.String())
	return h.Sum()
}

// encodeFlow writes the driver's part of a body entry: the state, whether
// stage 7 accepted it, and the onerr guard decisions. borrow.EncodeFacts
// exists, but the guard decisions are all a later stage reads of
// borrow.Facts (flowCheck), so the driver stores them and not the facts they
// are derived from.
func encodeFlow(w *world, it *types.Item, e *bodyEntry) []byte {
	var enc iface.Encoder
	enc.Uvarint(uint64(e.state))
	enc.Bool(e.flowed)
	enc.Bool(e.onerr != nil)
	enc.Uvarint(uint64(len(e.onerr)))
	for _, r := range e.onerr {
		enc.Bool(r)
	}
	if canCacheBodyHash(w, it, e) {
		enc.Bool(true)
		enc.Hash(w.sem5.hash.Body(e.mir))
	} else {
		enc.Bool(false)
	}
	return enc.Out()
}

func decodeFlow(b []byte) (*bodyEntry, error) {
	d := iface.NewDecoder(b)
	e := &bodyEntry{state: eval.BodyState(d.U8())}
	e.flowed = d.Bool()
	has := d.Bool()
	n := d.Count(1)
	if has {
		e.onerr = make([]bool, n)
		for i := range e.onerr {
			e.onerr[i] = d.Bool()
		}
	}
	if d.Bool() {
		e.bodyHash = d.Hash()
		e.hasBodyHash = true
	}
	if err := d.Done(); err != nil {
		return nil, fmt.Errorf("flow facts: %w", err)
	}
	return e, nil
}
