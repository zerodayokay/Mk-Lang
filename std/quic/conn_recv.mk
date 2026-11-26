// Why: incoming QUIC UDP datagrams carry coalesced Long and Short packets,
// Retry and Version Negotiation packets, stateless resets, and variable-length
// frames that must be decrypted, deduplicated, validated, and dispatched
// without partial state or memory leaks.
//
// What: ProcessDatagram, ProcessPacket, ApplyFrames, ProcessPostHandshakeCrypto,
// HandleAckedFrames, and HandleLostFrames.
//
// How: RFC 9000 sections 5.2, 6.2, 7.2, 8.1, 10.3, 12.2, 12.4, 13.1, 17.2 and
// RFC 9001 sections 4.1.3, 4.4, 4.6.1, 5.5, 5.7, and 6. Validates Initial and
// Handshake payloads completely before mutating state, records packets in
// AckSet only after every frame succeeds, and drops GapLimit packets unacked.

import std.net
import std.time
import std.tls

// Scales a wire ACK Delay value by peer's ack_delay_exponent (RFC 9000 19.3).
ScaleAckDelay(raw_delay: u64, exponent: u64): time.Duration {
    exp := if exponent > 20 { u64(20) } else { exponent }
    // Cap raw_delay before shifting to prevent overflow.
    max_us := u64(0x3FFF_FFFF_FFFF_FFFF) / 1000
    shifted := if exp >= 62 {
        max_us
    } else {
        limit := max_us >> exp
        if raw_delay > limit { max_us } else { raw_delay << exp }
    }
    return time.Nanos(Cast[i64](shifted) * 1000)
}

// Processes a list of newly acknowledged SentFrames across CRYPTO, Streams, and
// connection-level control frames.
HandleAckedFrames(e: mut Engine, acked: []SentFrame) {
    a := e.h.Alloc()
    for i in 0..acked.Len() {
        f := &acked[i]
        match f.kind {
            .Crypto { e.crypto.OnAcked(a, f) }
            .Stream { e.streams.OnAcked(a, f) }
            .Control(index) {
                if index >= 0 {
                    e.streams.OnAcked(a, f)
                } else {
                    match e.TakeCtrl(index) {
                        some(.HandshakeDone) { e.hs_done_acked = true }
                        _ {}
                    }
                }
            }
        }
    }
}

// Processes a list of lost SentFrames across CRYPTO, Streams, and connection
// control frames (RFC 9000 13.3).
HandleLostFrames(e: mut Engine, lost: []SentFrame) {
    a := e.h.Alloc()
    for i in 0..lost.Len() {
        f := &lost[i]
        match f.kind {
            .Crypto { e.crypto.OnLost(a, f) }
            .Stream { e.streams.OnLost(a, f) }
            .Control(index) {
                if index >= 0 {
                    e.streams.OnLost(a, f)
                } else {
                    match e.TakeCtrl(index) {
                        some(.HandshakeDone) {
                            if !e.hs_done_acked {
                                e.hs_done_pending = true
                            }
                        }
                        some(.NewCid(seq)) { e.cids.OnNewCidLost(seq) }
                        some(.RetireCid(seq)) { e.cids.OnRetireLost(a, seq) }
                        none {}
                    }
                }
            }
        }
    }
}

// Track enough frame notifications for the entire packet-number space.
// A grouped ACK may acknowledge or declare loss for several packets.
GrowFrameScratch(a: &Alloc, out: mut HeapList[SentFrame], n: int): OutOfMemory!void {
    if out.Len() >= n {
        return
    }
    empty: [64]SentFrame = [SentFrame(kind: SentKind.Control(index: 0)); 64]
    for out.Len() < n {
        left := n - out.Len()
        add := if left < empty.Len() { left } else { empty.Len() }
        try out.Extend(a, empty[0..add])
    }
}

