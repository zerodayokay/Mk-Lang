// Why: QUIC packet assembly must coalesce spaces under one DCID with 1-RTT
// last, enforce 1,200-byte Initial/path padding, 3x anti-amplification,
// congestion/pacing windows, sample-length padding, key-update confidentiality
// limits, and optimistic-ACK packet-number skips without per-packet allocation.
//
// What: QConn.FlushSend, BuildNextDatagram, BuildCloseDatagram, and
// BuildPathDatagram.
//
// How: FlushSend acquires sl (SendState), builds up to 10 datagrams under eng
// lock into SendState.batch_buf, releases eng, and sends via SharedDatagram or
// Endpoint. Client Handshake send immediately discards Initial keys (QS-20).

import std.net
import std.time

// Allocates the next send packet number in space, applying the optimistic-ACK
// skip on Space.App (QT-342) and closing silently at 2^62 - 1 (QT-88).
AllocSendPn(e: mut Engine, space: Space): ?i64 {
    pn := e.rec.NextPn(space)
    if pn >= MaxPacketNum {
        e.pn_exhausted = true
        return none
    }
    if space == Space.App && pn == e.next_skip_app_pn {
        e.rec.SkipPn(Space.App, pn)
        pn2 := e.rec.NextPn(Space.App)
        if pn2 >= MaxPacketNum {
            e.pn_exhausted = true
            return none
        }
        step := Cast[i64](256 + (e.NextRng() % 768))
        e.next_skip_app_pn = pn2 + step
        return some(pn2)
    }
    return some(pn)
}

// Returns remaining anti-amplification budget in bytes for dst, or -1 if unlimited.
Engine.RemainingAmpBudget(&self, dst: &net.Addr): int {
    var cap := -1
    if self.is_server && !self.addr_validated {
        rem := self.amp_rx_bytes * 3 - self.amp_tx_bytes
        cap = if rem <= 0 { 0 } else { int(rem) }
    }
    path_rem := self.path.Allowance(*dst)
    if path_rem >= 0 {
        if cap < 0 || path_rem < cap {
            cap = path_rem
        }
    }
    return cap
}

// Records sent bytes against server address validation and path budgets.
Engine.RecordAmpSent(mut self, dst: net.Addr, bytes: int) {
    if self.is_server && !self.addr_validated {
        self.amp_tx_bytes += i64(bytes)
    }
    self.path.RecordSend(dst, bytes)
}

// Pads plaintext buffer in place to satisfy minimum HP sample length (QS-35)
// plus optional extra target_plain_len (e.g. for 1,200-byte datagram padding).
PadPlain(plain: mut []u8, cur_len: int, pn_len: int, min_total_plain: int): int {
    need_sample := cur_len + MinSamplePadding(pn_len, cur_len)
    target := if min_total_plain > need_sample { min_total_plain } else { need_sample }
    if target > plain.Len() {
        return cur_len
    }
    if target > cur_len {
        _ = WritePadding(mut plain[cur_len..], target - cur_len)
        return target
    }
    return cur_len
}

// Estimates the header bytes for a Long or Short header with 4-byte worst-case PN.
HeaderOverhead(space: Space, dcid: &Cid, scid: &Cid, token_len: int, pn_len: int): int {
    if space == Space.App {
        return 1 + dcid.n + pn_len
    }
    tok_bytes := if space == Space.Initial { VarintLen(Cast[u64](token_len)) + token_len } else { 0 }
    // 1 first + 4 ver + 1 dlen + dcid + 1 slen + scid + tok + 2 len varint + pn_len.
    return 7 + dcid.n + scid.n + tok_bytes + 2 + pn_len
}

