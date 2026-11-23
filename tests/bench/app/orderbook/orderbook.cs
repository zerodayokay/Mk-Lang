// orderbook, C#, hand: the task of README.md, the replay of Dogfoods/orderbook
// (`orderbook replay FEED [--quiet] [--snapshot N]`). The feed is read through
// a 64 KiB buffered StreamReader with a hand CSV splitter for unquoted fields
// (about 50 lines; refuses quotes with status 3, as Rust, Zig and C do), fields
// trimmed of ASCII whitespace, numbers parsed with NumberStyles.AllowLeadingSign.
// Every container is .NET BCL's: Dictionary<ClientId, Handle> and
// HashSet<ClientId> for order lookup and done ids, HashSet<uint> for halted
// accounts, SortedList<long, Level> (descending key, so the best price is last
// and adding or dropping the best level is O(1)), List<Handle> with a head
// index for each level's FIFO queue, PriorityQueue<Handle, StopKey> min-heaps
// for armed stops, Dictionary<long, long> for volume by price, and List<Fill>
// for the trade log. The slot pool with generations (swept every 4,096
// commands) and the ulong[] bitset of live arrival numbers (BitOperations.PopCount)
// are the task's own structure, as in every cell. Checksum is FNV-1a 64 over
// 64-bit words (low byte first, wrapping ulong arithmetic). Batch statistics
// and report values always run, even under --quiet. Bounds-checked (class
// bounds). No unsafe, no MethodImpl, no Unsafe.*, no System.Runtime.Intrinsics,
// no Vector<T>.
using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Numerics;
using System.Text;

internal enum Side : byte
{
    Buy = 0,
    Sell = 1,
}

internal enum Kind : byte
{
    Limit = 0,
    Market = 1,
    Stop = 2,
    StopLimit = 3,
}

internal enum Op : byte
{
    Add = 0,
    Cancel = 1,
    Amend = 2,
    Halt = 3,
    Resume = 4,
    Bust = 5,
    Trigger = 6,
}

internal readonly struct ClientId : IEquatable<ClientId>
{
    public readonly uint Account;
    public readonly uint Serial;

    public ClientId(uint account, uint serial)
    {
        Account = account;
        Serial = serial;
    }

    public bool Equals(ClientId other) => Account == other.Account && Serial == other.Serial;
    public override bool Equals(object? obj) => obj is ClientId other && Equals(other);
    public override int GetHashCode() => HashCode.Combine(Account, Serial);
    public override string ToString() => $"{Account}/{Serial}";
}

internal readonly struct Handle : IEquatable<Handle>
{
    public readonly uint Idx;
    public readonly uint Gen;

    public Handle(uint idx, uint gen)
    {
        Idx = idx;
        Gen = gen;
    }

    public bool Equals(Handle other) => Idx == other.Idx && Gen == other.Gen;
    public override bool Equals(object? obj) => obj is Handle other && Equals(other);
    public override int GetHashCode() => HashCode.Combine(Idx, Gen);
    public static bool operator ==(Handle a, Handle b) => a.Idx == b.Idx && a.Gen == b.Gen;
    public static bool operator !=(Handle a, Handle b) => !(a == b);
}

internal struct Order
{
    public ClientId Id;
    public int Seq;
    public Side Side;
    public Kind Kind;
    public long Price;
    public long Stop;
    public long Qty;
    public long Left;
    public long Ts;
}

internal struct Fill : IEquatable<Fill>
{
    public ClientId Taker;
    public ClientId Maker;
    public int TakerSeq;
    public int MakerSeq;
    public Side Side;
    public long Price;
    public long Qty;
    public long Ts;

    public bool Equals(Fill other) =>
        Taker.Equals(other.Taker) &&
        Maker.Equals(other.Maker) &&
        TakerSeq == other.TakerSeq &&
        MakerSeq == other.MakerSeq &&
        Side == other.Side &&
        Price == other.Price &&
        Qty == other.Qty &&
        Ts == other.Ts;

    public override bool Equals(object? obj) => obj is Fill other && Equals(other);
    public override int GetHashCode() => HashCode.Combine(TakerSeq, MakerSeq, Price, Qty, Ts);
}

internal struct Command
{
    public Op Op;
    public Side Side;
    public Kind Kind;
    public ClientId Id;
    public uint Account;
    public long Ts;
    public long Qty;
    public long Price;
    public long Stop;
    public Handle H;
}

internal static class Rejects
{
    public const int Duplicate = 0;
    public const int Halted = 1;
    public const int Unknown = 2;
    public const int AlreadyDone = 3;
    public const int NoLiquidity = 4;
    public const int BadQuantity = 5;
    public const int NothingToBust = 6;
    public const int Count = 7;

    public static readonly string[] Names =
    {
        "duplicate id",
        "account halted",
        "unknown id",
        "already done",
        "no liquidity",
        "bad quantity",
        "nothing to bust",
    };
}

// ---- the slot pool

internal struct Slot
{
    public uint Gen;
    public bool Used;
    public Order Order;
}

internal sealed class Pool
{
    public Slot[] Slots = new Slot[1024];
    public int Count;
    private readonly List<uint> _vacant = new List<uint>();

    public Handle Add(in Order o)
    {
        int vc = _vacant.Count;
        if (vc > 0)
        {
            uint idx = _vacant[vc - 1];
            _vacant.RemoveAt(vc - 1);
            ref Slot s = ref Slots[idx];
            s.Used = true;
            s.Order = o;
            return new Handle(idx, s.Gen);
        }
        if (Count == Slots.Length)
        {
            Array.Resize(ref Slots, Slots.Length * 2);
        }
        uint i = (uint)Count;
        Count++;
        Slots[i] = new Slot { Gen = 0, Used = true, Order = o };
        return new Handle(i, 0);
    }

    public bool TryGetIndex(Handle h, out int idx)
    {
        int i = (int)h.Idx;
        if ((uint)i < (uint)Count)
        {
            ref Slot s = ref Slots[i];
            if (s.Used && s.Gen == h.Gen)
            {
                idx = i;
                return true;
            }
        }
        idx = -1;
        return false;
    }

    private void FreeAt(uint i)
    {
        ref Slot s = ref Slots[i];
        s.Used = false;
        s.Gen++;
        s.Order = default;
        _vacant.Add(i);
    }

    public bool Free(Handle h)
    {
        if (!TryGetIndex(h, out _))
        {
            return false;
        }
        FreeAt(h.Idx);
        return true;
    }

    public int Sweep(Orders orders)
    {
        int n = 0;
        for (int i = 0; i < Count; i++)
        {
            if (Slots[i].Used && !orders.Has(Slots[i].Order.Seq))
            {
                FreeAt((uint)i);
                n++;
            }
        }
        return n;
    }
}

// ---- the order store

internal sealed class Orders
{
    public readonly Pool Pool = new Pool();
    public readonly ulong[] Flags;
    public readonly int NBits;
    public readonly Dictionary<ClientId, Handle> Index = new Dictionary<ClientId, Handle>();
    public readonly HashSet<ClientId> Done = new HashSet<ClientId>();
    public int Seq;

    public Orders(int capacity)
    {
        NBits = capacity;
        Flags = new ulong[(capacity + 63) / 64];
    }

