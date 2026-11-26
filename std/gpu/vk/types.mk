// Why: std.gpu dispatches to Vulkan 1.3 through std.gpu.vk without exposing
// native handles outside std.
//
// What: minimal Unsupported stub for std.gpu.vk until the native Vulkan
// backend replaces it.
//
// How: exports Device, Unsupported, Open and Device.Close with no unsafe code.

/// The Vulkan backend's device placeholder.
export struct Device {
    handle: u64
}

/// Vulkan backend is not implemented on this target or build.
export error Unsupported

/// Opens a Vulkan device; stub always fails Unsupported. O(1); allocates nothing.
export Open(): Unsupported!Device {
    fail Unsupported
}

/// Releases the stub Vulkan device. O(1); allocates nothing.
export Device.Close(self) {
    _ = self.handle
}
