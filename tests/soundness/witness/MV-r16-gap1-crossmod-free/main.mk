// verdict: reject E0605
// note: R16 cross-module free store via raw accessor: v.V declares mut view, so MakeV holds the global node and t.Put (a free function in another module t) cannot store a local's reference into it
import v
import t

MakeV(): v.V {
    return v.V.Of(&v.GM)
}

Stash(io: Io) {
    var n: i32 = 5
    vv := MakeV()
    t.Put(&vv, io, &n) // <- E0605
}

export Main(sys: &System): void {
    Stash(sys.io)
}
