// Why: freeing a container must release its elements, so a container whose
// elements are linear and not Free must not satisfy a Free bound, or it
// panics at run time.
// What: TestFreeBounds, TestSetBounds, TestNoFreeNoBound, TestOpenT, TestOpenComposite, TestHomeExempt, TestPoolFreeUnchanged (release bounds), TestImplFreeRow,
// the container tests under Free bounds (TestGenericFreeBound to TestHomeIsTheModule) and TestHeapBTreeSet.
// How: builds worlds with and without std.core's Free (newReleaseWorld) and
// compares E0701 diagnostics and their release alternatives.

package check

import (
	"slices"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// newReleaseWorld is newWorldCore with Free among the prelude names only
// when std.core declares it (preludeNames has it).
func newReleaseWorld(t testing.TB, core string, withFree bool) *world {
	t.Helper()
	var names []string
	for _, n := range preludeNames {
		if n != "Free" || withFree {
			names = append(names, n)
		}
	}
	w := &world{t: t, sm: source.NewSourceMap(), in: source.NewInterner(), items: types.NewItems(), mods: map[string]*built{}}
	w.consts = types.NewConstTable()
	w.tab = types.NewTable(w.in, w.items, w.consts)
	w.rs = resolve.NewSession(&resolve.Config{Interner: w.in, Sources: w.sm, Items: w.items, PreludePath: "std.core", PreludeNames: names})
	w.cfg = &Config{Interner: w.in, Types: w.tab, Items: w.items, Consts: w.consts, Target: types.X86_64Windows}
	w.c = New(w.cfg)
	b := w.add("std.core", opt{std: true, noPrelude: true, parseErrors: true}, core)
	w.core = b.id
	w.c.known = w.rs.Known()
	return w
}

// freeIfaceText is fakePrelude's interface Free.
const freeIfaceText = "export interface Free {\n\tFree(self, a: &Alloc)\n}\n"

// implFreeHeapList is std's unbounded impl of Free for HeapList.
const implFreeHeapList = "impl[T] Free for HeapList[T] {\n\tFree(self, a: &Alloc) {\n\t}\n}\n"

// releaseCore is the fake prelude with the containers' release methods and,
// when withFree, std.core's interface Free. With it, HeapList's Free is an
// unbounded impl, as std's is; without it, an inherent method.
func releaseCore(withFree bool) string {
	// std's HeapList is linear; the fake prelude's is not.
	s := strings.Replace(fakePrelude(), "view struct HeapList[T]", "linear view struct HeapList[T]", 1)
	if withFree {
		s += implFreeHeapList
	} else {
		s = strings.Replace(s, freeIfaceText, "", 1) + "export HeapList[T].Free(self, a: &Alloc) {\n}\n"
	}
	return s + "export HeapList[T].Again(self, a: &Alloc) {\n\tself.Free(a)\n}\n" +
		"export HeapMap[K, V].Set(mut self, a: &Alloc, k: K into self, v: V into self) {\n}\n" +
		"export Map[K, V].Set(mut self, k: K into self, v: V into self) {\n}\n" +
		"export Map[K, V].Remove(mut self, k: &K): ?V {\n\treturn none\n}\n"
}

const releaseUser = "linear struct File {\n\th: int\n}\nlinear struct Res {\n\th: int\n}\nimpl Free for Res {\n\tFree(self, a: &Alloc) {\n\t\tRes(h) := self\n\t\t_ = h\n\t}\n}\n"

func releaseCase(t *testing.T, withFree bool, src, want string) []*diag.Diagnostic {
	t.Helper()
	w := newReleaseWorld(t, releaseCore(withFree), withFree)
	user := releaseUser
	if !withFree {
		user = "linear struct File {\n\th: int\n}\n"
	}
	b := w.add("m", opt{}, user+src)
	var ds []*diag.Diagnostic
	for _, d := range append(b.diags(), b.bodyDiags(t)...) {
		if d.Code != diag.E0208 {
			ds = append(ds, d)
		}
	}
	got := []string{}
	for _, d := range ds {
		got = append(got, d.Code.String()+":"+fact(d, "type"))
	}
	if g := strings.Join(got, " "); g != want {
		t.Errorf("got %q, want %q\n%s", g, want, dump(w, ds))
	}
	return ds
}

func TestFreeBounds(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"linear element", "F(a: &Alloc, l: HeapList[File]) {\n\tl.Free(a)\n}\n", "E0701:File"},
		{"plain element", "F(a: &Alloc, l: HeapList[int]) {\n\tl.Free(a)\n}\n", ""},
		{"Free element", "F(a: &Alloc, l: HeapList[Res]) {\n\tl.Free(a)\n}\n", ""},
		{"nested", "F(a: &Alloc, l: HeapList[HeapList[File]]) {\n\tl.Free(a)\n}\n", "E0701:HeapList[File]"},
		{"nested Free", "F(a: &Alloc, l: HeapList[HeapList[Res]]) {\n\tl.Free(a)\n}\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ds := releaseCase(t, true, c.src, c.want)
			for _, d := range ds {
				if d.Code == diag.E0701 && (fact(d, "param") != "T" || len(d.Alternatives) != 2) {
					t.Errorf("facts: param %q alts %v", fact(d, "param"), d.Alternatives)
				}
			}
		})
	}
}

