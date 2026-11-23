// strops-split, C#: split at 'e', trim each piece, and iterate whitespace words using MemoryExtensions.Split/SplitAny (README.md).
// Usage: strops-split <file> <rounds>.
using System;
using System.Buffers;
using System.Globalization;
using System.IO;
using System.Text;

internal static class Program
{
    private static readonly SearchValues<char> Whitespace = SearchValues.Create(" \t\n\v\f\r");

    private static ulong Round(string text)
    {
        ulong total = 0;
        ulong h = 0xcbf29ce484222325UL;
        ReadOnlySpan<char> span = text.AsSpan();
        foreach (Range r in span.Split('e'))
        {
            ReadOnlySpan<char> t = span[r].Trim();
            total++;
            h = (h ^ (ulong)t.Length) * 0x100000001b3UL;
        }
        total += h;
        foreach (Range r in span.SplitAny(Whitespace))
        {
            ReadOnlySpan<char> w = span[r];
            if (w.IsEmpty)
            {
                continue;
            }
            total += (ulong)w.Length + 1UL;
        }
        return total;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: strops-split <file> <rounds>");
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