// Builds a CONNECTION_CLOSE datagram across available levels (QT-323..QT-325).
BuildCloseDatagram(e: mut Engine, out: mut []u8, now: time.Instant): ?(len: int, dst: net.Addr) {
    dst := e.path.Destination()
    amp := e.RemainingAmpBudget(&dst)
    dgram_max := if amp >= 0 && amp < MaxSendDatagram { amp } else { MaxSendDatagram }
    if dgram_max < 40 {
        return none
    }
    var pos := 0
    dcid := *e.cids.Current()
    pto := e.rec.Pto()

    if e.hs_state == HsConfirmed && e.keys.HasWriteKeys(Space.App) {
        pn := AllocSendPn(e, Space.App) else { return none }
        la := e.rec.LargestAcked(Space.App) else { -1 }
        pn_len := EncodePnLen(pn, la)
        var plain: [256]u8 = [0; 256]
        wn := WriteConnectionClose(mut plain[..], e.close.code, e.close.frame_type, b"", e.close.app) else { return none }
        plen := PadPlain(mut plain[..], wn, pn_len, 0)
        hdr := WriteShortHeader(mut out[pos..], &dcid, e.keys.key_phase, pn, pn_len) else { return none }
        if pos + hdr.header_len + plen + 16 > dgram_max {
            return none
        }
        pkt_len := e.keys.SealPacket(Space.App, pn, hdr.header_len, hdr.pn_offset, pn_len, plain[0..plen], mut out[pos..], now, pto) else { return none }
        pos += pkt_len
    } else {
        // Before confirmation: coalesce Initial, Handshake, and App (if keys exist),
        // replacing 0x1d with 0x1c APPLICATION_ERROR in Initial/Handshake (QT-324, QT-325).
        hs_code := if e.close.app { ErrApplication } else { e.close.code }
        hs_ft := if e.close.app { u64(0) } else { e.close.frame_type }
        if e.keys.HasWriteKeys(Space.Initial) {
            pn := AllocSendPn(e, Space.Initial) else { return none }
            la := e.rec.LargestAcked(Space.Initial) else { -1 }
            pn_len := EncodePnLen(pn, la)
            tok := if !e.is_server && e.retry_token_len > 0 { e.retry_token[0..e.retry_token_len] } else { b"" }
            var plain: [256]u8 = [0; 256]
            wn := WriteConnectionClose(mut plain[..], hs_code, hs_ft, b"", false) else { return none }
            plen := PadPlain(mut plain[..], wn, pn_len, 0)
            hdr := WriteLongHeader(mut out[pos..], PktInitial, &dcid, &e.local_scid, tok, plen + 16, pn, pn_len) else { return none }
            if pos + hdr.header_len + plen + 16 <= dgram_max {
                pkt_len := e.keys.SealPacket(Space.Initial, pn, hdr.header_len, hdr.pn_offset, pn_len, plain[0..plen], mut out[pos..], now, pto) else { return none }
                pos += pkt_len
            }
        }
        if e.keys.HasWriteKeys(Space.Handshake) {
            pn := AllocSendPn(e, Space.Handshake) else { return none }
            la := e.rec.LargestAcked(Space.Handshake) else { -1 }
            pn_len := EncodePnLen(pn, la)
            var plain: [256]u8 = [0; 256]
            wn := WriteConnectionClose(mut plain[..], hs_code, hs_ft, b"", false) else { return none }
            plen := PadPlain(mut plain[..], wn, pn_len, 0)
            hdr := WriteLongHeader(mut out[pos..], PktHandshake, &dcid, &e.local_scid, b"", plen + 16, pn, pn_len) else { return none }
            if pos + hdr.header_len + plen + 16 <= dgram_max {
                pkt_len := e.keys.SealPacket(Space.Handshake, pn, hdr.header_len, hdr.pn_offset, pn_len, plain[0..plen], mut out[pos..], now, pto) else { return none }
                pos += pkt_len
            }
        }
        if e.keys.HasWriteKeys(Space.App) {
            pn := AllocSendPn(e, Space.App) else { return none }
            la := e.rec.LargestAcked(Space.App) else { -1 }
            pn_len := EncodePnLen(pn, la)
            var plain: [256]u8 = [0; 256]
            wn := WriteConnectionClose(mut plain[..], e.close.code, e.close.frame_type, b"", e.close.app) else { return none }
            plen := PadPlain(mut plain[..], wn, pn_len, 0)
            hdr := WriteShortHeader(mut out[pos..], &dcid, e.keys.key_phase, pn, pn_len) else { return none }
            if pos + hdr.header_len + plen + 16 <= dgram_max {
                pkt_len := e.keys.SealPacket(Space.App, pn, hdr.header_len, hdr.pn_offset, pn_len, plain[0..plen], mut out[pos..], now, pto) else { return none }
                pos += pkt_len
            }
        }
    }
    if pos == 0 {
        return none
    }
    e.RecordAmpSent(dst, pos)
    return some((len: pos, dst: dst))
}

