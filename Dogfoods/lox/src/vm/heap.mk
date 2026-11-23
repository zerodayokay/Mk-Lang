// Why: Lox programs allocate strings, functions and lists freely, and need a
// garbage collector.
//
// What: Obj and the object heap: every string, function, native and list is an
// object in a pool, named by a handle.
//
// How: Mark and sweep (Crafting Interpreters, chapter 26) with an epoch for the
// mark: an object is live when its mark equals the current epoch, so marks are
// never cleared. String bytes and list items live in two spaces that each
// collection compacts, so freed bytes and items are reused.

import std.mem
import src.code

// A workaround: a list would hold its own `List[Value]`, but a list made after the
// heap and stored in it cannot be reached from Main; so the items are a run of one space.
/// A heap object. Strings and lists are runs of the string space and the item space.
export enum Obj {
    Str(start: int, len: int)
    Fun(index: int, name: str)
    Native(index: int)
    Array(start: int, len: int, cap: int)
}

struct Object {
    mark: u32
    obj: Obj
}

/// What one collection did.
export struct Collection {
    export live: int
    export freed: int
}

/// The objects of one VM, its string space and its item space.
export view struct Heap {
    pool: mem.Pool[Object]
    strings: Text
    spare: Text
    items: List[code.Value]
    moved: List[code.Value]
    epoch: u32
    next: int
    export limit: int
    export stress: bool
    export collections: int
    export freed: int
    export requested: bool
}

/// The first collection runs at this many live objects; the threshold then doubles past
/// what survived, as clox's GC_HEAP_GROW_FACTOR does with bytes.
const FirstCollection: int = 256

/// An empty heap in `a`. `limit` caps the live objects after a collection, 0 for none.
export Heap.New(a: &Arena, limit: int, stress: bool): Heap from a {
    return Heap(pool: mem.Pool[Object].New(a), strings: Text.New(a), spare: Text.New(a), items: List[code.Value].New(a), moved: List[code.Value].New(a), epoch: 0, next: FirstCollection, limit: limit, stress: stress, collections: 0, freed: 0, requested: false)
}

/// The number of objects, live or not yet collected.
export Heap.Len(&self): int {
    return self.pool.Len()
}

/// Reports whether the VM should collect before it allocates again.
export Heap.Due(&self): bool {
    return self.stress || self.requested || self.pool.Len() >= self.next
}

Heap.Add(mut self, o: Obj into self): OutOfMemory!code.Value {
    h := try self.pool.Add(Object(mark: self.epoch, obj: o))
    return code.Value.Obj(h: h)
}

/// A new string object holding a copy of `s`. O(n); grows the string space by n bytes.
export Heap.NewString(mut self, s: str): OutOfMemory!code.Value {
    start := self.strings.Str().Len()
    try self.strings.Push(s)
    return try self.Add(Obj.Str(start: start, len: s.Len()))
}

/// A new string object of `a` followed by `b`, both string values. O(n).
export Heap.Concat(mut self, a: code.Value, b: code.Value): OutOfMemory!code.Value {
    // Both halves are copied to the end of the string space, so the new string is the
    // run from the old end, and no view of the space is held while it grows.
    start := self.strings.Str().Len()
    (start: sa, len: la) := self.StrSpan(a)
    (start: sb, len: lb) := self.StrSpan(b)
    try self.CopyWithin(sa, la)
    try self.CopyWithin(sb, lb)
    return try self.Add(Obj.Str(start: start, len: la + lb))
}

/// Appends bytes `start` to `start + len` of the string space to its end, through the
/// spare text, since the space cannot be read while it is pushed to.
Heap.CopyWithin(mut self, start: int, len: int): OutOfMemory!void {
    self.spare.Clear()
    try self.spare.Push(self.strings.Str()[start..start + len])
    try self.strings.Push(self.spare.Str())
}

/// The position and length of string value `v` in the string space, (0, 0) for another value.
Heap.StrSpan(&self, v: code.Value): (start: int, len: int) {
    o := self.Get(v) else { return (start: 0, len: 0) }
    match o {
        .Str(start, len) { return (start: *start, len: *len) }
        _ { return (start: 0, len: 0) }
    }
}

/// The object `v` names, if it is an object.
export Heap.Get(&self, v: code.Value): ?&Obj from self {
    h := Handle(v) else { return none }
    o := self.pool.Get(h) else { return none }
    return some(&o.obj)
}

