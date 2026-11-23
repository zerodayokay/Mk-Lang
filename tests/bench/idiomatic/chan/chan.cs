// chan, C#: channel ping-pong with two bounded Channel<long> of capacity 1 and async tasks (README.md).
// Usage: chan <n>.
using System;
using System.Globalization;
using System.Threading.Channels;
using System.Threading.Tasks;

internal static class Program
{
    private static async Task<int> Main(string[] args)
    {
        if (args.Length != 1)
        {
            Console.Error.WriteLine("usage: chan <n>");
            return 2;
        }
        long n = long.Parse(args[0], CultureInfo.InvariantCulture);
        var opts = new BoundedChannelOptions(1)
        {
            SingleReader = true,
            SingleWriter = true,
        };
        Channel<long> a = Channel.CreateBounded<long>(opts);
        Channel<long> b = Channel.CreateBounded<long>(opts);

        Task pong = Task.Run(async () =>
        {
            ChannelReader<long> ra = a.Reader;
            ChannelWriter<long> wb = b.Writer;
            for (long i = 0; i < n; i++)
            {
                long v = await ra.ReadAsync().ConfigureAwait(false);
                await wb.WriteAsync(v).ConfigureAwait(false);
            }
        });

        ChannelWriter<long> wa = a.Writer;
        ChannelReader<long> rb = b.Reader;
        long sum = 0;
        for (long i = 0; i < n; i++)
        {
            await wa.WriteAsync(i).ConfigureAwait(false);
            sum += await rb.ReadAsync().ConfigureAwait(false);
        }
        await pong.ConfigureAwait(false);
        Console.WriteLine(sum);
        return 0;
    }
}