// Seals a 1-RTT path probing packet (PATH_RESPONSE or PATH_CHALLENGE), padded
// to 1,200 bytes when the destination path's budget allows (QT-309, QT-315).
SealPathFramePacket(
    e: mut Engine,
    out: mut []u8,
    dst: net.Addr,
    dcid: &Cid,
    is_challenge: bool,
    data: &[8]u8,
    now: time.Instant,
): ?(len: int, padded_1200: bool) {
    if !e.keys.HasWriteKeys(Space.App) {
        return none
    }
    amp := e.RemainingAmpBudget(&dst)
    dgram_max := if amp >= 0 && amp < MaxSendDatagram { amp } else { MaxSendDatagram }
    if dgram_max < 36 {
        return none
    }
    pn := AllocSendPn(e, Space.App) else { return none }
    la := e.rec.LargestAcked(Space.App) else { -1 }
    pn_len := EncodePnLen(pn, la)
    hdr_oh := 1 + dcid.n + pn_len
    max_plain := dgram_max - hdr_oh - 16
    if max_plain < 9 {
        return none
    }
    var plain: [1200]u8 = [0; 1200]
    wn := if is_challenge {
        WritePathChallenge(mut plain[0..max_plain], data) else { return none }
    } else {
        WritePathResponse(mut plain[0..max_plain], data) else { return none }
    }
    want_pad := if dgram_max >= MaxSendDatagram { MaxSendDatagram - hdr_oh - 16 } else { 0 }
    plen := PadPlain(mut plain[0..max_plain], wn, pn_len, want_pad)
    hdr := WriteShortHeader(mut out[..], dcid, e.keys.key_phase, pn, pn_len) else { return none }
    pto := e.rec.Pto()
    pkt_len := e.keys.SealPacket(Space.App, pn, hdr.header_len, hdr.pn_offset, pn_len, plain[0..plen], out, now, pto) else {
        return none
    }
    // Path frames are not retransmitted via loss recovery (QT-316); record empty SentFrame list
    // only for PATH_CHALLENGE so bytes in flight aren't misattributed.
    empty_frames: [0]SentFrame = []
    _ = e.rec.OnSent(e.h.Alloc(), Space.App, pn, pkt_len, is_challenge, false, empty_frames[..], now) else {}
    e.RecordAmpSent(dst, pkt_len)
    return some((len: pkt_len, padded_1200: pkt_len >= MaxSendDatagram))
}

// Reports whether optional space sp equals target.
IsProbeSpace(sp: ?Space, target: Space): bool {
    return match sp {
        some(s) { s == target }
        none { false }
    }
}

// Checks whether Space.App has frames to emit right now.
AppHasWork(e: &Engine, now: time.Instant, allow_data: bool, is_probe: bool): bool {
    if !e.keys.HasWriteKeys(Space.App) || e.hs_state < HsComplete {
        return false
    }
    if e.acks.HasPendingAck(Space.App) {
        return true
    }
    if d := e.acks.Due(now) {
        if d.Nanos() <= 0 {
            return true
        }
    }
    if e.hs_done_pending {
        return true
    }
    // Pending NEW_CONNECTION_ID or RETIRE_CONNECTION_ID.
    if e.cids.pending_retires.Len() > 0 && !e.cids.peer_zero_len {
        return true
    }
    for i in 0..e.cids.local.Len() {
        if e.cids.local[i].pending_send && !e.cids.local[i].retired {
            return true
        }
    }
    if allow_data && e.streams.HasPending() {
        return true
    }
    return is_probe
}

