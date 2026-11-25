// Why: impl and member lookups must not scan every module per lookup, or
// their cost grows with modules times lookups.
// What: TestImplIndexEqualsScan, TestImplScansDoNotGrowWithLookups,
// TestImplStateDoesNotPinModules.
// How: compares the impl index by constructor and the memoized member lists
// (impls.go implsFor, implIndexOf; members.go depList, visibleMembers) with
// the implList scans they replaced, in order, for checked and loaded modules.

package check

import (
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"weak"

	"mk/internal/types"
)

// indexA and indexB spread impls over two modules above loadLib's: impls
// for lib's types, a generic struct with a generic impl and an instance
// impl, impls for primitives (the interface's own module may), a blanket
// impl (E0704, not in the table), a written Format for an error and an
// error whose provided Format is dropped (both reorder implList).
const indexA = `import lib

export interface Tag {
	Tag(&self): int
}

impl Tag for lib.Point {
	Tag(&self): int {
		return 1
	}
}

impl Tag for lib.Shape {
	Tag(&self): int {
		return 2
	}
}

export struct G[T] {
	export v: T
}

impl[T] Tag for G[T] {
	Tag(&self): int {
		return 3
	}
}

impl[T] lib.Named for G[T] {
	Name(&self): str {
		return "g"
	}
}

impl Tag for int {
	Tag(&self): int {
		return 4
	}
}

impl[T] Tag for T {
	Tag(&self): int {
		return 5
	}
}

impl[T] Tag for []T {
	Tag(&self): int {
		return 7
	}
}

impl[T] Tag for ?T {
	Tag(&self): int {
		return 8
	}
}

export error Oops(code: int)

impl Format for Oops {
	Format(&self, w: mut Writer): WriteError!void {
	}
}
`

const indexB = `import a
import lib

export interface Size {
	Size(&self): int
	Name(&self): str
}

impl Size for lib.Point {
	Size(&self): int {
		return 1
	}
	Name(&self): str {
		return "s"
	}
}

impl Size for a.G[int] {
	Size(&self): int {
		return 2
	}
	Name(&self): str {
		return "s"
	}
}

impl Size for int {
	Size(&self): int {
		return 8
	}
	Name(&self): str {
		return "i"
	}
}

impl Size for str {
	Size(&self): int {
		return 16
	}
	Name(&self): str {
		return "t"
	}
}

export error Late(p: lib.Point)

impl a.Tag for Late {
	Tag(&self): int {
		return 6
	}
}
`

// indexWorld is loadWorld plus a and b.
func indexWorld(t *testing.T) (*world, []types.ModuleID) {
	w, lib := loadWorld(t)
	a := w.add("a", opt{}, indexA)
	b := w.add("b", opt{}, indexB)
	return w, []types.ModuleID{w.core, lib.id, a.id, b.id}
}

// scanVisibleMembers is visibleMembers as it was before the index: a fresh
// sorted dependency list and implList scans, nothing memoized.
func scanVisibleMembers(m *Module, t types.TypeID) []Member {
	c := m.c
	ck, ok := c.ctorOf(t)
	if !ok {
		return nil
	}
	var all []Member
	if s := c.ctorSet(ck); s != nil {
		all = append(all, s.list...)
	}
	deps := make([]types.ModuleID, 0, len(m.deps))
	for d := range m.deps {
		deps = append(deps, d)
	}
	sort.Slice(deps, func(a, b int) bool { return deps[a] < deps[b] })
	for _, dep := range append(deps, m.ID) {
		om := c.module(dep)
		if om == nil {
			continue
		}
		if ck.prim != types.NoType {
			if s := om.primMembers[ck.prim]; s != nil {
				all = append(all, s.list...)
			}
		}
		for _, id := range om.implsForScan(ck, types.NoItem) {
			for _, mid := range c.cfg.Items.Get(id).Decl.(*types.ImplDecl).Methods {
				if om.shadowed[mid] {
					continue
				}
				mi := c.cfg.Items.Get(mid)
				all = append(all, Member{Name: mi.Name, Kind: MemberImplMethod, Item: mid, Exported: true})
			}
		}
	}
	return all
}

