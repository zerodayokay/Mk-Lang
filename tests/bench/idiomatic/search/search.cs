// search, C#: binary search over a sorted ulong array with MemoryExtensions.BinarySearch (README.md).
// Usage: search <n> <lookups> <seed>.
using System;
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
            Console.Error.WriteLine("usage: search <n> <lookups> <seed>");
            return 2;
        }
        int n = int.Parse(args[0], CultureInfo.InvariantCulture);
        int lookups = int.Parse(args[1], CultureInfo.InvariantCulture);
        ulong st = ulong.Parse(args[2], CultureInfo.InvariantCulture);

        ulong[] xs = new ulong[n];
        for (int i = 0; i < n; i++)
        {
            xs[i] = 2UL * (ulong)i + 1UL;
        }
        ReadOnlySpan<ulong> span = xs;
        ulong spanLen = 2UL * (ulong)n + 1UL;
        ulong total = 0;
        for (int j = 0; j < lookups; j++)
        {
            st += 0x9e3779b97f4a7c15UL;
            ulong k = Mix(st) % spanLen;
            int idx = span.BinarySearch(k);
            if (idx >= 0)
            {
                total += (ulong)idx + 1UL;
            }
            else
            {
                total++;
            }
        }
        Console.WriteLine(total);
        return 0;
    }
}
