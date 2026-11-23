// strbuild, C#, idiomatic std: the task of README.md with
// System.Text.StringBuilder growing from empty per document, appending
// literals, string slices (Append(string, int, int)), repeated chunks and
// formatted integers, then hashing the built ASCII characters via GetChunks()
// with FNV-1a 64 plus length. No unsafe, no MethodImpl, no Unsafe.*.
using System;
using System.Text;

internal static class Program
{
    private static readonly string[] Words =
    {
        "alpha", "bravo", "charlie", "delta",
        "echo", "foxtrot", "golf", "hotel",
        "india", "juliet", "kilo", "lima",
        "mike", "november", "oscar", "papa",
    };

    private const string Src = "the quick brown fox jumps over the lazy dog 0123456789 ABCDEFGHIJKLMNOPQRSTUVWXYZ ";

    private static ulong BuildDoc(int d, int lines)
    {
        var sb = new StringBuilder();
        for (int i = 0; i < lines; i++)
        {
            int x = d * 131 + i * 17;
            sb.Append("rec[");
            sb.Append(i);
            sb.Append("] ");
            sb.Append(Words[x & 15]);
            sb.Append(':');
            int off = (x * 7) % 40;
            int slen = 8 + (x % 25);
            sb.Append(Src, off, slen);
            int reps = (i & 3) + 1;
            for (int k = 0; k < reps; k++)
            {
                sb.Append("-=");
            }
            sb.Append(" #");
            sb.Append(x);
            sb.Append('\n');
        }
        ulong h = 0xcbf29ce484222325UL;
        foreach (ReadOnlyMemory<char> chunk in sb.GetChunks())
        {
            ReadOnlySpan<char> span = chunk.Span;
            for (int i = 0; i < span.Length; i++)
            {
                h = (h ^ (byte)span[i]) * 0x100000001b3UL;
            }
        }
        return h + (ulong)sb.Length;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: strbuild <docs> <lines>");
            return 2;
        }
        int docs = int.Parse(args[0]);
        int lines = int.Parse(args[1]);
        ulong total = 0;
        for (int d = 0; d < docs; d++)
        {
            total += BuildDoc(d, lines);
        }
        Console.WriteLine(total);
        return 0;
    }
}
