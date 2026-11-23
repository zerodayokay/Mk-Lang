// Why: borrow and check read MutView and LoanInterior from foreign module
// shapes (ReadShape), and an incremental build is only sound when toggling
// those facts re-checks every affected caller while reusing untouched bodies.
//
// What: mvCases, TestMutViewColdWarm and TestMutViewCacheReused.
//
// How: each case toggles a foreign type between read-only (`view struct`,
// non-carrier `Cell[i32]`/`Mutex[i32]`) and loan-interior-writable (`mut view
// struct`, carrier `Cell[?&i32]`/`Mutex[?&i32]`), checks cold and with one
// warm cache across without -> with -> without -> with, and asserts identical
// verdicts and warm reuse on unrelated edits.

package driver

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type mvToggle struct {
	name                  string
	files                 map[string]string
	file                  string
	wantWith, wantWithout string
}

var mvBlock = map[string]*regexp.Regexp{
	"A": regexp.MustCompile(`(?s)//A-BEGIN\n.*?//A-END\n`),
	"B": regexp.MustCompile(`(?s)//B-BEGIN\n.*?//B-END\n`),
}

func mvVersion(text string, with bool) string {
	if with {
		return mvBlock["B"].ReplaceAllString(text, "")
	}
	return mvBlock["A"].ReplaceAllString(text, "")
}

func mvCases() []mvToggle {
	return []mvToggle{
		// Toggling a foreign view between `view struct` and `mut view struct`
		// flips R16 global-store tracking on a source-less constructor.
		{
			name:        "mut-view-global",
			file:        "v/v.mk",
			wantWith:    "main.mk:11:5:E0605",
			wantWithout: "ok",
			files: map[string]string{
				"pkg.mk": manifest(map[string]string{"name": `"mv"`, "unsafe_modules": `[".", "v"]`}),
				"main.mk": `import v

MakeV(): v.V {
    return v.V.Of(&v.GM)
}

Stash(io: Io) {
    var n: i32 = 5
    vv := MakeV()
    _ = io
    v.Put(&vv, &n)
}
`,
				"v/v.mk": `import std.raw

export var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

//A-BEGIN
export mut view struct V {
    p: *Mutex[?&i32]
}
//A-END
//B-BEGIN
export view struct V {
    p: *Mutex[?&i32]
}
//B-END

export V.Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

export Put(vv: &V, x: &i32 into vv) {
    _ = vv
    _ = x
}
`,
			},
		},
		// Toggling a foreign view between `view struct` and `mut view struct`
		// flips split collapse across `&p`.
		{
			name:        "mut-view-split",
			file:        "v/v.mk",
			wantWith:    "main.mk:7:5:E0502",
			wantWithout: "ok",
			files: map[string]string{
				"pkg.mk": manifest(map[string]string{"name": `"mv"`}),
				"main.mk": `import v

Check(t: &i32): i32 {
    var n: i32 = 7
    p := v.Pair(a: t, b: &n)
    v.Touch(&p)
    n = 42
    return *p.a
}
`,
				"v/v.mk": `//A-BEGIN
export mut view struct Pair {
    export a: &i32
    export b: &i32
}
//A-END
//B-BEGIN
export view struct Pair {
    export a: &i32
    export b: &i32
}
//B-END

export Touch(p: &Pair) {
    _ = p
}
`,
			},
		},
		// Toggling a foreign struct field between Cell[i32] (non-carrier) and
		// Cell[?&i32] (carrier) flips LoanInterior and split collapse.
		{
			name:        "cell-carrier-toggle",
			file:        "v/v.mk",
			wantWith:    "main.mk:7:5:E0502",
			wantWithout: "ok",
			files: map[string]string{
				"pkg.mk": manifest(map[string]string{"name": `"mv"`}),
				"main.mk": `import v

Check(t: &i32): i32 {
    var n: i32 = 7
    p := v.Pair(slot: v.Slot.New(), a: t, b: &n)
    v.Touch(&p)
    n = 42
    return *p.a
}
`,
				"v/v.mk": `//A-BEGIN
export struct Slot {
    export c: Cell[?&i32]
}

export Slot.New(): Slot {
    return Slot(c: Cell[?&i32].New(none))
}
//A-END
//B-BEGIN
export struct Slot {
    export c: Cell[i32]
}

export Slot.New(): Slot {
    return Slot(c: Cell[i32].New(0))
}
//B-END

export struct Pair {
    export slot: Slot
    export a: &i32
    export b: &i32
}

export Touch(p: &Pair) {
    _ = p
}
`,
			},
		},
		// Toggling a foreign global between Mutex[i32] and Mutex[?&i32] flips
		// whether returning &G under a source is E0602.
		{
			name:        "mutex-carrier-global",
			file:        "v/v.mk",
			wantWith:    "main.mk:5:5:E0602",
			wantWithout: "ok",
			files: map[string]string{
				"pkg.mk": manifest(map[string]string{"name": `"mv"`}),
				"main.mk": `import v

Pick(d: &v.Wrap): &v.Wrap from d {
    _ = d
    return &v.G
}
`,
				"v/v.mk": `//A-BEGIN
export struct Wrap {
    export m: Mutex[?&i32]
}

export var G: Wrap = Wrap(m: Mutex[?&i32].New(none))
//A-END
//B-BEGIN
export struct Wrap {
    export m: Mutex[i32]
}

export var G: Wrap = Wrap(m: Mutex[i32].New(0))
//B-END
`,
			},
		},
		// Toggling a foreign const's view pointee between `view struct` and
		// `mut view struct` flips constValueTakes E0605.
		{
			name:        "const-mut-view",
			file:        "k/k.mk",
			wantWith:    "main.mk:6:5:E0605",
			wantWithout: "ok",
			files: map[string]string{
				"pkg.mk": manifest(map[string]string{"name": `"mv"`}),
				"main.mk": `import k

Stash(): int {
    var n: i32 = 5
    b := k.GB
    b.w.Keep(&n)
    return 0
}
`,
				"k/k.mk": `//A-BEGIN
export mut view struct W[T] {
    q: ?&T
}
//A-END
//B-BEGIN
export view struct W[T] {
    q: ?&T
}
//B-END

export W[T].Keep(&self, x: &T into self) {
    _ = self
    _ = x
}

MakeW(a: &Arena): &W[i32] from a {
    w := a.Put(W[i32](q: none)) else {
        return MakeW(a)
    }
    return &*w
}

export struct Box {
    export w: &W[i32]
}

export const GB: Box = Box(w: MakeW(EvalArena()))
`,
			},
		},
	}
}

