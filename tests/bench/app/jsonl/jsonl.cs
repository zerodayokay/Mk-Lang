// jsonl, C#, std: the task of README.md with System.Text.Json source-generated
// serialization (JsonSerializerContext, AOT-compatible). Reads the input file
// through a 64 KiB byte buffer one line at a time (refusing lines longer than
// 64 KiB), decodes each UTF-8 line directly into a typed Event via
// JsonSerializer.Deserialize, groups ok records by user in Dictionary<string,
// Group> (BCL randomized Marvin32 string hash), counts decoded msg length in
// UTF-8 bytes via Encoding.UTF8.GetByteCount, sorts groups with List<Group>.Sort,
// and serialises the compact Report to a 64 KiB buffered stdout followed by LF.
// Bounds-checked (class bounds). No unsafe, no MethodImpl, no Unsafe.*, no
// System.Runtime.Intrinsics, no Vector<T>.
// Usage: jsonl <file> [<lines>].
using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;

internal sealed class Event
{
    [JsonPropertyName("id")]
    public long Id { get; set; }

    [JsonPropertyName("user")]
    public string User { get; set; } = string.Empty;

    [JsonPropertyName("event")]
    public string EventName { get; set; } = string.Empty;

    [JsonPropertyName("status")]
    public long Status { get; set; }

    [JsonPropertyName("bytes")]
    public long Bytes { get; set; }

    [JsonPropertyName("ms")]
    public long Ms { get; set; }

    [JsonPropertyName("delta")]
    public long Delta { get; set; }

    [JsonPropertyName("ok")]
    public bool Ok { get; set; }

    [JsonPropertyName("tags")]
    public string[] Tags { get; set; } = Array.Empty<string>();

    [JsonPropertyName("msg")]
    public string Msg { get; set; } = string.Empty;
}

internal sealed class Group
{
    [JsonPropertyName("user")]
    public string User { get; set; } = string.Empty;

    [JsonPropertyName("count")]
    public long Count { get; set; }

    [JsonPropertyName("first")]
    public long First { get; set; }

    [JsonPropertyName("bytes")]
    public long Bytes { get; set; }

    [JsonPropertyName("ms_min")]
    public long MsMin { get; set; }

    [JsonPropertyName("ms_max")]
    public long MsMax { get; set; }

    [JsonPropertyName("delta")]
    public long Delta { get; set; }

    [JsonPropertyName("tags")]
    public long Tags { get; set; }

    [JsonPropertyName("msg")]
    public long Msg { get; set; }

    [JsonPropertyName("buys")]
    public long Buys { get; set; }

    [JsonPropertyName("fails")]
    public long Fails { get; set; }

    public void Add(Event e)
    {
        Count++;
        Bytes += e.Bytes;
        if (e.Ms < MsMin)
        {
            MsMin = e.Ms;
        }
        if (e.Ms > MsMax)
        {
            MsMax = e.Ms;
        }
        Delta += e.Delta;
        Tags += e.Tags.Length;
        Msg += Encoding.UTF8.GetByteCount(e.Msg);
        if (e.EventName == "buy")
        {
            Buys++;
        }
        if (e.Status >= 400)
        {
            Fails++;
        }
    }
}

internal sealed class Report
{
    [JsonPropertyName("lines")]
    public long Lines { get; set; }

    [JsonPropertyName("kept")]
    public long Kept { get; set; }

    [JsonPropertyName("dropped")]
    public long Dropped { get; set; }

    [JsonPropertyName("groups")]
    public List<Group> Groups { get; set; } = new List<Group>();
}

[JsonSerializable(typeof(Event))]
[JsonSerializable(typeof(Report))]
internal partial class JsonlContext : JsonSerializerContext
{
}

internal static class Program
{
    private const int BufSize = 64 * 1024;

    private static void ProcessLine(ReadOnlySpan<byte> line, long lineNo, Dictionary<string, Group> groups, ref long kept)
    {
        if (line.Length > 0 && line[line.Length - 1] == (byte)'\r')
        {
            line = line.Slice(0, line.Length - 1);
        }
        Event? ev = JsonSerializer.Deserialize(line, JsonlContext.Default.Event);
        if (ev == null)
        {
            throw new InvalidDataException($"line {lineNo}: null event");
        }
        if (!ev.Ok)
        {
            return;
        }
        kept++;
        if (!groups.TryGetValue(ev.User, out Group? g))
        {
            g = new Group
            {
                User = ev.User,
                First = ev.Id,
                MsMin = ev.Ms,
                MsMax = ev.Ms,
            };
            groups[ev.User] = g;
        }
        g.Add(ev);
    }

    private static void Run(string[] args)
    {
        if (args.Length < 1 || args.Length > 2)
        {
            throw new ArgumentException("usage: jsonl <file> [<lines>]");
        }
        long limit = -1;
        if (args.Length == 2)
        {
            limit = long.Parse(args[1]);
        }

        Dictionary<string, Group> groups = new Dictionary<string, Group>(StringComparer.Ordinal);
        long lines = 0;
        long kept = 0;
        byte[] buf = new byte[BufSize];
        int start = 0;
        int end = 0;

        using (FileStream fs = new FileStream(args[0], FileMode.Open, FileAccess.Read, FileShare.Read, 1))
        {
            bool eof = false;
            while (lines != limit)
            {
                int nl = Array.IndexOf(buf, (byte)'\n', start, end - start);
                if (nl >= 0)
                {
                    lines++;
                    ProcessLine(buf.AsSpan(start, nl - start), lines, groups, ref kept);
                    start = nl + 1;
                    continue;
                }
                if (eof)
                {
                    if (end > start)
                    {
                        lines++;
                        ProcessLine(buf.AsSpan(start, end - start), lines, groups, ref kept);
                    }
                    break;
                }
                if (start > 0)
                {
                    int rem = end - start;
                    if (rem > 0)
                    {
                        Buffer.BlockCopy(buf, start, buf, 0, rem);
                    }
                    start = 0;
                    end = rem;
                }
                if (end == buf.Length)
                {
                    throw new InvalidDataException($"line {lines + 1}: exceeds {BufSize} byte buffer");
                }
                int r = fs.Read(buf, end, buf.Length - end);
                if (r == 0)
                {
                    eof = true;
                }
                else
                {
                    end += r;
                }
            }
        }

        List<Group> gs = new List<Group>(groups.Values);
        gs.Sort(static (a, b) =>
        {
            int c = b.Count.CompareTo(a.Count);
            if (c != 0)
            {
                return c;
            }
            c = b.Bytes.CompareTo(a.Bytes);
            if (c != 0)
            {
                return c;
            }
            return string.CompareOrdinal(a.User, b.User);
        });

        Report report = new Report
        {
            Lines = lines,
            Kept = kept,
            Dropped = lines - kept,
            Groups = gs,
        };

        using Stream stdout = Console.OpenStandardOutput();
        using BufferedStream bw = new BufferedStream(stdout, BufSize);
        JsonSerializer.Serialize(bw, report, JsonlContext.Default.Report);
        bw.WriteByte((byte)'\n');
        bw.Flush();
    }

    public static int Main(string[] args)
    {
        try
        {
            Run(args);
            return 0;
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"jsonl: {ex.Message}");
            return 1;
        }
    }
}
