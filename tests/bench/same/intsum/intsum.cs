// intsum, C#: the reference algorithm of README.md. n long values from
// splitmix64 masked to 0..1023, then rounds passes of an index loop summing
// them, each pass writing s & 1023 back into element r % n. Bounds-checked,
// wrapping overflow (class bounds). No unsafe, no stackalloc, no MethodImpl,
// no Unsafe.*.
using System;

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
            Console.Error.WriteLine("usage: intsum <n> <rounds> <seed>");
            return 2;
        }
        int n = int.Parse(args[0]);
        long rounds = long.Parse(args[1]);
        ulong st = ulong.Parse(args[2]);
        long[] xs = new long[n];
        for (int i = 0; i < n; i++)
        {
            st += 0x9e3779b97f4a7c15UL;
            xs[i] = (long)(Mix(st) & 1023UL);
        }
        long total = 0;
        for (long r = 0; r < rounds; r++)
        {
            long s = 0;
            for (int i = 0; i < xs.Length; i++)
            {
                s += xs[i];
            }
            total += s;
            xs[(int)(r % n)] = s & 1023L;
        }
        Console.WriteLine(total);
        return 0;
    }
}
