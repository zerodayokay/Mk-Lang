// spectralnorm, C#: the reference algorithm of README.md (the Benchmarks Game's
// spectral-norm, scalar and single-threaded). A(i, j) computed from long on
// every use, double[] vectors, Math.Sqrt. Bounds-checked, wrapping overflow
// (class bounds). No unsafe, no stackalloc, no MethodImpl, no Unsafe.*.
using System;

internal static class Program
{
    private static double A(long i, long j)
    {
        return 1.0 / (double)((i + j) * (i + j + 1) / 2 + i + 1);
    }

    private static void MulAv(double[] v, double[] dst)
    {
        int n = v.Length;
        for (int i = 0; i < n; i++)
        {
            double s = 0.0;
            for (int j = 0; j < n; j++)
            {
                s = s + A(i, j) * v[j];
            }
            dst[i] = s;
        }
    }

    private static void MulAtv(double[] v, double[] dst)
    {
        int n = v.Length;
        for (int i = 0; i < n; i++)
        {
            double s = 0.0;
            for (int j = 0; j < n; j++)
            {
                s = s + A(j, i) * v[j];
            }
            dst[i] = s;
        }
    }

    private static void MulAtAv(double[] v, double[] dst, double[] tmp)
    {
        MulAv(v, tmp);
        MulAtv(tmp, dst);
    }

    private static int Main(string[] args)
    {
        if (args.Length != 1)
        {
            Console.Error.WriteLine("usage: spectralnorm <n>");
            return 2;
        }
        int n = int.Parse(args[0]);
        double[] u = new double[n];
        double[] v = new double[n];
        double[] tmp = new double[n];
        for (int i = 0; i < n; i++)
        {
            u[i] = 1.0;
        }
        for (int k = 0; k < 10; k++)
        {
            MulAtAv(u, v, tmp);
            MulAtAv(v, u, tmp);
        }
        double vbv = 0.0;
        double vv = 0.0;
        for (int i = 0; i < n; i++)
        {
            vbv = vbv + u[i] * v[i];
            vv = vv + v[i] * v[i];
        }
        Console.WriteLine((long)(Math.Sqrt(vbv / vv) * 1e9));
        return 0;
    }
}