func TestSetBounds(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"heap map linear value", "F(a: &Alloc, m: mut HeapMap[int, File], f: File) {\n\tm.Set(a, 1, f)\n}\n", "E0701:File"},
		{"heap map Free value", "F(a: &Alloc, m: mut HeapMap[int, Res], r: Res) {\n\tm.Set(a, 1, r)\n}\n", ""},
		{"arena map Free value", "F(m: mut Map[int, Res], r: Res) {\n\tm.Set(1, r)\n}\n", "E0701:Res"},
		{"arena map Remove", "F(m: mut Map[Res, int], r: &Res) {\n\t_ = m.Remove(r)\n}\n", "E0701:Res"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			releaseCase(t, true, c.src, c.want)
		})
	}
}

func TestNoFreeNoBound(t *testing.T) {
	releaseCase(t, false, "F(a: &Alloc, l: HeapList[File]) {\n\tl.Free(a)\n}\n", "")
}

func TestOpenT(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"open", "G[T](a: &Alloc, l: HeapList[T]) {\n\tl.Free(a)\n}\n", "E0701:T"},
		{"plain", "G[T: Plain](a: &Alloc, l: HeapList[T]) {\n\tl.Free(a)\n}\n", ""},
		{"Free", "G[T: Free](a: &Alloc, l: HeapList[T]) {\n\tl.Free(a)\n}\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ds := releaseCase(t, true, c.src, c.want)
			for _, d := range ds {
				if !strings.Contains(d.Message, "may be linear") {
					t.Errorf("message %q", d.Message)
				}
			}
		})
	}
}

// TestOpenComposite: a type that is linear only through a type parameter
// it holds (`?T`, `[N]T`, the `?T` elements a HeapList's Free would drop)
// may be linear, and the report says so; one that is linear whatever its
// parameters (a HeapList[T] value, a record holding a File) is linear.
func TestOpenComposite(t *testing.T) {
	cases := []struct{ name, src, want, says string }{
		{"optional", "G[T](m: mut Map[int, ?T], x: T into m) {\n\tm.Set(1, some(x))\n}\n", "E0701:?T", "`?T` may be linear"},
		{"array", "G[T, const N: int](m: mut Map[int, [N]T], xs: [N]T into m) {\n\tm.Set(1, xs)\n}\n", "E0701:[N]T", "`[N]T` may be linear"},
		{"freed element", "G[T](a: &Alloc, l: HeapList[?T]) {\n\tl.Free(a)\n}\n", "E0701:?T", "`?T` may be linear and not `Free`"},
		{"heap list value", "G[T](m: mut Map[int, HeapList[T]], l: HeapList[T] into m) {\n\tm.Set(1, l)\n}\n", "E0701:HeapList[T]", "`HeapList[T]` is linear"},
		{"record of a file", "G[T](m: mut Map[int, (f: File, x: T)], f: File, x: T into m) {\n\tm.Set(1, (f: f, x: x))\n}\n", "E0701:(f: File, x: T)", "`(f: File, x: T)` is linear"},
		{"plain", "G[T: Plain](m: mut Map[int, ?T], x: T into m) {\n\tm.Set(1, some(x))\n}\n", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for _, d := range releaseCase(t, true, c.src, c.want) {
				if !strings.Contains(d.Message, c.says) {
					t.Errorf("message %q, want it to say %q", d.Message, c.says)
				}
			}
		})
	}
}