    public bool Has(int seq) => (Flags[seq >> 6] & (1UL << (seq & 63))) != 0;

    private void Set(int seq)
    {
        if (seq >= NBits)
        {
            throw new InvalidOperationException("orders past the capacity");
        }
        Flags[seq >> 6] |= 1UL << (seq & 63);
    }

    private void Clear(int seq) => Flags[seq >> 6] &= ~(1UL << (seq & 63));

    public bool TryGetLive(Handle h, out int slotIdx)
    {
        if (Pool.TryGetIndex(h, out int i) && Has(Pool.Slots[i].Order.Seq))
        {
            slotIdx = i;
            return true;
        }
        slotIdx = -1;
        return false;
    }

    public Handle Add(Order o)
    {
        o.Seq = Seq;
        Handle h = Pool.Add(in o);
        Index[o.Id] = h;
        Set(o.Seq);
        Seq++;
        Done.Remove(o.Id);
        return h;
    }

    public Handle Reissue(Order o)
    {
        o.Seq = Seq;
        Handle h = Pool.Add(in o);
        if (Index.TryGetValue(o.Id, out Handle old) && Pool.TryGetIndex(old, out int prevIdx))
        {
            Clear(Pool.Slots[prevIdx].Order.Seq);
        }
        Index[o.Id] = h;
        Set(o.Seq);
        Seq++;
        return h;
    }

    public void Retire(Handle h)
    {
        if (!Pool.TryGetIndex(h, out int i))
        {
            return;
        }
        Order o = Pool.Slots[i].Order;
        Clear(o.Seq);
        Index.Remove(o.Id);
        Done.Add(o.Id);
    }

    public int LiveCount()
    {
        int c = 0;
        for (int i = 0; i < Flags.Length; i++)
        {
            c += BitOperations.PopCount(Flags[i]);
        }
        if (c != Index.Count)
        {
            throw new InvalidOperationException("live flags and index disagree");
        }
        return c;
    }

    public int Sweep() => Pool.Sweep(this);
}

// ---- one side of the book

internal sealed class Level
{
    public long Qty;
    public int Live;
    public List<Handle> Orders;
    public int Head;

    public Level(long qty, int live, List<Handle> orders)
    {
        Qty = qty;
        Live = live;
        Orders = orders;
        Head = 0;
    }

    public int QueueLen => Orders.Count - Head;
}

internal readonly struct Depth
{
    public readonly long Price;
    public readonly long Qty;
    public readonly int Orders;

    public Depth(long price, long qty, int orders)
    {
        Price = price;
        Qty = qty;
        Orders = orders;
    }
}

internal readonly struct Expired
{
    public readonly int Orders;
    public readonly long Qty;

    public Expired(int orders, long qty)
    {
        Orders = orders;
        Qty = qty;
    }
}

internal sealed class DescendingLongComparer : IComparer<long>
{
    public static readonly DescendingLongComparer Instance = new DescendingLongComparer();
    public int Compare(long x, long y) => y.CompareTo(x);
}

internal sealed class Ladder
{
    private const int CompactSlack = 8;
    // Descending key order: best price is always at Count - 1, so adding or
    // removing the best level moves no other level.
    private readonly SortedList<long, Level> _levels = new SortedList<long, Level>(DescendingLongComparer.Instance);
    private readonly long _sign;

    public Ladder(long sign)
    {
        _sign = sign;
    }

    public bool TryBest(out long price)
    {
        int n = _levels.Count;
        if (n == 0)
        {
            price = 0;
            return false;
        }
        price = _levels.Keys[n - 1] * _sign;
        return true;
    }

    private bool Crosses(long price, long limit) => _sign > 0 ? price <= limit : price >= limit;

    public void Rest(long price, long qty, Handle h)
    {
        long key = price * _sign;
        if (_levels.TryGetValue(key, out Level? lvl))
        {
            lvl.Orders.Add(h);
            lvl.Qty += qty;
            lvl.Live++;
            return;
        }
        List<Handle> q = new List<Handle>(4) { h };
        _levels.Add(key, new Level(qty, 1, q));
    }

    public void Withdraw(long price, long qty, Handle h)
    {
        long key = price * _sign;
        if (!_levels.TryGetValue(key, out Level? lvl))
        {
            return;
        }
        lvl.Qty -= qty;
        lvl.Live--;
        int count = lvl.Orders.Count;
        if (count > lvl.Head && lvl.Orders[count - 1] == h)
        {
            lvl.Orders.RemoveAt(count - 1);
        }
        if (lvl.Qty == 0)
        {
            _levels.Remove(key);
        }
    }

    public void Compact(long price, Orders book)
    {
        long key = price * _sign;
        int idx = _levels.IndexOfKey(key);
        if (idx < 0)
        {
            return;
        }
        Level old = _levels.Values[idx];
        if (old.QueueLen <= 2 * old.Live + CompactSlack)
        {
            return;
        }
        List<Handle> fresh = new List<Handle>(old.Live);
        for (int i = old.Head; i < old.Orders.Count; i++)
        {
            Handle h = old.Orders[i];
            if (book.TryGetLive(h, out _))
            {
                fresh.Add(h);
            }
        }
        old.Orders = fresh;
        old.Head = 0;
    }

    public Expired Close(Orders book)
    {
        int orders = 0;
        long qty = 0;
        IList<Level> vals = _levels.Values;
        for (int i = 0; i < vals.Count; i++)
        {
            Level lvl = vals[i];
            qty += lvl.Qty;
            for (int j = lvl.Head; j < lvl.Orders.Count; j++)
            {
                if (book.TryGetLive(lvl.Orders[j], out _))
                {
                    orders++;
                }
            }
        }
        _levels.Clear();
        return new Expired(orders, qty);
    }

    public void Match(Orders book, Handle taker, long now, Trades outLog)
    {
        while (true)
        {
            if (!book.TryGetLive(taker, out int tIdx))
            {
                return;
            }
            ref Order t = ref book.Pool.Slots[tIdx].Order;
            if (t.Left == 0)
            {
                return;
            }
            bool limited = t.Kind == Kind.Limit || t.Kind == Kind.StopLimit;
            int n = _levels.Count;
            if (n == 0)
            {
                return;
            }
            long key = _levels.Keys[n - 1];
            long price = key * _sign;
            if (limited && !Crosses(price, t.Price))
            {
                return;
            }
            Level lvl = _levels.Values[n - 1];
            SweepBest(n - 1, price, lvl, book, taker, now, outLog);
        }
    }

    private void SweepBest(int bestIdx, long price, Level lvl, Orders book, Handle taker, long now, Trades outLog)
    {
        while (lvl.Qty > 0 && lvl.Head < lvl.Orders.Count)
        {
            Handle maker = lvl.Orders[lvl.Head];
            if (!book.TryGetLive(maker, out int mIdx))
            {
                lvl.Head++;
                continue;
            }
            if (!book.Pool.TryGetIndex(taker, out int tIdx) || taker == maker)
            {
                lvl.Head++;
                continue;
            }
            ref Order t = ref book.Pool.Slots[tIdx].Order;
            ref Order m = ref book.Pool.Slots[mIdx].Order;
            long q = t.Left < m.Left ? t.Left : m.Left;
            t.Left -= q;
            m.Left -= q;
            lvl.Qty -= q;
            outLog.Record(new Fill
            {
                Taker = t.Id,
                Maker = m.Id,
                TakerSeq = t.Seq,
                MakerSeq = m.Seq,
                Side = t.Side,
                Price = price,
                Qty = q,
                Ts = now,
            });
            bool makerDone = m.Left == 0;
            bool takerDone = t.Left == 0;
            if (makerDone)
            {
                lvl.Head++;
                lvl.Live--;
                book.Retire(maker);
            }
            if (takerDone)
            {
                break;
            }
        }
        if (lvl.Qty == 0)
        {
            _levels.RemoveAt(bestIdx);
        }
    }

