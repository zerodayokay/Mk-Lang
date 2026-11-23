// grep, C#, std: the task of README.md with DirectoryInfo.GetFileSystemInfos
// (sorted by name with StringComparer.Ordinal, bytewise), a reused byte[] read
// buffer, System.Text.Unicode.Utf8.IsValid, ReadOnlySpan<byte>.IndexOf /
// IndexOfAny for literals and System.Text.RegularExpressions.Regex for regex
// patterns. The walk is mgrep's: each directory's entries in byte order, files
// and directories interleaved, depth first, names starting with "." skipped. A
// file with a NUL byte in its first 1,024 bytes is skipped as binary; every
// other file prints "path:count", the number of its lines that hold a match.
// Valid UTF-8 files are searched whole; invalid files are judged line by line
// and invalid lines skipped. -F or a pattern with no regex meta byte is a
// literal; -i on an ASCII literal folds ASCII case only; -i on a regex uses
// RegexOptions.IgnoreCase. Bounds-checked (class bounds). No unsafe, no
// MethodImpl, no Unsafe.*, no System.Runtime.Intrinsics, no Vector<T>.
// Usage: grep -c -j 1 --path-separator / [-F|-i] PATTERN DIR.
using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.RegularExpressions;
using System.Text.Unicode;

internal static class Program
{
    private const int SniffLen = 1024;
    private const string MetaBytes = @"\.+*?()|[]{}^$";

    private sealed class Searcher
    {
        public Regex? Re;
        public byte[]? Needle;
        public bool FoldAscii;
        public byte FirstLo;
        public byte FirstHi;
        public byte[] ReadBuf = new byte[64 * 1024];
        public char[] CharBuf = new char[64 * 1024];

        public int Count(ReadOnlySpan<byte> buf)
        {
            if (Utf8.IsValid(buf))
            {
                return CountLines(buf);
            }
            int n = 0;
            int offset = 0;
            while (offset < buf.Length)
            {
                ReadOnlySpan<byte> rem = buf.Slice(offset);
                int nl = rem.IndexOf((byte)'\n');
                ReadOnlySpan<byte> line;
                if (nl >= 0)
                {
                    line = rem.Slice(0, nl);
                    offset += nl + 1;
                }
                else
                {
                    line = rem;
                    offset = buf.Length;
                }
                if (line.Length > 0 && line[line.Length - 1] == (byte)'\r')
                {
                    line = line.Slice(0, line.Length - 1);
                }
                if (Utf8.IsValid(line) && CountLines(line) > 0)
                {
                    n++;
                }
            }
            return n;
        }

        private int CountLines(ReadOnlySpan<byte> buf)
        {
            if (buf.Length == 0)
            {
                return 0;
            }
            if (Re != null)
            {
                int maxChars = Encoding.UTF8.GetMaxCharCount(buf.Length);
                if (CharBuf.Length < maxChars)
                {
                    CharBuf = new char[Math.Max(maxChars, CharBuf.Length * 2)];
                }
                int charLen = Encoding.UTF8.GetChars(buf, CharBuf);
                ReadOnlySpan<char> text = CharBuf.AsSpan(0, charLen);
                int n = 0;
                int pos = 0;
                while (pos < text.Length)
                {
                    Regex.ValueMatchEnumerator en = Re.EnumerateMatches(text.Slice(pos));
                    if (!en.MoveNext())
                    {
                        break;
                    }
                    int at = pos + en.Current.Index;
                    n++;
                    int nl = text.Slice(at).IndexOf('\n');
                    if (nl < 0)
                    {
                        break;
                    }
                    pos = at + nl + 1;
                }
                return n;
            }
            byte[] needle = Needle!;
            if (!FoldAscii)
            {
                int n = 0;
                int pos = 0;
                ReadOnlySpan<byte> needleSpan = needle;
                while (pos < buf.Length)
                {
                    int idx = buf.Slice(pos).IndexOf(needleSpan);
                    if (idx < 0)
                    {
                        break;
                    }
                    int at = pos + idx;
                    n++;
                    int nl = buf.Slice(at).IndexOf((byte)'\n');
                    if (nl < 0)
                    {
                        break;
                    }
                    pos = at + nl + 1;
                }
                return n;
            }
            else
            {
                int n = 0;
                int pos = 0;
                int nlen = needle.Length;
                byte lo = FirstLo;
                byte hi = FirstHi;
                while (pos + nlen <= buf.Length)
                {
                    ReadOnlySpan<byte> rem = buf.Slice(pos, buf.Length - pos - nlen + 1);
                    int idx = lo == hi ? rem.IndexOf(lo) : rem.IndexOfAny(lo, hi);
                    if (idx < 0)
                    {
                        break;
                    }
                    int at = pos + idx;
                    bool ok = true;
                    for (int k = 1; k < nlen; k++)
                    {
                        byte c = buf[at + k];
                        if (c >= (byte)'A' && c <= (byte)'Z')
                        {
                            c = (byte)(c + 32);
                        }
                        if (c != needle[k])
                        {
                            ok = false;
                            break;
                        }
                    }
                    if (!ok)
                    {
                        pos = at + 1;
                        continue;
                    }
                    n++;
                    int nl = buf.Slice(at).IndexOf((byte)'\n');
                    if (nl < 0)
                    {
                        break;
                    }
                    pos = at + nl + 1;
                }
                return n;
            }
        }
    }

