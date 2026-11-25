// Why: a C `void *` parameter takes any object pointer, so a program must be
// able to make a *void from one and get the typed pointer back.
// What: Main, which round-trips a pointer through *void and prints what it
// reads.
// How: raw.PtrCast, raw.Null and raw.FromAddr with `void` written as a type
// argument; raw.Addr, raw.IsNull and raw.Same take the *void by inference.

import std.raw

struct Pair {
    a: i32
    b: i32
}

// Sum reads a Pair through an untyped pointer, as a C callback reads its
// context argument.
Sum(ctx: *void): i32 {
    unsafe "the caller passes a pointer to a live Pair" {
        p := raw.PtrCast[void, Pair](ctx)
        return (*p).a + (*p).b
    }
}

export Main(sys: &System): WriteError!void {
    var pair := Pair(a: 40, b: 2)
    typed := raw.OfMut(mut pair)
    var erased := raw.Null[void]()
    was_null := raw.IsNull(erased)
    var from_addr := raw.Null[void]()
    unsafe "typed points at pair, which is live; its address is that of the same object" {
        erased = raw.PtrCast[Pair, void](typed)
        from_addr = raw.FromAddr[void](raw.Addr(typed))
    }
    same := raw.Same(erased, from_addr)
    kept := raw.Addr(erased) == raw.Addr(typed)
    sum := Sum(erased)
    try sys.Print("{was_null} {same} {kept} {sum}\n", (was_null, same, kept, sum))
}