    public List<Depth> Top(int n)
    {
        List<Depth> res = new List<Depth>(n);
        IList<long> keys = _levels.Keys;
        IList<Level> vals = _levels.Values;
        for (int i = keys.Count - 1; i >= 0 && res.Count < n; i--)
        {
            Level lvl = vals[i];
            res.Add(new Depth(keys[i] * _sign, lvl.Qty, lvl.Live));
        }
        return res;
    }

    public long Quantity()
    {
        long s = 0;
        IList<Level> vals = _levels.Values;
        for (int i = 0; i < vals.Count; i++)
        {
            s += vals[i].Qty;
        }
        return s;
    }
}

// ---- trades and the checksum

internal sealed class Trades
{
    private const int TapeLength = 8;
    private const int TapeWindow = 1024;
    private const int TopFills = 5;
    public const ulong FnvBasis = 0xcbf29ce484222325UL;
    public const ulong FnvPrime = 0x100000001b3UL;

    public readonly List<Fill> Fills = new List<Fill>(1024);
    public readonly List<Fill> Top = new List<Fill>(TopFills + 1);
    private readonly Fill[] _tapeBuf = new Fill[TapeWindow];
    private int _tapeStart;
    private int _tapeLen;
    public readonly Dictionary<long, long> Volume = new Dictionary<long, long>();
    public long Last;
    public bool HasLast;
    public long Traded;
    public long Notional;
    public int Busted;
    public ulong Checksum = FnvBasis;

    public static ulong Mix(ulong h, ulong x)
    {
        for (int i = 0; i < 8; i++)
        {
            h = unchecked((h ^ (x & 0xffUL)) * FnvPrime);
            x >>= 8;
        }
        return h;
    }

    public static ulong MixInt(ulong h, long x) => Mix(h, unchecked((ulong)x));

    public void Record(in Fill f)
    {
        Fills.Add(f);
        int at = 0;
        while (at < Top.Count && Top[at].Qty >= f.Qty)
        {
            at++;
        }
        if (at < TopFills)
        {
            Top.Insert(at, f);
            if (Top.Count > TopFills)
            {
                Top.RemoveAt(TopFills);
            }
        }
        if (_tapeLen == TapeLength)
        {
            if (_tapeStart + _tapeLen == TapeWindow)
            {
                Array.Copy(_tapeBuf, _tapeStart + 1, _tapeBuf, 0, TapeLength - 1);
                _tapeStart = 0;
                _tapeLen = TapeLength - 1;
            }
            else
            {
                _tapeStart++;
                _tapeLen--;
            }
        }
        _tapeBuf[_tapeStart + _tapeLen] = f;
        _tapeLen++;

        if (Volume.TryGetValue(f.Price, out long v))
        {
            Volume[f.Price] = v + f.Qty;
        }
        else
        {
            Volume[f.Price] = f.Qty;
        }
        Last = f.Price;
        HasLast = true;
        Traded += f.Qty;
        Notional += f.Qty * f.Price;
        ulong h = Checksum;
        h = Mix(h, unchecked((ulong)f.TakerSeq));
        h = Mix(h, unchecked((ulong)f.MakerSeq));
        h = MixInt(h, f.Price);
        h = MixInt(h, f.Qty);
        h = MixInt(h, f.Ts);
        Checksum = h;
    }

    public bool Bust()
    {
        int n = Fills.Count;
        if (n == 0)
        {
            return false;
        }
        Fill f = Fills[n - 1];
        Fills.RemoveAt(n - 1);
        if (_tapeLen > 0)
        {
            _tapeLen--;
        }
        int at = Top.IndexOf(f);
        if (at >= 0)
        {
            Top.RemoveAt(at);
        }
        if (Volume.TryGetValue(f.Price, out long v))
        {
            long rem = v - f.Qty;
            if (rem == 0)
            {
                Volume.Remove(f.Price);
            }
            else
            {
                Volume[f.Price] = rem;
            }
        }
        Traded -= f.Qty;
        Notional -= f.Qty * f.Price;
        Busted++;
        Checksum = Mix(Checksum, 0xb057UL);
        return true;
    }

    public List<Fill> RecentNewestFirst()
    {
        List<Fill> res = new List<Fill>(_tapeLen);
        for (int i = _tapeLen - 1; i >= 0; i--)
        {
            res.Add(_tapeBuf[_tapeStart + i]);
        }
        return res;
    }
}

// ---- the engine

internal readonly struct StopKey : IComparable<StopKey>
{
    public readonly long Key;
    public readonly int Seq;

    public StopKey(long key, int seq)
    {
        Key = key;
        Seq = seq;
    }

    public int CompareTo(StopKey other)
    {
        int c = Key.CompareTo(other.Key);
        return c != 0 ? c : Seq.CompareTo(other.Seq);
    }
}

internal struct Snapshot
{
    public int Event;
    public int Live;
    public int Fills;
}

internal sealed class Engine
{
    private const int SweepEvery = 4096;

    public readonly Ladder Bids = new Ladder(-1);
    public readonly Ladder Asks = new Ladder(1);
    public readonly Orders Orders;
    public readonly Trades Log = new Trades();
    public readonly PriorityQueue<Handle, StopKey> BuyStops = new PriorityQueue<Handle, StopKey>();
    public readonly PriorityQueue<Handle, StopKey> SellStops = new PriorityQueue<Handle, StopKey>();
    private readonly List<Command> _work = new List<Command>(16);
    private readonly List<Handle> _fired = new List<Handle>(16);
    public readonly HashSet<uint> Halted = new HashSet<uint>();
    public readonly int[] RejectCounts = new int[Rejects.Count];
    public Snapshot Snap;
    private readonly int _every;
    private long _now;
    public int Events;
    public int Cancels;
    public int Swept;

    public Engine(int capacity, int every)
    {
        Orders = new Orders(capacity);
        _every = every;
    }

    public void Submit(in Command cmd)
    {
        _work.Clear();
        _work.Add(cmd);
        while (_work.Count > 0)
        {
            Command c = _work[_work.Count - 1];
            _work.RemoveAt(_work.Count - 1);
            Apply(in c);
        }
        Events++;
        if (Events % SweepEvery == 0)
        {
            Swept += Orders.Sweep();
        }
        if (_every > 0 && Events % _every == 0)
        {
            Snap = new Snapshot { Event = Events, Live = Orders.LiveCount(), Fills = Log.Fills.Count };
        }
    }

