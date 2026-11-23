// knucleotide, C#: the reference algorithm of README.md (2-bit packed DNA
// k-mers in a fixed open-addressed linear-probing hash table, Fibonacci hash,
// 70% load factor). Wrapping ulong hashing and checksum (overflow class
// "wraps"). Bounds-checked (class bounds). No unsafe, no stackalloc, no
// MethodImpl, no Unsafe.*.
using System;

internal struct Entry
{
    public ulong Key;
    public ulong Val;
}

internal struct Table
{
    public Entry[] Entries;
    public int Cap;
    public int Shift;
    public int Len;
}

internal static class Program
{
    // tools/bench flips this line in a copy for the counting build.
    private const bool BenchCount = false;

    private const long Im = 139968;
    private const long Ia = 3877;
    private const long Ic = 29573;
    private const ulong FibMul = 0x9e3779b97f4a7c15UL;

    private static Table NewTable()
    {
        return new Table { Entries = new Entry[64], Cap = 64, Shift = 6, Len = 0 };
    }

    private static void Grow(ref Table tab, ref ulong rehashes)
    {
        if (BenchCount)
        {
            rehashes += 1;
        }
        int newCap = tab.Cap * 2;
        int newShift = tab.Shift + 1;
        Entry[] next = new Entry[newCap];
        int mask = newCap - 1;
        int sh = 64 - newShift;
        Entry[] old = tab.Entries;
        for (int i = 0; i < tab.Cap; i++)
        {
            Entry e = old[i];
            if (e.Val != 0)
            {
                int slot = (int)((e.Key * FibMul) >> sh) & mask;
                for (;;)
                {
                    if (next[slot].Val == 0)
                    {
                        next[slot] = e;
                        break;
                    }
                    slot = (slot + 1) & mask;
                }
            }
        }
        tab.Entries = next;
        tab.Cap = newCap;
        tab.Shift = newShift;
    }

    private static void Inc(ref Table tab, ulong key, ref ulong probes, ref ulong rehashes)
    {
        if ((tab.Len + 1) * 10 > tab.Cap * 7)
        {
            Grow(ref tab, ref rehashes);
        }
        Entry[] entries = tab.Entries;
        int mask = tab.Cap - 1;
        int slot = (int)((key * FibMul) >> (64 - tab.Shift)) & mask;
        for (;;)
        {
            if (BenchCount)
            {
                probes += 1;
            }
            ulong v = entries[slot].Val;
            if (v == 0)
            {
                entries[slot] = new Entry { Key = key, Val = 1 };
                tab.Len += 1;
                return;
            }
            if (entries[slot].Key == key)
            {
                entries[slot].Val = v + 1;
                return;
            }
            slot = (slot + 1) & mask;
        }
    }

    private static ulong Get(ref Table tab, ulong key, ref ulong probes)
    {
        Entry[] entries = tab.Entries;
        int mask = tab.Cap - 1;
        int slot = (int)((key * FibMul) >> (64 - tab.Shift)) & mask;
        for (;;)
        {
            if (BenchCount)
            {
                probes += 1;
            }
            ulong v = entries[slot].Val;
            if (v == 0)
            {
                return 0;
            }
            if (entries[slot].Key == key)
            {
                return v;
            }
            slot = (slot + 1) & mask;
        }
    }

    private static ulong PackSeq(string s)
    {
        ulong key = 0;
        for (int i = 0; i < s.Length; i++)
        {
            char c = s[i];
            ulong code = 3;
            if (c == 'A')
            {
                code = 0;
            }
            else if (c == 'C')
            {
                code = 1;
            }
            else if (c == 'G')
            {
                code = 2;
            }
            key = (key << 2) | code;
        }
        return key;
    }

    private static ulong CountK(byte[] seq, int k, ref ulong probes, ref ulong rehashes)
    {
        Table tab = NewTable();
        int n = seq.Length;
        if (n >= k)
        {
            ulong mask = (1UL << (2 * k)) - 1;
            ulong key = 0;
            for (int i = 0; i < k - 1; i++)
            {
                key = (key << 2) | seq[i];
            }
            for (int i = k - 1; i < n; i++)
            {
                key = ((key << 2) | seq[i]) & mask;
                Inc(ref tab, key, ref probes, ref rehashes);
            }
        }
        ulong h = (ulong)tab.Len;
        ulong maxVal = 0;
        Entry[] entries = tab.Entries;
        for (int i = 0; i < tab.Cap; i++)
        {
            ulong v = entries[i].Val;
            if (v > maxVal)
            {
                maxVal = v;
            }
        }
        h = h * 31UL + maxVal;
        if (k == 1)
        {
            for (ulong c = 0; c < 4; c++)
            {
                h = h * 31UL + Get(ref tab, c, ref probes);
            }
        }
        else if (k == 2)
        {
            for (ulong c = 0; c < 16; c++)
            {
                h = h * 31UL + Get(ref tab, c, ref probes);
            }
        }
        else if (k == 3)
        {
            h = h * 31UL + Get(ref tab, PackSeq("GGT"), ref probes);
        }
        else if (k == 4)
        {
            h = h * 31UL + Get(ref tab, PackSeq("GGTA"), ref probes);
        }
        else if (k == 6)
        {
            h = h * 31UL + Get(ref tab, PackSeq("GGTATT"), ref probes);
        }
        else if (k == 12)
        {
            h = h * 31UL + Get(ref tab, PackSeq("GGTATTTTAATT"), ref probes);
        }
        else if (k == 18)
        {
            h = h * 31UL + Get(ref tab, PackSeq("GGTATTTTAATTTATAGT"), ref probes);
        }
        return h;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: knucleotide <n> <rounds>");
            return 2;
        }
        int n = int.Parse(args[0]);
        int rounds = int.Parse(args[1]);
        byte[] seq = new byte[n];
        double p0 = 0.3029549426680;
        double p1 = p0 + 0.1979883004921;
        double p2 = p1 + 0.1975473066391;
        long seed = 42;
        for (int i = 0; i < n; i++)
        {
            seed = (seed * Ia + Ic) % Im;
            double r = (double)seed / (double)Im;
            if (r < p0)
            {
                seq[i] = 0;
            }
            else if (r < p1)
            {
                seq[i] = 1;
            }
            else if (r < p2)
            {
                seq[i] = 2;
            }
            else
            {
                seq[i] = 3;
            }
        }
        int[] ks = new int[] { 1, 2, 3, 4, 6, 12, 18 };
        ulong probes = 0;
        ulong rehashes = 0;
        ulong total = 0;
        for (int r = 0; r < rounds; r++)
        {
            for (int i = 0; i < ks.Length; i++)
            {
                total = total * 31UL + CountK(seq, ks[i], ref probes, ref rehashes);
            }
        }
        Console.WriteLine(total);
        if (BenchCount)
        {
            Console.WriteLine($"counts probes={probes} rehashes={rehashes}");
        }
        return 0;
    }
}
