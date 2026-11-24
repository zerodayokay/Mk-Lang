// Why: a warm or edit build should generate IR only for the packs whose
// inputs changed; keying a pack by its IR means generating the IR first.
//
// What: the pack key before emission: preKeys and prekeysFor, unitDigest
// (the part every pack reads), packWorld (the deep layout parts of pack
// keys), the memo entries (memoGet, memoPut, memoVerify) and PackEvent for
// tests.
//
// How: a pack whose pre-emission key names a stored object is not
// generated at all. The object store stays keyed by the IR (objKey); a
// small memo entry maps a pack's pre-emission key to the object key its IR
// had, so a hit has the bytes a miss would have made, and MK_CACHE_VERIFY
// can check the memo against the IR it emits anyway.

package driver

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"mk/internal/cache"
	"mk/internal/codegen"
	"mk/internal/diag"
	"mk/internal/iface"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// preKeys is one bin's state for pack keys before emission: nil when no
// pack of the build may be keyed so (prekeysFor).
type preKeys struct {
	w     *world
	tools *toolchain.Tools
	unit  source.Hash // unitDigest
	world *packWorld
}

// prekeysFor is the bin's pre-emission key state, or nil when every pack
// must be emitted: no store or no build ID (nothing to look up), no front
// cache context, --emit-ll or the OnLL hook (they want every pack's IR), or
// an ICE while digesting the unit. Under MK_CACHE_VERIFY the keys are formed
// and every pack is emitted, so the memo is checked against the IR
// (emitPacks).
func (w *world) prekeysFor(u *codegen.Unit, tools *toolchain.Tools, st *cache.Store, llDir string) *preKeys {
	if st == nil || w.buildID().IsZero() || w.front == nil || w.sem5 == nil || llDir != "" || w.cfg.TestHooks.OnLL != nil {
		return nil
	}
	pk := &preKeys{w: w, tools: tools, world: newPackWorld(w)}
	if ie := source.Guard("pack keys", source.Span{}, func() { pk.unit = w.unitDigestInto(u, pk.world.decls) }); ie != nil {
		return nil
	}
	return pk
}

// unitDigest is the part of every pack's key that reads the whole unit:
// per Func in unit order its symbol, key, item key, whether it has a body,
// AddrTaken, Spawned and Local, and, for an item outside std, its
// mk.decl.v1 hash (signature, cpu clause, naked, extern: what a call or
// declare of it prints); per Global its key, declaration, type and value.
// A std item's declaration is in the front cache's context instead (the
// build ID with an embedded std, every std file's hash under MK_STD). So
// an edit that adds or removes an instance, or changes a flag of one,
// misses every pack; an edit of one body keeps the others.
func (w *world) unitDigest(u *codegen.Unit) source.Hash {
	return w.unitDigestInto(u, map[types.ItemID]source.Hash{})
}

func (w *world) unitDigestInto(u *codegen.Unit, decls map[types.ItemID]source.Hash) source.Hash {
	hs := w.sem5.hash
	h := source.NewHasher("mk.driver.prekey.unit.v1")
	h.Uvarint(uint64(len(u.Funcs)))
	for i := range u.Funcs {
		f := &u.Funcs[i]
		it := w.items.Get(f.Item)
		h.String(f.Symbol)
		h.String(f.Key)
		h.String(it.Key.String())
		h.Bool(f.Body == nil)
		h.Bool(f.AddrTaken)
		h.Bool(f.Spawned)
		h.Bool(f.Local)
		if isStdPath(w.items.Module(it.Module).Path) {
			h.Bool(false)
			continue
		}
		d, ok := decls[f.Item]
		if !ok {
			d = hs.Decl(it)
			decls[f.Item] = d
		}
		h.Bool(true)
		h.Hash(d)
	}
	h.Uvarint(uint64(len(u.Globals)))
	for _, gl := range u.Globals {
		it := w.items.Get(gl.Item)
		h.String(it.Key.String())
		d, ok := decls[gl.Item]
		if !ok {
			d = hs.Decl(it)
			decls[gl.Item] = d
		}
		h.Hash(d)
		switch d := it.Decl.(type) {
		case *types.GlobalDecl:
			h.Hash(hs.Type(d.Type))
			h.Bool(d.HasValue)
			if d.HasValue {
				h.Hash(hs.Const(d.Value))
			}
		case *types.ConstDecl:
			h.Hash(hs.Type(d.Type))
			h.Bool(d.HasValue)
			if d.HasValue {
				h.Hash(hs.Const(d.Value))
			}
		}
	}
	return h.Sum()
}

// isStdPath reports whether a module path is std's.
func isStdPath(p string) bool { return p == "std" || strings.HasPrefix(p, "std.") }