    private void Apply(in Command c)
    {
        switch (c.Op)
        {
            case Op.Add:
                _now = c.Ts;
                Add(new Order
                {
                    Id = c.Id,
                    Side = c.Side,
                    Kind = c.Kind,
                    Price = c.Price,
                    Stop = c.Stop,
                    Qty = c.Qty,
                    Left = c.Qty,
                    Ts = c.Ts,
                });
                break;
            case Op.Cancel:
                _now = c.Ts;
                Cancel(c.Id);
                break;
            case Op.Amend:
                _now = c.Ts;
                Amend(c.Ts, c.Id, c.Qty, c.Price);
                break;
            case Op.Halt:
                _now = c.Ts;
                Halted.Add(c.Account);
                break;
            case Op.Resume:
                _now = c.Ts;
                Halted.Remove(c.Account);
                break;
            case Op.Bust:
                _now = c.Ts;
                if (!Log.Bust())
                {
                    Refuse(Rejects.NothingToBust);
                }
                break;
            case Op.Trigger:
                Fire(c.H);
                break;
        }
    }

    private void Refuse(int code)
    {
        RejectCounts[code]++;
        Log.Checksum = Trades.Mix(Log.Checksum, 0x7e00UL + (ulong)code);
    }

    private void Add(in Order o)
    {
        if (o.Qty <= 0 || (o.Kind != Kind.Market && o.Kind != Kind.Stop && o.Price <= 0))
        {
            Refuse(Rejects.BadQuantity);
            return;
        }
        if (Halted.Contains(o.Id.Account))
        {
            Refuse(Rejects.Halted);
            return;
        }
        if (Orders.Index.ContainsKey(o.Id))
        {
            Refuse(Rejects.Duplicate);
            return;
        }
        Handle h = Orders.Add(o);
        if (o.Kind == Kind.Stop || o.Kind == Kind.StopLimit)
        {
            Arm(h);
            return;
        }
        Execute(h);
    }

    private void Arm(Handle h)
    {
        if (!Orders.TryGetLive(h, out int idx))
        {
            return;
        }
        Order o = Orders.Pool.Slots[idx].Order;
        if (Log.HasLast)
        {
            long last = Log.Last;
            if ((o.Side == Side.Buy && last >= o.Stop) || (o.Side == Side.Sell && last <= o.Stop))
            {
                Fire(h);
                return;
            }
        }
        if (o.Side == Side.Buy)
        {
            BuyStops.Enqueue(h, new StopKey(o.Stop, o.Seq));
        }
        else
        {
            SellStops.Enqueue(h, new StopKey(-o.Stop, o.Seq));
        }
    }

    private void Fire(Handle h)
    {
        if (!Orders.TryGetLive(h, out int idx))
        {
            return;
        }
        ref Order o = ref Orders.Pool.Slots[idx].Order;
        o.Kind = o.Kind == Kind.StopLimit ? Kind.Limit : Kind.Market;
        Execute(h);
    }

    private void Execute(Handle h)
    {
        int before = Log.Fills.Count;
        if (!Orders.TryGetLive(h, out int idx))
        {
            return;
        }
        Side side = Orders.Pool.Slots[idx].Order.Side;
        if (side == Side.Buy)
        {
            Asks.Match(Orders, h, _now, Log);
        }
        else
        {
            Bids.Match(Orders, h, _now, Log);
        }
        if (!Orders.TryGetLive(h, out idx))
        {
            return;
        }
        Order o = Orders.Pool.Slots[idx].Order;
        if (o.Left == 0)
        {
            Orders.Retire(h);
        }
        else if (o.Kind == Kind.Limit)
        {
            if (side == Side.Buy)
            {
                Bids.Rest(o.Price, o.Left, h);
            }
            else
            {
                Asks.Rest(o.Price, o.Left, h);
            }
        }
        else
        {
            Orders.Retire(h);
            if (Log.Fills.Count == before)
            {
                Orders.Pool.Free(h);
                Refuse(Rejects.NoLiquidity);
            }
        }
        if (Log.Fills.Count > before)
        {
            Triggers();
        }
    }

    private void Triggers()
    {
        if (!Log.HasLast)
        {
            return;
        }
        long last = Log.Last;
        while (BuyStops.TryPeek(out Handle h, out StopKey k) && k.Key <= last)
        {
            BuyStops.Dequeue();
            if (Orders.TryGetLive(h, out _))
            {
                _fired.Add(h);
            }
        }
        while (SellStops.TryPeek(out Handle h, out StopKey k) && k.Key <= -last)
        {
            SellStops.Dequeue();
            if (Orders.TryGetLive(h, out _))
            {
                _fired.Add(h);
            }
        }
        for (int i = _fired.Count - 1; i >= 0; i--)
        {
            _work.Add(new Command { Op = Op.Trigger, H = _fired[i] });
        }
        _fired.Clear();
    }

    private void DoneOrUnknown(ClientId id)
    {
        if (Orders.Done.Contains(id))
        {
            Refuse(Rejects.AlreadyDone);
        }
        else
        {
            Refuse(Rejects.Unknown);
        }
    }

    private void Cancel(ClientId id)
    {
        if (!Orders.Index.TryGetValue(id, out Handle h))
        {
            DoneOrUnknown(id);
            return;
        }
        if (Orders.TryGetLive(h, out int idx))
        {
            Order o = Orders.Pool.Slots[idx].Order;
            Orders.Retire(h);
            if (o.Kind == Kind.Limit)
            {
                Ladder l = o.Side == Side.Buy ? Bids : Asks;
                l.Withdraw(o.Price, o.Left, h);
                l.Compact(o.Price, Orders);
            }
        }
        Cancels++;
    }

    private void Amend(long ts, ClientId id, long qty, long price)
    {
        if (!Orders.Index.TryGetValue(id, out Handle h))
        {
            DoneOrUnknown(id);
            return;
        }
        if (qty <= 0 || price <= 0)
        {
            Refuse(Rejects.BadQuantity);
            return;
        }
        if (!Orders.TryGetLive(h, out int idx))
        {
            return;
        }
        Order o = Orders.Pool.Slots[idx].Order;
        if (o.Kind == Kind.Limit)
        {
            if (o.Side == Side.Buy)
            {
                Bids.Withdraw(o.Price, o.Left, h);
            }
            else
            {
                Asks.Withdraw(o.Price, o.Left, h);
            }
        }
        o.Qty = qty;
        o.Left = qty;
        o.Ts = ts;
        if (o.Kind == Kind.Stop)
        {
            o.Stop = price;
        }
        else
        {
            o.Price = price;
        }
        Handle h2 = Orders.Reissue(o);
        if (o.Kind == Kind.Stop || o.Kind == Kind.StopLimit)
        {
            Arm(h2);
            return;
        }
        Execute(h2);
    }

    public ulong ComputeChecksum()
    {
        ulong h = Log.Checksum;
        Bids.TryBest(out long bb);
        Asks.TryBest(out long ba);
        h = Trades.MixInt(h, bb);
        h = Trades.MixInt(h, ba);
        h = Trades.MixInt(h, Bids.Quantity());
        h = Trades.MixInt(h, Asks.Quantity());
        return Trades.Mix(h, (ulong)Orders.LiveCount());
    }
}

// ---- the hand CSV splitter for unquoted fields (~50 lines)

