// vm, C#, std: the task of README.md, a 23-opcode register-based bytecode
// virtual machine running four deterministic bytecode programs (recursive
// fibonacci, iterative prime sieve, arithmetic/checksum loop, and Shell sort
// plus weighted dot product) over a flat register file, call-frame stack and
// bounds-checked array heap. Pure safe C# (no unsafe, no MethodImpl, no
// Unsafe.*, no System.Runtime.Intrinsics, no Vector<T>). All arithmetic uses
// long (int64) without overflow. Bounds-checked (class bounds).
// Usage: vm <fib_n> <sieve_n> <loop_n> <sort_n> <rounds>.
using System;

internal static class Program
{
    private const uint OpHalt = 0;
    private const uint OpLoadI = 1;
    private const uint OpLoadK = 2;
    private const uint OpMov = 3;
    private const uint OpAdd = 4;
    private const uint OpSub = 5;
    private const uint OpMul = 6;
    private const uint OpDiv = 7;
    private const uint OpMod = 8;
    private const uint OpAddI = 9;
    private const uint OpBand = 10;
    private const uint OpBxor = 11;
    private const uint OpEq = 12;
    private const uint OpLt = 13;
    private const uint OpLe = 14;
    private const uint OpJmp = 15;
    private const uint OpJz = 16;
    private const uint OpJnz = 17;
    private const uint OpCall = 18;
    private const uint OpRet = 19;
    private const uint OpArrAlloc = 20;
    private const uint OpArrGet = 21;
    private const uint OpArrSet = 22;

    private const long ModSum = 1_000_000_007L;

    private struct Frame
    {
        public int RetPc;
        public int CallerBase;
        public int RetReg;
    }

    private struct HeapObj
    {
        public int Off;
        public int Len;
    }

    private sealed class Bytecode
    {
        public readonly uint[] Code;
        public readonly long[] Consts;
        public readonly int[] Funcs;

        public Bytecode(uint[] code, long[] consts, int[] funcs)
        {
            Code = code;
            Consts = consts;
            Funcs = funcs;
        }
    }

    private sealed class VmState
    {
        public readonly long[] Regs = new long[65536];
        public readonly Frame[] Frames = new Frame[4096];
        public long[] HeapData = new long[1024];
        public int HeapLen;
        public HeapObj[] HeapObjs = new HeapObj[16];
        public int ObjsLen;
    }

    private static uint EncABC(uint op, uint a, uint b, uint c) =>
        (op & 255u) | ((a & 255u) << 8) | ((b & 255u) << 16) | ((c & 255u) << 24);

    private static uint EncAU(uint op, uint a, uint u) =>
        (op & 255u) | ((a & 255u) << 8) | ((u & 65535u) << 16);

    private static uint EncAS(uint op, uint a, short s) =>
        (op & 255u) | ((a & 255u) << 8) | ((uint)(ushort)s << 16);

    private static long SignExt16(uint insn) => (short)(ushort)(insn >> 16);

    private static Bytecode BuildFib() => new Bytecode(
        new uint[]
        {
            EncAS(OpLoadI, 1, 1),
            EncABC(OpLe, 1, 0, 1),
            EncAU(OpJz, 1, 4),
            EncABC(OpRet, 0, 0, 0),
            EncABC(OpMov, 4, 0, 0),
            EncAS(OpAddI, 4, -1),
            EncABC(OpCall, 2, 4, 0),
            EncABC(OpMov, 4, 0, 0),
            EncAS(OpAddI, 4, -2),
            EncABC(OpCall, 3, 4, 0),
            EncABC(OpAdd, 0, 2, 3),
            EncABC(OpRet, 0, 0, 0),
        },
        Array.Empty<long>(),
        new int[] { 0 }
    );

