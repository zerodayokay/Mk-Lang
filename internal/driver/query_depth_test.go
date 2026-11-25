// Why: `mk query stack` bounds a recursion only when a depth parameter falls
// around each of its cycles; a wrong rule either rejects std.big's
// divide and conquer or bounds a recursion that has no bound.
//
// What: TestQuery_StackDepthBounded over small recursions, and
// TestQuery_StackStdBig over every std.big operation.
//
// How: each runs `stack` in process (queryOne) and reads the answer's lines.

package driver

import (
	"strings"
	"testing"
)

const depthSource = `Fib(n: int, depth: u8): int {
    if n < 2 || depth == 0 {
        return n
    }
    return Fib(n - 1, depth - 1) + Fib(n - 2, depth - 1)
}

Even(n: int, depth: u16): bool {
    if n == 0 {
        return true
    }
    return Odd(n - 1, depth)
}

Odd(n: int, depth: u16): bool {
    if n == 0 {
        return false
    }
    d := depth - 2
    return Even(n - 1, d)
}

Same(n: int, depth: u8): int {
    if n == 0 {
        return 0
    }
    return Same(n - 1, depth)
}

Wide(n: int, depth: u64): int {
    if n == 0 {
        return 0
    }
    return Wide(n - 1, depth - 1)
}

Wraps(n: int, depth: u8): int {
    if n == 0 {
        return 0
    }
    return Wraps(n - 1, depth -% 1)
}

Grows(n: int, depth: u8): int {
    if n == 0 {
        return 0
    }
    return Grows(n - 1, depth + 1)
}

Reset(n: int, var depth: u8): int {
    if n == 0 {
        return 0
    }
    depth = 9
    return Reset(n - 1, depth - 1)
}

Ping(n: int, depth: u8): int {
    if n == 0 {
        return 0
    }
    return Pong(n - 1, depth)
}

Pong(n: int, depth: u8): int {
    return Ping(n, depth)
}

Bump(d: mut u8) {
    *d = 200
}

LocalBumped(n: int, depth: u8): int {
    if n == 0 {
        return 0
    }
    var d := depth - 1
    Bump(mut d)
    return LocalBumped(n - 1, d)
}

ParamBumped(n: int, var depth: u8): int {
    if n == 0 {
        return 0
    }
    Bump(mut depth)
    return ParamBumped(n - 1, depth - 1)
}

Mixed(n: int, depth: u8): int {
    if n == 0 {
        return 0
    }
    return Mixed(n - 1, depth - 1) + Keep(n - 1, depth)
}

Keep(n: int, depth: u8): int {
    return Mixed(n, depth)
}

Fresh(n: int, depth: u8): int {
    if n == 0 {
        return 0
    }
    return Fresh(n - 1, 7)
}

struct Walker {
    step: int
}

Walker.Walk(&self, n: int, depth: u8): int {
    if n <= 0 || depth == 0 {
        return 0
    }
    return self.Walk(n - self.step, depth - 1) + 1
}

Loop(n: int, depth: u8): int {
    if n == 0 || depth == 0 {
        return 0
    }
    become Loop(n - 1, depth - 1)
}

export A(): int {
    return Fib(10, 40)
}

export I(): int {
    return LocalBumped(3, 4) + ParamBumped(3, 4) + Mixed(3, 4) + Fresh(3, 4)
}

export J(): int {
    w := Walker(step: 1)
    return w.Walk(3, 4) + Loop(3, 4)
}

export B(): bool {
    return Even(10, 40)
}

export C(): int {
    return Same(3, 4) + Fib(3, 4)
}

export D(): int {
    return Wide(3, 4)
}

export E(): int {
    return Wraps(3, 4)
}

export F(): int {
    return Grows(3, 4)
}

export G(): int {
    return Reset(3, 4)
}

export H(): int {
    return Ping(3, 4)
}
`

