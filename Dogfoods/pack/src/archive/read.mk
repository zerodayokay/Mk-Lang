// Why: verifying and extracting must work on an archive in memory and on one
// streamed from disk, without two copies of the checking code.
//
// What: Layout and Split for the sections, CheckSignature, CheckPacked,
// Decompress, the Source interface with Whole and Streamed, ReadPiece and
// CheckEntry.
//
// How: both sources feed ReadPiece, which hashes each piece with BLAKE3 as it
// goes, so an entry's digest is checked without holding the entry.

import std.compress
import std.crypto
import std.hash

/// An archive's sections, as views of the bytes it was read from.
export struct Layout {
    export header: Header
    export manifest: []u8
    /// The signer's public key and signature, or empty when unsigned.
    export signature: []u8
    export data: []u8
}

/// Splits a whole archive into its sections. Fails with the errors of
/// `DecodeHeader`, and with `Cut` when a section runs past the end. O(1).
export Split(b: []u8): NotArchive|Unsupported|Cut|BadHeader!Layout from b {
    h := try DecodeHeader(b)
    manifest_end := HeaderLen + h.manifest_len
    if b.Len() < manifest_end {
        fail Cut(section: Section.Manifest)
    }
    signature_end := if h.signed { manifest_end + SignatureLen } else { manifest_end }
    if b.Len() < signature_end {
        fail Cut(section: Section.Signature)
    }
    return Layout(header: h, manifest: b[HeaderLen..manifest_end], signature: b[manifest_end..signature_end], data: b[signature_end..])
}

/// Checks the signature block against the manifest. With `signer`, the
/// archive must be signed by that key: `Unsigned` when it is not signed,
/// `OtherSigner` when another key signed it. Without, an unsigned archive
/// passes. `BadSignature` when the signature does not verify. O(n) in the
/// manifest; allocates nothing.
export CheckSignature(manifest: []u8, signature: []u8, signer: ?[32]u8): Unsigned|OtherSigner|BadSignature!void {
    if signature.Len() == 0 {
        if signer.IsSome() {
            fail Unsigned
        }
        return
    }
    var public: [32]u8 = [0; 32]
    public.CopyFrom(signature[..32])
    if want := signer {
        if !crypto.Equal(want[..], public[..]) {
            fail OtherSigner
        }
    }
    var sig: [64]u8 = [0; 64]
    sig.CopyFrom(signature[32..96])
    if !crypto.Ed25519.Verify(&public, manifest, &sig) {
        fail BadSignature
    }
}

/// Checks the compressed data against the manifest's size and digest:
/// `DataSize`, then `DataDigest`. O(n); allocates nothing.
export CheckPacked(m: &Manifest, data: []u8): DataSize|DataDigest!void {
    if i64(data.Len()) != m.packed {
        fail DataSize
    }
    if !crypto.Equal(Digest(data)[..], m.packed_digest[..]) {
        fail DataDigest
    }
}

/// Decompresses the whole data, which must hold exactly `m.total` bytes:
/// `TooLarge` when it holds more, `DataSize` when fewer, and the codec's
/// other errors. O(n); the result lives in `a`.
export Decompress(a: &Arena, m: &Manifest, data: []u8): compress.Error|DataSize![]u8 from a {
    limit := Trunc[int](m.total)
    raw := match m.codec {
        .Deflate { try compress.Inflate(a, data, limit) }
        .Gzip { try compress.Gunzip(a, data, limit) }
    }
    if i64(raw.Len()) != m.total {
        fail DataSize
    }
    return raw
}

/// The decompressed bytes of an archive's data, read in pieces.
export interface Source {
    Fill(mut self, buf: mut []u8): compress.Error|ReadError!int
}

/// Data already decompressed in memory.
export view struct Whole {
    data: []u8
    pos: int
}

/// A source over `data`.
export Whole.New(data: []u8): Whole {
    return Whole(data: data, pos: 0)
}

impl Source for Whole {
    Fill(mut self, buf: mut []u8): compress.Error|ReadError!int {
        n := if buf.Len() < self.data.Len() - self.pos { buf.Len() } else { self.data.Len() - self.pos }
        (mut buf[..n]).CopyFrom(self.data[self.pos..self.pos + n])
        self.pos += n
        return n
    }
}

/// Data decompressed as it is read, from the rest of an archive's reader.
export view struct Streamed {
    inflater: compress.Inflater
    reader: Reader
}

/// A source that inflates what `reader` gives with `codec`'s framing. The
/// inflater's window comes from `a` at the first read.
export Streamed.New(a: &Arena, codec: Codec, reader: Reader): Streamed from a, reader {
    inflater := match codec {
        .Deflate { compress.Inflater.New(a) }
        .Gzip { compress.Inflater.NewGzip(a) }
    }
    return Streamed(inflater: inflater, reader: reader)
}

impl Source for Streamed {
    Fill(mut self, buf: mut []u8): compress.Error|ReadError!int {
        return try self.inflater.Read(mut self.reader, buf)
    }
}

/// Reads the next piece of an entry, at most `left` bytes, into `buf`, and
/// feeds it to `h`. Fails with `Cut` in the data when the source ends first,
/// and with the source's errors. O(n) in the piece; allocates nothing.
export ReadPiece[S: Source](src: mut S, h: mut hash.Blake3State, buf: mut []u8, left: i64): Cut|compress.Error|ReadError!int {
    want := if left < i64(buf.Len()) { Trunc[int](left) } else { buf.Len() }
    n := try src.Fill(mut buf[..want])
    if n == 0 {
        fail Cut(section: Section.Data)
    }
    h.Bytes(buf[..n])
    return n
}

/// Checks a finished entry's digest. `EntryDigest` names its index. O(1).
export CheckEntry(e: &Entry, index: int, h: hash.Blake3State): EntryDigest!void {
    if !crypto.Equal(h.Sum()[..], e.digest[..]) {
        fail EntryDigest(index: index)
    }
}
