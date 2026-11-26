// Why: D3D12 pipelines and descriptor binding require a versioned 1.1 root
// signature matching the portable group/space/immediates layout and keeping
// buffer bounds checks active on static descriptor tables.
//
// What: Device.CreateRootSignature and Device.DestroyRootSignature.
//
// How: maps immediates to b0 space8, non-sampler/non-dynamic entries to one
// CBV/SRV/UAV descriptor table per group with DESCRIPTORS_STATIC_KEEPING_BUFFER_BOUNDS_CHECKS,
// sampler entries to a sampler table, and dynamic uniforms to root CBVs.

import c.windows
import c.d3d12
import std.raw
import std.mem

struct CreateRootSigArgs {
    dev: *d3d12.ID3D12Device
    params_ptr: *d3d12.D3d12RootParameter1
    param_count: u32
    flags: d3d12.D3d12RootSignatureFlags
    out_sig: *d3d12.ID3D12RootSignature
    hr: windows.Hresult
    step: u8 // 0=serialize, 1=create
}

CreateRootSigSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to live CreateRootSigArgs and params_ptr array on caller frame" {
        p := raw.PtrCast[u8, CreateRootSigArgs](arg)
        var rdesc := mem.Zeroed[d3d12.D3d12VersionedRootSignatureDesc]()
        rdesc.version = d3d12.D3dRootSignatureVersion11
        var d11 := mem.Zeroed[d3d12.D3d12RootSignatureDesc1]()
        d11.num_parameters = (*p).param_count
        d11.p_parameters = (*p).params_ptr
        d11.num_static_samplers = 0
        d11.p_static_samplers = raw.Null[d3d12.D3d12StaticSamplerDesc]()
        d11.flags = (*p).flags
        rdesc.anon0.SetDesc11(d11)

        var blob: *d3d12.Id3dBlob = raw.Null[d3d12.Id3dBlob]()
        var err_blob: *d3d12.Id3dBlob = raw.Null[d3d12.Id3dBlob]()
        hr := d3d12.D3D12SerializeVersionedRootSignature(
            raw.Of(&rdesc),
            raw.OfMut(mut blob),
            raw.OfMut(mut err_blob),
        )
        if !raw.IsNull(err_blob) {
            _ = (*err_blob).Release()
        }
        if hr < 0 || raw.IsNull(blob) {
            (*p).step = 0
            (*p).hr = hr
            return
        }
        ptr := (*blob).GetBufferPointer()
        sz := (*blob).GetBufferSize()
        var sig_ptr: *void = raw.Null[void]()
        chr := (*(*p).dev).CreateRootSignature(
            0,
            ptr,
            sz,
            raw.Of(&d3d12.IidID3D12RootSignature),
            raw.OfMut(mut sig_ptr),
        )
        _ = (*blob).Release()
        (*p).step = 1
        (*p).hr = chr
        if chr >= 0 && !raw.IsNull(sig_ptr) {
            (*p).out_sig = raw.PtrCast[void, d3d12.ID3D12RootSignature](sig_ptr)
        }
    }
}

ReleaseRootSigSys(arg: *u8) {
    if Target.os != Os.Windows {
        return
    }
    unsafe "arg points to *ID3D12RootSignature" {
        pp := raw.PtrCast[u8, *d3d12.ID3D12RootSignature](arg)
        sig := raw.Read(pp)
        if !raw.IsNull(sig) {
            _ = (*sig).Release()
        }
    }
}

VisFromFlags(v: bool, f: bool, c: bool): d3d12.D3d12ShaderVisibility {
    if v && !f && !c {
        return d3d12.D3d12ShaderVisibilityVertex
    }
    if !v && f && !c {
        return d3d12.D3d12ShaderVisibilityPixel
    }
    return d3d12.D3d12ShaderVisibilityAll
}

RangeFlagsForEntry(kind: u8, read_only: bool): d3d12.D3d12DescriptorRangeFlags {
    if kind == 1 && !read_only {
        return d3d12.D3d12DescriptorRangeFlagDataVolatile |
            d3d12.D3d12DescriptorRangeFlagDescriptorsStaticKeepingBufferBoundsChecks
    }
    return d3d12.D3d12DescriptorRangeFlagDataStaticWhileSetAtExecute |
        d3d12.D3d12DescriptorRangeFlagDescriptorsStaticKeepingBufferBoundsChecks
}

