// utf8scan, Zig, idiomatic std: the task of README.md with
// std.unicode.utf8ValidateSlice and std.unicode.Utf8View over a generated
// mixed-script UTF-8 buffer (ASCII, Latin-1, Greek, Cyrillic, CJK, and
// 4-byte emoji), folding codepoint count and scalar sum each round and
// toggling one ASCII letter between rounds.
const std = @import("std");

const unit = "The quick brown fox jumps over 42 lazy dogs! Caf\u{00e9} na\u{00ef}ve r\u{00e9}sum\u{00e9} \u{00fc}ber\n\u{0391}\u{03bb}\u{03b3}\u{03cc}\u{03c1}\u{03b9}\u{03b8}\u{03bc}\u{03bf}\u{03c2} \u{03ba}\u{03b1}\u{03b9} \u{041f}\u{0440}\u{0438}\u{0432}\u{0435}\u{0442} \u{043c}\u{0438}\u{0440} 2026\n\u{65e5}\u{672c}\u{8a9e}\u{30c6}\u{30ad}\u{30b9}\u{30c8} \u{4e2d}\u{6587}\u{6d4b}\u{8bd5} \u{d55c}\u{ad6d}\u{c5b4} \u{6c34}\u{306f}\u{51b7}\u{305f}\u{3044}\nStatus: OK \u{1f600}\u{1f680}\u{1f30d}\u{1f525}\u{2728}\u{1f4bb}\u{1f389} end-of-record.\n";

pub fn main(init: std.process.Init) !void {
    const args = try init.minimal.args.toSlice(init.arena.allocator());
    if (args.len != 3) return error.Usage;
    const n = try std.fmt.parseInt(usize, args[1], 10);
    const rounds = try std.fmt.parseInt(usize, args[2], 10);
    const pa = std.heap.page_allocator;
    const ulen = unit.len;
    const buf = try pa.alloc(u8, n * ulen);
    defer pa.free(buf);
    for (0..n) |i| @memcpy(buf[i * ulen .. (i + 1) * ulen], unit);
    var total: u64 = 0;
    for (0..rounds) |r| {
        if (!std.unicode.utf8ValidateSlice(buf)) return error.InvalidUtf8;
        const view = std.unicode.Utf8View.initUnchecked(buf);
        var it = view.iterator();
        var count: u64 = 0;
        var cpsum: u64 = 0;
        while (it.nextCodepoint()) |c| {
            count +%= 1;
            cpsum +%= c;
        }
        total = total +% cpsum +% count;
        const idx = (r % n) * ulen;
        buf[idx] ^= 0x20;
    }
    var obuf: [64]u8 = undefined;
    var w = std.Io.File.stdout().writer(init.io, &obuf);
    try w.interface.print("{d}\n", .{total});
    try w.interface.flush();
}
