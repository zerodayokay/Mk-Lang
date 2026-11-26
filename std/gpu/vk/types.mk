// Why: std.gpu.vk must exchange device, resource, pipeline, barrier, and
// swapchain descriptors with std.gpu without a circular import, and hold
// all native Vulkan state on the device heap as Send values.
//
// What: VkError variants, descriptor structs, native handle records,
// debug-ring records, waiter-thread state, and Device.
//
// How: stores all native Vulkan handles as u64 addresses inside Heap-owned
// records so Device and its state stay Send, and provides HandleFrom /
// HandleAddr helpers for typed conversion inside unsafe blocks.

import c.vulkan
import std.raw
import std.runtime

/// Vulkan backend is not implemented or unavailable on this target.
export error Unsupported

/// No Vulkan physical device qualifies for the options given.
export error NoAdapter

/// A requested optional Vulkan feature or validation layer is missing.
export error Missing(feature: int)

/// The Vulkan device was lost (VK_ERROR_DEVICE_LOST).
export error DeviceLost(reason: int)

/// Device memory is exhausted (VK_ERROR_OUT_OF_DEVICE_MEMORY).
export error OutOfDeviceMemory

/// The window surface is gone (VK_ERROR_SURFACE_LOST_KHR).
export error SurfaceLost

/// Any other Vulkan failure code and originating call site.
export error Driver(code: i64, call: int)

/// Every error returned by std.gpu.vk.
export open type Error = Unsupported | NoAdapter | Missing | DeviceLost | OutOfDeviceMemory | OutOfMemory | SurfaceLost | Driver | Canceled

// Feature indices matching std.gpu.Feature.
export const FeatTextureCompressionBc: int = 0
export const FeatDepthClipControl: int = 1
export const FeatFloat32Filterable: int = 2
export const FeatSamplerAnisotropy: int = 3
export const FeatDebugLayer: int = 4

// LossReason indices matching std.gpu.LossReason.
export const LossHung: int = 0
export const LossReset: int = 1
export const LossRemoved: int = 2
export const LossDriverInternal: int = 3
export const LossOutOfMemory: int = 4
export const LossUnknown: int = 5

// Call indices matching std.gpu.Call.
export const CallCreateInstance: int = 14
export const CallEnumPhysicalDevices: int = 15
export const CallCreateDevice: int = 2
export const CallAllocateMemory: int = 16
export const CallCreateImage: int = 17
export const CallCreateBuffer: int = 18
export const CallCreateDescriptorPool: int = 19
export const CallCreateSemaphore: int = 20
export const CallCreatePipeline: int = 6
export const CallCreateSwapchain: int = 7
export const CallAcquireNextImage: int = 21
export const CallQueueSubmit: int = 22
export const CallQueuePresent: int = 23
export const CallWaitSemaphores: int = 24
export const CallMap: int = 25

// Resource state bitmasks matching std.gpu statetrack.mk.
export const ResCommon: u16 = 0
export const ResVertex: u16 = 1
export const ResIndex: u16 = 2
export const ResUniform: u16 = 4
export const ResShaderRead: u16 = 8
export const ResShaderWrite: u16 = 16
export const ResRenderTarget: u16 = 32
export const ResDepthWrite: u16 = 64
export const ResDepthRead: u16 = 128
export const ResCopySrc: u16 = 256
export const ResCopyDst: u16 = 512
export const ResPresent: u16 = 1024
export const ResUndefined: u16 = 2048

// DebugMode constants matching std.gpu.DebugMode.
export const DebugOff: int = 0
export const DebugValidate: int = 1
export const DebugStrict: int = 2

// Power constants matching std.gpu.Power.
export const PowerDefault: int = 0
export const PowerHighPerformance: int = 1
export const PowerLowPower: int = 2

// AdapterKind constants matching std.gpu.AdapterKind.
export const KindDiscrete: int = 0
export const KindIntegrated: int = 1
export const KindSoftware: int = 2
export const KindOther: int = 3

// PresentMode constants matching std.gpu.PresentMode.
export const ModeFifo: int = 0
export const ModeMailbox: int = 1
export const ModeImmediate: int = 2

