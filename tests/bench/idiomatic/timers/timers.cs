// timers, C#: sleeping tasks with Task.Delay and deadline scopes with CancellationTokenSource (README.md).
// Reads Environment.TickCount64 (the monotonic clock backing .NET's TimerQueue) around Task.Delay.
// Usage: timers sleep <n> <rounds> <seed> | timers deadline <n>.
using System;
using System.Globalization;
using System.Threading;
using System.Threading.Tasks;

internal static class Program
{
    private sealed class Counter
    {
        public long Sum;
    }

    private static ulong Mix(ulong z)
    {
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static async Task SleepOne(long idx, int ms, Counter counter)
    {
        long t0 = Environment.TickCount64;
        await Task.Delay(ms).ConfigureAwait(false);
        if ((Environment.TickCount64 - t0) * 2 >= ms)
        {
            Interlocked.Add(ref counter.Sum, idx);
        }
    }

    private static async Task<long> Sleeps(int n, int rounds, ulong seed)
    {
        ulong st = seed;
        long total = 0;
        Task[] tasks = new Task[n];
        for (int r = 0; r < rounds; r++)
        {
            var counter = new Counter();
            for (int i = 0; i < n; i++)
            {
                st += 0x9e3779b97f4a7c15UL;
                int ms = (int)(Mix(st) % 50UL) + 1;
                tasks[i] = SleepOne(i, ms, counter);
            }
            await Task.WhenAll(tasks).ConfigureAwait(false);
            total += Volatile.Read(ref counter.Sum);
        }
        return total;
    }

    private static long Deadlines(long n)
    {
        long disarmed = 0;
        long fired = 0;
        TimeSpan longTimeout = TimeSpan.FromSeconds(60);
        for (long i = 0; i < n; i++)
        {
            TimeSpan d = (i % 1000 == 999) ? TimeSpan.Zero : longTimeout;
            using var cts = new CancellationTokenSource(d);
            if (cts.IsCancellationRequested)
            {
                fired++;
            }
            else
            {
                disarmed++;
            }
        }
        return disarmed + 3 * fired;
    }

    private static async Task<int> Main(string[] args)
    {
        if (args.Length < 2)
        {
            Console.Error.WriteLine("usage: timers sleep <n> <rounds> <seed> | timers deadline <n>");
            return 2;
        }
        long total;
        if (args[0] == "sleep")
        {
            int n = int.Parse(args[1], CultureInfo.InvariantCulture);
            int rounds = int.Parse(args[2], CultureInfo.InvariantCulture);
            ulong seed = ulong.Parse(args[3], CultureInfo.InvariantCulture);
            total = await Sleeps(n, rounds, seed).ConfigureAwait(false);
        }
        else
        {
            long n = long.Parse(args[1], CultureInfo.InvariantCulture);
            total = Deadlines(n);
        }
        Console.WriteLine(total);
        return 0;
    }
}
