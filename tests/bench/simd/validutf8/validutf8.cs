// validutf8, C#: Keiser-Lemire UTF-8 validator with System.Numerics.Vector<byte> (README.md).
// Portable compare-and-select nibble lookups, overlapping unaligned prev loads, saturating subtraction,
// 64-byte chunks with all-ASCII skip. Safe pinned 64-byte aligned buffer, no unsafe, no MethodImpl.
// Usage: validutf8 <size> <reps> <seed>.
using System;
using System.Globalization;
using System.Numerics;
using System.Runtime.InteropServices;

internal static class Program
{
    private const byte TooShort = 1 << 0;
    private const byte TooLong = 1 << 1;
    private const byte Overlong3 = 1 << 2;
    private const byte TooLarge = 1 << 3;
    private const byte Surrogate = 1 << 4;
    private const byte Overlong2 = 1 << 5;
    private const byte TooLarge1000 = 1 << 6;
    private const byte Overlong4 = 1 << 6;
    private const byte TwoConts = 1 << 7;
    private const byte Carry = TooShort | TooLong | TwoConts;

    private static readonly byte[][] Enc =
    {
        new byte[] { 0x61 }, new byte[] { 0x20 }, new byte[] { 0x30 }, new byte[] { 0x7E },
        new byte[] { 0xC2, 0x80 }, new byte[] { 0xDF, 0xBF }, new byte[] { 0xC3, 0xA9 }, new byte[] { 0xD0, 0x96 },
        new byte[] { 0xE0, 0xA0, 0x80 }, new byte[] { 0xED, 0x9F, 0xBF }, new byte[] { 0xEE, 0x80, 0x80 }, new byte[] { 0xE4, 0xB8, 0x96 },
        new byte[] { 0xF0, 0x90, 0x80, 0x80 }, new byte[] { 0xF4, 0x8F, 0xBF, 0xBF }, new byte[] { 0xF0, 0x9F, 0x98, 0x80 }, new byte[] { 0xF3, 0xA0, 0x80, 0x80 },
    };

