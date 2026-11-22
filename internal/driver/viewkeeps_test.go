// Why: borrow's view scan reads other modules, and an incremental build is
// only sound if every such read is recorded, so an edit there re-checks the
// body.
// What: vkCases, a table of toggled programs (vkToggle), and
// TestViewKeepsColdWarm and TestViewKeepsReused.
// How: each case checks a program cold and with one warm cache across edits
// that add and remove lines, and wants the same verdict both ways. The scan
// (internal/borrow typewalk.go viewKeepsScan) must see these edits:
//   - an impl for &V added in the body's closure is in V's member list, so
//     the body's ReadExports of V's namespace sees it (iface refImplsOf);
//   - a function of V's module that stores into a parameter holding V counts
//     for a body of another module only when it is in that module's API (an
//     exported function or method, or an impl's method), since a private one
//     leaves the API hash that a foreign body's result is validated against
//     as it was (holdStores);
//   - an impl of V's module whose For has no type of the module to list it
//     (`for i32`, `for ?V`, `for []V`) is in the module's function list
//     (iface.ModuleFuncsKey), which holdStores reads, and an impl retargeted
//     between instances of a generic view (`for &G[i64]` to `&G[i32]`) is
//     read by its signature before the impl scan's bind test (without it a
//     warm check gave ok and a cold one E0605 in the order without, then
//     with).

package driver

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// vkToggle is one case: its files, and the file whose //A-BEGIN to //A-END
// lines exist only in version "with" and //B-BEGIN to //B-END only in
// "without"; want is the check's verdict with and without.
type vkToggle struct {
	name                  string
	files                 map[string]string
	file                  string
	wantWith, wantWithout string
}

var vkBlock = map[string]*regexp.Regexp{
	"A": regexp.MustCompile(`(?s)//A-BEGIN\n.*?//A-END\n`),
	"B": regexp.MustCompile(`(?s)//B-BEGIN\n.*?//B-END\n`),
}

func vkVersion(text string, with bool) string {
	if with {
		return vkBlock["B"].ReplaceAllString(text, "")
	}
	return vkBlock["A"].ReplaceAllString(text, "")
}

const vkStore = `    unsafe "probe: Ptr is the GM that V.Of was given" {
        m := &*vv.Ptr()
        var g := m.Lock(io) else {
            return
        }
        g.Store(some(x))
        g.Unlock()
    }
`

// vkView is module v: a view V that keeps &GM by V.Of, a private holder H,
// and the toggled declarations.
func vkView(toggle string) string {
	return `import std.raw

export var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

export view struct V {
    p: *Mutex[?&i32]
}

export V.Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

export V.Ptr(&self): *Mutex[?&i32] {
    return self.p
}

struct H {
    v: V
}

export interface Sink {
    Put(&self, io: Io, x: &i32 into self)
}
` + toggle
}