/// Creates a D3D12 1.1 root signature for spec and returns an opaque handle.
export Device.CreateRootSignature(mut self, spec: &PipeLayoutSpec): Error!u64 {
    if Target.os != Os.Windows {
        fail Unsupported
    }
    if self.lost {
        fail DxFail(kind: self.loss_reason, code: 0, call: CallCreateRootSignature, at: -1)
    }
    // Max parameters: 1 immediates + 4 * (1 cbv/srv/uav table + 1 sampler table + 4 dyn cbvs) = 25.
    var params: [25]d3d12.D3d12RootParameter1 = [mem.Zeroed[d3d12.D3d12RootParameter1](); 25]
    // Max descriptor ranges: 4 groups * 16 entries = 64.
    var ranges: [64]d3d12.D3d12DescriptorRange1 = [mem.Zeroed[d3d12.D3d12DescriptorRange1](); 64]
    var pcount: u32 = 0
    var rcount: int = 0

    var slots: [4]GroupRootSlots = [mem.Zeroed[GroupRootSlots](); 4]
    var has_imm := false
    var imm_param: u32 = 0
    var imm_words: u32 = 0

    if spec.immediates > 0 {
        has_imm = true
        imm_param = pcount
        imm_words = Cast[u32](spec.immediates / 4)
        var rp := mem.Zeroed[d3d12.D3d12RootParameter1]()
        rp.parameter_type = d3d12.D3d12RootParameterType32bitConstants
        rp.shader_visibility = d3d12.D3d12ShaderVisibilityAll
        rp.anon0.SetConstants(d3d12.D3d12RootConstants(
            shader_register: 0,
            register_space: 8,
            num32bit_values: imm_words,
        ))
        params[int(pcount)] = rp
        pcount += 1
    }

    gcount := spec.groups.Len()
    for gi in 0..gcount {
        ents := spec.groups[gi]
        // Non-sampler, non-dynamic CBV/SRV/UAV table.
        tbl_start := rcount
        var tbl_v := false
        var tbl_f := false
        var tbl_c := false
        for ei in 0..ents.Len() {
            e := &ents[ei]
            if e.kind == 3 || (e.kind == 0 && e.dynamic) {
                continue
            }
            rtype := if e.kind == 0 {
                d3d12.D3d12DescriptorRangeTypeCbv
            } else if e.kind == 1 && !e.read_only {
                d3d12.D3d12DescriptorRangeTypeUav
            } else {
                d3d12.D3d12DescriptorRangeTypeSrv
            }
            ranges[rcount] = d3d12.D3d12DescriptorRange1(
                range_type: rtype,
                num_descriptors: 1,
                base_shader_register: Cast[windows.Uint](e.binding),
                register_space: Cast[windows.Uint](gi),
                flags: RangeFlagsForEntry(e.kind, e.read_only),
                offset_in_descriptors_from_table_start: Cast[windows.Uint](rcount - tbl_start),
            )
            rcount += 1
            tbl_v = tbl_v || e.vis_vertex
            tbl_f = tbl_f || e.vis_fragment
            tbl_c = tbl_c || e.vis_compute
        }
        tbl_len := rcount - tbl_start
        if tbl_len > 0 {
            slots[gi].has_table = true
            slots[gi].table_param = pcount
            var rp := mem.Zeroed[d3d12.D3d12RootParameter1]()
            rp.parameter_type = d3d12.D3d12RootParameterTypeDescriptorTable
            rp.shader_visibility = VisFromFlags(tbl_v, tbl_f, tbl_c)
            rp.anon0.SetDescriptorTable(d3d12.D3d12RootDescriptorTable1(
                num_descriptor_ranges: Cast[windows.Uint](tbl_len),
                p_descriptor_ranges: raw.Of(&ranges[tbl_start]),
            ))
            params[int(pcount)] = rp
            pcount += 1
        }

        // Sampler table.
        smp_start := rcount
        var smp_v := false
        var smp_f := false
        var smp_c := false
        for ei in 0..ents.Len() {
            e := &ents[ei]
            if e.kind != 3 {
                continue
            }
            ranges[rcount] = d3d12.D3d12DescriptorRange1(
                range_type: d3d12.D3d12DescriptorRangeTypeSampler,
                num_descriptors: 1,
                base_shader_register: Cast[windows.Uint](e.binding),
                register_space: Cast[windows.Uint](gi),
                flags: d3d12.D3d12DescriptorRangeFlagNone,
                offset_in_descriptors_from_table_start: Cast[windows.Uint](rcount - smp_start),
            )
            rcount += 1
            smp_v = smp_v || e.vis_vertex
            smp_f = smp_f || e.vis_fragment
            smp_c = smp_c || e.vis_compute
        }
        smp_len := rcount - smp_start
        if smp_len > 0 {
            slots[gi].has_samplers = true
            slots[gi].sampler_param = pcount
            var rp := mem.Zeroed[d3d12.D3d12RootParameter1]()
            rp.parameter_type = d3d12.D3d12RootParameterTypeDescriptorTable
            rp.shader_visibility = VisFromFlags(smp_v, smp_f, smp_c)
            rp.anon0.SetDescriptorTable(d3d12.D3d12RootDescriptorTable1(
                num_descriptor_ranges: Cast[windows.Uint](smp_len),
                p_descriptor_ranges: raw.Of(&ranges[smp_start]),
            ))
            params[int(pcount)] = rp
            pcount += 1
        }

        // Dynamic uniform entries as root CBVs.
        for ei in 0..ents.Len() {
            e := &ents[ei]
            if e.kind != 0 || !e.dynamic {
                continue
            }
            dc := slots[gi].dyn_count
            slots[gi].dyn_params[int(dc)] = pcount
            slots[gi].dyn_count = dc + 1
            var rp := mem.Zeroed[d3d12.D3d12RootParameter1]()
            rp.parameter_type = d3d12.D3d12RootParameterTypeCbv
            rp.shader_visibility = VisFromFlags(e.vis_vertex, e.vis_fragment, e.vis_compute)
            rp.anon0.SetDescriptor(d3d12.D3d12RootDescriptor1(
                shader_register: Cast[windows.Uint](e.binding),
                register_space: Cast[windows.Uint](gi),
                flags: d3d12.D3d12RootDescriptorFlagDataStaticWhileSetAtExecute,
            ))
            params[int(pcount)] = rp
            pcount += 1
        }
    }

    rs_flags := d3d12.D3d12RootSignatureFlagAllowInputAssemblerInputLayout |
        d3d12.D3d12RootSignatureFlagDenyHullShaderRootAccess |
        d3d12.D3d12RootSignatureFlagDenyDomainShaderRootAccess |
        d3d12.D3d12RootSignatureFlagDenyGeometryShaderRootAccess

    var args := CreateRootSigArgs(
        dev: self.dev,
        params_ptr: if pcount > 0 { raw.Of(&params[0]) } else { raw.Null[d3d12.D3d12RootParameter1]() },
        param_count: pcount,
        flags: rs_flags,
        out_sig: raw.Null[d3d12.ID3D12RootSignature](),
        hr: 0,
        step: 0,
    )
    SysRun(mut args, CreateRootSigSys)
    _ = self.DrainInfoQueue()
    if args.hr < 0 || raw.IsNull(args.out_sig) {
        fail MapHrError(self, args.hr, CallCreateRootSignature)
    }
    SetRootSigName(args.out_sig, spec.label)
    a := self.heap.Alloc()
    rec := DxRootSig(
        sig: args.out_sig,
        has_imm: has_imm,
        imm_param: imm_param,
        imm_words: imm_words,
        group_count: gcount,
        groups: slots,
    )
    return AllocRec(a, rec) else e {
        var sig := args.out_sig
        SysRun(mut sig, ReleaseRootSigSys)
        fail e
    }
}

/// Releases a root signature handle.
export Device.DestroyRootSignature(mut self, nh: u64) {
    if Target.os != Os.Windows || nh == 0 {
        return
    }
    a := self.heap.Alloc()
    rec := FreeRec[DxRootSig](a, nh)
    var sig := rec.sig
    SysRun(mut sig, ReleaseRootSigSys)
}
