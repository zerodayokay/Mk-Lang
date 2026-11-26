// Why: an HTTP/2 or HTTP/3 Exchange lives in the caller's arena while its
// pooled MuxSlot lives inline inside the Client, and Box[h2.Conn] must sit
// behind raw.Atomic[*Box[h2.Conn]] so MuxSlot can reclaim it via 64-bit word
// CAS without storing linear fields inside an atomic word; this file holds the
// minimal unsafe helpers and soundness proofs.
//
// What: MuxSlotOf, AllocMuxBox, TakeMuxBox, and MuxConnOf.
//
// How: MuxSlotOf converts *MuxSlot to &MuxSlot while Send or Exchange holds a
// shared loan on &Client; AllocMuxBox and TakeMuxBox move Box[h2.Conn] to and
// from Alloc-backed storage; MuxConnOf dereferences *Box[h2.Conn] only while a
// MuxSlot reference (refs >= 1) prevents reclamation.

import std.http.h2
import std.raw

// MuxSlotOf converts a non-null *MuxSlot inside Client into &MuxSlot.
// Sound because Client is alive and immovable while any active operation or
// Exchange holds &Client (from a, self).
MuxSlotOf(p: *MuxSlot): &MuxSlot {
    unsafe "Send, Drive, and Exchange hold &Client so the inline MuxSlot in Client stays live and immovable until ReleaseMuxSlot completes" {
        return &*p
    }
}

// AllocMuxBox allocates space for b on a and moves b into it, returning the
// raw pointer. On OutOfMemory, b remains owned by the caller.
AllocMuxBox(a: &Alloc, b: Box[h2.Conn]): OutOfMemory!*Box[h2.Conn] {
    sz := SizeOf[Box[h2.Conn]]()
    unsafe "sz bytes with Box[h2.Conn] alignment is freshly allocated from a and initialized by moving b into it; on OOM, b stays with caller" {
        raw_p := a.Bytes(sz, AlignOf[Box[h2.Conn]]())
        if raw.IsNull(raw_p) {
            fail OutOfMemory
        }
        p := raw.PtrCast[u8, Box[h2.Conn]](raw_p)
        raw.Write(p, b)
        return p
    }
}

// TakeMuxBox recovers Box[h2.Conn] from p and returns its outer pointer cell
// to a. Must be called at most once after the atomic pointer swap returns p.
TakeMuxBox(a: &Alloc, p: *Box[h2.Conn]): Box[h2.Conn] {
    unsafe "p was created by AllocMuxBox on a and exclusively taken out by atomic Swap to null" {
        b := raw.Read(p)
        a.FreeBytes(raw.PtrCast[Box[h2.Conn], u8](p), SizeOf[Box[h2.Conn]](), AlignOf[Box[h2.Conn]]())
        return b
    }
}

// MuxConnOf borrows &h2.Conn from p while the caller holds a live MuxSlot
// reference (refs >= 1) or owns the unpooled H2Stream.
MuxConnOf(p: *Box[h2.Conn]): &h2.Conn {
    unsafe "caller holds a live MuxSlot reference (preventing Dead refs 1->0 reclamation) or exclusive unpooled ownership" {
        return (*p).Ref()
    }
}
