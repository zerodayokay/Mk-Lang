// sortints, C#: the reference algorithm of README.md. Go 1.27's pdqsortOrdered
// written out for long and <, with the same functions, constants, loop
// structure and counting build as every other column. Bounds-checked, wrapping
// overflow (class bounds). No unsafe, no stackalloc, no MethodImpl, no
// Unsafe.*.
using System;

internal static class Program
{
    // tools/bench flips this line in a copy for the counting build.
    private const bool BenchCount = false;

    private static ulong nPdq, nIns, nShift, nHeap, nRev, nPart, nPartEq, nPis, nBrk;

    private static ulong Mix(ulong z)
    {
        z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ (z >> 27)) * 0x94d049bb133111ebUL;
        return z ^ (z >> 31);
    }

    private static int BitLen(int n)
    {
        int l = 0;
        for (uint x = (uint)n; x > 0; x >>= 1)
        {
            l++;
        }
        return l;
    }

    private static void InsertionSort(long[] s, int a, int b)
    {
        if (BenchCount)
        {
            nIns += 1;
        }
        for (int i = a + 1; i < b; i++)
        {
            for (int j = i; j > a && s[j] < s[j - 1]; j--)
            {
                long t = s[j];
                s[j] = s[j - 1];
                s[j - 1] = t;
                if (BenchCount)
                {
                    nShift += 1;
                }
            }
        }
    }

    private static void SiftDown(long[] s, int lo, int hi, int first)
    {
        int root = lo;
        for (;;)
        {
            int child = 2 * root + 1;
            if (child >= hi)
            {
                return;
            }
            if (child + 1 < hi && s[first + child] < s[first + child + 1])
            {
                child++;
            }
            if (!(s[first + root] < s[first + child]))
            {
                return;
            }
            long t = s[first + root];
            s[first + root] = s[first + child];
            s[first + child] = t;
            root = child;
        }
    }

    private static void HeapSort(long[] s, int a, int b)
    {
        if (BenchCount)
        {
            nHeap += 1;
        }
        int first = a;
        int hi = b - a;
        for (int i = (hi - 1) / 2; i >= 0; i--)
        {
            SiftDown(s, i, hi, first);
        }
        for (int i = hi - 1; i >= 0; i--)
        {
            long t = s[first];
            s[first] = s[first + i];
            s[first + i] = t;
            SiftDown(s, 0, i, first);
        }
    }

    private static int Median(long[] s, int a, int b, int c, ref int swaps)
    {
        int x = a, y = b, z = c;
        if (s[y] < s[x])
        {
            swaps += 1;
            int t = x;
            x = y;
            y = t;
        }
        if (s[z] < s[y])
        {
            swaps += 1;
            int t = y;
            y = z;
            z = t;
        }
        if (s[y] < s[x])
        {
            swaps += 1;
            int t = x;
            x = y;
            y = t;
        }
        return y;
    }

    private static int ChoosePivot(long[] s, int a, int b, out int hint)
    {
        int l = b - a;
        int swaps = 0;
        int i = a + l / 4 * 1;
        int j = a + l / 4 * 2;
        int k = a + l / 4 * 3;
        if (l >= 8)
        {
            if (l >= 50)
            {
                i = Median(s, i - 1, i, i + 1, ref swaps);
                j = Median(s, j - 1, j, j + 1, ref swaps);
                k = Median(s, k - 1, k, k + 1, ref swaps);
            }
            j = Median(s, i, j, k, ref swaps);
        }
        if (swaps == 0)
        {
            hint = 1;
        }
        else if (swaps == 12)
        {
            hint = 2;
        }
        else
        {
            hint = 0;
        }
        return j;
    }

    private static void ReverseRange(long[] s, int a, int b)
    {
        if (BenchCount)
        {
            nRev += 1;
        }
        int i = a, j = b - 1;
        while (i < j)
        {
            long t = s[i];
            s[i] = s[j];
            s[j] = t;
            i++;
            j--;
        }
    }

    private static bool PartialInsertionSort(long[] s, int a, int b)
    {
        if (BenchCount)
        {
            nPis += 1;
        }
        int i = a + 1;
        for (int step = 0; step < 5; step++)
        {
            while (i < b && !(s[i] < s[i - 1]))
            {
                i++;
            }
            if (i == b)
            {
                return true;
            }
            if (b - a < 50)
            {
                return false;
            }
            long t = s[i];
            s[i] = s[i - 1];
            s[i - 1] = t;
            if (i - a >= 2)
            {
                for (int j = i - 1; j >= 1; j--)
                {
                    if (!(s[j] < s[j - 1]))
                    {
                        break;
                    }
                    long u = s[j];
                    s[j] = s[j - 1];
                    s[j - 1] = u;
                }
            }
            if (b - i >= 2)
            {
                for (int j = i + 1; j < b; j++)
                {
                    if (!(s[j] < s[j - 1]))
                    {
                        break;
                    }
                    long u = s[j];
                    s[j] = s[j - 1];
                    s[j - 1] = u;
                }
            }
        }
        return false;
    }

    private static void BreakPatterns(long[] s, int a, int b)
    {
        if (BenchCount)
        {
            nBrk += 1;
        }
        int length = b - a;
        if (length < 8)
        {
            return;
        }
        ulong r = (ulong)length;
        ulong modulus = 1UL << BitLen(length);
        int idx = a + (length / 4) * 2 - 1;
        for (int i = 0; i < 3; i++)
        {
            r ^= r << 13;
            r ^= r >> 7;
            r ^= r << 17;
            int other = (int)(r & (modulus - 1));
            if (other >= length)
            {
                other -= length;
            }
            long t = s[idx + i];
            s[idx + i] = s[a + other];
            s[a + other] = t;
        }
    }

    private static int Partition(long[] s, int a, int b, int pivot, out bool already)
    {
        if (BenchCount)
        {
            nPart += 1;
        }
        long t = s[a];
        s[a] = s[pivot];
        s[pivot] = t;
        int i = a + 1, j = b - 1;
        while (i <= j && s[i] < s[a])
        {
            i++;
        }
        while (i <= j && !(s[j] < s[a]))
        {
            j--;
        }
        if (i > j)
        {
            t = s[j];
            s[j] = s[a];
            s[a] = t;
            already = true;
            return j;
        }
        t = s[i];
        s[i] = s[j];
        s[j] = t;
        i++;
        j--;
        for (;;)
        {
            while (i <= j && s[i] < s[a])
            {
                i++;
            }
            while (i <= j && !(s[j] < s[a]))
            {
                j--;
            }
            if (i > j)
            {
                break;
            }
            t = s[i];
            s[i] = s[j];
            s[j] = t;
            i++;
            j--;
        }
        t = s[j];
        s[j] = s[a];
        s[a] = t;
        already = false;
        return j;
    }

    private static int PartitionEqual(long[] s, int a, int b, int pivot)
    {
        if (BenchCount)
        {
            nPartEq += 1;
        }
        long t = s[a];
        s[a] = s[pivot];
        s[pivot] = t;
        int i = a + 1, j = b - 1;
        for (;;)
        {
            while (i <= j && !(s[a] < s[i]))
            {
                i++;
            }
            while (i <= j && s[a] < s[j])
            {
                j--;
            }
            if (i > j)
            {
                break;
            }
            t = s[i];
            s[i] = s[j];
            s[j] = t;
            i++;
            j--;
        }
        return i;
    }

    private static void PdqSort(long[] s, int a, int b, int limit)
    {
        bool balanced = true;
        bool partitioned = true;
        for (;;)
        {
            int length = b - a;
            if (BenchCount)
            {
                nPdq += 1;
            }
            if (length <= 12)
            {
                InsertionSort(s, a, b);
                return;
            }
            if (limit == 0)
            {
                HeapSort(s, a, b);
                return;
            }
            if (!balanced)
            {
                BreakPatterns(s, a, b);
                limit--;
            }
            int pivot = ChoosePivot(s, a, b, out int hint);
            if (hint == 2)
            {
                ReverseRange(s, a, b);
                pivot = (b - 1) - (pivot - a);
                hint = 1;
            }
            if (balanced && partitioned && hint == 1)
            {
                if (PartialInsertionSort(s, a, b))
                {
                    return;
                }
            }
            if (a > 0 && !(s[a - 1] < s[pivot]))
            {
                a = PartitionEqual(s, a, b, pivot);
                continue;
            }
            int mid = Partition(s, a, b, pivot, out bool already);
            partitioned = already;
            int left = mid - a, right = b - mid;
            int threshold = length / 8;
            if (left < right)
            {
                balanced = left >= threshold;
                PdqSort(s, a, mid, limit);
                a = mid + 1;
            }
            else
            {
                balanced = right >= threshold;
                PdqSort(s, mid + 1, b, limit);
                b = mid;
            }
        }
    }

    private static long Check(long[] s)
    {
        long sum = 0;
        for (int i = 0; i < s.Length; i++)
        {
            if (i > 0 && s[i - 1] > s[i])
            {
                return -1;
            }
            sum += s[i] * (i % 1000);
        }
        return sum;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 3)
        {
            Console.Error.WriteLine("usage: sortints <n> <rounds> <seed>");
            return 2;
        }
        int n = int.Parse(args[0]);
        long rounds = long.Parse(args[1]);
        ulong st = ulong.Parse(args[2]);
        long[] xs = new long[n];
        long total = 0;
        for (long r = 0; r < rounds; r++)
        {
            for (int p = 0; p < 4; p++)
            {
                for (int i = 0; i < n; i++)
                {
                    if (p == 0)
                    {
                        st += 0x9e3779b97f4a7c15UL;
                        xs[i] = (long)(Mix(st) & 0xFFFFFUL);
                    }
                    else if (p == 1)
                    {
                        xs[i] = n - i;
                    }
                    else if (p == 2)
                    {
                        xs[i] = i % 1000;
                    }
                    else
                    {
                        st += 0x9e3779b97f4a7c15UL;
                        xs[i] = (long)(Mix(st) >> 60);
                    }
                }
                PdqSort(xs, 0, n, BitLen(n));
                total += Check(xs);
            }
        }
        Console.WriteLine(total);
        if (BenchCount)
        {
            Console.WriteLine($"counts pdq={nPdq} ins={nIns} shift={nShift} heap={nHeap} rev={nRev} part={nPart} parteq={nPartEq} pis={nPis} brk={nBrk}");
        }
        return 0;
    }
}
