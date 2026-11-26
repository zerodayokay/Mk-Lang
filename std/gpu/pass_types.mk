// Why: pass attachment and load/store descriptor types are exported by std.gpu
// and shared across render pass validation, state tracking, and command stream
// encoding while keeping pass.mk concise.
//
// What: Load, LoadDepth, LoadStencil, Store, ColorAttachment, DepthAttachment,
// and PassDesc.
//
// How: plain algebraic and record types describing attachment operations.

/// What a colour attachment holds when the pass begins. Clear values are
/// f64 so every R32Uint and R32Sint value clears exactly.
export enum Load {
    Clear(r: f64, g: f64, b: f64, a: f64)
    Keep
    DontCare
}

/// What a depth aspect holds when the pass begins.
export enum LoadDepth {
    Clear(depth: f32)
    Keep
    DontCare
}

/// What a stencil aspect holds when the pass begins.
export enum LoadStencil {
    Clear(value: u32)
    Keep
    DontCare
}

/// What an attachment keeps when the pass ends.
export enum Store {
    Store
    Discard
}

/// One colour attachment of a pass.
export struct ColorAttachment {
    export target: &TextureView
    export resolve: ?&TextureView
    export load: Load
    export store: Store
}

/// The depth-stencil attachment of a pass. read_only: depth and stencil
/// are only tested, so the same view may also be bound for sampling.
export struct DepthAttachment {
    export target: &TextureView
    export depth_load: LoadDepth
    export depth_store: Store
    export stencil_load: LoadStencil
    export stencil_store: Store
    export read_only: bool
}

/// The attachments of a render pass; all share one extent.
export struct PassDesc {
    export colors: []ColorAttachment
    export depth: ?DepthAttachment
    export label: str
}
