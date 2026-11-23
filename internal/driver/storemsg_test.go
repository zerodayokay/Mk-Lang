// Why: a store that reaches a parameter through a local's held loan is hard
// to understand unless E0604 and E0505 name the holder and the way out.
// What: tests of E0604's and E0505's text for such a store (internal/borrow
// store.go viaText), and of the alternative E0604 gives when no clause can
// declare the store (storefix.go noClauseAlt), each named after the
// dogfood program that found it.
// How: checks small programs, compares the diagnostic text, then applies
// the offered clause and checks the fixed program and its callers.

package driver

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"mk/internal/diag"
)

// TestE0604NamesHolder: a str read from a shared parameter whose
// contents hold a read-only view (`prog: &Program`, a `List` field) and pushed
// into a local list is E0604 naming the local that makes `a` a target, and the
// loan fact names `*prog`, not the shared parameter itself. Because `List` is
// a read-only view through `&`, `*prog into a` alone clears it and `*prog into
// prog` is rejected with E0507.
func TestE0604NamesHolder(t *testing.T) {
	t.Parallel()
	const decls = "struct Fun {\n    name: str\n}\n\nstruct Program {\n    funs: List[Fun]\n}\n\n"
	body := "    var rows := List[str].New(a)\n    for f in prog.funs {\n        try rows.Push(f.name)\n    }\n    return rows.Len()\n}\n"
	r := checkText(t, decls+"Names(a: &Arena, prog: &Program): OutOfMemory!int {\n"+body)
	if len(r.Report.Diags) != 1 {
		t.Fatalf("want one E0604:\n%s", diagLines(r))
	}
	d := r.Report.Diags[0].Diag
	if d.Code.String() != "E0604" || !strings.HasSuffix(d.Message, "(`rows` holds a loan of `a`, so the store reaches it); write `*prog into a`") {
		t.Errorf("got %v %q", d.Code, d.Message)
	}
	if len(d.Fixes) != 1 || len(d.Fixes[0].Edits) != 1 {
		t.Errorf("want one fix with one edit, got %+v", d.Fixes)
	}
	if one := checkText(t, decls+"Names(a: &Arena, prog: &Program): OutOfMemory!int *prog into a {\n"+body); len(one.Report.Diags) != 0 {
		t.Errorf("`*prog into a` alone, the clause E0604 names, does not clear it:\n%s", diagLines(one))
	}
	if ro := checkText(t, decls+"Names(a: &Arena, prog: &Program): OutOfMemory!int *prog into prog {\n"+body); !slices.Contains(codes(ro), "E0507") {
		t.Errorf("with `*prog into prog` on read-only `&Program`, want E0507:\n%s", diagLines(ro))
	}
	loan := ""
	for _, f := range d.Facts {
		if f.Key == "loan" {
			loan = f.Value.Str
		}
	}
	if loan != "*prog" {
		t.Errorf("loan fact names %q, want `*prog`", loan)
	}
	if both := checkText(t, decls+"Names(a: &Arena, prog: &Program): OutOfMemory!int *prog into a, *prog into prog {\n"+body); !slices.Equal(codes(both), []string{"E0507"}) {
		t.Errorf("`*prog into prog` on read-only `&Program` should be E0507 alone:\n%s", diagLines(both))
	}
}

