// Why: every `x.name` needs the member namespace of x's type: its fields,
// variants, Type.Name functions and impl methods, from this module and the
// modules it sees.
//
// What: Member and memberSet, memberPass (the members index), placeMethod
// (where methods live, the orphan-method E0704), lookup, Members,
// visibleMembers and collectMembers.
//
// How: one namespace holds fields and methods; each set is indexed by exact
// name and by folded name, so a near miss is found. A Type.Name function
// joins its constructor's set only when that type is declared in the same
// module (a builtin type takes methods only from std).

package check

import (
	"slices"
	"sort"

	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// MemberKind tags a Member.
type MemberKind uint8

const (
	MemberNone MemberKind = iota
	// MemberField is a struct's (or error's) field; Field indexes Fields.
	MemberField
	// MemberFunc is a Type.Name function of the constructor's module.
	MemberFunc
	// MemberImplMethod is a method of an impl for the constructor.
	MemberImplMethod
	// MemberVariant is an enum variant; Field indexes Variants.
	MemberVariant
)

// Member is one entry of a type's member namespace (one namespace for
// fields and methods, E0206).
type Member struct {
	Name     source.Symbol
	Kind     MemberKind
	Field    uint32 // index into the struct's Fields or the enum's Variants
	Item     types.ItemID
	Exported bool
}

// memberSet is the declared members of one constructor: fields or variants
// and Type.Name functions (impl methods are found through the impl tables,
// since an impl may live in the interface's module). list is sorted by name
// bytes; exact and folded are lookups only, never iterated into output.
// folded is keyed by resolve.MemberKey, so a field and a method whose names
// fold alike are two entries.
type memberSet struct {
	list   []Member
	spans  []source.Span // parallel to list
	exact  map[source.Symbol]int32
	folded map[string]int32
}

func newMemberSet() *memberSet {
	return &memberSet{exact: map[source.Symbol]int32{}, folded: map[string]int32{}}
}

// add appends a member unless its key or its exact name is taken: that
// collision was resolve's E0206 and the later entry is absent.
func (s *memberSet) add(in *source.Interner, mb Member, at source.Span) {
	f := resolve.MemberKey(in.Str(mb.Name), mb.Kind == MemberField)
	if _, dup := s.folded[f]; dup {
		return
	}
	if _, dup := s.exact[mb.Name]; dup {
		return
	}
	s.list = append(s.list, mb)
	s.spans = append(s.spans, at)
	s.folded[f] = int32(len(s.list) - 1)
	s.exact[mb.Name] = int32(len(s.list) - 1)
}

// sortList orders the list by name bytes and rebuilds the indexes.
func (s *memberSet) sortList(in *source.Interner) {
	idx := make([]int, len(s.list))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return in.Str(s.list[idx[a]].Name) < in.Str(s.list[idx[b]].Name) })
	list, spans := make([]Member, len(idx)), make([]source.Span, len(idx))
	for i, j := range idx {
		list[i], spans[i] = s.list[j], s.spans[j]
		s.exact[list[i].Name] = int32(i)
		s.folded[resolve.MemberKey(in.Str(list[i].Name), list[i].Kind == MemberField)] = int32(i)
	}
	s.list, s.spans = list, spans
}

// memberPass is the module's member step: a member set for every type constructor of
// the module, the builtin types' members when this is a std module, and the
// orphan-method E0704, whose only emitter is check.
func (m *Module) memberPass() {
	c := m.c
	in := c.cfg.Interner
	ids := m.itemIDs()
	for _, iid := range ids {
		it := c.cfg.Items.Get(iid)
		m.guard(it, func() {
			s := newMemberSet()
			switch d := it.Decl.(type) {
			case *types.StructDecl:
				for i, f := range d.Fields {
					s.add(in, Member{Name: f.Name, Kind: MemberField, Field: uint32(i), Item: it.ID, Exported: f.Exported}, f.Span)
				}
			case *types.EnumDecl:
				for i, v := range d.Variants {
					s.add(in, Member{Name: v.Name, Kind: MemberVariant, Field: uint32(i), Item: it.ID, Exported: it.Exported}, v.Span)
				}
			case *types.ErrorDecl:
				for i, f := range d.Fields {
					s.add(in, Member{Name: f.Name, Kind: MemberField, Field: uint32(i), Item: it.ID, Exported: f.Exported}, f.Span)
				}
			case *types.InterfaceDecl:
			default:
				return
			}
			m.members[it.ID] = s
		})
	}
	for _, iid := range ids {
		it := c.cfg.Items.Get(iid)
		if it.Kind != types.KindFunc || (it.Owner == types.NoItem && it.OwnerPrim == types.NoType) {
			continue
		}
		m.guard(it, func() { m.placeMethod(it) })
	}
	for _, s := range m.members {
		s.sortList(in)
	}
	for _, s := range m.primMembers {
		s.sortList(in)
	}
}