    private static ulong Mix(ulong z)
    {
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static Vector<byte> LookupT1H(Vector<byte> idx)
    {
        Vector<byte> r = Vector.Create(TooLong);
        Vector<sbyte> si = Vector.AsVectorSByte(idx);
        r = Vector.ConditionalSelect(Vector.AsVectorByte(Vector.GreaterThan(si, Vector.Create((sbyte)7))), Vector.Create(TwoConts), r);
        r = Vector.ConditionalSelect(Vector.Equals(idx, Vector.Create((byte)12)), Vector.Create((byte)(TooShort | Overlong2)), r);
        r = Vector.ConditionalSelect(Vector.Equals(idx, Vector.Create((byte)13)), Vector.Create(TooShort), r);
        r = Vector.ConditionalSelect(Vector.Equals(idx, Vector.Create((byte)14)), Vector.Create((byte)(TooShort | Overlong3 | Surrogate)), r);
        r = Vector.ConditionalSelect(Vector.Equals(idx, Vector.Create((byte)15)), Vector.Create((byte)(TooShort | TooLarge | TooLarge1000 | Overlong4)), r);
        return r;
    }

    private static Vector<byte> LookupT1L(Vector<byte> idx)
    {
        Vector<byte> r = Vector.Create((byte)(Carry | TooLarge | TooLarge1000));
        r |= Vector.Equals(idx, Vector.Create((byte)13)) & Vector.Create(Surrogate);
        r = Vector.ConditionalSelect(Vector.Equals(idx, Vector.Create((byte)4)), Vector.Create((byte)(Carry | TooLarge)), r);
        r = Vector.ConditionalSelect(Vector.Equals(idx & Vector.Create((byte)0xFE), Vector.Create((byte)2)), Vector.Create(Carry), r);
        r = Vector.ConditionalSelect(Vector.Equals(idx, Vector.Create((byte)1)), Vector.Create((byte)(Carry | Overlong2)), r);
        r = Vector.ConditionalSelect(Vector.Equals(idx, Vector.Create((byte)0)), Vector.Create((byte)(Carry | Overlong3 | Overlong2 | Overlong4)), r);
        return r;
    }

    private static Vector<byte> LookupT2H(Vector<byte> idx)
    {
        Vector<byte> r = Vector.Create(TooShort);
        r = Vector.ConditionalSelect(Vector.Equals(idx, Vector.Create((byte)8)), Vector.Create((byte)(TooLong | Overlong2 | TwoConts | Overlong3 | TooLarge1000 | Overlong4)), r);
        r = Vector.ConditionalSelect(Vector.Equals(idx, Vector.Create((byte)9)), Vector.Create((byte)(TooLong | Overlong2 | TwoConts | Overlong3 | TooLarge)), r);
        r = Vector.ConditionalSelect(Vector.Equals(idx & Vector.Create((byte)0xFE), Vector.Create((byte)10)), Vector.Create((byte)(TooLong | Overlong2 | TwoConts | Surrogate | TooLarge)), r);
        return r;
    }

    private static Vector<byte> CheckVec(Vector<byte> cur, Vector<byte> p1, Vector<byte> p2, Vector<byte> p3)
    {
        Vector<byte> sc = LookupT1H(Vector.ShiftRightLogical(p1, 4))
            & LookupT1L(p1 & Vector.Create((byte)0x0F))
            & LookupT2H(Vector.ShiftRightLogical(cur, 4));
        Vector<byte> must23 = Vector.SubtractSaturate(p2, Vector.Create((byte)(0xE0 - 0x80)))
            | Vector.SubtractSaturate(p3, Vector.Create((byte)(0xF0 - 0x80)));
        return (must23 & Vector.Create((byte)0x80)) ^ sc;
    }

    private static void ProcessChunk(ReadOnlySpan<byte> b, int off, Vector<byte> maxv, ref Vector<byte> err, ref Vector<byte> prevIncomplete)
    {
        if (Vector<byte>.Count == 32)
        {
            Vector<byte> v0 = new Vector<byte>(b.Slice(off));
            Vector<byte> v1 = new Vector<byte>(b.Slice(off + 32));
            if (!Vector.LessThanAny(Vector.AsVectorSByte(v0 | v1), Vector<sbyte>.Zero))
            {
                err |= prevIncomplete;
                prevIncomplete = Vector<byte>.Zero;
                return;
            }
            err |= CheckVec(v0, new Vector<byte>(b.Slice(off - 1)), new Vector<byte>(b.Slice(off - 2)), new Vector<byte>(b.Slice(off - 3)));
            err |= CheckVec(v1, new Vector<byte>(b.Slice(off + 31)), new Vector<byte>(b.Slice(off + 30)), new Vector<byte>(b.Slice(off + 29)));
            prevIncomplete = Vector.SubtractSaturate(v1, maxv);
        }
        else if (Vector<byte>.Count == 16)
        {
            Vector<byte> v0 = new Vector<byte>(b.Slice(off));
            Vector<byte> v1 = new Vector<byte>(b.Slice(off + 16));
            Vector<byte> v2 = new Vector<byte>(b.Slice(off + 32));
            Vector<byte> v3 = new Vector<byte>(b.Slice(off + 48));
            if (!Vector.LessThanAny(Vector.AsVectorSByte((v0 | v1) | (v2 | v3)), Vector<sbyte>.Zero))
            {
                err |= prevIncomplete;
                prevIncomplete = Vector<byte>.Zero;
                return;
            }
            err |= CheckVec(v0, new Vector<byte>(b.Slice(off - 1)), new Vector<byte>(b.Slice(off - 2)), new Vector<byte>(b.Slice(off - 3)));
            err |= CheckVec(v1, new Vector<byte>(b.Slice(off + 15)), new Vector<byte>(b.Slice(off + 14)), new Vector<byte>(b.Slice(off + 13)));
            err |= CheckVec(v2, new Vector<byte>(b.Slice(off + 31)), new Vector<byte>(b.Slice(off + 30)), new Vector<byte>(b.Slice(off + 29)));
            err |= CheckVec(v3, new Vector<byte>(b.Slice(off + 47)), new Vector<byte>(b.Slice(off + 46)), new Vector<byte>(b.Slice(off + 45)));
            prevIncomplete = Vector.SubtractSaturate(v3, maxv);
        }
        else
        {
            int w = Vector<byte>.Count;
            Vector<byte> all = Vector<byte>.Zero;
            for (int j = 0; j < 64; j += w)
            {
                all |= new Vector<byte>(b.Slice(off + j));
            }
            if (!Vector.LessThanAny(Vector.AsVectorSByte(all), Vector<sbyte>.Zero))
            {
                err |= prevIncomplete;
                prevIncomplete = Vector<byte>.Zero;
                return;
            }
            Vector<byte> last = Vector<byte>.Zero;
            for (int j = 0; j < 64; j += w)
            {
                int pos = off + j;
                last = new Vector<byte>(b.Slice(pos));
                err |= CheckVec(last, new Vector<byte>(b.Slice(pos - 1)), new Vector<byte>(b.Slice(pos - 2)), new Vector<byte>(b.Slice(pos - 3)));
            }
            prevIncomplete = Vector.SubtractSaturate(last, maxv);
        }
    }

    private static bool ValidUtf8(ReadOnlySpan<byte> b)
    {
        int n = b.Length;
        if (n == 0)
        {
            return true;
        }
        int w = Vector<byte>.Count;
        Span<byte> maxb = stackalloc byte[w];
        maxb.Fill(0xFF);
        maxb[w - 3] = 0xF0 - 1;
        maxb[w - 2] = 0xE0 - 1;
        maxb[w - 1] = 0xC0 - 1;
        Vector<byte> maxv = new Vector<byte>(maxb);

        Vector<byte> err = Vector<byte>.Zero;
        Vector<byte> prevIncomplete = Vector<byte>.Zero;
        Span<byte> scratch = stackalloc byte[68];

        int firstLen = n < 64 ? n : 64;
        b.Slice(0, firstLen).CopyTo(scratch.Slice(3));
        ProcessChunk(scratch, 3, maxv, ref err, ref prevIncomplete);

        int i = 64;
        for (; i + 64 <= n; i += 64)
        {
            ProcessChunk(b, i, maxv, ref err, ref prevIncomplete);
        }
        if (i < n)
        {
            scratch.Clear();
            b.Slice(i - 3, 3).CopyTo(scratch);
            b.Slice(i, n - i).CopyTo(scratch.Slice(3));
            ProcessChunk(scratch, 3, maxv, ref err, ref prevIncomplete);
        }
        err |= prevIncomplete;
        return err == Vector<byte>.Zero;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: validutf8 <size> <reps> <seed>");
            return 2;
        }
        int n = int.Parse(args[0], CultureInfo.InvariantCulture);
        long reps = long.Parse(args[1], CultureInfo.InvariantCulture);
        ulong st = ulong.Parse(args[2], CultureInfo.InvariantCulture);

        int blen = n < 65536 ? n : 65536;
        byte[] blk = new byte[blen];
        byte[] raw = GC.AllocateArray<byte>(n + 64, pinned: true);
        GCHandle handle = GCHandle.Alloc(raw, GCHandleType.Pinned);
        try
        {
            nuint rawAddr = (nuint)handle.AddrOfPinnedObject();
            int offset = (int)((64 - (rawAddr & 63)) & 63);
            if (((rawAddr + (nuint)offset) & 63) != 0)
            {
                Console.Error.WriteLine("validutf8: buffer not 64-byte aligned");
                return 3;
            }
            Span<byte> buf = raw.AsSpan(offset, n);
            int p = 0;
            while (true)
            {
                st += 0x9e3779b97f4a7c15UL;
                int k = (int)(Mix(st) & 15UL);
                int len = 1 + k / 4;
                if (p + len > blen)
                {
                    break;
                }
                Enc[k].CopyTo(blk, p);
                p += len;
            }
            blk.AsSpan(p).Fill(0x61);
            int i = 0;
            for (; i + blen <= n; i += blen)
            {
                blk.CopyTo(buf.Slice(i, blen));
            }
            if (i < n)
            {
                buf.Slice(i).Fill(0x61);
            }

            ulong span = (ulong)(n < 64 ? n : 64);
            ulong total = 0;
            for (long r = 0; r < reps; r++)
            {
                st += 0x9e3779b97f4a7c15UL;
                ulong z = Mix(st);
                int pos = n - 1 - (int)((z >> 1) % span);
                byte old = buf[pos];
                if ((z & 1UL) != 0)
                {
                    buf[pos] = (byte)(z >> 56);
                }
                total = total * 31UL + (ValidUtf8(buf) ? 1UL : 0UL);
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
