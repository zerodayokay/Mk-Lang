// dot, C#: f32 dot product with 16 partial sums using System.Numerics.Vector<float> (README.md).
// Safe pinned 64-byte aligned buffers, no unsafe, no MethodImpl, no FMA contraction:
// Vector<float>.Count is fixed by the CPU target (4 at x86-64-v2, 8 at x86-64-v3).
// Usage: dot <n> <reps> <seed>.
using System;
using System.Globalization;
using System.Numerics;
using System.Runtime.InteropServices;

internal static class Program
{
    private static float Dot(ReadOnlySpan<float> x, ReadOnlySpan<float> y)
    {
        Span<float> s = stackalloc float[16];
        int n = x.Length;
        int i = 0;
        if (Vector<float>.Count == 8)
        {
            ReadOnlySpan<Vector<float>> xv = MemoryMarshal.Cast<float, Vector<float>>(x);
            ReadOnlySpan<Vector<float>> yv = MemoryMarshal.Cast<float, Vector<float>>(y);
            int vlen = xv.Length;
            Vector<float> a0 = Vector<float>.Zero;
            Vector<float> a1 = Vector<float>.Zero;
            int vi = 0;
            for (; vi <= vlen - 2; vi += 2)
            {
                a0 += xv[vi] * yv[vi];
                a1 += xv[vi + 1] * yv[vi + 1];
            }
            a0.CopyTo(s);
            a1.CopyTo(s.Slice(8));
            i = vi * 8;
        }
        else if (Vector<float>.Count == 4)
        {
            ReadOnlySpan<Vector<float>> xv = MemoryMarshal.Cast<float, Vector<float>>(x);
            ReadOnlySpan<Vector<float>> yv = MemoryMarshal.Cast<float, Vector<float>>(y);
            int vlen = xv.Length;
            Vector<float> a0 = Vector<float>.Zero;
            Vector<float> a1 = Vector<float>.Zero;
            Vector<float> a2 = Vector<float>.Zero;
            Vector<float> a3 = Vector<float>.Zero;
            int vi = 0;
            for (; vi <= vlen - 4; vi += 4)
            {
                a0 += xv[vi] * yv[vi];
                a1 += xv[vi + 1] * yv[vi + 1];
                a2 += xv[vi + 2] * yv[vi + 2];
                a3 += xv[vi + 3] * yv[vi + 3];
            }
            a0.CopyTo(s);
            a1.CopyTo(s.Slice(4));
            a2.CopyTo(s.Slice(8));
            a3.CopyTo(s.Slice(12));
            i = vi * 4;
        }
        else
        {
            for (; i + 16 <= n; i += 16)
            {
                for (int j = 0; j < 16; j++)
                {
                    s[j] += x[i + j] * y[i + j];
                }
            }
        }
        for (int w = 8; w >= 1; w >>= 1)
        {
            for (int j = 0; j < w; j++)
            {
                s[j] += s[j + w];
            }
        }
        float r = s[0];
        for (; i < n; i++)
        {
            r += x[i] * y[i];
        }
        return r;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: dot <n> <reps> <seed>");
            return 2;
        }
        int n = int.Parse(args[0], CultureInfo.InvariantCulture);
        long reps = long.Parse(args[1], CultureInfo.InvariantCulture);
        ulong seed = ulong.Parse(args[2], CultureInfo.InvariantCulture);

        float[] xRaw = GC.AllocateArray<float>(n + 16, pinned: true);
        float[] yRaw = GC.AllocateArray<float>(n + 16, pinned: true);
        GCHandle xHandle = GCHandle.Alloc(xRaw, GCHandleType.Pinned);
        GCHandle yHandle = GCHandle.Alloc(yRaw, GCHandleType.Pinned);
        try
        {
            nuint xAddr = (nuint)xHandle.AddrOfPinnedObject();
            nuint yAddr = (nuint)yHandle.AddrOfPinnedObject();
            int xOff = (int)(((64 - (xAddr & 63)) & 63) / sizeof(float));
            int yOff = (int)(((64 - (yAddr & 63)) & 63) / sizeof(float));
            if (((xAddr + (nuint)(xOff * sizeof(float))) & 63) != 0 ||
                ((yAddr + (nuint)(yOff * sizeof(float))) & 63) != 0)
            {
                Console.Error.WriteLine("dot: buffer not 64-byte aligned");
                return 3;
            }
            Span<float> x = xRaw.AsSpan(xOff, n);
            Span<float> y = yRaw.AsSpan(yOff, n);
            const float scale = 1.0f / 65536.0f;
            for (int i = 0; i < n; i++)
            {
                x[i] = (float)(((ulong)i * 0x9e3779b97f4a7c15UL + seed) >> 48) * scale - 0.5f;
                y[i] = (float)(((ulong)i * 0xbf58476d1ce4e5b9UL + seed) >> 48) * scale - 0.5f;
            }
            ulong total = 0;
            for (long r = 0; r < reps; r++)
            {
                x[(int)(r % n)] += 1.0f;
                float d = Dot(x, y);
                total += BitConverter.SingleToUInt32Bits(d);
            }
            Console.WriteLine(total);
            return 0;
        }
        finally
        {
            yHandle.Free();
            xHandle.Free();
        }
    }
}