/// The text of string value `v`, as a view of the string space; none for another value.
export Heap.Str(&self, v: code.Value): ?str from self {
    o := self.Get(v) else { return none }
    match o {
        .Str(start, len) { return some(self.strings.Str()[*start..*start + *len]) }
        _ { return none }
    }
}

/// A new object for function `index` of the program, printed with `name`.
export Heap.NewFunction(mut self, index: int, name: str into self): OutOfMemory!code.Value {
    return try self.Add(Obj.Fun(index: index, name: name))
}

/// A new object for native `index`.
export Heap.NewNative(mut self, index: int): OutOfMemory!code.Value {
    return try self.Add(Obj.Native(index: index))
}

/// A new list of the `n` values at the end of `stack`. O(n).
export Heap.NewArray(mut self, stack: []code.Value, n: int): OutOfMemory!code.Value {
    start := self.items.Len()
    for v in stack[stack.Len() - n..] {
        try self.items.Push(v)
    }
    return try self.Add(Obj.Array(start: start, len: n, cap: n))
}

/// The length of list `v`, if it is one.
export Heap.ArrayLen(&self, v: code.Value): ?int {
    o := self.Get(v) else { return none }
    match o {
        .Array(len) { return some(*len) }
        _ { return none }
    }
}

/// Item `i` of list `v`; none when `v` is no list or `i` is out of range.
export Heap.ArrayGet(&self, v: code.Value, i: int): ?code.Value {
    o := self.Get(v) else { return none }
    match o {
        .Array(start, len) {
            if i < 0 || i >= *len {
                return none
            }
            return some(self.items[*start + i])
        }
        _ { return none }
    }
}

/// Sets item `i` of list `v` to `x`; false when `v` is no list or `i` is out of range.
export Heap.ArraySet(mut self, v: code.Value, i: int, x: code.Value): bool {
    h := Handle(v) else { return false }
    o := self.pool.GetMut(h) else { return false }
    match o.obj {
        .Array(start, len) {
            if i < 0 || i >= len {
                return false
            }
            self.items[start + i] = x
            return true
        }
        _ { return false }
    }
}

/// Appends `x` to list `v` and returns the new length; none when `v` is no list. A full
/// list moves to the end of the item space with twice the room. O(1) amortised.
export Heap.ArrayPush(mut self, v: code.Value, x: code.Value): OutOfMemory!?int {
    h := Handle(v) else { return none }
    o := self.pool.GetMut(h) else { return none }
    match mut o.obj {
        .Array(start, len, cap) {
            if *len == *cap {
                *start = try Grow(mut self.items, *start, *len, *len + 1)
                *cap = Room(*len + 1)
            }
            self.items[*start + *len] = x
            *len += 1
            return some(*len)
        }
        _ { return none }
    }
}

/// Removes and returns the last item of list `v`; none when it is empty or no list.
export Heap.ArrayPop(mut self, v: code.Value): ?code.Value {
    h := Handle(v) else { return none }
    o := self.pool.GetMut(h) else { return none }
    match mut o.obj {
        .Array(start, len) {
            if *len == 0 {
                return none
            }
            *len -= 1
            return some(self.items[*start + *len])
        }
        _ { return none }
    }
}

/// The room a list grows to so that `need` items fit: a power of two from 8.
Room(need: int): int {
    var cap := 8
    for cap < need {
        cap *= 2
    }
    return cap
}

/// Copies the `len` items at `start` to the end of `items` with room for `need`, and
/// returns where they now start.
Grow(items: mut List[code.Value], start: int, len: int, need: int): OutOfMemory!int {
    at := items.Len()
    for i in start..start + len {
        x := items[i]
        try items.Push(x)
    }
    for _ in len..Room(need) {
        try items.Push(code.Value.Nil)
    }
    return at
}

/// Appends the items of list `src` to list `dst`. They may be one list, and then its
/// items are appended once. False when either is not a list.
export Heap.Extend(mut self, dst: code.Value, src: code.Value): OutOfMemory!bool {
    hd := Handle(dst) else { return false }
    hs := Handle(src) else { return false }
    if hd.Eq(&hs) {
        n := self.ArrayLen(dst) else { return false }
        for i in 0..n {
            x := self.ArrayGet(dst, i) else { return false }
            _ = try self.ArrayPush(dst, x)
        }
        return true
    }
    // Both headers at once: the destination's grows, the source's is read.
    pair := self.pool.GetPair(hd, hs) else { return false }
    match &pair.b.obj {
        .Array(start: from, len: n) {
            match mut pair.a.obj {
                .Array(start, len, cap) {
                    if *len + *n > *cap {
                        *start = try Grow(mut self.items, *start, *len, *len + *n)
                        *cap = Room(*len + *n)
                    }
                    for i in 0..*n {
                        self.items[*start + *len + i] = self.items[*from + i]
                    }
                    *len += *n
                    return true
                }
                _ { return false }
            }
        }
        _ { return false }
    }
}

