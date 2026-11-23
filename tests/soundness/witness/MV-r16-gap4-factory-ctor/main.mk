// verdict: reject E0605
// note: R16 factory/generic constructor and receiver-less hand-out: V declares mut view, so Factory.Make, MakeGen and free hand-out Get all keep the global node and refuse storing a local
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

struct Factory {
    tag: int
}

Factory.Make(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

MakeGen[T: Plain](m: &Mutex[?&i32], unused: T): V from m {
    _ = unused
    return V(p: raw.Of(m))
}

Get(v: &V): &Mutex[?&i32] from v {
    unsafe "probe: p is GM" {
        return &*v.p
    }
}

FromFactory(): V {
    return Factory.Make(&GM)
}

FromGeneric(): V {
    return MakeGen[i32](&GM, 0)
}

StashFactory(io: Io) {
    var n: i32 = 5
    vv := FromFactory()
    m := Get(&vv)
    var g := m.Lock(io) else {
        return
    }
    g.Store(some(&n)) // <- E0605
    g.Unlock()
}

StashGeneric(io: Io) {
    var n: i32 = 5
    vv := FromGeneric()
    m := Get(&vv)
    var g := m.Lock(io) else {
        return
    }
    g.Store(some(&n)) // <- E0605
    g.Unlock()
}

export Main(sys: &System): void {
    StashFactory(sys.io)
    StashGeneric(sys.io)
}
