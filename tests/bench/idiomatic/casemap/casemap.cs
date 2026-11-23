// casemap, C#: lower-case each line of generated mixed-script text with MemoryExtensions.ToLowerInvariant (README.md).
// Usage: casemap <kib> <rounds> <seed>.
using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;

internal static class Program
{
    private static readonly (int Start, int End)[] Ranges =
    {
        (0x41, 0x5A), (0x61, 0x7A),
        (0xC0, 0xD6), (0xD8, 0xDE), (0xE0, 0xF6), (0xF8, 0xFE),
        (0x391, 0x3A1), (0x3A4, 0x3A9), (0x3B1, 0x3C1), (0x3C4, 0x3C9),
        (0x410, 0x44F),
    };

    private static ulong Mix(ulong z)
    {
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static string Gen(int targetBytes, ulong seed)
    {
        var tabList = new List<char>(256);
        foreach ((int start, int end) in Ranges)
        {
            for (int c = start; c <= end; c++)
            {
                tabList.Add((char)c);
            }
        }
        char[] tab = tabList.ToArray();
        ulong tabLen = (ulong)tab.Length;

        var sb = new StringBuilder(targetBytes);
        int utf8Len = 0;
        ulong st = seed;
        while (utf8Len < targetBytes)
        {
            st += 0x9e3779b97f4a7c15UL;
            int letters = (int)(Mix(st) % 12UL) + 1;
            for (int i = 0; i < letters; i++)
            {
                st += 0x9e3779b97f4a7c15UL;
                char ch = tab[(int)(Mix(st) % tabLen)];
                sb.Append(ch);
                utf8Len += ch <= 0x7F ? 1 : 2;
            }
            st += 0x9e3779b97f4a7c15UL;
            sb.Append((Mix(st) % 12UL == 0) ? '\n' : ' ');
            utf8Len++;
        }
        sb.Append('\n');
        return sb.ToString();
    }

    private static ulong Round(string text, ref char[] lowerBuf, ref byte[] utf8Buf)
    {
        ulong h = 0xcbf29ce484222325UL;
        ulong n = 0;
        ReadOnlySpan<char> span = text.AsSpan();
        foreach (Range r in span.Split('\n'))
        {
            ReadOnlySpan<char> line = span[r];
            if (line.IsEmpty)
            {
                continue;
            }
            if (line.Length > lowerBuf.Length)
            {
                lowerBuf = new char[line.Length * 2];
            }
            int written = line.ToLowerInvariant(lowerBuf);
            int maxBytes = written * 3;
            if (maxBytes > utf8Buf.Length)
            {
                utf8Buf = new byte[maxBytes * 2];
            }
            int byteCount = Encoding.UTF8.GetBytes(lowerBuf.AsSpan(0, written), utf8Buf);
            for (int i = 0; i < byteCount; i++)
            {
                h = (h ^ utf8Buf[i]) * 0x100000001b3UL;
            }
            n += (ulong)byteCount;
        }
        return h + n;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: casemap <kib> <rounds> <seed>");
            return 2;
        }
        int kib = int.Parse(args[0], CultureInfo.InvariantCulture);
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);
        ulong seed = ulong.Parse(args[2], CultureInfo.InvariantCulture);

        string text = Gen(kib * 1024, seed);
        char[] lowerBuf = new char[1024];
        byte[] utf8Buf = new byte[4096];
        ulong total = 0;
        for (int r = 0; r < rounds; r++)
        {
            total += Round(text, ref lowerBuf, ref utf8Buf);
        }
        Console.WriteLine(total);
        return 0;
    }
}
