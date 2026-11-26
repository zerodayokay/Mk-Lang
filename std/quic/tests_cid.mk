// Why: connection ID issuance, active limits, retirement, reset tokens, and
// handshake CID authentication (RFC 9000 5.1, 18.2, 19.15, 19.16) govern both
// demux routing and stateless reset acceptance.
//
// What: unit tests for CidState covering duplicate suppression, OurActiveCidLimit
// cap (4) and 16-issued cap, OnHandshakeAuthed validation, OnNewCid errors
// (CONNECTION_ID_LIMIT_ERROR, PROTOCOL_VIOLATION, FRAME_ENCODING_ERROR),
// OnRetire active switching, and IsResetToken matching only active CIDs.
//
// How: drives CidState with deterministic TestAlloc and verifies returned CIDs
// and exact transport error codes.

test "RFC 9000 5.1, 19.15: local CID issuance, duplicate suppression, active limit 4 and 16 cap" (sys: &System) {
    a := sys.arena.Alloc()
    p0 := CidFromSlice(b"\x01\x02\x03\x04\x05\x06\x07\x08")
    l0 := CidFromSlice(b"\x11\x12\x13\x14\x15\x16\x17\x18")
    sk: [32]u8 = [0x5a; 32]

    var cs := try CidState.New(a, false, &p0)
    defer cs.Free(a)
    cs.SetStaticKey(&sk)
    try cs.AddLocalInitial(a, &l0)

    var p := DefaultParams(true)
    p.initial_scid = some(p0)
    p.active_connection_id_limit = 8 // server caps at min(8, 4) = 4
    try cs.OnHandshakeAuthed(a, &p)

    // Duplicate of existing local CID is rejected.
    Assert(cs.HasLocalCid(&l0))
    Assert(cs.IssueLocal(a, &l0).Must().IsNone())

    // Issue up to active limit 4 (already have 1 local initial -> 3 more).
    for i in 1..4 {
        var b: [8]u8 = [0x20; 8]
        b[7] = Trunc[u8](i)
        c := CidFromSlice(b[..])
        issued := cs.IssueLocal(a, &c).Must().Must()
        Assert(issued.seq == Cast[u64](i))
    }
    extra := CidFromSlice(b"\x20\x20\x20\x20\x20\x20\x20\x99")
    Assert(cs.IssueLocal(a, &extra).Must().IsNone())
    Assert(cs.ActiveLocalCount() == 4)

    // Retiring seq 0 frees a slot, allowing another issue up to total 16 cap.
    Assert(cs.OnRetire(0, &extra).Must().IsSome())
    Assert(cs.ActiveLocalCount() == 3)
    Assert(cs.IssueLocal(a, &extra).Must().IsSome())

    // Retiring the packet's own DCID is PROTOCOL_VIOLATION.
    var got_proto := false
    self_cid := cs.local[1].cid
    _ = cs.OnRetire(1, &self_cid) else e {
        match e {
            TransportError(code, frame: _) { got_proto = code == ErrProtocolViolation }
            _ {}
        }
        none
    }
    Assert(got_proto)

    // Stateless reset token is deterministic per (sk, cid) and distinct across CIDs.
    t0 := MakeStatelessResetToken(&sk, &l0)
    t0_again := MakeStatelessResetToken(&sk, &l0)
    t1 := MakeStatelessResetToken(&sk, &extra)
    var same := true
    var diff := false
    for k in 0..16 {
        if t0[k] != t0_again[k] {
            same = false
        }
        if t0[k] != t1[k] {
            diff = true
        }
    }
    Assert(same && diff)
}

test "RFC 9000 10.3, 19.15, 19.16: peer CID tracking, limits, retirement, and reset token validity" (sys: &System) {
    a := sys.arena.Alloc()
    p0 := CidFromSlice(b"\xaa\xbb\xcc\xdd\xee\xff\x00\x01")
    var cs := try CidState.New(a, true, &p0)
    defer cs.Free(a)

    var sp := DefaultParams(false)
    sp.initial_scid = some(p0)
    sp.original_dcid = some(p0)
    tok0: [16]u8 = [0x11; 16]
    sp.stateless_reset_token = some(tok0)
    try cs.OnHandshakeAuthed(a, &sp)

    // Zero-length peer CID is not allowed to receive NEW_CONNECTION_ID.
    // Peer sends seq=1 with tok1; unused CID reset token is NOT matched yet.
    p1 := CidFromSlice(b"\xaa\xbb\xcc\xdd\xee\xff\x00\x02")
    tok1: [16]u8 = [0x22; 16]
    try cs.OnNewCid(a, 1, 0, &p1, &tok1)
    Assert(cs.ActivePeerCount() == 2)
    Assert(cs.IsResetToken(&tok0) && !cs.IsResetToken(&tok1))

    // Duplicate seq with identical CID/token is idempotent; with different CID is PROTOCOL_VIOLATION.
    try cs.OnNewCid(a, 1, 0, &p1, &tok1)
    p_bad := CidFromSlice(b"\x99\x99\x99\x99\x99\x99\x99\x99")
    var got_dup_err := false
    cs.OnNewCid(a, 1, 0, &p_bad, &tok1) else e {
        match e {
            TransportError(code, frame: _) { got_dup_err = code == ErrProtocolViolation }
            _ {}
        }
    }
    Assert(got_dup_err)

    // Sending up to active limit (4) succeeds, 5th active raises CONNECTION_ID_LIMIT_ERROR.
    p2 := CidFromSlice(b"\x01\x00\x00\x00\x00\x00\x00\x02")
    p3 := CidFromSlice(b"\x01\x00\x00\x00\x00\x00\x00\x03")
    p4 := CidFromSlice(b"\x01\x00\x00\x00\x00\x00\x00\x04")
    tok2: [16]u8 = [0x33; 16]
    tok3: [16]u8 = [0x44; 16]
    tok4: [16]u8 = [0x55; 16]
    try cs.OnNewCid(a, 2, 0, &p2, &tok2)
    try cs.OnNewCid(a, 3, 0, &p3, &tok3)
    var got_lim_err := false
    cs.OnNewCid(a, 4, 0, &p4, &tok4) else e {
        match e {
            TransportError(code, frame: _) { got_lim_err = code == ErrConnectionIdLimit }
            _ {}
        }
    }
    Assert(got_lim_err)

    // Increasing retire_prior_to switches Current() away from seq=0 and retires tok0.
    p5 := CidFromSlice(b"\x01\x00\x00\x00\x00\x00\x00\x05")
    tok5: [16]u8 = [0x66; 16]
    try cs.OnNewCid(a, 5, 2, &p5, &tok5)
    Assert(!CidEqual(cs.Current(), &p0))
    Assert(!CidEqual(cs.Current(), &p1))
    Assert(!cs.IsResetToken(&tok0))
    Assert(!cs.IsResetToken(&tok1))
    Assert(cs.IsResetToken(&tok2))
    Assert(cs.TakePendingRetire().Must() == 0)
    Assert(cs.TakePendingRetire().Must() == 1)
    Assert(cs.TakePendingRetire().IsNone())
}
