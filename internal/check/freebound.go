// Why: freeing a std container must not silently drop a linear value it
// holds; the release methods carry implicit bounds that say which element
// types they accept.
//
// What: releaseRows (the release methods as a table), releaseContainers,
// releaseBound (E0701 at a call), releaseGap, releaseElemsOK and
// releaseBoundFacts (the element condition of an impl of Free), freeIface,
// releaseHome and releaseOwner.
//
// How: a call that would drop a linear value is E0701 at the call, an
// implicit bound judged only when std.core declares the interface Free. A
// container of the table is Free when each element type is not linear or
// is Free, a condition std's unbounded impl cannot spell, so check judges it
// at every depth. A container's own code is exempt.

package check

import (
	"slices"
	"strings"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// releaseRow is one method the release bounds judge.
type releaseRow struct {
	module, owner, method string
	slots                 []int  // the owning type's generic parameters judged
	names                 string // their names, one per slot, for the param fact
	// free: each judged type must be not linear, or Free (a heap container
	// frees with its a); otherwise not linear (nothing to free with).
	free bool
	alts []string
	// stores: the call moves the value into arena memory (an arena
	// container's slot, or Arena.Put's fresh piece), which the arena frees
	// with no consumer, so a linear value there would be lost; the message
	// says so.
	stores bool
}

// releaseRows is the release protocol as a table. Clear of a List or
// HeapList and the last Shared.Free check at run time and are not here;
// Pool.Free is judged by poolFree. An arena container is never linear and
// ends with its arena, so it must never hold a linear value: the stores
// rows, with the arena Map's Set and Ensure, BTree.Set and Set.Add (which
// also drop), are every call that moves a value into arena memory; each
// names the heap container that holds one, where std has one.
var releaseRows = []releaseRow{
	{"std.core", "List", "Push", []int{0}, "T", false, []string{"HeapList"}, true},
	{"std.core", "List", "Insert", []int{0}, "T", false, []string{"HeapList"}, true},
	{"std.coll", "Deque", "Push", []int{0}, "T", false, []string{"HeapDeque"}, true},
	{"std.coll", "Deque", "PushFront", []int{0}, "T", false, []string{"HeapDeque"}, true},
	{"std.coll", "PriorityQueue", "Push", []int{0}, "T", false, nil, true},
	{"std.mem", "Pool", "Add", []int{0}, "T", false, nil, true},
	{"std.core", "Arena", "Put", []int{0}, "T", false, []string{"Box"}, true},
	{"std.core", "HeapList", "Free", []int{0}, "T", true, []string{"FreeEmpty", "FreeEach"}, false},
	{"std.core", "HeapMap", "Free", []int{0, 1}, "K V", true, []string{"FreeEmpty", "FreeEach"}, false},
	{"std.core", "TextMap", "Free", []int{0}, "V", true, []string{"FreeEmpty", "FreeEach"}, false},
	{"std.core", "Chan", "Free", []int{0}, "T", true, []string{"FreeEmpty", "FreeEach"}, false},
	{"std.core", "Box", "Free", []int{0}, "T", true, []string{"Take"}, false},
	{"std.coll", "HeapDeque", "Free", []int{0}, "T", true, []string{"FreeEmpty", "FreeEach"}, false},
	{"std.coll", "HeapBTree", "Free", []int{0, 1}, "K V", true, []string{"FreeEmpty", "FreeEach"}, false},
	{"std.core", "HeapMap", "Set", []int{0, 1}, "K V", true, []string{"Replace"}, false},
	{"std.core", "HeapMap", "Ensure", []int{0, 1}, "K V", true, []string{"Replace"}, false},
	{"std.core", "HeapMap", "Replace", []int{0}, "K", true, nil, false},
	{"std.coll", "HeapBTree", "Set", []int{0, 1}, "K V", true, []string{"Replace"}, false},
	{"std.coll", "HeapBTree", "Replace", []int{0}, "K", true, nil, false},
	{"std.core", "TextMap", "Set", []int{0}, "V", true, []string{"Replace"}, false},
	{"std.core", "TextMap", "Ensure", []int{0}, "V", true, []string{"Replace"}, false},
	{"std.core", "Map", "Set", []int{0, 1}, "K V", false, []string{"HeapMap"}, false},
	{"std.core", "Map", "Ensure", []int{0, 1}, "K V", false, []string{"HeapMap"}, false},
	{"std.core", "Map", "Replace", []int{0, 1}, "K V", false, []string{"HeapMap"}, true},
	{"std.core", "Map", "Remove", []int{0}, "K", false, nil, false},
	{"std.coll", "BTree", "Set", []int{0, 1}, "K V", false, []string{"HeapBTree"}, false},
	{"std.coll", "BTree", "Replace", []int{0, 1}, "K V", false, []string{"HeapBTree"}, true},
	{"std.core", "HeapMap", "Remove", []int{0}, "K", false, nil, false},
	{"std.core", "Set", "Add", []int{0}, "T", false, nil, false},
	{"std.core", "Set", "Remove", []int{0}, "T", false, nil, false},
	{"std.coll", "BTree", "Remove", []int{0}, "K", false, nil, false},
	{"std.coll", "HeapBTree", "Remove", []int{0}, "K", false, nil, false},
	{"std.core", "Guard", "Store", []int{0}, "T", false, []string{"Replace(g.Mut(), x)"}, false},
	{"std.atomic", "SpinGuard", "Store", []int{0}, "T", false, []string{"Replace(g.Mut(), x)"}, false},
	{"std.mem", "Pool", "Sweep", []int{0}, "T", false, nil, false},
}

// releaseContainers are the containers whose Free deep-frees: an instance
// of one is Free when each element type is not linear or is Free, which
// std's unbounded impl cannot spell.
var releaseContainers = map[string]bool{
	"std.core.HeapList": true, "std.core.HeapMap": true, "std.core.TextMap": true, "std.core.Chan": true,
	"std.core.Box": true, "std.coll.HeapDeque": true, "std.coll.HeapBTree": true,
}

// releaseRowOf is the row of callee item, or nil.
func (c *Checker) releaseRowOf(it types.ItemID) *releaseRow {
	if it == types.NoItem {
		return nil
	}
	item := c.cfg.Items.Get(it)
	if item.Kind != types.KindFunc || item.Owner == types.NoItem {
		return nil
	}
	name := c.cfg.Interner.Str(item.Name)
	owner := c.cfg.Items.Get(item.Owner)
	if owner.Kind == types.KindImpl {
		// A container's Free written inside
		// `impl[T] Free for HeapList[T]` has the impl as its owner; the row
		// is the impl's target type constructor's.
		d, ok := c.declOfItem(owner).(*types.ImplDecl)
		if !ok || d.Bound.Item == types.NoItem || c.cfg.Items.Get(d.Bound.Item).Key != types.ItemKey("std.core", "Free") {
			return nil
		}
		t := c.cfg.Types.Get(d.For)
		if t.Kind != types.KNamed || t.Item == types.NoItem {
			return nil
		}
		owner = c.cfg.Items.Get(t.Item)
	}
	mod := c.cfg.Items.Module(owner.Module)
	if mod == nil || !mod.Std {
		return nil
	}
	oname := c.cfg.Interner.Str(owner.Name)
	for i := range releaseRows {
		r := &releaseRows[i]
		if r.method == name && r.owner == oname && r.module == mod.Path {
			return r
		}
	}
	return nil
}

// freeIface is std.core's interface Free when it is declared with its one
// method Free(self, a: &Alloc); the lookup is a read either way. A
// hit is cached; a miss never is, since std.core may not be checked yet.
func (u *unit) freeIface() types.ItemID {
	c := u.c
	if id := c.freeItem.Load(); id != 0 {
		u.x.rec.signature(c.cfg.Items.Get(types.ItemID(id)).Key)
		return types.ItemID(id)
	}
	key := types.ItemKey("std.core", "Free")
	id, ok := c.cfg.Items.ByKey(key)
	if !ok {
		u.x.rec.noItem(key, "Free")
		return types.NoItem
	}
	it := c.cfg.Items.Get(id)
	d, ok := c.declOfItem(it).(*types.InterfaceDecl)
	if it.Kind != types.KindInterface || !it.Exported || !ok || len(d.Methods) != 1 {
		u.x.rec.signature(key)
		return types.NoItem
	}
	m := &d.Methods[0]
	if c.cfg.Interner.Str(m.Name) != "Free" || m.Receiver != types.ByValue || len(m.Params) != 2 {
		u.x.rec.signature(key)
		return types.NoItem
	}
	u.x.rec.signature(key)
	c.freeItem.Store(uint32(id))
	return id
}

// releaseBound is the E0701 of a release row at a call.
func (u *unit) releaseBound(r *releaseRow, item types.ItemID, at source.Span, g []types.GenericArg) {
	if u.releaseHome(item) {
		return // the container's own implementation
	}
	free := u.freeIface()
	if free == types.NoItem {
		return // no protocol before std.core declares Free
	}
	fixed := false // one fix per call, on its first E0701
	for k, slot := range r.slots {
		if slot >= len(g) || g[slot].IsConst {
			continue
		}
		t := g[slot].Type
		gap := u.c.releaseGap(u.env, t, r.free, free, u.x.rec, 0)
		if gap.ok {
			continue
		}
		open := gap.open
		ts := u.str(t)
		bound := boundNotLinear
		if r.free {
			bound = "not linear, or Free"
		}
		why := "is linear"
		if open {
			why = "may be linear"
		}
		if r.free {
			why += " and not `Free`"
		}
		es := ""
		if gap.holder != types.NoType {
			// A container element that is not Free, at some depth.
			es = u.str(gap.elem)
			ew := "is linear"
			if open {
				ew = "may be linear"
			}
			why += ", since its element `" + es + "` " + ew + " and not `Free`"
		}
		msg := "`" + ts + "` " + why + ": `" + r.owner + "." + r.method + "` would drop it"
		if r.stores {
			msg = "`" + ts + "` " + why + ": `" + r.owner + "." + r.method + "` would store it in arena memory, which the arena frees without consuming it"
		} else if r.method == "Set" || r.method == "Ensure" {
			msg = "`" + ts + "` " + why + ": `" + r.owner + "." + r.method + "` would drop the value it replaces"
		}
		pn := "T"
		if params := strings.Fields(r.names); k < len(params) {
			pn = params[k]
		}
		d := diag.New(diag.E0701, at, msg).Text("param", pn).Text("bound", bound).Text("type", ts)
		if es != "" {
			d.Text("element", es)
		}
		if !fixed && !open && u.c.closed(t) && r.method == "Free" && slices.Contains(r.alts, "FreeEmpty") && at.End-at.Start == uint32(len("Free")) {
			// The fix writes FreeEmpty: the old Free panicked on linear
			// contents, as FreeEmpty does, so the rewrite keeps the
			// program's behaviour; recompiling proves it. Only where at is
			// the method's name (a call, not a fn value), and only for a
			// closed type that is linear, not one that may be, nor one
			// that mentions a type parameter (Pair[T]): the old Free of a
			// HeapList[T] instance whose T is not linear freed its
			// elements, where FreeEmpty panics on any element (a
			// canonical rewrite changes only what it can prove the same).
			fixed = true
			d.Fix(diag.Fix{Class: diag.Compiles, Edits: []diag.Edit{{Span: at, Text: "FreeEmpty"}}, Why: ReleaseFreeEmptyWhy})
		}
		for _, a := range r.alts {
			d.Alt(a)
		}
		u.b.Diags = append(u.b.Diags, d)
	}
}

// ReleaseFreeEmptyWhy is the reason of E0701's fix that writes FreeEmpty
// for a container's Free; mk fix --canonical applies the fixes that carry
// it.
const ReleaseFreeEmptyWhy = "a container of linear elements that are not Free is emptied, then freed with FreeEmpty"

// releaseGap judges one type of a release row: not linear
// holds; with free, a linear type that is Free holds. A container of the
// table holds when each element does, recursively, and it has its impl of
// Free (std's is unbounded, so the element condition is check's).
// When t fails, elem is the innermost type that is linear and not Free (t
// itself, or an element at some depth), holder the container holding it
// (NoType when elem is t), and open reports a type parameter that may be
// linear.
func (c *Checker) releaseGap(env *Env, t types.TypeID, free bool, freeIface types.ItemID, rec recorder, depth int) (g releaseGapAt) {
	if env == nil {
		env = c.NewEnv(nil)
	}
	n := narrowed(c.cfg.Types, env, t)
	if n == types.Poison || n == types.NoType || depth > maxFoldDepth {
		return releaseGapAt{ok: true}
	}
	c.shapeReads(env, n, rec)
	if c.FactsIn(env, n).Kind != types.Linear {
		return releaseGapAt{ok: true}
	}
	nt := c.cfg.Types.Get(n)
	if nt.Kind == types.KParam {
		if free && int(nt.Index) < len(env.gen) {
			for _, b := range env.gen[nt.Index].Bounds {
				if b.Kind == types.BoundInterface && b.Item == freeIface {
					return releaseGapAt{ok: true}
				}
			}
		}
		return releaseGapAt{elem: n, open: true}
	}
	if !free {
		return releaseGapAt{elem: n, open: c.linearThroughParams(env, n)}
	}
	if nt.Kind == types.KNamed && c.isReleaseContainer(n) {
		for _, a := range nt.Args {
			if a.IsConst {
				continue
			}
			if in := c.releaseGap(env, a.Type, true, freeIface, rec, depth+1); !in.ok {
				if in.holder == types.NoType {
					in.holder = n
				}
				return in
			}
		}
	}
	if _, ok := c.implOf(env, freeIface, nil, n, rec, 0); ok {
		return releaseGapAt{ok: true}
	}
	return releaseGapAt{elem: n, open: c.linearThroughParams(env, n)}
}

// linearThroughParams reports whether n, linear in env, is linear only
// through the type parameters it mentions (`?T`, `[N]T`, a record of a T):
// with each one that is not narrowed taken as int it is not linear, so the
// report says it may be linear, not that it is. Kinds only grow from their
// parts, so a type still linear then (`(a: Ticket, b: T)`) is linear for
// every T.
func (c *Checker) linearThroughParams(env *Env, n types.TypeID) bool {
	if c.closed(n) {
		return false
	}
	e := env
	for i := range env.gen {
		if !env.gen[i].Const && env.narrow[i] == types.NoType {
			e = e.Narrow(uint32(i), types.Int)
		}
	}
	return c.FactsIn(e, n).Kind != types.Linear
}

// releaseGapAt is releaseGap's answer.
type releaseGapAt struct {
	ok, open     bool
	elem, holder types.TypeID
}

// isReleaseContainer reports whether t is an instance of a container of
// releaseContainers.
func (c *Checker) isReleaseContainer(t types.TypeID) bool {
	ty := c.cfg.Types.Get(t)
	return ty.Kind == types.KNamed && ty.Item != types.NoItem && releaseContainers[c.cfg.Items.Get(ty.Item).Key.String()]
}

// isFreeIface reports whether iface is std.core's interface Free.
func (c *Checker) isFreeIface(iface types.ItemID) bool {
	if iface == types.NoItem {
		return false
	}
	if id := c.freeItem.Load(); id != 0 {
		return types.ItemID(id) == iface // one std.core per checker
	}
	return c.cfg.Items.Get(iface).Key.String() == freeKeyText
}

// freeKeyText is the key of std.core's interface Free.
const freeKeyText = "std.core.Free"

// releaseElemsOK: an impl of std.core's Free for a
// container of the table applies only when each element type is not linear
// or is Free, the condition std's unbounded impl cannot spell. So a
// `T: Free` bound, an impl bound and a nested container's element are all
// judged at every depth, as a container's own Free call is (releaseGap).
func (c *Checker) releaseElemsOK(env *Env, t types.TypeID, freeIface types.ItemID, rec recorder, depth int) bool {
	if !c.isReleaseContainer(t) {
		return true
	}
	if env == nil {
		env = c.NewEnv(nil)
	}
	for _, a := range c.cfg.Types.Get(t).Args {
		if a.IsConst || a.Type == types.Poison {
			continue
		}
		n := narrowed(c.cfg.Types, env, a.Type) // the then-branch of `if T is X`
		c.shapeReads(env, n, rec)
		if c.FactsIn(env, n).Kind != types.Linear {
			continue
		}
		if !c.satisfiesDepth(env, n, types.Bound{Kind: types.BoundInterface, Item: freeIface}, rec, depth+1) {
			return false
		}
	}
	return true
}

// releaseBoundFacts adds the element detail to the E0701 of a `Free` bound that
// a container of the table misses (boundFailure): the element that is
// linear and not Free, and the holding container's alternatives. No fix:
// the span is a call or a type, not a Free method name.
func (c *Checker) releaseBoundFacts(d *diag.Diagnostic, env *Env, b types.Bound, t types.TypeID, gens []types.GenericParam) {
	if d == nil || b.Kind != types.BoundInterface || !c.isFreeIface(b.Item) {
		return
	}
	g := c.releaseGap(env, t, true, b.Item, recorder{}, 0)
	if g.ok || g.holder == types.NoType {
		return
	}
	es := c.cfg.Types.TypeString(g.elem, gens)
	why := "is linear and not `Free`"
	if g.open {
		why = "may be linear and is not `Free`"
	}
	d.Message += ": its element `" + es + "` " + why
	d.Text("element", es)
	for _, a := range c.releaseAlts(g.holder) {
		d.Alt(a)
	}
}

// releaseAlts are the alternatives of the Free row of container t.
func (c *Checker) releaseAlts(t types.TypeID) []string {
	it := c.cfg.Items.Get(c.cfg.Types.Get(t).Item)
	mod := c.cfg.Items.Module(it.Module)
	if mod == nil {
		return nil
	}
	name := c.cfg.Interner.Str(it.Name)
	for i := range releaseRows {
		if r := &releaseRows[i]; r.method == "Free" && r.owner == name && r.module == mod.Path {
			return r.alts
		}
	}
	return nil
}

// releaseHome reports whether the calling unit is the callee owner's own
// code: a method of the same owner, or a method of an impl for it.
// Either side's owner may be an impl, as std's `impl[T] Free for
// HeapList[T]` is; it stands for its target's type constructor. The
// caller must be in the callee's module: a user's impl of its own
// interface for a container is not the container's code.
func (u *unit) releaseHome(callee types.ItemID) bool {
	ci := u.c.cfg.Items.Get(callee)
	if u.it == nil || u.it.Module != ci.Module {
		return false
	}
	owner := u.c.releaseOwner(ci.Owner)
	return owner != types.NoItem && u.c.releaseOwner(u.it.Owner) == owner
}

// releaseOwner is owner, or the type constructor of an impl's target when
// owner is an impl; NoItem for none.
func (c *Checker) releaseOwner(owner types.ItemID) types.ItemID {
	if owner == types.NoItem {
		return types.NoItem
	}
	it := c.cfg.Items.Get(owner)
	if it.Kind != types.KindImpl {
		return owner
	}
	d, ok := c.declOfItem(it).(*types.ImplDecl)
	if !ok {
		return types.NoItem
	}
	ft := c.cfg.Types.Get(d.For)
	if ft.Kind != types.KNamed {
		return types.NoItem
	}
	return ft.Item
}