// placeMethod adds a Type.Name function to its constructor's set, or
// reports it as an orphan (E0704): the type it belongs to must be declared in this
// module; a builtin owner takes methods only from std.
func (m *Module) placeMethod(it *types.Item) {
	c := m.c
	mb := Member{Name: it.Name, Kind: MemberFunc, Item: it.ID, Exported: it.Exported}
	orphan := func(owner string) {
		m.Diags = append(m.Diags, diag.New(diag.E0704, it.Span, "a method of `"+owner+"` must be declared in the module that declares it").
			Text("reason", "orphan-method").Alt("declare a function that takes the value as a parameter"))
	}
	if it.OwnerPrim != types.NoType {
		if !m.mod.Std {
			orphan(c.cfg.Types.TypeString(it.OwnerPrim, nil))
			return
		}
		m.primSet(it.OwnerPrim).add(c.cfg.Interner, mb, it.Span)
		return
	}
	o := c.cfg.Items.Get(it.Owner)
	switch o.Kind {
	case types.KindStruct, types.KindEnum, types.KindError, types.KindInterface:
		if o.Module != m.ID {
			orphan(c.cfg.Interner.Str(o.Name))
			return
		}
		if m.errorFormatMethod(it, o) {
			return
		}
		if s := m.members[o.ID]; s != nil {
			s.add(c.cfg.Interner, mb, it.Span)
		}
	case types.KindAlias:
		// A Type.Name on an alias adds to the underlying type, in that
		// type's module only.
		ad := c.aliasDecl(o, m)
		if ad == nil || ad.Type == types.Poison {
			return
		}
		ck, ok := c.ctorOf(ad.Type)
		switch {
		case ok && ck.item != types.NoItem && c.ctorModule(ck) == m.ID:
			if m.errorFormatMethod(it, c.cfg.Items.Get(ck.item)) {
				return
			}
			if s := m.members[ck.item]; s != nil {
				s.add(c.cfg.Interner, mb, it.Span)
			}
		case ok && ck.item == types.NoItem && m.mod.Std:
			m.primSet(ad.Type).add(c.cfg.Interner, mb, it.Span)
		case ok:
			orphan(c.cfg.Types.TypeString(ad.Type, nil))
		}
	}
}

// errorFormatMethod reports a Type.Name Format of error o, directly or
// through an alias, as E0206: an error's Format is the compiler's impl
// method, decided after implCollisions, so it is judged here as a written
// impl's would be there, and `e.Format(w)` stays the impl's.
func (m *Module) errorFormatMethod(it, o *types.Item) bool {
	in := m.c.cfg.Interner
	if o.Kind != types.KindError || resolve.MemberKey(in.Str(it.Name), false) != resolve.MemberKey("Format", false) {
		return false
	}
	m.Diags = append(m.Diags, diag.New(diag.E0206, it.Span, "`"+in.Str(it.Name)+"` is already a member of `"+
		in.Str(o.Name)+"`: an error's `Format` is the compiler's").In(diag.PhaseTypes).Loc("first", o.Span))
	return true
}

// primSet is the std member set of a builtin owner type in this module.
func (m *Module) primSet(t types.TypeID) *memberSet {
	s := m.primMembers[t]
	if s == nil {
		s = newMemberSet()
		m.primMembers[t] = s
	}
	return s
}

// ctorSet is the declared member set of a named constructor, nil for none.
func (c *Checker) ctorSet(k ctorKey) *memberSet {
	if k.item == types.NoItem {
		return nil
	}
	if om := c.module(c.cfg.Items.Get(k.item).Module); om != nil {
		return om.members[k.item]
	}
	return nil
}

