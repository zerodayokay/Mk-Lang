// regex-redux, C#, idiomatic std: the task of README.md with
// System.Text.RegularExpressions.Regex (RegexOptions.Compiled |
// RegexOptions.CultureInvariant). Generates the deterministic FASTA DNA
// sequence, compiles the cleanup regex, 9 DNA match-count patterns (counted
// via Regex.Count), and 5 IUB replacement patterns once, then runs rounds
// passes folding all counts and lengths into a wrapping ulong checksum.
// No unsafe, no MethodImpl, no Unsafe.*.
using System;
using System.Text;
using System.Text.RegularExpressions;

internal static class Program
{
    private const int Im = 139968;
    private const int Ia = 3877;
    private const int Ic = 29573;

    private static readonly string[] CountPats =
    {
        @"agggtaaa|tttaccct",
        @"[cgt]gggtaaa|tttaccc[acg]",
        @"a[act]ggtaaa|tttacc[agt]t",
        @"ag[act]gtaaa|tttac[agt]ct",
        @"agg[act]taaa|ttta[agt]cct",
        @"aggg[acg]aaa|ttt[cgt]ccct",
        @"agggt[cgt]aa|tt[acg]accct",
        @"agggta[cgt]a|t[acg]taccct",
        @"agggtaa[cgt]|[acg]ttaccct",
    };

    private static readonly string[] ReplPats =
    {
        @"tHa[Nt]",
        @"aND|caN|Ha[DS]|WaS",
        @"a[NSt]|BY",
        @"<[^>]*>",
        @"\|[^|][^|]*\|",
    };

    private static readonly string[] ReplWith =
    {
        "<4>",
        "<3>",
        "<2>",
        "|",
        "-",
    };

    private static int NextRand(ref int seed)
    {
        seed = (seed * Ia + Ic) % Im;
        return seed;
    }

    private static char HomoBase(int r)
    {
        if (r < 42404) return 'a';
        if (r < 70116) return 'c';
        if (r < 97766) return 'g';
        return 't';
    }

    private static char IubBase(int r)
    {
        if (r < 37791) return 'a';
        if (r < 54587) return 'c';
        if (r < 71383) return 'g';
        if (r < 109174) return 't';
        if (r < 111973) return 'B';
        if (r < 114772) return 'D';
        if (r < 117571) return 'H';
        if (r < 120370) return 'K';
        if (r < 123169) return 'M';
        if (r < 125968) return 'N';
        if (r < 128767) return 'R';
        if (r < 131566) return 'S';
        if (r < 134365) return 'V';
        if (r < 137164) return 'W';
        return 'Y';
    }

    private static void WriteSection(StringBuilder sb, string header, int count, bool iub, ref int seed)
    {
        sb.Append(header);
        int col = 0;
        for (int i = 0; i < count; i++)
        {
            int r = NextRand(ref seed);
            sb.Append(iub ? IubBase(r) : HomoBase(r));
            col++;
            if (col == 60)
            {
                sb.Append('\n');
                col = 0;
            }
        }
        if (col > 0)
        {
            sb.Append('\n');
        }
    }

    private static string GenFasta(int n)
    {
        var sb = new StringBuilder(n + n / 60 + 128);
        int seed = 42;
        int n1 = n / 2;
        int n2 = n - n1;
        WriteSection(sb, ">ONE Homo sapiens frequency\n", n1, false, ref seed);
        WriteSection(sb, ">TWO IUB ambiguity codes\n", n2, true, ref seed);
        return sb.ToString();
    }

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: regex-redux <n> <rounds>");
            return 2;
        }
        int n = int.Parse(args[0]);
        int rounds = int.Parse(args[1]);
        string raw = GenFasta(n);
        const RegexOptions opts = RegexOptions.Compiled | RegexOptions.CultureInvariant;
        var clean = new Regex(@">[^\n]*\n|\n", opts);
        var counts = new Regex[CountPats.Length];
        for (int i = 0; i < CountPats.Length; i++)
        {
            counts[i] = new Regex(CountPats[i], opts);
        }
        var repls = new Regex[ReplPats.Length];
        for (int i = 0; i < ReplPats.Length; i++)
        {
            repls[i] = new Regex(ReplPats[i], opts);
        }
        ulong total = 0;
        for (int round = 0; round < rounds; round++)
        {
            string seq = clean.Replace(raw, "");
            ulong h = (ulong)raw.Length * 131UL + (ulong)seq.Length;
            for (int i = 0; i < counts.Length; i++)
            {
                h = h * 131UL + (ulong)counts[i].Count(seq);
            }
            string cur = seq;
            for (int i = 0; i < repls.Length; i++)
            {
                cur = repls[i].Replace(cur, ReplWith[i]);
            }
            h = h * 131UL + (ulong)cur.Length;
            total += h;
        }
        Console.WriteLine(total);
        return 0;
    }
}
