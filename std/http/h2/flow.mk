// Why: HTTP/2 flow control (RFC 9113 5.2, 6.9) tracks connection and stream
// windows, returns connection credit on buffering and stream credit at half the
// window consumed, and admits server request bodies in FIFO order so parallel
// uploads cannot deadlock.
//
// What: flow-control constants, Window, Credit, and Admission.
//
// How: Window guards every addition and adjustment against [0, 2^31-1] and
// tracks negative send windows after INITIAL_WINDOW_SIZE reductions; Credit
// separates immediate buffering credit from half-window consumption credit and
// never emits 0; Admission keeps a strict FIFO HeapList ring so an older
// reservation never starves behind smaller later ones.

import std.core

/// RFC 9113 6.9.1: maximum flow-control window size (2^31 - 1).
export const MaxWindow: int = 0x7FFF_FFFF

/// RFC 9113 6.9.2: initial flow-control window size for new streams and the
/// connection before WINDOW_UPDATE.
export const InitialWindow: int = 65535

/// Default HTTP/2 connection receive window (16 MiB).
export const ConnWindow: int = 16777216

/// Server initial stream receive window before body admission (16 KiB).
export const ServerInitialStreamWindow: int = 16384

/// Minimum window space before sending DATA (4 KiB), unless DribbleWait has
/// elapsed or all pending data fits.
export const MinDataUnit: int = 4096

/// Maximum time a window-blocked stream waits for MinDataUnit before sending
/// a smaller DATA frame (1 second).
export const DribbleWait: core.Duration = core.DurationOf(1_000_000_000)

/// One HTTP/2 flow-control window (RFC 9113 5.2, 6.9): what this side may
/// still send, or what the peer may still send to it. Signed so a send window
/// can go negative after SETTINGS_INITIAL_WINDOW_SIZE shrinks.
export struct Window {
    avail: int
}

/// A window initialized to initial octets. O(1); allocates nothing.
export Window.New(initial: int): Window {
    return Window(avail: initial)
}

/// Adds increment from a WINDOW_UPDATE frame. Errors: Violation(ProtocolError,
/// 0) when increment <= 0 (RFC 9113 6.9); Violation(FlowControlError, 0) when
/// adding increment would exceed MaxWindow (RFC 9113 6.9.1). O(1); allocates
/// nothing.
export Window.Grow(mut self, increment: int): Violation!void {
    if increment <= 0 {
        fail Violation(code: ProtocolError, stream: 0)
    }
    if increment > MaxWindow - self.avail {
        fail Violation(code: FlowControlError, stream: 0)
    }
    self.avail += increment
}

/// Adjusts the window by delta (new_initial - old_initial) when
/// SETTINGS_INITIAL_WINDOW_SIZE changes (RFC 9113 6.9.2); the window may go
/// negative. Errors: Violation(FlowControlError, 0) if delta pushes the window
/// above MaxWindow. O(1); allocates nothing.
export Window.Adjust(mut self, delta: int): Violation!void {
    if delta > 0 && delta > MaxWindow - self.avail {
        fail Violation(code: FlowControlError, stream: 0)
    }
    self.avail += delta
}

/// Consumes n octets of DATA against the window. Errors:
/// Violation(FlowControlError, 0) when n < 0 or n exceeds Avail (RFC 9113
/// 6.9). O(1); allocates nothing.
export Window.Take(mut self, n: int): Violation!void {
    if n < 0 || (n > 0 && n > self.avail) {
        fail Violation(code: FlowControlError, stream: 0)
    }
    self.avail -= n
}

/// Available octets in the window (may be negative). O(1); allocates nothing.
export Window.Avail(&self): int {
    return self.avail
}

/// Receive-side credit owed: connection credit is returned immediately on
/// buffering; stream credit is returned once at least half of window is
/// consumed. Never returns a zero increment when credit is owed.
export struct Credit {
    window: int
    buffered: int
    consumed: int
}

/// A credit tracker for a receive window of window octets. O(1); allocates
/// nothing.
export Credit.New(window: int): Credit {
    w := if window < 0 { 0 } else { window }
    return Credit(window: w, buffered: 0, consumed: 0)
}

/// Updates the target window size used for half-window stream credit. O(1);
/// allocates nothing.
export Credit.SetWindow(mut self, window: int) {
    self.window = if window < 0 { 0 } else { window }
}

/// Records n DATA octets (payload plus padding) buffered or discarded on the
/// connection receive window; owed immediately on the next Owed call. Ignores
/// n <= 0. O(1); allocates nothing.
export Credit.Buffered(mut self, n: int) {
    if n > 0 {
        self.buffered += n
    }
}