// TestMutViewColdWarm verifies cold and warm checks agree across toggles of
// MutView and LoanInterior in foreign modules.
func TestMutViewColdWarm(t *testing.T) {
	t.Parallel()
	for _, c := range mvCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := writeTree(t, c.files)
			warm := t.TempDir()
			path := filepath.Join(dir, filepath.FromSlash(c.file))
			text := c.files[c.file]
			for i, with := range []bool{false, true, false, true} {
				if err := os.WriteFile(path, []byte(mvVersion(text, with)), 0o644); err != nil {
					t.Fatal(err)
				}
				want := c.wantWithout
				if with {
					want = c.wantWith
				}
				runs := []string{warm}
				if i < 2 {
					runs = append(runs, t.TempDir())
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

// TestMutViewCacheReused verifies that a body reading a foreign mut view's
// shape is reused from cache when an unrelated function in its module changes.
func TestMutViewCacheReused(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"mv"`, "unsafe_modules": `[".", "v"]`}),
		"main.mk": `import v

MakeV(): v.V {
    return v.V.Of(&v.GM)
}

Stash() {
    var n: i32 = 5
    vv := MakeV()
    v.Put(&vv, &n)
}

Other(): int {
    return 2
}
`,
		"v/v.mk": `import std.raw

export var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

export mut view struct V {
    p: *Mutex[?&i32]
}

export V.Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

export Put(vv: &V, x: &i32 into vv) {
    _ = vv
    _ = x
}
`,
	}
	dir := writeTree(t, files)
	cfg := Config{Root: dir, CacheDir: t.TempDir()}
	const want = "main.mk:10:5:E0605"
	if got := strings.Join(baseLines(check(t, cfg)), " "); got != want {
		t.Fatalf("cold: %q, want %q", got, want)
	}
	mainPath := filepath.Join(dir, "main.mk")
	if err := os.WriteFile(mainPath, []byte(strings.Replace(files["main.mk"], "return 2", "return 3", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	warm := check(t, cfg)
	if got := strings.Join(baseLines(warm), " "); got != want {
		t.Errorf("warm: %q, want %q", got, want)
	}
	got := pkgItems(warm.Trace.ItemsChecked)
	if slices.Contains(got, "main.Stash") || !slices.Contains(got, "main.Other") {
		t.Errorf("warm rechecked %q: want main.Other and not main.Stash", got)
	}
}
