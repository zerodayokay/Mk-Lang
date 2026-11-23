// comphash, C#, std: the task of README.md. Pass 1 reads the input file in
// 64 KiB chunks, streaming SHA-256 with IncrementalHash(SHA256) and compressing
// with System.IO.Compression.ZLibStream (RFC 1950, CompressionLevel = 6) over
// a 64 KiB buffered output FileStream. Pass 2 opens the output file through a
// 64 KiB buffered FileStream wrapped in StrictEofStream (so truncated streams
// fail instead of returning silent EOF), decompresses with ZLibStream (checking
// header FCHECK/FDICT, Adler-32 trailer and stream termination) in 64 KiB
// chunks while streaming a second IncrementalHash(SHA256), verifies length and
// SHA-256 match Pass 1, and prints "<bytes> <hex-sha256>". Bounds-checked
// (class bounds). No unsafe, no MethodImpl, no Unsafe.*, no
// System.Runtime.Intrinsics, no Vector<T>.
// Usage: comphash <in> <out>.
using System;
using System.IO;
using System.IO.Compression;
using System.Security.Cryptography;

internal sealed class StrictEofStream : Stream
{
    private readonly Stream _inner;

    public StrictEofStream(Stream inner) => _inner = inner;

    public override bool CanRead => true;
    public override bool CanSeek => false;
    public override bool CanWrite => false;
    public override long Length => throw new NotSupportedException();
    public override long Position { get => throw new NotSupportedException(); set => throw new NotSupportedException(); }
    public override void Flush() { }
    public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
    public override void SetLength(long value) => throw new NotSupportedException();
    public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();

    public override int Read(byte[] buffer, int offset, int count)
    {
        int n = _inner.Read(buffer, offset, count);
        if (n == 0 && count > 0)
        {
            throw new InvalidDataException("truncated zlib stream");
        }
        return n;
    }

    public override int Read(Span<byte> buffer)
    {
        int n = _inner.Read(buffer);
        if (n == 0 && buffer.Length > 0)
        {
            throw new InvalidDataException("truncated zlib stream");
        }
        return n;
    }
}

internal static class Program
{
    private const int ChunkSize = 64 * 1024;

    private static (long Total, byte[] Digest) CompressAndHash(string inPath, string outPath, byte[] buf)
    {
        long total = 0;
        using IncrementalHash sha = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
        using (FileStream src = new FileStream(inPath, FileMode.Open, FileAccess.Read, FileShare.Read, 1))
        using (FileStream dst = new FileStream(outPath, FileMode.Create, FileAccess.Write, FileShare.None, ChunkSize))
        {
            ZLibCompressionOptions opts = new ZLibCompressionOptions { CompressionLevel = 6 };
            using (ZLibStream zw = new ZLibStream(dst, opts, leaveOpen: true))
            {
                while (true)
                {
                    int n = src.Read(buf, 0, buf.Length);
                    if (n == 0)
                    {
                        break;
                    }
                    sha.AppendData(buf, 0, n);
                    zw.Write(buf, 0, n);
                    total += n;
                }
            }
            dst.Flush();
        }
        return (total, sha.GetCurrentHash());
    }

    private static (long Total, byte[] Digest) DecompressAndHash(string inPath, byte[] buf)
    {
        long total = 0;
        using IncrementalHash sha = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
        using (FileStream src = new FileStream(inPath, FileMode.Open, FileAccess.Read, FileShare.Read, ChunkSize))
        using (StrictEofStream strictSrc = new StrictEofStream(src))
        using (ZLibStream zr = new ZLibStream(strictSrc, CompressionMode.Decompress, leaveOpen: true))
        {
            while (true)
            {
                int n = zr.Read(buf, 0, buf.Length);
                if (n == 0)
                {
                    break;
                }
                sha.AppendData(buf, 0, n);
                total += n;
            }
        }
        return (total, sha.GetCurrentHash());
    }

    public static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: comphash <in> <out>");
            return 2;
        }
        try
        {
            byte[] buf = new byte[ChunkSize];
            (long inTotal, byte[] inDigest) = CompressAndHash(args[0], args[1], buf);
            (long outTotal, byte[] outDigest) = DecompressAndHash(args[1], buf);
            if (outTotal != inTotal)
            {
                Console.Error.WriteLine("comphash: decompressed length differs from input length");
                return 1;
            }
            if (!inDigest.AsSpan().SequenceEqual(outDigest))
            {
                Console.Error.WriteLine("comphash: decompressed SHA-256 differs from input SHA-256");
                return 1;
            }
            string hex = Convert.ToHexStringLower(inDigest);
            Console.WriteLine($"{inTotal} {hex}");
            return 0;
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"comphash: {ex.Message}");
            return 1;
        }
    }
}