ApplyAck(e: mut Engine, space: Space, ranges: []AckRange, delay: time.Duration, largest: i64, now: time.Instant): Error!void {
    a := e.h.Alloc()
    n := e.rec.spaces[SpaceIndex(space)].frm_len
    try GrowFrameScratch(a, mut e.ack_frames, n)
    try GrowFrameScratch(a, mut e.lost_frames, n)
    var acked := Replace(mut e.ack_frames, HeapList[SentFrame].New(a))
    var lost := Replace(mut e.lost_frames, HeapList[SentFrame].New(a))
    defer {
        Replace(mut e.ack_frames, acked).Free(a)
        Replace(mut e.lost_frames, lost).Free(a)
    }
    res := try e.rec.OnAck(space, ranges, delay, now, acked.ItemsMut(), lost.ItemsMut())
    if res.acked > 0 {
        HandleAckedFrames(e, acked.Items()[0..res.acked])
        if space == Space.App {
            e.keys.OnAck1Rtt(largest, now)
        }
    }
    if res.lost > 0 {
        HandleLostFrames(e, lost.Items()[0..res.lost])
    }
}

// Checks whether a frame is permitted in Initial or Handshake packets: only PADDING, PING, ACK, CRYPTO, CONNECTION_CLOSE 0x1c.
IsAllowedHandshakeFrame(f: &QFrame): (ok: bool, frame_type: u64) {
    return match f {
        .Padding | .Ping | .Ack | .Crypto { (ok: true, frame_type: 0) }
        .ConnectionClose(code: _, frame_type: _, reason: _, app) {
            if *app {
                (ok: false, frame_type: FrameConnectionCloseApp)
            } else {
                (ok: true, frame_type: 0)
            }
        }
        .ResetStream { (ok: false, frame_type: FrameResetStream) }
        .StopSending { (ok: false, frame_type: FrameStopSending) }
        .NewToken { (ok: false, frame_type: FrameNewToken) }
        .Stream { (ok: false, frame_type: FrameStreamBase) }
        .MaxData { (ok: false, frame_type: FrameMaxData) }
        .MaxStreamData { (ok: false, frame_type: FrameMaxStreamData) }
        .MaxStreams(bidi, max: _) { (ok: false, frame_type: if *bidi { FrameMaxStreamsBidi } else { FrameMaxStreamsUni }) }
        .DataBlocked { (ok: false, frame_type: FrameDataBlocked) }
        .StreamDataBlocked { (ok: false, frame_type: FrameStreamDataBlocked) }
        .StreamsBlocked(bidi, max: _) { (ok: false, frame_type: if *bidi { FrameStreamsBlockedBidi } else { FrameStreamsBlockedUni }) }
        .NewConnectionId { (ok: false, frame_type: FrameNewConnectionId) }
        .RetireConnectionId { (ok: false, frame_type: FrameRetireConnectionId) }
        .PathChallenge { (ok: false, frame_type: FramePathChallenge) }
        .PathResponse { (ok: false, frame_type: FramePathResponse) }
        .HandshakeDone { (ok: false, frame_type: FrameHandshakeDone) }
    }
}

// Drains 1-RTT post-handshake CRYPTO bytes and validates each message with
// tls.QuicPostMessage (RFC 9001 4.4, 4.6.1, 6).
ProcessPostHandshakeCrypto(e: mut Engine): Error!void {
    a := e.h.Alloc()
    var tmp: [2048]u8 = [0; 2048]
    for {
        n := e.crypto.Read(Space.App, mut tmp[..])
        if n == 0 {
            break
        }
        if e.post_hs_buf.Len() + n > CryptoBufferLimit {
            fail TransportError(code: ErrCryptoBufferExceeded, frame: FrameCrypto)
        }
        try e.post_hs_buf.Extend(a, tmp[0..n])
    }
    for e.post_hs_buf.Len() > 0 {
        consumed := tls.QuicPostMessage(e.is_server, e.post_hs_buf.Items()) else err_post {
            match err_post {
                tls.Protocol {
                    fail TransportError(code: ErrProtocolViolation, frame: FrameCrypto)
                }
                tls.Alert(code) {
                    fail TransportError(code: ErrCryptoBase + Cast[u64](code), frame: FrameCrypto)
                }
            }
        }
        if consumed == 0 {
            break
        }
        if consumed >= e.post_hs_buf.Len() {
            e.post_hs_buf.Clear()
        } else {
            rem := e.post_hs_buf.Len() - consumed
            items := e.post_hs_buf.ItemsMut()
            for i in 0..rem {
                items[i] = items[consumed + i]
            }
            for e.post_hs_buf.Len() > rem {
                _ = e.post_hs_buf.Pop()
            }
        }
    }
}

