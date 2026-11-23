// Why: list, verify, extract and sum all read an archive, and must agree on
// what makes one damaged or forged.
//
// What: ListArchive, Verify, Extract and Sum, with the shared Examine,
// Unpack and Stream helpers.
//
// How: a plain archive is read only as far as it needs (manifest and
// signature at their offsets); a sealed one is read whole and opened first.
// Every entry's digest is checked as its data streams past.

import src.archive
import src.hex
import src.keys
import src.seal
import src.tree
import std.crypto
import std.fs
import std.hash

/// What to do with each file an archive holds.
enum Action {
    Extract
    Sum256
    Sum512
}

/// An archive's sections and its parsed, signature-checked manifest.
struct Checked {
    layout: archive.Layout
    manifest: archive.Manifest
}

/// The one archive a command takes.
One(c: &Ctx, command: str, args: []str): Failed!str from args {
    if args.Len() != 1 {
        fail c.Usage(command, "give exactly one archive")
    }
    return args[0]
}

/// The signing key of the public key file `path`, when one is given.
Signer(c: &Ctx, path: ?str): Failed!?[32]u8 {
    p := path else {
        return none
    }
    k := keys.LoadPublic(c.arena, c.io, p) else e {
        fail c.Fail(p, e)
    }
    return some(k.sign)
}

/// How a signature check came out, for a person.
Signed(a: &Arena, signature: []u8): OutOfMemory!str from a {
    if signature.Len() == 0 {
        return "unsigned"
    }
    var public: [32]u8 = [0; 32]
    public.CopyFrom(signature[..32])
    return try a.Fmt("signed by {fp}", (fp: hex.Hex(bytes: keys.Fingerprint(&public)[..])))
}

/// Reads a whole archive into `c.arena`, and when it is sealed, opens it
/// with the secret key file `key` into `plain`, which the budget counts.
ReadWhole(c: &Ctx, path: str, key: ?str, plain: mut HeapList[u8]): Failed|OutOfMemory![]u8 from c.arena, plain {
    bytes := fs.ReadBytes(c.arena, c.io, path) else e {
        fail c.Fail(path, e)
    }
    if !seal.IsSealed(bytes) {
        return bytes
    }
    key_path := key else {
        c.Say("pack: {path}: the archive is sealed; give --key\n", (path))
        fail Failed(code: ExitUsage)
    }
    var secret := keys.LoadSecret(c.arena, c.io, key_path) else e {
        fail c.Fail(key_path, e)
    }
    var o := seal.Open(bytes, &secret.box) else e {
        secret.Wipe()
        fail c.Fail(path, e)
    }
    secret.Wipe()
    defer o.Wipe()
    for _ in 0..o.plain_len {
        plain.Push(c.alloc, 0) else {
            fail c.Fail(path, OutOfMemory)
        }
    }
    o.Decrypt(bytes, mut plain[..]) else e {
        fail c.Fail(path, e)
    }
    return plain[..]
}

/// Splits a whole archive, parses its manifest and checks its signature.
Examine(c: &Ctx, a: &Arena, path: str, bytes: []u8, signer: ?[32]u8): Failed|OutOfMemory!Checked from a, bytes {
    layout := archive.Split(bytes) else e {
        fail c.Fail(path, e)
    }
    m := try Manifest(c, a, path, layout.manifest)
    archive.CheckSignature(layout.manifest, layout.signature, signer) else e {
        fail c.Fail(path, e)
    }
    return Checked(layout: layout, manifest: m)
}

/// Parses the manifest bytes of the archive `path`.
Manifest(c: &Ctx, a: &Arena, path: str, bytes: []u8): Failed|OutOfMemory!archive.Manifest from a, bytes {
    text := str.From(bytes) else {
        fail c.Fail(path, archive.BadManifest(line: 0))
    }
    return archive.ParseManifest(a, text) else e {
        fail c.Fail(path, e)
    }
}

