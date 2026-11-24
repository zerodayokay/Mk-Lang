# Mk memory model

Mk is memory safe without a garbage collector and without lifetime annotations. This page explains how: who owns a value, when a value moves, what a reference may do while it lives, and what a function signature promises its callers. It is written for programmers who know Rust or C. Read [the overview](overview.md) first if you have not written Mk before.

Sections 1 and 2 give the model and its words, and show how to read a borrow error. Section 3 states the rules R1 to R21 one at a time, each with a program the compiler accepts, one it rejects, and the fix. Sections 4 to 9 are for looking things up: what each operation does to loans, the signature clauses, hard cases, the patterns Rust accepts and Mk rejects, the run-time checks, and what is guaranteed.

## 1. The model

Seven sentences cover it:

1. Every value is plain, unique or linear: a plain value copies, a unique value moves, and a linear value moves and must be consumed.
2. `&p` and `mut p` borrow a place, and each borrow is a **loan**, shared or exclusive.
3. A value that contains a reference **holds** the loans it depends on. The compiler tracks this through the function, so you write no lifetimes.
4. A loan is **live** while some value that holds it, directly or through other values, may still be used.
5. While a loan is live, its place may not be used in a way that conflicts with it: no write under a shared loan, no other use under an exclusive loan, and no move or end of scope under either.
6. A signature says where a result's loans come from (`from`) when the types leave it open, and says when the function stores one argument inside another (`into`).
7. Each body is checked against its own signature, and a caller sees only signatures, never a body.

A lexer shows most of it. It holds a shared view of its input and hands out words from it:

```mk
// verdict: ok
struct Lexer {
    src: str // a shared view: the lexer does not own the text
    pos: int
}

// No clause is needed: a word can only come from the text behind src,
// never from the lexer's own bytes, so the lexer is free once Next returns.
Lexer.Next(mut self): ?str {
    for self.pos < self.src.Len() && self.src.Byte(self.pos) == b' ' {
        self.pos += 1
    }
    if self.pos == self.src.Len() {
        return none
    }
    start := self.pos
    for self.pos < self.src.Len() && self.src.Byte(self.pos) != b' ' {
        self.pos += 1
    }
    return some(self.src[start..self.pos])
}

Longest(src: str): str from src {
    var lx := Lexer(src: src, pos: 0) // lx holds the loans src holds
    var best := ""
    for {
        w := lx.Next() else { // w borrows the text, not lx
            break
        }
        if w.Len() > best.Len() {
            best = w // keeping w while lx goes on is fine
        }
    }
    return best // the result comes from src, as the signature says
}
```

Make the lexer own its bytes, and the same pattern is refused. A slice of `data` points into the buffer itself, so it keeps the buffer borrowed, and a second `mut` borrow conflicts with the first slice:

```mk
// verdict: reject E0501
struct Buf {
    data: [16]u8 // owned bytes, stored inline
    pos: int
}

Buf.Peek(mut self): []u8 {
    return self.data[self.pos..]
}

Two(b: mut Buf) {
    x := b.Peek()      // x borrows *b
    y := b.Peek() // <- E0501
    Use(&x)
    Use(&y)
}
```

The fix is to finish with `x` before taking `y`, or to copy what you need out of `x` first. Nothing in either program names a lifetime: the types of the fields decide (R10).

### Reading a borrow error

Compiled as a program, the rejected `Two` above gives:

```text
buf.mk:12:10: E0501 exclusive-conflict: *b conflicts with a live exclusive loan held by x
    |     y := b.Peek()
    |          ^^^^^^^^
    loan: *b (exclusive) at buf.mk:11:10
    holder: x
    signature: Peek(mut self): []u8
    explain: mk explain E0501
```

Every borrow error names three things:

- **The access**, on the first line: what you did, to which place.
- **The loan** it conflicts with: where it was made, and whether it is shared or exclusive.
- **The holder**: the value that keeps the loan live, and the signature that made it hold the loan.

Fix it in one of four ways:

1. **End the holder sooner.** Move the holder's last use before the conflicting access. A loan ends at its holder's last use, not at the end of a block.
2. **Hold less.** Copy what you need out of the holder (a number, a length, a copy of the text), so that no live value holds the loan at the access.
3. **Make the signature say less.** If the holder borrows more than it needs, the callee's signature can name where the result really comes from, for example `from self.src` instead of all of `self` (R10).
4. **Make the owner live longer.** For E0505, declare the borrowed value in an outer block, next to the value that keeps the reference.

| Codes | What went wrong | Rules |
|---|---|---|
| E0401, E0405, E0406 | A value used after it moved | R1, R12, R21 |
| E0402, E0411 | A linear value not consumed on some path, or changed before a failure | R2 |
| E0403 | A local read before it is assigned | R3 |
| E0404, E0508 | An assignment to, or a `mut` borrow of, a local not declared `var` | Add `var` |
| E0408, E0409 | A copy of a value that is not plain, or a move out of a field or reference | R1, R4 |
| E0410 | An exported struct whose private fields make it a carrier, unique, linear or interior-writable for loans, without `plain view`, `view`, `mut view` or `linear` | R9, R18 |
| E0501 to E0504 | An access conflicts with a live loan | R5 |
| E0505 | A loan outlives the local it borrows | R6, R11 |
| E0506 | A loan on a temporary kept past its statement | R14 |
| E0507 | A write through a shared reference | R12 |
| E0601, E0602, E0607, E0608 | A result's sources: missing, wrong, or not allowed | R9, R10 |
| E0603 | A result that borrows a local | R9 |
| E0604 | A store into a parameter with no `into` clause | R11 |
| E0605 | A short-lived loan stored in a global | R16 |
| E0609 | A `fn` literal that captures more than it may | R13 |
| E1001 to E1003 | A value that may not cross into a task, or a misused `Scope` | R17 |

`mk explain E0501` prints a code's meaning with an accepted and a rejected example.

## 2. Vocabulary

| Term | Meaning |
|---|---|
| Place | A path to memory: a local, then fields, dereferences and index steps. `a.b`, `*r`, `r.f`, `xs[_]`. All indexes of one array or slice are the same place |
| Root | What a place starts from: a local, a parameter, a temporary or a global |
| Loan | What `&p` or `mut p` creates: the borrowed place, and whether the borrow is shared or exclusive. A borrow error prints it as `loan:` |
| Holder | A value that holds a loan. A borrow error prints the one that keeps the loan live as `holder:` |
| Target of a loan | The root it points at. A loan taken through a dereference, such as `&r.f`, has no target of its own: the new reference also holds whatever `r` held |
| Holds, `H(x)` | The loans `x` depends on directly: "`x` borrows these" |
| Reaches | `H(x)`, plus what the targets of those loans hold, and so on |
| Behind, `B(x)` | What the targets of `H(x)` hold, one step on: "borrows what `x` points at". A value read through a reference that `x` holds holds `B(x)`. Two loans stay in `B(x)` as well: that of an allocator the read stepped through, and that of a global whose type holds a reference |
| Carrier | A type that can hold loans: a reference type, a type with a carrier field, a `view` type, or a type parameter |
| Live | Used later on some path, counting uses in a `defer` or `onerr`, and a `scope` value at its block's exit |

**Why `B` matters.** A lexer holds a view of its input. A word read from it needs the input to stay alive, but not the lexer. `H(lx)` is "borrows the lexer"; `B(lx)` is "borrows the text the lexer points at". Most of what Mk accepts without annotations comes from telling the two apart.

**Parameters.** Inside a function, the compiler does not know what a caller passed. It treats a reference parameter `p` as a loan on unknown memory, and what lies behind that memory as one more unknown loan. Reading through them is free. Storing another value's loans into them needs a clause, `into` (R11), because the caller may hold that memory through other values.

## 3. Rules

