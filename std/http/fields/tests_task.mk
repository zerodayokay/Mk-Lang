// Why: the decoder lives in a connection that tasks share, and a decoded
// FieldBlock moves from the reading task to the stream's task, so both
// must be Send and own all their memory.
//
// What: a test that moves a Decoder into a task, which decodes there, and
// moves the filled FieldBlock into a second task, which reads it; and one
// where two tasks share a Decoder by reference and an Encoder moves into a
// task.
//
// How: s.go with the values by move and the heap by reference (Alloc is not
// Share, so each task takes h.Alloc() itself).

// DecodeTask decodes block with dec, then hands the block it filled to a task
// of its own scope, and returns what that task read in *out.
DecodeTask(io: Io, dec: Decoder, h: &Heap, block: []u8, out: mut int) {
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
        s.go ReadTask(fb, h, out)
    }
}

// ReadTask reads the fields of fb, which it owns, then frees it.
ReadTask(fb: FieldBlock, h: &Heap, out: mut int) {
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
        s.go DecodeTask(sys.io, dec, sys.heap, block, mut got)
    }
    // :method GET, :scheme http, :path /, :authority www.example.com.
    Assert(got == (7 + 3 + 7 + 4 + 5 + 1 + 10 + 15) * 100 + 4)
}

// SizeTask reads the table size of a decoder that another task shares.
SizeTask(dec: &Decoder, out: mut int) {
    *out = dec.TableSize()
}

// EncodeTask encodes one field with an encoder it owns, then frees it.
EncodeTask(enc: Encoder, h: &Heap, out: mut int) {
    var e := enc
    var buf: [64]u8 = [0; 64]
    *out = e.Write(mut buf[..], "custom-key".AsBytes(), "custom-value".AsBytes(), Mode.Index) else {
        0
    }
    *out = *out * 10 + e.TableLen()
    e.Free(h.Alloc())
}

test "tasks share a Decoder by reference, and an Encoder moves into a task" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    try Fits(h, mut dec, mut fb, try Unhex(sys.arena, "400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572"))
    var x := 0
    var y := 0
    scope s := sys.io.Scope() {
        s.go SizeTask(&dec, mut x)
        s.go SizeTask(&dec, mut y)
    }
    Assert(x == 55 && y == 55)
    enc := try Encoder.New(h, DefaultTableBytes, false)
    var n := 0
    scope s := sys.io.Scope() {
        s.go EncodeTask(enc, sys.heap, mut n)
    }
    // 1 + 11 + 13 bytes, one entry.
    Assert(n == 251)
}
