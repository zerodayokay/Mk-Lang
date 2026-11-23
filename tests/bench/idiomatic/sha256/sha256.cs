// sha256, C#, idiomatic std: the task of README.md with
// System.Security.Cryptography.SHA256.HashData (zero-allocation Span overload)
// over a generated multi-MiB SplitMix64 buffer: bulk_reps full-buffer passes
// plus short_reps passes over 64-byte slices, XOR-folding each digest's first
// 8 little-endian bytes and mutating one buffer byte after each pass.
// No unsafe, no MethodImpl, no Unsafe.*.
using System;
using System.Buffers.Binary;
using System.Security.Cryptography;

internal static class Program
{
    private static ulong Mix(ulong x)
    {
        ulong z = x;
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static int Main(string[] args)
    {
        if (args.Length != 4)
        {
            Console.Error.WriteLine("usage: sha256 <mib> <bulk_reps> <short_reps> <seed>");
            return 2;
        }
        int mib = int.Parse(args[0]);
        int bulkReps = int.Parse(args[1]);
        int shortReps = int.Parse(args[2]);
        ulong st = ulong.Parse(args[3]);
        int n = mib * 1048576;
        byte[] buf = new byte[n];
        for (int i = 0; i < n; i += 8)
        {
            st += 0x9e3779b97f4a7c15UL;
            BinaryPrimitives.WriteUInt64LittleEndian(buf.AsSpan(i, 8), Mix(st));
        }
        byte[] digest = new byte[32];
        ulong sum = 0;
        for (int r = 0; r < bulkReps; r++)
        {
            SHA256.HashData(buf, digest);
            ulong h = BinaryPrimitives.ReadUInt64LittleEndian(digest);
            sum ^= h;
            buf[r % n] ^= (byte)h;
        }
        for (int r = 0; r < shortReps; r++)
        {
            ReadOnlySpan<byte> span = buf;
            for (int j = 0; j < n; j += 64)
            {
                SHA256.HashData(span.Slice(j, 64), digest);
                sum ^= BinaryPrimitives.ReadUInt64LittleEndian(digest);
            }
            int idx = (int)(((long)r * 997L) % n);
            buf[idx] ^= (byte)sum;
        }
        Console.WriteLine(sum);
        return 0;
    }
}
