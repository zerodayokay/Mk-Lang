// hashmap-heap, C#: hash map churn with Dictionary<ulong, ulong> on the
// default GC heap growing from empty (README.md).
// Usage: hashmap-heap <n> <rounds> <seed>.
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

    private static ulong Round(int n, ulong seed)
    {
        var m = new Dictionary<ulong, ulong>();
        ulong st = seed;
        for (int i = 0; i < n; i++)
        {
            st += 0x9e3779b97f4a7c15UL;
            m[Mix(st)] = (ulong)i;
        }
        ulong sum = 0;
        st = seed;
        for (int i = 0; i < n; i++)
        {
            st += 0x9e3779b97f4a7c15UL;
            ulong k = Mix(st);
            if (m.TryGetValue(k, out ulong v))
            {
                sum += v;
            }
            if (m.ContainsKey(Mix(st ^ 0x5555555555555555UL)))
            {
                sum++;
            }
            if ((i & 1) == 0)
            {
                m.Remove(k);
            }
        }
        st = seed;
        for (int i = 0; i < n; i++)
        {
            st += 0x9e3779b97f4a7c15UL;
            if (m.ContainsKey(Mix(st)))
            {
                sum += 3UL;
            }
        }
        return sum + (ulong)m.Count;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: hashmap-heap <n> <rounds> <seed>");
            return 2;
        }
        int n = int.Parse(args[0], CultureInfo.InvariantCulture);
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);
        ulong seed = ulong.Parse(args[2], CultureInfo.InvariantCulture);

        ulong total = 0;
        for (int r = 0; r < rounds; r++)
        {
            total += Round(n, seed + (ulong)r);
        }
        Console.WriteLine(total);
        return 0;
    }
}
