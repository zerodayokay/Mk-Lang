// Why: validating a cached entry asks, for each read it recorded, what the
// reading module sees now; that answer must come from the loaded session
// without scanning it, and be the same whether entities were decoded or
// built fresh.
//
// What: SessionWorld (the World over a session for one reader), Fold,
// TargetValue, TargetReadHash, TypeNamespaceKey, ModuleFuncsKey, and the
// namespace parts moduleNS, memberNS, funcsNS and withAsserts.
//
// How: each read kind has a part: a module's namespace is its items, a
// type's its members as the reader sees them (mk.names), ReadTarget "abi" is
// the target name, ReadNoItem keeps the folded-name neighbourhood. Answers
// are memoized per world, and only modules in the reader's closure are read.

package iface

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// SessionWorld implements World over decoded and freshly built entities.
// It is scoped to one reading module (Reader, Closure). Safe for concurrent
// use; the namespace memo is per SessionWorld, so a World is made per
// validation round, after the modules it reads are loaded.
type SessionWorld struct {
	H   *Hasher
	Env *Env
	// Body returns nil when the body is not available yet; Part then
	// returns false, so the candidate is skipped (a miss, never a wrong hit).
	Body func(types.ItemID) *mir.Body
	// Layout returns the layout of item k applied to the generic arguments
	// spelled args (Hasher.LayoutName), the ReadLayout read's Key and Name;
	// an error item's layout has args "".
	Layout func(k types.Key, args string) (*types.Layout, bool)
	// Impl finds the impl of interface iface for the type constructor
	// spelled target (the ReadImpl read's Key and Name).
	Impl   func(iface types.Key, target string) (types.ItemID, bool)
	Target types.Target
	Pkg    func(field string) (source.Hash, bool)
	File   func(path string) (source.Hash, bool)
	// Reader is the reading module's path; Closure reports whether a module
	// is the reader or in its transitive import closure (member lists).
	Reader  string
	Closure func(module string) bool

	once     sync.Once
	modules  map[string]types.ModuleID
	mu       sync.Mutex
	implsFor map[types.ItemID][]types.ItemID // impls by the item of their For type
	refImpls map[types.ItemID][]types.ItemID // impls for &T or mut T, by T's item (memberNS)
	parts    map[partKey]partVal
	modNS    map[types.ModuleID]*namespace
	members  map[types.ItemID]*namespace
}

// partKey is what a Read asks; its Hash is what the reader saw, not part of
// the question.
type partKey struct {
	kind types.ReadKind
	key  types.Key
	name string
}

// partVal is a memoized answer of Part.
type partVal struct {
	h  source.Hash
	ok bool
}

// namespace is one memoized namespace: its names sorted, their mk.names
// hash, the names grouped by folded form (ReadNoItem's neighbourhood), and
// the exact names.
type namespace struct {
	names  []nameEntry
	hash   source.Hash
	byFold map[string][]nameEntry
	exact  map[string]bool
}

func newNamespace(ns []nameEntry) *namespace {
	slices.SortFunc(ns, compareNames)
	n := &namespace{names: ns, hash: namesHash(ns), byFold: make(map[string][]nameEntry, len(ns)), exact: make(map[string]bool, len(ns))}
	for _, e := range ns {
		f := Fold(e.name)
		n.byFold[f] = append(n.byFold[f], e)
		n.exact[e.name] = true
	}
	return n
}

// TypeNamespaceKey is the Key of a ReadNoItem or ReadExports read about the
// member namespace of type item t (a member lookup that missed, E0203): t's
// key followed by ':', which is types.MemberKey(module, type name, "") for a
// top-level type. Module paths and item keys never contain ':' (MemberKey's
// doc), so a type namespace read is never taken for a module read, even
// where module geo.Point sits beside struct Point of module geo, whose keys
// are both the text "geo.Point". A key without the ':' is a module path.
func TypeNamespaceKey(t types.Key) types.Key { return types.KeyFromString(t.String() + ":") }

// Name kinds for members that are not items.
const (
	nameField   = 100
	nameVariant = 101
)

// nameEntry is one name of a namespace: (name, kind byte, exported).
type nameEntry struct {
	name     string
	kind     uint8
	exported bool
}

