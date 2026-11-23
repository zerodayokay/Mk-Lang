// hashmap-str, C#, idiomatic std: the task of README.md with
// System.Collections.Generic.Dictionary<string, ulong> (Marvin32 randomized
// per-process string hash: DoS resistant). Keys are 8-24 char ASCII strings
// built once from one contiguous char buffer before the rounds loop, and
// each round grows a fresh Dictionary from empty. No unsafe, no MethodImpl,
// no Unsafe.*.
using System;
using System.Collections.Generic;

internal static class Program
{
    private const string Alph = "abcdefghijklmnopqrstuvwxyz012345";

    private static ulong Mix(ulong x)
    {
        ulong z = x;
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static int FillKey(char[] buf, int pos, int idx, char tag, ref ulong st, ulong seed)
    {
        st += 0x9e3779b97f4a7c15UL;
        ulong z = Mix(st);
        int length = 8 + (int)(z % 17UL);
        ulong u = ((ulong)idx * 0x9e3779b97f4a7c15UL) ^ (seed * 0x517cc1b727220a95UL);
        for (int j = 0; j < 7; j++)
        {
            buf[pos + j] = Alph[(int)((u >> (5 * j)) & 31UL)];
        }
        buf[pos + 7] = tag;
        ulong w = z >> 8;
        for (int j = 8; j < length; j++)
        {
            if (j == 16)
            {
                st += 0x9e3779b97f4a7c15UL;
                w = Mix(st);
            }
            buf[pos + j] = Alph[(int)(w & 31UL)];
            w >>= 5;
        }
        return pos + length;
    }

    private static ulong Round(string[] keys, string[] misses, ulong r)
    {
        var m = new Dictionary<string, ulong>();
        int n = keys.Length;
        for (int i = 0; i < n; i++)
        {
            m[keys[i]] = (ulong)i + r;
        }
        ulong sum = 0;
        for (int i = 0; i < n; i++)
        {
            string k = keys[i];
            if (m.TryGetValue(k, out ulong v))
            {
                sum += v;
            }
            if (m.ContainsKey(misses[i]))
            {
                sum += 1UL;
            }
            if ((i & 1) == 0)
            {
                m.Remove(k);
            }
        }
        for (int i = 0; i < n; i++)
        {
            if (m.ContainsKey(keys[i]))
            {
                sum += 3UL;
            }
        }
        return sum + (ulong)m.Count;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: hashmap-str <n> <rounds> <seed>");
            return 2;
        }
        int n = int.Parse(args[0]);
        int rounds = int.Parse(args[1]);
        ulong seed = ulong.Parse(args[2]);
        char[] buf = new char[2 * n * 24];
        string[] keys = new string[n];
        string[] misses = new string[n];
        ulong st = seed;
        int pos = 0;
        for (int i = 0; i < n; i++)
        {
            int lo = pos;
            pos = FillKey(buf, pos, i, 'k', ref st, seed);
            keys[i] = new string(buf, lo, pos - lo);
        }
        for (int i = 0; i < n; i++)
        {
            int lo = pos;
            pos = FillKey(buf, pos, i, 'm', ref st, seed);
            misses[i] = new string(buf, lo, pos - lo);
        }
        ulong total = 0;
        for (int r = 0; r < rounds; r++)
        {
            total += Round(keys, misses, (ulong)r);
        }
        Console.WriteLine(total);
        return 0;
    }
}
