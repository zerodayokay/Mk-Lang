// hello, C#: process start, print one line, exit (tests/bench/README.md).
// Used by cs (RyuJIT, JIT start-up included) and cs-aot (NativeAOT, no JIT start-up).
using System;

internal static class Program
{
    private static void Main()
    {
        Console.WriteLine("1");
    }
}