// vkHoldFiles is a package whose main stores a local's reference through V
// by t.Put, a function of another module, which the view scan does not
// count; so main's verdict follows only what v's module declares.
func vkHoldFiles(toggle string) map[string]string {
	return map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"vk"`, "unsafe_modules": `[".", "v", "t"]`}),
		"main.mk": `import v
import t

MakeV(): v.V {
    return v.V.Of(&v.GM)
}

Stash(io: Io) {
    var n: i32 = 5
    vv := MakeV()
    t.Put(&vv, io, &n)
}
`,
		"t/t.mk": "import v\n\nexport Put(vv: &v.V, io: Io, x: &i32 into vv) {\n" + vkStore + "}\n",
		"v/v.mk": vkView(toggle),
	}
}

func vkCases() []vkToggle {
	const refused = "main.mk:11:5:E0605"
	hold := func(name, toggle, with string) vkToggle {
		return vkToggle{name: name, files: vkHoldFiles(toggle), file: "v/v.mk", wantWith: with, wantWithout: "ok"}
	}
	return []vkToggle{
		{name: "implref", file: "s/s.mk", wantWith: "main.mk:8:5:E0605", wantWithout: "ok", files: map[string]string{
			"pkg.mk": manifest(map[string]string{"name": `"vk"`, "unsafe_modules": `[".", "v", "s", "t"]`}),
			"main.mk": `import v
import t

Stash(io: Io) {
    var n: i32 = 5
    vv := v.V.Global()
    t.Touch()
    vv.Put(io, &n)
}
`,
			"t/t.mk": "import s\n\nexport Touch() {\n    s.Touch()\n}\n",
			"s/s.mk": `import v

export interface Src {
    Get(self): &Mutex[?&i32]
}

//A-BEGIN
impl Src for &v.V {
    Get(self): &Mutex[?&i32] from self {
        unsafe "probe: Ptr is GM's" {
            return &*self.Ptr()
        }
    }
}
//A-END

export Touch() {
}

export Size(vv: &v.V): int {
    _ = vv
    return 1
}
`,
			"v/v.mk": `import std.raw

export var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

export view struct V {
    p: *Mutex[?&i32]
}

export V.Global(): V {
    return V(p: raw.Of(&GM))
}

export V.Ptr(&self): *Mutex[?&i32] {
    return self.p
}

export V.Put(&self, io: Io, x: &i32 into self) {
    unsafe "probe: p is GM's, set by Global" {
        m := &*self.p
        var g := m.Lock(io) else {
            return
        }
        g.Store(some(x))
        g.Unlock()
    }
}
`,
		}},
		// A private function or a private holder's method is not in v's API,
		// so main counts neither, warm or cold.
		hold("private-fn", "//A-BEGIN\nKeep(vv: &V, io: Io, x: &i32 into vv) {\n}\n//A-END\n", "ok"),
		hold("private-method", "//A-BEGIN\nH.Put(&self, io: Io, x: &i32 into self) {\n}\n//A-END\n", "ok"),
		hold("exported-fn", "//A-BEGIN\nexport Keep(vv: &V, io: Io, x: &i32 into vv) {\n}\n//A-END\n", refused),
		hold("impl-method", "//A-BEGIN\nimpl Sink for H {\n    Put(&self, io: Io, x: &i32 into self) {\n    }\n}\n//A-END\n", refused),
		// An exported function handing out a private storing function as a
		// fn value: its signature's fn type declares the store (fnStoresInto).
		hold("exported-fn-value", "//A-BEGIN\nKeep2(vv: &V, io: Io, x: &i32 into vv) {\n}\n\nexport GetKeep(): fn(vv: &V, io: Io, x: &i32 into vv) {\n    return Keep2\n}\n//A-END\n", refused),
		hold("signature", "//A-BEGIN\nexport Note(vv: &V, io: Io, x: &i32 into vv) {\n}\n//A-END\n//B-BEGIN\nexport Note(vv: &V, io: Io, x: &i32) {\n}\n//B-END\n", refused),
		// A const or global of a fn type that declares the store is in v's
		// API, so a foreign body counts it (fnStoresInto on a const/global).
		hold("exported-const-fn", "//A-BEGIN\nexport const KeepC: fn(vv: &V, io: Io, x: &i32 into vv) = fn(vv: &V, io: Io, x: &i32 into vv) {\n}\n//A-END\n", refused),
		hold("exported-global-fn", "//A-BEGIN\nexport var KeepG: fn(vv: &V, io: Io, x: &i32 into vv) = fn(vv: &V, io: Io, x: &i32 into vv) {\n}\n//A-END\n", refused),
		// An impl of v whose For has no type of v to list it (`for i32`, `for
		// ?V`, `for []V`) stores into a parameter holding V: only v's function
		// list (iface.ModuleFuncsKey) shows it added.
		hold("impl-prim", "export interface Sink2 {\n    Put(&self, vv: &V, io: Io, x: &i32 into vv)\n}\n\n//A-BEGIN\nimpl Sink2 for i32 {\n    Put(&self, vv: &V, io: Io, x: &i32 into vv) {\n    }\n}\n//A-END\n", refused),
		hold("impl-opt", "//A-BEGIN\nimpl Sink for ?V {\n    Put(&self, io: Io, x: &i32 into self) {\n    }\n}\n//A-END\n", refused),
		hold("impl-slice", "//A-BEGIN\nimpl Sink for []V {\n    Put(&self, io: Io, x: &i32 into self) {\n    }\n}\n//A-END\n", refused),
		// An impl retargeted from another instance of a generic view to the
		// body's, whose method names are as they were: the impl scan reads its
		// signature before the bind test.
		vkRetargetStore(),
		vkRetargetGet("retarget-get-viewmod", false),
		vkRetargetGet("retarget-get-samemod", true),
		// A private method of V's own, a store into self, a keep or a
		// hand-out, counts for a body of any module, but neither v's API
		// hash nor a type namespace read kept at module level shows it
		// added: only the view module's keeping methods
		// (iface.ModuleKeepsKey) do; the -third cases put the body in a
		// third module b.
		hold("priv-store", vkPrivStore, refused),
		hold("priv-handout", vkPrivHandout, refused),
		vkPrivKeep("priv-keep", false),
		// Empty always contains a literal. Adding a private constructor with
		// a writable source changes how another module must treat its result.
		{name: "free-constructor", file: "v/v.mk", wantWith: "main.mk:5:5:E0602", wantWithout: "ok", files: map[string]string{
			"pkg.mk": manifest(map[string]string{"name": `"vk"`}),
			"main.mk": `import v

Forward(d: &i32): v.V from d {
    _ = d
    return v.Empty()
}
`,
			"v/v.mk": `export view struct V {
    text: str
}

export Empty(): V {
    return V(text: "")
}

V.Note(mut self, x: &i32 into self) {
}

ReadOnly(m: &i32): OutOfMemory!?V from m {
    return none
}

//A-BEGIN
Make(m: &Mutex[?&i32]): OutOfMemory!?V from m {
    return none
}
//A-END
`,
		}},
		vkThird(hold("priv-store-third", vkPrivStore, refused), "b.mk:11:5:E0605"),
		vkThird(hold("priv-handout-third", vkPrivHandout, refused), "b.mk:11:5:E0605"),
		vkPrivKeep("priv-keep-third", true),
		// An exported const whose value, not its type, decides whether
		// it takes stores (constvalue.go): v's API hash changes
		// with the value, so main misses, and F's own function result is
		// not reused, since its reads cover the value: the ReadValue borrow
		// records, and check's ReadSignature of D, whose declaration hash
		// encodes the value (so the case passes with either cut alone).
		// Under the const value rule D's Mutex pointee is E1002 at D
		// itself (a const's data is read-only), so const-view, whose pointee is a view
		// whose method stores into self, is the case that reaches
		// borrow's constTakes and E0602.
		vkConstValue(),
		vkConstView(),
		// An interface of v turned into a struct whose field's fn type
		// declares a store into V, keeping its own method: the module scan
		// reads no field of the interface (namedFields), and v's namespace,
		// which spells each item's kind, shows the change.
		hold("iface-to-struct", "//A-BEGIN\nexport struct Size {\n    export f: fn(vv: &V, io: Io, x: &i32 into vv)\n}\n//A-END\n"+
			"//B-BEGIN\nexport interface Size {\n    Len(&self): int\n}\n//B-END\n\nexport Size.Twice(&self): int {\n    _ = self\n    return 2\n}\n", refused),
	}
}

