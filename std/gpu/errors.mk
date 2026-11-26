// Why: one error set for the module, with payloads plain so they can be
// returned through any error set that contains them.
//
// What: Error, its members, Feature, LossReason, Misuse, Call, and Format
// for Misuse.
//
// How: open error union Error of gpu-specific errors plus Canceled and
// OutOfMemory from the prelude.

/// Every error std.gpu returns.
export open type Error = Unsupported | NoAdapter | Missing | DeviceLost | OutOfDeviceMemory | OutOfMemory | Invalid | SurfaceLost | Driver | Canceled

/// This target, backend, adapter or format cannot do it.
export error Unsupported

/// No adapter qualifies for the options given.
export error NoAdapter

/// An optional feature was asked for and the adapter lacks it.
export error Missing(feature: Feature)

/// The device stopped: removed, reset, hung, or a driver fault. Every later
/// fallible call of the device returns it; every Close still releases.
export error DeviceLost(reason: LossReason)

/// The device's memory is exhausted.
export error OutOfDeviceMemory

/// A use the rules forbid, found before anything reached the driver. at is
/// the entry index or command index the misuse was found at, or -1.
export error Invalid(misuse: Misuse, at: int)

/// The window under a swapchain is gone.
export error SurfaceLost

/// A driver result no other member covers: the HRESULT or VkResult, and the call.
export error Driver(code: i64, call: Call)

/// Optional capabilities DeviceOptions.features can ask for.
export open enum Feature {
    TextureCompressionBc
    DepthClipControl
    Float32Filterable
    SamplerAnisotropy
    /// The D3D12 debug layer or the Vulkan validation layers, for DebugMode.Strict.
    DebugLayer
}

/// Why a device was lost, as far as the driver says.
export open enum LossReason {
    Hung
    Reset
    Removed
    DriverInternal
    OutOfMemory
    Unknown
}

/// The rule a misuse broke. Each value's Format line names the rule.
export open enum Misuse {
    StaleHandle
    NoPipeline
    PipelineKind
    GroupMissing
    GroupIncompatible
    GroupIndex
    DynamicOffsetCount
    DynamicOffsetAlignment
    VertexBufferMissing
    IndexBufferMissing
    RangeOutOfBounds
    BadUsage
    AlignmentOffset
    AlignmentRowPitch
    FormatMismatch
    FormatCapability
    SamplesMismatch
    ExtentMismatch
    AttachmentAlsoBound
    WriteReadConflict
    ShaderMismatch
    StageMismatch
    BlockTooLarge
    UnsignedDxil
    MissingSpirv
    BadShader
    BadLayout
    BadView
    BadCopyAspect
    BindMismatch
    LimitExceeded
    ImmediatesRange
    DebugGroupUnbalanced
    ViewFormatNotListed
    StorageWriteInRender
    TooManyCommands
    DebugLayer
}

/// The driver call a Driver error came from.
export open enum Call {
    CreateFactory
    EnumAdapters
    CreateDevice
    CreateHeap
    CreateResource
    CreateRootSignature
    CreatePipeline
    CreateSwapchain
    ResizeBuffers
    Present
    ExecuteCommandLists
    Signal
    CreateInstance
    EnumeratePhysicalDevices
    AllocateMemory
    CreateImage
    CreateBuffer
    CreateDescriptorPool
    CreateSemaphore
    AcquireNextImage
    QueueSubmit
    QueuePresent
    WaitSemaphores
    Map
}

impl Format for Misuse {
    Format(&self, w: mut Writer): WriteError!void {
        try w.Str(match *self {
            .StaleHandle { "stale or closed handle" }
            .NoPipeline { "draw or dispatch without a pipeline" }
            .PipelineKind { "pipeline kind does not match pass" }
            .GroupMissing { "required bind group not bound" }
            .GroupIncompatible { "bound group layout incompatible with pipeline" }
            .GroupIndex { "bind group index out of range" }
            .DynamicOffsetCount { "dynamic offset count does not match layout" }
            .DynamicOffsetAlignment { "dynamic uniform offset not 256-byte aligned" }
            .VertexBufferMissing { "required vertex buffer slot not bound" }
            .IndexBufferMissing { "indexed draw without an index buffer" }
            .RangeOutOfBounds { "byte, texel or index range out of bounds" }
            .BadUsage { "resource or sampler usage flags invalid" }
            .AlignmentOffset { "buffer offset alignment invalid" }
            .AlignmentRowPitch { "bytes_per_row must be a multiple of 256 and >= row bytes" }
            .FormatMismatch { "formats do not match" }
            .FormatCapability { "format lacks required device capability" }
            .SamplesMismatch { "sample counts invalid or do not match" }
            .ExtentMismatch { "extents invalid or do not match" }
            .AttachmentAlsoBound { "attachment subresource also bound for reading" }
            .WriteReadConflict { "subresource bound both writable and readable in one dispatch" }
            .ShaderMismatch { "shader interface does not match pipeline or entry point" }
            .StageMismatch { "shader stage does not match pipeline slot" }
            .BlockTooLarge { "shader uniform or storage block larger than layout min_size" }
            .UnsignedDxil { "DXIL container hash is zero (unsigned)" }
            .MissingSpirv { "ShaderCode.spirv is empty" }
            .BadShader { "malformed SPIR-V or DXIL bytecode" }
            .BadLayout { "invalid bind group, pipeline or vertex layout" }
            .BadView { "invalid texture view range, dimension or aspect" }
            .BadCopyAspect { "invalid aspect for texture copy or depth24plus depth copy" }
            .BindMismatch { "bind group entry does not match layout" }
            .LimitExceeded { "device or portable limit exceeded" }
            .ImmediatesRange { "immediates offset or size misaligned or out of range" }
            .DebugGroupUnbalanced { "unbalanced PushDebugGroup / PopDebugGroup" }
            .ViewFormatNotListed { "view format not listed in texture view_formats" }
            .StorageWriteInRender { "writable storage buffer visible to vertex or fragment stage" }
            .TooManyCommands { "command stream exceeded 1,048,576 commands" }
            .DebugLayer { "debug or validation layer reported an error" }
        })
    }
}