    private static Bytecode BuildSieve() => new Bytecode(
        new uint[]
        {
            EncABC(OpMov, 1, 0, 0),
            EncAS(OpAddI, 1, 1),
            EncABC(OpArrAlloc, 2, 1, 0),
            EncAS(OpLoadI, 5, 1),
            EncAS(OpLoadI, 3, 2),
            EncABC(OpMul, 6, 3, 3),
            EncABC(OpLe, 6, 6, 0),
            EncAU(OpJz, 6, 18),
            EncABC(OpArrGet, 6, 2, 3),
            EncAU(OpJnz, 6, 16),
            EncABC(OpMul, 4, 3, 3),
            EncABC(OpLe, 6, 4, 0),
            EncAU(OpJz, 6, 16),
            EncABC(OpArrSet, 2, 4, 5),
            EncABC(OpAdd, 4, 4, 3),
            EncAU(OpJmp, 0, 11),
            EncAS(OpAddI, 3, 1),
            EncAU(OpJmp, 0, 5),
            EncAS(OpLoadI, 7, 0),
            EncAS(OpLoadI, 8, 0),
            EncAS(OpLoadI, 3, 2),
            EncABC(OpLe, 6, 3, 0),
            EncAU(OpJz, 6, 29),
            EncABC(OpArrGet, 6, 2, 3),
            EncAU(OpJnz, 6, 27),
            EncAS(OpAddI, 7, 1),
            EncABC(OpAdd, 8, 8, 3),
            EncAS(OpAddI, 3, 1),
            EncAU(OpJmp, 0, 21),
            EncAU(OpLoadK, 9, 0),
            EncABC(OpMod, 8, 8, 9),
            EncABC(OpMul, 7, 7, 9),
            EncABC(OpAdd, 0, 7, 8),
            EncABC(OpHalt, 0, 0, 0),
        },
        new long[] { 1_000_000L },
        Array.Empty<int>()
    );

    private static Bytecode BuildArith() => new Bytecode(
        new uint[]
        {
            EncAS(OpLoadI, 2, 0),
            EncAS(OpLoadI, 3, 0),
            EncAU(OpLoadK, 4, 0),
            EncAU(OpLoadK, 5, 1),
            EncAU(OpLoadK, 6, 2),
            EncAU(OpLoadK, 7, 3),
            EncAU(OpLoadK, 8, 4),
            EncABC(OpLt, 9, 3, 0),
            EncAU(OpJz, 9, 21),
            EncABC(OpMul, 1, 1, 5),
            EncABC(OpAdd, 1, 1, 6),
            EncABC(OpBand, 1, 1, 7),
            EncABC(OpDiv, 9, 1, 8),
            EncABC(OpMod, 10, 1, 8),
            EncABC(OpSub, 11, 1, 10),
            EncABC(OpBxor, 11, 11, 9),
            EncABC(OpEq, 9, 10, 8),
            EncABC(OpAdd, 2, 2, 11),
            EncABC(OpMod, 2, 2, 4),
            EncAS(OpAddI, 3, 1),
            EncAU(OpJmp, 0, 7),
            EncABC(OpHalt, 2, 0, 0),
        },
        new long[] { 1_000_000_007L, 1103515245L, 12345L, 0x7fffffffL, 97L },
        Array.Empty<int>()
    );

