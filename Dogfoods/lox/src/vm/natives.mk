// Why: Scripts call built-in functions written in Mk, some of which share
// state.
//
// What: The Native interface, its errors, Host, and the natives (len, str, num,
// int, substr, chr, ord, push, pop, extend, random, seed, gc, objects).
//
// How: Natives are called through the table that `gen dyn.Of(Native)` writes.
// Shared state sits in the host's registry as erased references, found by the
// type's erased key, as Lua's registry holds userdata for C functions.

import std.dyn
import std.dyn.erased
import std.mem
import src.code

/// Argument `arg` (from 1) has the wrong type.
export error WrongType(arg: int)
/// Argument `arg` (from 1) is outside what the native accepts.
export error OutOfRange(arg: int)
/// The native's shared state was never registered.
export error NoState

/// How a native call fails. The VM reports it with the native's name and `{e}`.
export type NativeError = WrongType | OutOfRange | NoState | OutOfMemory

/// A function implemented in Mk.
export interface Native {
    Arity(&self): int
    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value
}

gen dyn.Of(Native)

/// What natives may reach: the heap, and the registry of shared native state.
export mut view struct Host {
    export heap: Heap
    states: List[dyn.Ref]
    keys: Map[u64, int]
    /// Where natives build text before it becomes a string object.
    scratch: Text
    /// The lists being written, for `Heap.Write`.
    seen: Set[mem.Handle]
}

/// A host over `heap` with an empty registry in `a`.
export Host.New(a: &Arena, heap: Heap): Host from a, heap {
    return Host(heap: heap, states: List[dyn.Ref].New(a), keys: Map[u64, int].New(a), scratch: Text.New(a), seen: Set[mem.Handle].New(a))
}

/// Registers `x` as the shared state of type `T`, replacing an earlier one.
export Host.Register[T](mut self, x: mut T into self): OutOfMemory!void {
    key := erased.Key[T]()
    if i := self.keys.Get(&key) {
        self.states[*i] = dyn.Ref.Of(x)
        return
    }
    try self.states.Push(dyn.Ref.Of(x))
    try self.keys.Set(key, self.states.Len() - 1)
}

/// The shared state of type `T`, if one is registered.
export Host.State[T](mut self): ?mut T from self {
    i := self.keys.Get(&erased.Key[T]()) else { return none }
    if !self.states[*i].Is[T]() {
        return none
    }
    return some(self.states[*i].Mut[T]())
}

/// The shared state of type `T`, for reading.
export Host.Peek[T](&self): ?&T from self {
    i := self.keys.Get(&erased.Key[T]()) else { return none }
    if !self.states[*i].Is[T]() {
        return none
    }
    return some(self.states[*i].Get[T]())
}

/// The state of the `random` and `seed` natives: xorshift64* (Vigna, 2016).
export struct Random {
    export state: u64
    export calls: int
}

/// The next value in [0, 1), from the top 53 bits.
Random.Next(mut self): f64 {
    var x := self.state
    x ^= x >> 12
    x ^= x << 25
    x ^= x >> 27
    self.state = x
    self.calls += 1
    return Cast[f64]((x *% 0x2545F4914F6CDD1D) >> 11) / 9007199254740992.0
}

struct Len {
}

impl Native for Len {
    Arity(&self): int {
        return 1
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        if s := host.heap.Str(args[0]) {
            return code.Value.Num(n: Cast[f64](s.Len()))
        }
        n := host.heap.ArrayLen(args[0]) else { fail WrongType(arg: 1) }
        return code.Value.Num(n: Cast[f64](n))
    }
}

/// `str(x)`: the text `print x` writes, as a new string.
struct ToStr {
}

impl Native for ToStr {
    Arity(&self): int {
        return 1
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        host.scratch.Clear()
        var w := host.scratch.Writer()
        host.heap.Write(mut w, args[0], mut host.seen) else {
            fail OutOfMemory
        }
        return try host.heap.NewString(host.scratch.Str())
    }
}

/// `num(s)`: the number `s` spells, by `str.ParseFloat`, or nil.
struct ToNum {
}

impl Native for ToNum {
    Arity(&self): int {
        return 1
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        s := host.heap.Str(args[0]) else { fail WrongType(arg: 1) }
        n := s.Trim().ParseFloat() else { return code.Value.Nil }
        return code.Value.Num(n: n)
    }
}

/// `int(s)`: the integer `s` spells in decimal, by `str.ParseInt`, or nil.
struct ToInt {
}

impl Native for ToInt {
    Arity(&self): int {
        return 1
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        s := host.heap.Str(args[0]) else { fail WrongType(arg: 1) }
        n := s.Trim().ParseInt() else { return code.Value.Nil }
        return code.Value.Num(n: Cast[f64](n))
    }
}

/// `substr(s, start, len)`: bytes of `s`, which must fall on character boundaries.
struct Substr {
}

impl Native for Substr {
    Arity(&self): int {
        return 3
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        s := host.heap.Str(args[0]) else { fail WrongType(arg: 1) }
        start := Whole(args[1]) else { fail WrongType(arg: 2) }
        len := Whole(args[2]) else { fail WrongType(arg: 3) }
        if start < 0 || start > s.Len() {
            fail OutOfRange(arg: 2)
        }
        if len < 0 || start + len > s.Len() {
            fail OutOfRange(arg: 3)
        }
        if !Boundary(s, start) || !Boundary(s, start + len) {
            fail OutOfRange(arg: 2)
        }
        host.scratch.Clear()
        try host.scratch.Push(s[start..start + len])
        return try host.heap.NewString(host.scratch.Str())
    }
}

/// `chr(n)`: the one-character string of Unicode scalar value `n`.
struct Chr {
}