// TestQuery_StackDepthBounded: a recursion whose u8 or u16 depth falls
// around every cycle (through a local, across two functions) is bounded at
// (max+1) frames per function, through a receiver and a become as well; a
// depth kept, wrapped, raised, reassigned, changed through a mut borrow,
// replaced by a constant, of a wide type, or kept around some cycle of the
// recursion is not.
func TestQuery_StackDepthBounded(t *testing.T) {
	dir := testPkg(t, map[string]string{"q.mk": depthSource})
	answerHas(t, queryOne(t, dir, "stack", "A"), "bounded: no indirect call in the 2 function(s) A reaches",
		"deepest direct chain: 257 frame(s): A -> Fib (depth-bounded, 256 frames)",
		"depth-bounded recursion Fib -> Fib: depth (u8) falls around every cycle, at most 256 frame(s)")
	answerHas(t, queryOne(t, dir, "stack", "J"), "bounded: no indirect call in the 3 function(s) J reaches",
		"deepest direct chain: 257 frame(s)", "depth-bounded recursion Walker.Walk -> Walker.Walk", "depth-bounded recursion Loop -> Loop")
	answerHas(t, queryOne(t, dir, "stack", "B"), "deepest direct chain: 131073 frame(s)", "(u16) falls around every cycle, at most 131072 frame(s)")
	for _, f := range []struct{ fn, rec string }{
		{"C", "Same -> Same, reached by C -> Same"}, {"D", "Wide -> Wide"}, {"E", "Wraps -> Wraps"},
		{"F", "Grows -> Grows"}, {"G", "Reset -> Reset"}, {"H", "Ping -> Pong -> Ping"},
		{"LocalBumped", "LocalBumped -> LocalBumped"}, {"ParamBumped", "ParamBumped -> ParamBumped"},
		{"Mixed", "Mixed -> Mixed"}, {"Fresh", "Fresh -> Fresh"},
	} {
		answerHas(t, queryOne(t, dir, "stack", f.fn), "no static bound: recursion "+f.rec)
	}
}

const bigOpsSource = `import std.big

export OpAdd(a: &Arena, x: &big.Int, y: &big.Int): OutOfMemory!int {
    return (try x.Add(a, y)).BitLen()
}

export OpSub(a: &Arena, x: &big.Int, y: &big.Int): OutOfMemory!int {
    return (try x.Sub(a, y)).BitLen()
}

export OpMul(a: &Arena, x: &big.Int, y: &big.Int): OutOfMemory!int {
    return (try x.Mul(a, y)).BitLen()
}

export OpDivMod(a: &Arena, x: &big.Int, y: &big.Int): OutOfMemory!int {
    (q, r) := try x.DivMod(a, y)
    return q.Cmp(&r)
}

export OpMod(a: &Arena, x: &big.Int, y: &big.Int): OutOfMemory!int {
    return (try x.Mod(a, y)).BitLen()
}

export OpText(a: &Arena, x: &big.Int): OutOfMemory!int {
    return (try x.Text(a, 10)).Len()
}

export OpParse(a: &Arena, s: str): int {
    x := big.Parse(a, s, 10, 1000000) else { return 0 }
    return x.BitLen()
}

export OpGcd(a: &Arena, x: &big.Int, y: &big.Int): OutOfMemory!int {
    return (try x.Gcd(a, y)).BitLen()
}

export OpModInverse(a: &Arena, x: &big.Int, y: &big.Int): OutOfMemory!int {
    r := try x.ModInverse(a, y)
    return if r.IsNone() { 0 } else { 1 }
}

export OpModPow(a: &Arena, x: &big.Int, e: &big.Int, m: &big.Int): OutOfMemory!int {
    return (try x.ModPow(a, e, m)).BitLen()
}

export OpSqrt(a: &Arena, x: &big.Int): OutOfMemory!int {
    return (try x.Sqrt(a)).BitLen()
}
`

// TestQuery_StackStdBig: every std.big operation has a static stack bound
// in calls: each of its recursions is depth-bounded, and the answer names
// the deepest chain (the arena's own indirect calls aside).
func TestQuery_StackStdBig(t *testing.T) {
	dir := testPkg(t, map[string]string{"q.mk": bigOpsSource})
	for _, op := range []string{"OpAdd", "OpSub", "OpMul", "OpDivMod", "OpMod", "OpText", "OpParse", "OpGcd",
		"OpModInverse", "OpModPow", "OpSqrt"} {
		a := queryOne(t, dir, "stack", op)
		text := a.Text()
		if strings.Contains(text, "no static bound: recursion") || !strings.Contains(text, "deepest direct chain: ") {
			t.Errorf("stack %s has no static bound:\n%s", op, text)
		}
	}
	answerHas(t, queryOne(t, dir, "stack", "OpMul"), "depth-bounded recursion Karatsuba",
		"depth-bounded recursion Toom3 -> MulEq -> Toom3")
	answerHas(t, queryOne(t, dir, "stack", "OpDivMod"), "DivRecStep (depth-bounded, 256 frames)")
}