Each rule below says what it requires, why, and shows a program the compiler accepts, a program it rejects with the error it reports, and how to fix the rejected one. The examples use the test prelude of [the reference](reference.md#19-test-prelude): `Point`, the linear `Res` with `Acquire` and `Close`, and the helpers `Use`, `UseMut`, `Take`, `MakePoint` and `Cond`.

### R1. Value kinds

Every type has a kind that decides what happens when a value of it is used. Plain values copy, unique values move, and linear values move and must also be consumed.

| Kind | On use | Types |
|---|---|---|
| Plain | Copies | Numbers, `bool`, `char`, `&T`, `[]T`, `str`, `fn`, `*T`, `plain view` types, and enums and structs of plain fields |
| Unique | Moves, no obligation | `mut T`, `mut []T`, `List`, non-`plain` `view` types (`view`, `mut view`), anything holding a `Cell`, anything that asserts `Send` or `Share` in `unsafe`, anything holding a unique value |
| Linear | Moves, must be consumed (R2) | Types declared `linear`, such as `Arena` and `fs.File`, anything holding a linear value |

Why: a copy is safe only for a value that owns no resource and grants no exclusive access.

```mk
// verdict: ok
Copies() {
    a := Point(x: 1, y: 2)
    b := a // Point is plain: a copy
    Use(&a)
    Use(&b)
}
```

```mk
// verdict: reject E0401
Moves() {
    f := Acquire("a")
    g := f // Res is linear: f moves into g
    f.Close() // <- E0401
    g.Close()
}
```

Fix: use the new name, or borrow instead of moving.

```mk
// verdict: ok
Borrows() {
    f := Acquire("a")
    g := &f // a shared loan, not a move
    Use(g)
    f.Close()
}
```

The rules in full:

- A struct's kind is the worst kind among its fields, after type arguments are substituted, so `Pair[Res]` is linear with no keyword.
- Write `linear` on a type that has no linear field but must be consumed, such as a handle.
- In a generic body, an unbounded type parameter is treated as linear. `T: Plain` asks for a plain type; a non-`plain` `view` built on raw pointers is not `Plain`.
- `Cell[T]` needs `T: Plain`, because `Get` copies.
- An array repeat `[e; N]` needs a plain `e` (E0408):

```mk
// verdict: reject E0408
Dup() {
    var n := 0
    arr := [mut n; 2] // <- E0408
    Use(&arr)
}
```

### R2. Linear values are consumed exactly once on every path

A linear value must be consumed on every path: moved into a call, moved into another owner, or destructured. Why: Mk has no implicit destructors, so a forgotten `Close` is a compile error instead of a leak.

```mk
// verdict: ok
Reads() {
    f := Acquire("a")
    defer f.Close() // consumed at every exit
    Use(&f)
}
```

```mk
// verdict: reject E0402
Partly(c: bool) {
    f := Acquire("a") // <- E0402
    if c {
        f.Close()
    }
}
```

Fix: consume it on both paths, or once in a `defer`, as `Reads` does.

The rules in full:

- At a join, every path must agree: consumed on all of them, or on none.
- Assigning to a place of linear type needs the place to be moved out already, or the old value would be lost:

```mk
// verdict: reject E0402
Over() {
    var f := Acquire("a")
    f = Acquire("b") // <- E0402
    f.Close()
}
```

- `_ = e` does not consume a linear value (E0402).
- Leaving a statement by `try` or `else` while a linear temporary of it is still live is E0402.

**Fallible calls consume only on success.** A function returning `E!T` that takes a non-plain argument by value consumes it only when it succeeds. On the error edge of `try` or `else`, the caller still owns the argument and must still consume it, for example in an `onerr`:

```mk
// verdict: ok
error Full

// The check comes first, the move last, so a failure leaves r untouched.
Guarded(a: &Alloc, list: mut HeapList[Res], r: Res, room: bool): Full|OutOfMemory!void {
    if !room {
        fail Full
    }
    try list.Push(a, r)
}

Caller(a: &Alloc, list: mut HeapList[Res]): Full|OutOfMemory!void {
    r := Acquire("a")
    onerr r.Close() // runs only if Guarded fails, when r is still ours
    try Guarded(a, list, r, Cond())
}
```

So the callee must leave such a parameter unchanged on every path that can still produce an error. Those paths end in a `fail`, a `try` error edge (also one inside an `else`), or a `return` of an `E!T` value. Unchanged means:

- not moved, assigned, or written through a field;
- not borrowed `mut`, and not given to a `mut self` method;
- not consumed by a `defer` or `onerr`;
- for an inline `Mutex` of a non-plain type in it, not borrowed at all.

Changing it and then failing is E0411 at the change:

```mk
// verdict: reject E0411
error Full

Eager(a: &Alloc, list: mut HeapList[Res], r: Res, room: bool): Full|OutOfMemory!void {
    try list.Push(a, r) // <- E0411
    if !room {
        fail Full
    }
}
```

Fix: check first and move last, as `Guarded` does, or split the function in two. A plain parameter is a copy, and the rule does not apply to it. The rule costs nothing at run time: the callee works on its own copy of the argument's bytes, so the caller's bytes stay untouched until the call succeeds.

### R3. Assignment before use

A local may be declared with no value, as `x: i32`. Every path must assign it before it is read, and a local that is not `var` is assigned exactly once. Why: reading memory that was never written is undefined behaviour in C, and Mk has no zero default to hide it.

```mk
// verdict: ok
Late(c: bool): i32 {
    x: i32
    y: i32
    if c {
        x = 1
        y = 10
    } else {
        x = 2
        y = 20
    }
    return x + y
}
```

```mk
// verdict: reject E0403
TooLate(c: bool): i32 {
    x: i32
    if c {
        x = 1
    }
    return x // <- E0403
}
```

Fix: assign it on every path, as `Late` does, or give it a value where it is declared.

### R4. Places and moves out of them

The compiler tracks places, not values. Different fields of a struct are different places, so they may be borrowed separately. All indexes of one array or slice are one place, because the compiler does not compare index values. An enum payload is a field of its variant. Why: fields are told apart by name, which is known at compile time; indexes are not.

```mk
// verdict: ok
Disjoint() {
    var p := Point(x: 1, y: 2)
    a := mut p.x
    b := mut p.y // a different field: no conflict
    *a += 1
    *b += 1
}

struct Both {
    a: Res
    b: Res
}

Release(p: Both) {
    Both(a, b) := p // take the whole value apart
    a.Close()
    b.Close()
}
```

```mk
// verdict: reject E0501
Indexes(xs: mut []i32) {
    a := mut xs[0]
    b := mut xs[1] // <- E0501
    *a += *b
}
```

Fix: copy one element out first (`b := xs[1]`, then `a := mut xs[0]`), or use a library function that hands out disjoint elements, such as `GetPair` or `SplitAt` (R18). Iteration with `for x in mut xs` also gives each element its own loan (R20).

There are no partial moves. Reading a unique or linear field or element out of a value is E0409, with or without a reference in the way:

```mk
// verdict: reject E0409
struct Both {
    a: Res
    b: Res
}

Leak(p: Both): Res {
    return p.a // <- E0409
}
```

Fix: destructure the whole value, as `Release` does. A type with private fields can be destructured only in the module that declares it.

### R5. The conflict table

An access to a place conflicts with a live loan when the two places overlap and the table below says so. Two places overlap when one is a prefix of the other: `p` and `p.x` overlap, `p.x` and `p.y` do not. Why: this is the rule that makes data races and use-after-free impossible in safe code; a shared loan allows readers only, an exclusive loan allows no one else.

| Live loan | Read | Write | `&p` | `mut p` | Move | Scope end |
|---|---|---|---|---|---|---|
| Shared | ok | E0502 | ok | E0501 | E0503 | E0505 |
| Exclusive | E0504 | E0502 | E0504 | E0501 | E0503 | E0505 |

```mk
// verdict: ok
Shared() {
    var n := 1
    a := &n
    b := &n // two shared loans may coexist
    Use(a)
    Use(b)
    n = 2 // a and b are dead here
}
```

```mk
// verdict: reject E0502
Frozen() {
    var n := 1
    a := &n
    n = 2 // <- E0502
    Use(a)
}
```

Fix: finish with the reference before the write, as `Shared` does, or write through a `var` copy.

The rules in full:

- A loan counts as live at an access if a holder of it is used at or after that access. A value being moved counts as a holder of its own loans.
- A write to `p`, and the end of `p`'s scope, are **shallow**: they conflict with loans on `p`, its prefixes and its fields, but not with loans on `*p`, because the target of a reference outlives the reference.
- Every other access is **deep**: it conflicts with loans on anything under `p` through fields and dereferences. Depth follows places, not the loan graph.

### R6. Liveness is transitive

A loan is live while some value that **reaches** it may still be used. If `out` holds a loan on `c`, and `c` holds a loan on `short`, then a later use of `out` keeps the loan on `short` live. Why: a reference to a reference is only as good as the inner one.

```mk
// verdict: ok
LastUseEndsIt() {
    var p := MakePoint()
    a := &p.x
    Use(a) // last use of a: its loan ends here
    p.x = 2
}
```

A `defer` counts as a use at each exit, so a reference used in one stays live to the end of the block:

```mk
// verdict: reject E0502
DeferKeepsIt() {
    var n := 1
    a := &n
    defer Use(a)
    n = 2 // <- E0502
}
```

Fix: write `n` before borrowing it, or use `a` before the write instead of in the `defer`.

Transitivity matters when loans hide inside other values. Here `c` holds a loan on `long`. `Both` stores `&short` into the cell and reads it back through its other argument, which is the same cell, so `out` ends up reaching `short`:

```mk
// verdict: reject E0505
Both(a: &Cell[&i32], b: &Cell[&i32], v: &i32 into a): &i32 from b {
    a.Store(v)
    return b.Load()
}

Hole() {
    long: i32 = 1
    c := Cell.New(&long)
    var out := &long
    if Cond() {
        short: i32 = 2
        out = Both(&c, &c, &short) // <- E0505
    }
    Use(out)
}
```

Fix: give `short` a scope at least as long as `out`'s, for example by declaring it next to `long`. For a local struct tracked per field (R7), each field counts as a value of its own: a use of `p.a` keeps `a`'s loans live, and a use of the whole of `p` keeps every field's.

### R7. Values hold loans

A value that contains references holds their loans. Copying or moving the value copies its held set, and writing a whole local replaces it. Temporaries hold loans too, for the length of their statement. Why: a loan must stay live as long as anything that can reach the borrowed place.

```mk
// verdict: ok
Replace() {
    var n := 1
    var m := 2
    var r := &n
    r = &m // r no longer holds the loan on n
    n = 3
    Use(r)
}
```

```mk
// verdict: reject E0502
struct Pair {
    a: &i32
}

Held() {
    var n: i32 = 1
    p := Pair(a: &n) // p holds the loan on n
    n = 2 // <- E0502
    Use(&p)
}
```

Fix: write `n` after the last use of `p`, or before `p` is built.

**One set per field.** A local struct keeps one held set per field, so each field's loans end at that field's last use. A struct of two views behaves like two locals:

```mk
// verdict: ok
struct Views {
    a: &Res
    b: &Res
}

Split() {
    x := Acquire("x")
    y := Acquire("y")
    v := Views(a: &x, b: &y)
    y.Close() // v.b is never used again, so y may go
    Use(v.a)
    x.Close()
}
```

This applies to a local, a by-value parameter or a temporary whose type is a struct or record, also under `?T` and `E!T`. It keeps one set per loan-holding field (R9), one level deep: a nested struct counts as one field. While it does, the value is **split**. When it holds one set for all its fields, it is **whole**. A value behind a reference is always whole. A by-value parameter starts split, every field holding the parameter's own loan.

These uses keep `p` split, because each touches only the field it names:

- reading `p.f`, or borrowing `&p.f` or `mut p.f` (a store through that borrow adds to `f` alone);
- assigning `p.f = e`, or a deeper `p.f.g = e` or `p.f[i] = e`;
- assigning the whole of `p` from a literal, a call with a per-field clause (R9), or another split value of the same type;
- copying, moving, destructuring or returning `p` whole: each field goes to its own;
- `&p`, a `&self` call included, when `p`'s type holds no `Cell`, `Mutex`, `Chan`, `Arena` or `view` inline.

Passing `p` by value, storing it, or giving it to `s.go` or to a generic `T` keeps `p` split, but the value that leaves holds the union of its fields.

Every other use makes `p` whole until its next whole assignment, and a store that reaches `p` through a whole loan adds to every field. That includes every `mut p` (a `mut self` call included, whatever the callee), `for x in mut p`, and `&p` of a type with one of the types above inline. A callee given `mut p` may move a loan from one field to another with no clause (R11), so afterwards the caller cannot know which field holds what. In a generic body, `&p` also makes `p` whole when its type mentions a type parameter or `Self`, since an instance may hold a `Cell` inline.

A shared borrow of the whole keeps the split when nothing in the type writes past it:

```mk
// verdict: ok
struct Views {
    a: &Res
    b: &Res
}

Show(v: &Views) {
    Use(v.a)
}

Shared() {
    x := Acquire("x")
    y := Acquire("y")
    v := Views(a: &x, b: &y)
    Show(&v)
    y.Close()
    Use(v.a)
    x.Close()
}
```

A whole `mut t` makes `t` whole, so `t.a` may now hold the loan on `y`:

```mk
// verdict: reject E0505
struct Two {
    a: &i32
    b: &i32
}

Mix(t: mut Two) {
    t.a = t.b
}

Whole() {
    long: i32 = 1
    var t := Two(a: &long, b: &long)
    if Cond() {
        y: i32 = 2
        t.b = &y // <- E0505
        Mix(mut t)
    }
    Use(t.a)
}
```

Fix: keep `y` alive as long as `t` is used, or pass the callee only the field it works on. Declaring the store with `*t into t` changes nothing, since that is what the caller already assumes:

```mk
// verdict: reject E0505
struct Two {
    a: &i32
    b: &i32
}

Mix(t: mut Two) *t into t {
    t.a = t.b
}

Missed() {
    long: i32 = 1
    var t := Two(a: &long, b: &long)
    if Cond() {
        y: i32 = 2
        t.b = &y // <- E0505
        Mix(mut t)
    }
    Use(t.a)
}
```

Joins and loops take the union field by field, and a value whole on one path is whole after the join. Here the arm that wrote `t.a` keeps `y` in it:

```mk
// verdict: reject E0505
struct Two {
    a: &i32
    b: &i32
}

Join() {
    long: i32 = 1
    var t := Two(a: &long, b: &long)
    if Cond() {
        y: i32 = 2
        if Cond() {
            t.a = &y // <- E0505
        } else {
            t.b = &y
        }
    }
    Use(t.a)
}
```

More details of the split:

- A use of the whole value uses every field (R6). `defer` and `onerr` bodies count as code at each exit.
- `Swap(mut p.a, mut p.b)` leaves both fields with the union of the two. `Replace(mut p.f, v)` adds `v`'s set to `f`, and the result holds `f`'s set.
- A linear field is consumed with its whole value. A `view` field is an ordinary field.
- When a fallible call fails, the argument it hands back keeps its sets (R2).
- A move of the whole value leaves it moved, with its sets empty.
- Field sets never hide a store. When a call could write a loan of one field where another field's loans live, the compiler checks the whole function again with one set per value.

**A view's rest.** A `view` may keep loans outside its declared fields, in raw storage or in a field that holds no loans as typed. So a split view holds one more set after its fields, its **rest**: what it may hold that no field accounts for.

- A literal's rest holds every operand's loans. A per-field call result's rest holds every source of the clause (R9). A by-value parameter's rest holds the parameter's own loan.
- A write to one field leaves the rest alone. Only a write of the whole view replaces it.
- A use of the whole view, every method call included, needs everything the view was given. A read of one carrier field needs only that field's set.
- A use of a field that holds no loans as typed, such as a raw pointer, is a use of the whole view.
- A `mut view` (or any type that takes loan stores through `&`, such as a read-only `view` with a loan-interior-writable type argument like `Box[Mutex[?&i32]]`) may write past a shared reference, so a shared reference never keeps one split: `&v`, a `&self` call included, makes it whole. A read-only `view` or `plain view` keeps its split across `&v`.

A view whose fields are read one at a time splits like a struct: declared as `view struct Views`, the `Split` above still compiles.

A loan the view keeps in raw storage stays in its rest when a field is written, so the whole value still holds it:

```mk
// verdict: reject E0505
import std.raw

view struct Tagged {
    p: *i32
    tag: &i32
}

Tagged.Get(&self): i32 {
    unsafe "p points at an i32 the view holds a loan on" {
        return raw.Read(self.p)
    }
}

Keep(t: &i32): Tagged from t {
    var v := Tagged(p: raw.Of(t), tag: t)
    if Cond() {
        n: i32 = 7
        v = Tagged(p: raw.Of(&n), tag: &n) // <- E0505
        v.tag = t
    }
    return v
}
```

Fix: assign the whole view (`v = Tagged(p: raw.Of(t), tag: t)`), which replaces the rest. The split trusts `unsafe` code as a struct's does: a carrier field holds only the loans the compiler tracked into it. Writing a reference made from raw storage into a carrier field breaks that, and is the `unsafe` code's fault (R19).

### R8. Reading through a reference

A value read out of memory through a reference `r` holds `B(r)`: what the memory held, not the reference used to reach it. Why: once you have a copy of a field, the struct you read it from may change or go; what the field pointed at may not.

```mk
// verdict: ok
struct Holder {
    name: str
}

ReadOut(h: &Holder): str {
    return h.name // borrows the text, not *h
}
```

```mk
// verdict: reject E0501
struct Holder {
    name: str
}

ReadOut(h: &Holder): str {
    return h.name
}

StillHolds(a: &Arena) {
    var t := Text.New(a)
    h := Holder(name: t.Str())
    n := ReadOut(&h)
    t.Clear() // <- E0501
    Use(&n)
}
```

`n` does not borrow `h`, but it does borrow `t`, through the string `h` held. Fix: finish with `n` before `t.Clear()`, or copy the text into storage that outlives the clear.

### R9. Result sources

A result that can hold loans must come from somewhere the caller can see. Usually the types decide which parameter that is, and you write nothing. When more than one parameter could supply the result, the signature names its sources with `from`. Why: the caller checks its own body against the signature alone, so the signature must say which arguments the result keeps borrowed.

```mk
// verdict: ok
First(xs: []i32): &i32 {
    return &xs[0] // only xs can supply an &i32
}

At(xs: []i32, i: i32): &i32 {
    return &xs[i]
}

Longest(a: str, b: str): str from a, b {
    if a.Len() > b.Len() {
        return a
    }
    return b
}
```

```mk
// verdict: reject E0601
Longer(a: str, b: str): str { // <- E0601
    if a.Len() > b.Len() {
        return a
    }
    return b
}
```

Fix: say which parameters the result comes from, `from a, b`, as `Longest` does. A clause is checked against the body, so it cannot lie:

```mk
// verdict: reject E0602
Pick(a: str, b: str): str from a {
    return b // <- E0602
}
```

Fix: name `b` in the clause, or return `a`. A result can never borrow a local of the function:

```mk
// verdict: reject E0603
Dangling(): &i32 {
    n: i32 = 1
    return &n // <- E0603
}
```

Fix: return the value (`: i32`), or take the storage as a parameter and return a reference into it.

The sources by result type:

| Result type | Where its loans come from |
|---|---|
| Not a carrier | Nothing |
| Holds no reference of its own, but mentions a type parameter | What each reference parameter mentioning it points at (`B(p)`), and what each by-value parameter mentioning it holds (`H(p)`). Automatic |
| Holds a reference of its own, or is a `view` type | The one parameter that could supply it, automatically, by R10. If several could, the signature says `from`. If none could, only statics qualify, and the `return` check enforces that |

A clause may be written on any carrier result, and it replaces the automatic source. All of this is decided on the signature as written, before type arguments are substituted.

**Which parameters could supply a result.** A parameter could supply a result when both hold:

- the parameter is a carrier and is not a `fn`, and
- a type the result refers to is found inside the parameter's type.

What "refers to" and "found inside" mean:

- A result refers to every type its references point at. A `view` result refers to everything.
- A non-`view` type contains its declared fields. A `view` or `Self` contains everything.
- A type parameter contains itself and what its bounds mention, and `Self` mentions every type parameter.
- `&Alloc` and `&Heap` supply only `view` results, and `Io` never supplies one.

An exported type whose private fields make it a `Plain` and `Freeze` carrier must be declared `plain view` (or `view`), one whose private fields make it a non-`plain` carrier or unique must be declared `view`, one whose private fields take loan stores through `&` must be declared `mut view`, and one they make linear must be declared `linear` (E0410). So a change to private fields cannot change what a caller is allowed to do.

**`fn` types and interfaces.** A `fn` type or an interface method states its flows the same way, with parameter names where a clause needs them: `fn(x: &i32): T from x`. A `fn` value or an `impl` method fits only if each of its flows is covered by a declared flow (E0608). So `Id(x: &i32): &i32` does not fit `fn(&i32): T`, which declares no flow.

- Borrowing `p` covers borrowing what `p` points at: `H(p)` covers `B(p)`.
- A per-field flow is compared field by field.
- A whole declared flow covers a per-field one that it covers in every field.
- A per-field declared flow covers a whole one only when every loan-holding field's sources cover it.

**Per-field sources.** A struct or record result, also under `?T` and `E!T`, may give each field its own sources:

```mk
// verdict: ok
struct Parsed {
    nums: List[i64]
    name: str
}

// nums lives in the arena; name is a slice of src.
Parse(a: &Arena, src: str): OutOfMemory!Parsed from (nums: a, name: src) {
    var nums := List[i64].New(a)
    var name := ""
    var rest := src
    if i := src.Find(":") {
        name = src[..i]
        rest = src[i + 1..]
    }
    for w in rest.Words() {
        n := w.ParseInt() else {
            continue
        }
        try nums.Push(n)
    }
    return Parsed(nums: nums, name: name)
}

Name(sys: &System, src: str): OutOfMemory!str from src {
    var scratch := try Arena.New(sys.heap)
    onerr scratch.Free()
    Parsed(nums, name) := try Parse(&scratch, src)
    n := nums.Len()
    scratch.Free() // name does not borrow the arena, so this is fine
    Use(&n)
    return name
}
```

The forms are `from (nums: a, name: src)`, several sources for one field as `from (tree: a, src, name: src)`, and paths as R10 allows, `from (text: self.src)`.

- A field is **loan-holding** when its type as written is a carrier or mentions a type parameter.
- A loan-holding field the clause leaves out holds every source the clause names.
- A named field must exist (E0203), be named once (E0206) and be loan-holding (E0607).
- On a result that is not a struct or record, such as a bare type parameter, a reference or a slice, a per-field clause is E0203.
- A `view` result takes one as a struct does. Its rest (R7) holds every source of the clause, and no clause can name it.
- The declaring module may name a private field of an exported struct, which then becomes part of the public signature.

In the callee, a `return` is checked field by field when its value is built field by field. That is a literal, a call with a per-field clause, a part taken from either, a split local (R7), or a value chosen by `if` or `match` from those. Any other value has one set, and the sources of every loan-holding field must cover each of its loans (E0602):

```mk
// verdict: reject E0602
struct Pair {
    a: str
    b: str
}

Cross(x: str, y: str): Pair from (a: x, b: y) {
    return Pair(a: y, b: x) // <- E0602
}
```

Fix: return `Pair(a: x, b: y)`, or name both sources for both fields.

At the caller, each field of the result gets the union of its sources. The result keeps one set per field while it is taken apart by:

- a copy or move of the whole into a local, a temporary or a pattern, with `some` or `ok` added or unwrapped;
- a destructuring, with or without `..`, or a pattern in an `if` or `match`;
- a field access on the call, `F().b`;
- any of these under `try`, `else`, `.Must()` and `for (a, b) in`.

Any other step, through a reference, an index or a user enum's variant, reads the union. A local bound to the whole result is split (R7).

The sources are read after the call has made its stores (section 4, calls), because a source may hold what the same call stored into it. Here the loan on `y` reaches the field `r` only through `c into a`:

```mk
// verdict: reject E0505
struct Got {
    r: &i32
    s: str
}

K(c: &i32 into a, a: str, b: str): Got from (r: a, s: b) {
    return Got(r: c, s: b)
}

Caller() {
    long: i32 = 1
    var r := &long
    if Cond() {
        y: i32 = 2
        Got(r: q, ..) := K(&y, "a", "b")
        r = q // <- E0505
    }
    Use(r)
}
```

### R10. Where a result comes from

When a result has a reference of its own and you write no clause, the compiler picks its source by one question: **could this result point into the parameter's own bytes?** If it could, the result borrows the parameter. If it could only have come through a shared reference the parameter holds, the parameter stays free. Why: this is what lets a lexer hand out tokens while it keeps running, with no annotation.

A type's **own bytes** are its fields, not what its shared references point at. Some things count as own bytes together with everything they reach:

- a `mut` reference field and a `view` field, because neither can be copied out;
- a `view` type, `Self` and a type parameter.

`[]T` and `mut []T` are references to `T`, and `str` is a reference to `u8`.

| Parameter `p` | Result | The result holds | `p` |
|---|---|---|---|
| By value | Any | `H(p)` | Moved or copied |
| `&S` or `mut S` | Holds a `mut` reference at any depth | `H(p)` | Stays borrowed |
| `&S` or `mut S` | Refers to a type in `S`'s own bytes, or to `S` itself | `H(p)` | Stays borrowed |
| `&S` or `mut S` | Anything else: it can only come through a shared reference `S` holds | `B(p)` | Free |

A lexer stores no text in its own bytes, so a token can only come through the view it holds, and the lexer stays free:

```mk
// verdict: ok
struct Lexer {
    src: str
    pos: int
}

Lexer.Next(mut self): ?str {
    start := self.pos
    self.pos += 1
    return some(self.src[start..self.pos])
}

Two(lx: mut Lexer) {
    a := lx.Next()
    b := lx.Next()
    Use(&a)
    Use(&b)
}
```

A buffer stores its bytes, so a view of them borrows the buffer: the second `b.Peek()` of `Buf` in [the model](#1-the-model) is E0501.

Fix: use `x` before taking `y`. If the bytes do not need to live in the buffer, hold them as a `[]u8` view instead; then a result comes through the view and the buffer stays free.

A `mut` field is own bytes, because the reference cannot be copied out. Anything read through it depends on the struct staying put:

```mk
// verdict: reject E0502
struct Slot {
    m: mut i32
}

Peek(s: &Slot): &i32 {
    return &*s.m
}

Alias() {
    var n: i32 = 1
    var s := Slot(m: mut n)
    a := Peek(&s)
    *s.m = 2 // <- E0502
    Use(a)
}
```

Fix: write through `s.m` only after the last use of `a`.

**Which fields count.** The fields the function's callers can see. A private function sees them all. An exported function counts exported fields, and if a private field would change the answer, E0601 asks for a clause. So a change to private fields cannot change a caller.

**Writing `from`.** A clause names where the result comes from, the way the `return` does:

| Clause | Meaning | When it is needed |
|---|---|---|
| `from p` | Borrows `p`: the result holds `H(p)`, and `p` stays borrowed, whatever the table above says | Several parameters could supply the result |
| `from p.f` | Came through the reference in field `f`: the result holds `B(p)`, and `p` stays free | The table would keep `p` borrowed, or several parameters could supply the result |
| `from a, self.src` | Several sources | |
| `from (a: p, b: q.f)` | Per field: each field holds what its sources give, and a loan-holding field left out holds all of them (R9) | A result's fields come from different parameters |

```mk
// verdict: ok
view struct Words2 {
    text: str
    pos: int
}

// A view counts as own bytes, so with no clause the result would borrow w.
// from self.text says it came through text, so w stays free.
Words2.Next(mut self): ?str from self.text {
    start := self.pos
    self.pos = self.text.Len()
    return some(self.text[start..])
}

Two(w: mut Words2) {
    a := w.Next()
    b := w.Next()
    Use(&a)
    Use(&b)
}

struct Index {
    words: List[str]
}

Index.First(&self): str from self.words {
    return self.words[0]
}
```

The rules for `from p.f`:

- `p` must be a reference. On a by-value `p`, write `from p`.
- `f` is a field or a path of fields, and it must hold a shared reference or a `view`.
- An inline field means the result borrows `p`, so naming one is E0607. A `mut` field cannot be copied out, so naming one is E0607 too.
- A named field is part of the public signature, even if it is private.
- At a call, `from p.f` gives all of `B(p)`, not only what came through `f`.
- Under `from p.f` with a `view` field `f`, such as a `List`, only values copied out of it may be returned. A reference into its memory, such as `&self.xs[0]` under `from self.xs`, is E0602: write `from self`, or no clause.

Clauses are always checked against the body (E0602), and the compiler can compute the clause for you. A clause may name more sources than the body uses, never fewer.

```mk
// verdict: reject E0607
struct Sink {
    buf: mut []u8
}

Sink.Out(mut self): mut []u8 from self.buf { // <- E0607
    return mut *self.buf
}
```

Fix: write `from self`. The result then keeps `self` borrowed, which is what an exclusive reference requires.

### R11. `into`, and `*q into p`

A function that stores one parameter inside another says so in its signature with `into`. Nothing is inferred: a body that stores without saying so is rejected. Why: after the call, the caller must know which of its values now hold loans of which others, and it reads that from the signature alone.

```mk
// verdict: ok
struct Index {
    words: List[str]
}

Index.Add(mut self, word: str into self): OutOfMemory!void {
    try self.words.Push(word)
}

Trade[T](a: mut T, b: mut T) *a into b, *b into a {
    Swap(a, b)
}

struct Deep {
    a: &i32
    b: mut &i32
}

Deep.Mix(mut self) *self into self {
    *self.b = self.a
}
```

```mk
// verdict: reject E0604
struct Index {
    words: List[str]
}

Index.Add(mut self, word: str): OutOfMemory!void {
    try self.words.Push(word) // <- E0604
}
```

Fix: declare the store, `word: str into self`, as the accepted `Index.Add` does.

| Form | Effect at the call |
|---|---|
| `x: T into p` | Every carrier root reachable from `p`'s argument that can be written also holds `H(x)` |
| `&self into p`, `mut self into p`, `self into p` | The same, with `x` the receiver |
| `*q into p`, written after the parameter list | What `q` points at is written into `p`: the same, with `B(q)`. `q` may be `p` |

`*q into p` needs `p` to be `mut`, or to reach loan-interior mutability (a `mut view`, `Arena`, `Scope`, or a `Cell`, `Mutex`, `Chan` or `atomic.Spin` whose payload holds loans), and `q` must be a reference or a slice. One word, `into`, covers every store; `from` only ever says where a result comes from.

A parameter that holds no loans (`H(x)` is empty, as for a by-value non-carrier) needs no clause. Passing a parameter to a function that stores it is a store too, so the clause is repeated up the call chain:

```mk
// verdict: reject E0604
struct Holder {
    slot: &Cell[&i32]
}

Put(c: &Cell[&i32], v: &i32 into c) {
    c.Store(v)
}

Stash(h: &Holder, v: &i32) {
    Put(h.slot, v) // <- E0604
}
```

Fix: `Stash(h: &Holder, v: &i32 into h)`.

The receiver is the first parameter, so its clause is written on it. Here the keys of `self.map` are views of `self`'s own bytes (R10), and they are stored into a list in `arena`:

```mk
// verdict: ok
struct Names {
    map: TextMap[int]
}

Names.Keys(&self into arena, arena: &Arena): OutOfMemory!List[str] from arena, self {
    var out := List[str].New(arena)
    for (key, value) in self.map.Iter() {
        _ = value
        try out.Push(key)
    }
    return out
}
```

Without the clause, `out.Push(key)` is E0604, and its fix writes `&self into arena`.

At the caller, the clause is what keeps the stored value's source alive. Here `Store` keeps a reference into `pt` inside `cell`, which outlives `pt`:

```mk
// verdict: reject E0505
Store(c: &Cell[?&i32], p: &Point into c) {
    c.Store(some(&p.x))
}

Leak() {
    cell := Cell[?&i32].New(none)
    if Cond() {
        pt := Point(x: 1, y: 2)
        Store(&cell, &pt) // <- E0505
    }
    if r := cell.Load() {
        Use(r)
    }
}
```

Fix: declare `pt` where it lives as long as `cell`. The same holds when the stored value comes from a buffer that ends first:

```mk
// verdict: reject E0505
struct Index {
    words: List[str]
}

Index.Add(mut self, word: str into self): OutOfMemory!void {
    try self.words.Push(word)
}

Fill(index: mut Index, a: &Arena): OutOfMemory!void {
    var t := Text.New(a)
    try t.Push("word")
    try index.Add(t.Str()) // <- E0505
}
```

Fix: copy the word into memory that outlives `index` and add the copy: `try index.Add(try a.Dup(t.Str()))`, with the store of the arena declared as `a: &Arena into index`.

**What a store reaches.** The callee may store at any depth below `p`, so the clause reaches every root that the argument of `p` reaches and that can be written. Walk the path from the argument:

- An exclusive step keeps writing possible, and a shared step ends it.
- A step through a `Cell`, `Mutex` or `Chan` of a carrier, or through a `mut view`, restores it, because those write loans past a shared reference. So `Mutex[mut &i32]` stays writable through its guard. The restoring step may come after shared ones: `c: &Ctx`, whose field `h: &Hub` holds a `Chan[&i32]`, takes `*c into c`, since the channel writes loans past both references.
- A root that cannot be written cannot receive a store, so one `into` does not affect unrelated roots. The test is about what can be written, not about type names: a store that changes type, such as `&p.x` from `p: &Point`, is still caught.

A store through a `mut` field of a local wrapper reaches what the field points at:

```mk
// verdict: reject E0505
struct Slot {
    r: mut &i32
}

Set(s: mut Slot, v: &i32 into s) {
    *s.r = v
}

Caller() {
    long: i32 = 1
    var p := &long
    if Cond() {
        short: i32 = 2
        var b := Slot(r: mut p)
        Set(mut b, &short) // <- E0505
    }
    Use(p)
}
```

Fix: `short` must live as long as `p`, since `Set` writes `&short` into `p`.

A `Cell` behind a shared reference field is a store target of `into self`, in the callee as well as at the caller:

```mk
// verdict: ok
struct Shelf {
    c: &Cell[&i32]
    n: int
}

Shelf.Put(mut self, x: &i32 into self) {
    self.c.Store(x)
}

Shelf.PutShared(&self, x: &i32 into self) {
    self.c.Store(x)
}
```

**A stored value's own loans.** Once `x` is stored into `p`, `x`'s own loans are reachable from `p` too. So what they can write, such as the referent of a `mut &` field of `x`, also receives `x`'s loans, less what it holds by being itself. Otherwise a later write through that field could store a loan of a short-lived local into a reference that outlives it.

- Inside a callee, `h.s = x` followed by `*h.s.a = x.b` is a store of `x` into `x`.
- `x: S into x` declares such a store when `x` is stored nowhere else.
- A parameter takes one `into`. Beside `x into h`, nothing can declare a store of `x` into `x`, so no body may store `x` into `h` and then write through it.
- This binds `unsafe` and `extern` bodies too (R19).

**Inside one parameter.** A store from one part of a parameter into a deeper part says so too: `*self into self`. A store into the first level needs no clause, because that level already holds everything below it. That is why a callee may move loans between the fields of a `mut` argument with no clause, and why a split local passed whole as `mut` is whole after the call (R7).

```mk
// verdict: reject E0604
struct Deep {
    a: &i32
    b: mut &i32
}

Deep.Mix(mut self) {
    *self.b = self.a // <- E0604
}
```

Fix: declare `*self into self`, as the accepted `Deep.Mix` does.

**Chains.** Clauses chain: `x into q` and `q into p` together declare `x` into `p`, and so do `*q into p` steps after the first, so `*a into b, *b into a` declares `a`'s contents into `a`. A result `from p` may hold what was declared into `p`, because the call applies the stores before it reads the result. The cookbook's `AddChild` relies on this: `name into a`, `a into parent`, and a result `from a` that holds `name`.

**Allocators.** A `List`, `Map` or `Text` made in an arena holds a loan on the arena. A value stored into a `List`, `Map` or `Cell` made in an arena is stored into the arena's memory. So the store reaches the arena itself, and a parameter arena needs the clause:

```mk
// verdict: ok
CopyAll(a: &Arena, xs: &List[str]): OutOfMemory!List[str] from a, xs *xs into a {
    var out := List[str].New(a)
    for i in 0..xs.Len() {
        try out.Push(xs[i])
    }
    return out
}
```

`*xs into a` is needed, since `out`'s storage is `a`'s memory. `*xs into xs` is not: each element is a `str`, through which nothing can be written (shallow loans, below).

The rules for allocators (`Arena`, `Alloc`, `Heap`):

- An allocator the argument holds, directly or on a later step, receives the store. The walk does not go on past it, except into what was stored in its memory.
- An allocator reached through the loan another allocator holds by its making (a sub-arena's parent, an arena's heap) is skipped, since nothing in the first one lives in it.
- A store into an allocator drops the loans it holds by being itself, such as a loan of the arena stored into its own memory. So does a store into the memory of an arena that a parameter `a: &Arena` refers to, so `a` need not be declared `into a`. With `a` a reference to an allocator, `*a into a` stores the handle's loans, which the allocator holds by its making, into the allocator itself: they are dropped too, so its heap or parent stays skipped.
- The allocators a parameter reaches are tracked apart from what a `mut` reference or a view beside them reaches, never merged with it.

The drop covers only the store into the allocator itself. With `a into o` in the same signature, `*a into a` chains into `*a into o`, so the heap's loan reaches `o`, and the store of the arena into `o` reaches the heap:

```mk
// verdict: reject E0505
B(a: &Arena into o, o: mut List[str]) *a into a {
}

Run(h: &Heap): OutOfMemory!void {
    var ar := try Arena.New(h)
    defer ar.Free()
    var o := List[str].New(&ar)
    B(&ar, mut o) // <- E0505
}
```

Fix: split the clauses into two calls, so nothing chains:

```mk
// verdict: ok
B1(a: &Arena) *a into a {
}

B2(a: &Arena into o, o: mut List[str]) {
}

Run(h: &Heap): OutOfMemory!void {
    var ar := try Arena.New(h)
    defer ar.Free()
    var o := List[str].New(&ar)
    B1(&ar)
    B2(&ar, mut o)
}
```

A view made in an arena and stored next to the arena's own loan is therefore no store of the arena into itself:

```mk
// verdict: ok
struct R {
    s: str
    t: str
}

Set(a: &Arena into r, r: mut R, rest: str into r): OutOfMemory!void {
    r.s = try a.Dup("x")
    r.t = rest
}

Load(a: &Arena): OutOfMemory!str from a {
    text := try a.Dup("a b c")
    var cur := R(s: "", t: "")
    try Set(a, mut cur, text)
    return cur.s
}
```

What a store drops comes back on the read side: a value read out of an allocator's memory, through a loan of that allocator, borrows the allocator (`B`, section 2). It cannot be freed or reset while the value lives:

```mk
// verdict: reject E0503
struct Blk {
    b: str
}

Run(h: &Heap): OutOfMemory!int {
    var sa := try Arena.New(h)
    onerr sa.Free()
    s := try sa.Dup("hello arena")
    p := try sa.Put(Blk(b: s))
    e := p.b
    sa.Free() // <- E0503
    return e.Len()
}
```

Fix: use `e` before `sa.Free()`.

The read side cannot tell a value that points into the arena from one that does not. So a literal, or a string of another arena, pushed into a scratch list and read back also borrows the scratch arena. Freeing the arena before such values are used is refused, although the program is sound:

```mk
// verdict: reject E0503
Run(h: &Heap, o: &Arena): OutOfMemory!int {
    s := try o.Dup("a string of the outer arena")
    var sa := try Arena.New(h)
    onerr sa.Free()
    var l := List[str].New(&sa)
    try l.Push("a literal")
    try l.Push(s)
    d := l.Done()
    e := d[0]
    f := d[1]
    sa.Free() // <- E0503
    return e.Len() + f.Len()
}
```

Fix: use such values before the scratch arena ends. The same cost applies to a call that copies out of scratch memory through a `*q into p` clause. `Slice.CopyFrom` from a scratch list into a frame array keeps the scratch arena borrowed while the array is used, as copying element by element (`out[0] = d[0]`) does. `Swap` of two arenas is accepted, since there `*q` is the arena's handle, not its memory.

**Shallow loans.** A type is **shallow** when nothing can be written through it:

- every reference in it, inline or in its fields, payloads and elements, is shared and points to data that is no carrier, holds nothing writable (`Cell`, `Mutex`, `Chan`, `Arena`, a `view`) and is no allocator, and
- nothing writable, no `fn`, no type parameter and no `Self` is inline in it.

`str`, `[]u8`, `&i32`, `?str`, `[N]str` and structs or records of them are shallow, and so is a type with no references. A `view` never is. When a call stores a value of shallow type, nothing can later be written through what it stored, so the compiler does not follow the stored loans any further when it decides what a later store reaches. For `*q into p`, the type judged is what `q` points at. Liveness, conflicts and results see every loan, shallow or not.

### R12. Reborrowing

Passing a `mut T` local to a parameter declared `mut T` or `mut []T` lends it for the call instead of moving it. The local is usable again once the call's loans end. Binding, storing or returning it moves it. Why: an exclusive reference is unique, but a function must still be able to hand it to several calls in turn.

```mk
// verdict: ok
Twice(r: mut i32) {
    UseMut(r) // reborrowed for the call
    UseMut(r) // usable again
}
```

```mk
// verdict: reject E0401
Stored(r: mut i32) {
    s := r // binding moves r
    UseMut(r) // <- E0401
    UseMut(s)
}
```

Fix: use `r` directly, or use only `s` after the binding.

- The parameter's **declared** type decides: a generic parameter `x: T` with `T = mut i32` moves its argument.
- An `into` argument or a `.go` argument counts as storing, so it moves.
- The mode of a path is its weakest step: a `mut` reached through `&` is shared. Writing through it is E0507:

```mk
// verdict: reject E0507
struct Sink {
    out: mut []u8
}

Bad(s: &Sink) {
    s.out[0] = 1 // <- E0507
}
```

Fix: take the struct as `s: mut Sink`.

### R13. Order of evaluation

Arguments and constructor fields evaluate left to right, except that `&p` and `mut p` operands, a method receiver included, are formed after all the others. Why: so `list.Push(list.Len())` works, since the read of `list` is over before the exclusive borrow begins.

```mk
// verdict: ok
Grow(list: mut List[int]): OutOfMemory!void {
    try list.Push(list.Len())
}
```

```mk
// verdict: reject E0501
Same() {
    var n := 1
    Swap(mut n, mut n) // <- E0501
}
```

Fix: the two exclusive borrows of one call must be of different places, such as `Swap(mut a, mut b)`.

A borrow's whole place, index expressions and bounds checks included, is evaluated when the borrow is formed. An assignment evaluates its right side first, then its place.

**Captures.** A `fn` literal may name locals and parameters of the enclosing function when it is written directly as the argument of a `const` parameter of `fn` type, in a call that is not `s.go`:

- A captured name `x` that the literal only reads is a shared borrow `&x`. One that the literal assigns, borrows `mut`, calls a `mut self` method on, or reborrows `mut` is an exclusive borrow (`mut x`, or a reborrow `mut *x` or `mut x[..]` when `x` is already a `mut T` or `mut []T`). Both kinds of capture borrow are formed at the call together with the `&p` and `mut p` operands (after plain arguments are evaluated), so R5 catches conflicting borrows: `xs.Sort(fn(a: &i32, b: &i32): bool { return xs.Len() > 1 })`, `xs.Sort(fn(a: &i32, b: &i32): bool { xs.Push(1) else {}; return *a < *b })`, and two `const` `fn` arguments mutating the same local are E0501.
- To mutate a captured binding or value (`x = v`, `mut x`, `x.MutMethod()`), `x` must be declared `var` (E0508), unless the literal only mutates through a captured `mut T` or `mut []T` (`*r = v`, `zs[i] = v`). Writing through a captured `&T` or `[]T` is E0507.
- Inside the literal, a captured variable is borrowed and cannot be moved out of (even a `var` linear or unique local): moving a non-plain capture is E0609.
- A capture is not a parameter of the literal, so no `from` or `into` clause can name it. A literal whose result could hold a loan on a captured name, that stores a loan on a captured name into one of its parameters, or that stores a non-static loan (from a parameter, another capture, or writable global memory) into a captured variable is E0609. Storing a literal-local loan into a captured variable is E0505.
- In a contract predicate or struct `invariant`, the names are the function's parameters and `result`, or the struct's fields through `self`, and are read-only (mutating a capture there is E0609).
- A bracket argument is never a `fn` literal (E0101). A literal in a `fields` loop body may not capture a name that loop declares (E0609).
- No result of the call holds a capture loan, and the capture loans — shared and exclusive alike — end with the call's statement (R14).

```mk
// verdict: ok
CountSorted(xs: mut []i32): int {
    var compares := 0
    xs.Sort(fn(a: &i32, b: &i32): bool {
        compares += 1
        return *a < *b
    })
    return compares
}
```

```mk
// verdict: reject E0609
Visit(a: &Arena, const f: fn(out: mut List[&i32]): OutOfMemory!void): OutOfMemory!int {
    var l := List[&i32].New(a)
    try f(mut l)
    return l.Len()
}

Collect(a: &Arena, x: i32): OutOfMemory!int {
    return try Visit(a, fn(out: mut List[&i32]): OutOfMemory!void {
        try out.Push(&x) // <- E0609
    })
}
```

Fix: collect values, not references to captured names (a `List[i32]` and `out.Push(x)`), or write a loop instead of a callback.

```mk
// verdict: reject E0609
WithRef(const f: fn(&i32)) {
    n: i32 = 1
    f(&n)
}

KeepRef() {
    var best: ?&i32 = none
    WithRef(fn(p: &i32) {
        best = some(p) // <- E0609
    })
    _ = best
}
```

Fix: store a plain value (`var best: ?i32 = none` and `best = some(*p)`), or write a loop where the owner's scope is visible.

### R14. Temporaries

A temporary lives to the end of its statement. Nothing may keep a loan on it past that point. Why: Mk never extends a temporary's life silently, so where a value lives is visible in the code. All the locals of a block end together, after the block's `defer`s run.

```mk
// verdict: ok
Passes() {
    Use(&MakePoint()) // the temporary lives for this statement
}
```

```mk
// verdict: reject E0506
TooLong() {
    r := &MakePoint() // <- E0506
    Use(r)
}
```

Fix: bind the temporary to a local, `p := MakePoint()`, then borrow `&p`.

- For `if` and `match`, the statement is the whole construct.
- For `for`, the value iterated over, an `ItemsMut()` result included, lives for the whole loop (it holds R20's one loan). Every other temporary of the header or the body lives one iteration.
- A linear temporary must be consumed within its statement.
- The capture loans of R13 — both shared and exclusive — are temporaries of their call's statement, so a mutably-captured local is usable again on the next statement.

### R15. Reassigning a reference ends loans through it

Assigning to a reference `r` ends every loan whose place begins with `*r`. Those loans named memory through the old value of `r`, and the name now means something else. Why: this is what lets a cursor walk a linked list, which Rust's checker rejects.

```mk
// verdict: ok
struct Node {
    value: i32
    link: ?mut Node
}

Node.Next(mut self): ?mut Node {
    if n := mut self.link {
        return some(mut **n)
    }
    return none
}

Last(head: mut Node): mut Node {
    var cur := head
    for {
        next := cur.Next() // borrows through cur
        if n := next {
            cur = n // ends the loans through the old cur
        } else {
            return cur
        }
    }
}
```

The old target stays protected, because a reborrow also holds what `r` held. Only loans under `*r` end. A loan on `r` itself does not:

```mk
// verdict: reject E0502
Pinned(a: &i32, b: &i32) {
    var r := a
    q := &r
    r = b // <- E0502
    Use(q)
}
```

Fix: use `q` before reassigning `r`. Moving `r` while a loan under `*r` is live is still E0503.

### R16. Globals

A global `var` is shared by every task, so its type must be safe to share, and it may never hold a reference to anything shorter-lived than the program. Why: a global outlives every frame, and every task can reach it.

```mk
// verdict: ok
const Limit: i32 = 10

Reads(): &i32 {
    return &Limit
}
```

```mk
// verdict: reject E0605
var Latest: Mutex[?&i32] = Mutex.New(none)

Stash(io: Io): Canceled!void {
    n: i32 = 1
    var g := try Latest.Lock(io)
    defer g.Unlock()
    g.Store(some(&n)) // <- E0605
}
```

Fix: store a value, not a reference to a local: `Mutex[?i32]` and `g.Store(some(n))`.

| Rule | Detail |
|---|---|
| `const` | A compile-time value. Its type must be plain. `&Limit` is valid |
| `var` | One instance. Its type must be `Share` (E1002). It is never assigned, moved or borrowed `mut`. It is reached only through `&` |
| Static pseudo-loan | A reference to a global holds it. It never conflicts. Storing a value into anything reachable from it needs the value to hold no loan but the static one (E0605) |

```mk
// verdict: reject E1002
var Counter: Cell[i32] = Cell.New(0) // <- E1002
```

Fix: a `Cell` is not `Share`; use an `atomic.Atomic[i32]` from `std.atomic`, or a `Mutex[i32]`.

**Global memory reached through a call.** A function may return a reference to global memory that takes stores, such as a global `Mutex`. Its caller must not store a loan on a local through it, so the result holds the static pseudo-loan:

```mk
// verdict: reject E0605
var Latest: Mutex[?&i32] = Mutex.New(none)

Get(): &Mutex[?&i32] {
    return &Latest
}

Stash(io: Io): Canceled!void {
    m := Get()
    n: i32 = 1
    var g := try m.Lock(io)
    defer g.Unlock()
    g.Store(some(&n)) // <- E0605
}
```

A `mut view` (including `Arena` and `Scope`), a `Cell`, `Mutex`, `Chan` or `atomic.Spin` whose payload holds loans, or a type holding one inline (including a plain `view` with such a type argument), **takes stores**: it can accept a loan stored through a shared reference. A `Cell[T]`, `Mutex[T]`, `Chan[T]` or `atomic.Spin[T]` whose payload `T` holds no loans (such as `Cell[int]` or `Mutex[fs.File]`), and `std.raw.Atomic[T]`, still mutate data through `&` (they are not `Freeze`), but cannot store loans and do not take loan stores here. A result type matters here when a reference in it, at any depth, reaches such memory. Every level counts: a global `sync.Once[&Mutex[?&i32]]` has its payload written at run time.

The rules:

- A carrier result holds the static pseudo-loan when its type can reach memory that takes stores and either its signature names no source (by R9 only statics qualify then), or it comes from a `fn` value or an interface method, which may stand for such a function.
- The same holds for a result that holds inline a `mut view` (other than `Scope`, whose storage lives inline on its owner's stack frame), or a plain `view` whose type arguments hold loans (at the first level) or take stores (behind a shared reference).
- A raw-pointer parameter (`Io` does not count) exempts a direct callee only.
- A result whose signature names a source, or that has a raw-pointer parameter, may not hold the static pseudo-loan of such memory (E0602).
- Storing such a static into a parameter whose type may point into such memory is E0604, since no clause can name a static.
- A reference read through a reference that holds a global's static pseudo-loan keeps that loan, when the global's type holds a reference.

```mk
// verdict: reject E0602
var Latest: Mutex[?&i32] = Mutex.New(none)

Pick(d: &Mutex[?&i32]): &Mutex[?&i32] {
    _ = d
    return &Latest // <- E0602
}
```

Fix: return `d`, as the signature promises, or drop the parameter so that the result has no source and holds the static pseudo-loan.

The walk over what a parameter refers to stops after more than 64 distinct instances of one generic type, or 4,096 referents in all, and from there takes the parameter to reach any type, as it must for a polymorphically recursive type. What a referent past the stop takes stays with the parameter, so the bound changes no verdict. When the only memory behind a parameter that takes stores is a referent past the stop, the E0604 for a static stored into it names the bound (`bound: more than 64 instances of `B``), so the reader can tell which type takes the store.

In a generic body, a result is also judged for its instances:

- These results count when an instance could take stores: a `&T`, `?&T` or `T` from a `fn` value or from a callee with no source, and the bare `T` of an interface method that names no source.
- A `Plain` bound exempts no type parameter, since `&T` is plain.
- A `fn` type that names a source is trusted when one of its parameters holds the result's type. It may hold it directly, behind references, slices, optionals or arrays, or in the visible fields of a struct, record or enum that is not a `view`: `fn(T): T`, `fn(x: &T): &T`.
- An interface method's bare `T`, `&U` or `[]U` is judged at the impl when the method names a source, as every method with a receiver does.
- A concrete type is judged as written.

Which globals take stores:

- A named global takes stores when its first level does, or when a reference in its bytes reaches, at any level, memory that does. A global `Box { m: &Mutex[?&i32] }` whose `Mutex` its initializer put in compile-time memory takes stores: a store through its guard is E0605.
- A `const` of another module is judged by its value: it takes stores only when a reference in the value reaches memory that does. A `none` or an empty slice points nowhere.
- A `const` of the same module is judged by its type, since its value is not known while that module's bodies are checked.

These rules refuse some sound programs:

- A copy of a same-module `const` returned under a source is E0602 when the `const`'s type could take stores, even where its value points nowhere, as in `const D: L = L(d: [], p: none)` with a field `p: ?&MutView`. Move the `const` to another module, or return a fresh value.
- A `const` of another module whose value holds a reference to a `mut view` counts by the `mut view`'s type even when none of its methods writes.
- A generic body that returns or stores a `fn` value's `T` under a source is E0602 or E0604: `OrElse[T](x: ?T, f: fn(): T): T`, a `Map` adapter, `Apply[T, U](f: fn(x: &T): U, x: &T): U`, or an out-parameter `Fill[T](out: mut ?&T, ...)`.

A `view`'s raw pointer must reach only memory its signatures name, and a `view` whose `unsafe` code writes loans or hands out loan-interior-writable storage through `&self` must be declared `mut view struct` (R18, R19). An `unsafe` module that breaks either obligation breaks its `unsafe` blocks' duty, and no rule checks that.

### R17. Tasks

A task started with `s.go` may borrow from its parent, because the `scope` block waits for every task before its loans end. Values that cross into a task must be `Send`, and values that two tasks share must be `Share`. Why: this rules out data races and dangling references to a parent's stack.

```mk
// verdict: ok
Sum(xs: []i32, out: mut i64) {
    for x in xs {
        *out += i64(x)
    }
}

Parallel(io: Io, a: []i32, b: []i32): i64 {
    var left: i64 = 0
    var right: i64 = 0
    scope s := io.Scope() {
        s.go Sum(a, mut left) // borrows a and left until the scope ends
        s.go Sum(b, mut right)
    }
    return left + right // both tasks have finished here
}
```

```mk
// verdict: reject E1002
Count(c: &Cell[i32]) {
    c.Store(c.Load() + 1)
}

Racy(io: Io) {
    c := Cell[i32].New(0)
    scope s := io.Scope() {
        s.go Count(&c) // <- E1002
    }
}
```

Fix: a `Cell` is not `Share`, so `&Cell` cannot reach another task. Use an `atomic.Atomic[i32]`, or a `Mutex[i32]`.

| Rule | Detail |
|---|---|
| Spawn | `s.go F(args)` acts as a call with every argument `into s`. Each argument type must be `Send`. A task always starts; it sees cancellation at its first cancellable operation. A task's clauses hold until the join: for `*q into p`, a later store into what `q` points at, or into what `p` reaches, also reaches `p`; for `x into p`, a later store into what `p` reaches receives `x` too. A call an argument of which reaches a `Scope` holds its callee's clauses the same way, since a task it spawns is bound by them |
| Scope | `Scope` is a `view` and is `Share`. It exists only as the binding of a `scope` statement (E1003) |
| Exit order | `defer`s and `onerr`s run, then the tasks are cancelled if the exit is early, then joined, then the scope's loans end, then locals end. The join waits until no task remains, including tasks spawned during it. `s.go` in a `defer` of that block is E1003 |
| Helpers | A helper takes `s: &Scope`, and its arguments need `into s`. `&s` may be passed to a task. A spawn writes only the scope's own memory, so a helper may spawn the same arguments again |
| `Send` | Holds no `&C` with `C` not `Share`, and no raw pointer |
| `Share` | Holds no `Cell`, and no raw pointer |
| Assertions | `unsafe impl[T: Send] Share for Mutex[T]`. An assertion can carry bounds |
| `*T` | Neither `Send` nor `Share`. A type built on one asserts them, or stays out of tasks |

Allocators and tasks:

- `Alloc` is an erased handle that asserts nothing, so it does not cross tasks.
- `Heap` is `Share`.
- `Arena` is neither `Send` nor `Share`. It calls its chunk source itself, and that source may belong to one task.

So a task takes `&Heap` and makes its own `Arena`:

```mk
// verdict: reject E1001
Fill(a: Arena) {
    a.Free()
}

Own(h: &Heap) {
    var a := Arena.New(h) else {
        return
    }
    a.Free()
}

Spawn(io: Io, h: &Heap) {
    var a := Arena.New(h) else {
        return
    }
    scope s := io.Scope() {
        s.go Fill(a) // <- E1001
        s.go Own(h)
    }
}
```

Fix: spawn `Own(h)`, which makes its arena inside the task, and free `a` in the parent.

The parent may not touch what a task borrowed until the scope ends:

```mk
// verdict: reject E0504
Bump(out: mut i32) {
    *out += 1
}

Peek(io: Io) {
    var n: i32 = 0
    scope s := io.Scope() {
        s.go Bump(mut n)
        Use(&n) // <- E0504
    }
}
```

Fix: read `n` after the `scope` block. A helper that spawns must declare that its arguments go into the scope:

```mk
// verdict: reject E0604
Bump(out: mut i32) {
    *out += 1
}

Helper(s: &Scope, x: mut i32) {
    s.go Bump(x) // <- E0604
}
```

Fix: `Helper(s: &Scope, x: mut i32 into s)`.

A task runs until the join, so its `*q into p` is not applied once at the spawn. A value stored into the channel after the reader starts may still be received and stored into `*p`:

```mk
// verdict: reject E0505
Receive(ch: &Chan[&i32], io: Io, out: mut &i32) *ch into out {
    v := ch.Recv(io) else {
        return
    }
    *out = v
}

Late(io: Io, h: &Heap, p: mut &i32): OutOfMemory!void {
    short: i32 = 22
    ch := try Chan[&i32].New(h.Alloc(), 1)
    scope s := io.Scope() {
        s.go Receive(&ch, io, p)
        ch.Send(io, &short) else {} // <- E0505
    }
    ch.Free(h.Alloc())
}
```

Fix: send only what may live in `*p`, such as a value the caller declares `into p`:

```mk
// verdict: ok
Receive(ch: &Chan[&i32], io: Io, out: mut &i32) *ch into out {
    v := ch.Recv(io) else {
        return
    }
    *out = v
}

Late(io: Io, h: &Heap, p: mut &i32, v: &i32 into p): OutOfMemory!void {
    ch := try Chan[&i32].New(h.Alloc(), 1)
    scope s := io.Scope() {
        s.go Receive(&ch, io, p)
        ch.Send(io, v) else {}
    }
    ch.Free(h.Alloc())
}
```

### R18. `view` and `ends`

A `view` type is a declared carrier:

- A **`plain view struct`** is bitwise copyable (`Kind == Plain`, `Carrier == true`, `Freeze == true`) — the user-defined counterpart of `&T`, `[]T` and `str` for copyable index or slice handles such as `plain view struct Ref[T] { idx: u32 }` or `SpanView[T] { ptr: []T }`. Every field must be `Plain` and `Freeze` under its own generic bounds (E0408), so phantom handles stay `Plain + Freeze` even when `T` is unique or linear; and `ends` and holder `into` clauses on by-value or `&` parameters (`self`, `&self`) are forbidden (E0507), while `mut PlainView` allows `into` like `mut []T`.
- A **read-only `view struct`** (without `plain` or `mut`) is unique and read-only for loans through `&`: it borrows (such as `List`, `Map`, `Text`, `Box` or `Iter`), and takes loan stores through `&` only if one of its type arguments does (such as `Box[Mutex[?&i32]]`).
- A **`mut view struct`** additionally declares that it takes loan stores through `&` (such as `Arena`, `Scope`, `ErasedBox`, `dyn.Ref`, or a custom allocator), is unique, and is not `Freeze`.

You need `plain view`, `view` or `mut view` in two cases. A struct built on raw pointers has no reference field to show that it borrows (or writes loans through `&`), and a copyable index handle has no reference field to bind it to its backing arena or pool. An exported struct whose private fields make it a `Plain` and `Freeze` carrier must say `plain view` (or `view`), one whose private fields make it unique or a raw-pointer carrier must say `view`, one whose private fields take loan stores through `&` must say `mut view`, and one they make linear must say `linear` (E0410). Why: callers must see from the declaration alone whether a value borrows, copies, or takes loan stores through `&`.

```mk
// verdict: parse
view struct Iter[T] {
    ptr: *T
    end: *T
}
```

- A raw-pointer field of a `view` may be named in `from p.f`, as `Shared.Clone` does, and the `unsafe` code behind it answers for that.
- What a view keeps in raw storage is its rest when it splits (R7).
- A view on raw pointers takes its allocator as `&Heap`, never as `&Alloc` or `&Arena`. `from p.f` gives the result `B(p)`, which can step past the loan on the allocator that holds the storage, so a clone of an arena-backed value could outlive `Reset`.

**Exclusive views from one receiver.** Some library functions hand out exclusive views that the rules cannot prove disjoint:

- `Slice.SplitAt` gives two halves;
- `Slice.GetPair` and `Pool.GetPair` give two elements;
- `ChunksMut.Next` gives one piece while the iterator keeps the rest;
- `Arena.Put` gives fresh storage under a shared loan on its arena.

Each result holds the loan its receiver held (`from self`, or `from self.ptr` for a `view`), so nothing else touches the receiver while a view is live. The library's `unsafe` code promises the rest. No two live exclusive views such a function hands out share a byte, and none shares a byte with what another live loan on the receiver covers. So `GetPair` returns `none` rather than two views of one element, `SplitAt` and `ChunksMut` cut at element boundaries, and `Arena.Put` returns storage no earlier result reaches. A function that breaks this is a library bug, not a hole in the rules.

**`ends`.** `ends self` on a function means that the call ends everything borrowed from `self`. It is how a cyclic graph in an arena is released or reset.

```mk
// verdict: parse
Arena.Free(self) ends self
Arena.Reset(mut self) ends self
```

- At the call, a loan on `self` whose every live holder is `self` itself does not conflict.
- Afterwards `self` holds only what the call itself stored into it, through `x into self` or `*q into self`. On an error edge nothing is ended.
- A primitive that uses `ends` must be in a module permitted `unsafe`. Any other function may declare `ends p` if it calls an `ends` function on `p` on every path, so a reset can be wrapped.

```mk
// verdict: ok
Reuse(h: &Heap): OutOfMemory!int {
    var a := try Arena.New(h)
    defer a.Free()
    s := try a.Dup("first")
    n := s.Len()
    a.Reset() // s is dead: nothing but a holds a loan on a
    t := try a.Dup("second")
    return n + t.Len()
}
```

A value outside the arena that still holds a loan on it blocks the reset:

```mk
// verdict: reject E0501
Stale(h: &Heap): OutOfMemory!int {
    var a := try Arena.New(h)
    defer a.Free()
    s := try a.Dup("first")
    a.Reset() // <- E0501
    return s.Len()
}
```

Fix: finish with `s` before `Reset`, as `Reuse` does. No other exemption exists. A value holding a loan on itself cannot move:

```mk
// verdict: reject E0503
struct Tie {
    a: Cell[?&Tie]
}

Moves() {
    t := Tie(a: Cell.New(none))
    t.a.Store(some(&t))
    Take(t) // <- E0503
}
```

Fix: do not move it. Build such a structure in an arena, take the arena as a parameter, and release it with `ends`.

### R19. `unsafe`

Raw pointers hold no loans, and a reference made from one holds nothing. The `unsafe` code that makes it must tie it down with a signature, and its author carries the proof. Why: the compiler cannot see where a raw pointer points, so a person must state it.

```mk
// verdict: reject E1103
Peek(p: *i32): i32 {
    return *p // <- E1103
}
```

Fix: dereference inside an `unsafe` block with a reason, in a module permitted `unsafe` by its manifest:

```mk
// verdict: ok
Peek(p: *i32): i32 {
    unsafe "p comes from a live buffer owned by the caller" {
        return *p
    }
}
```

- A reference made from a raw pointer, or one that C passes in, still obeys the loan rules a checked reference of its kind would. An access those rules forbid is undefined behaviour however the references were made, for example two references to a common byte passed to one call, one of them `mut` ([the reference](reference.md#14-unsafe-and-c)).
- A body that writes through raw pointers, and a bodiless `extern`, may do only what a checked body with the same signature may do. Its clauses are its contract, and a store they do not declare breaks it.
- `unsafe` needs a reason string, and its module needs permission in the manifest.

Five duties are worth naming:

- **Interior-writable views.** A `view` whose `unsafe` code writes loans through `&self` behind raw pointers, or hands out loan-interior-writable storage (such as `&Mutex[?&i32]` or `&Alloc`) from raw pointers through `&self`, must be declared `mut view struct` (R18). A read-only `view struct` promises that `&self` cannot accept loan stores except through its type arguments, and a `plain view struct` holds only `Plain`, `Freeze` fields with no `ends` or shared `into`.
- **Borrowed exclusive views.** A read-only `view struct` whose raw pointer borrows external storage exclusively for writing through `mut self` (such as `Guard[T]`, `SpinGuard[T]` or `ChunksMut[T]`) must carry a `[0]mut T` marker field or be declared `mut view struct`. Without an exclusive marker, passing the view by value looks like moving an owning container (`List[T]`, `Box[T]`) rather than borrowing mutable external memory.
- **Type-erased views.** A non-generic read-only `view struct` that erases a carrier behind raw pointers must not return that carrier to callers (even through `mut self` or consuming `self`), because a non-generic read-only view carries no type argument for the borrow checker to walk. A type-erased box that hands its value back must be declared `mut view struct` (`ErasedBox`, `dyn.Ref`); a read-only `view struct` like `core.SealedBox` may expose its contents only to zero-flow callbacks returning `void`.
- **Fresh memory.** An allocator method that returns `mut T from self` with a shared `self`, such as `Arena.Put` and `Arena.Slice`, hands out an exclusive reference backed by a shared loan, so two results are usable at once. The loan rules do not check that they point at disjoint storage; the `unsafe` body does, by returning fresh memory every time. A method on `&Arena` that returned a `mut` reference to existing contents would be unsound, and the rules would not catch it.
- **Stores into owned memory.** Memory that a by-value parameter owns, as a `Box` owns its value, may take a store with no `into` clause when two things hold. The body moves that parameter into the result and nowhere else, and the result is a carrier `from` both that parameter and the stored value. `ErasedBox.Fill(b: Box[?T], x: T): ErasedBox from b, x` writes `x` into the memory `b` owns. `b` is consumed, so only the result reaches that memory afterwards, and the result holds `x`'s loans beside `b`'s. Both sources are needed: with the result `from b` alone, `x`'s loans could end while the box still holds `x`. A checked body cannot make this store (E0604), so only `unsafe` code uses it. `Box[T].New(a: &Alloc, x: T): OutOfMemory!Box[T] from a, x` works the same way.

`asm` is checked as a call of a bodiless `extern`:

- Each `in` operand is a read of its value and ends no loan.
- Each `out` place is assigned as by `=`, so it must be assignable (E0404 or E0508), and a place under a live loan is E0502.
- The statement creates no loan and ends none.
- Its operands are plain values and raw pointers only. What it does through memory is the raw-pointer case above: writing from `asm` to a place covered by a live loan is undefined behaviour.
- Without `nomem` or `readonly`, the compiler assumes it reads and writes all memory its pointers reach.

`extern var` and `std.mmio` are raw-pointer access too: they hold no loans, and their reads and writes are the author's to prove.

**What unsafe code may assume and must keep.**

- **The floating-point environment.** Every thread runs Mk code with round-to-nearest-even, no flush to zero and no denormals-are-zero, and no Mk code changes it, compile-time code included. C or `asm` that changes the environment restores it before Mk code runs again. Mk code that runs under another environment is undefined behaviour.
- **NaN.** An arithmetic operation that produces NaN produces some quiet NaN with an unspecified sign and payload. `Bits` reads whatever it is, and two runs, two builds, or compile time and run time may differ. A NaN that a `std.math` function computes has `math.Nan`'s bits.
- **Mixed widths.** Atomic accesses of different widths to overlapping bytes are undefined behaviour: an `Atomic[u32]` and an `Atomic[u64]` over one word, or a `raw` atomic of one width over another's bytes.
- **`Unordered`.** `atomic.Unordered` is LLVM's unordered order, which the compiler emits as monotonic. A load sees some value a store wrote, never a torn one, and nothing is ordered by it. It is an order for a load or a store only; a fence or a read-modify-write rejects it, as they reject `Acquire` on a store (E0706).
- **Element-wise atomic copy and fill.** `raw.AtomicCopy` and `raw.AtomicFill` access each element by one relaxed access, so a reader that races them sees each element old or new, never torn ([std.raw](std/raw.md)).
- **Spinning.** `atomic.Spin` yields the OS thread, never the task: see [Spin](std/atomic.md#spin).
- **Waits and wakes on a word.** `Atomic[T].Wait` may return early, so unsafe code that waits on a word rechecks it after each `Wait` ([waiting for a word to change](std/atomic.md#waiting-for-a-word-to-change)).
- **A bad order on a `std.raw` atomic.** An order constant that the operation cannot take is E0706 at the call, as on `std.atomic`'s methods.
- **Compile time.** Compile-time code runs on one thread, so each of these has its single-threaded meaning there. Where there is none, such as a fault filter or a code region, the call is E0901 with a reason.

Mk has no race detector.

### R20. Disjoint elements

Each iteration of `for x in mut xs` gets a different element, so the element loans of different iterations do not conflict, and `x` may be kept. The loop holds one exclusive loan on `xs` until it exits, so any other use of `xs` inside it conflicts. Why: iteration is the one place where the compiler knows two indexes differ.

```mk
// verdict: ok
Bump(x: mut i32) {
    *x += 1
}

All(io: Io, xs: mut []i32) {
    scope s := io.Scope() {
        for x in mut xs {
            s.go Bump(x) // each task gets its own element
        }
    }
}
```

```mk
// verdict: reject E0501
Reverse(xs: mut []i32) {
    for x in mut xs {
        *x += 1
        xs.Swap(0, 1) // <- E0501
    }
}
```

Fix: loop over indexes, `for i in 0..xs.Len()`, when the body must reach other elements.

- Entering the loop is a `mut xs` access, checked against every live loan, including elements kept from an earlier run of the same loop.
- All other indexing follows R4.
- For a `Slice` implementer, `for x in mut list` means `for x in mut list.ItemsMut()`, and that call's result lasts for the loop.

### R21. `onerr` and exits

`defer` runs its body at every exit of the block. `onerr` runs its body only at an error exit. Together they are how a linear value is released on every path. Why: Mk has no destructors, so cleanup is code you can see.

```mk
// verdict: ok
Build(h: &Heap): OutOfMemory!Arena {
    var a := try Arena.New(h)
    onerr a.Free() // runs only if a later try fails
    _ = try a.Dup("first")
    return a // success: the caller owns the arena
}
```

A file follows the same shape, as in the reference's [`Save`](reference.md#cleanup-on-the-error-path-onerr): `onerr f.Abandon()` after the file is made, then `try f.Close(io)` last. `Close` consumes `f` only on success (R2), so its failure edge runs the `onerr` too, and so does every earlier `try`. A failed `Close` leaves the file open and owned. If the final OS close itself fails, the handle is already marked released, and `Abandon` does nothing.

```mk
// verdict: reject E0807
error Full

Step(): Full!void {
    if Cond() {
        fail Full
    }
}

Two(): Full!void {
    a := Acquire("a")
    b := Acquire("b")
    onerr { // <- E0807
        a.Close()
        b.Close()
    }
    try Step()
    a.Close()
    b.Close()
}
```

Fix: one `onerr` per value, `onerr a.Close()` and `onerr b.Close()`.

- An **error exit** is a `try` edge, a `fail`, or a `break` or `continue` that leaves the `onerr`'s block from inside an `else` handler. A `return` is never one, even of an error value.
- R2's question is different: which exits produce an error. Those are a `fail`, a `try` error edge (also inside an `else`), and a `return` of an `E!T` value.
- An `onerr` body may consume one linear value at most (E0807). It is skipped at an exit where that value has been moved.
- `defer` and `onerr` bodies run in reverse order of declaration, as one stack.
- A `defer` is never skipped: moving a value that a `defer` uses is E0406 at the move.
- A `defer` or `onerr` body cannot leave itself by `try`, `fail`, `return`, or a `break` or `continue` to an outer loop (E0806).

## 4. What each operation does to loans

This section collects, in one place, what each construct does to the loans a value holds and which access it checks. Section 3 explains the same rules with examples. The state at each point of a function is, for each local, whether it is assigned and the set of loans it holds, one set per field for a split value (R7). Where paths join, the sets are unioned. A linear value consumed on one path and not on another is E0402.

| Construct | Loans afterwards | Access checked |
|---|---|---|
| `x := e`, `x = e` on a local | `x` holds what `e` holds, field by field when `e` is split (R7). Loans through the old `*x` end (R15) | Write to `x`, shallow |
| `p.f = e` on a local | `p` also holds what `e` holds; on a split `p`, `f` takes `e`'s set. Loans through the old `*p.f` end | Write to `p.f`, shallow |
| `&p`, no dereference in `p` | A new shared loan on `p` | Read of `p`, deep |
| `mut p`, no dereference in `p` | A new exclusive loan on `p` | `mut` of `p`, deep |
| `&*r`, `&r.f`, `r` shared | No new loan: the result holds what `r` holds | Read |
| `&*r`, `&r.f`, `r` exclusive | A new shared loan, plus what `r` holds | Read |
| `mut *r`, `mut r.f`, `r` exclusive | A new exclusive loan, plus what `r` holds | `mut` |
| `mut *r`, `mut r.f`, a shared step on the path | E0507 | |
| `&*q`, `q` a shared reference read through `r` | As reading `q`: `B(r)` | Read |
| `&*q` or `mut *q`, `q` an exclusive reference stored behind `r` | A new loan, plus `B(r)`, plus what every reference on the path holds | As a borrow |
| Slice of a `[]T` or `str` value | What the value holds | Read |
| `Slice` method on a `[]T` or `mut []T` value | The receiver is that value, with no borrow of the local holding it | As the parameter |
| Slice of a `mut []T` value | As `&*r`, or `mut *r` for a `mut` slice | As a borrow |
| Slice of an array `[N]T` | As `&p` or `mut p` of the array: a borrow of it | As a borrow |
| Index of a `[]T` or `mut []T` value | As a dereference of the slice, on the place `r[_]`. `s[i]` on a `str` is E0308: read a byte with `str.Byte(i)`, or slice it | As a borrow |
| Index of a `Slice` implementer, `list[i]` | As `list.Items()[i]`, or `list.ItemsMut()[i]` where the element is written or borrowed `mut`: the loan is on the container | As the call |
| Read `*r`, `r.f` through a reference | `B(r)` (R8). Unique or linear: E0409 | Read, deep |
| Read `s.f` on a local | What `s` holds, or `f`'s set when `s` is split. Unique or linear: E0409 | Read |
| Move out of `x` | The value takes what `x` held, and `x` is moved | Move, deep |
| Write through a reference | Writes through references, below. Loans through the old value end | Write, shallow |
| Call | Calls, below | Each argument |
| `asm(...)` | Each `in` operand is read as a call argument; each output place is assigned a value that holds no loans | Read of each input, write of each output, shallow |
| A `fn` literal as a `const` argument, naming an enclosing `x` | `&x`, formed with the other borrows of the call (R13), held until the statement ends (R14) | Read of `x`, deep |
| `return e` | Return check, below | |
| `if x := e`, `for x := e` | `x` takes `e`'s set on the taken edge | Move or copy |
| `if x := mut e` | `x` is a `mut` borrow of the payload | `mut` |
| `match e`, `&e`, `mut e` | Bindings take sets by the form matched | By the form |
| `for x in xs`, `xs` a slice value | `x` is a copy of the element and holds what reading it gives (R8). `T` must be plain, else E0409 | Read |
| `for x in &xs`, `xs` a slice value | `x` is `&T` and holds what `xs` holds | Read |
| `for x in &list`, `list` a `Slice` implementer | One shared loan for the loop. `for x in list` copies plain elements under the same loan | Read |
| `for x in mut xs` | `x` is `mut T` (R20) | `mut` of `xs` |
| `defer`, `onerr` | Code that runs at each exit (R21) | At each exit |
| `scope s := e { }` | `s` cannot move. Exit order in R17 | |
| `s.go F(args)` | A call with every argument `into s`. The task's clauses hold until the join: after a later store into what `q` points at, or into what `p` reaches, `*q into p` and `x into p` apply again. So do a call's clauses when an argument reaches a `Scope` | `Send` |
| End of `x`'s scope | `x` is dead | Scope end, shallow |

A **shallow** access, a write or a scope end, conflicts with loans on the place and its fields but not with loans on what it points at, because a reference's target outlives the reference (R5). A **deep** access conflicts with loans on anything under the place.

**Writes through references.** For `*r = e`, `r.f = e`, or a longer path:

1. A shared step on the path is E0507.
2. The compiler follows the path through what each reference holds, to the memory the write lands in.
3. A write that lands in a `view`, or in a value through which more memory can be written (a `mut` reference at any depth, a type parameter, or a shared reference to a type that takes loan stores: a `mut view`, `Arena`, `Scope`, or a `Cell`, `Mutex`, `Chan` or `atomic.Spin` whose payload holds loans), also reaches what that value holds. The walk stops at an allocator: the store goes into the allocator's memory, as a `Push` does (R11).
4. Where the write lands:

| Lands in | Result |
|---|---|
| A local | The local also holds what `e` holds |
| What a parameter `p` points at | At the first level, `e`'s loans must already be held there, or be declared `into p`. Deeper, only a declared `into` counts. Otherwise E0604. A loan on a local is E0505 |
| A global | `e` may hold only the static pseudo-loan, else E0605 (R16) |

A write into a field, variant or array element of a parameter that neither is nor holds a non-`plain` view counts as the first level. A type parameter there counts as a possible unique view unless it is bounded by `Plain`, since every non-`plain` view is unique (R1). So a generic `Pair[T: Plain].Dup(mut self)` writing `self.b = self.a`, a `TwoSyms` writing `self.a = self.b` on `plain view struct Sym`, or `Rev[T: Plain](xs: mut []T)` writing `xs[i] = xs[j]`, needs no clause.

`Get` returns `mut *s.m`, so its result holds the loan on the local `sv`. The type of `sv` holds a `mut` reference, so a write through the result lands on what `x` points at, which outlives `n`:

```mk
// verdict: reject E0505
struct S {
    m: mut ?&i32
}

Get(s: mut S): mut ?&i32 {
    return mut *s.m
}

Stash(x: mut ?&i32) {
    var sv := S(m: x)
    p := Get(mut sv)
    n: i32 = 5
    *p = some(&n) // <- E0505
}
```

A reference declared `into x` may be written there the same way:

```mk
// verdict: ok
struct S {
    m: mut ?&i32
}

Get(s: mut S): mut ?&i32 {
    return mut *s.m
}

Keep(x: mut ?&i32, k: &i32 into x) {
    var sv := S(m: x)
    p := Get(mut sv)
    *p = some(k)
}
```

The rule refuses some sound programs:

- It looks at the type of the whole local, not the part the write selects. With `ctx: Ctx { out: mut ?&i32, cur: ?&i32 }` and `q := mut ctx`, the write `q.cur = some(&n)` counts as a store into what `out` points at too, and is E0505, although it writes only the local. Write `ctx.cur` directly instead of through a reborrow.
- A value written into a `List` element by `l[i] = x`, `l.ItemsMut()[i] = x` or `*l.GetMut(i) = x`, or through a `Shared` clone's guard, keeps the loans of what it views. The arena then holds the loan until it is freed, so a `Box` stored that way cannot be taken back while the arena lives, even after the element's last use. `Push` has the same cost.

**Calls**, in order:

| Step | Action |
|---|---|
| 1 | Evaluate the arguments (R13) |
| 2 | Bind each parameter to what its argument holds |
| 3 | For each `x into p`: everything writable that `p`'s argument reaches also holds what `x`'s argument holds (R11) |
| 4 | For each `*q into p`: the same, with what `q`'s argument points at. For a reference to an allocator, that leaves out the allocator's own loan. All clauses read their sets before any is written |
| 5 | The result: `from p` adds what `p`'s argument holds, `from p.f` what it points at, and an automatic source what R10 says, read after steps 3 and 4. A per-field clause does this for each field (R9) |
| 6 | For `ends p`: a loan on `p` whose every live holder is `p` itself does not conflict, and `p` then holds nothing |
| 7 | Temporaries end at the end of the statement |

**Return check on `return e`.** For each loan `e` holds:

- A loan from a parameter must be covered by the `from` clause or the automatic rule, else E0602. `from p` covers everything `p` holds and points at; `from p.f` covers what came through `f`.
- A loan on a local of the function is E0603.
- A loan taken through a reference is fine, because the reference it passed through is held too.
- With a per-field clause, a value built field by field (R9) is checked per field, and any other value against the sources of every loan-holding field.

At `return e` the order is: evaluate `e`, run the return check, run the `defer`s, then the value leaves.

## 5. Signatures at a glance

A signature is the whole contract between a function and its callers. These are the parts that concern memory:

| You write | It means | Rule |
|---|---|---|
| nothing | The types decide where a result comes from, and the function stores no argument into another | R9, R10, R11 |
| `: T from p` | The result borrows `p`, and `p` stays borrowed while the result lives | R10 |
| `: T from p.f` | The result came through the reference in `p.f`; `p` stays free | R10 |
| `: T from a, b` | The result may come from either | R9 |
| `: S from (x: a, y: b)` | Each field of the result comes from its own sources | R9 |
| `x: T into p` | The function may store `x` somewhere inside `p` | R11 |
| `&self into p` | The method may store `self` somewhere inside `p` | R11 |
| `*q into p`, after the parameters | The function may store what `q` points at somewhere inside `p` | R11 |
| `ends self` | The call ends every loan on `self` | R18 |
| `plain view struct` | The type is a `Plain`, `Freeze` carrier handle: copied on use, cannot hide loans behind raw pointers | R1, R18 |
| `view struct` | The type borrows, though no field shows it; unique, read-only for loans through `&` | R18 |
| `mut view struct` | A `view struct` that also accepts loan stores through `&` | R16, R18 |
| `linear struct` | Values of the type must be consumed | R1, R2 |

The same clauses appear in `fn` types and interface methods (R9). The compiler checks one function at a time and reads only signatures at calls. There are no lifetime variables, no variance and no inference across functions.

## 6. Hard cases

These are programs that memory models tend to get wrong, with Mk's verdict and the rule that decides it. The Rust column gives rustc's behaviour for the same program where known.

| ID | Case | Mk verdict | Rust | Why |
|---|---|---|---|---|
| H01 | NLL problem case 3: return a borrow from a lookup, insert on miss | Accept | Rejected by NLL, accepted by Polonius Alpha | The `none` edge holds no loan |
| H02 | Linked-list cursor, then write through the cursor | Accept | Rejected, including Polonius Alpha `conditional()` | R15 |
| H03 | `v.Push(v.Len())` | Accept | Accepted through two-phase borrows | R13 |
| H04 | `Swap(mut x, mut y)` on `&T` values | Accept, with `*a into b, *b into a` | Accept | R11 |
| H05 | Reference into a list, then `Push` | Reject E0501 | Reject | R5 |
| H06 | Iterate, and mutate the list in the loop | Reject E0501 | Reject | One loan for the loop |
| H07 | Keep every `x` from `for x in mut xs` | Accept | Accept with `iter_mut` | R20 |
| H08 | Two tokens from one `mut` lexer | Accept, no clause | Accept with an impl lifetime | R10 |
| H09 | Struct with two views, consuming one source while reading the other | Accept | Accept with two lifetimes | R7: a local struct holds one set per field |
| H10 | Self-reference in a struct, then move it | Reject E0503 | Reject | R18 |
| H11 | Cycle in an arena with `Cell`, then `Free` | Accept | Accept with `may_dangle` | `ends self` |
| H12 | Cycle in an arena, then `Reset` | Accept | Reject | `ends self` |
| H13 | Stack reference in a global `Mutex` | Reject E0605 | Reject | R16 |
| H14 | `&Cell` to two tasks | Reject E1002 | Reject | R17 |
| H15 | `mut` argument read in the same scope | Reject E0504 | Reject | R17 |
| H16 | `mut` argument read after the `scope` block | Accept | Accept | The scope held the loan |
| H17 | `Scope` bound outside a `scope` statement | Reject E1003 | N/A | R17 |
| H18 | Writing a short reference through `mut &T` | Reject E0604 | Reject by invariance | R11 |
| H19 | `Cell[&T]` written, read through an alias | Reject E0505 when the source dies | Reject by invariance | R6 |
| H20 | Aliased cells across a call with `into` | Reject E0505 when the source dies | Reject by invariance | R6: a call makes its stores before its result is read |
| H21 | Function value storing its argument | Reject unless the `fn` type says `into` | Higher-ranked trait bounds | Flows are part of `fn` types |
| H22 | Generic `Pop` with `T = &X`, twice | Accept | Accept | R9: a generic result borrows what the parameter points at |
| H23 | `if T is &X` returning `&self.f`, where the result's source is automatic for a type parameter (R9) | Reject E0602 | N/A | The branch is checked against the signature |
| H24 | `mut` field reached through `&` | Reject E0507 | Reject | R12 |
| H25 | Return a borrow of a by-value parameter's own field | Reject E0603 | Reject | A by-value parameter is a local |
| H26 | Return a view held by a by-value parameter | Accept | Accept | Value read, `from` covers it |
| H27 | Conditional move, then implicit drop | Reject E0402 | Accept with drop flags | Consume on both paths |
| H28 | Local declared before the value it borrows, released by `defer` | Reject E0505 | Accept with `may_dangle` | Locals end together after `defer`s, but the list's storage is the allocator's memory, which outlives the value |
| H29 | Early `try` with an unreleased linear value | Reject E0402 | Accept with `Drop` | Add `onerr` |
| H30 | Deep recursion on a task stack | Accept, panics at the limit | Accept, aborts on the guard page | Name a stack class, or iterate |
| H31 | `mut` reference held across a suspension | Accept | Accept | Stacks do not move |
| H32 | Reference to a parent's stack from a child task | Accept | Accept with `thread::scope` | `scope` joins |
| H33 | Linear value in a reference-counted cycle | Accept, leaks | Accept | No safety argument depends on release |
| H34 | Reference into an enum payload, then assign the enum | Reject E0502 | Reject | R5 |
| H35 | Binding that fails on `none` | Accept | Accept | No holder on that edge |
| H36 | Store behind a parameter with no clause | Reject E0604 | Reject | R11 |
| H37 | Store through a `mut` field of a local wrapper | Reject E0505 | Reject | R11, what a store reaches |
| H38 | Store into a `Mutex` parameter through a guard | Reject E0604 | Reject | The guard reaches what the parameter points at, so the store needs `into` |
| H39 | Local spawned from a helper | Reject E0505 | Reject | R17 |
| H40 | Read a `mut` field twice | Reject E0409 | Reject | R4 |
| H41 | `Cell` in a `const` or a `var` global | Reject E1002 | Reject | R16 |
| H42 | `Mutex[&Cell[T]]` to two tasks | Reject E1002 | Reject | `Share` needs `T: Send` |
| H43 | Copying a `Mutex` out of a reference | Reject E0409 | Reject | R1: asserted types are unique |
| H44 | Slicing an inline array, with `from` naming another field | Reject E0602 | Reject | An array slice is a borrow of the array (section 4) |
| H45 | Self-loan view returned from a constructor | Reject E0603 | Reject | Take the arena as a parameter |
| H46 | A store that changes type, `&p.x` from `p: &Point into c` | Reject E0505 when the source dies | Reject | R11 tests what can be written, not names |
| H47 | `Reset` with a live `Cell` pointing into the arena | Reject E0501 | Reject | `ends` exempts only `self` as holder |
| H48 | Keep elements across two runs of `for x in mut xs` | Reject E0501 | Reject | R20 entry check |
| H49 | `xs.Swap` inside `for x in mut xs` | Reject E0501 | Reject | R20 |
| H50 | `Cell[mut i32]` | Reject E0701 | N/A | `Cell[T: Plain]` |
| H51 | `Box` from an arena's `&Alloc`, used after `Free` | Reject E0503 | N/A | `Box` is a `view`, so it holds its allocator's loan |
| H52 | `File.Writer` used after `Close` | Reject E0503 | Reject | `from self, buf` |
| H53 | `fn(&i32): T` given a function returning its argument | Reject E0608 | N/A | State `from x` in the `fn` type |
| H54 | Struct with `[16]u8` and a `[]u8` field, returning a view of the field | Accept with `from self.src`. With no clause, the result borrows `self` | Accept | R10 |
| H55 | `from self.buf` where `buf` is an inline array | Reject E0607 | N/A | R10: an inline field means `from self` |
| H56 | `Map.Remove` on `Map[&K, &V]`, twice in a row with the results kept | Accept | Accept | R9: a generic result borrows what the parameter points at |
| H57 | Shared result read through a `mut` field, then a write through that field | Reject E0502 | Reject | R10: a `mut` field is own bytes |
| H58 | `?mut T` result, twice from one `mut` parameter | Reject E0501 | Reject | R10: `mut` at any depth |
| H59 | `?&str` from a `HeapList` field, list freed while the result lives | Reject E0503 | Reject | R10: a `view` field is own bytes |
| H60 | `&T` from `mut []T`, used after the array ends | Reject E0505 | Reject | R10: a slice is a reference to `T` |
| H61 | Result holding `str` and `&Lexer`, from `&Lexer` | Accept, the lexer stays borrowed | Accept | R10: any own-bytes referent decides |
| H62 | Private `[8]u8` on an exported struct, with a `str` result and no clause | Reject E0601 at the function | N/A | R10: a private change cannot reach a caller |
| H63 | `from w.text` on a by-value `w` | Reject E0607 | N/A | R10 |
| H64 | `from t.a` with `return t.b` | Reject E0602 | N/A | The field is checked |
| H65 | `*self.b = self.a` with no clause | Reject E0604 | Reject by invariance | R11 |
| H66 | Store through a guard into `Mutex[mut &i32]`, then the source ends | Reject E0505 | Reject | R11: a `view` restores writing |
| H67 | Two live `mut xs[0..]` from one `mut []T` | Reject E0501 | Reject | A `mut` slice is an exclusive borrow (section 4) |
| H68 | `List` from parameter `a: &Arena`, then `Push` of a parameter | Accept with `into` on the arena | Accept | R11: a store into the list is a store into the arena under it |
| H69 | `&*self.src` where `src: &Point`, no clause | Accept | Accept | As reading `self.src` |
| H70 | `Index.First` with `from self.words`, then `Index.Add` while the result lives | Accept | Accept | R10: a copy of a stored `str` |
| H71 | `Cell` fresh from `arena.Put`, `Set` with a local, then the local ends | Reject E0505 | Reject | R11: the argument holds the arena's loan directly |
| H72 | `*g.Mut() = &short` through a guard on a local `Mutex[&i32]`, then `short` ends | Reject E0505 | Reject | A write that lands in a `view` reaches what it holds (section 4) |
| H73 | `out[0] = v` on a `List` from a parameter arena, with `v into out` | Accept; its caller needs `v into` the arena | Accept | A write stops at an allocator (section 4). A call stores into the arena |
| H74 | `Shared.New(arena.Alloc(), x)`, `Clone`, `Free` the original, `Reset` the arena, read the clone | Reject E0301: `Shared.New` takes `&Heap` | Reject | R18: a view on raw pointers takes `&Heap` |
| H75 | Bounded `Push` of a linear `T` that fails with `Full` before moving the item | Accept, and the caller still owns the item on the error edge | Accept with `Result<(), T>` | R2: consume only on success |
| H76 | `Push` the item, then `fail` on a later check | Reject E0411 | Accept, the item is dropped | R2 |

## 7. Programs Rust accepts and Mk rejects, with mechanical rewrites

Some patterns that Rust accepts have no direct form in Mk. Each has a rewrite that does not change what the program does.

| ID | Rust program | Mk error | Rewrite |
|---|---|---|---|
| X01 | Struct with two lifetimes, sources consumed separately | None for a local struct or record, a `view` included (R7, H09); E0503 behind a reference | Split into two structs, or hold one view and pass the other |
| X03 | Conditional move with implicit drop | E0402 | Consume on both paths |
| X04 | Early return with implicit drop | E0402 | `defer` or `onerr` |
| X05 | `let x = &temp();`, an extended temporary | E0506 | Bind the temporary to a local |
| X06 | Implicit `Drop` at scope end | E0402 | `defer x.Close()` for an infallible release; `onerr x.Abandon()` plus `try x.Close(io)` for a fallible one |
| X07 | `fn get(&self, k: &str) -> &str`, where the types do not settle it | E0601 | Add `from self` |
| X08 | Closure capturing locals | E0609 | A struct plus a `fn`, or a loop |
| X09 | Iterator chains | E1202 | A `for` loop |
| X10 | Trait object with a lifetime | none | `gen dyn.Of(I)`: the boxed type is a view |
| X11 | Deep recursion in a task, relying on a large stack | Run time | Name a stack class, or iterate |
| X12 | Three distinct lifetime depths through one reference | E0503 or E0603 | Copy out, or hold a local reference |
| X14 | Partial move out of a struct | E0409 | Destructure the whole struct |
| X15 | View holding a loan on itself, moved or returned | E0503, E0603 | Take the arena as a parameter |
| X16 | Helper returning a stored reference from a concrete container, then the container changes | E0501, E0502 | Read at the call site, or make the helper generic in the value |
| X17 | Move a non-plain by-value argument, then fail on a later check | E0411 | Check first and move last, or split the function |

## 8. Run-time checks

The rules above remove most memory errors at compile time. These checks cover the rest at run time. Each failure panics, except where the table says otherwise.

| Check | Where | Cost per use | How to avoid it |
|---|---|---|---|
| Bounds | Index, slice | One compare | Range analysis removes it where it can prove the index; `for` loops carry none |
| Overflow | `+ - *`, negate | One branch on the flag | `+% -% *%`, where wrapping is correct |
| Divide | `/ %`, including `MIN / -1` | One or two compares | A constant divisor |
| Shift amount | `<< >>` | One compare | A constant amount |
| `Cast[T]` | Named conversion | One or two compares | `Trunc[T]`, or a lossless `T(x)` |
| `Must()` | Named unwrap | One branch | Handle the case |
| UTF-8 validation | `str.From` | O(n), once | None outside std |
| `str` slice boundary | `s[a..b]` | Two compares | |
| Enum tag from C | At the C boundary | One compare | `unsafe` |
| Allocator owner | Every grow and free that takes `&Alloc` on `HeapList`, `HeapMap`, `HeapText`, `TextMap`, `Box` and `Chan`, on the slow path only | One compare, plus 8 bytes per value | None: this check is the safety argument for those types |
| Pool generation | `Pool` free and get | One compare, 4 bytes per slot | None. A stale handle gets `none` from `Get` and `GetMut` and `false` from `Free`, never a panic |
| Stack limit | A guard page below each task's stack, and probes on frames larger than a page | None on the fast path | Nothing to remove |

[The reference](reference.md#9-errors) lists everything that panics; this table prices the checks behind it. The `Pool` check never panics. A handle from another pool behaves as a stale one, unless its slot and generation happen to match a live value, which is never memory-unsafe.

**Stack limit.** Each stack has a guard page below it, and a frame larger than a page probes each page it skips, so an overflow always faults on the guard, and the handler reports a `stack limit` panic. On Linux 6.13 and later the guard needs no mapping of its own, so guard pages do not limit the number of tasks. On older kernels the limit is `(vm.max_map_count - mappings) / 2`.

## 9. What is guaranteed

A program without `unsafe` that compiles is meant to be free of three things: reads or writes of memory whose lifetime has ended, two conflicting accesses while a reference is usable, and data races. `unsafe` code keeps these guarantees only if its author meets the duties of R19.

The rules are tested, not proven. A program the compiler accepts that breaks these guarantees is a bug in the rules.
