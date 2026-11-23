// comphash, Mk, std: the task of ../README.md. Pass 1 reads the input file in
// 64 KiB chunks via fs.File.Read, feeding every chunk into std.hash.Sha256State
// and std.compress.Encoder (RFC 1950 zlib framing, level 6) writing through a
// 64 KiB Writer over the output file. Pass 2 reads the output file back
// through a 64 KiB Reader, inflates with std.compress.Inflater.NewZlib
// (checking header FCHECK/FDICT, stream end and Adler-32 trailer inside the
// inflater) into 64 KiB chunks, hashes the inflated bytes with a second
// Sha256State, verifies length and SHA-256 match Pass 1, and prints
// "<bytes> <hex-sha256>".
// Usage: comphash <in> <out>.

import std.compress
import std.fs
import std.hash

error Usage
error SizeDiffers
error DigestDiffers

type AppError = fs.Error | ReadError | WriteError | OutOfMemory | Utf8Error | compress.Error | Usage | SizeDiffers | DigestDiffers

struct HashResult {
    total: i64
    digest: [32]u8
}

CompressAndHash(sys: &System, a: &Arena, in_path: str, out_path: str, rbuf: mut []u8, wbuf: mut []u8): AppError!HashResult {
    var src := try fs.Open(sys.io, in_path)
    onerr src.Abandon()
    var dst := try fs.Create(sys.io, out_path)
    onerr dst.Abandon()
    var w := dst.Writer(sys.io, wbuf)
    var enc := try compress.Encoder.New(a, compress.Framing.Zlib, 6)
    onerr enc.Abandon()
    var sha := hash.Sha256State.New()
    var total: i64 = 0
    for {
        n := try src.Read(sys.io, rbuf)
        if n == 0 {
            break
        }
        chunk := rbuf[..n]
        sha.Bytes(chunk)
        try enc.Write(mut w, chunk)
        total += i64(n)
    }
    try enc.Finish(mut w)
    try w.Flush()
    try dst.Close(sys.io)
    try src.Close(sys.io)
    return HashResult(total: total, digest: sha.Sum())
}

DecompressAndHash(sys: &System, a: &Arena, in_path: str, rbuf: mut []u8, obuf: mut []u8): AppError!HashResult {
    var src := try fs.Open(sys.io, in_path)
    onerr src.Abandon()
    var r := src.Reader(sys.io, rbuf)
    var z := compress.Inflater.NewZlib(a)
    var sha := hash.Sha256State.New()
    var total: i64 = 0
    for {
        n := try z.Read(mut r, obuf)
        if n == 0 {
            break
        }
        sha.Bytes(obuf[..n])
        total += i64(n)
    }
    try src.Close(sys.io)
    return HashResult(total: total, digest: sha.Sum())
}

FmtHex32(d: [32]u8, out: mut []u8) {
    hex := b"0123456789abcdef"
    for i in 0..32 {
        b := d[i]
        out[2 * i] = hex[int(b >> 4)]
        out[2 * i + 1] = hex[int(b & 15)]
    }
}

export Main(sys: &System): AppError!void {
    if sys.args.Len() != 3 {
        var e := sys.Stderr()
        try Print(mut e, "usage: comphash <in> <out>\n")
        fail Usage
    }
    var a := try Arena.New(sys.heap)
    defer a.Free()
    buf1 := try a.Slice[u8](65536)
    buf2 := try a.Slice[u8](65536)

    in_res := try CompressAndHash(sys, &a, sys.args[1], sys.args[2], buf1, buf2)
    out_res := try DecompressAndHash(sys, &a, sys.args[2], buf1, buf2)

    if out_res.total != in_res.total {
        fail SizeDiffers
    }
    for i in 0..32 {
        if out_res.digest[i] != in_res.digest[i] {
            fail DigestDiffers
        }
    }

    var hex_buf: [64]u8 = [0; 64]
    FmtHex32(in_res.digest, mut hex_buf[..])
    digest_str := try str.From(hex_buf[..])
    var out := sys.Stdout()
    try Print(mut out, "{t} {h}\n", (t: in_res.total, h: digest_str))
}
