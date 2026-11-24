// verdict: ok
// note: storing box.tag.args (a nested tagged noAlloc field reaching no allocator whose target stays on box#2) into a local Arena keeps the walk off box#A across p#2's inner loop, so pushing arena-allocated copies into a List in that arena stays accepted (mutant M-R11-noalloc-loop)
struct Tag {
    args: []str
}

struct Box {
    arena: &Arena
    heap: &Heap
    tag: Tag
}

Collect(a: &Arena, args: []str): OutOfMemory!List[str] from a, args *args into a {
    var out := List[str].New(a)
    for s in args {
        try out.Push(s)
    }
    return out
}

Run(box: &Box): OutOfMemory!int {
    var arena := try Arena.New(box.heap)
    defer arena.Free()
    a := &arena
    kept := try Collect(a, box.tag.args[..])
    var owned := List[str].New(a)
    for s in kept[..] {
        try owned.Push(try a.Dup(s))
    }
    return owned.Len()
}

export Main(sys: &System): void {
    b := Box(arena: sys.arena, heap: sys.heap, tag: Tag(args: sys.args))
    _ = Run(&b) else { 0 }
}
