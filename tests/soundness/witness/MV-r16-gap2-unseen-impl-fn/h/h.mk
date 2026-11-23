import v

interface Sink {
    Put(self, io: Io, x: &i32 into self)
}

impl Sink for &v.V {
    Put(self, io: Io, x: &i32 into self) {
        unsafe "probe: Ptr is GM" {
            m := &*self.Ptr()
            var g := m.Lock(io) else {
                return
            }
            g.Store(some(x))
            g.Unlock()
        }
    }
}

RunSink[S: Sink](s: S, io: Io, x: &i32 into s) {
    s.Put(io, x)
}

DoPut(vv: &v.V, io: Io, x: &i32 into vv) {
    RunSink[&v.V](vv, io, x)
}

export Storer(): fn(vv: &v.V, io: Io, x: &i32 into vv) {
    return DoPut
}
