// sortstable, C#: sort four patterns of ulong keys ascending with Enumerable.Order (LINQ stable sort) (README.md).
// Usage: sortstable <n> <rounds> <seed>.
using System;
using System.Globalization;
using System.Linq;

internal static class Program
{
    private static ulong Mix(ulong z)
    {
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static ulong CheckOrdered(ulong[] xs)
    {
        ulong sum = 0;
        ulong prev = 0;
        int i = 0;
        foreach (ulong x in xs.Order())
        {
            if (i > 0 && prev > x)
            {
                return 0xdeadUL;
            }
            sum += x * (ulong)(i % 1000 + 1);
            prev = x;
            i++;
        }
        return sum;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: sortstable <n> <rounds> <seed>");
            return 2;
        }
        int n = int.Parse(args[0], CultureInfo.InvariantCulture);
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);
        ulong st = ulong.Parse(args[2], CultureInfo.InvariantCulture);

        ulong[] xs = new ulong[n];
        ulong total = 0;
        for (int r = 0; r < rounds; r++)
        {
            for (int p = 0; p < 4; p++)
            {
                switch (p)
                {
                    case 0:
                        for (int i = 0; i < n; i++)
                        {
                            st += 0x9e3779b97f4a7c15UL;
                            xs[i] = Mix(st);
                        }
                        break;
                    case 1:
                        for (int i = 0; i < n; i++)
                        {
                            xs[i] = (ulong)(n - i);
                        }
                        break;
                    case 2:
                        for (int i = 0; i < n; i++)
                        {
                            xs[i] = (ulong)(i % 1000);
                        }
                        break;
                    default:
                        for (int i = 0; i < n; i++)
                        {
                            st += 0x9e3779b97f4a7c15UL;
                            xs[i] = Mix(st) >> 60;
                        }
                        break;
                }
                total += CheckOrdered(xs);
            }
        }
        Console.WriteLine(total);
        return 0;
    }
}
