// json, C#: parse JSON into JsonDocument, serialise with Utf8JsonWriter, reparse and walk (README.md).
// Reflection-free System.Text.Json DOM APIs (compatible with both RyuJIT and NativeAOT).
// Usage: json <file> <rounds>.
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

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: json <file> <rounds>");
            return 2;
        }
        byte[] b = File.ReadAllBytes(args[0]);
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);

        var bw = new ArrayBufferWriter<byte>(b.Length);
        long total = 0;
        for (int r = 0; r < rounds; r++)
        {
            bw.Clear();
            using (JsonDocument doc1 = JsonDocument.Parse(b))
            {
                using var writer = new Utf8JsonWriter(bw);
                doc1.WriteTo(writer);
                writer.Flush();
            }
            using (JsonDocument doc2 = JsonDocument.Parse(bw.WrittenMemory))
            {
                total += Walk(doc2.RootElement);
            }
        }
        Console.WriteLine(total);
        return 0;
    }
}
