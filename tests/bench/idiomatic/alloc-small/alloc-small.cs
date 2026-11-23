// alloc-small, C#: ring of 4,096 live heap byte arrays allocated via Span.ToArray on the Workstation GC heap (README.md).
// Usage: alloc-small <steps> <seed>.
using System;
using System.Globalization;

internal static class Program
{
    private const int Live = 4096;

    private static ulong Mix(ulong z)
    {
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static byte[] MakeOne(ReadOnlySpan<byte> src, ref ulong st)
    {
        st += 0x9e3779b97f4a7c15UL;
        ulong v = Mix(st);
        int len = (int)(v % 249UL) + 8;
        int off = (int)((v >> 16) % 3840UL);
        return src.Slice(off, len).ToArray();
    }

    private static ulong ReadObj(byte[] o)
    {
        int n = o.Length;
        return (ulong)n + o[0] + o[n - 1];
    }

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: alloc-small <steps> <seed>");
            return 2;
        }
        int steps = int.Parse(args[0], CultureInfo.InvariantCulture);
        ulong st = ulong.Parse(args[1], CultureInfo.InvariantCulture);

        byte[] buf = new byte[4096];
        ulong gs = st ^ 0x5555555555555555UL;
        for (int i = 0; i < buf.Length; i++)
        {
            gs += 0x9e3779b97f4a7c15UL;
            buf[i] = (byte)(Mix(gs) % 26UL + 'a');
        }

        byte[][] ring = new byte[Live][];
        for (int i = 0; i < Live; i++)
        {
            ring[i] = MakeOne(buf, ref st);
        }

        ulong total = 0;
        for (int j = 0; j < steps; j++)
        {
            int slot = j & (Live - 1);
            total += ReadObj(ring[slot]);
            ring[slot] = MakeOne(buf, ref st);
        }
        for (int i = 0; i < Live; i++)
        {
            total += ReadObj(ring[i]);
        }
        Console.WriteLine(total);
        return 0;
    }
}
