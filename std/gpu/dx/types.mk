// Why: std.gpu dispatches Direct3D 12 operations to std.gpu.dx on Windows
// while keeping non-Windows targets compiling cleanly without native calls.
//
// What: Error/ErrKind, Device, handle record types, spec descriptors, raw
// pointer box helpers, HRESULT/loss mapping, and MkSystemRun helper.
//
// How: plain structs allocated on the device heap via Alloc.Bytes/FreeBytes,
// returning u64 addresses as native handles; Target.os guards keep Windows
// system-view calls pruned off non-Windows targets.

import c.windows
import c.dxgi
import c.d3d12
import std.raw
import std.task

/// D3D12 backend is not supported on this target or missing required runtime.
export error Unsupported

/// Categorised failure from the D3D12 backend, mapped by std.gpu to gpu.Error.
export open enum ErrKind {
    Unsupported
    NoAdapter
    MissingBc
    MissingDepthClip
    MissingF32Filter
    MissingAniso
    MissingDebugLayer
    DeviceLostHung
    DeviceLostReset
    DeviceLostRemoved
    DeviceLostInternal
    DeviceLostOom
    DeviceLostUnknown
    OutOfDeviceMemory
    LimitExceeded
    SurfaceLost
    Driver
    DebugLayer
}

/// Failure returned by std.gpu.dx operations.
export error DxFail(kind: ErrKind, code: i64, call: u8, at: int)

/// Error union for all fallible std.gpu.dx calls.
export open type Error = Unsupported | DxFail | OutOfMemory | Canceled

const CallCreateFactory: u8 = 0
const CallEnumAdapters: u8 = 1
const CallCreateDevice: u8 = 2
const CallCreateHeap: u8 = 3
const CallCreateResource: u8 = 4
const CallCreateRootSignature: u8 = 5
const CallCreatePipeline: u8 = 6
const CallCreateSwapchain: u8 = 7
const CallResizeBuffers: u8 = 8
const CallPresent: u8 = 9
const CallExecuteCommandLists: u8 = 10
const CallSignal: u8 = 11
const CallMap: u8 = 12

const HresultSOk: windows.Hresult = 0
const HresultOutOfMemory: windows.Hresult = -2147024882 // 0x8007000E
const HresultDxgiErrorNotFound: windows.Hresult = -2005270526 // 0x887A0002
const HresultDxgiErrorDeviceRemoved: windows.Hresult = -2005270523 // 0x887A0005
const HresultDxgiErrorDeviceHung: windows.Hresult = -2005270522 // 0x887A0006
const HresultDxgiErrorDeviceReset: windows.Hresult = -2005270521 // 0x887A0007
const HresultDxgiErrorWasStillDrawing: windows.Hresult = -2005270518 // 0x887A000A
const HresultDxgiErrorDriverInternal: windows.Hresult = -2005270496 // 0x887A0020
const HresultDxgiStatusOccluded: windows.Hresult = 142213121 // 0x087A0001
const HresultD3d12AdapterNotFound: windows.Hresult = -2005008383 // 0x887E0001

const DxgiCreateFactoryDebug: windows.Uint = 1
const DxgiMwaNoAltEnter: windows.Uint = 2
const DxgiPresentAllowTearingFlag: windows.Uint = 512

const DxgiFormatR16Uint: dxgi.DxgiFormat = 57
const DxgiFormatR32Uint: dxgi.DxgiFormat = 42

const DefaultZeroBufBytes: int = 4194304 // 4 MiB
const BufferHeapBlockBytes: u64 = 67108864 // 64 MiB
const UploadChunkBytes: u64 = 4194304 // 4 MiB

/// Discovered DXGI adapter summary.
export struct AdapterInfo {
    export luid: u64
    export name: str
    export vendor: u32
    export device: u32
    export is_software: bool
    export max_texture_size: int
}

/// Options passed from std.gpu.Open to dx.Open.
export struct OpenOptions {
    export power: u8
    export software: bool
    export has_luid: bool
    export target_luid: u64
    export debug: u8
    export frames_in_flight: int
    export descriptors: int
    export req_bc: bool
    export req_depth_clip: bool
    export req_f32_filter: bool
    export req_aniso: bool
}

