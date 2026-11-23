// mutex, C#: t OS threads each locking one shared System.Threading.Lock n times and incrementing count (README.md).
// Usage: mutex <n> <t>.
using System;
using System.Globalization;
using System.Threading;

internal static class Program
{
    private static int Main(string[] args)
    {
        if (args.Length != 2)
        {
            Console.Error.WriteLine("usage: mutex <n> <t>");
            return 2;
        }
        int n = int.Parse(args[0], CultureInfo.InvariantCulture);
        int t = int.Parse(args[1], CultureInfo.InvariantCulture);

        var gate = new Lock();
        long count = 0;
        Thread[] threads = new Thread[t];
        for (int i = 0; i < t; i++)
        {
            threads[i] = new Thread(() =>
            {
                for (int j = 0; j < n; j++)
                {
                    lock (gate)
                    {
                        count++;
                    }
                }
            });
            threads[i].Start();
        }
        for (int i = 0; i < t; i++)
        {
            threads[i].Join();
        }
        Console.WriteLine(count);
        return 0;
    }
}
