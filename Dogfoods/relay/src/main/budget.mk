// Why: a limit on streams per connection still lets independent connections
// occupy many handlers. Limit active forwards across all of them.
//
// What: Budget holds a shared admission gate and application payload counts.
//
// How: acquire one permit before sending to the origin. Keep the permit until
// the handler returns. Count the request body and copies made by Response.Body
// while the permit is held. HTTP's own buffers and queued request bodies are
// subject to the separate limits in ClientSettings and ServeSettings.

import std.sync

struct Budget {
    permits: sync.Semaphore
    usage: Mutex[PayloadUsage]
}

struct PayloadUsage {
    active: int
    bytes: int
    peak_bytes: int
}

NewBudget(): Budget {
    return Budget(
        permits: sync.Semaphore.New(MaxStreams),
        usage: Mutex[PayloadUsage].New(PayloadUsage(active: 0, bytes: 0, peak_bytes: 0)),
    )
}

Budget.Start(&self, io: Io, request_bytes: int): Canceled!void {
    try self.permits.Acquire(io, 1)
    var g := self.usage.Lock(io.NoCancel()).Must()
    u := g.Mut()
    u.active += 1
    u.bytes += request_bytes
    if u.bytes > u.peak_bytes {
        u.peak_bytes = u.bytes
    }
    g.Unlock()
}

Budget.RecordCopy(&self, io: Io, n: int) {
    var g := self.usage.Lock(io.NoCancel()).Must()
    u := g.Mut()
    u.bytes += n
    if u.bytes > u.peak_bytes {
        u.peak_bytes = u.bytes
    }
    g.Unlock()
}

Budget.Finish(&self, io: Io, retained_bytes: int) {
    var g := self.usage.Lock(io.NoCancel()).Must()
    u := g.Mut()
    u.active -= 1
    u.bytes -= retained_bytes
    g.Unlock()
    self.permits.Release(1)
}

Budget.Read(&self, io: Io): PayloadUsage {
    g := self.usage.Lock(io.NoCancel()).Must()
    result := *g.Ref()
    g.Unlock()
    return result
}

// Each active handler can retain up to one request and one response body.
// The copy buffers are two fixed blocks and contribute no retained data.
const MaxRetained = MaxStreams * MaxBody * 2
