// grep-j8, C#, std: the task of README.md, app/grep's re variant searched in
// parallel by -j OS threads (System.Threading.Thread), with
// DirectoryInfo.GetFileSystemInfos (sorted by name with StringComparer.Ordinal,
// bytewise), per-worker reused byte[] and char[] buffers,
// System.Text.Unicode.Utf8.IsValid and System.Text.RegularExpressions.Regex.
// The walk lists every file first in mgrep's order (depth first, names starting
// with "." skipped). Then -j worker threads claim file indexes via
// Interlocked.Increment; each reads its file whole, skips it as binary when a
// NUL byte is in its first 1,024 bytes, and counts the lines that hold a match
// into the file's slot. When every worker finishes, "path:count" is printed in
// walk order. Bounds-checked (class bounds). No unsafe, no MethodImpl, no
// Unsafe.*, no System.Runtime.Intrinsics, no Vector<T>.
// Usage: grepj8 -c -j N --path-separator / -i PATTERN DIR.
using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.RegularExpressions;
using System.Text.Unicode;
using System.Threading;

internal static class Program
{
    private const int SniffLen = 1024;

    private readonly struct FileEntry
    {
        public readonly string Path;
        public readonly string Shown;
        public FileEntry(string path, string shown)
        {
            Path = path;
            Shown = shown;
        }
    }

    private struct FileResult
    {
        public int Count;
        public bool Binary;
        public string? Error;
    }

    private static int Usage()
    {
        Console.Error.WriteLine("usage: grepj8 -c -j N --path-separator / -i PATTERN DIR");
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

    private static bool Walk(List<FileEntry> files, string dir, string name)
    {
        FileSystemInfo[] entries;
        try
        {
            entries = new DirectoryInfo(dir).GetFileSystemInfos();
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"grepj8: {name}: {ex.Message}");
            return true;
        }
        Array.Sort(entries, static (a, b) => string.CompareOrdinal(a.Name, b.Name));
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
                if (Walk(files, entry.FullName, shown))
                {
                    failed = true;
                }
                continue;
            }
            files.Add(new FileEntry(entry.FullName, shown));
        }
        return failed;
    }

    private static int CountRegex(Regex re, ReadOnlySpan<byte> buf, ref char[] charBuf)
    {
        if (buf.Length == 0)
        {
            return 0;
        }
        int maxChars = Encoding.UTF8.GetMaxCharCount(buf.Length);
        if (charBuf.Length < maxChars)
        {
            charBuf = new char[Math.Max(maxChars, charBuf.Length * 2)];
        }
        int charLen = Encoding.UTF8.GetChars(buf, charBuf);
        ReadOnlySpan<char> text = charBuf.AsSpan(0, charLen);
        int n = 0;
        int pos = 0;
        while (pos < text.Length)
        {
            Regex.ValueMatchEnumerator en = re.EnumerateMatches(text.Slice(pos));
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

    private static FileResult SearchFile(Regex re, string path, ref byte[] readBuf, ref char[] charBuf)
    {
        int len;
        try
        {
            using FileStream fs = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read, 1);
            long len64 = fs.Length;
            if (len64 == 0)
            {
                return new FileResult { Count = 0 };
            }
            len = checked((int)len64);
            if (readBuf.Length < len)
            {
                readBuf = new byte[Math.Max(len, readBuf.Length * 2)];
            }
            int total = 0;
            while (total < len)
            {
                int r = fs.Read(readBuf, total, len - total);
                if (r == 0)
                {
                    break;
                }
                total += r;
            }
            len = total;
        }
        catch (Exception ex)
        {
            return new FileResult { Error = ex.Message };
        }
        ReadOnlySpan<byte> buf = readBuf.AsSpan(0, len);
        if (buf.Slice(0, Math.Min(len, SniffLen)).IndexOf((byte)0) >= 0)
        {
            return new FileResult { Binary = true };
        }
        if (Utf8.IsValid(buf))
        {
            return new FileResult { Count = CountRegex(re, buf, ref charBuf) };
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
            if (Utf8.IsValid(line) && CountRegex(re, line, ref charBuf) > 0)
            {
                n++;
            }
        }
        return new FileResult { Count = n };
    }

    public static int Main(string[] args)
    {
        bool fold = false;
        bool count = false;
        int threads = 0;
        List<string> rest = new List<string>(2);
        for (int i = 0; i < args.Length; i++)
        {
            string a = args[i];
            switch (a)
            {
                case "-c":
                    count = true;
                    break;
                case "-i":
                    fold = true;
                    break;
                case "-j":
                    i++;
                    if (i == args.Length || !int.TryParse(args[i], out int n) || n < 1 || n > 256)
                    {
                        return Usage();
                    }
                    threads = n;
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
        if (!count || threads == 0 || rest.Count != 2)
        {
            return Usage();
        }
        string pat = rest[0];
        string root = rest[1];
        RegexOptions opts = RegexOptions.Compiled;
        if (fold)
        {
            opts |= RegexOptions.IgnoreCase;
        }
        Regex re;
        try
        {
            re = new Regex(pat, opts);
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"grepj8: {ex.Message}");
            return 2;
        }

        List<FileEntry> files = new List<FileEntry>(16384);
        bool failed = Walk(files, root, root.Replace('\\', '/'));
        FileResult[] results = new FileResult[files.Count];
        int nextIdx = -1;
        int fileCount = files.Count;

        Thread[] workers = new Thread[threads];
        for (int t = 0; t < threads; t++)
        {
            workers[t] = new Thread(() =>
            {
                byte[] readBuf = new byte[64 * 1024];
                char[] charBuf = new char[64 * 1024];
                while (true)
                {
                    int idx = Interlocked.Increment(ref nextIdx);
                    if (idx >= fileCount)
                    {
                        break;
                    }
                    results[idx] = SearchFile(re, files[idx].Path, ref readBuf, ref charBuf);
                }
            });
            workers[t].Start();
        }
        for (int t = 0; t < threads; t++)
        {
            workers[t].Join();
        }

        using Stream stdout = Console.OpenStandardOutput();
        using StreamWriter w = new StreamWriter(stdout, new UTF8Encoding(false), 64 * 1024, leaveOpen: true);
        w.NewLine = "\n";
        bool matched = false;
        for (int i = 0; i < fileCount; i++)
        {
            FileResult r = results[i];
            if (r.Error != null)
            {
                Console.Error.WriteLine($"grepj8: {files[i].Shown}: {r.Error}");
                failed = true;
            }
            else if (!r.Binary)
            {
                if (r.Count > 0)
                {
                    matched = true;
                }
                w.Write(files[i].Shown);
                w.Write(':');
                w.Write(r.Count);
                w.Write('\n');
            }
        }
        w.Flush();
        if (failed)
        {
            return 2;
        }
        return matched ? 0 : 1;
    }
}