    private static int Usage()
    {
        Console.Error.WriteLine("usage: grep -c -j 1 --path-separator / [-F|-i] PATTERN DIR");
        return 2;
    }

    private static string Joined(string name, string leaf)
    {
        if (name.EndsWith('/') || name.EndsWith('\\'))
        {
            return name + leaf;
        }
        return name + "/" + leaf;
    }

    private static int ReadWhole(string path, Searcher s)
    {
        using FileStream fs = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read, 1);
        long len64 = fs.Length;
        if (len64 == 0)
        {
            return 0;
        }
        int len = checked((int)len64);
        if (s.ReadBuf.Length < len)
        {
            s.ReadBuf = new byte[Math.Max(len, s.ReadBuf.Length * 2)];
        }
        int total = 0;
        while (total < len)
        {
            int r = fs.Read(s.ReadBuf, total, len - total);
            if (r == 0)
            {
                break;
            }
            total += r;
        }
        return total;
    }

    private static (bool Matched, bool Failed) Walk(StreamWriter w, Searcher s, string dir, string name)
    {
        FileSystemInfo[] entries;
        try
        {
            entries = new DirectoryInfo(dir).GetFileSystemInfos();
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"grep: {name}: {ex.Message}");
            return (false, true);
        }
        Array.Sort(entries, static (a, b) => string.CompareOrdinal(a.Name, b.Name));
        bool matched = false;
        bool failed = false;
        foreach (FileSystemInfo entry in entries)
        {
            string leaf = entry.Name;
            if (leaf.Length > 0 && leaf[0] == '.')
            {
                continue;
            }
            string shown = Joined(name, leaf);
            if ((entry.Attributes & FileAttributes.Directory) != 0)
            {
                (bool m, bool f) = Walk(w, s, entry.FullName, shown);
                matched |= m;
                failed |= f;
                continue;
            }
            int len;
            try
            {
                len = ReadWhole(entry.FullName, s);
            }
            catch (Exception ex)
            {
                Console.Error.WriteLine($"grep: {shown}: {ex.Message}");
                failed = true;
                continue;
            }
            ReadOnlySpan<byte> buf = s.ReadBuf.AsSpan(0, len);
            if (buf.Slice(0, Math.Min(len, SniffLen)).IndexOf((byte)0) >= 0)
            {
                continue;
            }
            int n = s.Count(buf);
            if (n > 0)
            {
                matched = true;
            }
            w.Write(shown);
            w.Write(':');
            w.Write(n);
            w.Write('\n');
        }
        return (matched, failed);
    }

    public static int Main(string[] args)
    {
        bool fixedPat = false;
        bool fold = false;
        bool count = false;
        List<string> rest = new List<string>(2);
        for (int i = 0; i < args.Length; i++)
        {
            string a = args[i];
            switch (a)
            {
                case "-c":
                    count = true;
                    break;
                case "-F":
                    fixedPat = true;
                    break;
                case "-i":
                    fold = true;
                    break;
                case "-j":
                    i++;
                    if (i == args.Length || args[i] != "1")
                    {
                        return Usage();
                    }
                    break;
                case "--path-separator":
                    i++;
                    if (i == args.Length || args[i] != "/")
                    {
                        return Usage();
                    }
                    break;
                default:
                    if (a.StartsWith('-'))
                    {
                        return Usage();
                    }
                    rest.Add(a);
                    break;
            }
        }
        if (!count || rest.Count != 2)
        {
            return Usage();
        }
        string pat = rest[0];
        string root = rest[1];

        bool ascii = true;
        bool hasMeta = false;
        for (int i = 0; i < pat.Length; i++)
        {
            char ch = pat[i];
            if (ch >= 0x80)
            {
                ascii = false;
            }
            if (MetaBytes.IndexOf(ch) >= 0)
            {
                hasMeta = true;
            }
        }

        Searcher s = new Searcher();
        if (pat.Length > 0 && (fixedPat || !hasMeta) && (!fold || ascii))
        {
            byte[] bytes = Encoding.UTF8.GetBytes(pat);
            if (fold)
            {
                s.FoldAscii = true;
                for (int i = 0; i < bytes.Length; i++)
                {
                    if (bytes[i] >= (byte)'A' && bytes[i] <= (byte)'Z')
                    {
                        bytes[i] = (byte)(bytes[i] + 32);
                    }
                }
                s.FirstLo = bytes[0];
                s.FirstHi = (bytes[0] >= (byte)'a' && bytes[0] <= (byte)'z')
                    ? (byte)(bytes[0] - 32)
                    : bytes[0];
            }
            s.Needle = bytes;
        }
        else
        {
            string rxPat = fixedPat ? Regex.Escape(pat) : pat;
            RegexOptions opts = RegexOptions.Compiled;
            if (fold)
            {
                opts |= RegexOptions.IgnoreCase;
            }
            try
            {
                s.Re = new Regex(rxPat, opts);
            }
            catch (Exception ex)
            {
                Console.Error.WriteLine($"grep: {ex.Message}");
                return 2;
            }
        }

        using Stream stdout = Console.OpenStandardOutput();
        using StreamWriter w = new StreamWriter(stdout, new UTF8Encoding(false), 64 * 1024, leaveOpen: true);
        w.NewLine = "\n";
        string shownRoot = root.Replace('\\', '/');
        (bool matched, bool failed) = Walk(w, s, root, shownRoot);
        w.Flush();
        if (failed)
        {
            return 2;
        }
        return matched ? 0 : 1;
    }
}