// Builds the next datagram under Engine lock into out[0..MaxSendDatagram].
BuildNextDatagram(e: mut Engine, out: mut []u8, now: time.Instant): ?(len: int, dst: net.Addr, sent_data: bool) {
    if e.abandoned || e.pn_exhausted || e.close.phase == ClosePhase.Draining {
        return none
    }
    if e.close.phase == ClosePhase.Closing {
        if !e.close_first_pending && !e.close_reply_owed {
            return none
        }
        e.close_first_pending = false
        e.close_reply_owed = false
        if res := BuildCloseDatagram(e, out, now) {
            return some((len: res.len, dst: res.dst, sent_data: false))
        }
        return none
    }

    // Priority 2: owed PATH_RESPONSE and PATH_CHALLENGE packets.
    if resp := e.path.TakeOwedResponse() {
        dcid := *e.cids.Current()
        if sr := SealPathFramePacket(e, out, resp.to, &dcid, false, &resp.data, now) {
            e.RestartIdleOnTx(now)
            e.RecomputeTimer(now)
            return some((len: sr.len, dst: resp.to, sent_data: false))
        }
    }
    if ch := e.path.TakeNewChallenge() {
        dcid := *e.cids.Current()
        if sr := SealPathFramePacket(e, out, ch.to, &dcid, true, &ch.data, now) {
            e.path.MarkNewChallengeSent(sr.padded_1200)
            e.RestartIdleOnTx(now)
            e.RecomputeTimer(now)
            return some((len: sr.len, dst: ch.to, sent_data: false))
        }
    }
    if och := e.path.TakeOldChallenge() {
        scid := *e.cids.SparePeerCid()
        if sr := SealPathFramePacket(e, out, och.to, &scid, true, &och.data, now) {
            e.RestartIdleOnTx(now)
            e.RecomputeTimer(now)
            return some((len: sr.len, dst: och.to, sent_data: false))
        }
    }

    dst := e.path.Destination()
    amp_rem := e.RemainingAmpBudget(&dst)
    dgram_max := if amp_rem >= 0 && amp_rem < MaxSendDatagram { amp_rem } else { MaxSendDatagram }
    if dgram_max <= 36 {
        if e.is_server && !e.addr_validated {
            e.rec.SetServerAmpBlocked(true, now)
        }
        return none
    }

    is_probe := e.probe_owed_count > 0
    probe_sp := e.probe_owed_space
    cc_ok := is_probe || e.rec.CanSend(MaxSendDatagram, now)
    pto := e.rec.Pto()
    a := e.h.Alloc()
    dcid := *e.cids.Current()

    // Determine which spaces have work to pack into this datagram (QT-80).
    init_ack := e.keys.HasWriteKeys(Space.Initial) && e.acks.HasPendingAck(Space.Initial)
    init_crypto := e.keys.HasWriteKeys(Space.Initial) && cc_ok && e.crypto.HasPending(Space.Initial)
    init_probe := e.keys.HasWriteKeys(Space.Initial) && is_probe && IsProbeSpace(probe_sp, Space.Initial)
    want_init := init_ack || init_crypto || init_probe

    hs_ack := e.keys.HasWriteKeys(Space.Handshake) && e.acks.HasPendingAck(Space.Handshake)
    hs_crypto := e.keys.HasWriteKeys(Space.Handshake) && cc_ok && e.crypto.HasPending(Space.Handshake)
    hs_probe := e.keys.HasWriteKeys(Space.Handshake) && is_probe && IsProbeSpace(probe_sp, Space.Handshake)
    want_hs := hs_ack || hs_crypto || hs_probe

    app_probe := is_probe && IsProbeSpace(probe_sp, Space.App)
    want_app := AppHasWork(&*e, now, cc_ok, app_probe)

    if !want_init && !want_hs && !want_app {
        return none
    }

    // Server anti-amplification + 1200-byte Initial rule (QT-262, QT-266).
    if e.is_server && !e.addr_validated && want_init && (init_crypto || init_probe) && dgram_max < MinInitialDatagramLen {
        e.rec.SetServerAmpBlocked(true, now)
        return none
    }

    var pos := 0
    var any_ae := false
    var need_pad_1200 := false
    var sent_data := false
    var plain: [1200]u8 = [0; 1200]
    var frames_buf: [64]SentFrame = [SentFrame(kind: SentKind.Control(index: 0)); 64]

    // 1. Initial packet.
    if want_init {
        pn := AllocSendPn(e, Space.Initial) else { return none }
        la := e.rec.LargestAcked(Space.Initial) else { -1 }
        pn_len := EncodePnLen(pn, la)
        tok := if !e.is_server && e.retry_token_len > 0 { e.retry_token[0..e.retry_token_len] } else { b"" }
        oh := HeaderOverhead(Space.Initial, &dcid, &e.local_scid, tok.Len(), pn_len)
        room := dgram_max - pos - oh - 16
        if room >= 4 {
            var poff := 0
            var fn_cnt := 0
            var ae := false
            if init_ack || e.acks.Largest(Space.Initial).IsSome() {
                aw := e.acks.Write(Space.Initial, mut plain[poff..room], now)
                poff += aw
            }
            if init_crypto {
                cr := e.crypto.Next(Space.Initial, mut plain[poff..room], room - poff, mut frames_buf[fn_cnt..])
                if cr.n > 0 {
                    poff += cr.n
                    fn_cnt += cr.frames
                    ae = true
                }
            }
            if init_probe && !ae && poff < room {
                if pw := WritePing(mut plain[poff..room]) {
                    poff += pw
                    ae = true
                }
            }
            if poff > 0 {
                // Client ALWAYS pads Initial datagrams to 1200; server pads ack-eliciting Initials (QT-265, QT-266).
                if !e.is_server || ae {
                    need_pad_1200 = true
                }
                pad_target := if need_pad_1200 && !want_hs && !want_app && dgram_max >= MinInitialDatagramLen {
                    dgram_max - pos - oh - 16
                } else { 0 }
                plen := PadPlain(mut plain[0..room], poff, pn_len, pad_target)
                hdr := WriteLongHeader(mut out[pos..], PktInitial, &dcid, &e.local_scid, tok, plen + 16, pn, pn_len) else {
                    return none
                }
                pkt_len := e.keys.SealPacket(Space.Initial, pn, hdr.header_len, hdr.pn_offset, pn_len, plain[0..plen], mut out[pos..], now, pto) else {
                    return none
                }
                in_flight := ae || plen > poff
                if init_probe {
                    _ = e.rec.OnProbeSent(a, Space.Initial, pn, pkt_len, frames_buf[0..fn_cnt], now) else {}
                } else {
                    _ = e.rec.OnSent(a, Space.Initial, pn, pkt_len, ae, in_flight, frames_buf[0..fn_cnt], now) else {}
                }
                pos += pkt_len
                if ae {
                    any_ae = true
                }
            }
        }
    }

    // 2. Handshake packet.
    if want_hs && pos + 40 < dgram_max {
        pn := AllocSendPn(e, Space.Handshake) else { return none }
        la := e.rec.LargestAcked(Space.Handshake) else { -1 }
        pn_len := EncodePnLen(pn, la)
        oh := HeaderOverhead(Space.Handshake, &dcid, &e.local_scid, 0, pn_len)
        room := dgram_max - pos - oh - 16
        if room >= 4 {
            var poff := 0
            var fn_cnt := 0
            var ae := false
            if hs_ack || e.acks.Largest(Space.Handshake).IsSome() {
                aw := e.acks.Write(Space.Handshake, mut plain[poff..room], now)
                poff += aw
            }
            if hs_crypto {
                cr := e.crypto.Next(Space.Handshake, mut plain[poff..room], room - poff, mut frames_buf[fn_cnt..])
                if cr.n > 0 {
                    poff += cr.n
                    fn_cnt += cr.frames
                    ae = true
                }
            }
            if hs_probe && !ae && poff < room {
                if pw := WritePing(mut plain[poff..room]) {
                    poff += pw
                    ae = true
                }
            }
            if poff > 0 {
                pad_target := if need_pad_1200 && !want_app && dgram_max >= MinInitialDatagramLen {
                    dgram_max - pos - oh - 16
                } else { 0 }
                plen := PadPlain(mut plain[0..room], poff, pn_len, pad_target)
                hdr := WriteLongHeader(mut out[pos..], PktHandshake, &dcid, &e.local_scid, b"", plen + 16, pn, pn_len) else {
                    return none
                }
                pkt_len := e.keys.SealPacket(Space.Handshake, pn, hdr.header_len, hdr.pn_offset, pn_len, plain[0..plen], mut out[pos..], now, pto) else {
                    return none
                }
                in_flight := ae || plen > poff
                if hs_probe {
                    _ = e.rec.OnProbeSent(a, Space.Handshake, pn, pkt_len, frames_buf[0..fn_cnt], now) else {}
                } else {
                    _ = e.rec.OnSent(a, Space.Handshake, pn, pkt_len, ae, in_flight, frames_buf[0..fn_cnt], now) else {}
                }
                pos += pkt_len
                if ae {
                    any_ae = true
                }
                // QS-20: client discards Initial keys as soon as it first sends a Handshake packet.
                if !e.is_server {
                    e.keys.DiscardInitial()
                    e.rec.Discard(Space.Initial)
                    e.crypto.Discard(Space.Initial)
                }
            }
        }
    }

    // 3. 1-RTT Application Data packet (always last in datagram, QT-80).
    if want_app && pos + 32 < dgram_max {
        pn := AllocSendPn(e, Space.App) else { return none }
        la := e.rec.LargestAcked(Space.App) else { -1 }
        pn_len := EncodePnLen(pn, la)
        oh := HeaderOverhead(Space.App, &dcid, &e.local_scid, 0, pn_len)
        room := dgram_max - pos - oh - 16
        if room >= 4 {
            var poff := 0
            var fn_cnt := 0
            var ae := false
            if e.acks.HasPendingAck(Space.App) || e.acks.Largest(Space.App).IsSome() {
                // Include ACK when pending/due, or piggyback on ack-eliciting App packets.
                due_now := match e.acks.Due(now) {
                    some(d) { d.Nanos() <= 0 }
                    none { false }
                }
                if e.acks.HasPendingAck(Space.App) || due_now || e.hs_done_pending || (cc_ok && e.streams.HasPending()) {
                    aw := e.acks.Write(Space.App, mut plain[poff..room], now)
                    poff += aw
                }
            }
            if e.hs_done_pending && fn_cnt < 64 {
                if hw := WriteHandshakeDone(mut plain[poff..room]) {
                    poff += hw
                    e.hs_done_pending = false
                    frames_buf[fn_cnt] = SentFrame(kind: SentKind.Control(index: e.AllocCtrl(ConnCtrlKind.HandshakeDone)))
                    fn_cnt += 1
                    ae = true
                }
            }
            for fn_cnt < 64 {
                ret_seq := e.cids.TakePendingRetire() else { break }
                if rw := WriteRetireConnectionId(mut plain[poff..room], ret_seq) {
                    poff += rw
                    frames_buf[fn_cnt] = SentFrame(kind: SentKind.Control(index: e.AllocCtrl(ConnCtrlKind.RetireCid(seq: ret_seq))))
                    fn_cnt += 1
                    ae = true
                } else {
                    e.cids.OnRetireLost(a, ret_seq)
                    break
                }
            }
            for fn_cnt < 64 {
                nc_item := e.cids.TakePendingNewCid() else { break }
                if nw := WriteNewConnectionId(mut plain[poff..room], nc_item.seq, nc_item.retire_prior, &nc_item.cid, &nc_item.token) {
                    poff += nw
                    frames_buf[fn_cnt] = SentFrame(kind: SentKind.Control(index: e.AllocCtrl(ConnCtrlKind.NewCid(seq: nc_item.seq))))
                    fn_cnt += 1
                    ae = true
                } else {
                    e.cids.OnNewCidLost(nc_item.seq)
                    break
                }
            }
            if cc_ok && fn_cnt < 64 && poff < room {
                // Reload dcid in case TakePendingRetire switched current peer CID (QT-215).
                sr := e.streams.Next(a, mut plain[poff..room], room - poff, mut frames_buf[fn_cnt..])
                if sr.n > 0 {
                    poff += sr.n
                    fn_cnt += sr.frames
                    ae = true
                    sent_data = true
                }
            }
            if app_probe && !ae && poff < room {
                if pw := WritePing(mut plain[poff..room]) {
                    poff += pw
                    ae = true
                }
            }
            if poff > 0 {
                cur_dcid := *e.cids.Current()
                pad_target := if need_pad_1200 && dgram_max >= MinInitialDatagramLen {
                    dgram_max - pos - (1 + cur_dcid.n + pn_len) - 16
                } else { 0 }
                plen := PadPlain(mut plain[0..room], poff, pn_len, pad_target)
                hdr := WriteShortHeader(mut out[pos..], &cur_dcid, e.keys.key_phase, pn, pn_len) else {
                    return none
                }
                pkt_len := e.keys.SealPacket(Space.App, pn, hdr.header_len, hdr.pn_offset, pn_len, plain[0..plen], mut out[pos..], now, pto) else {
                    return none
                }
                in_flight := ae || plen > poff
                if app_probe {
                    _ = e.rec.OnProbeSent(a, Space.App, pn, pkt_len, frames_buf[0..fn_cnt], now) else {}
                } else {
                    _ = e.rec.OnSent(a, Space.App, pn, pkt_len, ae, in_flight, frames_buf[0..fn_cnt], now) else {}
                }
                pos += pkt_len
                if ae {
                    any_ae = true
                }
            }
        }
    }

    // If Initial needed 1200-byte padding and coalesced HS/App wrote nothing, pad with a trailing Initial or fail.
    if pos == 0 {
        return none
    }
    if need_pad_1200 && pos < MinInitialDatagramLen && dgram_max >= MinInitialDatagramLen && e.keys.HasWriteKeys(Space.Initial) {
        // Fill remaining bytes up to 1200 with a PADDING-only Initial packet.
        rem_d := MinInitialDatagramLen - pos
        pn := AllocSendPn(e, Space.Initial) else { return none }
        la := e.rec.LargestAcked(Space.Initial) else { -1 }
        pn_len := EncodePnLen(pn, la)
        tok := if !e.is_server && e.retry_token_len > 0 { e.retry_token[0..e.retry_token_len] } else { b"" }
        oh := HeaderOverhead(Space.Initial, &dcid, &e.local_scid, tok.Len(), pn_len)
        if rem_d > oh + 16 {
            p_need := rem_d - oh - 16
            var pad_p: [1200]u8 = [0; 1200]
            _ = WritePadding(mut pad_p[0..p_need], p_need)
            hdr := WriteLongHeader(mut out[pos..], PktInitial, &dcid, &e.local_scid, tok, p_need + 16, pn, pn_len) else {
                return none
            }
            pad_pkt_len := e.keys.SealPacket(Space.Initial, pn, hdr.header_len, hdr.pn_offset, pn_len, pad_p[0..p_need], mut out[pos..], now, pto) else { -1 }
            if pad_pkt_len > 0 {
                empty_pad: [0]SentFrame = []
                _ = e.rec.OnSent(a, Space.Initial, pn, pad_pkt_len, false, true, empty_pad[..], now) else {}
                pos += pad_pkt_len
            }
        }
    }

    if is_probe && e.probe_owed_count > 0 {
        e.probe_owed_count -= 1
        if e.probe_owed_count == 0 {
            e.probe_owed_space = none
        }
    }
    e.RecordAmpSent(dst, pos)
    if any_ae {
        e.RestartIdleOnTx(now)
    }
    e.RecomputeTimer(now)
    return some((len: pos, dst: dst, sent_data: sent_data))
}

