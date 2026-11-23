// sieve, C#: the reference algorithm of README.md. A byte[] of n + 1 flags,
// bounds-checked, wrapping overflow (class bounds). No unsafe, no stackalloc,
// no MethodImpl, no Unsafe.*.
using System;

internal static class Program
{
    // tools/bench flips this line in a copy for the counting build.
    private const bool BenchCount = false;

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: sieve <n> <rounds>");
            return 2;
        }
        long n = long.Parse(args[0]);
        long rounds = long.Parse(args[1]);
        byte[] flags = new byte[n + 1];
        long total = 0;
        ulong marks = 0;
        for (long r = 0; r < rounds; r++)
        {
            for (int i = 0; i < flags.Length; i++)
            {
                flags[i] = 1;
            }
            long count = 0;
            for (long i = 2; i <= n; i++)
            {
                if (flags[(int)i] != 0)
                {
                    count += 1;
                    long j = i * i;
                    while (j <= n)
                    {
                        flags[(int)j] = 0;
                        if (BenchCount)
                        {
                            marks += 1;
                        }
                        j += i;
                    }
                }
            }
            total += count;
        }
        Console.WriteLine(total);
        if (BenchCount)
        {
            Console.WriteLine($"counts marks={marks}");
        }
        return 0;
    }
}