/// Lists an archive. A plain one is read only as far as its manifest and
/// signature, at their offsets; a sealed one is read whole and opened.
// Named ListArchive, since a function List would hide the prelude type List.
ListArchive(c: &Ctx, o: &ListOptions): Error!void {
    path := try One(c, "list", o.archives)
    var scratch := try c.arena.Sub()
    defer scratch.Free()
    var plain := HeapList[u8].New(c.alloc)
    defer plain.Free(c.alloc)
    var f := fs.Open(c.io, path) else e {
        fail c.Fail(path, e)
    }
    onerr f.Abandon()
    var head: [12]u8 = [0; 12]
    n := try ReadAt(c, &f, path, 0, mut head[..])
    f.Close(c.io) else e {
        fail c.Fail(path, e)
    }
    checked := if seal.IsSealed(head[..n]) {
        bytes := try ReadWhole(c, path, o.key, mut plain)
        try Examine(c, c.arena, path, bytes, none)
    } else {
        try Prefix(c, &scratch, path, head[..n])
    }
    m := &checked.manifest
    signed := try Signed(&scratch, checked.layout.signature)
    codec := if m.codec == archive.Codec.Gzip { "gzip" } else { "deflate" }
    try c.sys.Print("{path}: {f} files, {t} bytes, {codec}, {signed}, manifest {id}\n", (path, f: m.Files(), t: m.total, codec, signed, id: hex.Hex(bytes: archive.Digest(checked.layout.manifest)[..8])))
    if o.dups {
        try Dups(c, &scratch, path, m)
        return
    }
    for e in &m.entries {
        if e.is_dir {
            try c.sys.Print("d {s:>10}  {e.path}/\n", (s: "-", e))
        } else if o.long {
            try c.sys.Print("f {e.size:>10}  {d}  {e.path}\n", (e, d: hex.Hex(bytes: e.digest[..])))
        } else {
            try c.sys.Print("f {e.size:>10}  {e.path}\n", (e))
        }
    }
}

/// Reads the header, manifest and signature of the plain archive `path`
/// at their offsets, leaving the data unread. `head` is its first bytes.
Prefix(c: &Ctx, a: &Arena, path: str, head: []u8): Failed|OutOfMemory!Checked from a {
    h := archive.DecodeHeader(head) else e {
        fail c.Fail(path, e)
    }
    signature_len := if h.signed { archive.SignatureLen } else { 0 }
    want := archive.HeaderLen + h.manifest_len + signature_len
    bytes := try a.Slice[u8](want)
    var f := fs.Open(c.io, path) else e {
        fail c.Fail(path, e)
    }
    onerr f.Abandon()
    n := try ReadAt(c, &f, path, 0, bytes)
    f.Close(c.io) else e {
        fail c.Fail(path, e)
    }
    return try Examine(c, a, path, bytes[..n], none)
}

/// Reads from `pos` until `buf` is full or the file ends, and gives the
/// count.
ReadAt(c: &Ctx, f: &fs.File, path: str, pos: i64, buf: mut []u8): Failed!int {
    var done := 0
    for done < buf.Len() {
        n := f.ReadAt(c.io, pos + i64(done), mut buf[done..]) else e {
            fail c.Fail(path, e)
        }
        if n == 0 {
            break
        }
        done += n
    }
    return done
}

/// Verifies everything an archive says about itself: the signature, the
/// compressed data's size and digest, the decompressed size, and every
/// file's digest.
Verify(c: &Ctx, o: &VerifyOptions): Error!void {
    path := try One(c, "verify", o.archives)
    signer := try Signer(c, o.signer)
    var plain := HeapList[u8].New(c.alloc)
    defer plain.Free(c.alloc)
    bytes := try ReadWhole(c, path, o.key, mut plain)
    checked := try Examine(c, c.arena, path, bytes, signer)
    m := &checked.manifest
    archive.CheckPacked(m, checked.layout.data) else e {
        fail c.Fail(path, e)
    }
    var scratch := try c.arena.Sub()
    defer scratch.Free()
    raw := archive.Decompress(&scratch, m, checked.layout.data) else e {
        fail c.Fail(path, e)
    }
    for e in &m.entries {
        if e.is_dir {
            continue
        }
        start := Trunc[int](e.offset)
        digest := archive.Digest(raw[start..start + Trunc[int](e.size)])
        if !crypto.Equal(digest[..], e.digest[..]) {
            c.Say("pack: {path}: {e.path}: the contents do not match the manifest's digest\n", (path, e))
            fail Failed(code: ExitBad)
        }
    }
    signed := try Signed(&scratch, checked.layout.signature)
    try c.sys.Print("{path}: ok, {f} files, {t} bytes, {signed}\n", (path, f: m.Files(), t: m.total, signed))
}