// key is pack i's pre-emission key, false when it has none (codegen's
// PackFacts refuses the configuration, or an ICE while hashing: the pack
// is then emitted, never wrongly hit). It is the object key's options
// (build ID, toolchain, triple, CPU, -O level, -flto), the front cache's
// context, the unit digest, codegen's PackFacts, each member's
// InstanceContentHash over packWorld's layouts, and packWorld's layout of
// every type codegen.PackConstTypes reports.
func (pk *preKeys) key(u *codegen.Unit, i int, cfg codegen.Config, job toolchain.CompileJob) (source.Hash, bool) {
	w := pk.w
	var out source.Hash
	ok := false
	ie := source.Guard("pack key", source.Span{}, func() {
		h := source.NewHasher("mk.driver.prekey.v1")
		h.Hash(w.buildID())
		h.Hash(pk.tools.Identity())
		h.String(job.Triple)
		h.String(job.CPU)
		h.Uvarint(uint64(job.Opt))
		h.Bool(job.LTO)
		if k := job.CompileKey(); k != "" {
			h.String(k) // as objKey
		}
		h.Hash(w.front.ctx)
		h.Hash(pk.unit)
		if !codegen.PackFacts(u, i, cgEnv{w}, cfg, h) {
			return
		}
		for _, fi := range u.Packs[i] {
			b := u.Funcs[fi].Body
			if b == nil {
				return
			}
			h.Hash(w.sem5.hash.InstanceContentHash(pk.world, b))
		}
		// The layouts the pack's consts read: a defined global's initializer
		// and a const a member names are hashed by value, their types by
		// name, and a function they relocate may be a wrapper the pack
		// defines over its type arguments' layouts.
		h.String("consts")
		codegen.PackConstTypes(u, i, cgEnv{w}, cfg, func(t types.TypeID) {
			part, _ := pk.world.LayoutPart(t, types.NoItem)
			h.Hash(part)
		})
		out, ok = h.Sum(), true
	})
	if ie != nil {
		return source.Hash{}, false
	}
	return out, ok
}

// prekeyMemo is the payload prefix of a memo entry, the object key follows.
// The entry is cache.KindManifest, the mutable kind, keyed by the
// pre-emission key: a wrong entry (found by MK_CACHE_VERIFY) or one whose
// object GC took is rewritten in place, and the last write wins, as for
// the other candidate lists.
const prekeyMemo = "mk prekey v1\n"

// memo is the object key the store maps pre to.
func memoGet(st *cache.Store, pre source.Hash) (source.Hash, bool) {
	b, ok, _ := st.Get(cache.KindManifest, pre)
	if !ok || len(b) != len(prekeyMemo)+len(source.Hash{}) || string(b[:len(prekeyMemo)]) != prekeyMemo {
		return source.Hash{}, false
	}
	return source.Hash(b[len(prekeyMemo):]), true
}

// memoPut maps pre to obj; an error is ignored (the cache is an
// optimization).
func memoPut(st *cache.Store, pre, obj source.Hash) {
	_ = st.Put(cache.KindManifest, pre, append([]byte(prekeyMemo), obj[:]...))
}

// memoVerify is MK_CACHE_VERIFY's check of a memo entry against the object
// key of the IR just emitted: nil when they agree or there is no entry,
// else E0000 "cache verify" (the entry is replaced by the caller).
func memoVerify(st *cache.Store, pre, obj source.Hash) *diag.Diagnostic {
	if old, ok := memoGet(st, pre); ok && old != obj {
		return diag.FromInternal(&source.InternalError{Where: "cache verify",
			What: fmt.Sprintf("prekey %s: the stored object key %s differs from the emitted IR's %s", pre, old, obj)})
	}
	return nil
}

// packWorld is the iface.LayoutWorld of pack keys. A named type's layout
// part is deep: the sorted facts of the type and of every named type and
// error it reaches through any constructor, by value or behind a
// reference, each its mk.ty.v1 hash, its mk.layout.v1 hash (check's
// layout, which codegen reads through Env.Layout), its mk.decl.v1 hash
// (an enum's signed tag, an error's fields) and its Facts. Codegen reads
// layouts below the types a body names (holdsNonnull and the constant
// walk follow by-value fields; a pointee's size and Freeze reach
// dereferenceable and noalias), so a shallow layout would keep a key whose
// IR changed. An error item's part is the same closure from its fields.
type packWorld struct {
	w       *world
	mu      sync.RWMutex
	shallow map[types.TypeID]source.Hash
	errs    map[types.ItemID]source.Hash
	deep    map[types.TypeID]source.Hash
	deepErr map[types.ItemID]source.Hash
	decls   map[types.ItemID]source.Hash
}

