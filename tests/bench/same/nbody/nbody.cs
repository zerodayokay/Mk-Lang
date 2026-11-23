// nbody, C#: the reference algorithm of README.md. Five bodies in seven
// fixed-size [InlineArray(5)] double arrays of one struct (structure of
// arrays), every operation in the README's order and parenthesisation;
// Math.Sqrt (one sqrtsd). Bounds-checked, wrapping overflow (class bounds).
// No unsafe, no stackalloc, no MethodImpl, no Unsafe.*.
using System;
using System.Runtime.CompilerServices;

[InlineArray(5)]
internal struct Vec5
{
    private double _element0;
}

internal struct State
{
    public Vec5 X;
    public Vec5 Y;
    public Vec5 Z;
    public Vec5 Vx;
    public Vec5 Vy;
    public Vec5 Vz;
    public Vec5 M;
}

internal static class Program
{
    private const int Bodies = 5;
    private const double Dt = 0.0078125;

    private static double Energy(ref State s)
    {
        double e = 0.0;
        for (int i = 0; i < Bodies; i++)
        {
            e = e + (0.5 * s.M[i]) * ((s.Vx[i] * s.Vx[i] + s.Vy[i] * s.Vy[i]) + s.Vz[i] * s.Vz[i]);
            for (int j = i + 1; j < Bodies; j++)
            {
                double dx = s.X[i] - s.X[j];
                double dy = s.Y[i] - s.Y[j];
                double dz = s.Z[i] - s.Z[j];
                e = e - (s.M[i] * s.M[j]) / Math.Sqrt((dx * dx + dy * dy) + dz * dz);
            }
        }
        return e;
    }

    private static void Advance(ref State s, long steps)
    {
        for (long k = 0; k < steps; k++)
        {
            for (int i = 0; i < Bodies; i++)
            {
                for (int j = i + 1; j < Bodies; j++)
                {
                    double dx = s.X[i] - s.X[j];
                    double dy = s.Y[i] - s.Y[j];
                    double dz = s.Z[i] - s.Z[j];
                    double d2 = (dx * dx + dy * dy) + dz * dz;
                    double mag = Dt / (d2 * Math.Sqrt(d2));
                    s.Vx[i] = s.Vx[i] - (dx * s.M[j]) * mag;
                    s.Vy[i] = s.Vy[i] - (dy * s.M[j]) * mag;
                    s.Vz[i] = s.Vz[i] - (dz * s.M[j]) * mag;
                    s.Vx[j] = s.Vx[j] + (dx * s.M[i]) * mag;
                    s.Vy[j] = s.Vy[j] + (dy * s.M[i]) * mag;
                    s.Vz[j] = s.Vz[j] + (dz * s.M[i]) * mag;
                }
            }
            for (int i = 0; i < Bodies; i++)
            {
                s.X[i] = s.X[i] + Dt * s.Vx[i];
                s.Y[i] = s.Y[i] + Dt * s.Vy[i];
                s.Z[i] = s.Z[i] + Dt * s.Vz[i];
            }
        }
    }

    private static Vec5 Make5(double a, double b, double c, double d, double e)
    {
        Vec5 v = default;
        v[0] = a;
        v[1] = b;
        v[2] = c;
        v[3] = d;
        v[4] = e;
        return v;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 1)
        {
            Console.Error.WriteLine("usage: nbody <steps>");
            return 2;
        }
        long steps = long.Parse(args[0]);
        State s = new State
        {
            X = Make5(0.0, 4.0, -2.0, 6.0, -9.0),
            Y = Make5(0.0, 0.0, 0.0, 0.0, 0.0),
            Z = Make5(0.0, 0.25, -0.125, 0.5, 0.0),
            Vx = Make5(0.0, 0.0, 0.0, 0.0, 0.0),
            Vy = Make5(0.0, 1.375, -2.0, 1.125, -0.9375),
            Vz = Make5(0.0, 0.0, 0.0625, 0.0, 0.03125),
            M = Make5(8.0, 0.03125, 0.015625, 0.0625, 0.0078125),
        };
        Advance(ref s, steps);
        Console.WriteLine((long)(Energy(ref s) * 1e9));
        return 0;
    }
}