/// Extracts an archive under `--dest`, streaming a plain one through the
/// inflater and opening a sealed one whole.
Extract(c: &Ctx, o: &ExtractOptions): Error!void {
    path := try One(c, "extract", o.archives)
    dest := o.dest else { "." }
    tree.MakeDir(c.io, dest) else e {
        fail c.Fail(dest, e)
    }
    signer := try Signer(c, o.signer)
    (files, dirs) := try Unpack(c, path, o.key, signer, Action.Extract, dest)
    try c.sys.Print("pack: extracted {files} files and {dirs} directories into {dest}\n", (files, dirs, dest))
}

/// Prints the SHA-256, or with `--sha512` the SHA-512, of every file, in
/// the format sha256sum and sha512sum read.
Sum(c: &Ctx, o: &SumOptions): Error!void {
    path := try One(c, "sum", o.archives)
    action := if o.sha512 { Action.Sum512 } else { Action.Sum256 }
    _ = try Unpack(c, path, o.key, none, action, "")
}

/// Runs `action` over every file of the archive `path`, and gives how
/// many files and directories it holds.
Unpack(c: &Ctx, path: str, key: ?str, signer: ?[32]u8, action: Action, dest: str): Error!(files: int, dirs: int) {
    var scratch := try c.arena.Sub()
    defer scratch.Free()
    var f := fs.Open(c.io, path) else e {
        fail c.Fail(path, e)
    }
    onerr f.Abandon()
    var head: [12]u8 = [0; 12]
    n := try ReadAt(c, &f, path, 0, mut head[..])
    if seal.IsSealed(head[..n]) {
        f.Close(c.io) else e {
            fail c.Fail(path, e)
        }
        var plain := HeapList[u8].New(c.alloc)
        defer plain.Free(c.alloc)
        bytes := try ReadWhole(c, path, key, mut plain)
        checked := try Examine(c, c.arena, path, bytes, signer)
        m := &checked.manifest
        archive.CheckPacked(m, checked.layout.data) else e {
            fail c.Fail(path, e)
        }
        raw := archive.Decompress(&scratch, m, checked.layout.data) else e {
            fail c.Fail(path, e)
        }
        var src := archive.Whole.New(raw)
        try Each(c, &scratch, path, m, mut src, action, dest)
        return (files: m.Files(), dirs: m.entries.Len() - m.Files())
    }
    counts := try Stream(c, mut f, path, signer, action, dest)
    f.Close(c.io) else e {
        fail c.Fail(path, e)
    }
    return counts
}

// The plain archive in `f` past its first bytes: its header, manifest and
// signature, then each file through `action`. A function of its own with an
// arena of its own, so what holds the reader over `f` ends with the call and
// the caller can close the file.
Stream(c: &Ctx, f: mut fs.File, path: str, signer: ?[32]u8, action: Action, dest: str): Error!(files: int, dirs: int) {
    var scratch := try c.arena.Sub()
    defer scratch.Free()
    buf := try scratch.Slice[u8](65536)
    var r := f.Reader(c.io, buf)
    var lead: [12]u8 = [0; 12]
    got := r.ReadAll(mut lead[..]) else {
        fail c.Fail(path, ReadError)
    }
    h := archive.DecodeHeader(lead[..got]) else e {
        fail c.Fail(path, e)
    }
    manifest := try scratch.Slice[u8](h.manifest_len)
    if (r.ReadAll(manifest) else { fail c.Fail(path, ReadError) }) != h.manifest_len {
        fail c.Fail(path, archive.Cut(section: archive.Section.Manifest))
    }
    var signature: [96]u8 = [0; 96]
    signature_len := if h.signed { archive.SignatureLen } else { 0 }
    if (r.ReadAll(mut signature[..signature_len]) else { fail c.Fail(path, ReadError) }) != signature_len {
        fail c.Fail(path, archive.Cut(section: archive.Section.Signature))
    }
    m := try Manifest(c, &scratch, path, manifest[..])
    archive.CheckSignature(manifest[..], signature[..signature_len], signer) else e {
        fail c.Fail(path, e)
    }
    var src := archive.Streamed.New(&scratch, m.codec, r)
    try Each(c, &scratch, path, &m, mut src, action, dest)
    return (files: m.Files(), dirs: m.entries.Len() - m.Files())
}

