// Why: decoding and encoding sit on every HTTP/2 request, so their cost
// per field and the Huffman decoder's throughput must be measurable, and
// a warm decoder or encoder must not allocate per field.
//
// What: benchmarks of Decode and Encode over the RFC 7541 C.4 and C.6
// sequences, and of HuffDecode over 4 KiB of header text.
//
// How: each benchmark builds its decoder or encoder and its arena once,
// then per iteration empties the dynamic table (a capacity of 0, then the
// old one) and runs the three blocks of a sequence, resetting the arena.
// Plain mk test runs each once, as a check that it works.

import std.testing

// The C.4 and C.6 blocks, as the RFC prints them.
const C4 = ["8286 8441 8cf1 e3c2 e5f2 3a6b a0ab 90f4 ff", "8286 84be 5886 a8eb 1064 9cbf", "8287 85bf 4088 25a8 49e9 5ba9 7d7f 8925 a849 e95b b8e8 b4bf"]
const C6 = ["4882 6402 5885 aec3 771a 4b61 96d0 7abe 9410 54d4 44a8 2005 9504 0b81 66e0 82a6 2d1b ff6e 919d 29ad 1718 63c7 8f0b 97c8 e9ae 82ae 43d3", "4883 640e ffc1 c0bf", "88c1 6196 d07a be94 1054 d444 a820 0595 040b 8166 e084 a62d 1bff c05a 839b d9ab 77ad 94e7 821d d7f2 e6c7 b335 dfdf cd5b 3960 d5af 2708 7f36 72c1 ab27 0fb5 291f 9587 3160 65c0 03ed 4ee5 b106 3d50 07"]

// DecodeSeq decodes the blocks of hex n times, each time from an empty
// table of size size; it returns the fields decoded.
DecodeSeq(sys: &System, hex: []str, size: int, n: int): DecodeError|OutOfMemory!int {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, size, 1 << 16)
    defer dec.Free(h)
    var keep := try Arena.New(sys.heap)
    defer keep.Free()
    var wires := List[[]u8].New(&keep)
    for x in hex {
        try wires.Push(try Unhex(&keep, x))
    }
    var got := FieldBlock.New(h)
    defer got.Free(h)
    var total := 0
    for _ in 0..n {
        dec.table.SetCap(0)
        dec.table.SetCap(size)
        for w in &wires {
            got.Reset()
            _ = try dec.Decode(h, mut got, *w)
            total += got.Len()
        }
    }
    return total
}

// EncodeSeq encodes the field lists of the RFC sequence lists n times, each
// time from an empty table; it returns the bytes written.
EncodeSeq(sys: &System, lists: [][]str, size: int, huffman: bool, n: int): TooSmall|OutOfMemory!int {
    h := sys.heap.Alloc()
    var enc := try Encoder.New(h, size, huffman)
    defer enc.Free(h)
    var keep := try Arena.New(sys.heap)
    defer keep.Free()
    var fs := List[[]TField].New(&keep)
    for l in lists {
        try fs.Push(try ToFields(&keep, l))
    }
    out := try keep.Slice[u8](1024)
    var total := 0
    for _ in 0..n {
        enc.table.SetCap(0)
        enc.table.SetCap(size)
        for f in &fs {
            total += try enc.Encode(out, *f)
        }
    }
    return total
}

// The C.4 and C.6 header lists, block by block.
const L4: [3][]str = [
    [":method: GET", ":scheme: http", ":path: /", ":authority: www.example.com"],
    [":method: GET", ":scheme: http", ":path: /", ":authority: www.example.com", "cache-control: no-cache"],
    [":method: GET", ":scheme: https", ":path: /index.html", ":authority: www.example.com", "custom-key: custom-value"],
]
const L6: [3][]str = [
    [":status: 302", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:21 GMT", "location: https://www.example.com"],
    [":status: 307", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:21 GMT", "location: https://www.example.com"],
    [":status: 200", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:22 GMT", "location: https://www.example.com", "content-encoding: gzip", "set-cookie: foo=ASDJKHQKBZXOQWEOPIUAXQWEOIU; max-age=3600; version=1"],
]

test "bench: decode the C.4 requests (14 fields per run)" (sys: &System) {
    testing.BenchSys(sys, fn(s: &System, n: int) {
        Assert(DecodeSeq(s, C4[..], DefaultTableBytes, n).Must() == 14 * n)
    })
}

test "bench: decode the C.6 responses (14 fields per run)" (sys: &System) {
    testing.BenchSys(sys, fn(s: &System, n: int) {
        Assert(DecodeSeq(s, C6[..], 256, n).Must() == 14 * n)
    })
}

test "bench: encode the C.4 requests (14 fields per run)" (sys: &System) {
    testing.BenchSys(sys, fn(s: &System, n: int) {
        Assert(EncodeSeq(s, L4[..], DefaultTableBytes, true, n).Must() == 53 * n)
    })
}

test "bench: encode the C.6 responses (14 fields per run)" (sys: &System) {
    testing.BenchSys(sys, fn(s: &System, n: int) {
        Assert(EncodeSeq(s, L6[..], 256, true, n).Must() == 141 * n)
    })
}

// HuffDecodeN decodes 4096 bytes of Huffman-coded header text n times; it
// returns the bytes decoded.
HuffDecodeN(sys: &System, n: int): OutOfMemory!int {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    text := "accept-encoding: gzip, deflate, br; user-agent: Mozilla/5.0 (X11; Linux x86_64) date: Mon, 21 Oct 2013 20:13:21 GMT ".AsBytes()
    plain := try ar.Slice[u8](4096 * 8 / 5)
    for i in 0..plain.Len() {
        plain[i] = text[i % text.Len()]
    }
    var k := plain.Len()
    for HuffLen(plain[0..k]) > 4096 {
        k -= 1
    }
    coded := try ar.Slice[u8](HuffLen(plain[0..k]))
    _ = HuffPut(coded, plain[0..k])
    out := try ar.Slice[u8](HuffMaxOut(coded.Len()))
    var total := 0
    for _ in 0..n {
        total += HuffDecode(out, coded[..]).Must()
    }
    return total
}

test "bench: Huffman decode of 4 KiB of coded header text" (sys: &System) {
    testing.BenchSys(sys, fn(s: &System, n: int) {
        Assert(HuffDecodeN(s, n).Must() > 4096 * n)
    })
}
