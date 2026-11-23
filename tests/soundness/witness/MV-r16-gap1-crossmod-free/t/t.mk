import v

export Put(vv: &v.V, io: Io, x: &i32 into vv) {
    unsafe "probe: Ptr is GM set by V.Of" {
        m := &*vv.Ptr()
        var g := m.Lock(io) else {
            return
        }
        g.Store(some(x))
        g.Unlock()
    }
}
