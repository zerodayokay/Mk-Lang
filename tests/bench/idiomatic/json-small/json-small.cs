// json-small, C#: parse, serialise, reparse and walk each record line with JsonDocument and Utf8JsonWriter (README.md).
// Reflection-free System.Text.Json DOM APIs; each JsonDocument is disposed before the next document.
// Usage: json-small <file> <rounds>.
using System;
using System.Buffers;
using System.Globalization;
using System.IO;
using System.Text;
using System.Text.Json;

internal static class Program
{
    private static long Walk(JsonElement el)
    {
        switch (el.ValueKind)
        {
            case JsonValueKind.Null:
                return 7;
            case JsonValueKind.True:
                return 3;
            case JsonValueKind.False:
                return 5;
            case JsonValueKind.Number:
                return 1L + (long)el.GetDouble();
            case JsonValueKind.String:
                return 1L + Encoding.UTF8.GetByteCount(el.GetString()!);
            case JsonValueKind.Array:
                {
                    long n = 1;
                    foreach (JsonElement item in el.EnumerateArray())
                    {
                        n += Walk(item);
                    }
                    return n;
                }
            case JsonValueKind.Object:
                {
                    long n = 1;
                    foreach (JsonProperty prop in el.EnumerateObject())
                    {
                        n += 1L + Encoding.UTF8.GetByteCount(prop.Name) + Walk(prop.Value);
                    }
                    return n;
                }
            default:
                throw new InvalidDataException($"unexpected JSON value kind: {el.ValueKind}");
        }
    }

    private static ReadOnlyMemory<byte> TrimRecord(ReadOnlyMemory<byte> line)
    {
        ReadOnlySpan<byte> s = line.Span;
        int start = 0;
        int end = s.Length;
        if (end > 0 && s[end - 1] == (byte)'\r')
        {
            end--;
        }
        if (start < end && s[start] == (byte)'[')
        {
            start++;
        }
        if (end > start && (s[end - 1] == (byte)',' || s[end - 1] == (byte)']'))
        {
            end--;
        }
        return line.Slice(start, end - start);
    }

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: json-small <file> <rounds>");
            return 2;
        }
        byte[] b = File.ReadAllBytes(args[0]);
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);

        var bw = new ArrayBufferWriter<byte>(1024);
        using var writer = new Utf8JsonWriter(bw);
        ReadOnlyMemory<byte> mem = b;
        long total = 0;
        for (int r = 0; r < rounds; r++)
        {
            int pos = 0;
            while (pos < b.Length)
            {
                int nl = Array.IndexOf(b, (byte)'\n', pos);
                int end = nl >= 0 ? nl : b.Length;
                ReadOnlyMemory<byte> d = TrimRecord(mem.Slice(pos, end - pos));
                pos = nl >= 0 ? nl + 1 : b.Length;
                if (d.IsEmpty)
                {
                    continue;
                }
                bw.Clear();
                writer.Reset(bw);
                using (JsonDocument doc1 = JsonDocument.Parse(d))
                {
                    doc1.WriteTo(writer);
                    writer.Flush();
                }
                using (JsonDocument doc2 = JsonDocument.Parse(bw.WrittenMemory))
                {
                    total += Walk(doc2.RootElement);
                }
            }
        }
        Console.WriteLine(total);
        return 0;
    }
}