// Decodes and applies all frames in a decrypted packet payload. Returns whether
// the packet was ack-eliciting and whether it carried any non-probing frame.
ApplyFrames(
    e: mut Engine,
    io: Io,
    space: Space,
    pn: i64,
    pkt_dcid: &Cid,
    payload: []u8,
    from: net.Addr,
    now: time.Instant,
): Error!(ack_eliciting: bool, non_probing: bool) {
    _ = pn
    var ranges: [256]AckRange = [AckRange(lo: 0, hi: 0); 256]
    // Pre-validate Initial and Handshake payloads before any mutation so
    // an invalid later frame leaves no partial state.
    if space != Space.App {
        var vit := FrameIter.New(payload, !e.is_server)
        for {
            f := try vit.Next(mut ranges[..])
            match f {
                none { break }
                some(frame) {
                    leg := IsAllowedHandshakeFrame(&frame)
                    if !leg.ok {
                        fail TransportError(code: ErrProtocolViolation, frame: leg.frame_type)
                    }
                }
            }
        }
    }

    a := e.h.Alloc()
    var it := FrameIter.New(payload, !e.is_server)
    var ack_eliciting := false
    var non_probing := false
    peer_exp := match e.peer_params {
        some(pp) { pp.ack_delay_exponent }
        none { DefaultAckDelayExponent }
    }

    for {
        f := try it.Next(mut ranges[..])
        frame := f else {
            break
        }
        if space != Space.App {
            leg := IsAllowedHandshakeFrame(&frame)
            if !leg.ok {
                fail TransportError(code: ErrProtocolViolation, frame: leg.frame_type)
            }
        }
        match frame {
            .Padding {}
            .Ping {
                ack_eliciting = true
                non_probing = true
            }
            .Ack(largest, delay, ranges: rcount) {
                try ApplyAck(e, space, ranges[0..rcount], ScaleAckDelay(delay, peer_exp), largest, now)
                non_probing = true
            }
            .Crypto(offset, data) {
                ack_eliciting = true
                non_probing = true
                // CRYPTO frame in a previously installed level cannot extend past
                // highest offset already received at that level.
                if space == Space.Initial && e.keys.HasReadKeys(Space.Handshake) {
                    if offset + Cast[u64](data.Len()) > e.crypto.MaxOffset(Space.Initial) {
                        fail TransportError(code: ErrProtocolViolation, frame: FrameCrypto)
                    }
                } else if space == Space.Handshake && e.keys.HasReadKeys(Space.App) {
                    if offset + Cast[u64](data.Len()) > e.crypto.MaxOffset(Space.Handshake) {
                        fail TransportError(code: ErrProtocolViolation, frame: FrameCrypto)
                    }
                }
                try e.crypto.OnCrypto(a, space, offset, data)
                if space == Space.App && (e.hs_state == HsComplete || e.hs_state == HsConfirmed) {
                    try ProcessPostHandshakeCrypto(e)
                }
            }
            .ConnectionClose(code, frame_type, reason, app) {
                _ = frame_type
                _ = reason
                e.close.OnPeerClose(code, app, now, e.rec.Pto())
                e.hs_net_err = some(net.MuxClosed(code: code, app: app, peer: true))
                e.RecomputeTimer(now)
                non_probing = true
            }
            .NewToken {
                if e.is_server {
                    fail TransportError(code: ErrProtocolViolation, frame: FrameNewToken)
                }
                ack_eliciting = true
                non_probing = true
            }
            .HandshakeDone {
                if e.is_server {
                    fail TransportError(code: ErrProtocolViolation, frame: FrameHandshakeDone)
                }
                ack_eliciting = true
                non_probing = true
                if e.hs_state != HsConfirmed {
                    e.hs_state = HsConfirmed
                    e.keys.SetHandshakeConfirmed()
                    e.keys.DiscardHandshake()
                    e.rec.Discard(Space.Handshake)
                    e.crypto.Discard(Space.Handshake)
                    e.acks.ClearSpace(Space.Handshake)
                    e.rec.SetHandshakeConfirmed(true, now)
                }
            }
            .Stream(id, offset, data, fin) {
                ack_eliciting = true
                non_probing = true
                try e.streams.OnStream(a, id, offset, data, fin)
            }
            .ResetStream(id, code, final_size) {
                ack_eliciting = true
                non_probing = true
                try e.streams.OnReset(a, id, code, final_size)
            }
            .StopSending(id, code) {
                ack_eliciting = true
                non_probing = true
                try e.streams.OnStop(a, id, code)
            }
            .MaxData(max) {
                ack_eliciting = true
                non_probing = true
                e.streams.OnMaxData(max)
            }
            .MaxStreamData(id, max) {
                ack_eliciting = true
                non_probing = true
                try e.streams.OnMaxStreamData(a, id, max)
            }
            .MaxStreams(bidi, max) {
                ack_eliciting = true
                non_probing = true
                try e.streams.OnMaxStreams(bidi, max)
            }
            .DataBlocked(max) {
                ack_eliciting = true
                non_probing = true
                e.streams.OnDataBlocked(max)
            }
            .StreamDataBlocked(id, max) {
                ack_eliciting = true
                non_probing = true
                try e.streams.OnStreamDataBlocked(a, id, max)
            }
            .StreamsBlocked(bidi, max) {
                ack_eliciting = true
                non_probing = true
                e.streams.OnStreamsBlocked(bidi, max)
            }
            .NewConnectionId(seq, retire_prior, cid, token) {
                ack_eliciting = true
                try e.cids.OnNewCid(a, seq, retire_prior, &cid, &token)
            }
            .RetireConnectionId(seq) {
                ack_eliciting = true
                non_probing = true
                if ret_cid := try e.cids.OnRetire(seq, pkt_dcid) {
                    try e.pending_retire_ep.Push(a, ret_cid)
                    e.need_spare_cids = true
                }
            }
            .PathChallenge(data) {
                ack_eliciting = true
                e.path.QueueChallengeResponse(&data, from)
            }
            .PathResponse(data) {
                ack_eliciting = true
                pres := e.path.OnResponse(io.NoCancel(), &data, e.rec.Pto(), now)
                if pres.validated {
                    e.rec.OnPathValidated(pres.ip_changed, now)
                    e.close.SetValidatedAddr(e.path.ValidatedAddr())
                }
            }
        }
    }
    return (ack_eliciting: ack_eliciting, non_probing: non_probing)
}

