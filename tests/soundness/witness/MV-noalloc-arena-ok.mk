// verdict: ok
// note: storing sys.args[1..] (a tagged noAlloc field reaching no allocator) into a local Arena keeps the walk off sys#A across p#2's inner loop, so pushing arena-allocated copies into a List in that arena stays accepted (mutant M-R11-noalloc-loop)
Collect(a: &Arena, args: []str): OutOfMemory!List[str] from a, args *args into a {
    var out := List[str].New(a)
    for s in args {
        try out.Push(s)
    }
    return out
}

Run(sys: &System): OutOfMemory!int {
    var arena := try Arena.New(sys.heap)
    defer arena.Free()
    a := &arena
    kept := try Collect(a, sys.args[1..])
    var owned := List[str].New(a)
    for s in kept[..] {
        try owned.Push(try a.Dup(s))
    }
    return owned.Len()
}

export Main(sys: &System): void {
    _ = Run(sys) else { 0 }
}
