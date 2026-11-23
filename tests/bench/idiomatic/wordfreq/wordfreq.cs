// wordfreq, C#: count word frequencies with Dictionary<string, ulong> and ReadOnlySpan<char> AlternateLookup (README.md).
// Uses StringComparer.Ordinal (Marvin32 with a per-process random seed, DoS-resistant); keys are borrowed spans on lookup.
// Usage: wordfreq <file> <rounds>.
using System;
using System.Buffers;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;

internal static class Program
{
    private static readonly SearchValues<char> Whitespace = SearchValues.Create(" \t\n\v\f\r");

    private static ulong Fnv(string w)
    {
        ulong h = 0xcbf29ce484222325UL;
        for (int i = 0; i < w.Length; i++)
        {
            h = (h ^ (byte)w[i]) * 0x100000001b3UL;
        }
        return h;
    }

    private static ulong Round(string text)
    {
        var m = new Dictionary<string, ulong>(StringComparer.Ordinal);
        Dictionary<string, ulong>.AlternateLookup<ReadOnlySpan<char>> lookup = m.GetAlternateLookup<ReadOnlySpan<char>>();
        ReadOnlySpan<char> span = text.AsSpan();
        foreach (Range r in span.SplitAny(Whitespace))
        {
            ReadOnlySpan<char> w = span[r];
            if (w.IsEmpty)
            {
                continue;
            }
            ref ulong count = ref CollectionsMarshal.GetValueRefOrAddDefault(lookup, w, out _);
            count++;
        }
        ulong sum = 0;
        foreach (KeyValuePair<string, ulong> kv in m)
        {
            sum += Fnv(kv.Key) * kv.Value;
        }
        return sum + (ulong)m.Count;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: wordfreq <file> <rounds>");
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