func newPackWorld(w *world) *packWorld {
	return &packWorld{w: w, shallow: map[types.TypeID]source.Hash{}, errs: map[types.ItemID]source.Hash{},
		deep: map[types.TypeID]source.Hash{}, deepErr: map[types.ItemID]source.Hash{},
		decls: map[types.ItemID]source.Hash{}}
}

// DeclHash memoizes Hasher.Decl for frozen declarations during emitPacks.
func (pw *packWorld) DeclHash(it *types.Item) source.Hash {
	pw.mu.RLock()
	h, ok := pw.decls[it.ID]
	pw.mu.RUnlock()
	if ok {
		return h
	}
	h = pw.w.sem5.hash.Decl(it)
	pw.mu.Lock()
	pw.decls[it.ID] = h
	pw.mu.Unlock()
	return h
}

// BodyHash returns a cached mk.body hash stored alongside a FuncResult when b
// is an unchanged non-generic body (and MK_CACHE_VERIFY is off), letting
// InstanceContentHash skip re-walking unchanged MIR bodies during emitPacks.
func (pw *packWorld) BodyHash(b *mir.Body) (source.Hash, bool) {
	if b == nil || pw.w.cfg.CacheVerify || pw.w.sem5 == nil {
		return source.Hash{}, false
	}
	if e := pw.w.sem5.body(b.Item); e != nil && e.mir == b && e.hasBodyHash {
		return e.bodyHash, true
	}
	return source.Hash{}, false
}

// Part answers no read by key: InstanceContentHash asks LayoutPart.
func (pw *packWorld) Part(r types.Read) (source.Hash, bool) {
	source.ICEf(source.Span{}, "driver: pack key world asked read %v %s by key", r.Kind, r.Key)
	return source.Hash{}, false
}

// maxLayoutPartRecur bounds how deep one constructor nests in itself on a
// path of a deep layout part's walk, as check's maxRecur bounds its facts
// walk; maxLayoutPartInstances bounds the instances of one constructor the
// walk follows into, as borrow's maxReferents bounds its walks.
const (
	maxLayoutPartRecur     = 64
	maxLayoutPartInstances = 4096
)

// LayoutPart is the deep layout part of t, or of the error e.
func (pw *packWorld) LayoutPart(t types.TypeID, e types.ItemID) (source.Hash, bool) {
	pw.mu.RLock()
	if t != types.NoType {
		if h, ok := pw.deep[t]; ok {
			pw.mu.RUnlock()
			return h, true
		}
	} else if h, ok := pw.deepErr[e]; ok {
		pw.mu.RUnlock()
		return h, true
	}
	pw.mu.RUnlock()
	var parts []source.Hash
	seenT := map[types.TypeID]bool{}
	seenE := map[types.ItemID]bool{}
	onPath := map[types.ItemID]int{} // instances of a constructor on the walk's current path
	met := map[types.ItemID]int{}    // instances of a constructor met in the whole walk
	tab := pw.w.tab
	var visit func(t types.TypeID)
	var visitErr func(e types.ItemID)
	errSet := func(s types.ErrorSetID) {
		for _, m := range tab.Members(s) {
			if !m.IsParam {
				visitErr(m.Item)
			}
		}
	}
	visitErr = func(e types.ItemID) {
		if e == types.NoItem || seenE[e] {
			return
		}
		seenE[e] = true
		parts = append(parts, pw.errFact(e))
		if d, ok := pw.w.items.Get(e).Decl.(*types.ErrorDecl); ok {
			for _, f := range d.Fields {
				visit(f.Type)
			}
		}
	}
	visit = func(t types.TypeID) {
		if t == types.NoType || t == types.Poison || seenT[t] {
			return
		}
		seenT[t] = true
		ty := tab.Get(t)
		switch ty.Kind {
		case types.KNamed:
			parts = append(parts, pw.namedFact(t))
			for _, a := range ty.Args {
				if !a.IsConst {
					visit(a.Type)
				} else if a.Const.Kind == types.CASizeOf {
					visit(a.Const.Type)
				}
			}
			// A polymorphically recursive type (struct G[T] { next:
			// ?&G[?T] }) reaches a new instance of itself through its
			// fields at every hop, so seenT never repeats. Such an
			// instance's fields are its declaration's, walked at the
			// instances above it, over arguments walked just now: once a
			// constructor nests in itself maxLayoutPartRecur deep on one
			// path, or the walk has met maxLayoutPartInstances of it (two
			// such fields branch), the walk stops at that instance's
			// fields. A root that reaches many unnested instances, such as
			// List[A] and List[B], never stops; the bounds and the walk
			// order are fixed, so the key stays deterministic.
			met[ty.Item]++
			if onPath[ty.Item] >= maxLayoutPartRecur || met[ty.Item] > maxLayoutPartInstances {
				return
			}
			onPath[ty.Item]++
			defer func() { onPath[ty.Item]-- }()
			l := pw.w.sem5.chk.Layout(t)
			for _, f := range l.Fields {
				visit(f.Type)
			}
			for _, v := range l.Variants {
				for _, f := range v.Fields {
					visit(f.Type)
				}
			}
		case types.KRecord:
			for _, f := range ty.Fields {
				visit(f.Type)
			}
		case types.KFn:
			for _, p := range ty.Fn.Params {
				visit(p.Type)
			}
			visit(ty.Fn.Result)
			errSet(ty.Fn.Errors)
		case types.KArray:
			if ty.Len.Kind == types.CASizeOf {
				visit(ty.Len.Type)
			}
			visit(ty.Elem)
		case types.KErrorUnion:
			errSet(ty.Errors)
			visit(ty.Elem)
		case types.KErrorSet:
			errSet(ty.Errors)
		default:
			visit(ty.Elem)
		}
	}
	if t != types.NoType {
		visit(t)
	} else {
		visitErr(e)
	}
	slices.SortFunc(parts, func(a, b source.Hash) int { return a.Compare(b) })
	parts = slices.Compact(parts)
	h := source.NewHasher("mk.driver.prekey.layout.v1")
	h.Uvarint(uint64(len(parts)))
	for _, p := range parts {
		h.Hash(p)
	}
	out := h.Sum()
	pw.mu.Lock()
	if t != types.NoType {
		pw.deep[t] = out
	} else {
		pw.deepErr[e] = out
	}
	pw.mu.Unlock()
	return out, true
}