// PlantedBug constants for Test-profile validation.
export const BugNone: int = 0
export const BugSkipPassBarrier: int = 2
export const BugSkipGlobalBarrier: int = 5
export const BugWrongAcqStage: int = 6
export const BugSkipSignalPresent: int = 7
export const BugAcqRingF: int = 8

// Stream opcodes matching std.gpu stream.mk.
export const OpNop: u8 = 0
export const OpBarrierRef: u8 = 1
export const OpBeginRendering: u8 = 2
export const OpEndRendering: u8 = 3
export const OpSetPipeline: u8 = 4
export const OpSetGroup: u8 = 5
export const OpSetVertexBuffers: u8 = 6
export const OpSetIndexBuffer: u8 = 7
export const OpSetViewport: u8 = 8
export const OpSetScissor: u8 = 9
export const OpSetBlendConst: u8 = 10
export const OpSetStencilRef: u8 = 11
export const OpSetImmediates: u8 = 12
export const OpDraw: u8 = 13
export const OpDrawIndexed: u8 = 14
export const OpDispatch: u8 = 15
export const OpCopyBuffer: u8 = 16
export const OpCopyBufferToTexture: u8 = 17
export const OpCopyTextureToBuffer: u8 = 18
export const OpCopyTexture: u8 = 19
export const OpClearBuffer: u8 = 20
export const OpPushDebugGroup: u8 = 21
export const OpPopDebugGroup: u8 = 22

export const TexFmtCount: int = 44
export const TexFmtDepth24PlusStencil8Idx: int = 33

export struct VkLimits {
    export max_texture_size: int
    export max_texture_layers: int
    export max_bind_groups: int
    export max_bindings_per_group: int
    export max_dynamic_uniforms: int
    export max_immediates: int
    export max_vertex_buffers: int
    export max_vertex_attributes: int
    export max_vertex_stride: int
    export max_color_targets: int
    export uniform_offset_align: int
    export storage_offset_align: int
    export max_uniform_binding: int
    export max_storage_binding: int
    export max_buffer_size: int
    export max_uniforms_per_stage: int
    export max_storage_per_stage: int
    export max_textures_per_stage: int
    export max_samplers_per_stage: int
    export max_sampler_descriptors: int
    export max_workgroup_size_x: int
    export max_workgroup_size_y: int
    export max_workgroup_size_z: int
    export max_workgroup_invocations: int
    export max_workgroups_per_dimension: int
    export max_workgroup_storage: int
}

export struct VkFormatCaps {
    export sampled: bool
    export filterable: bool
    export render_target: bool
    export blendable: bool
    export multisample: bool
    export resolve: bool
    export depth_stencil: bool
}

export struct VkFeatures {
    export texture_compression_bc: bool
    export depth_clip_control: bool
    export float32_filterable: bool
    export sampler_anisotropy: bool
    export debug_layer: bool
}

export struct AdapterDesc {
    export luid: u64
    export name: str
    export vendor: u32
    export device_id: u32
    export kind: int
    export limits: VkLimits
    export features: VkFeatures
    export caps: [44]VkFormatCaps
    export depth_bias_clamp_ok: bool
    export draw_params_ok: bool
    export standard_sample_locations: bool
}

export struct OpenOptions {
    export power: int
    export software: bool
    export has_luid: bool
    export luid: u64
    export debug_mode: int
    export frames_in_flight: int
    export features: VkFeatures
    export descriptors: int
}

export struct DebugMessage {
    export severity: int
    export id: i64
    export text: str
}

export struct BufferDesc {
    export size: int
    export vertex: bool
    export index: bool
    export uniform: bool
    export uniform_dynamic: bool
    export storage: bool
    export copy_src: bool
    export copy_dst: bool
    export map_read: bool
    export map_write: bool
    export label: str
}