/// Records n DATA octets consumed by the application on a stream; owed once
/// accumulated consumed bytes reach at least half of window. Ignores n <= 0.
/// O(1); allocates nothing.
export Credit.Consumed(mut self, n: int) {
    if n > 0 {
        self.consumed += n
    }
}

/// Returns the positive WINDOW_UPDATE increment currently owed (clamped to
/// MaxWindow) and clears that owed amount, or 0 when nothing is owed yet.
/// Never returns 0 while buffered > 0 or consumed >= max(window / 2, 1).
/// O(1); allocates nothing.
export Credit.Owed(mut self): int {
    if self.buffered > 0 {
        grant := if self.buffered > MaxWindow { MaxWindow } else { self.buffered }
        self.buffered -= grant
        return grant
    }
    half := if self.window > 1 { self.window / 2 } else { 1 }
    if self.consumed >= half {
        grant := if self.consumed > MaxWindow { MaxWindow } else { self.consumed }
        self.consumed -= grant
        return grant
    }
    return 0
}

// One queued stream waiting for body-budget admission.
struct AdmitWait {
    stream: u32
    reserve: int
}

/// The server's per-connection body admission budget: admits requests in
/// strict FIFO order so no waiting stream can starve when budget returns.
/// Linear: owns a HeapList queue; release with Free.
export linear view struct Admission {
    budget: int
    used: int
    queue: HeapList[AdmitWait]
    head: int
}

/// An admission tracker with total budget octets, bound to allocator a. O(1);
/// allocates nothing until a stream waits.
export Admission.New(a: &Alloc, budget: int): Admission {
    b := if budget < 0 { 0 } else { budget }
    return Admission(budget: b, used: 0, queue: HeapList[AdmitWait].New(a), head: 0)
}

/// Releases the admission queue's buffer. O(1).
export Admission.Free(self, a: &Alloc) {
    Admission(queue, ..) := self
    queue.Free(a)
}

/// Requests reserve octets of body budget for stream. Returns true if admitted
/// immediately (no stream is waiting ahead and used + reserve <= budget);
/// otherwise queues stream at the FIFO tail and returns false. Fails with
/// OutOfMemory if the queue cannot grow. O(1) amortised.
export Admission.Ask(mut self, a: &Alloc, stream: u32, reserve: int): OutOfMemory!bool {
    r := if reserve < 0 { 0 } else if reserve > self.budget { self.budget } else { reserve }
    if self.head == self.queue.Len() && r <= self.budget - self.used {
        self.used += r
        return true
    }
    try self.queue.Push(a, AdmitWait(stream: stream, reserve: r))
    return false
}

/// Returns reserve octets from a finished stream, then admits waiting streams
/// from the FIFO head in order while their reservation fits in the freed
/// budget and out has space; writes newly admitted stream ids into out and
/// returns the count written. O(admitted + compaction when head > len/2);
/// allocates nothing.
export Admission.Return(mut self, reserve: int, admitted: mut []u32): int {
    r := if reserve < 0 { 0 } else { reserve }
    self.used = if r >= self.used { 0 } else { self.used - r }
    var count := 0
    for self.head < self.queue.Len() && count < admitted.Len() {
        w := self.queue[self.head]
        if w.reserve > self.budget - self.used {
            break
        }
        self.used += w.reserve
        admitted[count] = w.stream
        count += 1
        self.head += 1
    }
    self.Compact()
    return count
}

/// Removes a cancelled or reset unadmitted stream from the waiting FIFO if
/// present. O(waiting); allocates nothing.
export Admission.Cancel(mut self, stream: u32) {
    n := self.queue.Len()
    for i in self.head..n {
        if self.queue[i].stream == stream {
            _ = self.queue.Remove(i)
            break
        }
    }
    if self.head >= self.queue.Len() {
        self.queue.Clear()
        self.head = 0
    }
}

/// Current reserved octets in use. O(1).
export Admission.Used(&self): int {
    return self.used
}

/// Number of streams currently waiting in the FIFO. O(1).
export Admission.Waiting(&self): int {
    return self.queue.Len() - self.head
}

// Admission.Compact shifts the active tail to index 0 when head passes half
// of the backing array or the queue drains, keeping memory bounded.
Admission.Compact(mut self) {
    n := self.queue.Len()
    if self.head >= n {
        self.queue.Clear()
        self.head = 0
        return
    }
    if self.head > 16 && self.head * 2 >= n {
        rem := n - self.head
        items := self.queue.ItemsMut()
        for i in 0..rem {
            items[i] = items[self.head + i]
        }
        for self.queue.Len() > rem {
            _ = self.queue.Pop()
        }
        self.head = 0
    }
}