// Converts Error!int to ?int for optional seal padding.
SomeIntOrNone(r: Error!int): ?int {
    v := r else { return none }
    return some(v)
}

// Builds up to MaxBatchDatagrams outgoing packets under eng lock.
QConn.BuildSendBatch(&self, nc: Io, batch_buf: mut HeapList[u8], dsts: mut []net.Addr, lens: mut []int): int {
    var count := 0
    var any_sent_data := false
    var eg := self.eng.Lock(nc).Must()
    e := mut *eg.Mut()
    for e.pending_retire_ep.Len() > 0 {
        rc := e.pending_retire_ep.Remove(0)
        if ep := &self.ep {
            ep.Ref().RetireLocalCid(&rc)
        }
    }
    if e.need_spare_cids {
        e.need_spare_cids = false
        if ep := &self.ep {
            ep.Ref().IssueSpareCids(nc, e)
        }
    }
    now := time.Now(nc)
    buf := batch_buf.ItemsMut()
    for count < MaxBatchDatagrams {
        off := count * MaxSendDatagram
        built := BuildNextDatagram(e, mut buf[off..off + MaxSendDatagram], now) else {
            break
        }
        dsts[count] = built.dst
        lens[count] = built.len
        if built.sent_data {
            any_sent_data = true
        }
        count += 1
    }
    if any_sent_data {
        self.WakeSatisfied(e)
    }
    eg.Unlock()
    return count
}

