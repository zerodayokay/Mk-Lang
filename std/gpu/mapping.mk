// Why: results come back from the GPU through mapped buffers, and a
// mapping must end before the buffer or the device can be used again.
//
// What: Mapped, MappedMut, Device.MapRead, Device.MapWrite, Bytes, Unmap.
//
// How: waits for the buffer's last-use serial (first flushing pre-submit
// work and clamping to LastSubmitted), validates usage and range, and yields a
// slice borrowed from Mapped/MappedMut.

import std.gpu.vk

/// A readable mapped range. Linear: Unmap it.
export linear view struct Mapped {
    dev: mut Device
    buf: &Buffer
    at: int
    n: int
}

PrepMapCheck(st: &DeviceState, serial: u32, b: &Buffer, want_write: bool, at: int, n: int): Invalid!u64 {
    if b.device != serial || !st.buffers.IsLive(b.slot, b.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    be := st.buffers.Get(b.slot)
    ok_u := if want_write { be.usage.map_write } else { be.usage.map_read }
    if !ok_u {
        fail Invalid(misuse: Misuse.BadUsage, at: -1)
    }
    if at < 0 || n < 0 || i64(at) + i64(n) > i64(be.size) {
        fail Invalid(misuse: Misuse.RangeOutOfBounds, at: -1)
    }
    return st.buffers.LastUse(b.slot)
}

/// Waits until the GPU's last use of b is done and maps n bytes at at.
/// Errors: Invalid(BadUsage) without map_read, Invalid(RangeOutOfBounds),
/// DeviceLost, Canceled (repeatable). Suspends.
export Device.MapRead(mut self, io: Io, b: &Buffer, at: int, n: int): Error!Mapped from self, b {
    try self.CheckAlive()
    last_use := try PrepMapCheck(self.state.Ref(), self.serial, b, false, at, n)
    try self.FlushPreSubmit()
    st := self.state.Mut()
    wait_target := st.serials.ClampWait(last_use)
    match mut st.native {
        .Stub(s) {
            try s.WaitSerial(wait_target)
            st.serials.Complete(wait_target)
        }
        .Dx(d: _) {
            fail Unsupported
        }
        .Vk(v) {
            v.WaitSerial(io, wait_target) else e {
                match e {
                    vk.DeviceLost(reason: r) {
                        st.lost = true
                        st.sticky_err = some(DeviceLost(reason: MapVkLossReason(r)))
                    }
                    _ {}
                }
                fail MapVkErr(e)
            }
            st.serials.Complete(wait_target)
        }
    }
    self.DrainCompleted()
    return Mapped(dev: self, buf: b, at: at, n: n)
}

/// The mapped bytes, valid until Unmap. O(1).
export Mapped.Bytes(&self): []u8 from self {
    st := self.dev.state.Ref()
    nh := st.buffers.Get(self.buf.slot).native_handle
    match &st.native {
        .Stub(s) {
            idx := Cast[int](nh - 1)
            return s.buf_mems[idx].bytes.Items()[self.at..(self.at + self.n)]
        }
        .Dx(d: _) {
            Panic("gpu: mapped bytes on unimplemented backend")
        }
        .Vk(v) {
            return v.MapBytes(nh, self.at, self.n)
        }
    }
}

/// Ends the mapping. O(1).
export Mapped.Unmap(self) {
    Mapped(dev: _, buf: _, at: _, n: _) := self
}

/// A writable mapped range. Linear: Unmap it.
export linear view struct MappedMut {
    dev: mut Device
    buf: &Buffer
    at: int
    n: int
}

/// Waits until the GPU's last use of b is done and maps n bytes at at for
/// writing. Errors: as MapRead, with map_write.
export Device.MapWrite(mut self, io: Io, b: &Buffer, at: int, n: int): Error!MappedMut from self, b {
    try self.CheckAlive()
    last_use := try PrepMapCheck(self.state.Ref(), self.serial, b, true, at, n)
    try self.FlushPreSubmit()
    st := self.state.Mut()
    wait_target := st.serials.ClampWait(last_use)
    match mut st.native {
        .Stub(s) {
            try s.WaitSerial(wait_target)
            st.serials.Complete(wait_target)
        }
        .Dx(d: _) {
            fail Unsupported
        }
        .Vk(v) {
            v.WaitSerial(io, wait_target) else e {
                match e {
                    vk.DeviceLost(reason: r) {
                        st.lost = true
                        st.sticky_err = some(DeviceLost(reason: MapVkLossReason(r)))
                    }
                    _ {}
                }
                fail MapVkErr(e)
            }
            st.serials.Complete(wait_target)
        }
    }
    self.DrainCompleted()
    return MappedMut(dev: self, buf: b, at: at, n: n)
}

/// The mapped bytes, writable until Unmap. O(1).
export MappedMut.Bytes(mut self): mut []u8 from self {
    st := self.dev.state.Mut()
    nh := st.buffers.Get(self.buf.slot).native_handle
    match mut st.native {
        .Stub(s) {
            idx := Cast[int](nh - 1)
            return mut s.buf_mems[idx].bytes.ItemsMut()[self.at..(self.at + self.n)]
        }
        .Dx(d: _) {
            Panic("gpu: mapped bytes on unimplemented backend")
        }
        .Vk(v) {
            return v.MapBytesMut(nh, self.at, self.n)
        }
    }
}

/// Ends the mapping; the writes are visible to the next submission. O(1).
export MappedMut.Unmap(self) {
    MappedMut(dev, buf, at: _, n: _) := self
    st := dev.state.Mut()
    nh := st.buffers.Get(buf.slot).native_handle
    match mut st.native {
        .Vk(v) { v.FlushMapped(nh) }
        _ {}
    }
}
