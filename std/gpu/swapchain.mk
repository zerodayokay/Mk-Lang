// Why: a program shows frames in a window through a swapchain; the loan
// checker refuses acquire, present and resize misuse at compile time.
//
// What: PresentMode, SwapchainOptions, DefaultSwapchainOptions, Swapchain,
// SwapchainInfo, SurfaceTexture, Acquired, Presented, and Swapchain methods.
//
// How: validates swapchain options and window surfaces at creation, wraps
// acquired back-buffer views in SurfaceTexture holding mut Swapchain, and
// dispatches Acquire/Present/Resize to the active backend.

import std.window

/// When a presented frame reaches the screen.
export open enum PresentMode {
    /// At vertical blank; never tears; always available.
    Fifo
    /// The newest frame at vertical blank; never tears; Fifo where absent.
    Mailbox
    /// At once; may tear; Mailbox, then Fifo, where absent.
    Immediate
}

/// How NewSwapchain configures the surface. format is one of Bgra8Unorm,
/// Bgra8UnormSrgb, Rgba8Unorm, Rgba8UnormSrgb. Start from
/// DefaultSwapchainOptions.
export struct SwapchainOptions {
    export format: TextureFormat
    export mode: PresentMode
    /// Back buffers asked for: 2 or 3.
    export buffers: int
    /// Also copy_src, where the surface allows it (screenshots).
    export copy_src: bool
    sealed: u8
}

/// Bgra8UnormSrgb, Fifo, 3 buffers, no copy_src.
export const DefaultSwapchainOptions: SwapchainOptions = SwapchainOptions(format: TextureFormat.Bgra8UnormSrgb, mode: PresentMode.Fifo, buffers: 3, copy_src: false, sealed: 0)

/// The images a window shows. Linear: end it with Close.
export linear struct Swapchain {
    slot: u32
    generation: u32
    device: u32
}

struct SwapchainEntry {
    info: SwapchainInfo
    view_slot: u32
    view_gen: u32
    native_handle: u64
}

IsSwapchainFormat(f: TextureFormat): bool {
    return match f {
        .Bgra8Unorm | .Bgra8UnormSrgb | .Rgba8Unorm | .Rgba8UnormSrgb { true }
        _ { false }
    }
}