const vkPrivStore = "//A-BEGIN\nV.Stow(mut self, x: &i32 into self) {\n    _ = self\n    _ = x\n}\n//A-END\n"

const vkPrivHandout = "//A-BEGIN\nV.Peek(&self): &Mutex[?&i32] from self {\n" +
	"    unsafe \"probe: p was set by Of from the reference V borrows\" {\n        return &*self.p\n    }\n}\n//A-END\n"

// vkThird moves c's body (main.mk's Stash) into a third module b, which
// main calls; want is the verdict with the toggle.
func vkThird(c vkToggle, want string) vkToggle {
	files := map[string]string{}
	for p, text := range c.files {
		files[p] = text
	}
	files["b/b.mk"] = strings.Replace(files["main.mk"], "\nStash(", "\nexport Stash(", 1)
	files["main.mk"] = "import b\n\nRun(io: Io) {\n    b.Stash(io)\n}\n"
	c.files, c.wantWith = files, want
	return c
}

// vkPrivKeep is the priv-keep case: V has no exported keep, an exported
// store into self, and, with the toggle, a private keep V.Mk behind the
// source-less MakeGM; without it MakeGM makes V from &GM itself, so V
// keeps nothing its signatures show.
func vkPrivKeep(name string, third bool) vkToggle {
	c := vkToggle{name: name, file: "v/v.mk", wantWith: "main.mk:7:5:E0605", wantWithout: "ok", files: map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"vk"`, "unsafe_modules": `[".", "v", "t"]`}),
		"main.mk": `import v
import t

Stash(io: Io) {
    var n: i32 = 5
    vv := v.MakeGM()
    t.Put(&vv, io, &n)
}
`,
		"t/t.mk": "import v\n\nexport Put(vv: &v.V, io: Io, x: &i32 into vv) {\n" + vkStore + "}\n",
		"v/v.mk": `import std.raw

export var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

export view struct V {
    p: *Mutex[?&i32]
}

export V.Ptr(&self): *Mutex[?&i32] {
    return self.p
}

export V.Stow(mut self, x: &i32 into self) {
    _ = self
    _ = x
}
//A-BEGIN

V.Mk(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

export MakeGM(): V {
    return V.Mk(&GM)
}
//A-END
//B-BEGIN

export MakeGM(): V {
    return V(p: raw.Of(&GM))
}
//B-END
`,
	}}
	if third {
		return vkThird(c, "b.mk:7:5:E0605")
	}
	return c
}

// vkConstValue is the const-value cache case: v's exported const D of a struct
// with `p: ?&Mutex[?&i32]`, none without the toggle and, with it, the
// Mutex its initializer allocated in EvalArena; F returns a copy of D
// under `from d`. Under the const value rule D's value pointing at that
// Mutex is E1002 at D (a const's data is read-only), warm and
// cold alike; without the toggle it is ok.
func vkConstValue() vkToggle {
	return vkToggle{name: "const-value", file: "v/v.mk", wantWith: "v.mk:14:14:E1002", wantWithout: "ok", files: map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"vk"`}),
		"main.mk": `import v

F(d: []u8): v.L from d {
    var l := v.D
    l.d = d
    return l
}
`,
		"v/v.mk": `export struct L {
    export d: []u8
    export p: ?&Mutex[?&i32]
}

MakeM(a: &Arena): &Mutex[?&i32] from a {
    m := a.Put(Mutex[?&i32].New(none)) else {
        return MakeM(a)
    }
    return &*m
}

//A-BEGIN
export const D: L = L(d: [], p: some(MakeM(EvalArena())))
//A-END
//B-BEGIN
export const D: L = L(d: [], p: none)
//B-END
`,
	}}
}

