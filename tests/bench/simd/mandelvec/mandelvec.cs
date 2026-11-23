// mandelvec, C#: Mandelbrot iteration count with System.Numerics.Vector<double> (README.md).
// No unsafe, no MethodImpl, no FMA contraction: Vector<double>.Count is fixed by the CPU target
// (2 at x86-64-v2, 4 at x86-64-v3). Usage: mandelvec <n>.
using System;
using System.Globalization;
using System.Numerics;

internal static class Program
{
    private const int MaxIter = 50;

    private static long Point(double cr, double ci)
    {
        double zr = 0.0;
        double zi = 0.0;
        long i = 0;
        while (i < MaxIter)
        {
            double tr = (zr * zr - zi * zi) + cr;
            double ti = (2.0 * zr) * zi + ci;
            zr = tr;
            zi = ti;
            if (zr * zr + zi * zi > 4.0)
            {
                break;
            }
            i++;
        }
        return i;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 1)
        {
            Console.Error.WriteLine("usage: mandelvec <n>");
            return 2;
        }
        long n = long.Parse(args[0], CultureInfo.InvariantCulture);
        double fn = (double)n;
        long total = 0;
        int lanes = Vector<double>.Count;
        Vector<double> offsets = Vector.CreateSequence(0.0, 1.0);
        Vector<double> vfn = Vector.Create(fn);
        Vector<double> two = Vector.Create(2.0);
        Vector<double> four = Vector.Create(4.0);
        Vector<double> one = Vector.Create(1.0);
        Vector<double> halfThree = Vector.Create(1.5);

        for (long y = 0; y < n; y++)
        {
            double ci = (2.0 * (double)y) / fn - 1.0;
            Vector<double> civ = Vector.Create(ci);
            long x = 0;
            for (; x + lanes <= n; x += lanes)
            {
                Vector<double> cr = (two * (Vector.Create((double)x) + offsets)) / vfn - halfThree;
                Vector<double> zr = Vector<double>.Zero;
                Vector<double> zi = Vector<double>.Zero;
                Vector<double> cnt = Vector<double>.Zero;
                Vector<long> active = Vector<long>.AllBitsSet;
                for (int i = 0; i < MaxIter; i++)
                {
                    Vector<double> tr = (zr * zr - zi * zi) + cr;
                    Vector<double> ti = (two * zr) * zi + civ;
                    zr = tr;
                    zi = ti;
                    Vector<long> esc = Vector.GreaterThan(zr * zr + zi * zi, four);
                    active = Vector.AndNot(active, esc);
                    if (Vector.NoneWhereAllBitsSet(active))
                    {
                        break;
                    }
                    cnt += Vector.BitwiseAnd(Vector.AsVectorDouble(active), one);
                }
                total += (long)Vector.Sum(cnt);
            }
            for (; x < n; x++)
            {
                total += Point((2.0 * (double)x) / fn - 1.5, ci);
            }
        }
        Console.WriteLine(total);
        return 0;
    }
}
