// Source: the release protocol's run-time checks. With no
// argument, Clear of an emptied HeapList clears, and Clear of one that still holds a linear
// element panics (code 11; an arena List holds no linear element, MKH-596); with one, a
// Shared handle that is not the last frees, and the last Shared.Free of a linear value panics
// (code 8). Both are the library's explicit panic, so the report is the message alone; the
// .err files move when std does.
import std.sync

linear struct Token {
    id: int
}

Token.Close(self) {
    Token(id: _) := self
}

export Main(sys: &System): WriteError|OutOfMemory!void {
    if sys.args.Len() > 1 {
        t := Token(id: 2)
        onerr t.Close()
        s := try sync.Shared[Token].New(sys.heap, t)
        onerr s.Free(sys.heap)
        c := s.Clone()
        c.Free(sys.heap)
        try sys.Print("clone freed {id}\n", (id: s.Ref().id))
        s.Free(sys.heap)
        return
    }
    a := sys.heap.Alloc()
    var xs := HeapList[Token].New(a)
    onerr xs.FreeEmpty(a)
    first := Token(id: 1)
    onerr first.Close()
    try xs.Push(a, first)
    popped := xs.Pop() else { Token(id: 0) }
    id := popped.id
    popped.Close()
    xs.Clear()
    try sys.Print("emptied {id} {n}\n", (id, n: xs.Len()))
    second := Token(id: 3)
    onerr second.Close()
    try xs.Push(a, second)
    xs.Clear()
    xs.FreeEmpty(a)
}