// vkConstView is const-value's case with D's pointee a view W[i32], whose
// method Keep declares a store into self, in place of the Mutex: the pointee
// holds no Cell, interior-mutable item or raw pointer, so the const value
// rule passes D (a view is not in it), and borrow's constTakes counts D as
// taking stores only when its value points at W (inlineTakes, viewKeeps), so
// F is E0602 only then (the soundness mutant M-D756-constvalue has the
// witnesses G756-const-view*).
func vkConstView() vkToggle {
	return vkToggle{name: "const-view", file: "v/v.mk", wantWith: "main.mk:6:5:E0602", wantWithout: "ok", files: map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"vk"`}),
		"main.mk": `import v

F(d: []u8): v.L from d {
    var l := v.D
    l.d = d
    return l
}
`,
		"v/v.mk": `export view struct W[T] {
    q: ?&T
}

export W[T].Keep(&self, x: &T into self) {
    _ = self
    _ = x
}

export struct L {
    export d: []u8
    export p: ?&W[i32]
}

MakeW(a: &Arena): &W[i32] from a {
    w := a.Put(W[i32](q: none)) else {
        return MakeW(a)
    }
    return &*w
}

//A-BEGIN
export const D: L = L(d: [], p: some(MakeW(EvalArena())))
//A-END
//B-BEGIN
export const D: L = L(d: [], p: none)
//B-END
`,
	}}
}

// vkGView is a generic view G[T] over &GM, with T's referent beside it,
// and its raw accessor.
const vkGView = `import std.raw

export var GM: Mutex[?&i32] = Mutex[?&i32].New(none)
export var GI: i32 = 7

export view struct G[T] {
    p: *Mutex[?&i32]
    q: *T
}

export G[T].Ptr(&self): *Mutex[?&i32] {
    return self.p
}
`

// vkRetargetStore is the retarget-store-xmod case: an impl in the
// interface's module i, whose method stores into self, retargeted from
// &G[i64] to &G[i32]; main stores a local through t.Put, which the view scan
// does not count.
func vkRetargetStore() vkToggle {
	impl := func(arg string) string {
		return "impl Sink for &v.G[" + arg + "] {\n    Put(self, io: Io, x: &i32 into self) {\n    }\n}\n"
	}
	return vkToggle{name: "retarget-store-xmod", file: "i/i.mk", wantWith: "main.mk:12:5:E0605", wantWithout: "ok", files: map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"vk"`, "unsafe_modules": `[".", "v", "i", "t"]`}),
		"main.mk": `import v
import i
import t

MakeG(): v.G[i32] {
    return v.G[i32].Of(&v.GM, &v.GI)
}

Stash(io: Io) {
    var n: i32 = 5
    gg := MakeG()
    t.Put(&gg, io, &n)
    _ = i.Nop()
}
`,
		"t/t.mk": "import v\n\nexport Put(vv: &v.G[i32], io: Io, x: &i32 into vv) {\n" + vkStore + "}\n",
		"i/i.mk": "import v\n\nexport interface Sink {\n    Put(self, io: Io, x: &i32 into self)\n}\n\nexport Nop(): int {\n    return 0\n}\n\n" +
			"//A-BEGIN\n" + impl("i32") + "//A-END\n//B-BEGIN\n" + impl("i64") + "//B-END\n",
		"v/v.mk": vkGView + `
export G[T].Of(m: &Mutex[?&i32], t: &T): G[T] from m, t {
    return G[T](p: raw.Of(m), q: raw.Of(t))
}
`,
	}}
}

