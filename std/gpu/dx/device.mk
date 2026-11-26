// Why: DXGI adapter enumeration, D3D12 device opening, feature/format
// capability queries, and full resource teardown must run off task stacks,
// honour Target.os guards, and leave zero native leaks at Close.
//
// What: Adapters, Open, Device.Close, and Device.SimulateDeviceLoss.
//
// How: uses dl.Present for dxgi.dll and d3d12.dll, runs adapter probing,
// CheckFeatureSupport (FL 11.0, SM 6.0, binding tier 2, 44 formats), and
// device/zero-buffer creation on runtime.Blocking, and releases all COM
// interfaces and descriptor/buffer heaps on Close.

import c.windows
import c.dxgi
import c.d3d12
import std.raw
import std.mem
import std.dl
import std.rt
import std.runtime

const MaxTexDim: int = 16384

Utf16ZLen(units: []windows.Wchar): int {
    var total := 0
    var i := 0
    for i < units.Len() {
        u := u32(units[i])
        if u == 0 {
            break
        }
        var cp: u32 = u
        if u >= 0xD800 && u <= 0xDBFF && i + 1 < units.Len() {
            lo := u32(units[i + 1])
            if lo >= 0xDC00 && lo <= 0xDFFF {
                cp = 0x10000 + ((u - 0xD800) << 10) + (lo - 0xDC00)
                i += 1
            } else {
                cp = 0xFFFD
            }
        } else if u >= 0xDC00 && u <= 0xDFFF {
            cp = 0xFFFD
        }
        if cp < 0x80 {
            total += 1
        } else if cp < 0x800 {
            total += 2
        } else if cp < 0x10000 {
            total += 3
        } else {
            total += 4
        }
        i += 1
    }
    return total
}

Utf16ZEncode(dst: mut []u8, units: []windows.Wchar) {
    var out := 0
    var i := 0
    for i < units.Len() {
        u := u32(units[i])
        if u == 0 {
            break
        }
        var cp: u32 = u
        if u >= 0xD800 && u <= 0xDBFF && i + 1 < units.Len() {
            lo := u32(units[i + 1])
            if lo >= 0xDC00 && lo <= 0xDFFF {
                cp = 0x10000 + ((u - 0xD800) << 10) + (lo - 0xDC00)
                i += 1
            } else {
                cp = 0xFFFD
            }
        } else if u >= 0xDC00 && u <= 0xDFFF {
            cp = 0xFFFD
        }
        if cp < 0x80 {
            dst[out] = Trunc[u8](cp)
            out += 1
        } else if cp < 0x800 {
            dst[out] = Trunc[u8](0xC0 | (cp >> 6))
            dst[out + 1] = Trunc[u8](0x80 | (cp & 0x3F))
            out += 2
        } else if cp < 0x10000 {
            dst[out] = Trunc[u8](0xE0 | (cp >> 12))
            dst[out + 1] = Trunc[u8](0x80 | ((cp >> 6) & 0x3F))
            dst[out + 2] = Trunc[u8](0x80 | (cp & 0x3F))
            out += 3
        } else {
            dst[out] = Trunc[u8](0xF0 | (cp >> 18))
            dst[out + 1] = Trunc[u8](0x80 | ((cp >> 12) & 0x3F))
            dst[out + 2] = Trunc[u8](0x80 | ((cp >> 6) & 0x3F))
            dst[out + 3] = Trunc[u8](0x80 | (cp & 0x3F))
            out += 4
        }
        i += 1
    }
}

Utf16ZToStr(a: &Arena, units: []windows.Wchar): OutOfMemory!str from a {
    n := Utf16ZLen(units)
    if n == 0 {
        return ""
    }
    var out := try a.Slice[u8](n)
    Utf16ZEncode(mut out[..], units)
    return rt.StrFrom(out[..]) else { "" }
}

Utf16ZToHeapStr(a: &Alloc, units: []windows.Wchar): OutOfMemory!(ptr: *u8, len: int, text: str) from a {
    n := Utf16ZLen(units)
    if n == 0 {
        return (ptr: raw.Null[u8](), len: 0, text: "")
    }
    unsafe "allocates n bytes on a and encodes valid UTF-8 into them" {
        p := a.Bytes(n, 1)
        if raw.IsNull(p) {
            fail OutOfMemory
        }
        s := raw.SliceMut(p, n)
        Utf16ZEncode(s, units)
        txt := rt.StrFrom(raw.Slice(p, n)) else { "" }
        return (ptr: p, len: n, text: txt)
    }
}

PackLuid(l: windows.Luid): u64 {
    return u64(l.low_part) | (u64(Cast[u32](l.high_part)) << 32)
}

UnpackLuid(v: u64): windows.Luid {
    return windows.Luid(
        low_part: Cast[windows.Dword](v & 0xFFFFFFFF),
        high_part: Cast[windows.Long](Cast[u32](v >> 32)),
    )
}

CheckD3d12BaseCaps(dev: *d3d12.ID3D12Device): bool {
    if Target.os != Os.Windows || raw.IsNull(dev) {
        return false
    }
    unsafe "dev is a live ID3D12Device" {
        var sm := mem.Zeroed[d3d12.D3d12FeatureDataShaderModel]()
        sm.highest_shader_model = d3d12.D3dShaderModel60
        hr_sm := (*dev).CheckFeatureSupport(
            d3d12.D3d12FeatureShaderModel,
            raw.PtrCast[d3d12.D3d12FeatureDataShaderModel, void](raw.OfMut(mut sm)),
            Cast[windows.Uint](SizeOf[d3d12.D3d12FeatureDataShaderModel]()),
        )
        if hr_sm < 0 || sm.highest_shader_model < d3d12.D3dShaderModel60 {
            return false
        }
        var opts := mem.Zeroed[d3d12.D3d12FeatureDataD3d12Options]()
        hr_op := (*dev).CheckFeatureSupport(
            d3d12.D3d12FeatureD3d12Options,
            raw.PtrCast[d3d12.D3d12FeatureDataD3d12Options, void](raw.OfMut(mut opts)),
            Cast[windows.Uint](SizeOf[d3d12.D3d12FeatureDataD3d12Options]()),
        )
        if hr_op < 0 || opts.resource_binding_tier < d3d12.D3d12ResourceBindingTier2 {
            return false
        }
    }
    return true
}