// TestHomeExempt: the container's own methods are not judged (the
// fake prelude's HeapList.Again frees an open T).
func TestHomeExempt(t *testing.T) {
	releaseCase(t, true, "F(a: &Alloc, l: HeapList[int]) {\n\tl.Again(a)\n}\n", "")
}

// TestPoolFreeUnchanged: with the release gate on (std.core declares
// Free), Pool.Free keeps its poolFree verdict and gains no release row:
// a linear T is E0701 "`Pool.Free` drops the value"
// with no release alternatives, even when T implements Free.
func TestPoolFreeUnchanged(t *testing.T) {
	const pool = "export struct Handle {\n\ti: u32\n}\n\nexport struct Pool[T] {\n\tn: int\n}\n\n" +
		"export Pool[T].Free(mut self, h: Handle): bool {\n\treturn false\n}\n"
	cases := []struct{ name, src, want string }{
		{"plain", "F(p: mut mem.Pool[int], h: mem.Handle): bool {\n\treturn p.Free(h)\n}\n", ""},
		{"linear", "F(p: mut mem.Pool[File], h: mem.Handle): bool {\n\treturn p.Free(h)\n}\n", "E0701:File"},
		{"Free element", "F(p: mut mem.Pool[Res], h: mem.Handle): bool {\n\treturn p.Free(h)\n}\n", "E0701:Res"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newReleaseWorld(t, releaseCore(true), true)
			w.add("std.mem", opt{std: true}, pool)
			b := w.add("m", opt{}, "import std.mem\n\n"+releaseUser+c.src)
			var ds []*diag.Diagnostic
			got := []string{}
			for _, d := range append(b.diags(), b.bodyDiags(t)...) {
				if d.Code != diag.E0208 {
					ds = append(ds, d)
					got = append(got, d.Code.String()+":"+fact(d, "type"))
				}
			}
			if g := strings.Join(got, " "); g != c.want {
				t.Fatalf("got %q, want %q\n%s", g, c.want, dump(w, ds))
			}
			for _, d := range ds {
				if !strings.Contains(d.Message, "`Pool.Free` drops the value") || len(d.Alternatives) != 0 || fact(d, "bound") != "not linear" {
					t.Errorf("not poolFree's verdict: %q alts %v bound %q", d.Message, d.Alternatives, fact(d, "bound"))
				}
			}
		})
	}
}

// TestImplFreeRow: a container's Free written inside
// `impl[T] Free for HeapList[T]` keeps its release row (the impl's
// target's). releaseCore(true) writes the impl, as std does; this runs the
// inherent form beside the interface too, for a container with no impl.
func TestImplFreeRow(t *testing.T) {
	core := strings.Replace(releaseCore(true), implFreeHeapList, "export HeapList[T].Free(self, a: &Alloc) {\n}\n", 1)
	if core == releaseCore(true) {
		t.Fatal("releaseCore(true) has no impl of Free for HeapList")
	}
	for _, c := range []struct{ name, src, want string }{
		{"linear element", "F(a: &Alloc, l: HeapList[File]) {\n\tl.Free(a)\n}\n", "E0701:File"},
		{"Free element", "F(a: &Alloc, l: HeapList[Res]) {\n\tl.Free(a)\n}\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newReleaseWorld(t, core, true)
			b := w.add("m", opt{}, releaseUser+c.src)
			got := []string{}
			for _, d := range append(b.diags(), b.bodyDiags(t)...) {
				if d.Code != diag.E0208 {
					got = append(got, d.Code.String()+":"+fact(d, "type"))
				}
			}
			if g := strings.Join(got, " "); g != c.want {
				t.Errorf("got %q, want %q", g, c.want)
			}
		})
	}
}