internal static class FeedParser
{
    private static bool TryParseStrictInt64(ReadOnlySpan<char> s, out long val) =>
        long.TryParse(s, NumberStyles.AllowLeadingSign, CultureInfo.InvariantCulture, out val);

    private static bool TryDigits(ReadOnlySpan<char> s, out long val)
    {
        if (s.Length == 0 || s.Length > 15)
        {
            val = 0;
            return false;
        }
        long n = 0;
        for (int i = 0; i < s.Length; i++)
        {
            char c = s[i];
            if (c < '0' || c > '9')
            {
                val = 0;
                return false;
            }
            n = n * 10 + (c - '0');
        }
        val = n;
        return true;
    }

    private static bool TryParsePrice(ReadOnlySpan<char> s, out long val)
    {
        int dot = s.IndexOf('.');
        ReadOnlySpan<char> whole = dot >= 0 ? s.Slice(0, dot) : s;
        if (!TryDigits(whole, out long w))
        {
            val = 0;
            return false;
        }
        long c = 0;
        if (dot >= 0)
        {
            ReadOnlySpan<char> frac = s.Slice(dot + 1);
            if (frac.Length == 0 || frac.Length > 2 || !TryDigits(frac, out c))
            {
                val = 0;
                return false;
            }
            if (frac.Length == 1)
            {
                c *= 10;
            }
        }
        if (w > 9_999_999_999_999L)
        {
            val = 0;
            return false;
        }
        val = w * 100 + c;
        return true;
    }

    private static bool TryAccount(ReadOnlySpan<char> s, out uint val)
    {
        if (!TryParseStrictInt64(s, out long n) || n < 0 || n > 0xffffffffL)
        {
            val = 0;
            return false;
        }
        val = (uint)n;
        return true;
    }

    private static bool TryClientId(ReadOnlySpan<char> s, out ClientId id)
    {
        int slash = s.IndexOf('/');
        if (slash < 0 || !TryAccount(s.Slice(0, slash), out uint a) || !TryAccount(s.Slice(slash + 1), out uint b))
        {
            id = default;
            return false;
        }
        id = new ClientId(a, b);
        return true;
    }

    private static ReadOnlySpan<char> TrimAscii(ReadOnlySpan<char> s)
    {
        int lo = 0;
        int hi = s.Length;
        while (lo < hi && s[lo] <= ' ' && (s[lo] == ' ' || s[lo] == '\t' || s[lo] == '\r' || s[lo] == '\n' || s[lo] == '\v' || s[lo] == '\f'))
        {
            lo++;
        }
        while (hi > lo && s[hi - 1] <= ' ' && (s[hi - 1] == ' ' || s[hi - 1] == '\t' || s[hi - 1] == '\r' || s[hi - 1] == '\n' || s[hi - 1] == '\v' || s[hi - 1] == '\f'))
        {
            hi--;
        }
        return s.Slice(lo, hi - lo);
    }

    // Returns: 1 = command, 0 = comment/blank, -1 = error.
    public static int ParseLine(ReadOnlySpan<char> line, out Command cmd)
    {
        cmd = default;
        if (line.IndexOf('"') >= 0)
        {
            return -1;
        }
        int c0s = 0, c0l = 0, c1s = 0, c1l = 0, c2s = 0, c2l = 0, c3s = 0, c3l = 0;
        int c4s = 0, c4l = 0, c5s = 0, c5l = 0, c6s = 0, c6l = 0;
        int count = 0;
        int pos = 0;
        while (true)
        {
            int comma = line.Slice(pos).IndexOf(',');
            int end = comma >= 0 ? pos + comma : line.Length;
            ReadOnlySpan<char> raw = line.Slice(pos, end - pos);
            int lo = 0;
            int hi = raw.Length;
            while (lo < hi && (raw[lo] == ' ' || raw[lo] == '\t' || raw[lo] == '\r' || raw[lo] == '\n' || raw[lo] == '\v' || raw[lo] == '\f')) lo++;
            while (hi > lo && (raw[hi - 1] == ' ' || raw[hi - 1] == '\t' || raw[hi - 1] == '\r' || raw[hi - 1] == '\n' || raw[hi - 1] == '\v' || raw[hi - 1] == '\f')) hi--;
            int fs = pos + lo;
            int fl = hi - lo;
            if (count == 0 && fl > 0 && line[fs] == '#')
            {
                return 0;
            }
            switch (count)
            {
                case 0: c0s = fs; c0l = fl; break;
                case 1: c1s = fs; c1l = fl; break;
                case 2: c2s = fs; c2l = fl; break;
                case 3: c3s = fs; c3l = fl; break;
                case 4: c4s = fs; c4l = fl; break;
                case 5: c5s = fs; c5l = fl; break;
                case 6: c6s = fs; c6l = fl; break;
            }
            count++;
            if (comma < 0)
            {
                break;
            }
            pos = end + 1;
        }
        if (count == 1 && c0l == 0)
        {
            return 0;
        }
        if (c0l != 1)
        {
            return -1;
        }
        char tag = line[c0s];
        int want = tag switch
        {
            'X' => 2,
            'C' or 'H' or 'R' => 3,
            'M' or 'A' => 5,
            'N' or 'S' => 6,
            'T' => 7,
            _ => 0,
        };
        if (want == 0 || count != want)
        {
            return -1;
        }
        if (!TryParseStrictInt64(line.Slice(c1s, c1l), out long ts))
        {
            return -1;
        }
        cmd.Ts = ts;
        if (tag == 'X')
        {
            cmd.Op = Op.Bust;
            return 1;
        }
        if (tag == 'H' || tag == 'R')
        {
            if (!TryAccount(line.Slice(c2s, c2l), out uint acct))
            {
                return -1;
            }
            cmd.Op = tag == 'H' ? Op.Halt : Op.Resume;
            cmd.Account = acct;
            return 1;
        }
        if (!TryClientId(line.Slice(c2s, c2l), out ClientId id))
        {
            return -1;
        }
        cmd.Id = id;
        if (tag == 'C')
        {
            cmd.Op = Op.Cancel;
            return 1;
        }
        if (tag == 'A')
        {
            if (!TryParseStrictInt64(line.Slice(c3s, c3l), out long aq) || !TryParsePrice(line.Slice(c4s, c4l), out long ap))
            {
                return -1;
            }
            cmd.Op = Op.Amend;
            cmd.Qty = aq;
            cmd.Price = ap;
            return 1;
        }
        if (c3l != 1)
        {
            return -1;
        }
        char sc = line[c3s];
        if (sc == 'B')
        {
            cmd.Side = Side.Buy;
        }
        else if (sc == 'S')
        {
            cmd.Side = Side.Sell;
        }
        else
        {
            return -1;
        }
        if (!TryParseStrictInt64(line.Slice(c4s, c4l), out long qty))
        {
            return -1;
        }
        cmd.Op = Op.Add;
        cmd.Qty = qty;
        switch (tag)
        {
            case 'N':
                cmd.Kind = Kind.Limit;
                return TryParsePrice(line.Slice(c5s, c5l), out cmd.Price) ? 1 : -1;
            case 'M':
                cmd.Kind = Kind.Market;
                return 1;
            case 'S':
                cmd.Kind = Kind.Stop;
                return TryParsePrice(line.Slice(c5s, c5l), out cmd.Stop) ? 1 : -1;
            case 'T':
                cmd.Kind = Kind.StopLimit;
                return (TryParsePrice(line.Slice(c5s, c5l), out cmd.Stop) &&
                        TryParsePrice(line.Slice(c6s, c6l), out cmd.Price)) ? 1 : -1;
            default:
                return -1;
        }
    }
}

