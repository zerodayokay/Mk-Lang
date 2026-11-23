// fasta, Zig: the reference algorithm of README.md (the Benchmarks Game's
// fasta generator, hashed by line into an i64 checksum). ReleaseSafe checks
// bounds and traps overflow; ReleaseFast checks neither. No hints.
const std = @import("std");

// tools/bench flips this line in a copy for the counting build.
const bench_count = false;

const IM: i64 = 139968;
const IA: i64 = 3877;
const IC: i64 = 29573;
const LINE_LEN: usize = 60;

const Freq = struct {
    ch: u8,
    p: f64,
};

fn makeCumulative(tab: []Freq) void {
    var cp: f64 = 0.0;
    for (tab) |*e| {
        cp = cp + e.p;
        e.p = cp;
    }
    tab[tab.len - 1].p = 1.0;
}

fn nextRand(seed: *i64) f64 {
    seed.* = @rem(seed.* * IA + IC, IM);
    return @as(f64, @floatFromInt(seed.*)) / @as(f64, @floatFromInt(IM));
}

fn lineSum(buf: []const u8, m: usize) i64 {
    var s: i64 = 0;
    for (0..m) |k| {
        s += @as(i64, buf[k]) * @as(i64, @intCast(k + 1));
    }
    return s;
}

fn repeatFasta(alu: []const u8, count: i64) i64 {
    var buf: [61]u8 = undefined;
    const alu_len = alu.len;
    var pos: usize = 0;
    var rem: i64 = count;
    var total: i64 = 0;
    while (rem > 0) {
        const m: usize = if (rem < @as(i64, LINE_LEN)) @intCast(rem) else LINE_LEN;
        for (0..m) |k| {
            buf[k] = alu[pos];
            pos += 1;
            if (pos == alu_len) {
                pos = 0;
            }
        }
        buf[m] = 10;
        total += lineSum(&buf, m + 1);
        rem -= @as(i64, @intCast(m));
    }
    return total;
}

fn randomFasta(tab: []const Freq, count: i64, seed: *i64, probes: *u64) i64 {
    var buf: [61]u8 = undefined;
    var rem: i64 = count;
    var total: i64 = 0;
    while (rem > 0) {
        const m: usize = if (rem < @as(i64, LINE_LEN)) @intCast(rem) else LINE_LEN;
        for (0..m) |k| {
            const r = nextRand(seed);
            var j: usize = 0;
            while (true) {
                if (bench_count) {
                    probes.* += 1;
                }
                if (tab[j].p >= r) {
                    break;
                }
                j += 1;
            }
            buf[k] = tab[j].ch;
        }
        buf[m] = 10;
        total += lineSum(&buf, m + 1);
        rem -= @as(i64, @intCast(m));
    }
    return total;
}

pub fn main(init: std.process.Init) !void {
    var args = try init.minimal.args.iterateAllocator(init.gpa);
    defer args.deinit();
    _ = args.next();
    const arg1 = args.next() orelse return error.InvalidArguments;
    const n = try std.fmt.parseInt(i64, arg1, 10);
    const alu = "GGCCGGGCGCGGTGGCTCACGCCTGTAATCCCAGCACTTTGGGAGGCCGAGGCGGGCGGATCACCTGAGGTCAGGAGTTCGAGACCAGCCTGGCCAACATGGTGAAACCCCGTCTCTACTAAAAATACAAAAATTAGCCGGGCGTGGTGGCGCGCGCCTGTAATCCCAGCTACTCGGGAGGCTGAGGCAGGAGAATCGCTTGAACCCGGGAGGCGGAGGTTGCAGTGAGCCGAGATCGCGCCACTGCACTCCAGCCTGGGCGACAGAGCGAGACTCCGTCTCAAAAA";
    var iub = [_]Freq{
        .{ .ch = 'a', .p = 0.27 }, .{ .ch = 'c', .p = 0.12 }, .{ .ch = 'g', .p = 0.12 }, .{ .ch = 't', .p = 0.27 },
        .{ .ch = 'B', .p = 0.02 }, .{ .ch = 'D', .p = 0.02 }, .{ .ch = 'H', .p = 0.02 }, .{ .ch = 'K', .p = 0.02 },
        .{ .ch = 'M', .p = 0.02 }, .{ .ch = 'N', .p = 0.02 }, .{ .ch = 'R', .p = 0.02 }, .{ .ch = 'S', .p = 0.02 },
        .{ .ch = 'V', .p = 0.02 }, .{ .ch = 'W', .p = 0.02 }, .{ .ch = 'Y', .p = 0.02 },
    };
    var homo = [_]Freq{
        .{ .ch = 'a', .p = 0.3029549426680 },
        .{ .ch = 'c', .p = 0.1979883004921 },
        .{ .ch = 'g', .p = 0.1975473066391 },
        .{ .ch = 't', .p = 0.3015094502008 },
    };
    makeCumulative(&iub);
    makeCumulative(&homo);
    var seed: i64 = 42;
    var probes: u64 = 0;
    var total: i64 = 0;
    total += repeatFasta(alu, 2 * n);
    total += randomFasta(&iub, 3 * n, &seed, &probes);
    total += randomFasta(&homo, 5 * n, &seed, &probes);
    var out_buf: [256]u8 = undefined;
    var out = std.Io.File.stdout().writer(init.io, &out_buf);
    try out.interface.print("{d}\n", .{total});
    if (bench_count) {
        try out.interface.print("counts probes={d}\n", .{probes});
    }
    try out.interface.flush();
}