// TestE0604ReceiverClause: a key of `self.map.Iter()` pushed into a list
// in `arena` is E0604 on a method, and its fix is the receiver's clause
// `&self into arena`, as the free function's is `m: &TextMap[int] into
// arena`: the receiver is the first parameter. Both fixes are proven
// (private functions) and the written clause clears each body. A caller
// whose arena dies before the map checks; one passing `sys.arena` with a
// local map is E0505, and one taking a Names apart while the arena holds
// its keys is E0503.
func TestE0604ReceiverClause(t *testing.T) {
	t.Parallel()
	src := "struct Names {\n    map: TextMap[int]\n}\n\n" +
		"Names.Keys(&self, arena: &Arena): OutOfMemory!List[str] from arena, self {\n    var out := List[str].New(arena)\n    for (key, value) in self.map.Iter() {\n        _ = value\n        try out.Push(key)\n    }\n    return out\n}\n\n" +
		"Keys(m: &TextMap[int], arena: &Arena): OutOfMemory!List[str] from arena, m {\n    var out := List[str].New(arena)\n    for (key, value) in m.Iter() {\n        _ = value\n        try out.Push(key)\n    }\n    return out\n}\n"
	r := checkText(t, src)
	if len(r.Report.Diags) != 2 {
		t.Fatalf("want two E0604:\n%s", diagLines(r))
	}
	m, f := r.Report.Diags[0].Diag, r.Report.Diags[1].Diag
	if !strings.HasSuffix(m.Message, "(`out` holds a loan of `arena`, so the store reaches it); write `&self into arena`") || len(m.Alternatives) != 0 {
		t.Errorf("method: %q alternatives %q", m.Message, m.Alternatives)
	}
	if len(m.Fixes) != 1 || m.Fixes[0].Class != diag.Proven || len(m.Fixes[0].Edits) != 1 || m.Fixes[0].Edits[0].Text != " into arena" {
		t.Errorf("method: want one proven fix writing ` into arena`, got %+v", m.Fixes)
	}
	if len(f.Fixes) == 1 && f.Fixes[0].Class != diag.Proven {
		t.Errorf("function: fix class %v, want proven as the method's", f.Fixes[0].Class)
	}
	const method = "struct Names {\n    map: TextMap[int]\n}\n\n" +
		"Names.Keys(&self into arena, arena: &Arena): OutOfMemory!List[str] from arena, self {\n    var out := List[str].New(arena)\n    for (key, value) in self.map.Iter() {\n        _ = value\n        try out.Push(key)\n    }\n    return out\n}\n\n"
	if ok := checkText(t, method); len(ok.Report.Diags) != 0 {
		t.Errorf("the clause E0604 names does not clear the method:\n%s", diagLines(ok))
	}
	if !strings.HasSuffix(f.Message, "(`out` holds a loan of `arena`, so the store reaches it); write `m: &TextMap[int] into arena`") {
		t.Errorf("function: %q", f.Message)
	}
	if len(f.Fixes) != 1 || len(f.Fixes[0].Edits) != 1 {
		t.Errorf("function: want one fix with one edit, got %+v", f.Fixes)
	}
	dup := strings.Replace(src, "    for (key, value) in m.Iter() {\n        _ = value\n        try out.Push(key)", "    for (key, value) in m.Iter() {\n        _ = value\n        try out.Push(try arena.Dup(key))", 1)
	if ok := checkText(t, dup); len(ok.Report.Diags) != 1 {
		t.Errorf("the function's `Dup` does not clear it (the method's E0604 stays):\n%s", diagLines(ok))
	}
	const fn = "Keys(m: &TextMap[int] into arena, arena: &Arena): OutOfMemory!List[str] from arena, m {\n    var out := List[str].New(arena)\n    for (key, value) in m.Iter() {\n        _ = value\n        try out.Push(key)\n    }\n    return out\n}\n\n"
	const head = "export Main(sys: &System): OutOfMemory|WriteError!void {\n    a := sys.heap.Alloc()\n    var m := TextMap[int].New(a)\n    defer m.Free(a)\n    try m.Set(a, \"x\", 1)\n"
	const tail = "    try sys.Print(\"{n}\\n\", (n: keys.Len()))\n}\n"
	if ok := checkText(t, fn+head+"    var la := try Arena.New(sys.heap)\n    defer la.Free()\n    keys := try Keys(&m, &la)\n"+tail); len(ok.Report.Diags) != 0 {
		t.Errorf("the clause E0604 names, with an arena that dies before the map: want clean:\n%s", diagLines(ok))
	}
	long := checkText(t, fn+head+"    keys := try Keys(&m, sys.arena)\n"+tail)
	if got := codes(long); !slices.Equal(got, []string{"E0505"}) {
		t.Errorf("the clause with `sys.arena` and a local map: codes %q, want [E0505]:\n%s", got, diagLines(long))
	}
	// The method with its clause, called as the function is: a Names
	// that owns the map is taken apart only after the arena is freed.
	const mhead = "export Main(sys: &System): OutOfMemory|WriteError!void {\n    a := sys.heap.Alloc()\n    var m := TextMap[int].New(a)\n    m.Set(a, \"x\", 1) else {}\n    n := Names(map: m)\n"
	const mtail = "    count := keys.Len()\n    la.Free()\n    Names(map) := n\n    map.Free(a)\n    try sys.Print(\"{n}\\n\", (n: count))\n}\n"
	if ok := checkText(t, method+mhead+"    var la := Arena.New(sys.heap) else { Names(map) := n\n        map.Free(a)\n        return }\n    keys := n.Keys(&la) else { List[str].New(&la) }\n"+mtail); len(ok.Report.Diags) != 0 {
		t.Errorf("the method's clause, with an arena freed before the map: want clean:\n%s", diagLines(ok))
	}
	const late = "    count := keys.Len()\n    Names(map) := n\n    map.Free(a)\n    la.Free()\n    try sys.Print(\"{n}\\n\", (n: count))\n}\n"
	mlong := checkText(t, method+mhead+"    var la := Arena.New(sys.heap) else { Names(map) := n\n        map.Free(a)\n        return }\n    keys := n.Keys(&la) else { List[str].New(&la) }\n"+late)
	if got := codes(mlong); !slices.Equal(got, []string{"E0503"}) {
		t.Errorf("the method's clause, with the map taken apart while the arena holds its keys: codes %q, want [E0503]:\n%s", got, diagLines(mlong))
	}
}

