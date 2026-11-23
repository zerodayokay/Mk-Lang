// bufwrite, C#: write n formatted lines through Utf8.TryWrite and a 64 KiB buffered FileStream, then read back and hash (README.md).
// Usage: bufwrite <n>.
using System;
using System.Globalization;
using System.IO;
using System.Text.Unicode;

internal static class Program
{
    private static int Main(string[] args)
    {
        if (args.Length != 1)
        {
            Console.Error.WriteLine("usage: bufwrite <n>");
            return 2;
        }
        ulong n = ulong.Parse(args[0], CultureInfo.InvariantCulture);
        string path = Path.Combine(Path.GetTempPath(), $"mkbench-bufwrite-cs-{Environment.ProcessId}.txt");
        try
        {
            Span<byte> line = stackalloc byte[64];
            CultureInfo inv = CultureInfo.InvariantCulture;
            using (var fs = new FileStream(path, FileMode.Create, FileAccess.Write, FileShare.None, 65536))
            {
                for (ulong u = 0; u < n; u++)
                {
                    ulong hval = u * 0x9e3779b97f4a7c15UL;
                    if (!Utf8.TryWrite(line, inv, $"{u} {hval:x} {u * 7UL}\n", out int written))
                    {
                        return 1;
                    }
                    fs.Write(line.Slice(0, written));
                }
            }

            byte[] b = File.ReadAllBytes(path);
            ulong h = 0xcbf29ce484222325UL;
            for (int i = 0; i < b.Length; i++)
            {
                h = (h ^ b[i]) * 0x100000001b3UL;
            }
            ulong total = (ulong)b.Length + h;
            Console.WriteLine(total);
            return 0;
        }
        finally
        {
            if (File.Exists(path))
            {
                File.Delete(path);
            }
        }
    }
}
