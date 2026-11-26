// Why: a global is never assigned or borrowed mut (E0507), and its use is a
// temporary reference to it, so the report must name the global, not the
// temporary, and say what changes a global instead.
// What: TestGlobalWriteNamesGlobal.
// How: each write or mut borrow of a global, through an index, a field, a
// whole assignment and a mut method, checked as a package; its E0507 text
// names the global and offers an atomic or a Mutex. A write through a
// reference the global holds is spelled from the global with the shared
// reference's text, and one through a local reference keeps the local's name.

package driver

import (
	"slices"
	"strings"
	"testing"

	"mk/internal/types"
)

func TestGlobalWriteNamesGlobal(t *testing.T) {
	t.Parallel()
	const decls = "import std.atomic\n\nstruct Pair {\n    a: atomic.AtomicInt\n    n: int\n}\n\nPair.Bump(mut self) {\n    self.n += 1\n}\n\n" +
		"var Table: [8]u64 = [0; 8]\nvar P: Pair = Pair(a: atomic.AtomicInt.New(0), n: 0)\nvar Total: atomic.AtomicInt = atomic.AtomicInt.New(0)\n\n"
	const alt = "try: a global changes only through its methods: make it, or the part that changes, an atomic or a Mutex"
	for _, c := range []struct{ name, stmt, want string }{
		{"index", "Table[7] = 42", "E0507 mut-through-shared: Table[_] is part of the global Table, which is never assigned"},
		{"field", "P.n = 3", "E0507 mut-through-shared: P.n is part of the global P, which is never assigned"},
		{"whole", "Total = atomic.AtomicInt.New(1)", "E0507 mut-through-shared: Total is a global, which is never assigned"},
		{"mut method", "P.Bump()", "E0507 mut-through-shared: P is a global, which is never borrowed mut"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src := decls + "export F() {\n    " + c.stmt + "\n}\n"
			res, _, _ := gen10Check(t, map[string]string{"a/a.mk": src}, types.Dev)
			text := res.Report.Text(res.Sources)
			if got := codes(res); !slices.Equal(got, []string{"E0507"}) || !strings.Contains(text, c.want) || !strings.Contains(text, alt) || strings.Contains(text, "temporary") {
				t.Errorf("codes %q, want one E0507 %q with %q:\n%s", got, c.want, alt, text)
			}
		})
	}
	const holder = "struct Inner {\n    n: int\n}\n\nstruct Holder {\n    a: atomic.AtomicInt\n    r: &Inner\n}\n\nconst I: Inner = Inner(n: 1)\n\nvar H: Holder = Holder(a: atomic.AtomicInt.New(0), r: &I)\n\n"
	for _, c := range []struct{ name, body, want string }{
		{"through a local reference", "    t := &Table\n    t[1] = 2\n", "E0507 mut-through-shared: t[_] is reached through a shared reference and cannot be written"},
		{"through a held reference", "    H.r.n = 2\n", "E0507 mut-through-shared: H.r.n is reached through a shared reference and cannot be written"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src := decls + holder + "export F() {\n" + c.body + "}\n"
			res, _, _ := gen10Check(t, map[string]string{"a/a.mk": src}, types.Dev)
			text := res.Report.Text(res.Sources)
			if got := codes(res); !slices.Equal(got, []string{"E0507"}) || !strings.Contains(text, c.want) || strings.Contains(text, "try:") {
				t.Errorf("codes %q, want one E0507 %q and no alternative:\n%s", got, c.want, text)
			}
		})
	}
}