// cn1Case is releaseCase with every E0701's type and element facts, and
// its alternatives.
func cn1Case(t *testing.T, src string, want string) []*diag.Diagnostic {
	t.Helper()
	w := newReleaseWorld(t, releaseCore(true), true)
	b := w.add("m", opt{}, releaseUser+src)
	var ds []*diag.Diagnostic
	got := []string{}
	for _, d := range append(b.diags(), b.bodyDiags(t)...) {
		if d.Code == diag.E0208 {
			continue
		}
		ds = append(ds, d)
		s := d.Code.String() + ":" + fact(d, "type")
		if e := fact(d, "element"); e != "" {
			s += "/" + e
		}
		got = append(got, s)
	}
	if g := strings.Join(got, " "); g != want {
		t.Errorf("got %q, want %q\n%s", g, want, dump(w, ds))
	}
	return ds
}

// cn1G is a user generic over a Free bound.
const cn1G = "G[C: Free](a: &Alloc, c: C) {\n\tc.Free(a)\n}\n"

// TestGenericFreeBound: a container of the table whose element is
// linear and not Free does not satisfy `C: Free`: E0701 at the call,
// naming the element and the container's alternatives. These were once
// accepted and panicked at run time.
func TestGenericFreeBound(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"linear element", "F(a: &Alloc, l: HeapList[File]) {\n\tG(a, l)\n}\n", "E0701:HeapList[File]/File"},
		{"nested", "F(a: &Alloc, l: HeapList[HeapList[File]]) {\n\tG(a, l)\n}\n", "E0701:HeapList[HeapList[File]]/File"},
		{"plain element", "F(a: &Alloc, l: HeapList[int]) {\n\tG(a, l)\n}\n", ""},
		{"Free element", "F(a: &Alloc, l: HeapList[Res]) {\n\tG(a, l)\n}\n", ""},
		{"nested Free", "F(a: &Alloc, l: HeapList[HeapList[Res]]) {\n\tG(a, l)\n}\n", ""},
		{"explicit", "F(a: &Alloc, l: HeapList[File]) {\n\tG[HeapList[File]](a, l)\n}\n", "E0701:HeapList[File]/File"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for _, d := range cn1Case(t, cn1G+c.src, c.want) {
				if fact(d, "param") != "C" || len(d.Alternatives) != 2 || d.Alternatives[0] != "FreeEmpty" ||
					!strings.Contains(d.Message, "its element `File` is linear and not `Free`") || len(d.Fixes) != 0 {
					t.Errorf("facts: param %q alts %v fixes %d message %q", fact(d, "param"), d.Alternatives, len(d.Fixes), d.Message)
				}
			}
		})
	}
}

// TestGenericInstantiation: a generic whose body frees a HeapList[T]
// under `T: Free` is accepted, and its instance with a T that is a
// container of files is E0701 at the call, at every depth.
func TestGenericInstantiation(t *testing.T) {
	const g2 = "G2[T: Free](a: &Alloc, l: HeapList[T]) {\n\tl.Free(a)\n}\n"
	cases := []struct{ name, src, want string }{
		{"Free T", "F(a: &Alloc, l: HeapList[Res]) {\n\tG2(a, l)\n}\n", ""},
		{"container T", "F(a: &Alloc, l: HeapList[HeapList[File]]) {\n\tG2(a, l)\n}\n", "E0701:HeapList[File]/File"},
		{"container of Free T", "F(a: &Alloc, l: HeapList[HeapList[Res]]) {\n\tG2(a, l)\n}\n", ""},
		{"deep", "F(a: &Alloc, l: HeapList[HeapList[HeapList[File]]]) {\n\tG2(a, l)\n}\n", "E0701:HeapList[HeapList[File]]/File"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cn1Case(t, g2+c.src, c.want)
		})
	}
}

// TestOpenElement: a generic caller passing HeapList[T] to `C: Free`
// needs T not linear or Free; an unbounded T may be linear.
func TestOpenElement(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"open", "H[T](a: &Alloc, l: HeapList[T]) {\n\tG(a, l)\n}\n", "E0701:HeapList[T]/T"},
		{"Free", "H[T: Free](a: &Alloc, l: HeapList[T]) {\n\tG(a, l)\n}\n", ""},
		{"Plain", "H[T: Plain](a: &Alloc, l: HeapList[T]) {\n\tG(a, l)\n}\n", ""},
		{"nested open", "H[T](a: &Alloc, l: HeapList[HeapList[T]]) {\n\tG(a, l)\n}\n", "E0701:HeapList[HeapList[T]]/T"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for _, d := range cn1Case(t, cn1G+c.src, c.want) {
				if !strings.Contains(d.Message, "may be linear") {
					t.Errorf("message %q", d.Message)
				}
			}
		})
	}
}

