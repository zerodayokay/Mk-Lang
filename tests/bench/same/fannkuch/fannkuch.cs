// fannkuch, C#: the reference algorithm of README.md (the Benchmarks Game's
// fannkuch-redux, sequential). Three long[] arrays of length n, the same loops
// and next-permutation step as every column. Bounds-checked, wrapping overflow
// (class bounds). No unsafe, no stackalloc, no MethodImpl, no Unsafe.*.
using System;

internal static class Program
{
    private static int Main(string[] args)
    {
        if (args.Length != 1)
        {
            Console.Error.WriteLine("usage: fannkuch <n>");
            return 2;
        }
        int n = int.Parse(args[0]);
        long[] perm = new long[n];
        long[] perm1 = new long[n];
        long[] count = new long[n];
        for (int i = 0; i < n; i++)
        {
            perm1[i] = i;
        }
        int r = n;
        long permCount = 0;
        long checksum = 0;
        long maxFlips = 0;
        bool done = false;
        while (!done)
        {
            while (r != 1)
            {
                count[r - 1] = r;
                r -= 1;
            }
            for (int i = 0; i < n; i++)
            {
                perm[i] = perm1[i];
            }
            long flips = 0;
            int k = (int)perm[0];
            while (k != 0)
            {
                int i = 0;
                int j = k;
                while (i < j)
                {
                    long t = perm[i];
                    perm[i] = perm[j];
                    perm[j] = t;
                    i += 1;
                    j -= 1;
                }
                flips += 1;
                k = (int)perm[0];
            }
            if (flips > maxFlips)
            {
                maxFlips = flips;
            }
            if ((permCount & 1) == 0)
            {
                checksum += flips;
            }
            else
            {
                checksum -= flips;
            }
            for (;;)
            {
                if (r == n)
                {
                    done = true;
                    break;
                }
                long perm0 = perm1[0];
                for (int i = 0; i < r; i++)
                {
                    perm1[i] = perm1[i + 1];
                }
                perm1[r] = perm0;
                count[r] -= 1;
                if (count[r] > 0)
                {
                    break;
                }
                r += 1;
            }
            permCount += 1;
        }
        Console.WriteLine(checksum * 1000 + maxFlips);
        return 0;
    }
}