/// Format capability flags queried from CheckFeatureSupport(FORMAT_SUPPORT).
export struct FormatRowCaps {
    export sampled: bool
    export filterable: bool
    export render_target: bool
    export blendable: bool
    export multisample: bool
    export resolve: bool
    export depth_stencil: bool
}

/// Stored debug message indices into Device.msg_bytes.
struct StoredMsg {
    severity: u8
    id: i64
    off: int
    len: int
}

/// Debug layer message drained from ID3D12InfoQueue.
export struct DebugMsg {
    export severity: u8
    export id: i64
    export text: str
}

/// Buffer creation parameters.
export struct BufferSpec {
    export size: int
    export uniform_dynamic: bool
    export uniform: bool
    export storage: bool
    export map_read: bool
    export map_write: bool
    export label: str
}

/// Texture creation parameters.
export struct TextureSpec {
    export dxgi_res: u32
    export dxgi_rtv_dsv: u32
    export width: int
    export height: int
    export layers: int
    export mips: int
    export samples: int
    export sampled: bool
    export render_target: bool
    export is_depth: bool
    export copy_src: bool
    export copy_dst: bool
    export block_bytes: int
    export block_w: int
    export block_h: int
    export planes: int
    export label: str
}

/// View creation parameters.
export struct ViewSpec {
    export tex_nh: u64
    export dxgi_rtv_dsv: u32
    export dxgi_srv: u32
    export dxgi_stencil_srv: u32
    export dim: u8
    export aspect: u8
    export first_mip: int
    export mip_count: int
    export first_layer: int
    export layer_count: int
    export samples: int
    export render_target: bool
    export sampled: bool
    export is_depth: bool
    export has_stencil: bool
}

/// Sampler creation parameters.
export struct SamplerSpec {
    export min_linear: bool
    export mag_linear: bool
    export mip_linear: bool
    export addr_u: u8
    export addr_v: u8
    export addr_w: u8
    export lod_min: f32
    export lod_max: f32
    export anisotropy: int
    export has_compare: bool
    export compare: u8
}

/// Bind group layout entry specification.
export struct LayoutEntrySpec {
    export binding: int
    export vis_vertex: bool
    export vis_fragment: bool
    export vis_compute: bool
    export kind: u8 // 0=uniform, 1=storage, 2=texture, 3=sampler
    export dynamic: bool
    export read_only: bool
}

/// Pipeline layout specification.
export struct PipeLayoutSpec {
    export groups: [][]LayoutEntrySpec
    export immediates: int
    export label: str
}

/// Bind group item specification.
export struct BindEntrySpec {
    export binding: int
    export kind: u8 // 0=buf_cbv, 1=buf_srv, 2=buf_uav, 3=tex_srv, 4=sampler, 5=dyn_cbv
    export res_nh: u64
    export offset: int
    export size: int
    export stride: int
}

/// Vertex attribute specification for PSO creation.
export struct VertexAttrSpec {
    export location: int
    export dxgi_format: u32
    export offset: int
    export slot: int
    export per_instance: bool
}

/// Blend component specification.
export struct BlendCompSpec {
    export src: u8
    export dst: u8
    export op: u8
}

/// Render target specification for PSO creation.
export struct ColorTargetSpec {
    export dxgi_format: u32
    export blend_enable: bool
    export color: BlendCompSpec
    export alpha: BlendCompSpec
    export write_mask: u8
}

/// Stencil face specification.
export struct StencilFaceSpec {
    export compare: u8
    export on_fail: u8
    export on_depth_fail: u8
    export on_pass: u8
}

/// Render pipeline specification.
export struct RenderPipeSpec {
    export root_sig_nh: u64
    export vs_nh: u64
    export has_fs: bool
    export fs_nh: u64
    export attrs: []VertexAttrSpec
    export topology: u8
    export has_strip_cut: bool
    export strip_cut_u32: bool
    export cull: u8
    export front_ccw: bool
    export depth_clip_disable: bool
    export targets: []ColorTargetSpec
    export has_depth: bool
    export depth_dxgi: u32
    export depth_write: bool
    export depth_compare: u8
    export stencil_enable: bool
    export stencil_read_mask: u8
    export stencil_write_mask: u8
    export stencil_front: StencilFaceSpec
    export stencil_back: StencilFaceSpec
    export bias_constant: i32
    export bias_slope: f32
    export bias_clamp: f32
    export samples: int
    export alpha_to_coverage: bool
    export label: str
}