    private static Bytecode BuildSortDot() => new Bytecode(
        new uint[]
        {
            EncABC(OpArrAlloc, 2, 0, 0),
            EncABC(OpArrAlloc, 3, 0, 0),
            EncAU(OpLoadK, 15, 0),
            EncAU(OpLoadK, 12, 1),
            EncAU(OpLoadK, 13, 2),
            EncAU(OpLoadK, 14, 3),
            EncAS(OpLoadI, 11, 2),
            EncAS(OpLoadI, 4, 0),
            EncABC(OpLt, 9, 4, 0),
            EncAU(OpJz, 9, 19),
            EncABC(OpMul, 1, 1, 13),
            EncAS(OpAddI, 1, 17),
            EncABC(OpMod, 1, 1, 12),
            EncABC(OpArrSet, 2, 4, 1),
            EncABC(OpMod, 10, 4, 14),
            EncAS(OpAddI, 10, 1),
            EncABC(OpArrSet, 3, 4, 10),
            EncAS(OpAddI, 4, 1),
            EncAU(OpJmp, 0, 8),
            EncABC(OpDiv, 6, 0, 11),
            EncAU(OpJz, 6, 40),
            EncABC(OpMov, 4, 6, 0),
            EncABC(OpLt, 9, 4, 0),
            EncAU(OpJz, 9, 38),
            EncABC(OpArrGet, 7, 2, 4),
            EncABC(OpMov, 5, 4, 0),
            EncABC(OpLe, 9, 6, 5),
            EncAU(OpJz, 9, 35),
            EncABC(OpSub, 10, 5, 6),
            EncABC(OpArrGet, 8, 2, 10),
            EncABC(OpLt, 9, 7, 8),
            EncAU(OpJz, 9, 35),
            EncABC(OpArrSet, 2, 5, 8),
            EncABC(OpMov, 5, 10, 0),
            EncAU(OpJmp, 0, 26),
            EncABC(OpArrSet, 2, 5, 7),
            EncAS(OpAddI, 4, 1),
            EncAU(OpJmp, 0, 22),
            EncABC(OpDiv, 6, 6, 11),
            EncAU(OpJmp, 0, 20),
            EncAS(OpLoadI, 8, 0),
            EncAS(OpLoadI, 4, 0),
            EncABC(OpLt, 9, 4, 0),
            EncAU(OpJz, 9, 51),
            EncABC(OpArrGet, 7, 2, 4),
            EncABC(OpArrGet, 10, 3, 4),
            EncABC(OpMul, 7, 7, 10),
            EncABC(OpAdd, 8, 8, 7),
            EncABC(OpMod, 8, 8, 15),
            EncAS(OpAddI, 4, 1),
            EncAU(OpJmp, 0, 42),
            EncABC(OpHalt, 8, 0, 0),
        },
        new long[] { 1_000_000_007L, 100003L, 48271L, 97L },
        Array.Empty<int>()
    );

