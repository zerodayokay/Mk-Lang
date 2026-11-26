// Why: mono instantiates generics over what check, lower and borrow
// produced, and a compile-time evaluation of a generic needs mono's
// instance body; both read the session through one interface.
//
// What: monoEnv, the mono.Env over the session (Body, OnerrRuns,
// RuntimeEntry, the impl lookups and the instance bodies eval asks for),
// monoOptions, and noWorld.
//
// How: bodies come from the body store, layouts and impls from check, guard
// decisions from borrow. Instance bodies for eval are made once per
// instance name with mono.Instantiate and kept with the impl lookups their
// instantiation made, so an impl change invalidates a cached evaluation.

package driver

import (
	"encoding/binary"
	"sync"

	"mk/internal/eval"
	"mk/internal/iface"
	"mk/internal/mir"
	"mk/internal/mono"
	"mk/internal/source"
	"mk/internal/types"
)

// monoEnv is mono.Env over the session: the body store, check's layouts and
// impl table, borrow's guard decisions. Every method is safe for concurrent
// use (the body store and check are; the instance cache takes its lock).
type monoEnv struct {
	w *world

	mu    sync.Mutex
	insts map[source.Hash]served // instance bodies for eval by InstanceName; lookup only
}

// served is one instance body for eval and the impl lookups its
// instantiation made: eval records them with the body (eval.Body.Reads), so
// adding or removing an impl of Free invalidates a cached evaluation through
// an instance that resolved Release or folded HasFree.
type served struct {
	b     *mir.Body
	reads []types.Read
}

// readingEnv is monoEnv for one instantiation: ImplMethod also keeps the
// reads of check's ImplOf, which monoEnv's own ImplMethod drops.
type readingEnv struct {
	*monoEnv
	reads types.ReadSet
}

func (r *readingEnv) ImplMethod(ifc types.ItemID, m uint32, self types.TypeID) (types.ItemID, []types.GenericArg, bool) {
	return r.monoEnv.implMethod(ifc, m, self, &r.reads)
}

// ReadBody (mono.BodyReader) keeps the body of a requires predicate the
// instance folded, as eval records a body it enters.
func (r *readingEnv) ReadBody(item types.ItemID) {
	r.reads.Add(types.Read{Kind: types.ReadBody, Key: r.w.items.Get(item).Key})
}

// ReadLayout (mono.LayoutReader) records ReadLayout for every named or error
// type a by-value walk of t reaches, matching eval's noteType, so a folded
// requires predicate or substituted SizeOf argument invalidates a warm front
// cache when a private or generic layout it read changes.
func (r *readingEnv) ReadLayout(t types.TypeID) {
	w := r.w
	if w.sem5 == nil {
		return
	}
	s := w.sem5
	tab := w.tab
	seen := map[types.TypeID]bool{}
	seenErr := map[types.ItemID]bool{}
	var visit func(types.TypeID)
	visit = func(cur types.TypeID) {
		if cur == types.NoType || seen[cur] {
			return
		}
		seen[cur] = true
		ty := tab.Get(cur)
		switch ty.Kind {
		case types.KNamed:
			it := w.items.Get(ty.Item)
			l := s.chk.Layout(cur)
			h := s.hash.Layout(&l)
			name := s.hash.LayoutName(ty.Args)
			r.reads.Add(types.Read{Kind: types.ReadLayout, Key: it.Key, Name: name, Hash: h})
			for _, a := range ty.Args {
				if !a.IsConst {
					visit(a.Type)
				}
			}
		case types.KArray, types.KOptional:
			visit(ty.Elem)
		case types.KErrorUnion, types.KErrorSet:
			visit(ty.Elem)
			for _, m := range tab.Members(ty.Errors) {
				if !m.IsParam && !seenErr[m.Item] {
					seenErr[m.Item] = true
					it := w.items.Get(m.Item)
					if d, ok := it.Decl.(*types.ErrorDecl); ok {
						b := binary.AppendUvarint(nil, uint64(len(d.Fields)))
						for _, f := range d.Fields {
							l := s.chk.Layout(f.Type)
							lh := s.hash.Layout(&l)
							b = append(b, lh[:]...)
						}
						h := source.HashBytes("mk.eval.errorlayout.v1", b)
						r.reads.Add(types.Read{Kind: types.ReadLayout, Key: it.Key, Hash: h})
					}
				}
			}
		case types.KRecord:
			for _, f := range ty.Fields {
				visit(f.Type)
			}
		}
	}
	visit(t)
}

func (s *semantics) monoEnv(w *world) *monoEnv {
	s.monoOnce.Do(func() { s.menv = &monoEnv{w: w, insts: map[source.Hash]served{}} })
	return s.menv
}

func (e *monoEnv) Table() *types.Table       { return e.w.tab }
func (e *monoEnv) Items() *types.Items       { return e.w.items }
func (e *monoEnv) Consts() *types.ConstTable { return e.w.tab.Consts() }
func (e *monoEnv) Hasher() *iface.Hasher     { return e.w.sem5.hash }

// World answers every read with a zero hash (noWorld), so
// Instance.Content does not depend on the reads.
func (e *monoEnv) World() iface.World { return noWorld{} }

// Body is the item's MIR when stage 6 lowered it and stage 7 accepted it
// (or skipped it); nil for a bodiless item and for a body the build marked
// failed, which mono treats as an extern: no further work.
func (e *monoEnv) Body(id types.ItemID) *mir.Body {
	b := e.w.sem5.body(id)
	if b == nil || b.mir == nil || b.state != eval.BodyReady {
		return nil
	}
	return b.mir
}

