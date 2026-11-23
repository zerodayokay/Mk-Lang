// listpush, C#: grow List<ulong> from empty by n single Add calls and one AddRange of n/4 (README.md).
// Usage: listpush <n> <rounds> <seed>.
using System;
using System.Collections.Generic;
using System.Globalization;
using System.Runtime.InteropServices;

internal static class Program
{
    private static ulong Mix(ulong z)
    {
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static ulong Round(int n, ulong[] ext, ref ulong st)
    {
        var l = new List<ulong>();
        for (int i = 0; i < n; i++)
        {
            st += 0x9e3779b97f4a7c15UL;
            l.Add(Mix(st));
        }
        l.AddRange(ext);
        ulong sum = 0;
        ReadOnlySpan<ulong> span = CollectionsMarshal.AsSpan(l);
        for (int i = 0; i < span.Length; i++)
        {
            sum += span[i];
        }
        return sum + (ulong)span.Length;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: listpush <n> <rounds> <seed>");
            return 2;
        }
        int n = int.Parse(args[0], CultureInfo.InvariantCulture);
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);
        ulong st = ulong.Parse(args[2], CultureInfo.InvariantCulture);

        ulong[] ext = new ulong[n / 4];
        for (int i = 0; i < ext.Length; i++)
        {
            ext[i] = 3UL * (ulong)i + 1UL;
        }

        ulong total = 0;
        for (int r = 0; r < rounds; r++)
        {
            total += Round(n, ext, ref st);
        }
        Console.WriteLine(total);
        return 0;
    }
}
