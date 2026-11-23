// deflate, C#, idiomatic std: the task of README.md with
// System.IO.Compression.DeflateStream (CompressionLevel.Optimal, the default
// level) round-tripping a generated mixed text buffer in memory using a
// reusable MemoryStream and output buffer. Each round verifies exact output
// length and FNV-1a 64 checksum against the source buffer and flips one
// source byte between rounds. No unsafe, no MethodImpl, no Unsafe.*.
using System;
using System.IO;
using System.IO.Compression;

internal static class Program
{
    private const string Hex = "0123456789abcdef";

    private static readonly string[] Vocab =
    {
        "ERROR", "WARN ", "INFO ", "DEBUG",
        "GET", "POST", "PUT", "DELETE",
        "/api/v1/users", "/api/v1/orders", "/api/v1/items", "/healthz",
        "timeout", "ok", "cached", "retry",
    };

    private const string Prose = "The quick brown fox jumps over the lazy dog; pack my box with five dozen liquor jugs! 0123456789 ";

    private static ulong Mix(ulong x)
    {
        ulong z = x;
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static ulong Fnv(byte[] b, int len)
    {
        ulong h = 0xcbf29ce484222325UL;
        for (int i = 0; i < len; i++)
        {
            h = (h ^ b[i]) * 0x100000001b3UL;
        }
        return h;
    }

    private static void Emit(byte[] dst, ref int pos, string s)
    {
        int rem = dst.Length - pos;
        if (rem <= 0)
        {
            return;
        }
        int k = s.Length < rem ? s.Length : rem;
        for (int i = 0; i < k; i++)
        {
            dst[pos + i] = (byte)s[i];
        }
        pos += k;
    }

    private static void EmitSlice(byte[] dst, ref int pos, string s, int off, int len)
    {
        int rem = dst.Length - pos;
        if (rem <= 0)
        {
            return;
        }
        int k = len < rem ? len : rem;
        for (int i = 0; i < k; i++)
        {
            dst[pos + i] = (byte)s[off + i];
        }
        pos += k;
    }

    private static void Generate(byte[] dst, ulong seed)
    {
        ulong st = seed;
        int pos = 0;
        int n = dst.Length;
        while (pos < n)
        {
            st += 0x9e3779b97f4a7c15UL;
            ulong z = Mix(st);
            Emit(dst, ref pos, "ts=");
            for (int k = 0; k < 8; k++)
            {
                if (pos < n)
                {
                    int shift = 60 - 4 * k;
                    dst[pos++] = (byte)Hex[(int)((z >> shift) & 15UL)];
                }
            }
            Emit(dst, ref pos, " level=");
            Emit(dst, ref pos, Vocab[(int)(z & 3UL)]);
            Emit(dst, ref pos, " method=");
            Emit(dst, ref pos, Vocab[4 + (int)((z >> 2) & 3UL)]);
            Emit(dst, ref pos, " path=");
            Emit(dst, ref pos, Vocab[8 + (int)((z >> 4) & 3UL)]);
            Emit(dst, ref pos, " msg=");
            Emit(dst, ref pos, Vocab[12 + (int)((z >> 6) & 3UL)]);
            Emit(dst, ref pos, " body=");
            int off = (int)((z >> 8) % 60UL);
            int plen = 16 + (int)((z >> 16) % 21UL);
            EmitSlice(dst, ref pos, Prose, off, plen);
            Emit(dst, ref pos, "\n");
        }
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: deflate <bytes> <rounds> <seed>");
            return 2;
        }
        int n = int.Parse(args[0]);
        int rounds = int.Parse(args[1]);
        ulong seed = ulong.Parse(args[2]);
        byte[] src = new byte[n];
        Generate(src, seed);
        using var compMs = new MemoryStream(n + (n >> 4) + 65536);
        byte[] decBuf = new byte[n + 64];
        ulong total = 0;
        for (int r = 0; r < rounds; r++)
        {
            ulong want = Fnv(src, n);
            compMs.SetLength(0);
            compMs.Position = 0;
            using (var ds = new DeflateStream(compMs, CompressionLevel.Optimal, leaveOpen: true))
            {
                ds.Write(src, 0, n);
            }
            int compLen = (int)compMs.Length;
            byte[] compArr = compMs.GetBuffer();
            int decLen = 0;
            using (var readMs = new MemoryStream(compArr, 0, compLen, writable: false))
            using (var inf = new DeflateStream(readMs, CompressionMode.Decompress, leaveOpen: true))
            {
                while (true)
                {
                    int k = inf.Read(decBuf, decLen, decBuf.Length - decLen);
                    if (k == 0)
                    {
                        break;
                    }
                    decLen += k;
                }
            }
            if (decLen != n)
            {
                return 1;
            }
            ulong got = Fnv(decBuf, decLen);
            if (got != want)
            {
                return 1;
            }
            total += got + (ulong)decLen;
            int idx = (int)(((long)r * 1009L) % n);
            src[idx] ^= 0x01;
        }
        Console.WriteLine(total);
        return 0;
    }
}
