// Why: the portable state planner emits abstract ResState transitions and UAV
// hazards; D3D12 replay must lower them into batched D3D12_RESOURCE_BARRIER
// arrays without allocation on the hot path.
//
// What: MapResState, EmitTransition, EmitUavBarrier, and EmitBarrierBatch.
//
// How: translates ResState bitmasks into D3D12_RESOURCE_STATES (merging read
// bits cleanly), batches up to 64 D3D12_RESOURCE_BARRIER structs on the stack,
// and submits them to ID3D12GraphicsCommandList::ResourceBarrier.

import c.windows
import c.d3d12
import std.raw
import std.mem

const D3d12AllSubresources: windows.Uint = 0xFFFFFFFF

const ResStateCommon: u16 = 0
const ResStateVertex: u16 = 1
const ResStateIndex: u16 = 2
const ResStateUniform: u16 = 4
const ResStateShaderRead: u16 = 8
const ResStateShaderWrite: u16 = 16
const ResStateRenderTarget: u16 = 32
const ResStateDepthWrite: u16 = 64
const ResStateDepthRead: u16 = 128
const ResStateCopySrc: u16 = 256
const ResStateCopyDst: u16 = 512
const ResStatePresent: u16 = 1024

MapResState(s: u16): d3d12.D3d12ResourceStates {
    if s == ResStateCommon || s == ResStatePresent {
        return d3d12.D3d12ResourceStateCommon
    }
    var out: d3d12.D3d12ResourceStates = 0
    if (s & (ResStateVertex | ResStateUniform)) != 0 {
        out |= d3d12.D3d12ResourceStateVertexAndConstantBuffer
    }
    if (s & ResStateIndex) != 0 {
        out |= d3d12.D3d12ResourceStateIndexBuffer
    }
    if (s & ResStateShaderRead) != 0 {
        out |= d3d12.D3d12ResourceStateNonPixelShaderResource | d3d12.D3d12ResourceStatePixelShaderResource
    }
    if (s & ResStateShaderWrite) != 0 {
        out |= d3d12.D3d12ResourceStateUnorderedAccess
    }
    if (s & ResStateRenderTarget) != 0 {
        out |= d3d12.D3d12ResourceStateRenderTarget
    }
    if (s & ResStateDepthWrite) != 0 {
        out |= d3d12.D3d12ResourceStateDepthWrite
    }
    if (s & ResStateDepthRead) != 0 {
        out |= d3d12.D3d12ResourceStateDepthRead
    }
    if (s & ResStateCopySrc) != 0 {
        out |= d3d12.D3d12ResourceStateCopySource
    }
    if (s & ResStateCopyDst) != 0 {
        out |= d3d12.D3d12ResourceStateCopyDest
    }
    return out
}

MakeTransitionBarrier(
    res: *d3d12.ID3D12Resource,
    subresource: windows.Uint,
    before: d3d12.D3d12ResourceStates,
    after: d3d12.D3d12ResourceStates,
): d3d12.D3d12ResourceBarrier {
    var b := mem.Zeroed[d3d12.D3d12ResourceBarrier]()
    b.type_ = d3d12.D3d12ResourceBarrierTypeTransition
    b.flags = d3d12.D3d12ResourceBarrierFlagNone
    b.anon0.SetTransition(d3d12.D3d12ResourceTransitionBarrier(
        p_resource: res,
        subresource: subresource,
        state_before: before,
        state_after: after,
    ))
    return b
}

MakeUavBarrier(res: *d3d12.ID3D12Resource): d3d12.D3d12ResourceBarrier {
    var b := mem.Zeroed[d3d12.D3d12ResourceBarrier]()
    b.type_ = d3d12.D3d12ResourceBarrierTypeUav
    b.flags = d3d12.D3d12ResourceBarrierFlagNone
    b.anon0.SetUav(d3d12.D3d12ResourceUavBarrier(p_resource: res))
    return b
}

FlushBarrierBuf(cmd: *d3d12.ID3D12GraphicsCommandList, buf: &[64]d3d12.D3d12ResourceBarrier, count: mut int) {
    if Target.os != Os.Windows || *count <= 0 {
        *count = 0
        return
    }
    unsafe "cmd is a live recording ID3D12GraphicsCommandList and buf has *count initialized barriers" {
        (*cmd).ResourceBarrier(Cast[windows.Uint](*count), raw.Of(&(*buf)[0]))
    }
    *count = 0
}

PushBarrier(
    cmd: *d3d12.ID3D12GraphicsCommandList,
    buf: mut [64]d3d12.D3d12ResourceBarrier,
    count: mut int,
    b: d3d12.D3d12ResourceBarrier,
) {
    buf[*count] = b
    *count += 1
    if *count >= 64 {
        FlushBarrierBuf(cmd, &*buf, count)
    }
}

EmitTransition(
    cmd: *d3d12.ID3D12GraphicsCommandList,
    res: *d3d12.ID3D12Resource,
    sub: windows.Uint,
    before: d3d12.D3d12ResourceStates,
    after: d3d12.D3d12ResourceStates,
) {
    if Target.os != Os.Windows || raw.IsNull(res) || before == after {
        return
    }
    var b := MakeTransitionBarrier(res, sub, before, after)
    unsafe "cmd is a live recording ID3D12GraphicsCommandList" {
        (*cmd).ResourceBarrier(1, raw.OfMut(mut b))
    }
}

EmitBarrierBatch(cmd: *d3d12.ID3D12GraphicsCommandList, items: []BarrierSpec) {
    if Target.os != Os.Windows || items.Len() == 0 {
        return
    }
    var buf: [64]d3d12.D3d12ResourceBarrier = [mem.Zeroed[d3d12.D3d12ResourceBarrier](); 64]
    var count := 0
    for i in 0..items.Len() {
        it := &items[i]
        if it.nh == 0 {
            continue
        }
        if !it.is_tex {
            unsafe "it.nh is a live DxBuffer pointer" {
                bp := raw.FromAddr[DxBuffer](Trunc[uint](it.nh))
                if it.is_uav {
                    PushBarrier(cmd, mut buf, mut count, MakeUavBarrier((*bp).res))
                } else {
                    before := MapResState(it.old_state)
                    after := MapResState(it.new_state)
                    if before != after {
                        PushBarrier(cmd, mut buf, mut count, MakeTransitionBarrier((*bp).res, D3d12AllSubresources, before, after))
                    }
                }
            }
            continue
        }
        before := MapResState(it.old_state)
        after := MapResState(it.new_state)
        if before == after {
            continue
        }
        unsafe "it.nh is a live DxTexture pointer" {
            tp := raw.FromAddr[DxTexture](Trunc[uint](it.nh))
            if it.all_sub {
                PushBarrier(cmd, mut buf, mut count, MakeTransitionBarrier((*tp).res, D3d12AllSubresources, before, after))
            } else {
                total_mips := (*tp).mips
                total_layers := (*tp).layers
                for plane in 0..2 {
                    if (it.plane_mask & Trunc[u8](1 << plane)) == 0 {
                        continue
                    }
                    fm := int(it.first_mip)
                    mc := int(it.mip_count)
                    fl := int(it.first_layer)
                    lc := int(it.layer_count)
                    for m in fm..(fm + mc) {
                        for l in fl..(fl + lc) {
                            sub := Cast[windows.Uint](m + (l * total_mips) + (plane * total_mips * total_layers))
                            PushBarrier(cmd, mut buf, mut count, MakeTransitionBarrier((*tp).res, sub, before, after))
                        }
                    }
                }
            }
        }
    }
    FlushBarrierBuf(cmd, &buf, mut count)
}
