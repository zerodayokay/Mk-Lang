// Why: an Arena over an &Alloc is sound only if an Arena is not Send, a Sim
// holds its arena's loan, and proc.Run's readers use arenas of their own.
// What: TestArenaNotSend, TestSimHoldsArenaLoan,
// TestSimIsAView and their controls.
// How: each program is rejected with the one diagnostic named, and each
// control checks clean; on the older std every rejected program checked.

package std

import (
	"strings"
	"testing"
)

type dfPack05Case struct {
	name, body, want string
}

func dfPack05Check(t *testing.T, cases []dfPack05Case) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, status := dfRun(t, map[string]string{"main.mk": c.body}, "check", "main.mk")
			if c.want == "" {
				if status != 0 {
					t.Fatalf("mk check: exit %d\n%s", status, out)
				}
				return
			}
			if status == 0 || !strings.Contains(out, c.want) || strings.Count(out, ": E") != 1 {
				t.Fatalf("mk check: exit %d, want the one diagnostic %s\n%s", status, c.want, out)
			}
		})
	}
}

// TestArenaNotSend: an Arena crosses to a task neither by value,
// nor by mut, nor inside a struct; a task takes &Heap and makes its own.
func TestArenaNotSend(t *testing.T) {
	dfPack05Check(t, []dfPack05Case{
		{"an arena by value", `Work(a: Arena) {
    var b := a
    _ = b.Dup("in the task") else {
    }
    b.Free()
}

export Main(sys: &System) {
    var arena := Arena.New(sys.heap) else {
        return
    }
    scope s := sys.io.Scope() {
        s.go Work(arena)
    }
}
`, "main.mk:13:19: E1001"},
		{"an arena by mut", `Fill(a: mut Arena) {
    _ = a.Dup("in the task") else {
    }
}

export Main(sys: &System) {
    var arena := Arena.New(sys.heap) else {
        return
    }
    scope s := sys.io.Scope() {
        s.go Fill(mut arena)
    }
    arena.Free()
}
`, "main.mk:11:19: E1001"},
		{"an arena in a struct", `linear struct Job {
    arena: Arena
    n: int
}

Run(j: Job) {
    Job(arena, n: _) := j
    arena.Free()
}

export Main(sys: &System) {
    arena := Arena.New(sys.heap) else {
        return
    }
    scope s := sys.io.Scope() {
        s.go Run(Job(arena: arena, n: 1))
    }
}
`, "main.mk:16:18: E1001"},
		{"control: the task makes its own arena", `Work(h: &Heap, out: mut int) {
    var a := Arena.New(h) else {
        return
    }
    t := a.Dup("made in the task") else {
        a.Free()
        return
    }
    *out = t.Len()
    a.Free()
}

export Main(sys: &System): WriteError!void {
    var n := 0
    scope s := sys.io.Scope() {
        s.go Work(sys.heap, mut n)
    }
    try sys.Print("{n}\n", (n))
}
`, ""},
	})
}

// TestSimHoldsArenaLoan: Sim.New(a, seed) returns a Sim that
// holds a's loan, so a is not freed, reset or moved while the Sim
// lives, and a Sim does not leave the frame that owns its arena, directly,
// through a helper or inside a struct. The controls keep the shapes legal
// when the Sim ends first, or when its arena is the caller's.
func TestSimHoldsArenaLoan(t *testing.T) {
	dfPack05Check(t, []dfPack05Case{
		{"the arena freed while the Sim lives", `import std.sim

export Main(sys: &System) {
    var arena := Arena.New(sys.heap) else {
        return
    }
    var s := sim.Sim.New(&arena, 7) else {
        arena.Free()
        return
    }
    arena.Free()
    _ = s.Arena().Dup("after the free") else {
    }
    s.Free()
}
`, "main.mk:11:5: E0503"},
		{"the arena reset while the Sim lives", `import std.sim

export Main(sys: &System) {
    var arena := Arena.New(sys.heap) else {
        return
    }
    var s := sim.Sim.New(&arena, 7) else {
        arena.Free()
        return
    }
    arena.Reset()
    s.Free()
    arena.Free()
}
`, "main.mk:11:5: E0501"},
		{"the arena moved while the Sim lives", `import std.sim

export Main(sys: &System) {
    var arena := Arena.New(sys.heap) else {
        return
    }
    var s := sim.Sim.New(&arena, 7) else {
        arena.Free()
        return
    }
    var moved := arena
    s.Free()
    moved.Free()
}
`, "main.mk:11:5: E0503"},
		{"a struct holding the Sim outlives the arena", `import std.sim

linear struct World {
    s: sim.Sim
}

export Main(sys: &System) {
    var arena := Arena.New(sys.heap) else {
        return
    }
    s := sim.Sim.New(&arena, 7) else {
        arena.Free()
        return
    }
    w := World(s: s)
    arena.Free()
    World(s: t) := w
    t.Free()
}
`, "main.mk:16:5: E0503"},
		{"control: the Sim ends before its arena, through a helper", `import std.sim

Over(a: &Arena): OutOfMemory!sim.Sim {
    return try sim.Sim.New(a, 7)
}

export Main(sys: &System) {
    var arena := Arena.New(sys.heap) else {
        return
    }
    var s := Over(&arena) else {
        arena.Free()
        return
    }
    t := s.Arena().Dup("in the world") else {
        s.Free()
        arena.Free()
        return
    }
    sys.Print("{t}\n", (t)).Must()
    s.Free()
    arena.Reset()
    arena.Free()
}
`, ""},
		{"control: a struct holding a Sim over the caller's arena", `import std.sim

linear struct World {
    s: sim.Sim
}

Hold(a: &Arena): OutOfMemory!World {
    s := try sim.Sim.New(a, 7)
    return World(s: s)
}

export Main(sys: &System) {
    w := Hold(sys.arena) else {
        return
    }
    World(s) := w
    s.Free()
}
`, ""},
	})
}