QueryFormatCapsRow(
    dev: *d3d12.ID3D12Device,
    dxgi_res: u32,
    dxgi_rtv_dsv: u32,
    dxgi_srv: u32,
    f32_filter: bool,
): FormatRowCaps {
    var out := FormatRowCaps(
        sampled: false,
        filterable: false,
        render_target: false,
        blendable: false,
        multisample: false,
        resolve: false,
        depth_stencil: false,
    )
    if Target.os != Os.Windows || raw.IsNull(dev) {
        return out
    }
    unsafe "dev is a live ID3D12Device" {
        if dxgi_srv != 0 {
            var fs := mem.Zeroed[d3d12.D3d12FeatureDataFormatSupport]()
            fs.format = Cast[dxgi.DxgiFormat](dxgi_srv)
            if (*dev).CheckFeatureSupport(
                d3d12.D3d12FeatureFormatSupport,
                raw.PtrCast[d3d12.D3d12FeatureDataFormatSupport, void](raw.OfMut(mut fs)),
                Cast[windows.Uint](SizeOf[d3d12.D3d12FeatureDataFormatSupport]()),
            ) >= 0 {
                s1 := fs.support1
                has_sample := (s1 & d3d12.D3d12FormatSupport1ShaderSample) != 0
                has_load := (s1 & d3d12.D3d12FormatSupport1ShaderLoad) != 0
                out.sampled = has_sample || has_load
                is_f32 := dxgi_srv == 41 || dxgi_srv == 16 || dxgi_srv == 2
                out.filterable = if is_f32 { f32_filter && has_sample } else { has_sample }
            }
        }
        q_rt := if dxgi_rtv_dsv != 0 { dxgi_rtv_dsv } else { dxgi_res }
        if q_rt != 0 {
            var fs := mem.Zeroed[d3d12.D3d12FeatureDataFormatSupport]()
            fs.format = Cast[dxgi.DxgiFormat](q_rt)
            if (*dev).CheckFeatureSupport(
                d3d12.D3d12FeatureFormatSupport,
                raw.PtrCast[d3d12.D3d12FeatureDataFormatSupport, void](raw.OfMut(mut fs)),
                Cast[windows.Uint](SizeOf[d3d12.D3d12FeatureDataFormatSupport]()),
            ) >= 0 {
                s1 := fs.support1
                out.render_target = (s1 & d3d12.D3d12FormatSupport1RenderTarget) != 0
                out.blendable = (s1 & d3d12.D3d12FormatSupport1Blendable) != 0
                out.multisample = (s1 & d3d12.D3d12FormatSupport1MultisampleRendertarget) != 0
                out.resolve = (s1 & d3d12.D3d12FormatSupport1MultisampleResolve) != 0
                out.depth_stencil = (s1 & d3d12.D3d12FormatSupport1DepthStencil) != 0
            }
        }
    }
    return out
}

CapsSatisfy(got: FormatRowCaps, req: FormatRowCaps): bool {
    if req.sampled && !got.sampled { return false }
    if req.filterable && !got.filterable { return false }
    if req.render_target && !got.render_target { return false }
    if req.blendable && !got.blendable { return false }
    if req.multisample && !got.multisample { return false }
    if req.resolve && !got.resolve { return false }
    if req.depth_stencil && !got.depth_stencil { return false }
    return true
}

struct RawAdapterRec {
    luid: u64
    vendor: u32
    device: u32
    is_software: bool
    desc_utf16: [128]windows.Wchar
}

struct EnumAdaptersArgs {
    a: *Arena
    out: *List[AdapterInfo]
    hr: windows.Hresult
    oom: bool
}

ProbeAdapterCandidate(ad: *dxgi.IdxgiAdapter1, format_dxgi: []u32, format_guar: []FormatRowCaps): ?RawAdapterRec {
    if Target.os != Os.Windows || raw.IsNull(ad) {
        return none
    }
    var desc := mem.Zeroed[dxgi.DxgiAdapterDesc1]()
    var dev_ptr: *void = raw.Null[void]()
    unsafe "ad is a live IDXGIAdapter1" {
        if (*ad).GetDesc1(raw.OfMut(mut desc)) < 0 {
            return none
        }
        dhr := d3d12.D3D12CreateDevice(
            raw.PtrCast[dxgi.IdxgiAdapter1, windows.IUnknown](ad),
            d3d12.D3dFeatureLevel110,
            raw.Of(&d3d12.IidID3D12Device),
            raw.OfMut(mut dev_ptr),
        )
        if dhr < 0 || raw.IsNull(dev_ptr) {
            return none
        }
        dev := raw.PtrCast[void, d3d12.ID3D12Device](dev_ptr)
        ok := CheckD3d12BaseCaps(dev)
        var caps_ok := ok
        if caps_ok && format_dxgi.Len() >= 132 && format_guar.Len() >= 44 {
            for fi in 0..44 {
                dx_res := format_dxgi[fi * 3]
                dx_rt := format_dxgi[fi * 3 + 1]
                dx_srv := format_dxgi[fi * 3 + 2]
                row := QueryFormatCapsRow(dev, dx_res, dx_rt, dx_srv, false)
                if !CapsSatisfy(row, format_guar[fi]) {
                    caps_ok = false
                    break
                }
            }
        }
        _ = (*dev).Release()
        if !caps_ok {
            return none
        }
    }
    is_sw := (desc.flags & dxgi.DxgiAdapterFlagSoftware) != 0
    return some(RawAdapterRec(
        luid: PackLuid(desc.adapter_luid),
        vendor: desc.vendor_id,
        device: desc.device_id,
        is_software: is_sw,
        desc_utf16: desc.description,
    ))
}

