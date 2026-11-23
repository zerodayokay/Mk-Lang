// verdict: reject E0605
// note: R16 keep through a wrapper struct W { v: V }: V declares mut view, so W inherits storesFirst through its inline field v and MakeW() holds the global node
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

struct W {
    v: V
}

Of(m: &Mutex[?&i32]): W from m {
    return W(v: V(p: raw.Of(m)))
}

V.Put(&self, io: Io, x: &i32 into self) {
    unsafe "probe: p is GM" {
        m := &*self.p
        var g := m.Lock(io) else {
            return
        }
        g.Store(some(x))
        g.Unlock()
    }
}

MakeW(): W {
    return Of(&GM)
}

Stash(io: Io) {
    var n: i32 = 5
    w := MakeW()
    w.v.Put(io, &n) // <- E0605
}

export Main(sys: &System): void {
    Stash(sys.io)
}
