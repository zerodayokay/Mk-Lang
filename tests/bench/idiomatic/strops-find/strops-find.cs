// strops-find, C#: find all non-overlapping occurrences of 8 needles per line with ReadOnlySpan<char>.IndexOf (README.md).
// Usage: strops-find <file> <rounds>.
using System;
using System.Globalization;
using System.IO;
using System.Text;

internal static class Program
{
    private static readonly string[] Needles =
    {
        "the", "ing", "qzx", "e a", "tion", "s an d", "abcdefgh", "xylophone quartz",
    };

    private static ulong Round(string text)
    {
        ulong total = 0;
        ReadOnlySpan<char> span = text.AsSpan();
        foreach (Range r in span.Split('\n'))
        {
            ReadOnlySpan<char> line = span[r];
            for (int k = 0; k < Needles.Length; k++)
            {
                ReadOnlySpan<char> nd = Needles[k].AsSpan();
                int p = 0;
                while (true)
                {
                    int q = line.Slice(p).IndexOf(nd, StringComparison.Ordinal);
                    if (q < 0)
                    {
                        break;
                    }
                    int at = p + q;
                    total += (ulong)at + 1UL;
                    p = at + nd.Length;
                }
            }
        }
        return total;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: strops-find <file> <rounds>");
            return 2;
        }
        string text = File.ReadAllText(args[0], new UTF8Encoding(false, true));
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);
        ulong total = 0;
        for (int r = 0; r < rounds; r++)
        {
            total += Round(text);
        }
        Console.WriteLine(total);
        return 0;
    }
}