// ownMemberSpan finds a declared member (field, variant, Type.Name) of ck by
// resolve.MemberKey: for a named constructor in its module's set, for a
// builtin in the std sets this module sees.
func (m *Module) ownMemberSpan(ck ctorKey, folded string) (source.Span, bool) {
	if s := m.c.ctorSet(ck); s != nil {
		if i, ok := s.folded[folded]; ok {
			return s.spans[i], true
		}
		return source.Span{}, false
	}
	if ck.prim == types.NoType {
		return source.Span{}, false
	}
	for _, dep := range append(m.depList(), m.ID) {
		if om := m.c.module(dep); om != nil {
			if s := om.primMembers[ck.prim]; s != nil {
				if i, ok := s.folded[folded]; ok {
					return s.spans[i], true
				}
			}
		}
	}
	return source.Span{}, false
}

// depList is the module's transitive imports in module order. deps is
// fixed once computeDeps returns, so the sorted list is made once and
// shared; it is clipped, so a caller's append (append(depList(), m.ID),
// then a sort) copies and never reorders the shared list.
func (m *Module) depList() []types.ModuleID {
	s := m.state()
	if p := s.deps.Load(); p != nil {
		return *p
	}
	out := make([]types.ModuleID, 0, len(m.deps))
	for d := range m.deps {
		out = append(out, d)
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	out = slices.Clip(out)
	s.deps.Store(&out)
	return out
}

// computeDeps fills deps: every module a file binds, the prelude's module,
// and their own transitive imports (each is through stage 5 already).
func (m *Module) computeDeps() {
	c := m.c
	m.deps = map[types.ModuleID]bool{}
	var add func(id types.ModuleID)
	add = func(id types.ModuleID) {
		if id == types.NoModule || id == m.ID || m.deps[id] {
			return
		}
		m.deps[id] = true
		if om := c.module(id); om != nil {
			for d := range om.deps {
				add(d)
			}
		}
	}
	if m.info == nil { // a loaded module (Load)
		for _, d := range m.mod.Imports {
			add(d)
		}
	} else {
		for _, fi := range m.info.Files {
			for _, b := range fi.Imports {
				add(b.Module)
			}
		}
	}
	for _, k := range []types.ItemID{c.known.Slice, c.known.Cell, c.known.System} {
		if k != types.NoItem {
			add(c.cfg.Items.Get(k).Module)
		}
	}
}

// Members returns the member set of a type (fields, Type.Name functions,
// methods of every impl for its constructor in a module whose stage 5 is
// finished), sorted by name bytes. It is a post-stage-5 query:
// asked while sibling modules are still in Signatures it sees fewer impls;
// stage-5 and stage-6 work uses Module.lookup instead. Methods reported E0206 at their
// declaration are left out.
func (c *Checker) Members(t types.TypeID) []Member {
	ck, ok := c.ctorOf(t)
	if !ok {
		return nil
	}
	var out []Member
	if s := c.ctorSet(ck); s != nil {
		out = append(out, s.list...)
	}
	for _, mod := range c.modulesInOrder() {
		if ck.prim != types.NoType {
			if s := mod.primMembers[ck.prim]; s != nil {
				out = append(out, s.list...)
			}
		}
		out = append(out, mod.implMembers(ck)...)
	}
	in := c.cfg.Interner
	sort.SliceStable(out, func(a, b int) bool { return in.Str(out[a].Name) < in.Str(out[b].Name) })
	return out
}

// implMembers are the methods of this module's impls for ck, in item order.
func (m *Module) implMembers(ck ctorKey) []Member {
	return appendImplMethods(m.c, m.shadowed, nil, m.implsFor(ck, types.NoItem))
}

// appendImplMethods appends the methods of impls ids, those not in
// shadowed, in order.
func appendImplMethods(c *Checker, shadowed map[types.ItemID]bool, out []Member, ids []types.ItemID) []Member {
	for _, id := range ids {
		for _, mid := range c.cfg.Items.Get(id).Decl.(*types.ImplDecl).Methods {
			if shadowed[mid] {
				continue
			}
			mi := c.cfg.Items.Get(mid)
			out = append(out, Member{Name: mi.Name, Kind: MemberImplMethod, Item: mid, Exported: true})
		}
	}
	return out
}

// modulesInOrder lists the modules whose stage 5 is finished, by
// ModuleID. A module still in Signatures (perhaps on another goroutine) is
// left out: its tables are being written.
func (c *Checker) modulesInOrder() []*Module {
	mods := *c.mods.Load()
	out := make([]*Module, 0, len(mods))
	for _, mod := range mods {
		if mod.done.Load() {
			out = append(out, mod)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// lookup is one member lookup from module m's point of view: the
// declared members of t's constructor, then the methods of the impls this
// module sees (its own and its imports'). It returns every candidate with
// the exact name; when there is none, every candidate with the same folded
// name, with folded set. Two impl methods among the candidates is the
// use-site E0206 `reason: ambiguous-member`, the caller's to report.
func (m *Module) lookup(t types.TypeID, name source.Symbol) (cands []Member, folded bool) {
	in := m.c.cfg.Interner
	all := m.visibleMembers(t)
	for _, mb := range all {
		if mb.Name == name {
			cands = append(cands, mb)
		}
	}
	if len(cands) > 0 {
		return cands, false
	}
	f := resolve.Fold(in.Str(name))
	for _, mb := range all {
		if resolve.Fold(in.Str(mb.Name)) == f {
			cands = append(cands, mb)
		}
	}
	return cands, len(cands) > 0
}

// visibleMembers is t's member list as module m sees it: the declared
// members of its constructor, then std's members of a builtin type and the
// methods of the impls of m and its transitive imports, in dependency order.
// Once m's tables are final the answer is memoized by constructor, when
// everything it read was final too (every dependency checked or loaded,
// the constructor's module done or m): the callers only read it.
func (m *Module) visibleMembers(t types.TypeID) []Member {
	c := m.c
	ck, ok := c.ctorOf(t)
	if !ok {
		return nil
	}
	var st *implState
	if m.final {
		st = m.state()
		if v, ok := st.members.Load(ck); ok {
			return v.([]Member)
		}
	}
	all, stable := m.collectMembers(ck)
	if st != nil && stable {
		all = slices.Clip(all)
		st.members.Store(ck, all)
	}
	return all
}

// collectMembers is visibleMembers' list for ck; stable is false when a
// module it reads may still change (missing, or not through stage 5).
func (m *Module) collectMembers(ck ctorKey) (all []Member, stable bool) {
	c := m.c
	stable = true
	if ck.item != types.NoItem {
		if cm := c.module(c.cfg.Items.Get(ck.item).Module); cm == nil || cm != m && !cm.done.Load() {
			stable = false
		}
	}
	if s := c.ctorSet(ck); s != nil {
		all = append(all, s.list...)
	}
	if view := m.closureIndex(); view != nil {
		for _, d := range view {
			if ck.prim != types.NoType {
				if s := d.prim[ck.prim]; s != nil {
					all = append(all, s.list...)
				}
			}
			all = appendImplMethods(c, d.shadowed, all, d.index[ck])
		}
		return all, stable
	}
	for _, dep := range append(m.depList(), m.ID) {
		om := c.module(dep)
		if om == nil {
			stable = false
			continue
		}
		if om != m && !om.done.Load() {
			stable = false
		}
		if ck.prim != types.NoType {
			if s := om.primMembers[ck.prim]; s != nil {
				all = append(all, s.list...)
			}
		}
		all = append(all, om.implMembers(ck)...)
	}
	return all, stable
}

// closureModule is what collectMembers reads of one module of a
// closureIndex: its std member sets of builtin types, its shadowed impl
// methods and its impl index (the maps only, so the state a Module keeps
// in its impls field reads no Module through it).
type closureModule struct {
	prim     map[types.TypeID]*memberSet
	shadowed map[types.ItemID]bool
	index    implIndex
}

// closureIndex is m's dependencies in module order, then m, each with its
// impl index: collectMembers' walk without a module lookup per step. It is
// made once every one of them is final (m) or done (the others); nil
// before.
func (m *Module) closureIndex() []closureModule {
	if !m.final {
		return nil
	}
	s := m.state()
	if p := s.closure.Load(); p != nil {
		return *p
	}
	var out []closureModule
	for _, dep := range append(m.depList(), m.ID) {
		om := m.c.module(dep)
		if om == nil || om != m && !om.done.Load() {
			return nil
		}
		out = append(out, closureModule{prim: om.primMembers, shadowed: om.shadowed, index: om.implIndexOf()})
	}
	s.closure.Store(&out)
	return out
}