struct ListJobState {
    dxgi_ptr: *u32
    dxgi_len: int
    guar_ptr: *FormatRowCaps
    guar_len: int
    recs: [16]RawAdapterRec
    rec_count: int
    hr: windows.Hresult
}

ListAdaptersJob(addr: uint): uint {
    if Target.os != Os.Windows {
        return addr
    }
    unsafe "addr points to live ListJobState on caller frame" {
        p := raw.FromAddr[ListJobState](addr)
        var fac_ptr: *void = raw.Null[void]()
        fhr := dxgi.CreateDXGIFactory2(0, raw.Of(&dxgi.IidIdxgiFactory6), raw.OfMut(mut fac_ptr))
        (*p).hr = fhr
        if fhr < 0 || raw.IsNull(fac_ptr) {
            return addr
        }
        fac := raw.PtrCast[void, dxgi.IdxgiFactory6](fac_ptr)
        dxgi_sl := raw.Slice((*p).dxgi_ptr, (*p).dxgi_len)
        guar_sl := raw.Slice((*p).guar_ptr, (*p).guar_len)

        var idx: windows.Uint = 0
        for (*p).rec_count < 16 {
            var ad_ptr: *void = raw.Null[void]()
            ehr := (*fac).EnumAdapterByGpuPreference(
                idx,
                dxgi.DxgiGpuPreferenceHighPerformance,
                raw.Of(&dxgi.IidIdxgiAdapter1),
                raw.OfMut(mut ad_ptr),
            )
            if ehr < 0 || raw.IsNull(ad_ptr) {
                break
            }
            ad := raw.PtrCast[void, dxgi.IdxgiAdapter1](ad_ptr)
            if rec_opt := ProbeAdapterCandidate(ad, dxgi_sl, guar_sl) {
                if !rec_opt.is_software {
                    var seen := false
                    for k in 0..(*p).rec_count {
                        if (*p).recs[k].luid == rec_opt.luid {
                            seen = true
                            break
                        }
                    }
                    if !seen {
                        (*p).recs[(*p).rec_count] = rec_opt
                        (*p).rec_count += 1
                    }
                }
            }
            _ = (*ad).Release()
            idx += 1
        }

        // Append WARP software adapter if present.
        var warp_ptr: *void = raw.Null[void]()
        if (*fac).EnumWarpAdapter(raw.Of(&dxgi.IidIdxgiAdapter1), raw.OfMut(mut warp_ptr)) >= 0 && !raw.IsNull(warp_ptr) {
            wad := raw.PtrCast[void, dxgi.IdxgiAdapter1](warp_ptr)
            if rec_opt := ProbeAdapterCandidate(wad, dxgi_sl, guar_sl) {
                if (*p).rec_count < 16 {
                    (*p).recs[(*p).rec_count] = rec_opt
                    (*p).rec_count += 1
                }
            }
            _ = (*wad).Release()
        }
        _ = (*fac).Release()
    }
    return addr
}

/// Lists qualifying D3D12 adapters (hardware first, then WARP) via DXGI on runtime.Blocking.
export Adapters(
    a: &Arena,
    io: Io,
    format_dxgi_triples: []u32,
    format_guaranteed: []FormatRowCaps,
): Error!List[AdapterInfo] from a {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if !dl.Present(io, dxgi.CreateDXGIFactory2) || !dl.Present(io, d3d12.D3D12CreateDevice) {
        fail Unsupported
    }
    var st := ListJobState(
        dxgi_ptr: raw.Data(format_dxgi_triples),
        dxgi_len: format_dxgi_triples.Len(),
        guar_ptr: raw.Data(format_guaranteed),
        guar_len: format_guaranteed.Len(),
        recs: [mem.Zeroed[RawAdapterRec](); 16],
        rec_count: 0,
        hr: 0,
    )
    _ = try runtime.Blocking(io, raw.Addr(raw.OfMut(mut st)), ListAdaptersJob)
    if st.hr < 0 {
        fail DxFail(kind: ErrKind.Driver, code: i64(st.hr), call: CallCreateFactory, at: -1)
    }
    var out := List[AdapterInfo].New(a)
    for i in 0..st.rec_count {
        r := &st.recs[i]
        name := try Utf16ZToStr(a, r.desc_utf16[..])
        try out.Push(AdapterInfo(
            luid: r.luid,
            name: name,
            vendor: r.vendor,
            device: r.device,
            is_software: r.is_software,
            max_texture_size: MaxTexDim,
        ))
    }
    return out
}

struct OpenJobState {
    opts: OpenOptions
    dxgi_ptr: *u32
    dxgi_len: int
    guar_ptr: *FormatRowCaps
    guar_len: int
    factory: *dxgi.IdxgiFactory6
    dev: *d3d12.ID3D12Device
    queue: *d3d12.ID3D12CommandQueue
    info_queue: *d3d12.ID3D12InfoQueue
    cmd_alloc: *d3d12.ID3D12CommandAllocator
    cmd_list: *d3d12.ID3D12GraphicsCommandList
    fence: *d3d12.ID3D12Fence
    fence_event: uint
    zero_buf: *d3d12.ID3D12Resource
    tearing_ok: bool
    ad_rec: RawAdapterRec
    caps: [44]FormatRowCaps
    err_kind: ErrKind
    has_err: bool
    hr: windows.Hresult
    call: u8
}