// namedFact is one named type's own facts in a deep part.
func (pw *packWorld) namedFact(t types.TypeID) source.Hash {
	pw.mu.RLock()
	h, ok := pw.shallow[t]
	pw.mu.RUnlock()
	if ok {
		return h
	}
	w, hs := pw.w, pw.w.sem5.hash
	l := w.sem5.chk.Layout(t)
	it := w.items.Get(w.tab.Get(t).Item)
	x := source.NewHasher("mk.driver.prekey.named.v1")
	x.Hash(hs.Type(t))
	x.Hash(hs.Layout(&l))
	x.Hash(pw.DeclHash(it))
	x.String(fmt.Sprintf("%+v", w.sem5.chk.Facts(t)))
	h = x.Sum()
	pw.mu.Lock()
	pw.shallow[t] = h
	pw.mu.Unlock()
	return h
}

// errFact is one error item's own facts in a deep part: its key and its
// mk.decl.v1 hash.
func (pw *packWorld) errFact(e types.ItemID) source.Hash {
	pw.mu.RLock()
	h, ok := pw.errs[e]
	pw.mu.RUnlock()
	if ok {
		return h
	}
	it := pw.w.items.Get(e)
	x := source.NewHasher("mk.driver.prekey.error.v1")
	x.String(it.Key.String())
	x.Hash(pw.DeclHash(it))
	h = x.Sum()
	pw.mu.Lock()
	pw.errs[e] = h
	pw.mu.Unlock()
	return h
}

// The pack key world must answer every layout InstanceContentHash asks.
var _ iface.LayoutWorld = (*packWorld)(nil)

// PackEvent is one pack of a native build as emitPacks decided it
// (TestHooks.OnPack).
type PackEvent struct {
	// Name is the pack's file name, p0000 to pNNNN, or support.
	Name string
	// First is the key text of the pack's first member; "" for support.
	First string
	// Pre is its pre-emission key; zero when HasPre is false (the support
	// pack, no store, --emit-ll, OnLL, --debug, --pgo, --pgo-gen).
	Pre    source.Hash
	HasPre bool
	// Obj is its object key: the memo's on a hit, the IR's when emitted.
	Obj source.Hash
	// Emitted: codegen generated its IR in this build.
	Emitted bool
}

// packEvent records a pack's codegen decision: emitted packs go to
// Trace.PacksEmitted, and OnPack sees every one.
func (w *world) packEvent(name, first string, pre source.Hash, hasPre bool, obj source.Hash, emitted bool) {
	if emitted {
		w.packs.mu.Lock()
		w.packs.emitted = append(w.packs.emitted, name)
		w.packs.mu.Unlock()
	}
	if h := w.cfg.TestHooks.OnPack; h != nil {
		h(PackEvent{Name: name, First: first, Pre: pre, HasPre: hasPre, Obj: obj, Emitted: emitted})
	}
}

// firstKey is the key text of pack i's first member, "" for the support
// pack (i == len(u.Packs)).
func firstKey(u *codegen.Unit, i int) string {
	if i >= len(u.Packs) || len(u.Packs[i]) == 0 {
		return ""
	}
	return u.Funcs[u.Packs[i][0]].Key
}
