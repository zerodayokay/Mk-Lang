// fmtints, C#: the reference algorithm of README.md. The textbook digit loop
// (one digit per division by 10 into a 20-byte buffer, copied out byte by
// byte), not int.TryFormat: the idiomatic fmt row compares std formatters.
// Bounds-checked, wrapping overflow (class bounds). No unsafe, no stackalloc,
// no MethodImpl, no Unsafe.*.
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
            Console.Error.WriteLine("usage: fmtints <n> <rounds> <seed>");
            return 2;
        }
        int n = int.Parse(args[0]);
        long rounds = long.Parse(args[1]);
        ulong st = ulong.Parse(args[2]);
        long[] xs = new long[n];
        byte[] buf = new byte[n * 12];
        for (int i = 0; i < n; i++)
        {
            st += 0x9e3779b97f4a7c15UL;
            xs[i] = (long)(Mix(st) % 2000000001UL) - 1000000000L;
        }
        byte[] tmp = new byte[20];
        long total = 0;
        for (long r = 0; r < rounds; r++)
        {
            int pos = 0;
            for (int i = 0; i < n; i++)
            {
                long x = xs[i];
                ulong u;
                if (x < 0)
                {
                    buf[pos] = 45;
                    pos += 1;
                    u = (ulong)(-x);
                }
                else
                {
                    u = (ulong)x;
                }
                int k = 20;
                for (;;)
                {
                    k -= 1;
                    tmp[k] = (byte)(48 + u % 10);
                    u /= 10;
                    if (u == 0)
                    {
                        break;
                    }
                }
                for (int j = k; j < 20; j++)
                {
                    buf[pos] = tmp[j];
                    pos += 1;
                }
                buf[pos] = 10;
                pos += 1;
            }
            long sum = 0;
            for (int i = 0; i < pos; i++)
            {
                sum += buf[i];
            }
            total += sum;
        }
        Console.WriteLine(total);
        return 0;
    }
}