// ---- the statistics

internal struct Bar
{
    public long Open;
    public long High;
    public long Low;
    public long Close;
    public long Volume;
    public int Trades;
}

internal struct AccountRow
{
    public uint Account;
    public long Bought;
    public long Sold;
    public int Fills;
}

internal readonly struct OptPrice
{
    public readonly long Value;
    public readonly bool Ok;

    public OptPrice(long value, bool ok)
    {
        Value = value;
        Ok = ok;
    }

    public string FormatText() => Ok ? Program.PriceText(Value) : "-";
}

internal sealed class Summary
{
    public OptPrice P50, P90, P99, Vwap, BuyVwap, SellVwap;
    public int AtOrBelow;
    public List<Bar> Bars = new List<Bar>();
    public readonly int[] Sizes = new int[7];
    public List<AccountRow> Accounts = new List<AccountRow>();
}

internal sealed class ReportValues
{
    public readonly List<string> Prices = new List<string>();
    public readonly List<string> Levels = new List<string>();
    public readonly List<string> Largest = new List<string>();
    public readonly List<string> Bars = new List<string>();
    public readonly List<AccountRow> Accounts = new List<AccountRow>();
    public int RejectsFound;
}

internal static class Program
{
    private static readonly long[] SizeLimits = { 10, 50, 100, 500, 1000, 5000 };
    private static readonly string[] SizeLabels = { "<=10", "<=50", "<=100", "<=500", "<=1000", "<=5000", ">5000" };

    public static string PriceText(long t)
    {
        if (t < 0)
        {
            long pos = -t;
            return string.Create(CultureInfo.InvariantCulture, $"-{pos / 100}.{pos % 100:D2}");
        }
        return string.Create(CultureInfo.InvariantCulture, $"{t / 100}.{t % 100:D2}");
    }

    private static string SideText(Side s) => s == Side.Buy ? "Buy" : "Sell";

    private static string KindText(Kind k) => k switch
    {
        Kind.Limit => "Limit",
        Kind.Market => "Market",
        Kind.Stop => "Stop",
        Kind.StopLimit => "StopLimit",
        _ => "?",
    };

    private static OptPrice Percentile(long[] sorted, int p)
    {
        if (sorted.Length == 0)
        {
            return default;
        }
        long rank = ((long)sorted.Length * p + 99) / 100;
        int at = rank > 0 ? (int)(rank - 1) : 0;
        return new OptPrice(sorted[at], true);
    }

    private static OptPrice ComputeVwap(ReadOnlySpan<Fill> fills)
    {
        long q = 0;
        long n = 0;
        for (int i = 0; i < fills.Length; i++)
        {
            q += fills[i].Qty;
            n += fills[i].Qty * fills[i].Price;
        }
        return q == 0 ? default : new OptPrice(n / q, true);
    }

    private static int UpperBound(long[] sorted, long target)
    {
        int lo = 0;
        int hi = sorted.Length;
        while (lo < hi)
        {
            int mid = lo + ((hi - lo) >> 1);
            if (sorted[mid] <= target)
            {
                lo = mid + 1;
            }
            else
            {
                hi = mid;
            }
        }
        return lo;
    }

    private static List<Bar> ComputeBars(List<Fill> fills, int per)
    {
        List<Bar> res = new List<Bar>();
        int n = fills.Count;
        if (n == 0 || per < 1)
        {
            return res;
        }
        long[] volumes = new long[n];
        for (int i = 0; i < n; i++)
        {
            volumes[i] = fills[i].Qty;
        }
        for (int lo = 0; lo < n; lo += per)
        {
            int hi = Math.Min(lo + per, n);
            long sum = 0;
            for (int i = lo; i < hi; i++)
            {
                sum += volumes[i];
                volumes[i] = sum;
            }
        }
        for (int lo = 0; lo < n; lo += per)
        {
            int hi = Math.Min(lo + per, n);
            long open = fills[lo].Price;
            long high = open;
            long low = open;
            long close = fills[hi - 1].Price;
            for (int i = lo; i < hi; i++)
            {
                long p = fills[i].Price;
                if (p > high) high = p;
                if (p < low) low = p;
            }
            res.Add(new Bar
            {
                Open = open,
                High = high,
                Low = low,
                Close = close,
                Volume = volumes[hi - 1],
                Trades = hi - lo,
            });
        }
        if (res.Count >= 2 && res[res.Count - 1].Trades * 2 < per)
        {
            Bar last = res[res.Count - 1];
            Bar prev = res[res.Count - 2];
            if (last.High > prev.High) prev.High = last.High;
            if (last.Low < prev.Low) prev.Low = last.Low;
            prev.Close = last.Close;
            prev.Volume += last.Volume;
            prev.Trades += last.Trades;
            res[res.Count - 2] = prev;
            res.RemoveAt(res.Count - 1);
        }
        return res;
    }

    private sealed class AccountAcc
    {
        public uint Account;
        public long Bought;
        public long Sold;
        public int Fills;
    }

    private static List<AccountRow> ComputeAccounts(List<Fill> fills)
    {
        Dictionary<uint, AccountAcc> by = new Dictionary<uint, AccountAcc>();
        for (int i = 0; i < fills.Count; i++)
        {
            Fill f = fills[i];
            uint buyer = f.Taker.Account;
            uint seller = f.Maker.Account;
            if (f.Side != Side.Buy)
            {
                (buyer, seller) = (seller, buyer);
            }
            if (!by.TryGetValue(buyer, out AccountAcc? b))
            {
                b = new AccountAcc { Account = buyer };
                by[buyer] = b;
            }
            b.Bought += f.Qty;
            b.Fills++;
            if (!by.TryGetValue(seller, out AccountAcc? s))
            {
                s = new AccountAcc { Account = seller };
                by[seller] = s;
            }
            s.Sold += f.Qty;
            s.Fills++;
        }
        List<AccountRow> rows = new List<AccountRow>(by.Count);
        foreach (AccountAcc a in by.Values)
        {
            rows.Add(new AccountRow { Account = a.Account, Bought = a.Bought, Sold = a.Sold, Fills = a.Fills });
        }
        // Equivalent to sorting by account asc then stably by volume desc, since
        // every AccountRow has a distinct Account.
        rows.Sort(static (a, b) =>
        {
            int c = (b.Bought + b.Sold).CompareTo(a.Bought + a.Sold);
            return c != 0 ? c : a.Account.CompareTo(b.Account);
        });
        return rows;
    }