/// The handle of an object value.
Handle(v: code.Value): ?mem.Handle {
    match v {
        .Obj(h) { return some(h) }
        _ { return none }
    }
}

/// Writes `v` as `print` shows it: numbers in shortest round-trip form, strings as their
/// text, lists as `[a, b]`. A list met again inside itself prints as `[...]`; `seen` holds
/// the lists being written and is left as it was given.
export Heap.Write(&self, w: mut Writer, v: code.Value, seen: mut Set[mem.Handle]): WriteError|OutOfMemory!void {
    match v {
        .Nil { try w.Str("nil") }
        .Bool(b) { try w.Str(if b { "true" } else { "false" }) }
        .Num(n) { try Print(w, "{n}", (n)) }
        .Obj(h) {
            o := self.pool.Get(h) else {
                try w.Str("<freed>")
                return
            }
            match o.obj {
                .Str(start, len) { try w.Str(self.strings.Str()[start..start + len]) }
                .Fun(name) { try Print(w, "<fn {name}>", (name)) }
                .Native { try w.Str("<native fn>") }
                .Array(start, len) {
                    if seen.Has(&h) {
                        try w.Str("[...]")
                        return
                    }
                    _ = try seen.Add(h)
                    try w.Str("[")
                    for i in 0..len {
                        if i > 0 {
                            try w.Str(", ")
                        }
                        try self.Write(w, self.items[start + i], seen)
                    }
                    try w.Str("]")
                    _ = seen.Remove(&h)
                }
            }
        }
    }
}

/// Collects every object not reachable from `roots`, then compacts both spaces.
/// `scratch` holds the worklist and is the caller's to free. O(live + dead + space).
export Heap.Collect(mut self, scratch: &Arena, roots: []code.Value): OutOfMemory!Collection {
    self.epoch +%= 1
    self.requested = false
    var gray := List[mem.Handle].New(scratch)
    for r in roots {
        if h := Handle(r) {
            try gray.Push(h)
        }
    }
    for h := gray.Pop() {
        o := self.pool.GetMut(h) else { continue }
        if o.mark == self.epoch {
            continue
        }
        o.mark = self.epoch
        match o.obj {
            .Array(start, len) {
                for x in self.items.Items()[start..start + len] {
                    if k := Handle(x) {
                        try gray.Push(k)
                    }
                }
            }
            _ {}
        }
    }
    epoch := self.epoch
    freed := self.pool.Sweep(fn(h: mem.Handle, o: &Object): bool { return o.mark == epoch })
    try self.Compact(scratch)
    live := self.pool.Len()
    self.next = if live * 2 > FirstCollection { live * 2 } else { FirstCollection }
    self.collections += 1
    self.freed += freed
    return Collection(live: live, freed: freed)
}

/// Moves the bytes of every live string and the items of every live list to the front of
/// fresh spaces, in slot order, and makes them the spaces.
Heap.Compact(mut self, scratch: &Arena): OutOfMemory!void {
    var live := List[mem.Handle].New(scratch)
    for (handle, value) in self.pool.Iter() {
        match value.obj {
            .Str | .Array { try live.Push(handle) }
            _ {}
        }
    }
    self.spare.Clear()
    self.moved.Clear()
    for h in live {
        o := self.pool.GetMut(h) else { continue }
        match mut o.obj {
            .Str(start, len) {
                at := self.spare.Str().Len()
                try self.spare.Push(self.strings.Str()[*start..*start + *len])
                *start = at
            }
            .Array(start, len, cap) {
                at := self.moved.Len()
                for x in self.items.Items()[*start..*start + *len] {
                    try self.moved.Push(x)
                }
                *start = at
                *cap = *len
            }
            _ {}
        }
    }
    Swap(mut self.strings, mut self.spare)
    Swap(mut self.items, mut self.moved)
    self.spare.Clear()
    self.moved.Clear()
}
