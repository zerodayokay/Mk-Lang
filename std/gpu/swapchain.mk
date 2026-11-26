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
import std.gpu.dx

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
    surface: ?window.Surface
    handed_out: bool
}

IsSwapchainFormat(f: TextureFormat): bool {
    return match f {
        .Bgra8Unorm | .Bgra8UnormSrgb | .Rgba8Unorm | .Rgba8UnormSrgb { true }
        _ { false }
    }
}

SwapchainDxgiFormats(f: TextureFormat): (dxgi_res: u32, dxgi_rtv: u32) {
    res := match f {
        .Rgba8Unorm | .Rgba8UnormSrgb { u32(28) }
        _ { u32(87) }
    }
    return (dxgi_res: res, dxgi_rtv: RowOf(f).dxgi_rtv_dsv)
}

MapWindowErr(e: window.Error): Error {
    return match e {
        window.Unsupported { Unsupported }
        Closed { SurfaceLost }
        Canceled { Canceled }
        OutOfMemory { OutOfMemory }
        window.Os(code: _) { SurfaceLost }
    }
}

PresentModeU8(m: PresentMode): u8 {
    return match m {
        .Fifo { 0 }
        .Mailbox { 1 }
        .Immediate { 2 }
    }
}

PresentModeFromU8(m: u8): PresentMode {
    return if m == 2 { PresentMode.Immediate } else if m == 1 { PresentMode.Mailbox } else { PresentMode.Fifo }
}

