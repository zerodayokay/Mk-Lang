// mandelbrot, C#: the reference algorithm of README.md. Scalar double, one
// point at a time, at most 50 iterations, the escape test after each step, the
// README's parenthesisation. long counters. Bounds-checked, wrapping overflow
// (class bounds). No unsafe, no stackalloc, no MethodImpl, no Unsafe.*.
using System;

internal static class Program
{
    private const long MaxIter = 50;

    private static int Main(string[] args)
    {
        if (args.Length != 1)
        {
            Console.Error.WriteLine("usage: mandelbrot <n>");
            return 2;
        }
        long n = long.Parse(args[0]);
        double fn = (double)n;
        long total = 0;
        for (long y = 0; y < n; y++)
        {
            double ci = (2.0 * (double)y) / fn - 1.0;
            for (long x = 0; x < n; x++)
            {
                double cr = (2.0 * (double)x) / fn - 1.5;
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
                    i += 1;
                }
                total += i;
            }
        }
        Console.WriteLine(total);
        return 0;
    }
}
