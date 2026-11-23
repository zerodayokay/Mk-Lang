// mapiter, C#: fill Dictionary<ulong, ulong> once and iterate it passes times (README.md).
// Uses the default EqualityComparer<ulong> (ulong.GetHashCode(), fixed/unkeyed).
// Usage: mapiter <n> <passes> <seed>.
using System;
using System.Collections.Generic;
using System.Globalization;

internal static class Program
{
    private static ulong Mix(ulong z)
    {
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: mapiter <n> <passes> <seed>");
            return 2;
        }
        int n = int.Parse(args[0], CultureInfo.InvariantCulture);
        int passes = int.Parse(args[1], CultureInfo.InvariantCulture);
        ulong st = ulong.Parse(args[2], CultureInfo.InvariantCulture);

        var m = new Dictionary<ulong, ulong>();
        for (int i = 0; i < n; i++)
        {
            st += 0x9e3779b97f4a7c15UL;
            m[Mix(st)] = (ulong)i;
        }

        ulong total = 0;
        ulong count = (ulong)m.Count;
        for (int p = 0; p < passes; p++)
        {
            foreach (KeyValuePair<ulong, ulong> kv in m)
            {
                total += kv.Key * (kv.Value + 1UL);
            }
            total += count;
        }
        Console.WriteLine(total);
        return 0;
    }
}