// compareIndex checks every module of c against the scans: implsFor for
// every constructor any impl names (and the zero key, and primitives with
// none), visibleMembers for every impl's For and a few builtins, each asked
// twice (the second answer is the memo's). It returns how many answers
// were nonempty.
func compareIndex(t *testing.T, w *world, c *Checker, ids []types.ModuleID, label string) int {
	t.Helper()
	keys := []ctorKey{{}, {prim: types.Bool}, {prim: types.Int}, {prim: types.Str}}
	tys := []types.TypeID{types.Int, types.Str, types.Bool, types.F64}
	for _, id := range ids {
		for _, iid := range c.module(id).implList {
			f := w.items.Get(iid).Decl.(*types.ImplDecl).For
			tys = append(tys, f)
			if k, ok := c.ctorOf(f); ok {
				keys = append(keys, k)
			}
		}
	}
	nonEmpty := 0
	for _, id := range ids {
		m := c.module(id)
		path := w.items.Module(id).Path
		if !m.final {
			t.Fatalf("%s: %s is not final", label, path)
		}
		for _, k := range keys {
			want := m.implsForScan(k, types.NoItem)
			for i := 0; i < 2; i++ {
				if got := m.implsFor(k, types.NoItem); !reflect.DeepEqual(got, want) {
					t.Errorf("%s: %s implsFor(%s) = %v, scan %v", label, path, c.ctorName(k), got, want)
				}
			}
			if len(want) > 0 {
				nonEmpty++
			}
		}
		for _, ty := range tys {
			want := scanVisibleMembers(m, ty)
			for i := 0; i < 2; i++ {
				if got := m.visibleMembers(ty); !reflect.DeepEqual(got, want) {
					t.Errorf("%s: %s visibleMembers(%s) round %d:\n got %s\nwant %s", label, path,
						w.tab.TypeString(ty, nil), i, memberKeys(w, got), memberKeys(w, want))
				}
			}
		}
		if got, want := m.depList(), sortedDeps(m); !reflect.DeepEqual(got, want) || cap(got) != len(got) {
			t.Errorf("%s: %s depList %v (cap %d), want %v", label, path, got, cap(got), want)
		}
	}
	return nonEmpty
}