// TestImplBound: an impl bounded by `T: Free` (a user's wrapper) and a
// type expression's `C: Free` bound reach the same condition through
// implApplies.
func TestImplBound(t *testing.T) {
	const wrap = "linear struct W[T] {\n\tx: T\n}\n\nimpl[T: Free] Free for W[T] {\n\tFree(self, a: &Alloc) {\n\t\tW(x) := self\n\t\tx.Free(a)\n\t}\n}\n\n"
	cases := []struct{ name, src, want string }{
		{"wrapper of files", wrap + "F(a: &Alloc, w: W[HeapList[File]]) {\n\tG(a, w)\n}\n", "E0701:W[HeapList[File]]"},
		{"wrapper of Free", wrap + "F(a: &Alloc, w: W[HeapList[Res]]) {\n\tG(a, w)\n}\n", ""},
		{"list of wrappers", wrap + "F(a: &Alloc, l: HeapList[W[HeapList[File]]]) {\n\tl.Free(a)\n}\n", "E0701:W[HeapList[File]]"},
		{"type bound", "struct Hold[C: Free] {\n\tc: C\n}\n\nF(h: &Hold[HeapList[File]]) {\n}\n", "E0701:HeapList[File]/File"},
		{"type bound Free", "struct Hold[C: Free] {\n\tc: C\n}\n\nF(h: &Hold[HeapList[Res]]) {\n}\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cn1Case(t, cn1G+c.src, c.want)
		})
	}
}

// TestDirectNested: a container's own Free on a nested container was
// E0701 before (releaseOK recursed); it now also names the element.
func TestDirectNested(t *testing.T) {
	ds := cn1Case(t, "F(a: &Alloc, l: HeapList[HeapList[File]]) {\n\tl.Free(a)\n}\n", "E0701:HeapList[File]/File")
	for _, d := range ds {
		if !strings.Contains(d.Message, "since its element `File` is linear and not `Free`") || len(d.Alternatives) != 2 {
			t.Errorf("message %q alts %v", d.Message, d.Alternatives)
		}
	}
}

// TestImplOfAnswer: the public ImplOf, which mono's HasFree and
// Release ask through the driver, answers no for a container of
// linear elements that are not Free, at any depth, and yes otherwise; so
// HasFree folds false where check refused every Free, and mono's ICE
// (a Release of a linear type with no impl) is not reached through std's
// HasFree-guarded ReleaseValue.
func TestImplOfAnswer(t *testing.T) {
	w := newReleaseWorld(t, releaseCore(true), true)
	b := w.add("m", opt{}, releaseUser)
	core := w.mods["std.core"]
	free, list := core.item("Free"), core.item("HeapList")
	file, res := w.tab.Named(b.item("File"), nil), w.tab.Named(b.item("Res"), nil)
	hl := func(t types.TypeID) types.TypeID { return w.tab.Named(list, []types.GenericArg{types.TypeArg(t)}) }
	for _, c := range []struct {
		t    types.TypeID
		want bool
	}{
		{hl(file), false}, {hl(hl(file)), false}, {hl(hl(hl(file))), false},
		{hl(res), true}, {hl(hl(res)), true}, {hl(types.Int), true}, {res, true}, {file, false},
	} {
		if _, got := w.c.ImplOf(free, nil, c.t, &types.ReadSet{}); got != c.want {
			t.Errorf("ImplOf(Free, %s) = %v, want %v", w.tab.TypeString(c.t, nil), got, c.want)
		}
	}
}

// TestNarrowed: inside `if T is int`, a HeapList[T] meets `C: Free`,
// since its element is narrowed to int.
func TestNarrowed(t *testing.T) {
	cn1Case(t, cn1G+"H[T](a: &Alloc, l: HeapList[T]) {\n\tif T is int {\n\t\tG(a, l)\n\t\treturn\n\t}\n\tl.Free(a)\n}\n", "E0701:T")
}

