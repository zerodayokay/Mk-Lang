// gzip, C#: inflate a gzip file in 64 KiB chunks with System.IO.Compression.GZipStream (README.md).
// GZipStream validates both CRC-32 and ISIZE in the gzip trailer on completion.
// Usage: gzip <file> <rounds>.
using System;
using System.Globalization;
using System.IO;
using System.IO.Compression;

internal static class Program
{
    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: gzip <file> <rounds>");
            return 2;
        }
        byte[] gz = File.ReadAllBytes(args[0]);
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);
        byte[] buf = new byte[64 * 1024];
        ulong total = 0;
        for (int r = 0; r < rounds; r++)
        {
            using var ms = new MemoryStream(gz, writable: false);
            using var zr = new GZipStream(ms, CompressionMode.Decompress);
            int n;
            while ((n = zr.Read(buf, 0, buf.Length)) > 0)
            {
                for (int i = 0; i < n; i++)
                {
                    total += buf[i];
                }
                total += (ulong)n;
            }
        }
        Console.WriteLine(total);
        return 0;
    }
}
