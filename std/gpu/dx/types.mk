// Why: std.gpu dispatches to Direct3D 12 through std.gpu.dx without exposing
// native handles outside std.
//
// What: minimal Unsupported stub for std.gpu.dx until the native D3D12
// backend replaces it.
//
// How: exports Device, Unsupported, Open and Device.Close with no unsafe code.

/// The D3D12 backend's device placeholder.
export struct Device {
    handle: u64
}

/// D3D12 backend is not implemented on this target or build.
export error Unsupported

/// Opens a D3D12 device; stub always fails Unsupported. O(1); allocates nothing.
export Open(): Unsupported!Device {
    fail Unsupported
}

/// Releases the stub D3D12 device. O(1); allocates nothing.
export Device.Close(self) {
    _ = self.handle
}
