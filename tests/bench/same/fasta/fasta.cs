// fasta, C#: the reference algorithm of README.md (the Benchmarks Game's fasta
// generator, hashed by line into a long checksum). Bounds-checked, wrapping
// overflow (class bounds). No unsafe, no stackalloc, no MethodImpl, no
// Unsafe.*.
using System;

internal struct Freq
{
    public byte Ch;
    public double P;
}

internal static class Program
{
    // tools/bench flips this line in a copy for the counting build.
    private const bool BenchCount = false;

    private const long Im = 139968;
    private const long Ia = 3877;
    private const long Ic = 29573;
    private const int LineLen = 60;

    private static void MakeCumulative(Freq[] tab)
    {
        double cp = 0.0;
        for (int i = 0; i < tab.Length; i++)
        {
            cp = cp + tab[i].P;
            tab[i].P = cp;
        }
        tab[tab.Length - 1].P = 1.0;
    }

    private static double NextRand(ref long seed)
    {
        seed = (seed * Ia + Ic) % Im;
        return (double)seed / (double)Im;
    }

    private static long LineSum(byte[] buf, int m)
    {
        long s = 0;
        for (int k = 0; k < m; k++)
        {
            s += (long)buf[k] * (k + 1);
        }
        return s;
    }

    private static long RepeatFasta(byte[] alu, long count)
    {
        byte[] buf = new byte[61];
        int aluLen = alu.Length;
        int pos = 0;
        long rem = count;
        long total = 0;
        while (rem > 0)
        {
            int m = rem < LineLen ? (int)rem : LineLen;
            for (int k = 0; k < m; k++)
            {
                buf[k] = alu[pos];
                pos += 1;
                if (pos == aluLen)
                {
                    pos = 0;
                }
            }
            buf[m] = 10;
            total += LineSum(buf, m + 1);
            rem -= m;
        }
        return total;
    }

    private static long RandomFasta(Freq[] tab, long count, ref long seed, ref ulong probes)
    {
        byte[] buf = new byte[61];
        long rem = count;
        long total = 0;
        while (rem > 0)
        {
            int m = rem < LineLen ? (int)rem : LineLen;
            for (int k = 0; k < m; k++)
            {
                double r = NextRand(ref seed);
                int j = 0;
                for (;;)
                {
                    if (BenchCount)
                    {
                        probes += 1;
                    }
                    if (tab[j].P >= r)
                    {
                        break;
                    }
                    j += 1;
                }
                buf[k] = tab[j].Ch;
            }
            buf[m] = 10;
            total += LineSum(buf, m + 1);
            rem -= m;
        }
        return total;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 1)
        {
            Console.Error.WriteLine("usage: fasta <n>");
            return 2;
        }
        long n = long.Parse(args[0]);
        byte[] alu = System.Text.Encoding.ASCII.GetBytes(
            "GGCCGGGCGCGGTGGCTCACGCCTGTAATCCCAGCACTTTGGGAGGCCGAGGCGGGCGGATCACCTGAGGTCAGGAGTTCGAGACCAGCCTGGCCAACATGGTGAAACCCCGTCTCTACTAAAAATACAAAAATTAGCCGGGCGTGGTGGCGCGCGCCTGTAATCCCAGCTACTCGGGAGGCTGAGGCAGGAGAATCGCTTGAACCCGGGAGGCGGAGGTTGCAGTGAGCCGAGATCGCGCCACTGCACTCCAGCCTGGGCGACAGAGCGAGACTCCGTCTCAAAAA");
        Freq[] iub = new Freq[]
        {
            new Freq { Ch = (byte)'a', P = 0.27 }, new Freq { Ch = (byte)'c', P = 0.12 },
            new Freq { Ch = (byte)'g', P = 0.12 }, new Freq { Ch = (byte)'t', P = 0.27 },
            new Freq { Ch = (byte)'B', P = 0.02 }, new Freq { Ch = (byte)'D', P = 0.02 },
            new Freq { Ch = (byte)'H', P = 0.02 }, new Freq { Ch = (byte)'K', P = 0.02 },
            new Freq { Ch = (byte)'M', P = 0.02 }, new Freq { Ch = (byte)'N', P = 0.02 },
            new Freq { Ch = (byte)'R', P = 0.02 }, new Freq { Ch = (byte)'S', P = 0.02 },
            new Freq { Ch = (byte)'V', P = 0.02 }, new Freq { Ch = (byte)'W', P = 0.02 },
            new Freq { Ch = (byte)'Y', P = 0.02 },
        };
        Freq[] homo = new Freq[]
        {
            new Freq { Ch = (byte)'a', P = 0.3029549426680 },
            new Freq { Ch = (byte)'c', P = 0.1979883004921 },
            new Freq { Ch = (byte)'g', P = 0.1975473066391 },
            new Freq { Ch = (byte)'t', P = 0.3015094502008 },
        };
        MakeCumulative(iub);
        MakeCumulative(homo);
        long seed = 42;
        ulong probes = 0;
        long total = 0;
        total += RepeatFasta(alu, 2 * n);
        total += RandomFasta(iub, 3 * n, ref seed, ref probes);
        total += RandomFasta(homo, 5 * n, ref seed, ref probes);
        Console.WriteLine(total);
        if (BenchCount)
        {
            Console.WriteLine($"counts probes={probes}");
        }
        return 0;
    }
}