// vkRetargetGet is the retarget-get cases: an impl that hands out &GM from
// self, retargeted from G[i64] to G[i32], in the view's module, which main
// imports (viewmod) or is (samemod); G's own Put stores into self.
func vkRetargetGet(name string, same bool) vkToggle {
	impl := func(arg string) string {
		return "impl Src[&Mutex[?&i32]] for G[" + arg + "] {\n    Get(&self): &Mutex[?&i32] from self.p {\n" +
			"        unsafe \"probe: p was set by MakeG from GM\" {\n            return &*self.p\n        }\n    }\n}\n"
	}
	view := vkGView + `
export G[T].Put(&self, x: &i32 into self) {
    _ = self
    _ = x
}

export MakeG(): G[i32] {
    return G[i32](p: raw.Of(&GM), q: raw.Of(&GI))
}

export interface Src[R] {
    Get(&self): R
}
` + "\n//A-BEGIN\n" + impl("i32") + "//A-END\n//B-BEGIN\n" + impl("i64") + "//B-END\n"
	work := "Work(): i32 {\n    var n: i32 = 5\n    gg := MakeG()\n    gg.Put(&n)\n    return n\n}\n"
	if same {
		return vkToggle{name: name, file: "main.mk", wantWith: "main.mk:41:5:E0605", wantWithout: "ok", files: map[string]string{
			"pkg.mk":  manifest(map[string]string{"name": `"vk"`, "unsafe_modules": `["."]`}),
			"main.mk": view + "\n" + work,
		}}
	}
	return vkToggle{name: name, file: "v/v.mk", wantWith: "main.mk:6:5:E0605", wantWithout: "ok", files: map[string]string{
		"pkg.mk":  manifest(map[string]string{"name": `"vk"`, "unsafe_modules": `[".", "v"]`}),
		"main.mk": "import v\n\n" + strings.Replace(work, "MakeG()", "v.MakeG()", 1),
		"v/v.mk":  view,
	}}
}

// TestViewKeepsColdWarm: each case's check gives its verdict cold and with
// one warm cache across the edits without, with, without, with.
func TestViewKeepsColdWarm(t *testing.T) {
	t.Parallel()
	for _, c := range vkCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := writeTree(t, c.files)
			warm := t.TempDir()
			path := filepath.Join(dir, filepath.FromSlash(c.file))
			text := c.files[c.file]
			for i, with := range []bool{false, true, false, true} {
				if err := os.WriteFile(path, []byte(vkVersion(text, with)), 0o644); err != nil {
					t.Fatal(err)
				}
				want := c.wantWithout
				if with {
					want = c.wantWith
				}
				runs := []string{warm}
				if i < 2 {
					runs = append(runs, t.TempDir()) // cold
				}
				for j, cache := range runs {
					got := strings.Join(baseLines(check(t, Config{Root: dir, CacheDir: cache})), " ")
					if got == "" {
						got = "ok"
					}
					if got != want {
						t.Errorf("step %d (with %v, cold %v): %q, want %q", i+1, with, j == 1, got, want)
					}
				}
			}
		})
	}
}