impl Native for Chr {
    Arity(&self): int {
        return 1
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        n := Whole(args[0]) else { fail WrongType(arg: 1) }
        if n < 0 || n > 0x10FFFF || (n >= 0xD800 && n <= 0xDFFF) {
            fail OutOfRange(arg: 1)
        }
        host.scratch.Clear()
        try host.scratch.PushChar(Cast[char](Cast[u32](n)))
        return try host.heap.NewString(host.scratch.Str())
    }
}

/// `ord(s)`: the Unicode scalar value of the first character of `s`, or nil when `s` is
/// empty.
struct Ord {
}

impl Native for Ord {
    Arity(&self): int {
        return 1
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        s := host.heap.Str(args[0]) else { fail WrongType(arg: 1) }
        c := s.Scalars().Next() else { return code.Value.Nil }
        return code.Value.Num(n: Cast[f64](Cast[u32](c)))
    }
}

/// Reports whether byte offset `i` of `s` starts a character or is its end.
Boundary(s: str, i: int): bool {
    return i == s.Len() || s.Byte(i) & 0xC0 != 0x80
}

/// The integer a number value holds exactly, if it does and fits in an `int`.
export Whole(v: code.Value): ?int {
    match v {
        .Num(n) {
            if n >= -9007199254740992.0 && n <= 9007199254740992.0 {
                i := Cast[i64](n)
                if Cast[f64](i) == n {
                    return some(Trunc[int](i))
                }
            }
            return none
        }
        _ { return none }
    }
}

/// `push(list, x)`: appends `x` and returns the list's new length.
struct Push {
}

impl Native for Push {
    Arity(&self): int {
        return 2
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        n := try host.heap.ArrayPush(args[0], args[1]) else { fail WrongType(arg: 1) }
        return code.Value.Num(n: Cast[f64](n))
    }
}

/// `pop(list)`: removes and returns the last item, or nil when the list is empty.
struct Pop {
}

impl Native for Pop {
    Arity(&self): int {
        return 1
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        if host.heap.ArrayLen(args[0]).IsNone() {
            fail WrongType(arg: 1)
        }
        return host.heap.ArrayPop(args[0]) else { code.Value.Nil }
    }
}

/// `extend(dst, src)`: appends the items of list `src` to list `dst` and returns `dst`.
struct Extend {
}

impl Native for Extend {
    Arity(&self): int {
        return 2
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        if !try host.heap.Extend(args[0], args[1]) {
            fail WrongType(arg: 1)
        }
        return args[0]
    }
}

/// `random()`: the next number in [0, 1) from the shared generator.
struct RandomNext {
}

impl Native for RandomNext {
    Arity(&self): int {
        return 0
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        r := host.State[Random]() else { fail NoState }
        return code.Value.Num(n: r.Next())
    }
}

/// `seed(n)`: restarts the shared generator from a non-zero whole number.
struct Seed {
}

impl Native for Seed {
    Arity(&self): int {
        return 1
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        n := Whole(args[0]) else { fail WrongType(arg: 1) }
        if n <= 0 {
            fail OutOfRange(arg: 1)
        }
        r := host.State[Random]() else { fail NoState }
        r.state = Cast[u64](n)
        return code.Value.Nil
    }
}

/// `gc()`: asks for a collection before the next instruction, and returns nil.
struct Gc {
}

impl Native for Gc {
    Arity(&self): int {
        return 0
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        host.heap.requested = true
        return code.Value.Nil
    }
}

/// `objects()`: the number of objects in the heap, live or not yet collected.
struct Objects {
}

impl Native for Objects {
    Arity(&self): int {
        return 0
    }

    Call(mut self, host: mut Host, args: []code.Value): NativeError!code.Value {
        return code.Value.Num(n: Cast[f64](host.heap.Len()))
    }
}

/// One native: its Lox name and its table.
struct Entry {
    name: str
    fun: NativeDyn
}

/// Every native, in the order their global names are defined. Each lives in `a` for the
/// life of the VM.
Install(a: &Arena): OutOfMemory!List[Entry] from a {
    var out := List[Entry].New(a)
    try out.Push(Entry(name: "len", fun: NativeDyn.Of(try a.Put(Len()))))
    try out.Push(Entry(name: "str", fun: NativeDyn.Of(try a.Put(ToStr()))))
    try out.Push(Entry(name: "num", fun: NativeDyn.Of(try a.Put(ToNum()))))
    try out.Push(Entry(name: "int", fun: NativeDyn.Of(try a.Put(ToInt()))))
    try out.Push(Entry(name: "substr", fun: NativeDyn.Of(try a.Put(Substr()))))
    try out.Push(Entry(name: "chr", fun: NativeDyn.Of(try a.Put(Chr()))))
    try out.Push(Entry(name: "ord", fun: NativeDyn.Of(try a.Put(Ord()))))
    try out.Push(Entry(name: "push", fun: NativeDyn.Of(try a.Put(Push()))))
    try out.Push(Entry(name: "pop", fun: NativeDyn.Of(try a.Put(Pop()))))
    try out.Push(Entry(name: "extend", fun: NativeDyn.Of(try a.Put(Extend()))))
    try out.Push(Entry(name: "random", fun: NativeDyn.Of(try a.Put(RandomNext()))))
    try out.Push(Entry(name: "seed", fun: NativeDyn.Of(try a.Put(Seed()))))
    try out.Push(Entry(name: "gc", fun: NativeDyn.Of(try a.Put(Gc()))))
    try out.Push(Entry(name: "objects", fun: NativeDyn.Of(try a.Put(Objects()))))
    return out
}

/// Registers the state the natives share in `host`, kept in `a`.
ShareState(a: &Arena into host, host: mut Host, seed: u64): OutOfMemory!void {
    try host.Register(try a.Put(Random(state: seed, calls: 0)))
}