// TestHomeIsTheModule: the home exemption is the container's own code
// in its own module. A user's impl of its own interface for HeapList is no
// home, so its Free of an open or linear element is judged.
func TestHomeIsTheModule(t *testing.T) {
	const mine = "interface Mine {\n\tGo(self, a: &Alloc)\n}\n\n"
	cases := []struct{ name, src, want string }{
		{"open", mine + "impl[T] Mine for HeapList[T] {\n\tGo(self, a: &Alloc) {\n\t\tself.Free(a)\n\t}\n}\n", "E0701:T"},
		{"linear", mine + "impl Mine for HeapList[File] {\n\tGo(self, a: &Alloc) {\n\t\tself.Free(a)\n\t}\n}\n", "E0701:File"},
		{"plain", mine + "impl Mine for HeapList[int] {\n\tGo(self, a: &Alloc) {\n\t\tself.Free(a)\n\t}\n}\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cn1Case(t, c.src, c.want)
		})
	}
}

// TestHeapBTreeSet: std.coll's HeapBTree.Set releases the
// old value and the surplus key with its a, as HeapMap.Set does, so a K or V
// that is linear and not Free is E0701 at the call (the reference's release
// paragraph). It was once accepted and panicked at run time (code 11).
// The diagnostic names Replace as the alternative, and judges the arena
// BTree.Set too (K and V not linear), whose run-time panic (code 8) is gone;
// there it names HeapBTree, since the arena tree's Replace refuses a linear
// value too.
func TestHeapBTreeSet(t *testing.T) {
	const coll = "export linear view struct HeapBTree[K, V] {\n\tk: *K\n\tv: *V\n}\n\n" +
		"export HeapBTree[K, V].Set(mut self, a: &Alloc, k: K into self, v: V into self) {\n}\n\n" +
		"export view struct BTree[K, V] {\n\tk: *K\n\tv: *V\n}\n\n" +
		"export BTree[K, V].Set(mut self, k: K into self, v: V into self) {\n}\n"
	cases := []struct{ name, src, want, elem string }{
		{"linear value", "F(a: &Alloc, t: mut coll.HeapBTree[int, File], f: File) {\n\tt.Set(a, 1, f)\n}\n", "E0701:File", ""},
		{"linear key", "F(a: &Alloc, t: mut coll.HeapBTree[File, int], f: File) {\n\tt.Set(a, f, 1)\n}\n", "E0701:File", ""},
		{"container value", "F(a: &Alloc, t: mut coll.HeapBTree[int, HeapList[File]], l: HeapList[File]) {\n\tt.Set(a, 1, l)\n}\n", "E0701:HeapList[File]", "File"},
		{"Free value", "F(a: &Alloc, t: mut coll.HeapBTree[int, Res], r: Res) {\n\tt.Set(a, 1, r)\n}\n", "", ""},
		{"container of Free", "F(a: &Alloc, t: mut coll.HeapBTree[int, HeapList[Res]], l: HeapList[Res]) {\n\tt.Set(a, 1, l)\n}\n", "", ""},
		{"plain", "F(a: &Alloc, t: mut coll.HeapBTree[int, int]) {\n\tt.Set(a, 1, 2)\n}\n", "", ""},
		{"arena BTree", "F(t: mut coll.BTree[int, File], f: File) {\n\tt.Set(1, f)\n}\n", "E0701:File", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newReleaseWorld(t, releaseCore(true), true)
			w.add("std.coll", opt{std: true}, coll)
			b := w.add("m", opt{}, "import std.coll\n\n"+releaseUser+c.src)
			var ds []*diag.Diagnostic
			got := []string{}
			for _, d := range append(b.diags(), b.bodyDiags(t)...) {
				if d.Code != diag.E0208 {
					ds = append(ds, d)
					got = append(got, d.Code.String()+":"+fact(d, "type"))
				}
			}
			if g := strings.Join(got, " "); g != c.want {
				t.Fatalf("got %q, want %q\n%s", g, c.want, dump(w, ds))
			}
			alts := []string{"Replace"}
			if strings.HasPrefix(c.name, "arena") {
				alts = []string{"HeapBTree"}
			}
			for _, d := range ds {
				if fact(d, "element") != c.elem || !slices.Equal(d.Alternatives, alts) ||
					!strings.Contains(d.Message, "BTree.Set` would drop the value it replaces") {
					t.Errorf("facts: element %q alts %v message %q", fact(d, "element"), d.Alternatives, d.Message)
				}
			}
		})
	}
}
