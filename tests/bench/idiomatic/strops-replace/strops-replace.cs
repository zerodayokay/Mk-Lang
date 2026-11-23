// strops-replace, C#: chain three non-overlapping string.Replace calls per line and hash with FNV-1a 64 (README.md).
// Usage: strops-replace <file> <rounds>.
using System;
using System.Globalization;
using System.IO;
using System.Text;

internal static class Program
{
    private static ulong Round(string text)
    {
        ulong h = 0xcbf29ce484222325UL;
        ulong n = 0;
        ReadOnlySpan<char> span = text.AsSpan();
        foreach (Range r in span.Split('\n'))
        {
            string line = text[r];
            string a = line.Replace("e", "EEE", StringComparison.Ordinal);
            string b = a.Replace("s ", "S", StringComparison.Ordinal);
            string c = b.Replace(" a", "_A", StringComparison.Ordinal);
            for (int i = 0; i < c.Length; i++)
            {
                h = (h ^ (byte)c[i]) * 0x100000001b3UL;
            }
            n += (ulong)c.Length;
        }
        return h + n;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: strops-replace <file> <rounds>");
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
