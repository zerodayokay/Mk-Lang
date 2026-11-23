// binarytrees, C#: the reference algorithm of README.md (the Benchmarks Game's
// binary-trees, with the row's bump allocator). Nodes are struct Node { uint Left,
// Right; } in one array, allocated by a uint bump index in a struct Pool passed
// by ref (no static state); index 0 is the null node. Bounds-checked, wrapping
// overflow (class bounds). No unsafe, no stackalloc, no MethodImpl, no Unsafe.*.
using System;

internal struct Node
{
    public uint Left;
    public uint Right;
}

internal struct Pool
{
    public Node[] Nodes;
    public uint Top;
}

internal static class Program
{
    private static uint Build(ref Pool p, long depth)
    {
        uint idx = p.Top;
        p.Top += 1;
        if (depth > 0)
        {
            uint l = Build(ref p, depth - 1);
            uint r = Build(ref p, depth - 1);
            p.Nodes[(int)idx] = new Node { Left = l, Right = r };
        }
        else
        {
            p.Nodes[(int)idx] = default;
        }
        return idx;
    }

    private static long Check(Node[] nodes, uint idx)
    {
        Node n = nodes[(int)idx];
        if (n.Left == 0)
        {
            return 1;
        }
        return 1 + Check(nodes, n.Left) + Check(nodes, n.Right);
    }

    private static int Main(string[] args)
    {
        if (args.Length != 1)
        {
            Console.Error.WriteLine("usage: binarytrees <n>");
            return 2;
        }
        long n = long.Parse(args[0]);
        const long minDepth = 4;
        long maxDepth = n;
        if (minDepth + 2 > maxDepth)
        {
            maxDepth = minDepth + 2;
        }
        int cap = (1 << ((int)maxDepth + 2)) + 1;
        Pool p = new Pool { Nodes = new Node[cap], Top = 1 };
        uint t = Build(ref p, maxDepth + 1);
        long total = Check(p.Nodes, t);
        p.Top = 1;
        uint longLived = Build(ref p, maxDepth);
        uint mark = p.Top;
        for (long d = minDepth; d <= maxDepth; d += 2)
        {
            long iterations = 1L << (int)(maxDepth - d + minDepth);
            for (long i = 0; i < iterations; i++)
            {
                p.Top = mark;
                t = Build(ref p, d);
                total += Check(p.Nodes, t);
            }
        }
        total += Check(p.Nodes, longLived);
        Console.WriteLine(total);
        return 0;
    }
}
