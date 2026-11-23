// zlib-d, C#, std: the task of README.md with System.IO.Compression.ZLibStream
// (RFC 1950 zlib framing; validates header FCHECK/FDICT, Adler-32 trailer and
// stream termination: ZLibStream stops reading once inflate reaches
// Z_STREAM_END, so a StrictEofStream wrapper throws InvalidDataException if the
// underlying stream hits EOF before the zlib stream terminates). Reads the
// input file through a 64 KiB buffered FileStream, inflates into a 64 KiB
// buffer, writes each buffer to a 64 KiB buffered output FileStream, and prints
// the count of decompressed bytes. Bounds-checked (class bounds). No unsafe, no
// MethodImpl, no Unsafe.*, no System.Runtime.Intrinsics, no Vector<T>.
// Usage: zlibd <in> <out>.
using System;
using System.IO;
using System.IO.Compression;

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

    public static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: zlibd <in> <out>");
            return 2;
        }
        try
        {
            long total = 0;
            byte[] buf = new byte[ChunkSize];
            using (FileStream src = new FileStream(args[0], FileMode.Open, FileAccess.Read, FileShare.Read, ChunkSize))
            using (StrictEofStream strictSrc = new StrictEofStream(src))
            using (FileStream dst = new FileStream(args[1], FileMode.Create, FileAccess.Write, FileShare.None, ChunkSize))
            {
                using (ZLibStream zr = new ZLibStream(strictSrc, CompressionMode.Decompress, leaveOpen: true))
                {
                    while (true)
                    {
                        int n = zr.Read(buf, 0, buf.Length);
                        if (n == 0)
                        {
                            break;
                        }
                        dst.Write(buf, 0, n);
                        total += n;
                    }
                }
                dst.Flush();
            }
            Console.WriteLine(total);
            return 0;
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"zlibd: {ex.Message}");
            return 1;
        }
    }
}