TryCreateDeviceOnAdapter(
    ad: *dxgi.IdxgiAdapter1,
    opts: &OpenOptions,
    dxgi_sl: []u32,
    guar_sl: []FormatRowCaps,
    out_dev: mut *d3d12.ID3D12Device,
    out_caps: mut [44]FormatRowCaps,
    out_rec: mut RawAdapterRec,
): bool {
    if Target.os != Os.Windows || raw.IsNull(ad) {
        return false
    }
    var desc := mem.Zeroed[dxgi.DxgiAdapterDesc1]()
    unsafe "ad is a live IDXGIAdapter1" {
        if (*ad).GetDesc1(raw.OfMut(mut desc)) < 0 {
            return false
        }
    }
    is_sw := (desc.flags & dxgi.DxgiAdapterFlagSoftware) != 0
    if opts.software && !is_sw {
        return false
    }
    var dev_ptr: *void = raw.Null[void]()
    unsafe "ad is live" {
        dhr := d3d12.D3D12CreateDevice(
            raw.PtrCast[dxgi.IdxgiAdapter1, windows.IUnknown](ad),
            d3d12.D3dFeatureLevel110,
            raw.Of(&d3d12.IidID3D12Device),
            raw.OfMut(mut dev_ptr),
        )
        if dhr < 0 || raw.IsNull(dev_ptr) {
            return false
        }
        dev := raw.PtrCast[void, d3d12.ID3D12Device](dev_ptr)
        if !CheckD3d12BaseCaps(dev) {
            _ = (*dev).Release()
            return false
        }
        var ok := true
        for fi in 0..44 {
            dx_res := dxgi_sl[fi * 3]
            dx_rt := dxgi_sl[fi * 3 + 1]
            dx_srv := dxgi_sl[fi * 3 + 2]
            row := QueryFormatCapsRow(dev, dx_res, dx_rt, dx_srv, opts.req_f32_filter)
            if !CapsSatisfy(row, guar_sl[fi]) {
                ok = false
                break
            }
            if opts.req_f32_filter && (dx_srv == 41 || dx_srv == 16 || dx_srv == 2) && !row.filterable {
                ok = false
                break
            }
            out_caps[fi] = row
        }
        if !ok {
            _ = (*dev).Release()
            return false
        }
        *out_dev = dev
    }
    *out_rec = RawAdapterRec(
        luid: PackLuid(desc.adapter_luid),
        vendor: desc.vendor_id,
        device: desc.device_id,
        is_software: is_sw,
        desc_utf16: desc.description,
    )
    return true
}

CleanupOpenPartial(p: *OpenJobState) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "cleanup partially created COM objects in OpenJobState" {
        if !raw.IsNull((*p).zero_buf) { _ = (*(*p).zero_buf).Release() }
        if (*p).fence_event != 0 { _ = windows.CloseHandle(raw.FromAddr[void]((*p).fence_event)) }
        if !raw.IsNull((*p).fence) { _ = (*(*p).fence).Release() }
        if !raw.IsNull((*p).cmd_list) { _ = (*(*p).cmd_list).Release() }
        if !raw.IsNull((*p).cmd_alloc) { _ = (*(*p).cmd_alloc).Release() }
        if !raw.IsNull((*p).info_queue) { _ = (*(*p).info_queue).Release() }
        if !raw.IsNull((*p).queue) { _ = (*(*p).queue).Release() }
        if !raw.IsNull((*p).dev) { _ = (*(*p).dev).Release() }
        if !raw.IsNull((*p).factory) { _ = (*(*p).factory).Release() }
    }
}

InitZeroBuffer(p: *OpenJobState): bool {
    if Target.os != Os.Windows {
        return false
    }
    unsafe "p.dev, p.queue, p.cmd_alloc, p.cmd_list, p.fence are live" {
        hp_def := DefaultHeapProps(d3d12.D3d12HeapTypeDefault)
        desc := BufferResourceDesc(Cast[u64](DefaultZeroBufBytes), d3d12.D3d12ResourceFlagNone)
        var z_ptr: *void = raw.Null[void]()
        hr := (*(*p).dev).CreateCommittedResource(
            raw.Of(&hp_def),
            d3d12.D3d12HeapFlagNone,
            raw.Of(&desc),
            d3d12.D3d12ResourceStateCommon,
            raw.Null[d3d12.D3d12ClearValue](),
            raw.Of(&d3d12.IidID3D12Resource),
            raw.OfMut(mut z_ptr),
        )
        if hr < 0 || raw.IsNull(z_ptr) {
            (*p).has_err = true
            (*p).err_kind = if hr == HresultOutOfMemory { ErrKind.OutOfDeviceMemory } else { ErrKind.Driver }
            (*p).hr = hr
            (*p).call = CallCreateResource
            return false
        }
        zbuf := raw.PtrCast[void, d3d12.ID3D12Resource](z_ptr)
        (*p).zero_buf = zbuf

        hp_up := DefaultHeapProps(d3d12.D3d12HeapTypeUpload)
        var u_ptr: *void = raw.Null[void]()
        uhr := (*(*p).dev).CreateCommittedResource(
            raw.Of(&hp_up),
            d3d12.D3d12HeapFlagNone,
            raw.Of(&desc),
            d3d12.D3d12ResourceStateGenericRead,
            raw.Null[d3d12.D3d12ClearValue](),
            raw.Of(&d3d12.IidID3D12Resource),
            raw.OfMut(mut u_ptr),
        )
        if uhr < 0 || raw.IsNull(u_ptr) {
            (*p).has_err = true
            (*p).err_kind = if uhr == HresultOutOfMemory { ErrKind.OutOfDeviceMemory } else { ErrKind.Driver }
            (*p).hr = uhr
            (*p).call = CallCreateResource
            return false
        }
        ubuf := raw.PtrCast[void, d3d12.ID3D12Resource](u_ptr)
        var mp: *void = raw.Null[void]()
        mhr := (*ubuf).Map(0, raw.Null[d3d12.D3d12Range](), raw.OfMut(mut mp))
        if mhr < 0 || raw.IsNull(mp) {
            _ = (*ubuf).Release()
            (*p).has_err = true
            (*p).err_kind = ErrKind.Driver
            (*p).hr = mhr
            (*p).call = CallMap
            return false
        }
        raw.Fill(raw.PtrCast[void, u8](mp), 0, DefaultZeroBufBytes)
        empty_r := mem.Zeroed[d3d12.D3d12Range]()
        (*ubuf).Unmap(0, raw.Of(&empty_r))

        // Copy zero upload buffer -> zero_buf and transition zero_buf to COPY_SOURCE.
        _ = (*(*p).cmd_alloc).Reset()
        _ = (*(*p).cmd_list).Reset((*p).cmd_alloc, raw.Null[d3d12.ID3D12PipelineState]())
        (*(*p).cmd_list).CopyBufferRegion(zbuf, 0, ubuf, 0, Cast[u64](DefaultZeroBufBytes))
        EmitTransition((*p).cmd_list, zbuf, D3d12AllSubresources, d3d12.D3d12ResourceStateCopyDest, d3d12.D3d12ResourceStateCopySource)
        _ = (*(*p).cmd_list).Close()
        var lists: [1]*d3d12.ID3D12CommandList = [raw.PtrCast[d3d12.ID3D12GraphicsCommandList, d3d12.ID3D12CommandList]((*p).cmd_list)]
        (*(*p).queue).ExecuteCommandLists(1, raw.Of(&lists[0]))
        _ = (*(*p).queue).Signal((*p).fence, 1)
        if (*(*p).fence).GetCompletedValue() < 1 {
            ev := raw.FromAddr[void]((*p).fence_event)
            _ = windows.ResetEvent(ev)
            _ = (*(*p).fence).SetEventOnCompletion(1, ev)
            _ = windows.WaitForSingleObject(ev, 0xFFFFFFFF)
        }
        _ = (*ubuf).Release()
    }
    return true
}

