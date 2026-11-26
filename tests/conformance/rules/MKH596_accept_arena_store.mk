// verdict: ok
// rule: MKH-596
// also: R2
// note: an arena container holds any value that need not be consumed, unique ones included, and a generic body pushes a `T: Plain`; linear values go into a heap container, drained and ended with FreeEmpty
import std.coll

Nested(a: &Arena): OutOfMemory!int {
    var inner := List[int].New(a)
    try inner.Push(1)
    var outer := List[List[int]].New(a)
    try outer.Push(inner)
    var d := coll.Deque[List[int]].New(a)
    try d.Push(List[int].New(a))
    return outer.Len() + d.Len()
}

Put[T: Plain](l: mut List[T], x: T into l): OutOfMemory!void {
    try l.Push(x)
}

Linear(h: &Alloc, r: Res, s: Res) {
    var l := HeapList[Res].New(h)
    l.Push(h, r) else {
        Panic("out of memory")
    }
    var d := coll.HeapDeque[Res].New(h)
    d.Push(h, s) else {
        Panic("out of memory")
    }
    for l.Len() > 0 {
        l.Pop().Must().Close()
    }
    l.FreeEmpty(h)
    d.FreeEach(h, fn(x: Res) {
        x.Close()
    })
}