    private static long Exec(VmState vm, Bytecode prog, long arg0, long arg1)
    {
        vm.HeapLen = 0;
        vm.ObjsLen = 0;
        long[] regs = vm.Regs;
        Frame[] frames = vm.Frames;
        uint[] code = prog.Code;
        long[] consts = prog.Consts;
        int[] funcs = prog.Funcs;

        regs[0] = arg0;
        regs[1] = arg1;
        int pc = 0;
        int bbase = 0;
        int sp = 0;

        while (true)
        {
            uint insn = code[pc++];
            uint op = insn & 255u;
            int a = (int)((insn >> 8) & 255u);
            switch (op)
            {
                case OpHalt:
                    return regs[bbase + a];
                case OpLoadI:
                    regs[bbase + a] = SignExt16(insn);
                    break;
                case OpLoadK:
                    regs[bbase + a] = consts[insn >> 16];
                    break;
                case OpMov:
                {
                    int b = (int)((insn >> 16) & 255u);
                    regs[bbase + a] = regs[bbase + b];
                    break;
                }
                case OpAdd:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    regs[bbase + a] = regs[bbase + b] + regs[bbase + c];
                    break;
                }
                case OpSub:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    regs[bbase + a] = regs[bbase + b] - regs[bbase + c];
                    break;
                }
                case OpMul:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    regs[bbase + a] = regs[bbase + b] * regs[bbase + c];
                    break;
                }
                case OpDiv:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    regs[bbase + a] = regs[bbase + b] / regs[bbase + c];
                    break;
                }
                case OpMod:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    regs[bbase + a] = regs[bbase + b] % regs[bbase + c];
                    break;
                }
                case OpAddI:
                    regs[bbase + a] += SignExt16(insn);
                    break;
                case OpBand:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    regs[bbase + a] = regs[bbase + b] & regs[bbase + c];
                    break;
                }
                case OpBxor:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    regs[bbase + a] = regs[bbase + b] ^ regs[bbase + c];
                    break;
                }
                case OpEq:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    regs[bbase + a] = regs[bbase + b] == regs[bbase + c] ? 1L : 0L;
                    break;
                }
                case OpLt:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    regs[bbase + a] = regs[bbase + b] < regs[bbase + c] ? 1L : 0L;
                    break;
                }
                case OpLe:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    regs[bbase + a] = regs[bbase + b] <= regs[bbase + c] ? 1L : 0L;
                    break;
                }
                case OpJmp:
                    pc = (int)(insn >> 16);
                    break;
                case OpJz:
                    if (regs[bbase + a] == 0)
                    {
                        pc = (int)(insn >> 16);
                    }
                    break;
                case OpJnz:
                    if (regs[bbase + a] != 0)
                    {
                        pc = (int)(insn >> 16);
                    }
                    break;
                case OpCall:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    frames[sp] = new Frame { RetPc = pc, CallerBase = bbase, RetReg = a };
                    sp++;
                    bbase += b;
                    pc = funcs[c];
                    break;
                }
                case OpRet:
                {
                    long val = regs[bbase + a];
                    if (sp == 0)
                    {
                        return val;
                    }
                    sp--;
                    Frame f = frames[sp];
                    bbase = f.CallerBase;
                    pc = f.RetPc;
                    regs[bbase + f.RetReg] = val;
                    break;
                }
                case OpArrAlloc:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int n = checked((int)regs[bbase + b]);
                    if (n < 0)
                    {
                        throw new InvalidOperationException("negative array size");
                    }
                    int off = vm.HeapLen;
                    int need = off + n;
                    if (need > vm.HeapData.Length)
                    {
                        int cap = vm.HeapData.Length;
                        while (cap < need) cap *= 2;
                        Array.Resize(ref vm.HeapData, cap);
                    }
                    Array.Clear(vm.HeapData, off, n);
                    vm.HeapLen = need;
                    if (vm.ObjsLen == vm.HeapObjs.Length)
                    {
                        Array.Resize(ref vm.HeapObjs, vm.HeapObjs.Length * 2);
                    }
                    int id = vm.ObjsLen++;
                    vm.HeapObjs[id] = new HeapObj { Off = off, Len = n };
                    regs[bbase + a] = id;
                    break;
                }
                case OpArrGet:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    int id = (int)regs[bbase + b];
                    int idx = (int)regs[bbase + c];
                    HeapObj obj = vm.HeapObjs[id];
                    if ((uint)idx >= (uint)obj.Len)
                    {
                        throw new IndexOutOfRangeException("array index out of bounds");
                    }
                    regs[bbase + a] = vm.HeapData[obj.Off + idx];
                    break;
                }
                case OpArrSet:
                {
                    int b = (int)((insn >> 16) & 255u);
                    int c = (int)((insn >> 24) & 255u);
                    int id = (int)regs[bbase + a];
                    int idx = (int)regs[bbase + b];
                    HeapObj obj = vm.HeapObjs[id];
                    if ((uint)idx >= (uint)obj.Len)
                    {
                        throw new IndexOutOfRangeException("array index out of bounds");
                    }
                    vm.HeapData[obj.Off + idx] = regs[bbase + c];
                    break;
                }
                default:
                    throw new InvalidOperationException($"illegal opcode {op}");
            }
        }
    }

    public static int Main(string[] args)
    {
        if (args.Length != 5)
        {
            Console.Error.WriteLine("usage: vm <fib_n> <sieve_n> <loop_n> <sort_n> <rounds>");
            return 2;
        }
        long fibN = long.Parse(args[0]);
        long sieveN = long.Parse(args[1]);
        long loopN = long.Parse(args[2]);
        long sortN = long.Parse(args[3]);
        long rounds = long.Parse(args[4]);

        Bytecode pFib = BuildFib();
        Bytecode pSieve = BuildSieve();
        Bytecode pArith = BuildArith();
        Bytecode pSort = BuildSortDot();
        VmState vm = new VmState();

        long sFib = 0, sSieve = 0, sArith = 0, sSort = 0;
        for (long r = 0; r < rounds; r++)
        {
            long seed = r + 1;
            sFib = (sFib + Exec(vm, pFib, fibN, 0)) % ModSum;
            sSieve = (sSieve + Exec(vm, pSieve, sieveN, 0)) % ModSum;
            sArith = (sArith + Exec(vm, pArith, loopN, seed)) % ModSum;
            sSort = (sSort + Exec(vm, pSort, sortN, seed)) % ModSum;
        }
        long total = (sFib + sSieve + sArith + sSort) % ModSum;
        Console.WriteLine($"fib={sFib} sieve={sSieve} arith={sArith} sortdot={sSort} total={total}");
        return 0;
    }
}
