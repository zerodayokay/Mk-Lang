// zlib-c, C#, std: the task of README.md with System.IO.Compression.ZLibStream
// at level 6 (ZLibCompressionOptions.CompressionLevel = 6, RFC 1950 zlib
// framing). Reads the input file in 64 KiB chunks, writes each chunk into
// ZLibStream over a 64 KiB buffered output FileStream, disposes ZLibStream to
// write the final block and Adler-32 trailer, flushes and closes the output
// file, and prints the count of input bytes. Bounds-checked (class bounds).
// No unsafe, no MethodImpl, no Unsafe.*, no System.Runtime.Intrinsics, no
// Vector<T>.
// Usage: zlibc <in> <out>.
using System;
using System.IO;
using System.IO.Compression;

internal static class Program
{
    private const int ChunkSize = 64 * 1024;

    public static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: zlibc <in> <out>");
            return 2;
        }
        try
        {
            long total = 0;
            byte[] buf = new byte[ChunkSize];
            using (FileStream src = new FileStream(args[0], FileMode.Open, FileAccess.Read, FileShare.Read, 1))
            using (FileStream dst = new FileStream(args[1], FileMode.Create, FileAccess.Write, FileShare.None, ChunkSize))
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
                        zw.Write(buf, 0, n);
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
            Console.Error.WriteLine($"zlibc: {ex.Message}");
            return 1;
        }
    }
}
