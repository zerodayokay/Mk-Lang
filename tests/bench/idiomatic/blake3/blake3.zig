// blake3, Zig, idiomatic std: the task of README.md with
// std.crypto.hash.Blake3 over a generated multi-MiB SplitMix64 buffer:
// bulk_reps full-buffer passes plus short_reps passes over 64-byte slices,
// XOR-folding each 32-byte digest's first 8 little-endian bytes and mutating
// one buffer byte after each pass.
const std = @import("std");

fn mix(x: u64) u64 {
    var z = x;
    z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9;
    z = (z ^ (z >> 27)) *% 0x94d049bb133111eb;
    return z ^ (z >> 31);
}

fn blake3Lo64(b: []const u8) u64 {
    var out: [32]u8 = undefined;
    std.crypto.hash.Blake3.hash(b, &out, .{});
    return std.mem.readInt(u64, out[0..8], .little);
}

pub fn main(init: std.process.Init) !void {
    const args = try init.minimal.args.toSlice(init.arena.allocator());
    if (args.len != 5) return error.Usage;
    const mib = try std.fmt.parseInt(usize, args[1], 10);
    const bulk_reps = try std.fmt.parseInt(usize, args[2], 10);
    const short_reps = try std.fmt.parseInt(usize, args[3], 10);
    var st = try std.fmt.parseInt(u64, args[4], 10);
    const n = mib * 1048576;
    const pa = std.heap.page_allocator;
    const buf = try pa.alloc(u8, n);
    defer pa.free(buf);
    var i: usize = 0;
    while (i < n) : (i += 8) {
        st +%= 0x9e3779b97f4a7c15;
        std.mem.writeInt(u64, buf[i..][0..8], mix(st), .little);
    }
    var sum: u64 = 0;
    for (0..bulk_reps) |r| {
        const h = blake3Lo64(buf);
        sum ^= h;
        buf[r % n] ^= @truncate(h);
    }
    for (0..short_reps) |r| {
        var j: usize = 0;
        while (j < n) : (j += 64) {
            sum ^= blake3Lo64(buf[j .. j + 64]);
        }
        buf[(r * 997) % n] ^= @truncate(sum);
    }
    var obuf: [64]u8 = undefined;
    var w = std.Io.File.stdout().writer(init.io, &obuf);
    try w.interface.print("{d}\n", .{sum});
    try w.interface.flush();
}