/// Swapchain creation specification.
export struct SwapchainSpec {
    export hwnd: uint
    export width: int
    export height: int
    export dxgi_res: u32
    export dxgi_rtv: u32
    export mode: u8 // 0=Fifo, 1=Mailbox, 2=Immediate
    export buffers: int
    export copy_src: bool
}

/// Outcome of Acquire on D3D12 swapchain.
export struct AcquireResult {
    export status: u8 // 0=Ready, 1=Outdated, 2=Hidden
    export image_idx: u32
    export res_ptr: u64
    export rtv_ptr: u64
}

/// Color attachment payload for BeginRendering / EndRendering replay.
export struct PassColorSpec {
    export is_swapchain: bool
    export res_ptr: u64
    export rtv_ptr: u64
    export has_resolve: bool
    export resolve_res_ptr: u64
    export resolve_sub: u32
    export src_sub: u32
    export dxgi_format: u32
    export load_op: u8 // 0=Clear, 1=Keep, 2=DontCare
    export clear_r: f32
    export clear_g: f32
    export clear_b: f32
    export clear_a: f32
}

/// Depth attachment payload for BeginRendering replay.
export struct PassDepthSpec {
    export has_depth: bool
    export dsv_ptr: u64
    export read_only: bool
    export depth_load: u8
    export stencil_load: u8
    export depth_clear: f32
    export stencil_clear: u8
    export has_stencil: bool
}

/// One portable barrier item passed to replay.
export struct BarrierSpec {
    export is_tex: bool
    export nh: u64
    export all_sub: bool
    export first_mip: u8
    export mip_count: u8
    export first_layer: u16
    export layer_count: u16
    export plane_mask: u8
    export old_state: u16
    export new_state: u16
    export is_uav: bool
}

/// Descriptor range allocator state using a sorted free-span list.
export struct ChunkSpan {
    export ptr: *u64
    export len: int
}

struct Span {
    offset: u64
    size: u64
}

struct SpanAllocator {
    total: u64
    free_list: HeapList[Span]
}

struct DescHeap {
    heap: *d3d12.ID3D12DescriptorHeap
    cpu_start: u64
    gpu_start: u64
    inc_size: u64
    capacity: u64
    alloc: SpanAllocator
}

struct SamplerCacheEntry {
    count: int
    sampler_nhs: [16]u64
    offset: u32
    ref_count: u32
}

struct PlacedHeap {
    heap: *d3d12.ID3D12Heap
    size: u64
    alloc_nh: u64
}

struct UploadChunk {
    res: *d3d12.ID3D12Resource
    cpu_ptr: *u8
    gpu_va: u64
    size: u64
    used: u64
    serial: u64
}

struct StagingBuf {
    res: *d3d12.ID3D12Resource
    cpu_ptr: *u8
    size: u64
    class_idx: int
    serial: u64
    in_use: bool
}

struct CmdAllocRec {
    alloc: *d3d12.ID3D12CommandAllocator
    serial: u64
}

struct PendingZero {
    kind: u8 // 0=buf, 1=tex_copy, 2=tex_rtv, 3=tex_dsv
    res: *d3d12.ID3D12Resource
    size: u64
    mips: u8
    layers: u16
    planes: u8
    width: int
    height: int
    dxgi_fmt: u32
    dxgi_rtv_dsv: u32
    block_bytes: int
    block_w: int
    block_h: int
    samples: u8
}

struct PendingUploadCopy {
    kind: u8 // 0=buf, 1=tex
    src_res: *d3d12.ID3D12Resource
    src_off: u64
    dst_res: *d3d12.ID3D12Resource
    dst_off: u64
    bytes: u64
    subresource: u32
    dst_x: u32
    dst_y: u32
    width: u32
    height: u32
    row_pitch: u32
    dxgi_fmt: u32
    cur_state: u16
}