/// Makes a swapchain on w's client area at its current size. Errors:
/// Unsupported (format), SurfaceLost (the window is closed), DeviceLost.
/// Runs on the system stack. Counts as a user of w's native window.
export Device.NewSwapchain(mut self, io: Io, w: &window.Window, o: SwapchainOptions): Error!Swapchain {
    _ = io
    try self.CheckAlive()
    if !IsSwapchainFormat(o.format) {
        fail Unsupported
    }
    if o.buffers < 2 || o.buffers > 3 {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    nh := try self.NativeCreateSwapchain()
    a := self.heap.Alloc()
    onerr self.state.Mut().native.DestroySwapchain(a, nh)
    (width: ww, height: wh) := w.Size()
    sw := if ww > 0 { ww } else { 800 }
    sh := if wh > 0 { wh } else { 600 }

    v_nh := try self.NativeCreateView()
    onerr self.state.Mut().native.DestroyView(v_nh)
    var usage := NoTextureUsage
    usage.render_target = true
    usage.copy_src = o.copy_src
    v_entry := ViewEntry(
        tex_slot: 0,
        format: o.format,
        dim: ViewDimension.D2,
        aspect: Aspect.All,
        first_mip: 0,
        mip_count: 1,
        first_layer: 0,
        layer_count: 1,
        width: sw,
        height: sh,
        samples: 1,
        usage: usage,
        plane_mask: 1,
        is_swapchain: true,
        native_handle: v_nh,
    )
    var vslot: u32 = 0
    var vgen: u32 = 0
    match self.state.Mut().views.Alloc(a, v_entry) {
        .Ok(slot, gen_) {
            vslot = slot
            vgen = gen_
        }
        .Oom(val: _) {
            self.state.Mut().native.DestroyView(v_nh)
            self.state.Mut().native.DestroySwapchain(a, nh)
            fail OutOfMemory
        }
    }

    info := SwapchainInfo(format: o.format, mode: o.mode, width: sw, height: sh, images: o.buffers)
    entry := SwapchainEntry(info: info, view_slot: vslot, view_gen: vgen, native_handle: nh)
    match self.state.Mut().swapchains.Alloc(a, entry) {
        .Ok(slot, gen_) { return Swapchain(slot: slot, generation: gen_, device: self.serial) }
        .Oom(val: _) {
            self.ReleaseSwapViewSlot(vslot)
            self.state.Mut().native.DestroyView(v_nh)
            self.state.Mut().native.DestroySwapchain(a, nh)
            fail OutOfMemory
        }
    }
}

Device.ReleaseSwapViewSlot(mut self, vslot: u32) {
    _ = self.state.Mut().views.Release(self.heap.Alloc(), vslot)
}

Device.ReleaseSwapchainSlot(mut self, slot: u32, gen_: u32): u32 {
    a := self.heap.Alloc()
    st := self.state.Mut()
    if !st.swapchains.IsLive(slot, gen_) {
        return 0
    }
    match st.swapchains.Release(a, slot) {
        some(e) {
            st.native.DestroySwapchain(a, e.native_handle)
            return e.view_slot
        }
        none { return 0 }
    }
}

/// Waits until the GPU and the presentation engine are done with the
/// images, then releases them. Not cancellable; never fails.
export Swapchain.Close(self, io: Io, dev: mut Device) {
    _ = io
    Swapchain(slot, generation, device) := self
    if device != dev.serial {
        Panic("gpu: a handle closed with another device")
    }
    vslot := dev.ReleaseSwapchainSlot(slot, generation)
    if vslot != 0 {
        dev.DestroyViewSlot(vslot)
    }
}

/// What a swapchain was granted.
export struct SwapchainInfo {
    export format: TextureFormat
    export mode: PresentMode
    export width: int
    export height: int
    /// Images the driver made; may exceed SwapchainOptions.buffers.
    export images: int
}

/// What sc was granted. O(1).
export Device.SwapchainInfo(&self, sc: &Swapchain): SwapchainInfo {
    if sc.device != self.serial {
        return SwapchainInfo(format: TextureFormat.Bgra8UnormSrgb, mode: PresentMode.Fifo, width: 0, height: 0, images: 0)
    }
    st := self.state.Ref()
    if !st.swapchains.IsLive(sc.slot, sc.generation) {
        return SwapchainInfo(format: TextureFormat.Bgra8UnormSrgb, mode: PresentMode.Fifo, width: 0, height: 0, images: 0)
    }
    return st.swapchains.Get(sc.slot).info
}

/// One image of a swapchain to render into and present. Holds the swapchain
/// exclusively. Linear: Present it, or Abandon it.
export linear view struct SurfaceTexture {
    sc: mut Swapchain
    target: TextureView
    image: u32
}

/// The outcome of Acquire.
export enum Acquired {
    Ready(frame: SurfaceTexture)
    /// The surface changed, or an image was abandoned: Resize, then Acquire.
    Outdated
    /// The window is minimised: wait for an event, then Acquire.
    Hidden
}

/// Waits until a frame may start (frames in flight and the surface's
/// latency) and takes the next image. Errors: DeviceLost, SurfaceLost,
/// Canceled (repeatable: nothing taken). Suspends. Allocates nothing.
export Device.Acquire(mut self, io: Io, sc: mut Swapchain): Error!Acquired from sc {
    _ = io
    try self.CheckAlive()
    if sc.device != self.serial {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    try self.FlushPreSubmit()
    st := self.state.Mut()
    if !st.swapchains.IsLive(sc.slot, sc.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    se := st.swapchains.Get(sc.slot)
    vslot := se.view_slot
    vgen := se.view_gen
    match mut st.native {
        .Stub(s) {
            if s.acquire_outdated {
                s.acquire_outdated = false
                return Acquired.Outdated
            }
            if s.acquire_hidden {
                s.acquire_hidden = false
                return Acquired.Hidden
            }
            if se.info.width == 0 || se.info.height == 0 {
                return Acquired.Hidden
            }
            return Acquired.Ready(frame: SurfaceTexture(
                sc: sc,
                target: TextureView(slot: vslot, generation: vgen, device: self.serial),
                image: 0,
            ))
        }
        .Dx(d: _) | .Vk(v: _) {
            fail Unsupported
        }
    }
}

/// The image's render-target view, valid while t lives. O(1).
export SurfaceTexture.View(&self): &TextureView from self {
    return &self.target
}

/// Gives the image back unpresented. The next Acquire answers Outdated and
/// Resize recreates the swapchain. Never fails; needs no device. O(1).
export SurfaceTexture.Abandon(self) {
    SurfaceTexture(sc: _, target, image: _) := self
    TextureView(slot: _, generation: _, device: _) := target
}

/// What a present reported.
export struct Presented {
    /// The surface would present better recreated: Resize when convenient.
    export suboptimal: bool
}

CheckAndPresentStub(st: mut DeviceState, sc: &Swapchain): Error!void {
    if !st.swapchains.IsLive(sc.slot, sc.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    match mut st.native {
        .Stub(s) {
            match s.fail_next_submit {
                some(e) {
                    s.fail_next_submit = none
                    st.lost = true
                    st.sticky_err = some(e)
                    fail e
                }
                none {}
            }
        }
        .Dx(d: _) | .Vk(v: _) {
            fail Unsupported
        }
    }
}

/// Presents t, after the last submission that rendered to it. Errors:
/// DeviceLost, SurfaceLost; on an error t is still the caller's. Not a
/// cancellation point. O(1) driver calls on the system stack.
export Device.Present(mut self, io: Io, t: SurfaceTexture): Error!Presented {
    _ = io
    try self.CheckAlive()
    if t.sc.device != self.serial {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    try CheckAndPresentStub(self.state.Mut(), &*t.sc)
    SurfaceTexture(sc: _, target, image: _) := t
    TextureView(slot: _, generation: _, device: _) := target
    return Presented(suboptimal: false)
}

/// Recreates the images at width by height once the GPU and presentation
/// engine are done with them. Errors: DeviceLost, SurfaceLost, Canceled
/// (repeatable). Suspends.
export Device.Resize(mut self, io: Io, sc: mut Swapchain, width: int, height: int): Error!void {
    _ = io
    try self.CheckAlive()
    if sc.device != self.serial {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    if width < 0 || height < 0 {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    try self.FlushPreSubmit()
    st := self.state.Mut()
    if !st.swapchains.IsLive(sc.slot, sc.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    se := st.swapchains.GetMut(sc.slot)
    se.info.width = width
    se.info.height = height
    v := st.views.GetMut(se.view_slot)
    v.width = width
    v.height = height
    match mut st.native {
        .Stub(s: _) {}
        .Dx(d: _) | .Vk(v: _) { fail Unsupported }
    }
}