// TestE0604NamesArena: strings read from the items of a
// `match` on a `&json.Value` and pushed into a local list in `a` are E0604
// naming the list's arena. Because a `str` is shallow, E0604 offers
// `v: &Value into a`, a store into the list's arena, and it clears the
// body. A caller whose arenas are local and die in order checks; one
// passing `sys.arena` with a tree parsed into `sys.arena` is E0505 through
// `*sys`.
func TestE0604NamesArena(t *testing.T) {
	t.Parallel()
	const strs = "import std.json\n\nStrs(a: &Arena, v: &json.Value%s): OutOfMemory!List[str] from a, v {\n    var out := List[str].New(a)\n    match v {\n        .Array(items) {\n            for item in &items {\n                s := item.Str() else { continue }\n                try out.Push(s)\n            }\n        }\n        .Null | .Bool | .Number | .Text | .Object { }\n    }\n    return out\n}\n\n"
	r := checkText(t, fmt.Sprintf(strs, ""))
	if len(r.Report.Diags) != 1 {
		t.Fatalf("want one E0604:\n%s", diagLines(r))
	}
	if d := r.Report.Diags[0].Diag; d.Code.String() != "E0604" || !strings.HasSuffix(d.Message, "(`out` holds a loan of `a`, so the store reaches it); write `v: &Value into a`") || len(d.Fixes) != 1 {
		t.Errorf("got %v %q fixes %+v", d.Code, d.Message, d.Fixes)
	}
	const local = "export Main(sys: &System): json.Error|WriteError!void {\n    var pa := try Arena.New(sys.heap)\n    defer pa.Free()\n    v := try json.Parse(&pa, \"[\\\"a\\\", \\\"b\\\"]\")\n    var la := try Arena.New(sys.heap)\n    defer la.Free()\n    xs := try Strs(&la, &v)\n    try sys.Print(\"{n}\\n\", (n: xs.Len()))\n}\n"
	if ok := checkText(t, fmt.Sprintf(strs, " into a")+local); len(ok.Report.Diags) != 0 {
		t.Errorf("the clause E0604 names, local arenas: want clean:\n%s", diagLines(ok))
	}
	const sysArena = "export Main(sys: &System): json.Error|WriteError!void {\n    v := try json.Parse(sys.arena, \"[\\\"a\\\", \\\"b\\\"]\")\n    xs := try Strs(sys.arena, &v)\n    try sys.Print(\"{n}\\n\", (n: xs.Len()))\n}\n"
	if e := checkText(t, fmt.Sprintf(strs, " into a")+sysArena); !slices.Equal(codes(e), []string{"E0505"}) {
		t.Errorf("the clause with `sys.arena`: codes %q, want [E0505]:\n%s", codes(e), diagLines(e))
	}
}

