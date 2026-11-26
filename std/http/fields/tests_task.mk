// Why: the decoder lives in a connection that tasks share, and a decoded
// FieldBlock moves from the reading task to the stream's task, so both
// must be Send and own all their memory.
//
// What: a test that moves a Decoder into a task, which decodes there, and
// moves the filled FieldBlock into a second task, which reads it.
//
// How: s.go with the values by move and the heap by reference (Alloc is not
// Share, so each task takes h.Alloc() itself).

// Reader decodes block with dec, then hands the block it filled to a task
// of its own scope, and returns what that task read in *out.
Reader(io: Io, dec: Decoder, h: &Heap, block: []u8, out: mut int) {
    a := h.Alloc()
    var d := dec
    var fb := FieldBlock.New(a)
    o := d.Decode(a, mut fb, block) else {
        Outcome.TooLarge
    }
    d.Free(a)
    if o != Outcome.Fits {
        fb.Free(a)
        return
    }
    scope s := io.Scope() {
        s.go Handler(fb, h, out)
    }
}

// Handler reads the fields of fb, which it owns, then frees it.
Handler(fb: FieldBlock, h: &Heap, out: mut int) {
    var n := 0
    for f in fb.Iter() {
        n += f.name.Len() + f.value.Len()
    }
    *out = n * 100 + fb.Len()
    fb.Free(h.Alloc())
}

test "a Decoder and a filled FieldBlock move into tasks" (sys: &System) {
    h := sys.heap.Alloc()
    block := try Unhex(sys.arena, "8286 8441 8cf1 e3c2 e5f2 3a6b a0ab 90f4 ff")
    dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    var got := 0
    scope s := sys.io.Scope() {
        s.go Reader(sys.io, dec, sys.heap, block, mut got)
    }
    // :method GET, :scheme http, :path /, :authority www.example.com.
    Assert(got == (7 + 3 + 7 + 4 + 5 + 1 + 10 + 15) * 100 + 4)
}
