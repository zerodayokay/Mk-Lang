// verdict: reject E0605
// note: R16 unseen impl fn value: v.V declares mut view, so a fn value backed by a private impl in another module h still cannot store a local's reference into MakeV()
import v
import h

MakeV(): v.V {
    return v.V.Of(&v.GM)
}

Stash(io: Io, f: fn(vv: &v.V, io: Io, x: &i32 into vv)) {
    var n: i32 = 5
    vv := MakeV()
    f(&vv, io, &n) // <- E0605
}

export Main(sys: &System): void {
    Stash(sys.io, h.Storer())
}