func compareNames(a, b nameEntry) int {
	return cmp.Or(strings.Compare(a.name, b.name), cmp.Compare(a.kind, b.kind), boolCmp(a.exported, b.exported))
}

func boolCmp(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}

// Fold is the case-and-underscore folded form of a name (ASCII lower case,
// every '_' removed), the same folding as internal/resolve's Fold, which
// iface cannot import: the neighbourhood of a ReadNoItem is the names that
// fold equal.
func Fold(name string) string {
	folded := true
	for i := 0; i < len(name) && folded; i++ {
		folded = name[i] != '_' && (name[i] < 'A' || name[i] > 'Z')
	}
	if folded {
		return name
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// namesHash is mk.names over a namespace, sorted; the zero hash for none.
func namesHash(ns []nameEntry) source.Hash {
	if len(ns) == 0 {
		return source.Hash{}
	}
	s := slices.Clone(ns)
	slices.SortFunc(s, compareNames)
	var e Encoder
	e.Uvarint(uint64(len(s)))
	for _, n := range s {
		e.String(n.name)
		e.Uvarint(uint64(n.kind))
		e.Bool(n.exported)
	}
	return e.sum(DomNames)
}

// neighbourhood keeps the names that fold equal to x.
func neighbourhood(ns []nameEntry, x string) []nameEntry {
	fx := Fold(x)
	var out []nameEntry
	for _, n := range ns {
		if Fold(n.name) == fx {
			out = append(out, n)
		}
	}
	return out
}

// TargetValue is the value a ReadTarget read hashes for field (arch, os,
// pointer_bytes, vector_bits, or the pseudo-field abi, whose value is the
// target name); ok is false for any other field.
func TargetValue(t types.Target, field string) (string, bool) {
	switch field {
	case types.TargetArch.String():
		return t.Arch.String(), true
	case types.TargetOS.String():
		return t.OS.String(), true
	case types.TargetPointerBytes.String():
		return strconv.Itoa(int(t.PointerBytes)), true
	case types.TargetVectorBits.String():
		// Fixed per target, never from --cpu.
		return strconv.Itoa(int(t.VectorBits)), true
	case "abi":
		return t.Name, true
	}
	return "", false
}

// TargetReadHash is the Hash of a ReadTarget read of field on t.
func TargetReadHash(t types.Target, field string) (source.Hash, bool) {
	v, ok := TargetValue(t, field)
	if !ok {
		return source.Hash{}, false
	}
	return source.HashString(DomTarget, v), true
}

// init checks the world's tables and builds its module table, once.
func (w *SessionWorld) init() {
	w.once.Do(func() {
		if w.H.items != w.Env.Items {
			source.ICEf(source.Span{}, "iface: a SessionWorld's Hasher and Env are over different item tables")
		}
		w.modules = make(map[string]types.ModuleID)
		for i := 1; i < w.Env.Items.NumModules(); i++ {
			m := w.Env.Items.Module(types.ModuleID(i))
			w.modules[m.Path] = m.ID
		}
	})
}

// module and item answer only for a module the world sees (Sees): every
// read names an item of the reader or its closure. A stored
// candidate can still name a module that has left the closure but is in
// the session, maybe in its own check or decode on another goroutine; its
// items' Decls are not read, and the read misses.
func (w *SessionWorld) module(path string) (types.ModuleID, bool) {
	w.init()
	id, ok := w.modules[path]
	if !ok || !w.Sees(id) {
		return 0, false
	}
	return id, true
}

func (w *SessionWorld) item(k types.Key) (*types.Item, bool) {
	id, ok := w.Env.Items.ByKey(k)
	if !ok {
		return nil, false
	}
	it := w.Env.Items.Get(id)
	if !w.Sees(it.Module) {
		return nil, false
	}
	return it, true
}

// topLevel reports whether an item is a name of its module's namespace, by
// its current Owner and OwnerPrim (resolve sets both after AddItem).
func topLevel(it *types.Item) bool {
	if it.Owner != types.NoItem || it.Name == source.NoSymbol || it.OwnerPrim != types.NoType {
		return false
	}
	switch it.Kind {
	case types.KindStruct, types.KindEnum, types.KindError, types.KindInterface, types.KindAlias,
		types.KindConst, types.KindGlobal, types.KindFunc:
		return true
	}
	return false
}

// moduleNS is a module's namespace, its top-level named items, memoized per
// world. It costs the module's size: the items come from the Hasher's
// session-wide index, never from a scan of the session.
func (w *SessionWorld) moduleNS(m types.ModuleID) *namespace {
	w.mu.Lock()
	n, ok := w.modNS[m]
	w.mu.Unlock()
	if ok {
		return n
	}
	ids := w.H.moduleItems(m)
	ns := make([]nameEntry, 0, len(ids))
	for _, id := range ids {
		if it := w.Env.Items.Get(id); topLevel(it) {
			ns = append(ns, nameEntry{name: w.Env.Interner.Str(it.Name), kind: uint8(it.Kind), exported: it.Exported})
		}
	}
	n = newNamespace(ns)
	w.mu.Lock()
	if w.modNS == nil {
		w.modNS = make(map[types.ModuleID]*namespace)
	}
	w.modNS[m] = n
	w.mu.Unlock()
	return n
}

// impls is the impls whose For type is item t. The grouping is built once per
// world from the session's impl items: an impl's Decl is the checker's, so it
// is read when the world is first asked, not when the item is indexed. Only
// the impls of modules the world sees (Sees) are read: any other module may be
// in its own check or decode on another goroutine, writing its impls' Decls.
// Reading them would race with that writer, and a Decl read while it is
// written can be a non-nil type with a nil pointer, a nil dereference here. No
// part changes for a program check accepts: coherence accepts an unsafe impl
// Send or Share only in its type's module, and memberNS keeps only impls of
// the type's module and the reader's closure, which holds the type's module.
// An orphan impl outside the closure is rejected in its own module, which is
// then not done clean, so no stored entry depends on it.
func (w *SessionWorld) impls(t types.ItemID) []types.ItemID {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.indexImpls()
	return w.implsFor[t]
}

// refImplsOf is the impls whose For type is a reference to item t (`impl
// Sink for &V`, `for mut V`). memberNS hashes their methods into t's
// namespace, since borrow's view scan reads them as t's (typewalk.go
// implsFor); withAsserts does not read them.
func (w *SessionWorld) refImplsOf(t types.ItemID) []types.ItemID {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.indexImpls()
	return w.refImpls[t]
}

// indexImpls builds implsFor and refImpls once per world, from the impls of
// the modules the world sees only. Called with w.mu held.
func (w *SessionWorld) indexImpls() {
	if w.implsFor != nil {
		return
	}
	w.implsFor = make(map[types.ItemID][]types.ItemID)
	w.refImpls = make(map[types.ItemID][]types.ItemID)
	for _, id := range w.H.implItems() {
		it := w.Env.Items.Get(id)
		if !w.Sees(it.Module) {
			continue
		}
		im, ok := it.Decl.(*types.ImplDecl)
		if !ok || im == nil || im.For == types.NoType {
			continue
		}
		ft := w.Env.Types.Get(im.For)
		switch ft.Kind {
		case types.KNamed:
			w.implsFor[ft.Item] = append(w.implsFor[ft.Item], id)
		case types.KRef, types.KMut:
			if et := w.Env.Types.Get(ft.Elem); et.Kind == types.KNamed {
				w.refImpls[et.Item] = append(w.refImpls[et.Item], id)
			}
		}
	}
}

// Sees reports whether module mod is the reader or in its closure, the
// modules whose items are settled while the world is asked (a World is
// made after the modules it reads are loaded); with no Closure, every
// module. An item's Module and its module's Path are fixed when it is
// added, so reading them races with nothing.
func (w *SessionWorld) Sees(mod types.ModuleID) bool {
	if w.Closure == nil {
		return true
	}
	p := w.Env.Items.Module(mod).Path
	return p == w.Reader || w.Closure(p)
}

// withAsserts is the shape part of item t, read when a type's shape, kind,
// Send, Share or Freeze is used: shape, the hash of its
// shape term or shape, extended by the impl hash of every unsafe impl Send
// or Share whose For type is t. A generic term cannot show what such an
// impl asserts (TermMarks only say "never Send"): `unsafe impl[T: Send]
// Send for Box[T]` changed to `[T: Share]`, or removed, leaves Box's term as
// it was, yet changes Box[P]'s Send. An item with no unsafe impl keeps the
// plain hash, so its stored reads stay valid. The impls are taken in key
// order (TestShapePartCoversUnsafeImpls).
func (w *SessionWorld) withAsserts(t types.ItemID, shape source.Hash) source.Hash {
	var asserts []*types.Item
	for _, id := range w.impls(t) {
		it := w.Env.Items.Get(id)
		if d, ok := it.Decl.(*types.ImplDecl); ok && d.Unsafe &&
			(d.Bound.Kind == types.BoundSend || d.Bound.Kind == types.BoundShare) {
			asserts = append(asserts, it)
		}
	}
	if len(asserts) == 0 {
		return shape
	}
	slices.SortFunc(asserts, func(a, b *types.Item) int { return a.Key.Compare(b.Key) })
	var e Encoder
	e.Hash(shape)
	e.Uvarint(uint64(len(asserts)))
	for _, it := range asserts {
		e.Hash(w.H.Impl(it))
	}
	return e.sum(DomShape)
}

// memberNS is type t's namespace as the reader sees it, memoized per world:
// its fields (exported only for a foreign reader), variants, interface
// methods, Type.Name methods, and the methods of every impl of it, or of a
// reference to it (refImplsOf), defined in its own module or in a module of
// the reader's closure.
func (w *SessionWorld) memberNS(t *types.Item) *namespace {
	w.mu.Lock()
	n, ok := w.members[t.ID]
	w.mu.Unlock()
	if ok {
		return n
	}
	var ns []nameEntry
	in := w.Env.Interner
	tmod := w.Env.Items.Module(t.Module).Path
	foreign := tmod != w.Reader
	fields := func(fs []types.FieldDecl) {
		for _, f := range fs {
			if !foreign || f.Exported {
				ns = append(ns, nameEntry{name: in.Str(f.Name), kind: nameField, exported: f.Exported})
			}
		}
	}
	switch d := t.Decl.(type) {
	case *types.StructDecl:
		fields(d.Fields)
	case *types.ErrorDecl:
		fields(d.Fields)
	case *types.EnumDecl:
		for _, v := range d.Variants {
			ns = append(ns, nameEntry{name: in.Str(v.Name), kind: nameVariant, exported: true})
		}
	case *types.InterfaceDecl:
		for _, m := range d.Methods {
			ns = append(ns, nameEntry{name: in.Str(m.Name), kind: uint8(types.KindFunc), exported: true})
		}
	}
	for _, id := range w.H.methodItems(in.Str(t.Name)) {
		// check accepts a Type.Name method only in its type's module (E0704
		// otherwise; resolve's Session.MethodsOf lists only those), and
		// resolve sets Owner after AddItem: another module's candidate,
		// maybe being resolved now, is skipped by its Module before its
		// Owner is read.
		if it := w.Env.Items.Get(id); it.Module == t.Module && it.Owner == t.ID && it.Name != source.NoSymbol {
			ns = append(ns, nameEntry{name: in.Str(it.Name), kind: uint8(types.KindFunc), exported: it.Exported})
		}
	}
	for _, id := range append(slices.Clone(w.impls(t.ID)), w.refImplsOf(t.ID)...) {
		it := w.Env.Items.Get(id)
		if p := w.Env.Items.Module(it.Module).Path; p != tmod && p != w.Reader && (w.Closure == nil || !w.Closure(p)) {
			continue
		}
		for _, m := range it.Decl.(*types.ImplDecl).Methods {
			ns = append(ns, nameEntry{name: in.Str(w.Env.Items.Get(m).Name), kind: uint8(types.KindFunc), exported: true})
		}
	}
	n = newNamespace(ns)
	w.mu.Lock()
	if w.members == nil {
		w.members = make(map[types.ItemID]*namespace)
	}
	w.members[t.ID] = n
	w.mu.Unlock()
	return n
}

// namespace resolves a ReadNoItem or ReadExports key: a TypeNamespaceKey
// names a type's member namespace, any other key a module's namespace.
func (w *SessionWorld) namespace(k types.Key) (*namespace, bool) {
	if tk, isType := strings.CutSuffix(k.String(), ":"); isType {
		it, ok := w.item(types.KeyFromString(tk))
		if !ok {
			return nil, false
		}
		switch it.Kind {
		case types.KindStruct, types.KindEnum, types.KindError, types.KindInterface:
			return w.memberNS(it), true
		}
		return nil, false
	}
	if m, ok := w.module(k.String()); ok {
		return w.moduleNS(m), true
	}
	return nil, false
}

// Part implements World. Answers are memoized per (Kind, Key, Name) for the
// world's lifetime, so a read asked again in one validation round is a map
// lookup; a World is made per (reader, validation round), after the modules
// it reads are loaded.
func (w *SessionWorld) Part(r types.Read) (source.Hash, bool) {
	k := partKey{r.Kind, r.Key, r.Name}
	w.mu.Lock()
	v, ok := w.parts[k]
	w.mu.Unlock()
	if ok {
		return v.h, v.ok
	}
	v.h, v.ok = w.part(r)
	w.mu.Lock()
	if w.parts == nil {
		w.parts = make(map[partKey]partVal)
	}
	w.parts[k] = v
	w.mu.Unlock()
	return v.h, v.ok
}

func (w *SessionWorld) part(r types.Read) (source.Hash, bool) {
	w.init()
	h := w.H
	switch r.Kind {
	case types.ReadSignature:
		it, ok := w.item(r.Key)
		if !ok || it.Decl == nil {
			return source.Hash{}, false
		}
		return h.Decl(it), true
	case types.ReadShape:
		it, ok := w.item(r.Key)
		if !ok {
			return source.Hash{}, false
		}
		switch d := it.Decl.(type) {
		case *types.StructDecl:
			if d.Shape != nil {
				return w.withAsserts(it.ID, h.ShapeTerm(d.Shape)), true
			}
		case *types.EnumDecl:
			if d.Shape != nil {
				return w.withAsserts(it.ID, h.ShapeTerm(d.Shape)), true
			}
		}
		if w.Layout != nil {
			if l, ok := w.Layout(r.Key, ""); ok {
				return w.withAsserts(it.ID, h.Shape(l.Shape)), true
			}
		}
		return source.Hash{}, false
	case types.ReadFields:
		it, ok := w.item(r.Key)
		if !ok {
			return source.Hash{}, false
		}
		return h.Fields(it, r.Name == "all")
	case types.ReadValue:
		it, ok := w.item(r.Key)
		if !ok {
			return source.Hash{}, false
		}
		switch d := it.Decl.(type) {
		case *types.ConstDecl:
			if d.HasValue {
				return h.Const(d.Value), true
			}
		case *types.GlobalDecl:
			if d.HasValue {
				return h.Const(d.Value), true
			}
		}
		return source.Hash{}, false
	case types.ReadBody:
		it, ok := w.item(r.Key)
		if !ok || w.Body == nil {
			return source.Hash{}, false
		}
		b := w.Body(it.ID)
		if b == nil {
			return source.Hash{}, false
		}
		return h.Body(b), true
	case types.ReadLayout:
		if w.Layout == nil {
			return source.Hash{}, false
		}
		l, ok := w.Layout(r.Key, r.Name)
		if !ok {
			return source.Hash{}, false
		}
		return h.Layout(l), true
	case types.ReadImpl, types.ReadNoImpl:
		if w.Impl == nil {
			return source.Hash{}, false
		}
		id, ok := w.Impl(r.Key, r.Name)
		if !ok {
			return source.Hash{}, false
		}
		if r.Kind == types.ReadNoImpl {
			return source.Hash{}, true
		}
		return h.Impl(w.Env.Items.Get(id)), true
	case types.ReadNoItem:
		n, ok := w.namespace(r.Key)
		if !ok {
			// The module or type itself is gone: the read no longer holds.
			return source.Hash{}, true
		}
		return namesHash(n.byFold[Fold(r.Name)]), n.exact[r.Name]
	case types.ReadExports:
		// Name "" (foreign) or "all" (same module) only says which view the
		// reader had; memberNS already computes the reader's view.
		n, ok := w.namespace(r.Key)
		if !ok {
			return source.Hash{}, false
		}
		return n.hash, true
	case types.ReadTarget:
		return TargetReadHash(w.Target, r.Name)
	case types.ReadPkg:
		if w.Pkg == nil {
			return source.Hash{}, false
		}
		return w.Pkg(r.Name)
	case types.ReadFile:
		if w.File == nil {
			return source.Hash{}, false
		}
		return w.File(r.Name)
	}
	source.ICEf(source.Span{}, "iface: read kind %v", r.Kind)
	return source.Hash{}, false
}
