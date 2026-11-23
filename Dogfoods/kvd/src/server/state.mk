// Why: the tasks of a running server share one store, one log and one set
// of settings, and report events.
// What: Config, Event, State (a linear view) with Report, Stop and Finish.
// How: the store sits behind a read-write lock so readers proceed together;
// the log sits behind a mutex that every change takes first, so the order of
// records on disk is the order changes reach the store.

import std.atomic
import std.net
import std.sync
import std.time
import src.store
import src.wal

/// Settings fixed at start and read by every connection (sync.Shared).
export struct Config {
    /// Longest key accepted, in bytes.
    export max_key: int
    /// Longest value accepted, in bytes.
    export max_value: int
    /// Changes logged since the last compaction that start the next one.
    export compact_after: int
    /// A request slower than this is logged, in microseconds.
    export slow_micros: i64
    /// Connections served at once; more are told so and closed.
    export max_clients: int
    /// How long a client may send nothing before it is dropped, in
    /// milliseconds; 0 never drops it.
    export idle_millis: i64
    /// How long a request that only reads may wait before it is answered
    /// `-ERR TimedOut`, in milliseconds; 0 waits as long as it takes. A
    /// change, once it holds the log, runs to its end.
    export request_millis: i64
    /// Whether this is the admin channel's listener.
    export admin: bool
    /// Whether `SHUTDOWN` and `COMPACT` are refused here, since an admin
    /// channel serves them.
    export admin_only: bool
}

/// The settings `kvd serve` starts with when no flag changes them.
export Config.Default(): Config {
    return Config(max_key: 256, max_value: 65536, compact_after: 1000, slow_micros: 250_000, max_clients: 1000, idle_millis: 0, request_millis: 0, admin: false, admin_only: false)
}

/// What happened, sent by any task to the one that writes the log.
export enum Event {
    Connected(id: u64)
    Disconnected(id: u64, commands: int)
    Refused(id: u64)
    AcceptFailed(cause: net.Error)
    Partial(id: u64, bytes: int)
    Dropped(id: u64, cause: ReadError)
    TooLong(id: u64)
    Slow(id: u64, micros: i64)
    Rejected(id: u64, cause: wal.Error)
    Compacted(records: int, bytes: i64, generation: u64)
    CompactFailed(cause: wal.Error)
    Crashed(change: int)
    Stopping(clients: int)
    IdleTimeout(id: u64)
    RequestTimeout(id: u64)
    AdminConnected(id: u64)
    OptionFailed(id: u64, cause: net.Error)
}

/// One connection's entry in the client table.
struct Session {
    name: HeapText
    commands: int
    since: time.Instant
}

impl Free for Session {
    Free(self, a: &Alloc) {
        Session(name, ..) := self
        name.Free(a)
    }
}

/// The last compaction's numbers, for `STATS`.
export struct Compaction {
    export records: int
    export bytes: i64
    export count: int
}

/// The shared state of one server.
export linear mut view struct State {
    store: sync.RwLock[store.Store]
    wal: Mutex[wal.Wal]
    sessions: Mutex[HeapMap[u64, Session]]
    events: Chan[Event]
    wake: sync.Notify
    halt: sync.Notify
    changed: sync.Notify
    last: Mutex[Compaction]
    open: atomic.AtomicInt
    accepted: atomic.AtomicInt
    commands: atomic.AtomicInt
    changes: atomic.AtomicInt
    failures: atomic.AtomicInt
    busy_nanos: atomic.AtomicInt
    pending: atomic.AtomicInt
    stopping: atomic.AtomicInt
    down: atomic.AtomicInt
}

/// Puts a recovered store and its open log behind their locks. Allocates
/// the event channel, room for `clients` connections' events and more.
export State.New(a: &Alloc, s: store.Store, w: wal.Wal, clients: int): OutOfMemory!State from a, s, w {
    events := try Chan[Event].New(a, 4 * clients + 256)
    return State(
        store: sync.RwLock.New(s),
        wal: Mutex.New(w),
        sessions: Mutex.New(HeapMap[u64, Session].New(a)),
        events: events,
        wake: sync.Notify.New(),
        halt: sync.Notify.New(),
        changed: sync.Notify.New(),
        last: Mutex.New(Compaction(records: 0, bytes: 0, count: 0)),
        open: atomic.AtomicInt.New(0),
        accepted: atomic.AtomicInt.New(0),
        commands: atomic.AtomicInt.New(0),
        changes: atomic.AtomicInt.New(0),
        failures: atomic.AtomicInt.New(0),
        busy_nanos: atomic.AtomicInt.New(0),
        pending: atomic.AtomicInt.New(0),
        stopping: atomic.AtomicInt.New(0),
        down: atomic.AtomicInt.New(0),
    )
}

/// Reports `ev` to the log task, even while the server stops, so a client
/// that leaves during shutdown is logged; the channel has room for every
/// connection's events. A closed channel drops it.
export State.Report(&self, io: Io, ev: Event) {
    self.events.Send(io.NoCancel(), ev) else {}
}

/// Whether a simulated crash stopped the server.
export State.Crashed(&self): bool {
    return self.down.Load() != 0
}

/// Whether the server has begun to stop.
export State.IsStopping(&self): bool {
    return self.stopping.Load() != 0
}

/// Starts a graceful shutdown, once. A task that holds the server's scope
/// then cancels it; `Watch` cancels it for the tasks that do not.
export State.Stop(&self, io: Io) {
    if self.stopping.Add(1) == 1 {
        self.Report(io, Event.Stopping(clients: Cast[int](self.open.Load())))
    }
    self.wake.NotifyAll()
    self.halt.NotifyOne()
}

/// What is left when the server's tasks have all ended.
export struct Remains {
    export store: store.Store
    export wal: wal.Wal
    export crashed: bool
}

/// Takes the state apart once every task has ended. Every connection has
/// left the client table by then, so it is freed empty.
export State.Finish(self, a: &Alloc): Remains from self {
    State(store, wal, sessions, events, down, wake: _, halt: _, changed: _, last: _, open: _, accepted: _, commands: _, changes: _, failures: _, busy_nanos: _, pending: _, stopping: _) := self
    sessions.Into().FreeEmpty(a)
    events.Free(a)
    return Remains(store: store.Into(), wal: wal.Into(), crashed: down.Load() != 0)
}
