// verdict: reject E0701
// rule: MKH-596
// note: a generic body is checked once, and an unbounded T may be linear, so pushing it into an arena List is refused there; `T: Plain` admits the push (MKH596_accept_arena_store)
Put[T](l: mut List[T], x: T into l): OutOfMemory!void {
    try l.Push(x) // <- E0701
}
