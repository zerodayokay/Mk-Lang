// Why: how a program chooses an adapter and configures its device, in
// sealed structs that can grow without breaking callers.
//
// What: Backend, Power, AdapterKind, DebugMode, DeviceOptions,
// DefaultDeviceOptions, Features, NoFeatures, AdapterId.
//
// How: plain types and default constants.

/// Which graphics API a device runs on.
export open enum Backend {
    /// D3D12 first on Windows, then Vulkan; Vulkan elsewhere.
    Auto
    D3d12
    Vulkan
}

/// Which adapter Open prefers when several qualify.
export open enum Power {
    Default
    HighPerformance
    LowPower
}

/// What kind of processor an adapter is.
export open enum AdapterKind {
    Discrete
    Integrated
    /// WARP on D3D12; a CPU device on Vulkan.
    Software
    Other
}

/// What the device checks beyond the portable layer's own validation,
/// which is always on.
export open enum DebugMode {
    /// Portable validation only.
    Off
    /// Also the D3D12 debug layer or the Vulkan validation layers when
    /// present; their errors become the device's sticky error.
    Validate
    /// As Validate, the layer required (Missing(DebugLayer) without it), and
    /// warnings outside the documented performance-only filter fail too.
    Strict
}

/// Optional capabilities, each false unless asked for and present.
/// Start from NoFeatures and set fields.
export struct Features {
    export texture_compression_bc: bool
    export depth_clip_control: bool
    export float32_filterable: bool
    export sampler_anisotropy: bool
    sealed: u8
}

/// No optional feature.
export const NoFeatures: Features = Features(texture_compression_bc: false, depth_clip_control: false, float32_filterable: false, sampler_anisotropy: false, sealed: 0)

/// Names one adapter across runs while it is present: its backend and its
/// LUID (DXGI's AdapterLuid, Vulkan's deviceLUID).
export struct AdapterId {
    export backend: Backend
    export luid: u64
}

/// How Open chooses and makes a device. Start from DefaultDeviceOptions
/// and assign the fields you change.
export struct DeviceOptions {
    export backend: Backend
    export power: Power
    /// Only a software adapter qualifies.
    export software: bool
    /// some: exactly this adapter (from Adapters); NoAdapter when it is gone.
    export adapter: ?AdapterId
    export debug: DebugMode
    /// Frames the CPU may record ahead of the GPU before Acquire waits: 1 to 3.
    export frames_in_flight: int
    /// Optional features; Open fails with Missing when the adapter lacks one.
    export features: Features
    /// Shader-visible CBV/SRV/UAV descriptors the device reserves (D3D12)
    /// or sizes its pools for (Vulkan).
    export descriptors: int
    sealed: u8
}

/// Auto backend, default power, no software, DebugMode.Off, two frames in
/// flight, no optional features, 65,536 descriptors.
export const DefaultDeviceOptions: DeviceOptions = DeviceOptions(backend: Backend.Auto, power: Power.Default, software: false, adapter: none, debug: DebugMode.Off, frames_in_flight: 2, features: NoFeatures, descriptors: 65536, sealed: 0)