/// Makes a swapchain on w's client area at its current size. Errors:
/// Unsupported (format), SurfaceLost (the window is closed), DeviceLost.
/// Runs on the system stack. Counts as a user of w's native window.
export Device.NewSwapchain(mut self, io: Io, w: &window.Window, o: SwapchainOptions): Error!Swapchain {
    try self.CheckAlive()
    if !IsSwapchainFormat(o.format) {
        fail Unsupported
    }
    if o.buffers < 2 || o.buffers > 3 {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    is_stub := match &self.state.Ref().native {
        .Stub(s: _) { true }
        _ { false }
    }
    var window_handle: uint = 0
    var window_instance: uint = 0
    var ww := 0
    var wh := 0
    surf: ?window.Surface = match w.Surface() {
        ok(sf) {
            match sf.Native() {
                .Win32(hwnd: hw, hinstance: hi) {
                    window_handle = hw
                    window_instance = hi
                }
                .Unix(handle) {
                    window_handle = handle
                }
                _ {
                    if !is_stub {
                        sf.Close()
                        fail Unsupported
                    }
                }
            }
            sz := sf.Size()
            ww = sz.width
            wh = sz.height
            some(sf)
        }
        err(e) {
            if !is_stub {
                fail MapWindowErr(e)
            }
            match e {
                Closed { fail SurfaceLost }
                _ {}
            }
            sz := w.Size()
            ww = sz.width
            wh = sz.height
            none
        }
    }
    onerr CloseOptSurface(surf)
    sc := try self.NewSwapchainFromNative(io, window_handle, window_instance, ww, wh, o)
    old_surf := Replace(mut self.state.Mut().swapchains.GetMut(sc.slot).surface, surf)
    CloseOptSurface(old_surf)
    return sc
}

CloseOptSurface(surf: ?window.Surface) {
    match surf {
        some(s) { s.Close() }
        none {}
    }
}

Device.NewSwapchainFromNative(mut self, io: Io, window_handle: uint, window_instance: uint, ww: int, wh: int, o: SwapchainOptions): Error!Swapchain {
    _ = io
    try self.CheckAlive()
    if !IsSwapchainFormat(o.format) {
        fail Unsupported
    }
    if o.buffers < 2 || o.buffers > 3 {
        fail Invalid(misuse: Misuse.LimitExceeded, at: -1)
    }
    a := self.heap.Alloc()
    sw := if ww > 0 { ww } else { 800 }
    sh := if wh > 0 { wh } else { 600 }
    var nh: u64 = 0
    var actual_w := sw
    var actual_h := sh
    var actual_imgs := o.buffers
    var actual_mode := o.mode

    (stub: is_stub, dx: is_dx) := match &self.state.Ref().native {
        .Stub(s: _) { (stub: true, dx: false) }
        .Dx(d: _) { (stub: false, dx: true) }
        .Vk(v: _) { (stub: false, dx: false) }
    }
    if is_stub {
        nh = try self.NativeCreateSwapchain(&o, sw, sh, window_handle, window_instance)
    } else if is_dx {
        (dxgi_res, dxgi_rtv) := SwapchainDxgiFormats(o.format)
        spec := dx.SwapchainSpec(
            hwnd: window_handle,
            width: ww,
            height: wh,
            dxgi_res: dxgi_res,
            dxgi_rtv: dxgi_rtv,
            mode: PresentModeU8(o.mode),
            buffers: o.buffers,
            copy_src: o.copy_src,
        )
        st := self.state.Mut()
        match mut st.native {
            .Dx(d) {
                res := d.CreateSwapchain(&spec) else e {
                    SyncDxMessages(a, st)
                    ge := MapDxError(e)
                    RecordDxFailure(st, ge)
                    fail ge
                }
                nh = res.sc_nh
                actual_w = ww
                actual_h = wh
                actual_imgs = res.images
                actual_mode = PresentModeFromU8(res.mode)
                // CreateSwapchain also allocates a view handle inside dx.CreateSwapchain;
                // store that DxView handle on ViewEntry instead of allocating a duplicate.
                v_nh := res.view_nh
                SyncDxMessages(a, st)
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
                    width: if actual_w > 0 { actual_w } else { 1 },
                    height: if actual_h > 0 { actual_h } else { 1 },
                    samples: 1,
                    usage: usage,
                    plane_mask: 1,
                    is_swapchain: true,
                    native_handle: v_nh,
                )
                var vslot: u32 = 0
                var vgen: u32 = 0
                match st.views.Alloc(a, v_entry) {
                    .Ok(slot, gen_) {
                        vslot = slot
                        vgen = gen_
                    }
                    .Oom(val: _) {
                        st.native.DestroyView(v_nh)
                        st.native.DestroySwapchain(a, nh)
                        fail OutOfMemory
                    }
                }
                info := SwapchainInfo(format: o.format, mode: actual_mode, width: actual_w, height: actual_h, images: actual_imgs)
                entry := SwapchainEntry(info: info, view_slot: vslot, view_gen: vgen, native_handle: nh, surface: none, handed_out: false)
                match st.swapchains.Alloc(a, entry) {
                    .Ok(slot, gen_) { return Swapchain(slot: slot, generation: gen_, device: self.serial) }
                    .Oom(val) {
                        SwapchainEntry(info: _, view_slot: _, view_gen: _, native_handle: _, surface, handed_out: _) := val
                        CloseOptSurface(surface)
                        st.native.DestroyView(v_nh)
                        st.native.DestroySwapchain(a, nh)
                        _ = st.views.Release(a, vslot)
                        fail OutOfMemory
                    }
                }
            }
            _ {
                fail Unsupported
            }
        }
    } else {
        nh = try self.NativeCreateSwapchain(&o, sw, sh, window_handle, window_instance)
    }

    onerr self.state.Mut().native.DestroySwapchain(a, nh)
    var usage := NoTextureUsage
    usage.render_target = true
    usage.copy_src = o.copy_src
    var v_entry := ViewEntry(
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
        native_handle: 0,
    )
    v_nh := try self.NativeCreateSwapView()
    v_entry.native_handle = v_nh
    onerr self.state.Mut().native.DestroyView(v_nh)
    var vslot: u32 = 0
    var vgen: u32 = 0
    match self.state.Mut().views.Alloc(a, v_entry) {
        .Ok(slot, gen_) {
            vslot = slot
            vgen = gen_
        }
        .Oom(val: _) {
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
    entry := SwapchainEntry(info: actual_info, view_slot: vslot, view_gen: vgen, native_handle: nh, surface: none, handed_out: false)
    match self.state.Mut().swapchains.Alloc(a, entry) {
        .Ok(slot, gen_) { return Swapchain(slot: slot, generation: gen_, device: self.serial) }
        .Oom(val) {
            SwapchainEntry(info: _, view_slot: _, view_gen: _, native_handle: _, surface, handed_out: _) := val
            CloseOptSurface(surface)
            self.ReleaseSwapViewSlot(vslot)
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
            SwapchainEntry(info: _, view_slot, view_gen: _, native_handle, surface, handed_out: _) := e
            st.native.DestroySwapchain(a, native_handle)
            CloseOptSurface(surface)
            return view_slot
        }
        none { return 0 }
    }
}

WaitSwapchainClose(io: Io, dev: mut Device) {
    st := dev.state.Mut()
    if !st.lost {
        match mut st.native {
            .Dx(d) { _ = d.WaitQueueIdle(io.NoCancel(), mut st.serials.next) else {} }
            _ {}
        }
        st.serials.Complete(st.serials.LastSubmitted())
    }
}

/// Waits until the GPU and the presentation engine are done with the
/// images, then releases them. Not cancellable; never fails.
export Swapchain.Close(self, io: Io, dev: mut Device) {
    WaitSwapchainClose(io, dev)
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
    sc_nh: u64
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
    a := self.heap.Alloc()
    st := self.state.Mut()
    if !st.swapchains.IsLive(sc.slot, sc.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    se := st.swapchains.Get(sc.slot)
    sc_nh := se.native_handle
    vslot := se.view_slot
    vgen := se.view_gen
    v_nh := st.views.Get(vslot).native_handle
    cur_w := se.info.width
    cur_h := se.info.height
    handed_out := se.handed_out
    var win_w := cur_w
    var win_h := cur_h
    match &se.surface {
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
            if win_w == 0 || win_h == 0 {
                return Acquired.Hidden
            }
            return Acquired.Ready(frame: SurfaceTexture(
                sc: sc,
                target: TextureView(slot: vslot, generation: vgen, device: self.serial),
                image: 0,
                sc_nh: 0,
            ))
        }
        .Dx(d) {
            if win_w == 0 || win_h == 0 {
                return Acquired.Hidden
            }
            if handed_out {
                dx.AbandonSwapchain(sc_nh)
                return Acquired.Outdated
            }
            ar := d.AcquireSwapchain(io, sc_nh, v_nh, cur_w, cur_h) else e {
                SyncDxMessages(a, st)
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
            SyncDxMessages(a, st)
            if ar.status == 1 {
                return Acquired.Outdated
            }
            if ar.status == 2 {
                return Acquired.Hidden
            }
            st.swapchains.GetMut(sc.slot).handed_out = true
            return Acquired.Ready(frame: SurfaceTexture(
                sc: sc,
                target: TextureView(slot: vslot, generation: vgen, device: self.serial),
                image: ar.image_idx,
                sc_nh: sc_nh,
            ))
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
                sc_nh: 0,
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
    SurfaceTexture(sc: _, target, image: _, sc_nh) := self
    TextureView(slot: _, generation: _, device: _) := target
    if sc_nh != 0 {
        dx.AbandonSwapchain(sc_nh)
    }
}

/// What a present reported.
export struct Presented {
    /// The surface would present better recreated: Resize when convenient.
    export suboptimal: bool
}

CheckAndPresent(a: &Alloc, st: mut DeviceState, sc: &Swapchain, img_idx: u32): Error!bool {
    if !st.swapchains.IsLive(sc.slot, sc.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    sc_nh := st.swapchains.Get(sc.slot).native_handle
    last_sub := st.serials.LastSubmitted()
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
        .Dx(d) {
            sub := d.PresentSwapchain(sc_nh, last_sub) else e {
                SyncDxMessages(a, st)
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
            SyncDxMessages(a, st)
            st.swapchains.GetMut(sc.slot).handed_out = false
            return sub
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
    sub := try CheckAndPresent(self.heap.Alloc(), self.state.Mut(), &*t.sc, t.image)
    SurfaceTexture(sc: _, target, image: _, sc_nh: _) := t
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
    a := self.heap.Alloc()
    st := self.state.Mut()
    if !st.swapchains.IsLive(sc.slot, sc.generation) {
        fail Invalid(misuse: Misuse.StaleHandle, at: -1)
    }
    sc_nh := st.swapchains.Get(sc.slot).native_handle
    view_slot := st.swapchains.Get(sc.slot).view_slot
    v_nh := st.views.Get(view_slot).native_handle
    match mut st.native {
        .Stub(s: _) {
            se := st.swapchains.GetMut(sc.slot)
            se.handed_out = false
            se.info.width = width
            se.info.height = height
            v := st.views.GetMut(view_slot)
            v.width = if width > 0 { width } else { 1 }
            v.height = if height > 0 { height } else { 1 }
        }
        .Dx(d) {
            d.ResizeSwapchain(io, sc_nh, v_nh, width, height, mut st.serials.next) else e {
                SyncDxMessages(a, st)
                ge := MapDxError(e)
                RecordDxFailure(st, ge)
                fail ge
            }
            SyncDxMessages(a, st)
            se := st.swapchains.GetMut(sc.slot)
            se.handed_out = false
            se.info.width = width
            se.info.height = height
            v := st.views.GetMut(view_slot)
            v.width = if width > 0 { width } else { 1 }
            v.height = if height > 0 { height } else { 1 }
        }
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
            se2.handed_out = false
            se2.info.width = gi.width
            se2.info.height = gi.height
            se2.info.images = gi.images
            v2 := st.views.GetMut(view_slot)
            v2.width = gi.width
            v2.height = gi.height
        }
    }
}