// TestSimIsAView: the three ways a Sim that is a view changes
// user code that checked clean before (a view "contains everything").
// A Sim beside another reference parameter could supply the result, so the
// signature names its source with `from` (E0601); a result whose only
// reference parameter is a Sim holds the Sim's loan (E0503); an exported
// type with a private Sim field is declared mut view (E0410). The controls are
// the fixed forms.
func TestSimIsAView(t *testing.T) {
	dfPack05Check(t, []dfPack05Case{
		{"a Sim beside an arena with no from", `import std.sim

Label(s: &sim.Sim, a: &Arena): OutOfMemory!str {
    return try a.Dup("a label")
}

export Main(sys: &System) {
    var s := sim.Sim.New(sys.arena, 1).Must()
    t := Label(&s, sys.arena).Must()
    s.Free()
    sys.Print("{t}\n", (t)).Must()
}
`, "main.mk:3:1: E0601"},
		{"control: from a, by & and by mut", `import std.sim

Label(s: &sim.Sim, a: &Arena): OutOfMemory!str from a {
    return try a.Dup("a label")
}

Tick(s: mut sim.Sim, a: &Arena, n: int): OutOfMemory!mut []u8 from a {
    return try a.Slice[u8](n)
}

export Main(sys: &System) {
    var s := sim.Sim.New(sys.arena, 1).Must()
    t := Label(&s, sys.arena).Must()
    b := Tick(mut s, sys.arena, 4).Must()
    s.Free()
    sys.Print("{t} {n}\n", (t, n: b.Len())).Must()
}
`, ""},
		{"a result whose only reference parameter is a Sim", `import std.sim

Name(s: &sim.Sim): str {
    return "world"
}

export Main(sys: &System) {
    var s := sim.Sim.New(sys.arena, 1).Must()
    n := Name(&s)
    s.Free()
    sys.Print("hello {n}\n", (n)).Must()
}
`, "main.mk:10:5: E0503"},
		{"an exported struct with a private Sim field, not view", `import std.sim

export linear struct Harness {
    s: sim.Sim
    n: int
}

export Harness.New(a: &Arena, seed: u64): OutOfMemory!Harness from a {
    s := try sim.Sim.New(a, seed)
    return Harness(s: s, n: 0)
}

export Harness.Free(self) {
    Harness(s, n: _) := self
    s.Free()
}

export Main(sys: &System) {
    h := Harness.New(sys.arena, 1).Must()
    h.Free()
}
`, "main.mk:3:22: E0410"},
		{"a shared view cannot wrap the mutable Sim view", `import std.sim

export linear view struct Harness {
    s: sim.Sim
}
`, "main.mk:3:27: E0410"},
		{"control: the struct declared mut view", `import std.sim

export linear mut view struct Harness {
    s: sim.Sim
    n: int
}

export Harness.New(a: &Arena, seed: u64): OutOfMemory!Harness from a {
    s := try sim.Sim.New(a, seed)
    return Harness(s: s, n: 0)
}

export Harness.Free(self) {
    Harness(s, n: _) := self
    s.Free()
}

export Main(sys: &System) {
    h := Harness.New(sys.arena, 1).Must()
    h.Free()
}
`, ""},
	})
}

// TestSimLeavesNoFrame: a Sim returned past the frame that owns
// its arena is E0603, made there directly or through a helper, or wrapped
// in a struct. Each program also frees the arena under a defer while the
// result holds its loan, which is the E0503 reported with it; both lines
// are wanted.
func TestSimLeavesNoFrame(t *testing.T) {
	for _, c := range []struct {
		name, body string
	}{
		{"directly", `import std.sim

Make(sys: &System): OutOfMemory!sim.Sim {
    var arena := try Arena.New(sys.heap)
    s := sim.Sim.New(&arena, 7) else e {
        arena.Free()
        fail e
    }
    defer arena.Free()
    return s
}

export Main(sys: &System) {
    s := Make(sys) else {
        return
    }
    s.Free()
}
`},
		{"through a helper", `import std.sim

Over(a: &Arena): OutOfMemory!sim.Sim {
    return try sim.Sim.New(a, 7)
}

Make(sys: &System): OutOfMemory!sim.Sim {
    var arena := try Arena.New(sys.heap)
    s := Over(&arena) else e {
        arena.Free()
        fail e
    }
    defer arena.Free()
    return s
}

export Main(sys: &System) {
    s := Make(sys) else {
        return
    }
    s.Free()
}
`},
		{"in a struct", `import std.sim

linear struct World {
    s: sim.Sim
}

Make(sys: &System): OutOfMemory!World {
    var arena := try Arena.New(sys.heap)
    s := sim.Sim.New(&arena, 7) else e {
        arena.Free()
        fail e
    }
    defer arena.Free()
    return World(s: s)
}

export Main(sys: &System) {
    w := Make(sys) else {
        return
    }
    World(s) := w
    s.Free()
}
`},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, status := dfRun(t, map[string]string{"main.mk": c.body}, "check", "main.mk")
			if status == 0 || !strings.Contains(out, ": E0603 ") || !strings.Contains(out, ": E0503 ") || strings.Count(out, ": E") != 2 {
				t.Fatalf("mk check: exit %d, want E0603 at the return and E0503 at the deferred Free\n%s", status, out)
			}
		})
	}
}
