// sortstr, C#: sort ReadOnlyMemory<byte> views into one byte[] buffer bytewise ascending with MemoryExtensions.Sort (README.md).
// Usage: sortstr <n> <rounds> <seed>.
using System;
using System.Collections.Generic;
using System.Globalization;

internal static class Program
{
    private readonly struct ByteMemoryComparer : IComparer<ReadOnlyMemory<byte>>
    {
        public int Compare(ReadOnlyMemory<byte> x, ReadOnlyMemory<byte> y)
        {
            return x.Span.SequenceCompareTo(y.Span);
        }
    }

    private static ulong Mix(ulong z)
    {
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static ulong Fnv(ReadOnlySpan<byte> w)
    {
        ulong h = 0xcbf29ce484222325UL;
        for (int i = 0; i < w.Length; i++)
        {
            h = (h ^ w[i]) * 0x100000001b3UL;
        }
        return h;
    }

    private static ulong Check(ReadOnlySpan<ReadOnlyMemory<byte>> xs)
    {
        ulong sum = 0;
        for (int i = 0; i < xs.Length; i++)
        {
            if (i > 0 && xs[i - 1].Span.SequenceCompareTo(xs[i].Span) > 0)
            {
                return 0xdeadUL;
            }
            sum += Fnv(xs[i].Span) * (ulong)(i % 1000 + 1);
        }
        return sum;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: sortstr <n> <rounds> <seed>");
            return 2;
        }
        int n = int.Parse(args[0], CultureInfo.InvariantCulture);
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);
        ulong st = ulong.Parse(args[2], CultureInfo.InvariantCulture);

        byte[] buf = new byte[n * 16];
        int[] offs = new int[n + 1];
        int pos = 0;
        for (int i = 0; i < n; i++)
        {
            offs[i] = pos;
            st += 0x9e3779b97f4a7c15UL;
            int len = 1 + (int)(Mix(st) % 16UL);
            for (int j = 0; j < len; j++)
            {
                st += 0x9e3779b97f4a7c15UL;
                buf[pos++] = (byte)(0x61UL + Mix(st) % 26UL);
            }
        }
        offs[n] = pos;

        ReadOnlyMemory<byte>[] orig = new ReadOnlyMemory<byte>[n];
        for (int i = 0; i < n; i++)
        {
            orig[i] = buf.AsMemory(offs[i], offs[i + 1] - offs[i]);
        }
        ReadOnlyMemory<byte>[] work = new ReadOnlyMemory<byte>[n];
        var cmp = new ByteMemoryComparer();
        ulong total = 0;
        for (int r = 0; r < rounds; r++)
        {
            orig.AsSpan().CopyTo(work);
            work.AsSpan().Sort(cmp);
            total += Check(work);
        }
        Console.WriteLine(total);
        return 0;
    }
}