/// Runs `action` over each entry of `m`, reading the files' bytes from
/// `src`, then checks that the data holds nothing more.
Each[S: archive.Source](c: &Ctx, a: &Arena, path: str, m: &archive.Manifest, src: mut S, action: Action, dest: str): Error!void {
    buf := try a.Slice[u8](65536)
    for i, e in &m.entries {
        match action {
            .Extract { try ExtractEntry(c, a, path, e, i, src, buf, dest) }
            .Sum256 { try SumEntry(c, path, e, i, src, buf, false) }
            .Sum512 { try SumEntry(c, path, e, i, src, buf, true) }
        }
    }
    extra := src.Fill(buf) else e {
        fail c.Fail(path, e)
    }
    if extra != 0 {
        fail c.Fail(path, archive.DataSize)
    }
}

/// Reads the next piece of entry `e` into `buf`, reporting what stops it.
Piece[S: archive.Source](c: &Ctx, path: str, src: mut S, h: mut hash.Blake3State, buf: mut []u8, left: i64): Failed!int {
    return archive.ReadPiece(src, h, buf, left) else e {
        fail c.Fail(path, e)
    }
}

/// Writes entry `e` under `dest`, and removes it again when its digest does
/// not match.
ExtractEntry[S: archive.Source](c: &Ctx, a: &Arena, path: str, e: &archive.Entry, index: int, src: mut S, buf: mut []u8, dest: str): Error!void {
    full := try a.Fmt("{dest}/{p}", (dest, p: e.path))
    if e.is_dir {
        tree.MakeDir(c.io, full) else err {
            fail c.Fail(full, err)
        }
        return
    }
    var f := fs.Create(c.io, full) else err {
        fail c.Fail(full, err)
    }
    onerr Discard(c.io, f, full)
    var h := hash.Blake3State.New()
    var left := e.size
    for left > 0 {
        n := try Piece(c, path, src, mut h, buf, left)
        f.WriteAll(c.io, buf[..n]) else err {
            fail c.Fail(full, err)
        }
        left -= i64(n)
    }
    archive.CheckEntry(e, index, h) else {
        c.Say("pack: {path}: {e.path}: the contents do not match the manifest's digest\n", (path, e))
        fail Failed(code: ExitBad)
    }
    f.Close(c.io) else err {
        fail c.Fail(full, err)
    }
}

/// Prints the SHA-256 or SHA-512 of entry `e`, after checking its BLAKE3.
SumEntry[S: archive.Source](c: &Ctx, path: str, e: &archive.Entry, index: int, src: mut S, buf: mut []u8, wide: bool): Error!void {
    if e.is_dir {
        return
    }
    var h := hash.Blake3State.New()
    var narrow_sum := hash.Sha256State.New()
    var wide_sum := hash.Sha512State.New()
    var left := e.size
    for left > 0 {
        n := try Piece(c, path, src, mut h, buf, left)
        if wide {
            wide_sum.Bytes(buf[..n])
        } else {
            narrow_sum.Bytes(buf[..n])
        }
        left -= i64(n)
    }
    archive.CheckEntry(e, index, h) else {
        c.Say("pack: {path}: {e.path}: the contents do not match the manifest's digest\n", (path, e))
        fail Failed(code: ExitBad)
    }
    if wide {
        sum := wide_sum.Sum()
        try c.sys.Print("{h}  {e.path}\n", (h: hex.Hex(bytes: sum[..]), e))
    } else {
        sum := narrow_sum.Sum()
        try c.sys.Print("{h}  {e.path}\n", (h: hex.Hex(bytes: sum[..]), e))
    }
}
