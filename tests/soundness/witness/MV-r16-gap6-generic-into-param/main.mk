// verdict: reject E0605
// note: R16 exported generic function whose into target is a bare type parameter T: v.V declares mut view, so MakeV() holds the global node and g.Put[v.V] refuses storing a local's reference into it
import v
import g

MakeV(): v.V {
    return v.V.Of(&v.GM)
}

WriteV(vv: &v.V, io: Io, x: &i32 into vv) {
    unsafe "probe: Ptr is GM" {
        m := &*vv.Ptr()
        var gr := m.Lock(io) else {
            return
        }
        gr.Store(some(x))
        gr.Unlock()
    }
}

Stash(io: Io) {
    var n: i32 = 5
    vv := MakeV()
    g.Put[v.V](&vv, io, &n, WriteV) // <- E0605
}

export Main(sys: &System): void {
    Stash(sys.io)
}
