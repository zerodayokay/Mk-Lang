// regex, C#: filter log lines with System.Text.RegularExpressions.Regex (NonBacktracking linear-time engine) (README.md).
// Usage: regex <file> <rounds>.
using System;
using System.Globalization;
using System.IO;
using System.Text;
using System.Text.RegularExpressions;

internal static class Program
{
    private const string Pattern = @"status=5[0-9][0-9] dur=[0-9]{4,}ms";

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: regex <file> <rounds>");
            return 2;
        }
        string text = File.ReadAllText(args[0], new UTF8Encoding(false, true));
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);
        var re = new Regex(Pattern, RegexOptions.NonBacktracking | RegexOptions.CultureInvariant);

        long total = 0;
        for (int r = 0; r < rounds; r++)
        {
            foreach (ReadOnlySpan<char> line in text.AsSpan().EnumerateLines())
            {
                foreach (ValueMatch m in re.EnumerateMatches(line))
                {
                    total += 1_000_000L + m.Length;
                    break;
                }
            }
        }
        Console.WriteLine(total);
        return 0;
    }
}
