// Why: D3D12 validation errors and warnings must be captured synchronously
// from ID3D12InfoQueue into device messages and sticky DebugLayer errors,
// with Strict mode filtering only documented performance-only warnings.
//
// What: EnableDebugLayer, SetupInfoQueue, Device.DrainInfoQueue,
// Device.TakeMessages, Device.MessageCounts, and debug object naming helpers.
//
// How: queries ID3D12Debug and ID3D12InfoQueue on Windows (pruned off other
// targets), pushes a PushStorageFilter for id 820 in Strict mode, and copies
// up to 256 message strings into the device heap.

import c.windows
import c.d3d12
import std.raw
import std.mem
import std.rt
import std.dl

const DebugModeOff: u8 = 0
const DebugModeValidate: u8 = 1
const DebugModeStrict: u8 = 2

const SevInfo: u8 = 0
const SevWarning: u8 = 1
const SevError: u8 = 2

const MaxStoredMsgs: int = 256

EnableDebugLayer(io: Io, strict: bool): Error!bool {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if !dl.Present(io, d3d12.D3D12GetDebugInterface) {
        if strict {
            fail DxFail(kind: ErrKind.MissingDebugLayer, code: 0, call: CallCreateDevice, at: -1)
        }
        return false
    }
    var dbg_ptr: *void = raw.Null[void]()
    var hr: windows.Hresult = 0
    unsafe "D3D12GetDebugInterface writes ID3D12Debug pointer into dbg_ptr" {
        hr = d3d12.D3D12GetDebugInterface(raw.Of(&d3d12.IidID3D12Debug), raw.OfMut(mut dbg_ptr))
        if hr >= 0 && !raw.IsNull(dbg_ptr) {
            dbg := raw.PtrCast[void, d3d12.ID3D12Debug](dbg_ptr)
            (*dbg).EnableDebugLayer()
            _ = (*dbg).Release()
            return true
        }
    }
    if strict {
        fail DxFail(kind: ErrKind.MissingDebugLayer, code: i64(hr), call: CallCreateDevice, at: -1)
    }
    return false
}

SetupInfoQueue(dev: *d3d12.ID3D12Device, mode: u8): *d3d12.ID3D12InfoQueue {
    if Target.os != Os.Windows || mode == DebugModeOff || raw.IsNull(dev) {
        return raw.Null[d3d12.ID3D12InfoQueue]()
    }
    var iq_ptr: *void = raw.Null[void]()
    unsafe "QueryInterface for ID3D12InfoQueue on live ID3D12Device" {
        hr := (*dev).QueryInterface(raw.Of(&d3d12.IidID3D12InfoQueue), raw.OfMut(mut iq_ptr))
        if hr < 0 || raw.IsNull(iq_ptr) {
            return raw.Null[d3d12.ID3D12InfoQueue]()
        }
        iq := raw.PtrCast[void, d3d12.ID3D12InfoQueue](iq_ptr)
        _ = (*iq).SetBreakOnSeverity(d3d12.D3d12MessageSeverityCorruption, 0)
        _ = (*iq).SetBreakOnSeverity(d3d12.D3d12MessageSeverityError, 0)
        _ = (*iq).SetBreakOnSeverity(d3d12.D3d12MessageSeverityWarning, 0)
        _ = (*iq).SetMessageCountLimit(1024)
        if mode == DebugModeStrict {
            var deny_ids: [2]d3d12.D3d12MessageId = [
                d3d12.D3d12MessageIdClearrendertargetviewMismatchingclearvalue,
                d3d12.D3d12MessageIdCleardepthstencilviewMismatchingclearvalue,
            ]
            var filter := mem.Zeroed[d3d12.D3d12InfoQueueFilter]()
            filter.deny_list.num_i_ds = 2
            filter.deny_list.p_id_list = raw.OfMut(mut deny_ids[0])
            _ = (*iq).PushStorageFilter(raw.OfMut(mut filter))
        }
        (*iq).ClearStoredMessages()
        return iq
    }
}

MapD3dSeverity(s: d3d12.D3d12MessageSeverity): u8 {
    if s == d3d12.D3d12MessageSeverityCorruption || s == d3d12.D3d12MessageSeverityError {
        return SevError
    }
    if s == d3d12.D3d12MessageSeverityWarning {
        return SevWarning
    }
    return SevInfo
}