    private static Summary Summarize(List<Fill> fills, int perBar)
    {
        Summary s = new Summary();
        int n = fills.Count;
        long[] prices = new long[n];
        for (int i = 0; i < n; i++)
        {
            prices[i] = fills[i].Price;
        }
        long[] sorted = new long[n];
        Array.Copy(prices, sorted, n);
        Array.Sort(sorted);

        Fill[] xs = fills.ToArray();
        s.Vwap = ComputeVwap(xs);
        if (s.Vwap.Ok)
        {
            s.AtOrBelow = UpperBound(sorted, s.Vwap.Value);
        }
        int cut = 0;
        for (int i = 0; i < xs.Length; i++)
        {
            if (xs[i].Side == Side.Buy)
            {
                (xs[i], xs[cut]) = (xs[cut], xs[i]);
                cut++;
            }
        }
        s.BuyVwap = ComputeVwap(xs.AsSpan(0, cut));
        s.SellVwap = ComputeVwap(xs.AsSpan(cut));
        s.P50 = Percentile(sorted, 50);
        s.P90 = Percentile(sorted, 90);
        s.P99 = Percentile(sorted, 99);
        s.Bars = ComputeBars(fills, perBar);
        for (int i = 0; i < n; i++)
        {
            long qty = fills[i].Qty;
            int cls = 0;
            for (int k = 0; k < SizeLimits.Length; k++)
            {
                if (qty <= SizeLimits[k])
                {
                    break;
                }
                cls++;
            }
            s.Sizes[cls]++;
        }
        s.Accounts = ComputeAccounts(fills);
        return s;
    }

    private static ReportValues BuildReport(Engine e, Summary s, List<Depth> bids, List<Depth> asks)
    {
        ReportValues r = new ReportValues();
        bool okb = e.Bids.TryBest(out long bb);
        bool oka = e.Asks.TryBest(out long ba);
        OptPrice[] ps =
        {
            s.Vwap, s.BuyVwap, s.SellVwap, s.P50, s.P99,
            new OptPrice(e.Log.Last, e.Log.HasLast),
            new OptPrice(bb, okb),
            new OptPrice(ba, oka),
        };
        for (int i = 0; i < ps.Length; i++)
        {
            if (ps[i].Ok)
            {
                r.Prices.Add(PriceText(ps[i].Value));
            }
        }
        for (int i = 0; i < bids.Count; i++) r.Levels.Add(PriceText(bids[i].Price));
        for (int i = 0; i < asks.Count; i++) r.Levels.Add(PriceText(asks[i].Price));
        for (int i = 0; i < e.Log.Top.Count; i++)
        {
            Fill f = e.Log.Top[i];
            r.Largest.Add($"{f.Taker} {f.Maker} {PriceText(f.Price)}");
        }
        for (int i = 0; i < s.Bars.Count; i++)
        {
            Bar b = s.Bars[i];
            r.Bars.Add(PriceText(b.Open) + PriceText(b.High) + PriceText(b.Low) + PriceText(b.Close));
        }
        int ac = Math.Min(10, s.Accounts.Count);
        for (int i = 0; i < ac; i++) r.Accounts.Add(s.Accounts[i]);
        for (int i = 0; i < e.RejectCounts.Length; i++)
        {
            if (e.RejectCounts[i] > 0) r.RejectsFound++;
        }
        return r;
    }

    private static void DepthTable(StreamWriter w, List<Depth> bids, List<Depth> asks)
    {
        w.Write(string.Format(CultureInfo.InvariantCulture, "{0,10} {1,6} {2,12} | {3,-12} {4,10} {5,6}\n", "bid qty", "orders", "bid", "ask", "ask qty", "orders"));
        int rows = Math.Max(bids.Count, asks.Count);
        if (rows == 0)
        {
            w.Write(string.Format(CultureInfo.InvariantCulture, "{0,20}{1}{2,21}\n", "", "(the book is empty)", ""));
        }
        for (int i = 0; i < rows; i++)
        {
            if (i < bids.Count)
            {
                w.Write(string.Format(CultureInfo.InvariantCulture, "{0,10} {1,6} {2,12}", bids[i].Qty, bids[i].Orders, PriceText(bids[i].Price)));
            }
            else
            {
                w.Write(string.Format(CultureInfo.InvariantCulture, "{0,30}", ""));
            }
            w.Write(" | ");
            if (i < asks.Count)
            {
                w.Write(string.Format(CultureInfo.InvariantCulture, "{0,-12} {1,10} {2,6}", PriceText(asks[i].Price), asks[i].Qty, asks[i].Orders));
            }
            w.Write('\n');
        }
    }

    private static void FillsTable(StreamWriter w, string title, List<Fill> fills)
    {
        w.Write(title);
        w.Write('\n');
        if (fills.Count == 0)
        {
            w.Write("  (none)\n");
            return;
        }
        for (int i = 0; i < fills.Count; i++)
        {
            Fill f = fills[i];
            w.Write(string.Format(CultureInfo.InvariantCulture, "  {0,10}  {1,-4} {2,7} @ {3,10}  taker {4,-9} maker {5}\n",
                f.Ts, SideText(f.Side), f.Qty, PriceText(f.Price), f.Taker.ToString(), f.Maker.ToString()));
        }
    }

    private static void WriteLinePair(StreamWriter w, string label, object v) =>
        w.Write(string.Format(CultureInfo.InvariantCulture, "{0,-18}{1,12}\n", label, v));