export struct TextureDesc {
    export format_idx: int
    export vk_format: i32
    export width: int
    export height: int
    export layers: int
    export mips: int
    export samples: int
    export copy_src: bool
    export copy_dst: bool
    export sampled: bool
    export render_target: bool
    export is_depth: bool
    export has_stencil: bool
    export is_compressed: bool
    export block_w: int
    export block_h: int
    export block_bytes: int
    export view_formats: []i32
    export label: str
}

export struct ViewDesc {
    export tex_id: u64
    export vk_format: i32
    export format_idx: int
    export dim: int
    export aspect: int
    export first_mip: int
    export mip_count: int
    export first_layer: int
    export layer_count: int
    export is_depth: bool
    export has_stencil: bool
}

export struct SamplerDesc {
    export min_linear: bool
    export mag_linear: bool
    export mip_linear: bool
    export addr_u: int
    export addr_v: int
    export addr_w: int
    export lod_min: f32
    export lod_max: f32
    export anisotropy: int
    export has_compare: bool
    export compare: int
}

export struct BgLayoutEntry {
    export binding: int
    export vis_vs: bool
    export vis_fs: bool
    export vis_cs: bool
    export kind: int
    export dynamic: bool
    export read_only: bool
}

export struct BgBindEntry {
    export binding: int
    export kind: int
    export buf_id: u64
    export offset: int
    export size: int
    export view_id: u64
    export sampler_id: u64
}

export struct StencilFaceDesc {
    export compare: int
    export on_fail: int
    export on_depth_fail: int
    export on_pass: int
}

export struct VertexAttrDesc {
    export location: int
    export vk_format: i32
    export offset: int
}

export struct VertexLayoutDesc {
    export stride: int
    export step_instance: bool
    export attrs: []VertexAttrDesc
}

export struct ColorTargetDesc {
    export vk_format: i32
    export format_idx: int
    export blend_enable: bool
    export src_color: int
    export dst_color: int
    export op_color: int
    export src_alpha: int
    export dst_alpha: int
    export op_alpha: int
    export write_mask: u8
}

export struct RenderPipelineDesc {
    export layout_id: u64
    export vs_id: u64
    export has_fs: bool
    export fs_id: u64
    export vertex_layouts: []VertexLayoutDesc
    export topology: int
    export has_strip_idx: bool
    export cull: int
    export front_cw: bool
    export targets: []ColorTargetDesc
    export has_depth: bool
    export depth_vk_format: i32
    export depth_format_idx: int
    export depth_write: bool
    export depth_compare: int
    export has_stencil: bool
    export stencil_front: StencilFaceDesc
    export stencil_back: StencilFaceDesc
    export stencil_read_mask: u8
    export stencil_write_mask: u8
    export bias_const: i32
    export bias_slope: f32
    export bias_clamp: f32
    export samples: int
    export alpha_to_coverage: bool
}

