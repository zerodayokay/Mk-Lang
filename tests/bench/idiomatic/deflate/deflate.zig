// deflate, Zig, idiomatic std: the task of README.md with
// std.compress.flate.Compress (.raw, .default) and std.compress.flate.Decompress
// (.raw) round-tripping a generated mixed text buffer in memory. Reusable
// window and I/O buffers are allocated once before the loop; each round
// verifies exact output length and FNV-1a 64 checksum against the source
// buffer and flips one source byte between rounds.
const std = @import("std");
const flate = std.compress.flate;

const hex = "0123456789abcdef";

const vocab = [16][]const u8{
    "ERROR", "WARN ", "INFO ", "DEBUG",
    "GET", "POST", "PUT", "DELETE",
    "/api/v1/users", "/api/v1/orders", "/api/v1/items", "/healthz",
    "timeout", "ok", "cached", "retry",
};

const prose = "The quick brown fox jumps over the lazy dog; pack my box with five dozen liquor jugs! 0123456789 ";

fn mix(x: u64) u64 {
    var z = x;
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9;
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb;
    return z ^ (z >> 31);
}

fn fnv(b: []const u8) u64 {
    var h: u64 = 0xcbf29ce484222325;
    for (b) |c| h = (h ^ c) *% 0x100000001b3;
    return h;
}

fn emit(dst: []u8, pos: *usize, s: []const u8) void {
    const rem = dst.len - pos.*;
    if (rem == 0) return;
    const k = @min(s.len, rem);
    @memcpy(dst[pos.* .. pos.* + k], s[0..k]);
    pos.* += k;
}

fn generate(dst: []u8, seed: u64) void {
    var st = seed;
    var pos: usize = 0;
    const n = dst.len;
    while (pos < n) {
        st +%= 0x9e3779b97f4a7c15;
        const z = mix(st);
        emit(dst, &pos, "ts=");
        for (0..8) |k| {
            if (pos < n) {
                const shift: u6 = @intCast(60 - 4 * k);
                dst[pos] = hex[@as(usize, @intCast((z >> shift) & 15))];
                pos += 1;
            }
        }
        emit(dst, &pos, " level=");
        emit(dst, &pos, vocab[@as(usize, @intCast(z & 3))]);
        emit(dst, &pos, " method=");
        emit(dst, &pos, vocab[4 + @as(usize, @intCast((z >> 2) & 3))]);
        emit(dst, &pos, " path=");
        emit(dst, &pos, vocab[8 + @as(usize, @intCast((z >> 4) & 3))]);
        emit(dst, &pos, " msg=");
        emit(dst, &pos, vocab[12 + @as(usize, @intCast((z >> 6) & 3))]);
        emit(dst, &pos, " body=");
        const off: usize = @intCast((z >> 8) % 60);
        const plen: usize = 16 + @as(usize, @intCast((z >> 16) % 21));
        emit(dst, &pos, prose[off .. off + plen]);
        emit(dst, &pos, "\n");
    }
}

pub fn main(init: std.process.Init) !void {
    const args = try init.minimal.args.toSlice(init.arena.allocator());
    if (args.len != 4) return error.Usage;
    const n = try std.fmt.parseInt(usize, args[1], 10);
    const rounds = try std.fmt.parseInt(usize, args[2], 10);
    const seed = try std.fmt.parseInt(u64, args[3], 10);
    const pa = std.heap.page_allocator;
    const src = try pa.alloc(u8, n);
    defer pa.free(src);
    generate(src, seed);
    const comp_win = try pa.alloc(u8, flate.max_window_len);
    defer pa.free(comp_win);
    const dec_win = try pa.alloc(u8, flate.max_window_len);
    defer pa.free(dec_win);
    const comp_buf = try pa.alloc(u8, n + (n >> 4) + (1 << 20));
    defer pa.free(comp_buf);
    const dec_buf = try pa.alloc(u8, n + 64);
    defer pa.free(dec_buf);
    var total: u64 = 0;
    for (0..rounds) |r| {
        const want = fnv(src);
        var out: std.Io.Writer = .fixed(comp_buf);
        var c = try flate.Compress.init(&out, comp_win, .raw, .default);
        try c.writer.writeAll(src);
        try c.finish();
        const comp = comp_buf[0..out.end];
        var in: std.Io.Reader = .fixed(comp);
        var d: flate.Decompress = .init(&in, .raw, dec_win);
        var dec_len: usize = 0;
        while (true) {
            const avail = dec_buf[dec_len..];
            if (avail.len == 0) return error.OutputOverflow;
            const k = try d.reader.readSliceShort(avail);
            dec_len += k;
            if (k < avail.len) break;
        }
        if (dec_len != n) return error.LengthMismatch;
        const got = fnv(dec_buf[0..dec_len]);
        if (got != want) return error.ChecksumMismatch;
        total = total +% got +% dec_len;
        const idx = (r * 1009) % n;
        src[idx] ^= 0x01;
    }
    var obuf: [64]u8 = undefined;
    var w = std.Io.File.stdout().writer(init.io, &obuf);
    try w.interface.print("{d}\n", .{total});
    try w.interface.flush();
}