// Layout is check's layout of a closed type.
func (e *monoEnv) Layout(t types.TypeID) *types.Layout {
	l := e.w.sem5.chk.Layout(t)
	return &l
}

// OnerrRuns is borrow's decision for the guard ending block b of item's
// body. A body with a guard and no decision was not accepted by
// stage 7, which no instance body can be: an ICE.
func (e *monoEnv) OnerrRuns(item types.ItemID, b mir.BlockID) bool {
	be := e.w.sem5.body(item)
	if be == nil || int(b) >= len(be.onerr) {
		source.ICEf(source.Span{}, "driver: no onerr decision for block %d of %s", b, e.w.items.Get(item).Key)
	}
	return be.onerr[b]
}

// RuntimeEntry finds a std function by stable key.
func (e *monoEnv) RuntimeEntry(key types.Key) (types.ItemID, bool) { return e.w.items.ByKey(key) }

// ImplMethod resolves method m of interface iface on the closed type self:
// check's impl for (iface, self), its member named as the interface's
// method m, and the impl's generic arguments bound by matching its target
// against self (check's ImplArgs, the binding ImplOf's search made).
func (e *monoEnv) ImplMethod(ifc types.ItemID, m uint32, self types.TypeID) (types.ItemID, []types.GenericArg, bool) {
	var rs types.ReadSet
	return e.implMethod(ifc, m, self, &rs)
}

// implMethod is ImplMethod with ImplOf's reads kept in rs.
func (e *monoEnv) implMethod(ifc types.ItemID, m uint32, self types.TypeID, rs *types.ReadSet) (types.ItemID, []types.GenericArg, bool) {
	w := e.w
	d, ok := w.items.Get(ifc).Decl.(*types.InterfaceDecl)
	if !ok || int(m) >= len(d.Methods) {
		return types.NoItem, nil, false
	}
	impl, ok := w.sem5.chk.ImplOf(ifc, nil, self, rs)
	if !ok {
		return types.NoItem, nil, false
	}
	if impl == types.NoItem {
		// A structural yes (Format on an error set of several
		// members) has no impl item; mono intercepts it before asking,
		// so reaching here is a compiler bug.
		source.ICEf(source.Span{}, "ImplMethod: ImplOf answered a structural yes with no impl item; mono must intercept an error set's Format")
	}
	name := w.in.Str(d.Methods[m].Name)
	fn, ok := w.items.ByKey(types.ImplMemberKey(w.items.Get(impl).Key, name))
	if !ok {
		return types.NoItem, nil, false
	}
	args, ok := w.sem5.chk.ImplArgs(impl, self)
	if !ok {
		return types.NoItem, nil, false
	}
	return fn, args, true
}

// instance is the instance body of a generic fn with args, as eval asks for
// it (Program.Body), or mono's body of a non-generic caller of a Slice[T]
// function (args nil): mono.Instantiate, once per instance name. nil when the
// generic body is not available (failed, bodiless): eval then gets poison.
func (e *monoEnv) instance(fn types.ItemID, args []types.GenericArg) *mir.Body {
	return e.instanceReads(fn, args).b
}

// instanceReads is instance with the impl lookups the instantiation
// made, for eval.Body.Reads.
func (e *monoEnv) instanceReads(fn types.ItemID, args []types.GenericArg) served {
	w := e.w
	if e.Body(fn) == nil {
		return served{}
	}
	key := w.sem5.hash.InstanceName(w.items.Get(fn).Key, args)
	e.mu.Lock()
	sv, ok := e.insts[key]
	e.mu.Unlock()
	if ok {
		return sv
	}
	re := &readingEnv{monoEnv: e}
	sv = served{b: mono.Instantiate(re, fn, args, w.monoOptions())}
	sv.reads = re.reads.Sorted()
	e.mu.Lock()
	if prev, ok := e.insts[key]; ok {
		sv = prev // first store wins, so every evaluation sees one body
	} else {
		e.insts[key] = sv
	}
	e.mu.Unlock()
	return sv
}

// callsSliceFn reports whether b calls a Slice[T] namespace function: a
// direct callee whose owner is an interface (only Slice[T] declares such
// functions). It mirrors mono's own test (slice.go), under which
// mono.Instantiate rewrites a non-generic body.
func callsSliceFn(items *types.Items, b *mir.Body) bool {
	for i := range b.Blocks {
		t := &b.Blocks[i].Term
		if t.Kind != mir.TermCall && t.Kind != mir.TermTailCall || t.Callee.Kind != mir.CallDirect || t.Callee.Fn.Item == types.NoItem {
			continue
		}
		it := items.Get(t.Callee.Fn.Item)
		if it.Owner == types.NoItem {
			continue
		}
		if _, ok := items.Get(it.Owner).Decl.(*types.InterfaceDecl); ok {
			return true
		}
	}
	return false
}

// monoOptions are the build's mono.Options. Validate is on, as for lower
// (the driver validates in dev and test builds of mk itself).
func (w *world) monoOptions() mono.Options {
	return mono.Options{Profile: w.cfg.Profile, Target: w.cfg.Target, Known: &w.sem5.known, Validate: true, LibcMode: w.libcBuild()}
}

// noWorld is the iface.World monoEnv gives mono: every part
// exists with a zero hash. Deterministic, and never stored.
type noWorld struct{}

func (noWorld) Part(types.Read) (source.Hash, bool) { return source.Hash{}, true }
