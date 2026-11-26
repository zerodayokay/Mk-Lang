// Why: std.derive's public generators are documented in its library page.
// What: TestDeriveDeclared and TestDeriveEndToEnd.
// How: checks the exported signatures against the library page,
// then builds and runs a program whose gen lines use Eq, Hash and Format.

package std

import (
	"strings"
	"testing"
)

// TestDeriveDeclared: derive/derive.mk exports Eq, Hash and Format with
// the generator signature (g: mut Gen, t: Type) shown on the library page.
func TestDeriveDeclared(t *testing.T) {
	src := readText(t, "derive/derive.mk")
	doc := readText(t, "../doc/std/derive.md")
	_, api, ok := strings.Cut(doc, "\n<!-- api:begin -->\n")
	if !ok {
		t.Fatal("std.derive's library page has no API section")
	}
	api, _, ok = strings.Cut(api, "\n<!-- api:end -->")
	if !ok {
		t.Fatal("std.derive's library page has no API end marker")
	}
	for _, n := range []string{"Eq", "Hash", "Format"} {
		sig := "export " + n + "(g: mut Gen, t: Type)"
		if !strings.Contains(src, "\n"+sig+" {\n") {
			t.Errorf("derive/derive.mk does not declare %q", sig)
		}
		if !strings.Contains(api, "\n"+sig+"\n") {
			t.Errorf("std.derive's library page does not document %q", sig)
		}
	}
}

const deriveProgram = `import std.derive

struct User {
    name: str
    age: int
}

gen derive.Eq(User)
gen derive.Hash(User)
gen derive.Format(User)

export Main(sys: &System): WriteError|OutOfMemory!void {
    var out := sys.Stdout()
    a := User(name: "ann", age: 3)
    b := User(name: "ann", age: 3)
    c := User(name: "bob", age: 3)
    try a.Format(mut out)
    ab := a.Eq(&b)
    ac := a.Eq(&c)
    try Print(mut out, " {ab} {ac}\n", (ab, ac))
    var m := Map[User, int].New(sys.arena)
    try m.Set(a, 1)
    try m.Set(c, 2)
    try m.Set(b, 5)
    n := m.Len()
    v := m.Get(&b).Must()
    w := *v
    try Print(mut out, "{n} {w}\n", (n, w))
}
`

// TestDeriveEndToEnd: deriveProgram prints `User(name: ann, age: 3)
// true false` and `2 5` (a derived struct as a Map key).
func TestDeriveEndToEnd(t *testing.T) {
	t.Parallel()
	out, status := checkProgram(t, nil, deriveProgram)
	if status != 0 {
		t.Fatalf("mk check: exit %d\n%s", status, out)
	}
	r := runNative(t, buildNative(t, nil, deriveProgram), "")
	wantRun(t, r, 0, "User(name: ann, age: 3) true false\n2 5\n")
}
