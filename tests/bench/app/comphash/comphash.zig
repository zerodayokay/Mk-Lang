// comphash, Zig, std: the task of README.md. Pass 1 reads the input file in
// 64 KiB chunks through a streaming File.Reader, hashing each chunk with
// std.crypto.hash.sha2.Sha256 and compressing it with std.compress.flate.Compress
// (.zlib, .level_6) through a 64 KiB streaming File.Writer. Pass 2 reads the
// compressed file back through a 64 KiB streaming File.Reader, checks RFC 1950
// header FCHECK and FDICT, inflates with std.compress.flate.Decompress (.zlib)
// into 64 KiB chunks while computing both std's zlib Adler-32 hasher (which
// Zig 0.17's Decompress does not compare) and a second Sha256, verifies the
// Adler-32 trailer, decompressed length and SHA-256 against Pass 1, and prints
// "<bytes> <hex-sha256>". Zig chooses SHA instructions at compile time from
// the target CPU features; x86-64-v2 has none.
// Usage: comphash <in> <out>.
const std = @import("std");
const flate = std.compress.flate;
const Sha256 = std.crypto.hash.sha2.Sha256;

const chunk_size: usize = 64 << 10;

const HashResult = struct {
    total: u64,
    digest: [Sha256.digest_length]u8,
};

fn compressAndHash(
    io: std.Io,
    in_path: []const u8,
    out_path: []const u8,
    rbuf: []u8,
    wbuf: []u8,
    buf: []u8,
    window: []u8,
) !HashResult {
    const cwd = std.Io.Dir.cwd();
    const src = try cwd.openFile(io, in_path, .{});
    defer src.close(io);
    const dst = try cwd.createFile(io, out_path, .{});
    defer dst.close(io);

    var fr = src.readerStreaming(io, rbuf);
    var fw = dst.writerStreaming(io, wbuf);
    var comp = try flate.Compress.init(&fw.interface, window, .zlib, .level_6);
    var sha = Sha256.init(.{});
    var total: u64 = 0;

    while (true) {
        const n = try fr.interface.readSliceShort(buf);
        if (n == 0) break;
        sha.update(buf[0..n]);
        try comp.writer.writeAll(buf[0..n]);
        total += n;
        if (n < buf.len) break;
    }
    try comp.finish();
    try fw.interface.flush();

    var digest: [Sha256.digest_length]u8 = undefined;
    sha.final(&digest);
    return .{ .total = total, .digest = digest };
}

fn decompressAndHash(
    io: std.Io,
    in_path: []const u8,
    rbuf: []u8,
    buf: []u8,
    window: []u8,
) !HashResult {
    const cwd = std.Io.Dir.cwd();
    const src = try cwd.openFile(io, in_path, .{});
    defer src.close(io);

    var fr = src.readerStreaming(io, rbuf);
    // RFC 1950 2.3: verify FCHECK and refuse FDICT; Zig 0.17 Decompress checks
    // CM and CINFO only.
    const hdr = try fr.interface.peekArray(2);
    const cmf_flg = (@as(u16, hdr[0]) << 8) | hdr[1];
    if (cmf_flg % 31 != 0 or hdr[1] & 0x20 != 0) return error.BadZlibHeader;

    var d: flate.Decompress = .init(&fr.interface, .zlib, window);
    var adler: flate.Container.Hasher = .init(.zlib);
    var sha = Sha256.init(.{});
    var total: u64 = 0;

    while (true) {
        const n = try d.reader.readSliceShort(buf);
        if (n > 0) {
            adler.update(buf[0..n]);
            sha.update(buf[0..n]);
            total += n;
        }
        if (n < buf.len) break;
    }
    if (d.container_metadata.zlib.adler != adler.zlib.adler) return error.WrongZlibChecksum;

    var digest: [Sha256.digest_length]u8 = undefined;
    sha.final(&digest);
    return .{ .total = total, .digest = digest };
}

pub fn main(init: std.process.Init) !void {
    const args = try init.minimal.args.toSlice(init.arena.allocator());
    if (args.len != 3) return error.Usage;
    const io = init.io;
    const pa = std.heap.page_allocator;

    const rbuf = try pa.alloc(u8, chunk_size);
    defer pa.free(rbuf);
    const wbuf = try pa.alloc(u8, chunk_size);
    defer pa.free(wbuf);
    const buf = try pa.alloc(u8, chunk_size);
    defer pa.free(buf);
    const window = try pa.alloc(u8, flate.max_window_len);
    defer pa.free(window);

    const in_res = try compressAndHash(io, args[1], args[2], rbuf, wbuf, buf, window);
    const out_res = try decompressAndHash(io, args[2], rbuf, buf, window);

    if (out_res.total != in_res.total) return error.SizeDiffers;
    if (!std.mem.eql(u8, &out_res.digest, &in_res.digest)) return error.DigestDiffers;

    const hex = std.fmt.bytesToHex(in_res.digest, .lower);
    var out: [128]u8 = undefined;
    var w = std.Io.File.stdout().writer(io, &out);
    try w.interface.print("{d} {s}\n", .{ in_res.total, hex });
    try w.interface.flush();
}