OpenDeviceJob(addr: uint): uint {
    if Target.os != Os.Windows {
        return addr
    }
    unsafe "addr points to live OpenJobState on caller frame" {
        p := raw.FromAddr[OpenJobState](addr)
        fac_flags: windows.Uint = if (*p).opts.debug != DebugModeOff { DxgiCreateFactoryDebug } else { 0 }
        var fac_ptr: *void = raw.Null[void]()
        var fhr := dxgi.CreateDXGIFactory2(fac_flags, raw.Of(&dxgi.IidIdxgiFactory6), raw.OfMut(mut fac_ptr))
        if (fhr < 0 || raw.IsNull(fac_ptr)) && fac_flags != 0 {
            fhr = dxgi.CreateDXGIFactory2(0, raw.Of(&dxgi.IidIdxgiFactory6), raw.OfMut(mut fac_ptr))
        }
        if fhr < 0 || raw.IsNull(fac_ptr) {
            (*p).has_err = true
            (*p).err_kind = ErrKind.Driver
            (*p).hr = fhr
            (*p).call = CallCreateFactory
            return addr
        }
        fac := raw.PtrCast[void, dxgi.IdxgiFactory6](fac_ptr)
        (*p).factory = fac

        var tearing: windows.Bool = 0
        if (*fac).CheckFeatureSupport(
            dxgi.DxgiFeaturePresentAllowTearing,
            raw.PtrCast[windows.Bool, void](raw.OfMut(mut tearing)),
            Cast[windows.Uint](SizeOf[windows.Bool]()),
        ) >= 0 {
            (*p).tearing_ok = tearing != 0
        }

        dxgi_sl := raw.Slice((*p).dxgi_ptr, (*p).dxgi_len)
        guar_sl := raw.Slice((*p).guar_ptr, (*p).guar_len)

        var chosen_dev := raw.Null[d3d12.ID3D12Device]()
        var found := false
        if (*p).opts.has_luid {
            var ad_ptr: *void = raw.Null[void]()
            luid := UnpackLuid((*p).opts.target_luid)
            if (*fac).EnumAdapterByLuid(luid, raw.Of(&dxgi.IidIdxgiAdapter1), raw.OfMut(mut ad_ptr)) >= 0 && !raw.IsNull(ad_ptr) {
                ad := raw.PtrCast[void, dxgi.IdxgiAdapter1](ad_ptr)
                found = TryCreateDeviceOnAdapter(ad, &(*p).opts, dxgi_sl, guar_sl, mut chosen_dev, mut (*p).caps, mut (*p).ad_rec)
                _ = (*ad).Release()
            }
        } else if (*p).opts.software {
            var warp_ptr: *void = raw.Null[void]()
            if (*fac).EnumWarpAdapter(raw.Of(&dxgi.IidIdxgiAdapter1), raw.OfMut(mut warp_ptr)) >= 0 && !raw.IsNull(warp_ptr) {
                wad := raw.PtrCast[void, dxgi.IdxgiAdapter1](warp_ptr)
                found = TryCreateDeviceOnAdapter(wad, &(*p).opts, dxgi_sl, guar_sl, mut chosen_dev, mut (*p).caps, mut (*p).ad_rec)
                _ = (*wad).Release()
            }
        } else {
            pref := if (*p).opts.power == 1 {
                dxgi.DxgiGpuPreferenceHighPerformance
            } else if (*p).opts.power == 2 {
                dxgi.DxgiGpuPreferenceMinimumPower
            } else {
                dxgi.DxgiGpuPreferenceUnspecified
            }
            var idx: windows.Uint = 0
            for {
                var ad_ptr: *void = raw.Null[void]()
                if (*fac).EnumAdapterByGpuPreference(idx, pref, raw.Of(&dxgi.IidIdxgiAdapter1), raw.OfMut(mut ad_ptr)) < 0 || raw.IsNull(ad_ptr) {
                    break
                }
                ad := raw.PtrCast[void, dxgi.IdxgiAdapter1](ad_ptr)
                var desc := mem.Zeroed[dxgi.DxgiAdapterDesc1]()
                _ = (*ad).GetDesc1(raw.OfMut(mut desc))
                if (desc.flags & dxgi.DxgiAdapterFlagSoftware) == 0 {
                    if TryCreateDeviceOnAdapter(ad, &(*p).opts, dxgi_sl, guar_sl, mut chosen_dev, mut (*p).caps, mut (*p).ad_rec) {
                        found = true
                        _ = (*ad).Release()
                        break
                    }
                }
                _ = (*ad).Release()
                idx += 1
            }
            if !found {
                var warp_ptr: *void = raw.Null[void]()
                if (*fac).EnumWarpAdapter(raw.Of(&dxgi.IidIdxgiAdapter1), raw.OfMut(mut warp_ptr)) >= 0 && !raw.IsNull(warp_ptr) {
                    wad := raw.PtrCast[void, dxgi.IdxgiAdapter1](warp_ptr)
                    found = TryCreateDeviceOnAdapter(wad, &(*p).opts, dxgi_sl, guar_sl, mut chosen_dev, mut (*p).caps, mut (*p).ad_rec)
                    _ = (*wad).Release()
                }
            }
        }
        if !found || raw.IsNull(chosen_dev) {
            CleanupOpenPartial(p)
            (*p).has_err = true
            (*p).err_kind = ErrKind.NoAdapter
            (*p).hr = 0
            (*p).call = CallCreateDevice
            return addr
        }
        (*p).dev = chosen_dev
        (*p).info_queue = SetupInfoQueue(chosen_dev, (*p).opts.debug)

        var qdesc := mem.Zeroed[d3d12.D3d12CommandQueueDesc]()
        qdesc.type_ = d3d12.D3d12CommandListTypeDirect
        var q_ptr: *void = raw.Null[void]()
        qhr := (*chosen_dev).CreateCommandQueue(raw.Of(&qdesc), raw.Of(&d3d12.IidID3D12CommandQueue), raw.OfMut(mut q_ptr))
        if qhr < 0 || raw.IsNull(q_ptr) {
            CleanupOpenPartial(p)
            (*p).has_err = true
            (*p).err_kind = ErrKind.Driver
            (*p).hr = qhr
            (*p).call = CallCreateDevice
            return addr
        }
        (*p).queue = raw.PtrCast[void, d3d12.ID3D12CommandQueue](q_ptr)

        var ca_ptr: *void = raw.Null[void]()
        ahr := (*chosen_dev).CreateCommandAllocator(d3d12.D3d12CommandListTypeDirect, raw.Of(&d3d12.IidID3D12CommandAllocator), raw.OfMut(mut ca_ptr))
        if ahr < 0 || raw.IsNull(ca_ptr) {
            CleanupOpenPartial(p)
            (*p).has_err = true
            (*p).err_kind = ErrKind.Driver
            (*p).hr = ahr
            (*p).call = CallCreateDevice
            return addr
        }
        (*p).cmd_alloc = raw.PtrCast[void, d3d12.ID3D12CommandAllocator](ca_ptr)

        var cl_ptr: *void = raw.Null[void]()
        lhr := (*chosen_dev).CreateCommandList(
            0,
            d3d12.D3d12CommandListTypeDirect,
            (*p).cmd_alloc,
            raw.Null[d3d12.ID3D12PipelineState](),
            raw.Of(&d3d12.IidID3D12GraphicsCommandList),
            raw.OfMut(mut cl_ptr),
        )
        if lhr < 0 || raw.IsNull(cl_ptr) {
            CleanupOpenPartial(p)
            (*p).has_err = true
            (*p).err_kind = ErrKind.Driver
            (*p).hr = lhr
            (*p).call = CallCreateDevice
            return addr
        }
        cl := raw.PtrCast[void, d3d12.ID3D12GraphicsCommandList](cl_ptr)
        _ = (*cl).Close()
        (*p).cmd_list = cl

        var fn_ptr: *void = raw.Null[void]()
        fnhr := (*chosen_dev).CreateFence(0, d3d12.D3d12FenceFlagNone, raw.Of(&d3d12.IidID3D12Fence), raw.OfMut(mut fn_ptr))
        if fnhr < 0 || raw.IsNull(fn_ptr) {
            CleanupOpenPartial(p)
            (*p).has_err = true
            (*p).err_kind = ErrKind.Driver
            (*p).hr = fnhr
            (*p).call = CallCreateDevice
            return addr
        }
        (*p).fence = raw.PtrCast[void, d3d12.ID3D12Fence](fn_ptr)

        ev := windows.CreateEventW(raw.Null[windows.SecurityAttributes](), 1, 0, raw.Null[windows.Wchar]())
        if raw.IsNull(ev) {
            CleanupOpenPartial(p)
            (*p).has_err = true
            (*p).err_kind = ErrKind.Driver
            (*p).hr = 0
            (*p).call = CallCreateDevice
            return addr
        }
        (*p).fence_event = raw.Addr(ev)

        if !InitZeroBuffer(p) {
            CleanupOpenPartial(p)
            return addr
        }
    }
    return addr
}

