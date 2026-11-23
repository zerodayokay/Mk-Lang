// jsonscan, C#: the reference algorithm of README.md. Reads the file once into
// one byte[] buffer, then scans rounds times with the README's exact if-chain
// order (no switch). Token hash is ulong (wrapping, overflow class "wraps").
// Bounds-checked (class bounds). No unsafe, no stackalloc, no MethodImpl, no
// Unsafe.*.
using System;
using System.IO;

internal static class Program
{
    private static ulong Scan(byte[] b, ulong h0)
    {
        ulong h = h0;
        int n = b.Length;
        int i = 0;
        while (i < n)
        {
            byte c = b[i];
            if (c == (byte)' ' || c == (byte)'\n' || c == (byte)'\r' || c == (byte)'\t')
            {
                i++;
            }
            else if (c == (byte)'"')
            {
                i++;
                long l = 0;
                while (i < n && b[i] != (byte)'"')
                {
                    if (b[i] == (byte)'\\')
                    {
                        i++;
                    }
                    i++;
                    l++;
                }
                i++;
                h = h * 31UL + (ulong)l;
            }
            else if (c == (byte)'-' || (c >= (byte)'0' && c <= (byte)'9'))
            {
                ulong neg = 0;
                if (c == (byte)'-')
                {
                    neg = 1;
                    i++;
                }
                ulong v = 0;
                while (i < n && ((b[i] >= (byte)'0' && b[i] <= (byte)'9') || b[i] == (byte)'.'))
                {
                    if (b[i] != (byte)'.')
                    {
                        v = v * 10UL + (ulong)(b[i] - (byte)'0');
                    }
                    i++;
                }
                h = h * 31UL + (v * 2UL + neg);
            }
            else if (c == (byte)'t')
            {
                h = h * 31UL + 1UL;
                i += 4;
            }
            else if (c == (byte)'f')
            {
                h = h * 31UL + 2UL;
                i += 5;
            }
            else if (c == (byte)'n')
            {
                h = h * 31UL + 3UL;
                i += 4;
            }
            else
            {
                h = h * 31UL + c;
                i++;
            }
        }
        return h;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: jsonscan <file> <rounds>");
            return 2;
        }
        byte[] b = File.ReadAllBytes(args[0]);
        long rounds = long.Parse(args[1]);
        ulong total = 0;
        for (long r = 0; r < rounds; r++)
        {
            total = Scan(b, total);
        }
        Console.WriteLine(total);
        return 0;
    }
}