struct DxBuffer {
    res: *d3d12.ID3D12Resource
    gpu_va: u64
    mapped: *u8
    alloc_size: u64
    user_size: u64
    heap_idx: i32
    heap_off: u64
    is_upload: bool
    is_readback: bool
}

struct DxTexture {
    res: *d3d12.ID3D12Resource
    dxgi_res: u32
    dxgi_rtv_dsv: u32
    width: int
    height: int
    layers: int
    mips: int
    samples: int
    block_bytes: int
    block_w: int
    block_h: int
    planes: int
    is_depth: bool
}

struct DxView {
    tex_res: *d3d12.ID3D12Resource
    dxgi_srv: u32
    dxgi_stencil_srv: u32
    dim: u8
    aspect: u8
    first_mip: int
    mip_count: int
    first_layer: int
    layer_count: int
    samples: int
    is_depth: bool
    has_rtv: bool
    rtv_slot: u64
    rtv_ptr: u64
    has_dsv: bool
    dsv_rw_slot: u64
    dsv_rw_ptr: u64
    dsv_ro_slot: u64
    dsv_ro_ptr: u64
    is_swapchain: bool
    swap_res: *d3d12.ID3D12Resource
}

struct DxSampler {
    desc: d3d12.D3d12SamplerDesc
}

struct GroupRootSlots {
    has_table: bool
    table_param: u32
    has_samplers: bool
    sampler_param: u32
    dyn_count: u32
    dyn_params: [4]u32
}

struct DxRootSig {
    sig: *d3d12.ID3D12RootSignature
    has_imm: bool
    imm_param: u32
    imm_words: u32
    group_count: int
    groups: [4]GroupRootSlots
}

struct DxShader {
    dxil: HeapList[u8]
}

struct DxPipeline {
    pso: *d3d12.ID3D12PipelineState
    root_sig: *DxRootSig
    is_compute: bool
    topology: d3d12.D3d12PrimitiveTopology
}

struct DxBindGroup {
    has_cbv_srv_uav: bool
    cbv_srv_uav_off: u64
    cbv_srv_uav_cnt: u64
    cbv_srv_uav_gpu: u64
    has_samplers: bool
    sampler_cache_idx: int
    sampler_gpu: u64
    dyn_count: int
    dyn_vas: [4]u64
}

struct DxSwapchain {
    sc: *dxgi.IdxgiSwapChain3
    hwnd: windows.Hwnd
    waitable: uint
    dxgi_res: u32
    dxgi_rtv: u32
    mode: u8
    buffers: u32
    copy_src: bool
    width: int
    height: int
    flags: windows.Uint
    back_res: [3]*d3d12.ID3D12Resource
    rtv_slots: [3]u64
    rtv_ptrs: [3]u64
    present_serials: [3]u64
    frame_idx: u64
    needs_recreate: bool
    abandoned: bool
    credit_held: bool
}

/// D3D12 backend device owning native COM interfaces, heaps, upload ring, and fence.
export linear view struct Device {
    heap: &Heap
    factory: *dxgi.IdxgiFactory6
    dev: *d3d12.ID3D12Device
    queue: *d3d12.ID3D12CommandQueue
    info_queue: *d3d12.ID3D12InfoQueue
    cmd_list: *d3d12.ID3D12GraphicsCommandList
    cmd_allocs: HeapList[CmdAllocRec]
    fence: *d3d12.ID3D12Fence
    fence_event: uint
    debug_mode: u8
    frames_in_flight: int
    tearing_ok: bool
    lost: bool
    loss_reason: ErrKind
    last_submitted: u64
    completed_serial: u64
    present_pending: bool
    cbv_srv_uav_heap: DescHeap
    sampler_heap: DescHeap
    rtv_heap: DescHeap
    dsv_heap: DescHeap
    sampler_cache: HeapList[SamplerCacheEntry]
    buf_heaps: HeapList[PlacedHeap]
    zero_buf: *d3d12.ID3D12Resource
    zero_buf_size: u64
    upload_chunks: HeapList[UploadChunk]
    cur_upload_idx: int
    staging_bufs: HeapList[StagingBuf]
    pending_zeros: HeapList[PendingZero]
    pending_uploads: HeapList[PendingUploadCopy]
    msgs: HeapList[StoredMsg]
    msg_bytes: HeapList[u8]
    name_ptr: *u8
    name_len: int
    err_count: int
    warn_count: int
    total_err_count: int
    total_warn_count: int
}