/// Drains ID3D12InfoQueue into device messages; returns true when a failing
/// message (error in Validate/Strict, or warning in Strict) was recorded.
export Device.DrainInfoQueue(mut self): bool {
    if Target.os != Os.Windows || raw.IsNull(self.info_queue) {
        return false
    }
    iq := self.info_queue
    var count: windows.Uint64 = 0
    unsafe "info_queue is a live ID3D12InfoQueue" {
        count = (*iq).GetNumStoredMessages()
    }
    if count == 0 {
        return false
    }
    a := self.heap.Alloc()
    var failed := false
    var buf := HeapList[u8].New(a)
    defer buf.Free(a)
    var i: windows.Uint64 = 0
    for i < count {
        var byte_len: windows.SizeTType = 0
        var hr: windows.Hresult = 0
        unsafe "GetMessageA size query with null D3d12Message pointer" {
            hr = (*iq).GetMessageA(i, raw.Null[d3d12.D3d12Message](), raw.OfMut(mut byte_len))
        }
        if byte_len >= Cast[u64](SizeOf[d3d12.D3d12Message]()) {
            need := Cast[int](byte_len)
            if buf.Len() < need {
                extra := need - buf.Len()
                var ok_grow := true
                for _ in 0..extra {
                    buf.Push(a, 0) else {
                        ok_grow = false
                        break
                    }
                }
                if !ok_grow {
                    break
                }
            }
            unsafe "buf holds at least byte_len bytes for D3d12Message" {
                mp := raw.PtrCast[u8, d3d12.D3d12Message](raw.DataMut(buf.ItemsMut()))
                hr = (*iq).GetMessageA(i, mp, raw.OfMut(mut byte_len))
                if hr >= 0 {
                    msg := raw.Read(mp)
                    sev := MapD3dSeverity(msg.severity)
                    if sev == SevError {
                        self.total_err_count += 1
                        failed = true
                    } else if sev == SevWarning {
                        self.total_warn_count += 1
                        if self.debug_mode == DebugModeStrict {
                            failed = true
                        }
                    }
                    if self.msgs.Len() < MaxStoredMsgs && !raw.IsNull(msg.p_description) && msg.description_byte_length > 0 {
                        var dlen := Cast[int](msg.description_byte_length)
                        raw_bytes := raw.Slice(msg.p_description, dlen)
                        if dlen > 0 && raw_bytes[dlen - 1] == 0 {
                            dlen -= 1
                        }
                        if dlen > 0 {
                            off := self.msg_bytes.Len()
                            self.msg_bytes.Extend(a, raw_bytes[0..dlen]) else {}
                            copied := self.msg_bytes.Len() - off
                            self.msgs.Push(a, StoredMsg(severity: sev, id: i64(msg.id), off: off, len: copied)) else {}
                        }
                    }
                }
            }
        }
        i += 1
    }
    unsafe "info_queue is live" {
        (*iq).ClearStoredMessages()
    }
    return failed
}

/// Copies and clears stored debug messages into caller arena a. O(messages).
export Device.TakeMessages(mut self, a: &Arena): OutOfMemory!List[DebugMsg] from a {
    var out := List[DebugMsg].New(a)
    for i in 0..self.msgs.Len() {
        m := &self.msgs[i]
        raw_s := self.msg_bytes.Items()[m.off..(m.off + m.len)]
        var dup := try a.Slice[u8](raw_s.Len())
        (mut dup).CopyFrom(raw_s)
        txt := rt.StrFrom(dup[..]) else { "" }
        try out.Push(DebugMsg(severity: m.severity, id: m.id, text: txt))
    }
    self.msgs.Clear()
    self.msg_bytes.Clear()
    return out
}

/// Returns total debug error and warning counts observed on this device. O(1).
export Device.MessageCounts(&self): (errors: int, warnings: int) {
    return (errors: self.total_err_count, warnings: self.total_warn_count)
}

/// Reports whether stored debug messages await draining into std.gpu. O(1).
export Device.HasMessages(&self): bool {
    return self.msgs.Len() > 0
}

SetResourceName(res: *d3d12.ID3D12Resource, label: str) {
    if Target.os != Os.Windows || raw.IsNull(res) || label.Len() == 0 {
        return
    }
    unsafe "res is a live ID3D12Resource and label bytes are valid for SetPrivateData" {
        _ = (*res).SetPrivateData(
            raw.Of(&d3d12.WkpdidD3dDebugObjectName),
            Cast[windows.Uint](label.Len()),
            raw.PtrCast[u8, void](raw.StrData(label)),
        )
    }
}

SetRootSigName(sig: *d3d12.ID3D12RootSignature, label: str) {
    if Target.os != Os.Windows || raw.IsNull(sig) || label.Len() == 0 {
        return
    }
    unsafe "sig is a live ID3D12RootSignature" {
        _ = (*sig).SetPrivateData(
            raw.Of(&d3d12.WkpdidD3dDebugObjectName),
            Cast[windows.Uint](label.Len()),
            raw.PtrCast[u8, void](raw.StrData(label)),
        )
    }
}

SetPipelineName(pso: *d3d12.ID3D12PipelineState, label: str) {
    if Target.os != Os.Windows || raw.IsNull(pso) || label.Len() == 0 {
        return
    }
    unsafe "pso is a live ID3D12PipelineState" {
        _ = (*pso).SetPrivateData(
            raw.Of(&d3d12.WkpdidD3dDebugObjectName),
            Cast[windows.Uint](label.Len()),
            raw.PtrCast[u8, void](raw.StrData(label)),
        )
    }
}
