// utf8scan, C#, idiomatic std: the task of README.md with
// System.Text.Unicode.Utf8.IsValid and System.Text.Rune.DecodeFromUtf8 over a
// generated mixed-script UTF-8 buffer (ASCII, Latin-1, Greek, Cyrillic, CJK,
// and 4-byte emoji), folding codepoint count and scalar sum each round and
// toggling one ASCII letter between rounds. No unsafe, no MethodImpl, no
// Unsafe.*.
using System;
using System.Buffers;
using System.Text;
using System.Text.Unicode;

internal static class Program
{
    private const string UnitStr =
        "The quick brown fox jumps over 42 lazy dogs! Caf\u00e9 na\u00efve r\u00e9sum\u00e9 \u00fcber\n" +
        "\u0391\u03bb\u03b3\u03cc\u03c1\u03b9\u03b8\u03bc\u03bf\u03c2 \u03ba\u03b1\u03b9 \u041f\u0440\u0438\u0432\u0435\u0442 \u043c\u0438\u0440 2026\n" +
        "\u65e5\u672c\u8a9e\u30c6\u30ad\u30b9\u30c8 \u4e2d\u6587\u6d4b\u8bd5 \ud55c\uad6d\uc5b4 \u6c34\u306f\u51b7\u305f\u3044\n" +
        "Status: OK \U0001f600\U0001f680\U0001f30d\U0001f525\u2728\U0001f4bb\U0001f389 end-of-record.\n";

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: utf8scan <n> <rounds>");
            return 2;
        }
        int n = int.Parse(args[0]);
        int rounds = int.Parse(args[1]);
        byte[] ub = Encoding.UTF8.GetBytes(UnitStr);
        int ulen = ub.Length;
        byte[] buf = new byte[n * ulen];
        for (int i = 0; i < n; i++)
        {
            Buffer.BlockCopy(ub, 0, buf, i * ulen, ulen);
        }
        ulong total = 0;
        for (int r = 0; r < rounds; r++)
        {
            ReadOnlySpan<byte> span = buf;
            if (!Utf8.IsValid(span))
            {
                return 1;
            }
            ulong count = 0;
            ulong cpsum = 0;
            while (!span.IsEmpty)
            {
                if (Rune.DecodeFromUtf8(span, out Rune rune, out int consumed) != OperationStatus.Done)
                {
                    return 1;
                }
                count++;
                cpsum += (ulong)rune.Value;
                span = span.Slice(consumed);
            }
            total += cpsum + count;
            int idx = (r % n) * ulen;
            buf[idx] ^= 0x20;
        }
        Console.WriteLine(total);
        return 0;
    }
}
