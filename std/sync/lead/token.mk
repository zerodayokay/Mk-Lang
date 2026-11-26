// Why: a multiplexed connection has one read token and any number of waiting
// tasks, and the state must sit inside the connection's own lock without
// allocating on Enter or Leave.
//
// What: Step (Done, Lead, Wait) and Token (New, Grow, Free, Enter, Leave,
// Reading, Waiting).
//
// How: Token keeps an intrusive doubly-linked FIFO inside a HeapList of
// per-party slots, so Enter, Leave and unlinking a cancelled waiter are all
// worst-case O(1) with zero allocation after New or Grow.

import std.time

/// What a party does next on a Driven connection.
export enum Step {
    /// The party's condition or the connection's fault holds.
    Done
    /// The read token is the party's: read once, with this deadline.
    Lead(deadline: ?time.Instant)
    /// Wait on the party's Notify until this deadline.
    Wait(deadline: ?time.Instant)
}

const StateIdle: u8 = 0
const StateQueued: u8 = 1
const StateLeader: u8 = 2

struct PartySlot {
    state: u8
    prev: int
    next: int
}

/// The read token and the FIFO of waiting parties of one connection, kept
/// inside the connection's state under the connection's own lock. Linear:
/// release it with Free.
export linear view struct Token {
    reading: bool
    leader: int
    q_head: int
    q_tail: int
    waiting: int
    slots: HeapList[PartySlot]
}

/// A read token sized for `parties` parties, allocated from `a`. Negative
/// counts are treated as 0. Fails with OutOfMemory. O(parties); allocates one
/// buffer of `parties` slot records from `a`.
export Token.New(a: &Alloc, parties: int): OutOfMemory!Token {
    n := if parties < 0 { 0 } else { parties }
    var slots := HeapList[PartySlot].New(a)
    onerr slots.Free(a)
    for _ in 0..n {
        try slots.Push(a, PartySlot(state: StateIdle, prev: -1, next: -1))
    }
    return Token(reading: false, leader: -1, q_head: -1, q_tail: -1, waiting: 0, slots: slots)
}

/// Grows the token's party table to at least `parties` entries from `a`. Does
/// nothing when it already holds at least `parties`. Fails with OutOfMemory,
/// leaving existing entries intact. O(parties - len); grows `slots` in `a`.
export Token.Grow(mut self, a: &Alloc, parties: int): OutOfMemory!void {
    for self.slots.Len() < parties {
        try self.slots.Push(a, PartySlot(state: StateIdle, prev: -1, next: -1))
    }
}

/// Releases the token's party table back to `a`. O(1); frees nothing else.
export Token.Free(self, a: &Alloc) {
    Token(slots, ..) := self
    slots.Free(a)
}

// Unlink removes queued party p from the FIFO and sets its state to StateIdle.
Token.Unlink(mut self, p: int) {
    s := self.slots.ItemsMut()
    pv := s[p].prev
    nx := s[p].next
    if pv >= 0 {
        s[pv].next = nx
    } else {
        self.q_head = nx
    }
    if nx >= 0 {
        s[nx].prev = pv
    } else {
        self.q_tail = pv
    }
    s[p].prev = -1
    s[p].next = -1
    s[p].state = StateIdle
    self.waiting -= 1
}

// Pop removes and returns the oldest queued party, or -1 when empty.
Token.Pop(mut self): int {
    h := self.q_head
    if h < 0 {
        return -1
    }
    self.Unlink(h)
    return h
}

/// Party `p` asks for the read token: returns true when the token was free
/// (or already held by `p`) and is now `p`'s, or false when `p` is (or
/// already was) queued in FIFO order. Panics (code 2) if `p` is out of
/// range. O(1); allocates nothing.
export Token.Enter(mut self, p: int): bool {
    s := self.slots.ItemsMut()
    cur := s[p].state
    if !self.reading {
        if cur == StateQueued {
            self.Unlink(p)
        }
        s2 := self.slots.ItemsMut()
        s2[p].state = StateLeader
        self.reading = true
        self.leader = p
        return true
    }
    if cur == StateLeader {
        return true
    }
    if cur == StateQueued {
        return false
    }
    tail := self.q_tail
    s[p].state = StateQueued
    s[p].prev = tail
    s[p].next = -1
    if tail >= 0 {
        s[tail].next = p
    } else {
        self.q_head = p
    }
    self.q_tail = p
    self.waiting += 1
    return false
}

/// Party `p` leaves (satisfied, cancelled, or giving the token up). Returns
/// the next queued party to wake when the token is now free and a party
/// waits, or -1. Panics (code 2) if `p` is out of range. O(1); allocates
/// nothing.
export Token.Leave(mut self, p: int): int {
    s := self.slots.ItemsMut()
    cur := s[p].state
    if cur == StateLeader {
        s[p].state = StateIdle
        self.reading = false
        self.leader = -1
        return self.Pop()
    }
    if cur == StateQueued {
        self.Unlink(p)
        if !self.reading {
            return self.Pop()
        }
        return -1
    }
    if !self.reading {
        return self.Pop()
    }
    return -1
}

/// Reports whether the read token is currently held. O(1); allocates nothing.
export Token.Reading(&self): bool {
    return self.reading
}

/// Returns the number of parties currently queued waiting for the token.
/// O(1); allocates nothing.
export Token.Waiting(&self): int {
    return self.waiting
}
