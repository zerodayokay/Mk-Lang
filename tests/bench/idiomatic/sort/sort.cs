// sort, C#: sort four patterns of ulong keys ascending with MemoryExtensions.Sort (IntroSort) (README.md).
// Usage: sort <n> <rounds> <seed>.
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

    private static ulong Check(ReadOnlySpan<ulong> xs)
    {
        ulong sum = 0;
        for (int i = 0; i < xs.Length; i++)
        {
            if (i > 0 && xs[i - 1] > xs[i])
            {
                return 0xdeadUL;
            }
            sum += xs[i] * (ulong)(i % 1000 + 1);
        }
        return sum;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: sort <n> <rounds> <seed>");
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
                xs.AsSpan().Sort();
                total += Check(xs);
            }
        }
        Console.WriteLine(total);
        return 0;
    }
}
