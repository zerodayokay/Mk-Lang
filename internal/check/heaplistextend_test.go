// Why: HeapList[T].Extend and List[T].Resize copy elements, so T must be
// Plain.
// What: TestHeapListExtendPlain, TestListResizePlain.
// How: checks the body against the implicit `T: Plain` and calls with plain
// and non-plain elements.

package check

import "testing"

// TestHeapListExtendPlain: std.core's `HeapList[T].Extend`
// copies its slice's elements, so its body sees an implicit `T: Plain`
// (a copy that needs it, here `CopyFrom`, checks clean) and every call whose
// element is not plain is E0701 at the method name, by a value or in the
// namespace form.
func TestHeapListExtendPlain(t *testing.T) {
	core := fakePrelude() + "export Slice[T].CopyFrom(mut self, src: []T) {\n}\n" +
		"export struct HeapList[T] {\n\tn: int\n}\n" +
		"export HeapList[T].Extend(mut self, xs: []T, d: mut []T) {\n\td.CopyFrom(xs)\n}\n" +
		"export HeapList[T].Other(mut self, xs: []T) {\n}\n"
	for _, tc := range []struct{ name, src, want string }{
		{"plain", "F(l: mut HeapList[int], xs: []int, d: mut []int) {\n\tl.Extend(xs, d)\n}\n", ""},
		{"plain struct", "struct P {\n\tn: int\n}\n\nF(l: mut HeapList[P], xs: []P, d: mut []P) {\n\tl.Extend(xs, d)\n}\n", ""},
		{"not plain", "F(l: mut HeapList[Box[int]], xs: []Box[int], d: mut []Box[int]) {\n\tl.Extend(xs, d)\n}\n", "E0701@2:4"},
		{"not plain, namespace form", "F(l: mut HeapList[Box[int]], xs: []Box[int], d: mut []Box[int]) {\n\tHeapList[Box[int]].Extend(l, xs, d)\n}\n", "E0701@2:21"},
		{"generic caller, unbounded", "F[T](l: mut HeapList[T], xs: []T, d: mut []T) {\n\tl.Extend(xs, d)\n}\n", "E0701@2:4"},
		{"generic caller, plain", "F[T: Plain](l: mut HeapList[T], xs: []T, d: mut []T) {\n\tl.Extend(xs, d)\n}\n", ""},
		{"another method has no bound", "F(l: mut HeapList[Box[int]], xs: []Box[int]) {\n\tl.Other(xs)\n}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorldCore(t, true, core)
			core := w.mods["std.core"]
			if got := codes(w, core.bodyDiags(t)); got != "" {
				t.Fatalf("std.core bodies: %s", got)
			}
			checkBody(t, w, tc.want, tc.src)
		})
	}
}

// TestListResizePlain: std.core's `List[T].Resize` writes copies of x, so its
// body sees an implicit `T: Plain` (a call that needs it, here `Copy`, checks
// clean) and every call whose element is not plain is E0701 at the method
// name, by a value or in the namespace form.
func TestListResizePlain(t *testing.T) {
	core := fakePrelude() + "export struct List[T] {\n\tn: int\n}\n" +
		"Copy[U: Plain](x: U) {\n}\n" +
		"export List[T].Resize(mut self, n: int, x: T) {\n\tCopy(x)\n}\n" +
		"export List[T].Other(mut self, x: T) {\n}\n"
	for _, tc := range []struct{ name, src, want string }{
		{"plain", "F(l: mut List[int]) {\n\tl.Resize(3, 7)\n}\n", ""},
		{"not plain", "F(l: mut List[Box[int]], x: Box[int]) {\n\tl.Resize(3, x)\n}\n", "E0701@2:4"},
		{"not plain, namespace form", "F(l: mut List[Box[int]], x: Box[int]) {\n\tList[Box[int]].Resize(l, 3, x)\n}\n", "E0701@2:17"},
		{"generic caller, unbounded", "F[T](l: mut List[T], x: T) {\n\tl.Resize(3, x)\n}\n", "E0701@2:4"},
		{"generic caller, plain", "F[T: Plain](l: mut List[T], x: T) {\n\tl.Resize(3, x)\n\tl.Resize(4, x)\n}\n", ""},
		{"another method has no bound", "F(l: mut List[Box[int]], x: Box[int]) {\n\tl.Other(x)\n}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorldCore(t, true, core)
			core := w.mods["std.core"]
			if got := codes(w, core.bodyDiags(t)); got != "" {
				t.Fatalf("std.core bodies: %s", got)
			}
			checkBody(t, w, tc.want, tc.src)
		})
	}
}
