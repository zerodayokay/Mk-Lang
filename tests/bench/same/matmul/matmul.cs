// matmul, C#: the reference algorithm of README.md. Three flat double[] arrays
// of n * n, row-major, the i-k-j loop, c[i*n+j] = c[i*n+j] + aik * b[k*n+j],
// repeated rounds times into the same c. Bounds-checked, wrapping overflow
// (class bounds). No unsafe, no stackalloc, no MethodImpl, no Unsafe.*.
using System;

internal static class Program
{
    private static ulong Mix(ulong z)
    {
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: matmul <n> <rounds> <seed>");
            return 2;
        }
        int n = int.Parse(args[0]);
        long rounds = long.Parse(args[1]);
        ulong st = ulong.Parse(args[2]);
        int nn = n * n;
        double[] a = new double[nn];
        double[] b = new double[nn];
        double[] c = new double[nn];
        for (int i = 0; i < nn; i++)
        {
            st += 0x9e3779b97f4a7c15UL;
            a[i] = (double)(Mix(st) & 255UL) / 256.0;
            st += 0x9e3779b97f4a7c15UL;
            b[i] = (double)(Mix(st) & 255UL) / 256.0;
        }
        for (long r = 0; r < rounds; r++)
        {
            for (int i = 0; i < n; i++)
            {
                for (int k = 0; k < n; k++)
                {
                    double aik = a[i * n + k];
                    for (int j = 0; j < n; j++)
                    {
                        c[i * n + j] = c[i * n + j] + aik * b[k * n + j];
                    }
                }
            }
        }
        double total = 0.0;
        for (int i = 0; i < nn; i++)
        {
            total = total + c[i];
        }
        Console.WriteLine((long)(total * 65536.0));
        return 0;
    }
}