/// Opens a Direct3D 12 device, checking requirements and format capabilities.
export Open(
    heap: &Heap,
    io: Io,
    opts: OpenOptions,
    format_dxgi_triples: []u32,
    format_guaranteed: []FormatRowCaps,
): Error!(dev: Device, info: AdapterInfo, caps: [44]FormatRowCaps) from heap {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if !dl.Present(io, dxgi.CreateDXGIFactory2) || !dl.Present(io, d3d12.D3D12CreateDevice) {
        fail Unsupported
    }
    if opts.debug != DebugModeOff {
        _ = try EnableDebugLayer(io, opts.debug == DebugModeStrict)
    }

    var job := OpenJobState(
        opts: opts,
        dxgi_ptr: raw.Data(format_dxgi_triples),
        dxgi_len: format_dxgi_triples.Len(),
        guar_ptr: raw.Data(format_guaranteed),
        guar_len: format_guaranteed.Len(),
        factory: raw.Null[dxgi.IdxgiFactory6](),
        dev: raw.Null[d3d12.ID3D12Device](),
        queue: raw.Null[d3d12.ID3D12CommandQueue](),
        info_queue: raw.Null[d3d12.ID3D12InfoQueue](),
        cmd_alloc: raw.Null[d3d12.ID3D12CommandAllocator](),
        cmd_list: raw.Null[d3d12.ID3D12GraphicsCommandList](),
        fence: raw.Null[d3d12.ID3D12Fence](),
        fence_event: 0,
        zero_buf: raw.Null[d3d12.ID3D12Resource](),
        tearing_ok: false,
        ad_rec: mem.Zeroed[RawAdapterRec](),
        caps: [mem.Zeroed[FormatRowCaps](); 44],
        err_kind: ErrKind.Unsupported,
        has_err: false,
        hr: 0,
        call: 0,
    )
    _ = runtime.Blocking(io, raw.Addr(raw.OfMut(mut job)), OpenDeviceJob) else e {
        CleanupOpenPartial(raw.OfMut(mut job))
        fail e
    }
    if job.has_err {
        fail DxFail(kind: job.err_kind, code: i64(job.hr), call: job.call, at: -1)
    }

    a := heap.Alloc()
    var cbv_heap := CreateDescHeap(a, job.dev, d3d12.D3d12DescriptorHeapTypeCbvSrvUav, Cast[u64](opts.descriptors), true) else e {
        CleanupOpenPartial(raw.OfMut(mut job))
        fail e
    }
    var smp_heap := CreateDescHeap(a, job.dev, d3d12.D3d12DescriptorHeapTypeSampler, 2048, true) else e {
        cbv_heap.Close(a)
        CleanupOpenPartial(raw.OfMut(mut job))
        fail e
    }
    var rtv_heap := CreateDescHeap(a, job.dev, d3d12.D3d12DescriptorHeapTypeRtv, 1024, false) else e {
        smp_heap.Close(a)
        cbv_heap.Close(a)
        CleanupOpenPartial(raw.OfMut(mut job))
        fail e
    }
    var dsv_heap := CreateDescHeap(a, job.dev, d3d12.D3d12DescriptorHeapTypeDsv, 256, false) else e {
        rtv_heap.Close(a)
        smp_heap.Close(a)
        cbv_heap.Close(a)
        CleanupOpenPartial(raw.OfMut(mut job))
        fail e
    }
    // Reserve slot 0 on CPU RTV/DSV heaps for scratch zeroing clears.
    _ = rtv_heap.AllocRange(a, 1)
    _ = dsv_heap.AllocRange(a, 1)

    var cmd_allocs := HeapList[CmdAllocRec].New(a)
    cmd_allocs.Push(a, CmdAllocRec(alloc: job.cmd_alloc, serial: 1)) else e {
        dsv_heap.Close(a)
        rtv_heap.Close(a)
        smp_heap.Close(a)
        cbv_heap.Close(a)
        cmd_allocs.Free(a)
        CleanupOpenPartial(raw.OfMut(mut job))
        fail e
    }

    (ptr: name_ptr, len: name_len, text: name) := Utf16ZToHeapStr(a, job.ad_rec.desc_utf16[..]) else e {
        dsv_heap.Close(a)
        rtv_heap.Close(a)
        smp_heap.Close(a)
        cbv_heap.Close(a)
        cmd_allocs.Free(a)
        CleanupOpenPartial(raw.OfMut(mut job))
        fail e
    }

    var dev := Device(
        heap: heap,
        factory: job.factory,
        dev: job.dev,
        queue: job.queue,
        info_queue: job.info_queue,
        cmd_list: job.cmd_list,
        cmd_allocs: cmd_allocs,
        fence: job.fence,
        fence_event: job.fence_event,
        debug_mode: opts.debug,
        frames_in_flight: opts.frames_in_flight,
        tearing_ok: job.tearing_ok,
        lost: false,
        loss_reason: ErrKind.DeviceLostUnknown,
        last_submitted: 1,
        completed_serial: 1,
        present_pending: false,
        cbv_srv_uav_heap: cbv_heap,
        sampler_heap: smp_heap,
        rtv_heap: rtv_heap,
        dsv_heap: dsv_heap,
        sampler_cache: HeapList[SamplerCacheEntry].New(a),
        buf_heaps: HeapList[PlacedHeap].New(a),
        zero_buf: job.zero_buf,
        zero_buf_size: Cast[u64](DefaultZeroBufBytes),
        upload_chunks: HeapList[UploadChunk].New(a),
        cur_upload_idx: -1,
        staging_bufs: HeapList[StagingBuf].New(a),
        pending_zeros: HeapList[PendingZero].New(a),
        pending_uploads: HeapList[PendingUploadCopy].New(a),
        msgs: HeapList[StoredMsg].New(a),
        msg_bytes: HeapList[u8].New(a),
        name_ptr: name_ptr,
        name_len: name_len,
        err_count: 0,
        warn_count: 0,
        total_err_count: 0,
        total_warn_count: 0,
    )
    _ = dev.DrainInfoQueue()

    info := AdapterInfo(
        luid: job.ad_rec.luid,
        name: name,
        vendor: job.ad_rec.vendor,
        device: job.ad_rec.device,
        is_software: job.ad_rec.is_software,
        max_texture_size: MaxTexDim,
    )
    return (dev: dev, info: info, caps: job.caps)
}