func sortedDeps(m *Module) []types.ModuleID {
	out := make([]types.ModuleID, 0, len(m.deps))
	for d := range m.deps {
		out = append(out, d)
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

func memberKeys(w *world, ms []Member) string {
	var b strings.Builder
	for _, mb := range ms {
		fmt.Fprintf(&b, " %s#%d", w.in.Str(mb.Name), mb.Item)
	}
	return b.String()
}

// TestImplIndexEqualsScan: the index and the memo answer as the scans do,
// over std.core's impls (primitives, the generic Slice impls) and lib, a
// and b's, for modules checked from source and for the same modules
// loaded (Load rebuilds the tables, so it builds its own index).
func TestImplIndexEqualsScan(t *testing.T) {
	w, ids := indexWorld(t)
	if n := compareIndex(t, w, w.c, ids, "checked"); n < 10 {
		t.Fatalf("only %d nonempty answers: the fixture should spread impls over the modules", n)
	}
	// The impls on builtin forms ([]T, ?T) are in the table, under their
	// kind's key.
	am := w.c.module(ids[2])
	for _, k := range []types.TypeKind{types.KSlice, types.KOptional} {
		if len(am.implsFor(ctorKey{kind: k}, types.NoItem)) != 1 {
			t.Errorf("a: no impl indexed for kind %v; %s", k, dump(w, am.Diags))
		}
	}
	// The cross-module order is pinned: Point's impl methods as b sees them
	// are a's Tag, then b's Size. lib's Name collides with Point.Name
	// and b's Name with lib's: both are shadowed (E0206), so left out.
	b := w.c.module(ids[3])
	pt := w.tab.Named(w.mods["lib"].item("Point"), nil)
	var impl []string
	for _, mb := range b.visibleMembers(pt) {
		if mb.Kind == MemberImplMethod {
			impl = append(impl, w.items.Module(w.items.Get(mb.Item).Module).Path+"."+w.in.Str(mb.Name))
		}
	}
	if want := []string{"a.Tag", "b.Size"}; !reflect.DeepEqual(impl, want) {
		t.Errorf("Point's impl methods from b: %v, want %v", impl, want)
	}
	_, loaded := loadedChecker(t, w, ids...)
	c2 := loaded[0].c
	compareIndex(t, w, c2, ids, "loaded")
}

// TestImplScansDoNotGrowWithLookups: K modules each with impls for one
// struct, and many member lookups from the last: the implList entries
// visited (index builds and scans) are the same after 1 and after 400
// lookups, where the scans visited every module's impls on every lookup.
func TestImplScansDoNotGrowWithLookups(t *testing.T) {
	w := newWorld(t, true)
	w.add("m0", opt{}, "export struct S {\n\tx: int\n}\n")
	prev := "m0"
	const k = 8
	for i := 1; i <= k; i++ {
		var src strings.Builder
		for j := 0; j < i; j++ {
			fmt.Fprintf(&src, "import m%d\n", j)
		}
		fmt.Fprintf(&src, "\nexport interface I%d {\n\tM%d(&self): int\n}\n\nimpl I%d for m0.S {\n\tM%d(&self): int {\n\t\treturn %d\n\t}\n}\n", i, i, i, i, i)
		fmt.Fprintf(&src, "\nexport struct T%d {\n\tx: int\n}\n\nimpl I%d for T%d {\n\tM%d(&self): int {\n\t\treturn 0\n\t}\n}\n", i, i, i, i)
		prev = fmt.Sprintf("m%d", i)
		b := w.add(prev, opt{}, src.String())
		if len(b.m.Diags) != 0 {
			t.Fatalf("%s: %s", prev, dump(w, b.m.Diags))
		}
	}
	last := w.mods[prev].m
	s := w.tab.Named(w.mods["m0"].item("S"), nil)
	visited := func() int64 {
		var n int64
		for _, b := range w.mods {
			if b.m != nil {
				n += b.m.state().scans.Load()
			}
		}
		return n
	}
	cands, _ := last.lookup(s, w.in.Intern("M1"))
	if len(cands) != 1 {
		t.Fatalf("M1 on S: %d candidates", len(cands))
	}
	after1 := visited()
	for i := 0; i < 400; i++ {
		last.lookup(s, w.in.Intern(fmt.Sprintf("M%d", 1+i%k)))
		last.collectMembers(ctorKey{item: w.mods["m0"].item("S")}) // the index alone, past the memo
	}
	if n := visited(); n != after1 {
		t.Errorf("400 more lookups visited %d more impl entries; want 0 (scans would visit %d per lookup)", n-after1, k*2)
	}
	if got := len(last.visibleMembers(s)); got != k+1 {
		t.Errorf("S has %d members from m%d, want %d (its field and %d impl methods)", got, k, k+1, k)
	}
}

// TestImplStateDoesNotPinModules: nothing outside a world keeps its
// Modules (the state is their impls field), so a dropped world is collected.
func TestImplStateDoesNotPinModules(t *testing.T) {
	wp := func() weak.Pointer[Module] {
		w, ids := indexWorld(t)
		b := w.c.module(ids[3])
		b.visibleMembers(w.tab.Named(w.mods["lib"].item("Point"), nil))
		if b.closureIndex() == nil {
			t.Fatal("no closure index")
		}
		return weak.Make(b)
	}()
	for i := 0; i < 4 && wp.Value() != nil; i++ {
		runtime.GC()
	}
	if wp.Value() != nil {
		t.Fatal("a checked Module is still reachable after its world was dropped")
	}
}