AllocRecSlot[T](a: &Alloc): OutOfMemory!*T {
    sz := SizeOf[T]()
    al := AlignOf[T]()
    n := if sz > 0 { sz } else { 1 }
    unsafe "returns uninitialized aligned storage for T on a" {
        p := a.Bytes(n, al)
        if raw.IsNull(p) {
            fail OutOfMemory
        }
        return raw.PtrCast[u8, T](p)
    }
}

WriteRecSlot[T](tp: *T, x: T): u64 {
    unsafe "tp points to live storage allocated by AllocRecSlot[T]" {
        raw.Write(tp, x)
        return Trunc[u64](raw.Addr(tp))
    }
}

AllocRec[T](a: &Alloc, x: T): OutOfMemory!u64 {
    tp := try AllocRecSlot[T](a)
    return WriteRecSlot(tp, x)
}

FreeRec[T](a: &Alloc, h: u64): T {
    sz := SizeOf[T]()
    al := AlignOf[T]()
    n := if sz > 0 { sz } else { 1 }
    unsafe "h was allocated by AllocRec[T] on a and is still owned" {
        tp := raw.FromAddr[T](Trunc[uint](h))
        v := raw.Read(tp)
        a.FreeBytes(raw.PtrCast[T, u8](tp), n, al)
        return v
    }
}

SysRun[T](arg: mut T, f: fn(*u8)) {
    unsafe "arg outlives the synchronous MkSystemRun call" {
        task.MkSystemRun(f, raw.PtrCast[T, u8](raw.OfMut(arg)))
    }
}

IsDeviceLostHr(hr: windows.Hresult): bool {
    return hr == HresultDxgiErrorDeviceRemoved ||
        hr == HresultDxgiErrorDeviceHung ||
        hr == HresultDxgiErrorDeviceReset ||
        hr == HresultDxgiErrorDriverInternal
}

LossKindFromHr(hr: windows.Hresult): ErrKind {
    if hr == HresultDxgiErrorDeviceHung {
        return ErrKind.DeviceLostHung
    }
    if hr == HresultDxgiErrorDeviceReset {
        return ErrKind.DeviceLostReset
    }
    if hr == HresultDxgiErrorDeviceRemoved {
        return ErrKind.DeviceLostRemoved
    }
    if hr == HresultDxgiErrorDriverInternal {
        return ErrKind.DeviceLostInternal
    }
    if hr == HresultOutOfMemory {
        return ErrKind.DeviceLostOom
    }
    return ErrKind.DeviceLostUnknown
}

MapHrError(dev: mut Device, hr: windows.Hresult, call: u8): Error {
    if IsDeviceLostHr(hr) {
        var reason := hr
        if !raw.IsNull(dev.dev) {
            unsafe "dev.dev is a live ID3D12Device" {
                r := (*dev.dev).GetDeviceRemovedReason()
                if r != HresultSOk {
                    reason = r
                }
            }
        }
        kind := LossKindFromHr(reason)
        dev.lost = true
        dev.loss_reason = kind
        return DxFail(kind: kind, code: i64(reason), call: call, at: -1)
    }
    if hr == HresultOutOfMemory {
        return DxFail(kind: ErrKind.OutOfDeviceMemory, code: i64(hr), call: call, at: -1)
    }
    if !raw.IsNull(dev.dev) {
        unsafe "dev.dev is a live ID3D12Device" {
            rem := (*dev.dev).GetDeviceRemovedReason()
            if rem != HresultSOk {
                kind := LossKindFromHr(rem)
                dev.lost = true
                dev.loss_reason = kind
                return DxFail(kind: kind, code: i64(rem), call: call, at: -1)
            }
        }
    }
    return DxFail(kind: ErrKind.Driver, code: i64(hr), call: call, at: -1)
}