// TestViewKeepsReused: the reads the view scan adds hold while nothing they
// cover changes, so a body whose verdict the scan decided is reused, not
// rechecked, when another function of its module changes; a read the world
// could not answer would leave it never stored (funccache.go filled), warm
// equal to cold by never hitting. Its replayed verdict is the cold one. One
// case reads v's function list from another module (impl-prim's "with"),
// one its own module's (an impl for i32 in main), and two the signature of
// an impl the impl scan considers, binding or not (retarget-store-xmod's
// two forms).
func TestViewKeepsReused(t *testing.T) {
	t.Parallel()
	const other = "\nOther(): int {\n    return 2\n}\n"
	foreign := vkHoldFiles("export interface Sink2 {\n    Put(&self, vv: &V, io: Io, x: &i32 into vv)\n}\n\n" +
		"impl Sink2 for i32 {\n    Put(&self, vv: &V, io: Io, x: &i32 into vv) {\n    }\n}\n")
	foreign["main.mk"] += other
	same := map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"vk"`, "unsafe_modules": `[".", "t"]`}),
		"t/t.mk": "export PutAny[T](p: &T, io: Io, x: &i32 into p) {\n}\n",
		"main.mk": `import std.raw
import t

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

view struct V {
    p: *Mutex[?&i32]
}

V.Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

interface Sink2 {
    Put(&self, vv: &V, io: Io, x: &i32 into vv)
}

impl Sink2 for i32 {
    Put(&self, vv: &V, io: Io, x: &i32 into vv) {
    }
}

MakeV(): V {
    return V.Of(&GM)
}

Stash(io: Io) {
    var n: i32 = 5
    vv := MakeV()
    t.PutAny(&vv, io, &n)
}
` + other,
	}
	// The impl scan's signature reads: retarget-store-xmod's impl in i, for
	// &v.G[i64] (not binding, ok) and for &v.G[i32] (binding, E0605).
	retarget := func(with bool) map[string]string {
		c := vkRetargetStore()
		files := map[string]string{}
		for p, text := range c.files {
			files[p] = text
		}
		files[c.file] = vkVersion(files[c.file], with)
		files["main.mk"] += other
		return files
	}
	// The module scan's fn-type walk through the `&self` of an interface's
	// own method (`Size.Twice`, as std.core's `Slice[T].Len`), an interface
	// value that shows no fields, records no read that has no answer, so the
	// body is stored (without it, std modules such as std.fs, std.net and
	// std.http were never stored).
	ifaceParam := vkHoldFiles("export interface Size {\n    Len(&self): int\n}\n\nexport Size.Twice(&self): int {\n    _ = self\n    return 2\n}\n")
	ifaceParam["main.mk"] += other
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"iface-param", ifaceParam, ""},
		{"foreign", foreign, "main.mk:11:5:E0605"},
		{"same-module", same, "main.mk:30:5:E0605"},
		{"impl-not-binding", retarget(false), ""},
		{"impl-binding", retarget(true), "main.mk:12:5:E0605"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := writeTree(t, c.files)
			cfg := Config{Root: dir, CacheDir: t.TempDir()}
			if got := strings.Join(baseLines(check(t, cfg)), " "); got != c.want {
				t.Fatalf("cold: %q, want %q", got, c.want)
			}
			main := filepath.Join(dir, "main.mk")
			if err := os.WriteFile(main, []byte(strings.Replace(c.files["main.mk"], "return 2", "return 3", 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			warm := check(t, cfg)
			if got := strings.Join(baseLines(warm), " "); got != c.want {
				t.Errorf("warm: %q, want %q", got, c.want)
			}
			got := pkgItems(warm.Trace.ItemsChecked)
			t.Logf("warm rechecked %q", got)
			if slices.Contains(got, "main.Stash") || !slices.Contains(got, "main.Other") {
				t.Errorf("warm rechecked %q: want main.Other and not main.Stash", got)
			}
		})
	}
}
