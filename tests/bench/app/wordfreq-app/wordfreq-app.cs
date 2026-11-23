// wordfreq-app, C#, std: the task of README.md. Streams the input file through
// a 64 KiB read buffer, tokenizes ASCII words ([A-Za-z], folded to lowercase)
// carrying partial words across chunk boundaries, counts frequencies in
// Dictionary<string, long> using .NET's GetAlternateLookup<ReadOnlySpan<char>>
// and CollectionsMarshal.GetValueRefOrAddDefault (zero string allocation and
// zero boxing on hash hits), sorts unique entries with List.Sort by (count
// desc, word asc), and prints total, unique and top-K lines to a 64 KiB
// buffered StreamWriter. Bounds-checked (class bounds). No unsafe, no
// MethodImpl, no Unsafe.*, no System.Runtime.Intrinsics, no Vector<T>.
// Usage: wordfreq-app <file> <top_k> [<max_words>].
using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;

internal static class Program
{
    private const int ChunkSize = 64 * 1024;

    private readonly struct Entry
    {
        public readonly string Word;
        public readonly long Count;

        public Entry(string word, long count)
        {
            Word = word;
            Count = count;
        }
    }

    private static void RecordWord(
        Dictionary<string, long>.AlternateLookup<ReadOnlySpan<char>> lookup,
        ReadOnlySpan<char> word)
    {
        ref long count = ref CollectionsMarshal.GetValueRefOrAddDefault(lookup, word, out _);
        count++;
    }

    public static int Main(string[] args)
    {
        if (args.Length < 2 || args.Length > 3)
        {
            Console.Error.WriteLine("usage: wordfreq-app <file> <top_k> [<max_words>]");
            return 2;
        }
        int topK = int.Parse(args[1], CultureInfo.InvariantCulture);
        long limit = args.Length == 3 ? long.Parse(args[2], CultureInfo.InvariantCulture) : -1;

        Dictionary<string, long> counts = new Dictionary<string, long>(1024, StringComparer.Ordinal);
        Dictionary<string, long>.AlternateLookup<ReadOnlySpan<char>> lookup = counts.GetAlternateLookup<ReadOnlySpan<char>>();

        byte[] buf = new byte[ChunkSize];
        char[] wbuf = new char[256];
        int wlen = 0;
        long total = 0;
        bool done = false;

        try
        {
            using FileStream fs = new FileStream(args[0], FileMode.Open, FileAccess.Read, FileShare.Read, 1);
            while (!done)
            {
                int n = fs.Read(buf, 0, buf.Length);
                if (n == 0)
                {
                    break;
                }
                int i = 0;
                while (i < n)
                {
                    byte c = buf[i];
                    if ((c >= (byte)'a' && c <= (byte)'z') || (c >= (byte)'A' && c <= (byte)'Z'))
                    {
                        if (wlen == 0)
                        {
                            int start = i;
                            i++;
                            while (i < n)
                            {
                                byte b = buf[i];
                                if ((b >= (byte)'a' && b <= (byte)'z') || (b >= (byte)'A' && b <= (byte)'Z'))
                                {
                                    i++;
                                }
                                else
                                {
                                    break;
                                }
                            }
                            int len = i - start;
                            if (len > wbuf.Length)
                            {
                                int cap = wbuf.Length;
                                while (cap < len) cap *= 2;
                                wbuf = new char[cap];
                            }
                            for (int j = 0; j < len; j++)
                            {
                                byte ch = buf[start + j];
                                wbuf[j] = (char)(ch >= (byte)'A' && ch <= (byte)'Z' ? ch + 32 : ch);
                            }
                            if (i == n)
                            {
                                wlen = len;
                            }
                            else
                            {
                                RecordWord(lookup, wbuf.AsSpan(0, len));
                                total++;
                                if (limit >= 0 && total >= limit)
                                {
                                    done = true;
                                    break;
                                }
                            }
                        }
                        else
                        {
                            if (wlen == wbuf.Length)
                            {
                                Array.Resize(ref wbuf, wbuf.Length * 2);
                            }
                            wbuf[wlen++] = (char)(c >= (byte)'A' && c <= (byte)'Z' ? c + 32 : c);
                            i++;
                        }
                    }
                    else
                    {
                        if (wlen > 0)
                        {
                            RecordWord(lookup, wbuf.AsSpan(0, wlen));
                            wlen = 0;
                            total++;
                            if (limit >= 0 && total >= limit)
                            {
                                done = true;
                                break;
                            }
                        }
                        i++;
                    }
                }
            }
            if (!done && wlen > 0)
            {
                RecordWord(lookup, wbuf.AsSpan(0, wlen));
                total++;
            }
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"wordfreq-app: {ex.Message}");
            return 1;
        }

        List<Entry> entries = new List<Entry>(counts.Count);
        foreach (KeyValuePair<string, long> kv in counts)
        {
            entries.Add(new Entry(kv.Key, kv.Value));
        }
        entries.Sort(static (a, b) =>
        {
            int c = b.Count.CompareTo(a.Count);
            return c != 0 ? c : string.CompareOrdinal(a.Word, b.Word);
        });

        int uniq = entries.Count;
        int k = Math.Min(Math.Max(topK, 0), uniq);
        using Stream stdout = Console.OpenStandardOutput();
        using StreamWriter w = new StreamWriter(stdout, new UTF8Encoding(false), ChunkSize, leaveOpen: true);
        w.NewLine = "\n";
        w.Write(string.Create(CultureInfo.InvariantCulture, $"total {total}\nunique {uniq}\n"));
        for (int i = 0; i < k; i++)
        {
            w.Write(entries[i].Word);
            w.Write(' ');
            w.Write(entries[i].Count);
            w.Write('\n');
        }
        w.Flush();
        return 0;
    }
}