// Checks trailing 16 bytes of an unassociated/undecryptable datagram against
// used peer stateless reset tokens (RFC 9000 10.3.1).
CheckStatelessReset(e: mut Engine, dgram: []u8, now: time.Instant): bool {
    if dgram.Len() < MinShortPacketLen {
        return false
    }
    var tail: [16]u8 = [0; 16]
    (mut tail[..]).CopyFrom(dgram[dgram.Len() - 16..dgram.Len()])
    if e.cids.IsResetToken(&tail) {
        e.close.OnPeerClose(0, false, now, e.rec.Pto())
        e.hs_net_err = some(net.MuxClosed(code: 0, app: false, peer: true))
        e.RecomputeTimer(now)
        return true
    }
    return false
}

// Checks whether a Version Negotiation version list includes Version1.
VnHasVersion1(versions: []u8): bool {
    var i := 0
    for i + 4 <= versions.Len() {
        if ReadBe32(versions, i) == Version1 {
            return true
        }
        i += 4
    }
    return false
}

// Processes a full received UDP datagram (potentially holding coalesced QUIC
// packets) under the Engine lock. O(dgram.Len()).
ProcessDatagram(e: mut Engine, io: Io, dgram: mut []u8, from: net.Addr, now: time.Instant) {
    if e.abandoned || e.pn_exhausted {
        return
    }
    if e.is_server {
        e.amp_rx_bytes += i64(dgram.Len())
        if e.rec.server_amp_blocked {
            ApplyRecoveryEvent(e, now, true) else {
                e.FailTransport(ErrInternal, 0, now)
                return
            }
        } else {
            var empty: [0]SentFrame = []
            _ = e.rec.OnDatagramReceived(now, mut empty[..])
        }
    }
    if e.close.phase == ClosePhase.Draining {
        return
    }
    if e.close.phase == ClosePhase.Closing {
        if e.close.Answer(&from, dgram.Len(), 64) {
            e.close_reply_owed = true
        }
        return
    }

    var pos := 0
    var first_pkt := true
    var first_dcid := Cid(b: [0; MaxCidLen], n: 0)
    var plain: [1500]u8 = [0; 1500]

    for pos < dgram.Len() {
        parsed := ParsePacket(dgram[pos..], LocalCidLen) else {
            if first_pkt {
                _ = CheckStatelessReset(e, dgram[..], now)
            }
            break
        }
        was_first := first_pkt
        first_pkt = false
        match parsed {
            .VersionNeg(dcid, scid, versions) {
                if e.is_server || !was_first || e.any_pkt_processed {
                    break
                }
                dc := CidFromSlice(dcid)
                sc := CidFromSlice(scid)
                if !CidEqual(&dc, &e.local_scid) || !CidEqual(&sc, e.cids.Current()) {
                    break
                }
                if VnHasVersion1(versions) {
                    break
                }
                e.hs_net_err = some(net.Unsupported)
                e.pn_exhausted = true
                e.RecomputeTimer(now)
                break
            }
            .Retry(version, dcid, scid, token, tag, packet) {
                _ = version
                if e.is_server || !was_first || e.retry_received || e.any_pkt_processed {
                    break
                }
                if token.Len() == 0 || CidEqual(&scid, &e.orig_dcid) || !CidEqual(&dcid, &e.local_scid) {
                    break
                }
                if !VerifyRetryTag(&e.orig_dcid, packet, &tag) {
                    break
                }
                if token.Len() > 128 {
                    break
                }
                e.retry_received = true
                e.retry_scid = some(scid)
                (mut e.retry_token[0..token.Len()]).CopyFrom(token)
                e.retry_token_len = token.Len()
                e.cids.SetPeerHandshakeCid(&scid)
                e.keys.ReinitInitial(&scid)
                e.rec.ResetForRetry(now)
                e.crypto.ResetTx(Space.Initial)
                e.RecomputeTimer(now)
                break
            }
            .UnknownVersion {
                if was_first {
                    _ = CheckStatelessReset(e, dgram[..], now)
                }
                break
            }
            .Long(hdr, packet) {
                pkt_len := packet.Len()
                if was_first {
                    first_dcid = hdr.dcid
                } else if !CidEqual(&hdr.dcid, &first_dcid) {
                    // Ignore later coalesced packets with a different DCID.
                    pos += pkt_len
                    continue
                }
                if !e.is_server && !CidEqual(&hdr.dcid, &e.local_scid) {
                    if was_first {
                        _ = CheckStatelessReset(e, dgram[..], now)
                    }
                    pos += pkt_len
                    continue
                }
                if hdr.kind == PktZeroRtt {
                    pos += pkt_len
                    continue
                }
                if hdr.kind == PktInitial {
                    if e.is_server && dgram.Len() < MinInitialDatagramLen {
                        pos += pkt_len
                        continue
                    }
                    if !e.is_server && hdr.token.Len() > 0 {
                        pos += pkt_len
                        continue
                    }
                }
                if e.peer_scid_locked {
                    if exp := e.peer_initial_scid {
                        if !CidEqual(&hdr.scid, &exp) {
                            pos += pkt_len
                            continue
                        }
                    }
                }
                space := if hdr.kind == PktInitial { Space.Initial } else { Space.Handshake }
                largest_rx := e.acks.Largest(space) else { -1 }
                pn_off := hdr.pn_offset
                hdr_dcid := hdr.dcid
                hdr_scid := hdr.scid
                opened := e.keys.OpenLong(space, mut dgram[pos..pos + pkt_len], pn_off, largest_rx, mut plain[..]) else err_open {
                    code := match err_open {
                        TransportError(code: c, frame: _) { c }
                        _ { ErrProtocolViolation }
                    }
                    e.FailTransport(code, 0, now)
                    return
                }
                match opened {
                    none {
                        if was_first {
                            if CheckStatelessReset(e, dgram[..], now) {
                                return
                            }
                        }
                        pos += pkt_len
                        continue
                    }
                    some(op) {
                        if e.acks.IsDuplicate(space, op.pn) {
                            pos += pkt_len
                            continue
                        }
                        if !e.is_server && space == Space.Initial && !e.peer_scid_locked {
                            e.peer_initial_scid = some(hdr_scid)
                            e.peer_scid_locked = true
                            e.cids.SetPeerHandshakeCid(&hdr_scid)
                        }
                        if e.is_server && space == Space.Handshake {
                            e.keys.DiscardInitial()
                            e.rec.Discard(Space.Initial)
                            e.crypto.Discard(Space.Initial)
                            e.acks.ClearSpace(Space.Initial)
                            e.addr_validated = true
                            e.rec.SetServerAmpBlocked(false, now)
                            e.close.SetValidatedAddr(from)
                        }
                        res := ApplyFrames(e, io, space, op.pn, &hdr_dcid, plain[0..op.plain_len], from, now) else err_f {
                            match err_f {
                                GapLimit {
                                    pos += pkt_len
                                    continue
                                }
                                TransportError(code, frame) {
                                    e.FailTransport(code, frame, now)
                                    return
                                }
                                _ {
                                    e.FailTransport(ErrInternal, 0, now)
                                    return
                                }
                            }
                        }
                        _ = e.acks.Add(space, op.pn, res.ack_eliciting, now)
                        e.any_pkt_processed = true
                        e.RestartIdleOnRx(now)
                    }
                }
                pos += pkt_len
            }
            .Short(hdr, packet) {
                _ = packet
                if !e.is_server && !e.cids.HasLocalCid(&hdr.dcid) {
                    if was_first {
                        _ = CheckStatelessReset(e, dgram[..], now)
                    }
                    break
                }
                if e.hs_state != HsComplete && e.hs_state != HsConfirmed {
                    // Never decrypt or acknowledge 1-RTT before handshake completes.
                    break
                }
                largest_rx := e.acks.Largest(Space.App) else { -1 }
                next_w_pn := e.rec.PeekPn(Space.App)
                opened := e.keys.OpenShort(mut dgram[pos..], &hdr, largest_rx, next_w_pn, now, e.rec.Pto(), mut plain[..]) else err_open {
                    code := match err_open {
                        TransportError(code: c, frame: _) { c }
                        _ { ErrProtocolViolation }
                    }
                    e.FailTransport(code, 0, now)
                    return
                }
                match opened {
                    none {
                        if was_first {
                            _ = CheckStatelessReset(e, dgram[..], now)
                        }
                        break
                    }
                    some(op) {
                        if e.acks.IsDuplicate(Space.App, op.pn) {
                            break
                        }
                        res := ApplyFrames(e, io, Space.App, op.pn, &hdr.dcid, plain[0..op.plain_len], from, now) else err_f {
                            match err_f {
                                GapLimit { break }
                                TransportError(code, frame) {
                                    e.FailTransport(code, frame, now)
                                    return
                                }
                                _ {
                                    e.FailTransport(ErrInternal, 0, now)
                                    return
                                }
                            }
                        }
                        _ = e.acks.Add(Space.App, op.pn, res.ack_eliciting, now)
                        if e.is_server {
                            _ = e.path.OnPacket(io, from, dgram.Len(), op.pn, res.non_probing, e.rec.Pto(), now)
                        }
                        e.any_pkt_processed = true
                        e.RestartIdleOnRx(now)
                    }
                }
                break
            }
        }
    }
    e.RecomputeTimer(now)
}
