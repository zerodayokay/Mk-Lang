// spawn, C#: start n lightweight tasks via Task.Run per round, each adding its index to an atomic counter, and join (README.md).
// Usage: spawn <n> <rounds>.
using System;
using System.Globalization;
using System.Threading;
using System.Threading.Tasks;

internal static class Program
{
    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: spawn <n> <rounds>");
            return 2;
        }
        long n = long.Parse(args[0], CultureInfo.InvariantCulture);
        int rounds = int.Parse(args[1], CultureInfo.InvariantCulture);

        long total = 0;
        for (int r = 0; r < rounds; r++)
        {
            if (n == 0)
            {
                continue;
            }
            long sum = 0;
            long remaining = n;
            using var done = new ManualResetEventSlim(false);
            for (long i = 0; i < n; i++)
            {
                long idx = i;
                Task.Run(() =>
                {
                    Interlocked.Add(ref sum, idx);
                    if (Interlocked.Decrement(ref remaining) == 0)
                    {
                        done.Set();
                    }
                });
            }
            done.Wait();
            total += Volatile.Read(ref sum);
        }
        Console.WriteLine(total);
        return 0;
    }
}