/// Triggers ID3D12Device5::RemoveDevice on this device for device-loss tests.
export Device.SimulateDeviceLoss(mut self) {
    if Target.os != Os.Windows || raw.IsNull(self.dev) {
        return
    }
    unsafe "QueryInterface for ID3D12Device5 on live ID3D12Device" {
        var d5_ptr: *void = raw.Null[void]()
        if (*self.dev).QueryInterface(raw.Of(&d3d12.IidID3D12Device5), raw.OfMut(mut d5_ptr)) >= 0 && !raw.IsNull(d5_ptr) {
            d5 := raw.PtrCast[void, d3d12.ID3D12Device5](d5_ptr)
            (*d5).RemoveDevice()
            _ = (*d5).Release()
        }
    }
    self.lost = true
    self.loss_reason = ErrKind.DeviceLostRemoved
}

struct CloseComArgs {
    factory: *dxgi.IdxgiFactory6
    dev: *d3d12.ID3D12Device
    queue: *d3d12.ID3D12CommandQueue
    info_queue: *d3d12.ID3D12InfoQueue
    cmd_list: *d3d12.ID3D12GraphicsCommandList
    fence: *d3d12.ID3D12Fence
    fence_event: uint
    zero_buf: *d3d12.ID3D12Resource
}

CloseComSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live CloseComArgs" {
        p := raw.PtrCast[u8, CloseComArgs](arg)
        if !raw.IsNull((*p).zero_buf) { _ = (*(*p).zero_buf).Release() }
        if (*p).fence_event != 0 { _ = windows.CloseHandle(raw.FromAddr[void]((*p).fence_event)) }
        if !raw.IsNull((*p).fence) { _ = (*(*p).fence).Release() }
        if !raw.IsNull((*p).cmd_list) { _ = (*(*p).cmd_list).Release() }
        if !raw.IsNull((*p).info_queue) { _ = (*(*p).info_queue).Release() }
        if !raw.IsNull((*p).queue) { _ = (*(*p).queue).Release() }
        if !raw.IsNull((*p).dev) { _ = (*(*p).dev).Release() }
        if !raw.IsNull((*p).factory) { _ = (*(*p).factory).Release() }
    }
}

ReleaseCmdAllocSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to *ID3D12CommandAllocator" {
        pp := raw.PtrCast[u8, *d3d12.ID3D12CommandAllocator](arg)
        ca := raw.Read(pp)
        if !raw.IsNull(ca) {
            _ = (*ca).Release()
        }
    }
}

/// Releases every D3D12/DXGI resource, heap, and command object owned by self.
export Device.Close(self) {
    if Target.os != Os.Windows {
        Device(
            heap: _, factory: _, dev: _, queue: _, info_queue: _, cmd_list: _, cmd_allocs: _,
            fence: _, fence_event: _, debug_mode: _, frames_in_flight: _, tearing_ok: _,
            lost: _, loss_reason: _, last_submitted: _, completed_serial: _, present_pending: _,
            cbv_srv_uav_heap: _, sampler_heap: _, rtv_heap: _, dsv_heap: _,
            sampler_cache: _, buf_heaps: _, zero_buf: _, zero_buf_size: _,
            upload_chunks: _, cur_upload_idx: _, staging_bufs: _,
            pending_zeros: _, pending_uploads: _, msgs: _, msg_bytes: _,
            name_ptr: _, name_len: _,
            err_count: _, warn_count: _, total_err_count: _, total_warn_count: _,
        ) := self
        return
    }
    Device(
        heap,
        factory,
        dev,
        queue,
        info_queue,
        cmd_list,
        cmd_allocs: cmd_allocs0,
        fence,
        fence_event,
        debug_mode: _,
        frames_in_flight: _,
        tearing_ok: _,
        lost: _,
        loss_reason: _,
        last_submitted: _,
        completed_serial: _,
        present_pending: _,
        cbv_srv_uav_heap,
        sampler_heap,
        rtv_heap,
        dsv_heap,
        sampler_cache,
        buf_heaps: buf_heaps0,
        zero_buf,
        zero_buf_size: _,
        upload_chunks: upload_chunks0,
        cur_upload_idx: _,
        staging_bufs: staging_bufs0,
        pending_zeros,
        pending_uploads,
        msgs,
        msg_bytes,
        name_ptr,
        name_len,
        err_count: _,
        warn_count: _,
        total_err_count: _,
        total_warn_count: _,
    ) := self

    a := heap.Alloc()
    if name_len > 0 && !raw.IsNull(name_ptr) {
        unsafe "name_ptr was allocated on a by Utf16ZToHeapStr with name_len bytes" {
            a.FreeBytes(name_ptr, name_len, 1)
        }
    }
    pending_zeros.Free(a)
    pending_uploads.Free(a)
    msgs.Free(a)
    msg_bytes.Free(a)
    sampler_cache.Free(a)

    var upload_chunks := upload_chunks0
    for upload_chunks.Len() > 0 {
        uc := upload_chunks.Pop().Must()
        ReleaseResPtr(uc.res, true)
    }
    upload_chunks.FreeEmpty(a)

    var staging_bufs := staging_bufs0
    for staging_bufs.Len() > 0 {
        sb := staging_bufs.Pop().Must()
        ReleaseResPtr(sb.res, true)
    }
    staging_bufs.FreeEmpty(a)

    var buf_heaps := buf_heaps0
    for buf_heaps.Len() > 0 {
        PlacedHeap(heap: hp, size: _, alloc_nh) := buf_heaps.Pop().Must()
        salloc := FreeRec[SpanAllocator](a, alloc_nh)
        salloc.FreeAll(a)
        ReleaseHeapPtr(hp)
    }
    buf_heaps.FreeEmpty(a)

    var cmd_allocs := cmd_allocs0
    for cmd_allocs.Len() > 0 {
        ca := cmd_allocs.Pop().Must()
        var captr := ca.alloc
        SysRun(mut captr, ReleaseCmdAllocSys)
    }
    cmd_allocs.FreeEmpty(a)

    cbv_srv_uav_heap.Close(a)
    sampler_heap.Close(a)
    rtv_heap.Close(a)
    dsv_heap.Close(a)

    var args := CloseComArgs(
        factory: factory,
        dev: dev,
        queue: queue,
        info_queue: info_queue,
        cmd_list: cmd_list,
        fence: fence,
        fence_event: fence_event,
        zero_buf: zero_buf,
    )
    SysRun(mut args, CloseComSys)
}
