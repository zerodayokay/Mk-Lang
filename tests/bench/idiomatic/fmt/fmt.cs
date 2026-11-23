// fmt, C#: format and parse n int64s and n doubles using IUtf8SpanFormattable.TryFormat (shortest round-trip) and Parse (README.md).
// Usage: fmt <n> <rounds> <seed>.
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
            Console.Error.WriteLine("usage: fmt <n> <rounds> <seed>");
            return 2;
        }
        int n = int.Parse(args[0], CultureInfo.InvariantCulture);
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);
        ulong st = ulong.Parse(args[2], CultureInfo.InvariantCulture);

        Span<byte> buf = stackalloc byte[64];
        CultureInfo inv = CultureInfo.InvariantCulture;
        long sum = 0;
        long ok = 0;
        for (int r = 0; r < rounds; r++)
        {
            for (int i = 0; i < n; i++)
            {
                st += 0x9e3779b97f4a7c15UL;
                ulong z = Mix(st);
                long x = (long)(z >> 1);
                if ((z & 1UL) != 0)
                {
                    x = -x;
                }
                st += 0x9e3779b97f4a7c15UL;
                double f = (double)(Mix(st) >> 11) * (1.0 / 9007199254740992.0) * 1e6;

                if (!x.TryFormat(buf, out int ilen, default, inv))
                {
                    return 1;
                }
                long y = long.Parse(buf.Slice(0, ilen), inv);

                if (!f.TryFormat(buf, out int flen, "G", inv))
                {
                    return 1;
                }
                double g = double.Parse(buf.Slice(0, flen), inv);

                sum += y + (long)g;
                if (g == f)
                {
                    ok++;
                }
            }
        }
        Console.WriteLine(sum + ok);
        return 0;
    }
}
