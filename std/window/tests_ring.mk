// Why: the SPSC event ring and accumulator must coalesce motion/wheel/resize
// without reordering, enforce the 75% reserve for high-priority events,
// emit Dropped(count) on overflow and work concurrently across two tasks.
// What: unit and two-task stress tests for Ring and Accumulator.
// How: checks RawEvent 32-byte layout, coalescing, 75% reserve, Dropped,
// pack/unpack round-trips and 10,000-event producer/consumer stress.

test "RawEvent is 32 bytes and round-trips every EventKind" (sys: &System) {
    _ = sys
    Assert(SizeOf[RawEvent]() == 32)
    Assert(SizeOf[RingSlot]() == 32)
    mods := Mods(shift: true, ctrl: false, alt: true, meta: false)
    ch := Cast[char](0x20AC)
    kinds := [
        EventKind.CloseRequested,
        EventKind.Resized(width: 1920, height: 1080),
        EventKind.Moved(x: -50, y: 200),
        EventKind.ScaleChanged(scale: 1.5),
        EventKind.Minimized,
        EventKind.Restored,
        EventKind.Focus(on: false),
        EventKind.Key(key: Key.KeyW, layout: Key.KeyZ, scancode: 0x11, down: true, repeat: true, mods: mods),
        EventKind.Text(ch: ch),
        EventKind.Pointer(x: 12.5, y: 34.25),
        EventKind.Button(button: Button.Right, down: true, x: 5.0, y: 6.0, mods: mods),
        EventKind.Wheel(dx: 0.5, dy: -1.5),
        EventKind.Hover(inside: true),
        EventKind.Dropped(count: 42),
    ]
    for i in 0..kinds.Len() {
        raw := PackEventKind(Cast[i64](1000 + i), kinds[i])
        out := UnpackEventKind(raw)
        Assert(raw.at_nanos == Cast[i64](1000 + i))
        match out {
            EventKind.Resized(width, height) { Assert(width == 1920 && height == 1080) }
            EventKind.Moved(x, y) { Assert(x == -50 && y == 200) }
            EventKind.ScaleChanged(scale) { Assert(scale == 1.5) }
            EventKind.Key(key, layout, scancode, down, repeat, mods: m) {
                Assert(KeyCode(key) == KeyCode(Key.KeyW) && KeyCode(layout) == KeyCode(Key.KeyZ))
                Assert(scancode == 0x11 && down && repeat && m.shift && !m.ctrl && m.alt && !m.meta)
            }
            EventKind.Text(ch: got_ch) { Assert(got_ch == ch) }
            EventKind.Dropped(count) { Assert(count == 42) }
            _ {}
        }
    }
}

test "Accumulator coalesces Pointer and Wheel and respects 75 percent reserve" (sys: &System) {
    ring := try RingNew(sys.heap, 64)
    defer ring.RingClose(sys.heap)
    var acc := AccumulatorNew()

    // Coalesce 3 pointers -> 1 pointer; 2 wheels -> 1 summed wheel.
    _ = acc.Record(&ring, PackEventKind(1, EventKind.Pointer(x: 1.0, y: 2.0)))
    _ = acc.Record(&ring, PackEventKind(2, EventKind.Pointer(x: 3.0, y: 4.0)))
    _ = acc.Record(&ring, PackEventKind(3, EventKind.Pointer(x: 5.0, y: 6.0)))
    _ = acc.Record(&ring, PackEventKind(4, EventKind.Wheel(dx: 1.0, dy: 2.0)))
    _ = acc.Record(&ring, PackEventKind(5, EventKind.Wheel(dx: 0.5, dy: -0.5)))
    _ = acc.Flush(&ring)
    Assert(ring.RingLen() == 2)
    e0 := UnpackEventKind(ring.RingPop() else { Panic("missing 0") })
    e1 := UnpackEventKind(ring.RingPop() else { Panic("missing 1") })
    match e0 {
        EventKind.Pointer(x, y) { Assert(x == 5.0 && y == 6.0) }
        _ { Assert(false) }
    }
    match e1 {
        EventKind.Wheel(dx, dy) { Assert(dx == 1.5 && dy == 1.5) }
        _ { Assert(false) }
    }

    // Fill 48/64 (75%) with pointer events; 49th pointer cannot enter reserve.
    for i in 0..48 {
        Assert(ring.RingPush(PackEventKind(Cast[i64](i), EventKind.Pointer(x: Cast[f32](i), y: 0.0))))
    }
    Assert(!ring.RingPush(PackEventKind(48, EventKind.Pointer(x: 48.0, y: 0.0))))

    // High-priority Key events CAN enter the remaining 16 slots (up to 64).
    mods := Mods(shift: false, ctrl: false, alt: false, meta: false)
    for i in 0..16 {
        Assert(ring.RingPush(PackEventKind(Cast[i64](100 + i), EventKind.Key(key: Key.KeyA, layout: Key.KeyA, scancode: 0, down: true, repeat: false, mods: mods))))
    }
    Assert(ring.RingLen() == 64)

    // Overflow via Accumulator: record 2 keys while full -> drain -> Dropped(2) precedes flush.
    _ = acc.Record(&ring, PackEventKind(200, EventKind.Key(key: Key.KeyB, layout: Key.KeyB, scancode: 0, down: true, repeat: false, mods: mods)))
    _ = acc.Record(&ring, PackEventKind(201, EventKind.Key(key: Key.KeyC, layout: Key.KeyC, scancode: 0, down: true, repeat: false, mods: mods)))
    Assert(acc.dropped == 2)
    for _ in 0..64 {
        _ = ring.RingPop() else { Panic("expected 64") }
    }
    Assert(acc.Flush(&ring))
    d_ev := UnpackEventKind(ring.RingPop() else { Panic("missing Dropped") })
    match d_ev {
        EventKind.Dropped(count) { Assert(count == 2) }
        _ { Assert(false) }
    }
}

RingProducer(io: Io, r: &Ring, count: int) {
    var i := 0
    for i < count {
        ev := PackEventKind(Cast[i64](i + 1), EventKind.Text(ch: 'a'))
        if r.RingPush(ev) {
            i += 1
        } else {
            io.Yield() else { return }
        }
    }
}

RingConsumer(io: Io, r: &Ring, count: int, bad: mut int) {
    var got := 0
    for got < count {
        if ev := r.RingPop() {
            if ev.at_nanos != Cast[i64](got + 1) {
                *bad += 1
            }
            got += 1
        } else {
            io.Yield() else { return }
        }
    }
}

test "Ring two-task SPSC stress transfers 10000 events in order" (sys: &System) {
    ring := try RingNew(sys.heap, 256)
    defer ring.RingClose(sys.heap)
    var bad := 0
    scope s := sys.io.Scope() {
        s.go RingProducer(sys.io, &ring, 10000)
        s.go RingConsumer(sys.io, &ring, 10000, mut bad)
    }
    Assert(bad == 0)
    Assert(ring.RingLen() == 0)
}