// Assembles and sends up to 10 pending QUIC datagrams (Section 3.6.1).
QConn.FlushSend(&self, io: Io): net.Error!void {
    nc := io.NoCancel()
    var sg := try self.sl.Lock(io)
    defer sg.Unlock()
    s := mut *sg.Mut()

    need_bytes := MaxBatchDatagrams * MaxSendDatagram
    if s.batch_buf.Len() < need_bytes {
        a := self.h.Alloc()
        var zeros: [256]u8 = [0; 256]
        for s.batch_buf.Len() < need_bytes {
            take := if need_bytes - s.batch_buf.Len() < 256 { need_bytes - s.batch_buf.Len() } else { 256 }
            s.batch_buf.Extend(a, zeros[0..take]) else {
                fail OutOfMemory
            }
        }
    }

    var dsts: [MaxBatchDatagrams]net.Addr = [QuicZeroAddr; MaxBatchDatagrams]
    var lens: [MaxBatchDatagrams]int = [0; MaxBatchDatagrams]
    count := self.BuildSendBatch(nc, mut s.batch_buf, mut dsts[..], mut lens[..])
    if count == 0 {
        return
    }

    // Coalesce contiguous runs with equal destination and equal size when possible.
    buf_ro := s.batch_buf.Items()
    var i := 0
    for i < count {
        off := i * MaxSendDatagram
        seg := lens[i]
        d := dsts[i]
        // Send each datagram (or contiguous equal-length batch to same dst).
        var run := 1
        if seg == MaxSendDatagram {
            for i + run < count && lens[i + run] == MaxSendDatagram && AddrEqual(&dsts[i + run], &d) {
                run += 1
            }
        }
        if run == 1 {
            if sock := &self.sock {
                try sock.Send(io, buf_ro[off..off + seg], none)
            } else if ep := &self.ep {
                try ep.Ref().SendDatagram(io, buf_ro[off..off + seg], d)
            }
        } else {
            // Contiguous full 1200-byte datagrams in batch_buf.
            if sock := &self.sock {
                try sock.SendBatch(io, buf_ro[off..off + run * MaxSendDatagram], MaxSendDatagram, none)
            } else if ep := &self.ep {
                try ep.Ref().SendBatch(io, buf_ro[off..off + run * MaxSendDatagram], MaxSendDatagram, d)
            }
        }
        i += run
    }
}
