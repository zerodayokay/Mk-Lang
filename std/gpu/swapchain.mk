// Why: a program shows frames in a window through a swapchain; the loan
// checker refuses acquire, present and resize misuse at compile time.
//
// What: PresentMode, SwapchainOptions, DefaultSwapchainOptions, Swapchain,
// SwapchainInfo, SurfaceTexture, Acquired, Presented, and Swapchain methods.
//
// How: validates swapchain options and window surfaces at creation, wraps
// acquired back-buffer views in SurfaceTexture holding mut Swapchain, and
// dispatches Acquire/Present/Resize to the active backend.

import std.gpu.vk
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
    surf: ?window.Surface
}

IsSwapchainFormat(f: TextureFormat): bool {
    return match f {
        .Bgra8Unorm | .Bgra8UnormSrgb | .Rgba8Unorm | .Rgba8UnormSrgb { true }
        _ { false }
    }
}

CloseOptSurface(s: ?window.Surface) {
    match s {
        some(sf) { sf.Close() }
        none {}
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
    (width: ww, height: wh) := w.Size()
    sw := if ww > 0 { ww } else { 800 }
    sh := if wh > 0 { wh } else { 600 }
    var hwnd: uint = 0
    var hinst: uint = 0
    surf: ?window.Surface = match w.Surface() {
        ok(sf) {
            match sf.Native() {
                .Win32(hwnd: hw, hinstance: hi) {
                    hwnd = hw
                    hinst = hi
                }
                _ {}
            }
            some(sf)
        }
        err(e) {
            if self.Backend() == Backend.Vulkan {
                match e {
                    Closed { fail SurfaceLost }
                    _ { fail Unsupported }
                }
            }
            none
        }
    }
    onerr CloseOptSurface(surf)
    nh := try self.NativeCreateSwapchain(&o, sw, sh, hwnd, hinst)
    a := self.heap.Alloc()
    onerr self.state.Mut().native.DestroySwapchain(a, nh)

    v_nh := try self.NativeCreateSwapView()
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

    var actual_info := SwapchainInfo(format: o.format, mode: o.mode, width: sw, height: sh, images: o.buffers)
    match &self.state.Ref().native {
        .Vk(v) {
            gi := v.GetSwapchainInfo(nh)
            actual_info = SwapchainInfo(
                format: o.format,
                mode: if gi.mode == vk.ModeMailbox { PresentMode.Mailbox } else if gi.mode == vk.ModeImmediate { PresentMode.Immediate } else { PresentMode.Fifo },
                width: gi.width,
                height: gi.height,
                images: gi.images,
            )
            self.state.Mut().views.GetMut(vslot).width = gi.width
            self.state.Mut().views.GetMut(vslot).height = gi.height
        }
        _ {}
    }
    entry := SwapchainEntry(info: actual_info, view_slot: vslot, view_gen: vgen, native_handle: nh, surf: surf)
    match self.state.Mut().swapchains.Alloc(a, entry) {
        .Ok(slot, gen_) { return Swapchain(slot: slot, generation: gen_, device: self.serial) }
        .Oom(val) {
            SwapchainEntry(info: _, view_slot: _, view_gen: _, native_handle: _, surf: s2) := val
            match s2 {
                some(sf2) { sf2.Close() }
                none {}
            }
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
            SwapchainEntry(info: _, view_slot, view_gen: _, native_handle, surf) := e
            match surf {
                some(sf) { sf.Close() }
                none {}
            }
            st.native.DestroySwapchain(a, native_handle)
            return view_slot
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
    sc_nh := se.native_handle
    vslot := se.view_slot
    vgen := se.view_gen
    var win_w := se.info.width
    var win_h := se.info.height
    match &se.surf {
        some(sf) {
            sz := sf.Size()
            win_w = sz.width
            win_h = sz.height
        }
        none {}
    }
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
        .Dx(d: _) {
            fail Unsupported
        }
        .Vk(v) {
            ar := v.SwapchainAcquire(io, sc_nh, win_w, win_h) else e {
                match e {
                    vk.DeviceLost(reason: r) {
                        st.lost = true
                        st.sticky_err = some(DeviceLost(reason: MapVkLossReason(r)))
                    }
                    _ {}
                }
                fail MapVkErr(e)
            }
            if ar.status == vk.AcqOutdated {
                return Acquired.Outdated
            }
            if ar.status == vk.AcqHidden {
                return Acquired.Hidden
            }
            gi := v.GetSwapchainInfo(sc_nh)
            st.views.GetMut(vslot).native_handle = ar.view_id
            st.views.GetMut(vslot).width = gi.width
            st.views.GetMut(vslot).height = gi.height
            st.swapchains.GetMut(sc.slot).info.width = gi.width
            st.swapchains.GetMut(sc.slot).info.height = gi.height
            return Acquired.Ready(frame: SurfaceTexture(
                sc: sc,
                target: TextureView(slot: vslot, generation: vgen, device: self.serial),
                image: ar.image_idx,
            ))
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

CheckAndPresentNative(st: mut DeviceState, sc: &Swapchain, img_idx: u32): Error!bool {
    if !st.swapchains.IsLive(sc.slot, sc.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    sc_nh := st.swapchains.Get(sc.slot).native_handle
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
            return false
        }
        .Dx(d: _) {
            fail Unsupported
        }
        .Vk(v) {
            s1 := st.serials.Advance()
            s2 := st.serials.Advance()
            sub := v.SwapchainPresent(sc_nh, img_idx, s1, s2) else e {
                gerr := MapVkErr(e)
                match e {
                    vk.DeviceLost(reason: _) {
                        st.lost = true
                        st.sticky_err = some(gerr)
                    }
                    _ {}
                }
                fail gerr
            }
            return sub
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
    sub := try CheckAndPresentNative(self.state.Mut(), &*t.sc, t.image)
    SurfaceTexture(sc: _, target, image: _) := t
    TextureView(slot: _, generation: _, device: _) := target
    return Presented(suboptimal: sub)
}

/// Recreates the images at width by height once the GPU and presentation
/// engine are done with them. Errors: DeviceLost, SurfaceLost, Canceled
/// (repeatable). Suspends.
export Device.Resize(mut self, io: Io, sc: mut Swapchain, width: int, height: int): Error!void {
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
    sc_nh := se.native_handle
    vslot := se.view_slot
    match mut st.native {
        .Stub(s: _) {
            se.info.width = width
            se.info.height = height
            v := st.views.GetMut(vslot)
            v.width = width
            v.height = height
        }
        .Dx(d: _) { fail Unsupported }
        .Vk(v) {
            v.SwapchainResize(io, sc_nh, width, height) else e {
                gerr := MapVkErr(e)
                match e {
                    vk.DeviceLost(reason: _) {
                        st.lost = true
                        st.sticky_err = some(gerr)
                    }
                    _ {}
                }
                fail gerr
            }
            gi := v.GetSwapchainInfo(sc_nh)
            se2 := st.swapchains.GetMut(sc.slot)
            se2.info.width = gi.width
            se2.info.height = gi.height
            se2.info.images = gi.images
            v2 := st.views.GetMut(vslot)
            v2.width = gi.width
            v2.height = gi.height
        }
    }
}
