// indexbyte, C#: find the first 0xFF byte with System.Numerics.Vector<byte> (README.md).
// Safe pinned 64-byte aligned buffer, no unsafe, no MethodImpl, no run-time dispatch:
// Vector<byte>.Count is fixed by the CPU target (16 at x86-64-v2, 32 at x86-64-v3).
// Usage: indexbyte <size> <reps> <seed>.
using System;
using System.Globalization;
using System.Numerics;
using System.Runtime.InteropServices;

internal static class Program
{
    private static ulong Mix(ulong z)
    {
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static long IndexByte(ReadOnlySpan<byte> s, byte b)
    {
        int w = Vector<byte>.Count;
        Vector<byte> nb = Vector.Create(b);
        ReadOnlySpan<Vector<byte>> vecs = MemoryMarshal.Cast<byte, Vector<byte>>(s);
        int vlen = vecs.Length;
        int vi = 0;
        for (; vi <= vlen - 4; vi += 4)
        {
            Vector<byte> e0 = Vector.Equals(vecs[vi], nb);
            Vector<byte> e1 = Vector.Equals(vecs[vi + 1], nb);
            Vector<byte> e2 = Vector.Equals(vecs[vi + 2], nb);
            Vector<byte> e3 = Vector.Equals(vecs[vi + 3], nb);
            Vector<byte> any = (e0 | e1) | (e2 | e3);
            if (Vector.AnyWhereAllBitsSet(any))
            {
                break;
            }
        }
        for (; vi < vlen; vi++)
        {
            int hit = Vector.IndexOf(vecs[vi], b);
            if (hit >= 0)
            {
                return (long)vi * w + hit;
            }
        }
        for (int i = vlen * w; i < s.Length; i++)
        {
            if (s[i] == b)
            {
                return i;
            }
        }
        return -1;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: indexbyte <size> <reps> <seed>");
            return 2;
        }
        int n = int.Parse(args[0], CultureInfo.InvariantCulture);
        long reps = long.Parse(args[1], CultureInfo.InvariantCulture);
        ulong st = ulong.Parse(args[2], CultureInfo.InvariantCulture);

        byte[] raw = GC.AllocateArray<byte>(n + 64, pinned: true);
        GCHandle handle = GCHandle.Alloc(raw, GCHandleType.Pinned);
        try
        {
            nuint rawAddr = (nuint)handle.AddrOfPinnedObject();
            int offset = (int)((64 - (rawAddr & 63)) & 63);
            if (((rawAddr + (nuint)offset) & 63) != 0)
            {
                Console.Error.WriteLine("indexbyte: buffer not 64-byte aligned");
                return 3;
            }
            Span<byte> buf = raw.AsSpan(offset, n);
            for (int i = 0; i < n; i++)
            {
                buf[i] = (byte)(((ulong)i * 131UL + st) & 0x7FUL);
            }
            long total = 0;
            ulong span = (ulong)(n < 64 ? n : 64);
            for (long r = 0; r < reps; r++)
            {
                st += 0x9e3779b97f4a7c15UL;
                int pos = n - 1 - (int)(Mix(st) % span);
                byte old = buf[pos];
                buf[pos] = 0xFF;
                total += IndexByte(buf, 0xFF);
                buf[pos] = old;
            }
            Console.WriteLine(total);
            return 0;
        }
        finally
        {
            handle.Free();
        }
    }
}