export struct BarrierCmd {
    export is_tex: bool
    export handle: u64
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

export struct StreamChunk {
    export words: []u64
}

export struct SwapchainOptions {
    export hwnd: uint
    export hinstance: uint
    export vk_format: i32
    export format_idx: int
    export mode: int
    export buffers: int
    export copy_src: bool
    export width: int
    export height: int
}

export struct SwapchainInfo {
    export format_idx: int
    export mode: int
    export width: int
    export height: int
    export images: int
}

export const AcqReady: int = 0
export const AcqOutdated: int = 1
export const AcqHidden: int = 2

export struct AcquireResult {
    export status: int
    export view_id: u64
    export image_idx: u32
}

// Memory allocation record (TLSF block span or dedicated).
struct VkMemAlloc {
    mem_type: u32
    block_idx: int
    span_id: i32
    dedicated_mem: u64
    offset: u64
    size: u64
    mapped_ptr: uint
}

struct VkMemBlock {
    alive: bool
    mem_type: u32
    is_image_block: bool
    memory: u64
    size: u64
    mapped_ptr: uint
    tlsf_idx: int
}

// Native resource records stored in dense DeviceState lists (1-based IDs).
struct VkBufRec {
    alive: bool
    vk_buffer: u64
    size: u64
    alloc: VkMemAlloc
    needs_zero: bool
    map_read: bool
    map_write: bool
}

struct VkTexRec {
    alive: bool
    vk_image: u64
    vk_format: i32
    width: int
    height: int
    layers: int
    mips: int
    samples: int
    is_depth: bool
    has_stencil: bool
    is_compressed: bool
    block_w: int
    block_h: int
    block_bytes: int
    alloc: VkMemAlloc
    needs_zero: bool
    tracked_start: int
}

struct VkViewRec {
    alive: bool
    vk_view: u64
    tex_id: u64
    vk_image: u64
    is_swapchain: bool
    sc_id: u64
    sc_img_idx: u32
    first_mip: int
    mip_count: int
    first_layer: int
    layer_count: int
    plane_mask: u8
    is_depth: bool
    has_stencil: bool
}

struct VkSamplerRec {
    alive: bool
    vk_sampler: u64
}

struct VkBgLayoutRec {
    alive: bool
    vk_layout: u64
    entries: HeapList[BgLayoutEntry]
    pools: HeapList[u64]
    free_sets: HeapList[u64]
}

VkBgLayoutRec.Free(self, a: &Alloc) {
    VkBgLayoutRec(alive: _, vk_layout: _, entries, pools, free_sets) := self
    entries.Free(a)
    pools.Free(a)
    free_sets.Free(a)
}

struct VkPipeLayoutRec {
    alive: bool
    vk_layout: u64
}

struct VkBgRec {
    alive: bool
    vk_set: u64
    layout_id: u64
    tex_views: HeapList[u64]
}

VkBgRec.Free(self, a: &Alloc) {
    VkBgRec(alive: _, vk_set: _, layout_id: _, tex_views) := self
    tex_views.Free(a)
}

struct VkShaderRec {
    alive: bool
    vk_module: u64
}

struct VkPipeRec {
    alive: bool
    is_compute: bool
    vk_pipe: u64
    layout_id: u64
}

struct VkUploadBuf {
    vk_buffer: u64
    alloc: VkMemAlloc
    size: int
    used: int
    serial: u64
}

struct VkStagingBuf {
    vk_buffer: u64
    alloc: VkMemAlloc
    size: int
    in_use: bool
    serial: u64
}

struct VkPreCopyOp {
    kind: int // 0=buf, 1=tex
    src_buf: u64
    dst_handle: u64
    src_off: u64
    dst_off: u64
    size: u64
    mip: u32
    layer: u32
    layers: u32
    x: i32
    y: i32
    w: u32
    h: u32
    bpr: u32
    rpl: u32
    pmask: u8
}

struct VkCmdSlot {
    pool: u64
    buf: u64
    serial: u64
    in_use: bool
}

// 256-slot atomic debug messenger ring buffer.
const DebugRingSlots: int = 256
const DebugRingTextBytes: int = 480

struct VkDebugSlot {
    state: raw.Atomic[u32]
    severity: u32
    id: i64
    len: u32
    text: [480]u8
}

struct VkDebugRing {
    write_idx: raw.Atomic[u32]
    err_count: raw.Atomic[u32]
    warn_count: raw.Atomic[u32]
    dropped: raw.Atomic[u32]
    slots: [256]VkDebugSlot
}

// Waiter OS thread shared control block (Heap-allocated).
struct VkWaiterShared {
    device: u64
    timeline_sem: u64
    control_sem: u64
    wait_fn_addr: uint
    signal_fn_addr: uint
    get_counter_fn_addr: uint
    target: raw.Atomic[u64]
    completed: raw.Atomic[u64]
    control_seq: raw.Atomic[u64]
    stop: raw.Atomic[u32]
    lost: raw.Atomic[u32]
    waker: runtime.Waker
    thread_handle: i64
    started: bool
}

struct VkAcqSem {
    sem: u64
    waited_by: u64
    signaled_unused: bool
}

struct VkSwapImage {
    vk_image: u64
    vk_view: u64
    view_id: u64
    render_done_sem: u64
    ever_rendered: bool
    tracked_layout: i32
}

struct VkSwapchainRec {
    alive: bool
    hwnd: uint
    hinstance: uint
    surface: u64
    swapchain: u64
    vk_format: i32
    format_idx: int
    mode: int
    req_buffers: int
    copy_src: bool
    width: int
    height: int
    frames_in_flight: int
    images: HeapList[VkSwapImage]
    acq: [4]VkAcqSem
    acq_cursor: int
    present_serials: [3]u64
    present_cursor: int
    handed_out: bool
    handed_image: u32
    pending_submit_wait: bool
    pending_wait_sem: u64
    pending_wait_acq_idx: int
    needs_recreate: bool
}

VkSwapchainRec.Free(self, a: &Alloc) {
    VkSwapchainRec(
        alive: _, hwnd: _, hinstance: _, surface: _, swapchain: _, vk_format: _, format_idx: _,
        mode: _, req_buffers: _, copy_src: _, width: _, height: _, frames_in_flight: _,
        images, acq: _, acq_cursor: _, present_serials: _, present_cursor: _,
        handed_out: _, handed_image: _, pending_submit_wait: _, pending_wait_sem: _,
        pending_wait_acq_idx: _, needs_recreate: _,
    ) := self
    images.Free(a)
}

struct VkDeviceState {
    instance: u64
    physical_device: u64
    device: u64
    queue: u64
    queue_family: u32
    gipa: vulkan.PfnVkGetInstanceProcAddr
    inst_d: InstanceDispatch
    dev_d: DeviceDispatch
    has_surface: bool
    has_swapchain: bool
    has_debug: bool
    debug_messenger: u64
    debug_ring_ptr: uint
    d24s8_vk_format: i32
    frames_in_flight: int
    mem_props: vulkan.VkPhysicalDeviceMemoryProperties
    mem_blocks: HeapList[VkMemBlock]
    tlsf_allocs: HeapList[VkTlsf]
    timeline_sem: u64
    next_serial: u64
    waiter_ptr: uint
    cmd_slots: HeapList[VkCmdSlot]
    buffers: HeapList[VkBufRec]
    textures: HeapList[VkTexRec]
    views: HeapList[VkViewRec]
    samplers: HeapList[VkSamplerRec]
    bg_layouts: HeapList[VkBgLayoutRec]
    pipe_layouts: HeapList[VkPipeLayoutRec]
    bind_groups: HeapList[VkBgRec]
    shaders: HeapList[VkShaderRec]
    pipelines: HeapList[VkPipeRec]
    swapchains: HeapList[VkSwapchainRec]
    pending_zero_bufs: HeapList[u64]
    pending_zero_texs: HeapList[u64]
    pending_copies: HeapList[VkPreCopyOp]
    upload_chunks: HeapList[VkUploadBuf]
    staging_bufs: HeapList[VkStagingBuf]
    zero_buf: u64
    zero_buf_alloc: VkMemAlloc
    zero_buf_size: u64
    tracked_layouts: HeapList[i32]
    live_native: int
    lost: bool
    loss_reason: int
    fault_next_submit: raw.Atomic[u32]
    planted_bug: int
}

/// The Vulkan backend device. Linear-owned by std.gpu.Device; Send.
export linear view struct Device {
    heap: &Heap
    state: Box[VkDeviceState]
}

HandleFrom[T](h: u64): *T {
    unsafe "h is a valid 64-bit Vulkan handle or pointer address" {
        return raw.FromAddr[T](Trunc[uint](h))
    }
}

HandleAddr[T](p: *T): u64 {
    return Cast[u64](raw.Addr(p))
}

IsDeviceLostResult(r: vulkan.VkResult): bool {
    return r == vulkan.VkErrorDeviceLost
}

MapVkError(r: vulkan.VkResult, call: int): Error {
    if r == vulkan.VkErrorDeviceLost {
        return DeviceLost(reason: LossUnknown)
    }
    if r == vulkan.VkErrorOutOfDeviceMemory {
        return OutOfDeviceMemory
    }
    if r == vulkan.VkErrorOutOfHostMemory {
        return OutOfMemory
    }
    if r == vulkan.VkErrorSurfaceLostKhr {
        return SurfaceLost
    }
    return Driver(code: i64(r), call: call)
}