    private static void TextReport(StreamWriter w, Engine e, Summary s, List<Depth> bids, List<Depth> asks, string sum)
    {
        Trades t = e.Log;
        WriteLinePair(w, "commands", e.Events);
        WriteLinePair(w, "orders", e.Orders.Seq);
        WriteLinePair(w, "fills", t.Fills.Count);
        WriteLinePair(w, "cancels", e.Cancels);
        WriteLinePair(w, "busts", t.Busted);
        WriteLinePair(w, "live orders", e.Orders.LiveCount());
        WriteLinePair(w, "stop queue entries", e.BuyStops.Count + e.SellStops.Count);
        for (int i = 0; i < e.RejectCounts.Length; i++)
        {
            if (e.RejectCounts[i] > 0)
            {
                w.Write(string.Format(CultureInfo.InvariantCulture, "rejected: {0,-14}{1,12}\n", Rejects.Names[i], e.RejectCounts[i]));
            }
        }
        WriteLinePair(w, "volume", t.Traded);
        WriteLinePair(w, "vwap", s.Vwap.FormatText());
        WriteLinePair(w, "vwap, buyers", s.BuyVwap.FormatText());
        WriteLinePair(w, "vwap, sellers", s.SellVwap.FormatText());
        WriteLinePair(w, "median fill", s.P50.FormatText());
        WriteLinePair(w, "90th percentile", s.P90.FormatText());
        WriteLinePair(w, "99th percentile", s.P99.FormatText());
        WriteLinePair(w, "fills <= vwap", s.AtOrBelow);
        WriteLinePair(w, "last", new OptPrice(t.Last, t.HasLast).FormatText());
        WriteLinePair(w, "traded prices", t.Volume.Count);
        w.Write('\n');
        DepthTable(w, bids, asks);
        w.Write('\n');
        FillsTable(w, "largest fills", t.Top);
        FillsTable(w, "recent fills, newest first", t.RecentNewestFirst());

        List<Depth> busiest = new List<Depth>(t.Volume.Count);
        foreach (KeyValuePair<long, long> kv in t.Volume)
        {
            busiest.Add(new Depth(kv.Key, kv.Value, 0));
        }
        busiest.Sort(static (a, b) =>
        {
            int c = b.Qty.CompareTo(a.Qty);
            return c != 0 ? c : a.Price.CompareTo(b.Price);
        });
        w.Write("most traded prices\n");
        int bm = Math.Min(3, busiest.Count);
        for (int i = 0; i < bm; i++)
        {
            w.Write(string.Format(CultureInfo.InvariantCulture, "  {0,12} {1,10}\n", PriceText(busiest[i].Price), busiest[i].Qty));
        }

        List<Order> resting = new List<Order>();
        for (int i = 0; i < e.Orders.Pool.Count; i++)
        {
            ref Slot sl = ref e.Orders.Pool.Slots[i];
            if (sl.Used && e.Orders.Has(sl.Order.Seq))
            {
                resting.Add(sl.Order);
            }
        }
        resting.Sort(static (a, b) => a.Seq.CompareTo(b.Seq));
        w.Write(string.Format(CultureInfo.InvariantCulture, "oldest resting orders ({0} live)\n", resting.Count));
        int rm = Math.Min(3, resting.Count);
        for (int i = 0; i < rm; i++)
        {
            Order r = resting[i];
            long p = r.Kind == Kind.Stop ? r.Stop : r.Price;
            w.Write(string.Format(CultureInfo.InvariantCulture, "  {0,-9} {1,-4} {2,-9} {3,7} @ {4,10}\n",
                r.Id.ToString(), SideText(r.Side), KindText(r.Kind), r.Left, PriceText(p)));
        }
        if (s.Bars.Count > 0)
        {
            w.Write(string.Format(CultureInfo.InvariantCulture, "bars, newest first\n{0,5} {1,10} {2,10} {3,10} {4,10} {5,10}\n",
                "#", "open", "high", "low", "close", "volume"));
            int bc = Math.Min(10, s.Bars.Count);
            for (int i = 0; i < bc; i++)
            {
                Bar b = s.Bars[s.Bars.Count - 1 - i];
                w.Write(string.Format(CultureInfo.InvariantCulture, "{0,5} {1,10} {2,10} {3,10} {4,10} {5,10}\n",
                    s.Bars.Count - i, PriceText(b.Open), PriceText(b.High), PriceText(b.Low), PriceText(b.Close), b.Volume));
            }
        }
        w.Write("fill sizes\n");
        for (int i = 0; i < s.Sizes.Length; i++)
        {
            w.Write(string.Format(CultureInfo.InvariantCulture, "  {0,-7}{1,9}\n", SizeLabels[i], s.Sizes[i]));
        }
        w.Write(string.Format(CultureInfo.InvariantCulture, "accounts by volume\n{0,4} {1,8} {2,10} {3,10} {4,7}\n",
            "rank", "account", "bought", "sold", "fills"));
        int ac = Math.Min(10, s.Accounts.Count);
        for (int i = 0; i < ac; i++)
        {
            AccountRow r = s.Accounts[i];
            w.Write(string.Format(CultureInfo.InvariantCulture, "{0,4} {1,8} {2,10} {3,10} {4,7}\n",
                i + 1, r.Account, r.Bought, r.Sold, r.Fills));
        }
        w.Write(string.Format(CultureInfo.InvariantCulture, "checksum {0}\n", sum));
    }

    private static int Usage(string msg)
    {
        Console.Error.WriteLine($"error: {msg}");
        Console.Error.WriteLine("usage: orderbook replay FEED [--quiet] [--snapshot N]");
        return 2;
    }

    public static int Main(string[] args)
    {
        bool quiet = false;
        int every = 10_000;
        string path = string.Empty;
        if (args.Length == 0 || args[0] != "replay")
        {
            return Usage("the only command is replay");
        }
        for (int i = 1; i < args.Length; i++)
        {
            string a = args[i];
            if (a == "--quiet")
            {
                quiet = true;
            }
            else if (a == "--snapshot" && i + 1 < args.Length)
            {
                if (!long.TryParse(args[i + 1], NumberStyles.AllowLeadingSign, CultureInfo.InvariantCulture, out long n) ||
                    n < 0 || n > 1_000_000_000_000L)
                {
                    return Usage("--snapshot does not take that value");
                }
                every = (int)n;
                i++;
            }
            else if (!a.StartsWith("--", StringComparison.Ordinal) && path.Length == 0)
            {
                path = a;
            }
            else
            {
                return Usage("unexpected argument " + a);
            }
        }
        if (path.Length == 0)
        {
            return Usage("replay needs a feed file");
        }

        long fileSize;
        try
        {
            FileInfo fi = new FileInfo(path);
            if (!fi.Exists || (fi.Attributes & FileAttributes.Directory) != 0)
            {
                Console.Error.WriteLine($"error: cannot read {path}");
                return 4;
            }
            fileSize = fi.Length;
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"error: cannot read {path}: {ex.Message}");
            return 4;
        }

        Engine e = new Engine((int)(fileSize / 3) + 2, every);
        try
        {
            using FileStream fs = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read, 64 * 1024);
            using StreamReader sr = new StreamReader(fs, Encoding.UTF8, detectEncodingFromByteOrderMarks: false, bufferSize: 64 * 1024);
            int lineNo = 0;
            string? line;
            while ((line = sr.ReadLine()) != null)
            {
                lineNo++;
                int res = FeedParser.ParseLine(line.AsSpan(), out Command cmd);
                if (res < 0)
                {
                    Console.Error.WriteLine($"{path}:{lineNo}: not a valid record");
                    return 3;
                }
                if (res > 0)
                {
                    e.Submit(in cmd);
                }
            }
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"error: cannot read {path}: {ex.Message}");
            return 4;
        }

        Summary s = Summarize(e.Log.Fills, 1000);
        List<Depth> bids = e.Bids.Top(5);
        List<Depth> asks = e.Asks.Top(5);
        string sum = e.ComputeChecksum().ToString("x16", CultureInfo.InvariantCulture);

        using Stream stdout = Console.OpenStandardOutput();
        using StreamWriter w = new StreamWriter(stdout, new UTF8Encoding(false), 64 * 1024, leaveOpen: true);
        w.NewLine = "\n";
        if (quiet)
        {
            w.Write(string.Format(CultureInfo.InvariantCulture, "commands {0} fills {1} checksum {2}\n", e.Events, e.Log.Fills.Count, sum));
        }
        else
        {
            TextReport(w, e, s, bids, asks, sum);
        }
        ReportValues rv = BuildReport(e, s, bids, asks);
        Snapshot snap = e.Snap;
        Expired eb = e.Bids.Close(e.Orders);
        Expired ea = e.Asks.Close(e.Orders);
        if (!quiet)
        {
            w.Write(string.Format(CultureInfo.InvariantCulture, "last snapshot at command {0}: {1} live orders, {2} fills\n", snap.Event, snap.Live, snap.Fills));
            w.Write(string.Format(CultureInfo.InvariantCulture, "expired at close: {0} bids ({1}), {2} asks ({3})\n", eb.Orders, eb.Qty, ea.Orders, ea.Qty));
        }
        w.Flush();
        if (rv.RejectsFound < 0)
        {
            return 1;
        }
        return 0;
    }
}
