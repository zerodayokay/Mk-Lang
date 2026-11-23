// verdict: reject E0605
// note: R16 storing fn literal bound to a local: V declares mut view, so a local fn literal storing into &V catches the local loan stored into MakeV()
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

V.Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

MakeV(): V {
    return V.Of(&GM)
}

Stash(io: Io) {
    var n: i32 = 5
    vv := MakeV()
    f := fn(v: &V, io2: Io, x: &i32 into v) {
        unsafe "probe: p is GM" {
            m := &*v.p
            var g := m.Lock(io2) else {
                return
            }
            g.Store(some(x))
            g.Unlock()
        }
    }
    f(&vv, io, &n) // <- E0605
}

export Main(sys: &System): void {
    Stash(sys.io)
}