// TestE0505NamesHolder: a local arena's loan stored beside a `sys.arena`
// reference is E0505, since the local arena reaches `sys`'s allocators; the
// message names the target and its holder (`arena`). Storing read-only
// `sys.args` strings (which reach no allocator) into the local arena instead
// checks clean. The same store from `sys.arena` on a helper goes into `sys`'s
// own allocator, which holds `*sys` by being part of it, so it checks clean
// with the clause or without it.
func TestE0505NamesHolder(t *testing.T) {
	t.Parallel()
	const keep = "Keep(a: &Arena into out, names: []str, out: mut List[str]): OutOfMemory!void *names into out {\n    for n in names {\n        try out.Push(try a.Dup(n))\n    }\n}\n\n"
	r := checkText(t, keep+"Grep(sys: &System): OutOfMemory!int {\n    var arena := try Arena.New(sys.heap)\n    defer arena.Free()\n    a := &arena\n    var refs := List[&Arena].New(a)\n    try refs.Push(sys.arena)\n    var files := List[str].New(a)\n    try Keep(a, sys.args[..], mut files)\n    return files.Len() + refs.Len()\n}\n")
	if len(r.Report.Diags) != 1 {
		t.Fatalf("want one E0505:\n%s", diagLines(r))
	}
	if d := r.Report.Diags[0].Diag; d.Code.String() != "E0505" || !strings.Contains(d.Message, "into `*sys` (`arena` holds a loan of `*sys`, so the store reaches it)") {
		t.Errorf("got %v %q", d.Code, d.Message)
	}
	clean := checkText(t, keep+"GrepArgs(sys: &System): OutOfMemory!int {\n    var arena := try Arena.New(sys.heap)\n    defer arena.Free()\n    a := &arena\n    var names := List[str].New(a)\n    try names.Push(sys.args[0])\n    var files := List[str].New(a)\n    try Keep(a, names[..], mut files)\n    return files.Len()\n}\n")
	if len(clean.Report.Diags) != 0 {
		t.Errorf("`sys.args` holds no allocator, want clean:\n%s", diagLines(clean))
	}
	e := checkText(t, keep+"Count(sys: &System): int {\n    var out := List[str].New(sys.arena)\n    Keep(sys.arena, sys.args[1..], mut out) else {\n        return 2\n    }\n    return out.Len()\n}\n")
	if len(e.Report.Diags) != 0 {
		t.Fatalf("want no diagnostic without the clause (C3's self filter):\n%s", diagLines(e))
	}
	ok := checkText(t, keep+"Count(sys: &System): int *sys into sys {\n    var out := List[str].New(sys.arena)\n    Keep(sys.arena, sys.args[1..], mut out) else {\n        return 2\n    }\n    return out.Len()\n}\n")
	if len(ok.Report.Diags) != 0 {
		t.Errorf("the clause E0604 names does not clear it:\n%s", diagLines(ok))
	}
}

// TestE0604SecondIntoReceiver: a receiver that already flows into d and is
// stored into c gets no clause (it takes one `into`, as a parameter does),
// and E0604 says to keep c and d in one struct that self flows into.
func TestE0604SecondIntoReceiver(t *testing.T) {
	t.Parallel()
	r := checkText(t, "struct Ref {\n    p: &i32\n}\n\nRef.Put(self into d, c: &Cell[?&i32], d: &Cell[?&i32]) {\n    c.Store(some(self.p))\n    d.Store(some(self.p))\n}\n")
	if len(r.Report.Diags) != 1 {
		t.Fatalf("want one E0604:\n%s", diagLines(r))
	}
	d := r.Report.Diags[0].Diag
	const want = "`self` already flows into `d`, and the receiver takes one `into`: keep `c` and `d` in one struct and let `self` flow into it"
	if d.Code.String() != "E0604" || len(d.Fixes) != 0 || len(d.Alternatives) != 1 || d.Alternatives[0] != want {
		t.Errorf("got %v fixes %+v alternatives %q", d.Code, d.Fixes, d.Alternatives)
	}
}

// TestE0604SecondInto: a parameter that already flows into one
// target cannot be declared into a second (one `into` per parameter), and
// E0604 says so instead of offering nothing.
//
// A chain may close through a `*q into p` step: with `a into
// self` and `*self into a`, a's loan stored into a is declared, so Twice's
// own store needs no second `into`. Without `*self into a` the chain does
// not close and the one-`into` E0604 stands.
func TestE0604SecondInto(t *testing.T) {
	t.Parallel()
	const head = "struct Inner {\n    names: List[str]\n}\n\nstruct Outer {\n    all: List[Inner]\n    first: List[str]\n}\n\n" +
		"Outer.Grow(mut self, a: &Arena into self): OutOfMemory!void *self into self {\n    var inner := Inner(names: List[str].New(a))\n    if self.first.Len() > 0 {\n        try inner.names.Push(self.first[0])\n    }\n    try self.all.Push(inner)\n}\n\n"
	oneInto := func(r Result) bool {
		for _, e := range r.Report.Diags {
			for _, alt := range e.Diag.Alternatives {
				if strings.Contains(alt, "`a` already flows into `self`, and a parameter takes one `into`") {
					return true
				}
			}
		}
		return false
	}
	r := checkText(t, head+"Outer.Twice(mut self, a: &Arena into self): OutOfMemory!void *self into self, *a into a {\n    try self.Grow(a)\n}\n")
	if !oneInto(r) {
		t.Errorf("no E0604 names the one-`into` rule:\n%s", diagLines(r))
	}
	closed := checkText(t, head+"Outer.Twice(mut self, a: &Arena into self): OutOfMemory!void *self into self, *a into a, *self into a {\n    try self.Grow(a)\n}\n")
	if oneInto(closed) {
		t.Errorf("`a into self` and `*self into a` declare a's loan into a, yet E0604 names the one-`into` rule:\n%s", diagLines(closed))
	}
}
