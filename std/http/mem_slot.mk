// Why: an Exchange's Line lives in the caller's arena and its pooled Slot
// inside the Client, and no borrow can name the one from the other, so the
// Line holds its Slot by a raw pointer; this file holds that pointer's one
// unsafe use, the Line's placement in the arena, and the argument that both
// are sound.
//
// What: SlotOf, the Slot a Line's raw.Atomic[*Slot] names; PutLine, the
// Line's place in the caller's arena.
//
// How: the pointer is set by Client.Send and cleared by TakeConn on every
// way out of the Line's use, both under the Line's lock, and in between the
// client's shared loan keeps the Slot alive and in place. The argument
// follows.

import std.core
import std.raw

// A reference from the Line to the Slot would be a store of the client's
// loan into the caller's arena, which no clause on a receiver can declare
// (`&self` takes no `into`, E0101), and the direct write rule refuses it in
// Client.Send. So the Line holds its slot by raw pointer, in a
// raw.Atomic[*Slot] (null for none), which holds no loan and is Send and
// Share as an atomic word is, as `&Slot` is, since Slot is Share. This file
// answers for that pointer.
//
// Why nothing reads a dead Slot through it:
//   - Only Client.Send stores it, into the Line it made in that call
//     (NewLineIn), and the slot is one of `&self.slots`, borrowed by Send's
//     `&self`.
//   - Only TakeConn clears it, and TakeConn runs on every way out of the
//     Line's use: Send's failure path (AbandonLine, before a retry or the
//     fail), Exchange.Close and Exchange.Abandon. Every other exit of Send
//     after the store returns the Exchange, and Client.Exchange fails only
//     into Send's handler. An Exchange is linear, so it ends only in Close
//     or Abandon (the only two destructurings of one), and a panic ends the
//     process.
//   - So the pointer is not null only while Send runs or the Exchange is
//     not yet ended, and both hold the client's shared loan (the Exchange
//     is `from a, self`): the client is not moved, closed or freed then.
//     The slots are inline in the Client, so the Slot's address holds for
//     that time.
//   - Every load of the pointer and its clear happen under the Line's lock
//     (LineConnRead under LineFill's, LineWrite, TakeConn), and a load that
//     finds null uses no slot. So whoever reads the Line, Send's lm, the
//     Exchange's line field, or the raw body Reader in the arena, even one
//     a decoder's wrap (a user's compress.Content) kept in its result,
//     reads the Slot only before the clear, while the client lives, or
//     finds null (and no connection: TakeConn took it) after.
//
// What goes through it: SlotOf's reference is used only to lock the slot's
// state, read or write on its connection, and Release, which stores an
// Instant and takes the connection out. None of these stores a loan, so a
// reference that holds none hides no store from the checker (such a body
// does only what a checked body with the same signature may do).

// SlotOf is the Slot a Line's pointer p names, p not null. Its result holds no
// loan; the comment at the top of this file says why the Slot is alive and in
// place wherever it is called.
SlotOf(p: *Slot): &Slot {
    unsafe "a Line names its Slot only from Client.Send's store to TakeConn's clear, while Send or the Exchange holds the client's loan, so the Slot inside the Client is alive and does not move" {
        return &*p
    }
}

// PutLine moves m into fresh memory of a, as Arena.Put does, and returns a
// shared reference to it. Arena.Put refuses a linear value, which the arena
// would free unconsumed, and a Line is linear for its connection. Here
// nothing linear is lost: the Line's one linear part is its connection, and
// TakeConn takes it out on every way out of the Line's use (the argument
// above: Send's failure path, Exchange.Close and Exchange.Abandon), so the
// arena frees a Line whose connection is none. The memory is bumped from a
// through its Alloc, as Arena.Put bumps it, and not zero-filled. OutOfMemory
// when the arena cannot grow, and then the caller still owns m.
PutLine(a: &Arena, m: Mutex[Line] into a): OutOfMemory!&Mutex[Line] from a {
    b := core.AllocBytes(a.Alloc(), SizeOf[Mutex[Line]](), AlignOf[Mutex[Line]]())
    if raw.IsNull(b) {
        fail OutOfMemory
    }
    unsafe "b is fresh arena memory of one Mutex[Line]'s size and alignment, alive while a is; m's connection is taken out by TakeConn before the arena ends (see above)" {
        p := raw.PtrCast[u8, Mutex[Line]](b)
        raw.Write(p, m)
        return &*p
    }
}
