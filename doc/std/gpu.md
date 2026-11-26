# std.gpu

`std.gpu` draws and computes on a graphics card through Direct3D 12 or Vulkan behind one API: devices, buffers, textures, samplers, bind groups, render and compute pipelines, a command encoder with render and compute passes, swapchains that present into a `std.window` window, and readback. Every device object is a linear handle closed on the device that made it, so a leak or a double close does not compile. Misuse a GPU would not catch, such as a draw with no pipeline or a binding that does not match its layout, is caught before anything reaches the driver and reported as `Invalid` with what was wrong. The module is Experimental: its API may change before it is stabilised.

Read the [library guide](README.md) first, and [std.window](window.md) for windows.

```mk
// verdict: parse
import std.gpu
```

## Contents

- [The main pieces](#the-main-pieces)
- [Requirements](#requirements)
- [Objects and their lifetimes](#objects-and-their-lifetimes)
- [Recording and submitting](#recording-and-submitting)
- [Presenting to a window](#presenting-to-a-window)
- [Compiling shaders](#compiling-shaders)
- [The renderer pattern: pass fields, not self](#the-renderer-pattern-pass-fields-not-self)
- [Debugging](#debugging)
- [Errors](#errors)
- [Examples](#examples): [render offscreen and read back](#render-offscreen-and-read-back), [a compute pass](#a-compute-pass), [a window's frame loop](#a-windows-frame-loop), [refused at compile time](#refused-at-compile-time)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Find and open an adapter | `Adapters`, `Open` with `DeviceOptions` (`DefaultDeviceOptions`) |
| Make memory | `Device.NewBuffer` with `BufferUsage`, `Device.NewTexture` with `TextureDesc`, `Device.NewView`, `Device.NewSampler` |
| Fill and read memory | `Device.WriteBuffer`, `Device.WriteTexture`, `Device.ReadTexture`, `Device.MapRead` and `Device.MapWrite` on staging buffers |
| Describe what shaders see | `Device.NewBindGroupLayout`, `Device.NewPipelineLayout`, `Device.NewBindGroup` |
| Load and check shaders | `ShaderCode` (SPIR-V and DXIL), `CheckShader`, `Device.NewShader` |
| Make pipelines | `Device.NewRenderPipeline` with `RenderPipelineDesc` (`DefaultRenderPipeline`), `Device.NewComputePipeline` |
| Record work | `Device.Encoder`, `Encoder.RenderPass`, `Encoder.ComputePass`, the copy calls, `Encoder.Submit` |
| Show frames in a window | `Device.NewSwapchain`, `Device.Acquire`, `Device.Present`, `Device.Resize` |
| Find mistakes | `DebugMode`, `Device.TakeMessages`, `Device.Close`'s `Leaks`, debug groups |

## Requirements

- **Targets.** Direct3D 12 runs on x86-64 Windows. Vulkan runs on x86-64 Windows, x86-64 and AArch64 Linux, and x86-64 and AArch64 macOS. A program can check for a supported adapter and continue without GPU access.
- **Direct3D 12.** An adapter at feature level 11_0 or higher. WARP, the software adapter Windows ships, is always present: `DeviceOptions.software = true` picks it, which is how tests and machines without a GPU run. `Backend.Auto` tries Direct3D 12 first.
- **Vulkan.** Install a Vulkan 1.3 loader and driver. Windows loads `vulkan-1.dll`. Linux uses `libvulkan.so.1` and a glibc executable built with `pkg.Link(name: "libc.so.6", kind: "dynamic", path: "", target: "x86_64-linux")` or the AArch64 equivalent. macOS uses `libvulkan.1.dylib` and a Vulkan 1.3-capable MoltenVK ICD on a Metal-capable host. On macOS, `MK_VULKAN_LIBRARY` can point to an absolute loader path. Make that same loader discoverable to GLFW when presenting a window. [MoltenVK installation](https://github.com/KhronosGroup/MoltenVK#using-the-vulkan-sdk)
- **Windowed Vulkan.** Install GLFW 3 on Linux or macOS. On Linux, GLFW uses the available X11 or Wayland backend. On macOS, it creates the Metal-backed window surface. Offscreen rendering needs only the Vulkan loader and driver. [GLFW platform support](https://www.glfw.org/docs/latest/compat_guide.html)
- **Debug layers.** `DebugMode.Validate` uses the Direct3D 12 debug layer (the Windows "Graphics Tools" optional feature) or the Vulkan validation layers (the Vulkan SDK) when installed, and goes on without them; `DebugMode.Strict` fails with `Missing(feature: Feature.DebugLayer)` when they are absent.
- **Shaders.** Both blobs of every shader, SPIR-V and DXIL, made ahead of time from HLSL; see [Compiling shaders](#compiling-shaders). Nothing compiles shaders at run time.

## Objects and their lifetimes

`Open` returns a `Device`, which owns the adapter and everything made on it. Each `New*` call returns a linear handle (`Buffer`, `Texture`, `TextureView`, `Sampler`, `Shader`, `BindGroupLayout`, `PipelineLayout`, `BindGroup`, `RenderPipeline`, `ComputePipeline`, `Swapchain`) that is ended by its `Close`, which takes the device: `buf.Close(dev)`. The compiler checks that every handle is closed on every path, so a function that makes several registers an `onerr` or `defer` close after each one.

- A handle is a small value (a slot and a generation), so passing it is free. A handle used after its object was closed, or on another device, fails with `Invalid(misuse: Misuse.StaleHandle)`; closing one on another device panics (code 12).
- Closing an object still in use is safe: the native object goes once the GPU and every bind group that holds it are done with it.
- `Device.Close(io)` waits for the GPU, releases the device and returns its `Leaks`: the native objects still alive (zero when every handle was closed) and the debug layer's errors and warnings. A test asserts `leaks.native == 0 && leaks.errors == 0`. `Device.Abandon(io)` releases it without counting, for error paths: `onerr dev.Abandon(io)`.
- Every resource starts zeroed: a buffer or texture read before it is written reads zeros on every backend.

## Recording and submitting

`Device.Encoder(label)` starts a command list. Inside it, `Encoder.RenderPass(&desc)` opens a render pass on its colour and depth attachments and `Encoder.ComputePass(label)` a compute pass; each pass is linear and ends with `End`. Between passes the encoder records copies (`CopyBuffer`, `CopyBufferToTexture`, `CopyTextureToBuffer`, `CopyTexture`) and `WriteBuffer`. `Encoder.Submit()` sends the work to the GPU's queue; `Encoder.Abandon()` drops it unsent.

- Recording only stores commands; nothing calls the driver until `Submit`, which replays them in one go. A pass's calls cost a few stores each.
- Synchronisation is the library's: it tracks the state of every buffer and every texture subresource and inserts the barriers each use needs, including between passes and between a copy and a draw. A program never writes a barrier.
- Draw and dispatch state is checked as it is recorded: a draw with no pipeline, a bind group whose layout does not match the pipeline's, a missing vertex or index buffer, or a dynamic offset count that differs from the layout is noted, and `Encoder.Submit` then fails with the first one, `Invalid(misuse, at)` with `at` the index of the offending command, before anything reaches the driver. The encoder is still the caller's, to `Abandon`.
- `Device.WriteBuffer` and `Device.WriteTexture` copy at once through an upload ring and are ordered before the next submit. `Device.ReadTexture` waits for the GPU and copies a region into an arena.
- A buffer the CPU reads or writes directly is a staging buffer: `map_read` with only `copy_dst`, or `map_write` with only `copy_src`. Copy into or out of it on the encoder; `Device.MapRead` waits for the GPU's last use and returns a linear `Mapped` view, ended by `Unmap`.

## Presenting to a window

`Device.NewSwapchain(io, &win, options)` attaches a swapchain to a [`window.Window`](window.md). `SwapchainOptions` chooses the format (`Bgra8UnormSrgb` by default; `Bgra8Unorm` keeps bytes as they are), the `PresentMode` (`Fifo` waits for vertical blank; `Mailbox` and `Immediate` fall back to it where absent) and 2 or 3 buffers. Each frame:

1. `Device.Acquire(io, mut sc)` returns `Acquired.Ready(frame)`, `Acquired.Outdated` (the window changed size: call `Device.Resize` with `window.Size()`, then acquire again) or `Acquired.Hidden` (minimised: wait for a window event).
2. Record passes whose colour attachment is `frame.View()`.
3. `Device.Present(io, frame)` consumes the frame. A frame not presented is ended by `frame.Abandon()`, after which the next `Acquire` reports `Outdated`.

`Device.Acquire` waits while `frames_in_flight` frames are still on the GPU, so the CPU stays at most that many frames ahead. On a `window.EventKind.Resized` event, call `Device.Resize` with the new size before the next acquire.

## Compiling shaders

Shaders are written in HLSL and compiled ahead of time with [DXC](https://github.com/microsoft/DirectXShaderCompiler) into two blobs: SPIR-V for Vulkan and DXIL for Direct3D 12. A program embeds both, usually with `build.File`, and passes them as one `ShaderCode`. Use one DXC release for every blob and pin it; the dogfoods use v1.9.2609.

For each entry point, with `<s>` the stage (`vs`, `ps` or `cs`):

```sh
dxc -T <s>_6_0 -E <entry> -O3 -Zpr -Qstrip_debug -Fo shader.dxil shader.hlsl
dxc -T <s>_6_0 -E <entry> -O3 -Zpr -spirv -fspv-target-env=vulkan1.3 -fvk-use-dx-layout -fvk-support-nonzero-base-vertex -fvk-support-nonzero-base-instance -Fo shader.spv shader.hlsl
```

Add `-fvk-use-dx-position-w` for a pixel shader's SPIR-V. With these flags both backends see the same matrices, buffer layouts, clip space and vertex and instance ids. Each stage is its own `Shader`, so a file holding a vertex and a pixel shader is compiled twice, once per entry.

The binding convention, which `CheckShader` enforces:

| HLSL | Means |
|---|---|
| `register(xN, spaceG)` | Binding N of bind group G, whatever the register class `x` (`b`, `t`, `u`, `s`); N is unique within a group across classes |
| `[[vk::push_constant]] ConstantBuffer<T> pc : register(b0, space8);` | The immediates: the bytes `SetImmediates` writes, at most the pipeline layout's `immediates` size |
| `[[vk::location(n)]] float3 pos : LOC<n>` | Vertex attribute at location n |
| Matrices | Row-major (`-Zpr`) on both backends |

`CheckShader(code, stage, entry)` reads both blobs without a device and checks that the SPIR-V holds the entry point for that stage, follows the convention and stays within the reader's bounds, and that the DXIL container is signed and of that stage; it is cheap enough to run in a test on every target. `Device.NewShader` runs the same checks, and `NewRenderPipeline` and `NewComputePipeline` check the shaders' bindings against the pipeline layout.

The dogfoods [scene](../../Dogfoods/scene) and [sketch](../../Dogfoods/sketch) each keep their HLSL in `shaders/`, the blobs in `shaders/bin`, and a `tools/shaders.ps1` that runs both command lines, runs `CheckShader` on every pair, and writes a module that embeds the blobs with the SHA-256 of each source and blob, so a test checks the committed blobs match their sources without DXC.

## The renderer pattern: pass fields, not self

A renderer usually keeps the device and everything made on it in one linear struct, built by a function that closes what it has made when a later step fails, and ended by a `Close` that releases it all in reverse order and returns the device's `Leaks`. Recording borrows the device mutably (the encoder holds it) while the passes borrow pipelines, bind groups and views. A method on `&mut self` cannot hand out both, so give the recording function the fields it needs, one by one:

```mk
// verdict: ok
import std.gpu

struct Renderer {
    dev: gpu.Device
    pipe: gpu.RenderPipeline
    group: gpu.BindGroup
}

// Record draws one triangle into target; it borrows pipe and group while the
// caller's encoder borrows the device.
Record(enc: mut gpu.Encoder, target: &gpu.TextureView, pipe: &gpu.RenderPipeline, group: &gpu.BindGroup) {
    colors := [gpu.ColorAttachment(target: target, resolve: none, load: gpu.Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: gpu.Store.Store)]
    desc := gpu.PassDesc(colors: colors[..], depth: none, label: "triangle")
    var pass := enc.RenderPass(&desc)
    pass.SetPipeline(pipe)
    var no_offsets: [0]u32 = []
    pass.SetBindGroup(0, group, no_offsets[..])
    pass.Draw(3, 1, 0, 0)
    pass.End()
}

Renderer.Frame(mut self, target: &gpu.TextureView): gpu.Error!void {
    var enc := try self.dev.Encoder("frame")
    onerr enc.Abandon()
    Record(mut enc, target, &self.pipe, &self.group)
    try enc.Submit()
}
```

The `Dogfoods/scene` renderer is this pattern at full size: about sixty objects, created under `onerr` closes and released by one `Close`.

## Debugging

- `DebugMode.Off` (the default) runs the portable checks only. `Validate` adds the driver's debug layer where installed and turns its errors into the device's sticky error, so the next call fails. `Strict` requires the layer and also fails on its warnings, except those about performance.
- `Device.TakeMessages(a)` returns the layer's messages since the last call, with their `Severity`, id and text.
- `Encoder.PushDebugGroup(label)` and `PopDebugGroup`, and the same on passes, name regions of a frame for a GPU debugger such as PIX or RenderDoc; every `label` argument names its object there too.
- `Device.Close`'s `Leaks` counts what was not released, and the debug layer's errors and warnings over the device's life.

## Errors

| Error | When |
|---|---|
| `Unsupported` | No GPU backend on this target |
| `NoAdapter` | No adapter matches `DeviceOptions` (backend, software, adapter id) |
| `Missing(feature)` | `DeviceOptions.features` asked for one the adapter lacks, or `Strict` without a debug layer |
| `Invalid(misuse, at)` | The program broke a rule; `misuse` says which (`Misuse` formats as a sentence) and `at` the command, entry or attribute index, or -1 |
| `DeviceLost(reason)` | The driver reset or removed the device; it is unusable, so close it and open another |
| `OutOfDeviceMemory` | GPU memory is exhausted; `OutOfMemory` is the CPU side's |
| `SurfaceLost` | The swapchain's window is gone |
| `Driver(code, call)` | A driver call failed in a way none of the above covers; `call` names it |
| `Canceled` | The task was canceled while waiting for the GPU |

`DeviceLost` and the debug layer's errors are sticky: after one, every call on the device fails the same way until it is closed. Other errors leave the device as it was.

## Examples

These take their shaders as `ShaderCode` arguments; a program embeds the blobs with `build.File`.

### Render offscreen and read back

Draws one triangle into a texture and reads its pixels back, tight RGBA8 rows, without a window, as a test does:

```mk
// verdict: ok
import std.gpu

const Size: int = 64

Triangle(a: &Arena, io: Io, dev: mut gpu.Device, vs_code: gpu.ShaderCode, ps_code: gpu.ShaderCode): gpu.Error![]u8 from a {
    var td := gpu.DefaultTextureDesc
    td.width = Size
    td.height = Size
    td.format = gpu.TextureFormat.Rgba8Unorm
    td.usage.render_target = true
    td.usage.copy_src = true
    tex := try dev.NewTexture(td)
    defer tex.Close(dev)
    target := try dev.NewView(&tex, gpu.DefaultViewDesc)
    defer target.Close(dev)

    var no_groups: [0]&gpu.BindGroupLayout = []
    layout := try dev.NewPipelineLayout(no_groups[..], 0, "triangle")
    defer layout.Close(dev)
    vs := try dev.NewShader(vs_code, gpu.Stage.Vertex, "VsMain", "triangle vs")
    defer vs.Close(dev)
    ps := try dev.NewShader(ps_code, gpu.Stage.Fragment, "PsMain", "triangle ps")
    defer ps.Close(dev)
    targets := [gpu.ColorOf(gpu.TextureFormat.Rgba8Unorm)]
    var pd := gpu.DefaultRenderPipeline
    pd.targets = targets[..]
    pipe := try dev.NewRenderPipeline(io, &layout, &vs, some(&ps), pd)
    defer pipe.Close(dev)

    var enc := try dev.Encoder("offscreen")
    onerr enc.Abandon()
    colors := [gpu.ColorAttachment(target: &target, resolve: none, load: gpu.Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0), store: gpu.Store.Store)]
    desc := gpu.PassDesc(colors: colors[..], depth: none, label: "triangle")
    var pass := enc.RenderPass(&desc)
    pass.SetPipeline(&pipe)
    pass.Draw(3, 1, 0, 0)
    pass.End()
    try enc.Submit()
    region := gpu.TextureRegion(texture: &tex, mip: 0, layer: 0, x: 0, y: 0, aspect: gpu.Aspect.All)
    return try dev.ReadTexture(a, io, region, gpu.Extent(width: Size, height: Size, layers: 1))
}

// Run opens WARP, so it runs on any Windows machine, GPU or not.
Run(sys: &System, vs: gpu.ShaderCode, ps: gpu.ShaderCode): gpu.Error!int {
    var o := gpu.DefaultDeviceOptions
    o.backend = gpu.Backend.D3d12
    o.software = true
    var dev := try gpu.Open(sys.heap, sys.io, o)
    onerr dev.Abandon(sys.io)
    pixels := try Triangle(sys.arena, sys.io, mut dev, vs, ps)
    leaks := dev.Close(sys.io)
    Assert(leaks.native == 0)
    return pixels.Len()
}
```

### A compute pass

Doubles the floats in a storage buffer and reads them back through a staging buffer. The shader is `RWStructuredBuffer<float> data : register(u0, space0);` with `[numthreads(64, 1, 1)]`:

```mk
// verdict: ok
import std.gpu

Double(a: &Arena, io: Io, dev: mut gpu.Device, cs_code: gpu.ShaderCode, input: []u8): gpu.Error![]u8 from a {
    var bu := gpu.NoBufferUsage
    bu.storage = true
    bu.copy_src = true
    bu.copy_dst = true
    buf := try dev.NewBuffer(input.Len(), bu, "data")
    defer buf.Close(dev)
    try dev.WriteBuffer(&buf, 0, input)
    var ru := gpu.NoBufferUsage
    ru.map_read = true
    ru.copy_dst = true
    rb := try dev.NewBuffer(input.Len(), ru, "readback")
    defer rb.Close(dev)

    vis := gpu.Visibility(vertex: false, fragment: false, compute: true)
    entries := [gpu.LayoutEntry(binding: 0, visibility: vis, kind: gpu.BindingKind.Storage(read_only: false, min_size: 4, stride: 4))]
    bgl := try dev.NewBindGroupLayout(entries[..], "data")
    defer bgl.Close(dev)
    groups := [&bgl]
    layout := try dev.NewPipelineLayout(groups[..], 0, "double")
    defer layout.Close(dev)
    binds := [gpu.BindEntry(binding: 0, bound: gpu.Bound.Buffer(b: &buf, offset: 0, size: input.Len()))]
    group := try dev.NewBindGroup(&bgl, binds[..], "data")
    defer group.Close(dev)
    cs := try dev.NewShader(cs_code, gpu.Stage.Compute, "CsMain", "double")
    defer cs.Close(dev)
    pipe := try dev.NewComputePipeline(io, &layout, &cs, "double")
    defer pipe.Close(dev)

    var enc := try dev.Encoder("double")
    onerr enc.Abandon()
    var pass := enc.ComputePass("double")
    pass.SetPipeline(&pipe)
    var no_offsets: [0]u32 = []
    pass.SetBindGroup(0, &group, no_offsets[..])
    pass.Dispatch((input.Len() / 4 + 63) / 64, 1, 1)
    pass.End()
    enc.CopyBuffer(&buf, 0, &rb, 0, input.Len())
    try enc.Submit()

    m := try dev.MapRead(io, &rb, 0, input.Len())
    defer m.Unmap()
    out := a.Slice[u8](input.Len()) else { fail OutOfMemory }
    out.CopyFrom(m.Bytes())
    return out[..]
}
```

### A window's frame loop

Clears a window every frame until it is closed, resizing the swapchain when the window changes and waiting while it is minimised:

```mk
// verdict: ok
import std.gpu
import std.window

Clear(io: Io, dev: mut gpu.Device, sc: mut gpu.Swapchain, win: &window.Window): gpu.Error!bool {
    match try dev.Acquire(io, sc) {
        .Ready(frame) {
            onerr frame.Abandon()
            var enc := try dev.Encoder("frame")
            onerr enc.Abandon()
            colors := [gpu.ColorAttachment(target: frame.View(), resolve: none, load: gpu.Load.Clear(r: 0.1, g: 0.2, b: 0.3, a: 1.0), store: gpu.Store.Store)]
            desc := gpu.PassDesc(colors: colors[..], depth: none, label: "clear")
            var pass := enc.RenderPass(&desc)
            pass.End()
            try enc.Submit()
            _ = try dev.Present(io, frame)
            return true
        }
        .Outdated {
            (width: w, height: h) := win.Size()
            try dev.Resize(io, sc, w, h)
            return true
        }
        .Hidden { return false }
    }
}

Run(sys: &System, dev: mut gpu.Device, win: &window.Window): gpu.Error!void {
    var sc := try dev.NewSwapchain(sys.io, win, gpu.DefaultSwapchainOptions)
    defer sc.Close(sys.io, dev)
    for !win.CloseRequested() {
        for ev := win.Poll() {
            match ev.kind {
                .Resized(width, height) { try dev.Resize(sys.io, mut sc, width, height) }
                _ {}
            }
        }
        if !try Clear(sys.io, dev, mut sc, win) {
            win.Wait(sys.io) else { return }
        }
    }
}
```

### Refused at compile time

A handle that is never closed does not compile:

```mk
// verdict: reject E0402
import std.gpu

Leak(dev: mut gpu.Device): gpu.Error!void {
    var u := gpu.NoBufferUsage
    u.copy_dst = true
    buf := try dev.NewBuffer(256, u, "leaked") // <- E0402
    zeros: [4]u8 = [0; 4]
    try dev.WriteBuffer(&buf, 0, zeros[..])
}
```

## API

<!-- api:begin -->
Stability: Experimental (its API may change before stabilisation).

### `Acquired`

```
export enum Acquired
```

The outcome of Acquire.

Variants:

- `Ready(frame: SurfaceTexture)`
- `Outdated`: The surface changed, or an image was abandoned: Resize, then Acquire.
- `Hidden`: The window is minimised: wait for an event, then Acquire.

### `AdapterId`

```
export struct AdapterId
```

Names one adapter across runs while it is present: its backend and its
LUID (DXGI's AdapterLuid, Vulkan's deviceLUID).

Fields:

- `backend: Backend`
- `luid: u64`

### `AdapterInfo`

```
export struct AdapterInfo
```

One adapter Adapters found.

Fields:

- `id: AdapterId`
- `name: str`
- `vendor: u32`
- `device: u32`
- `kind: AdapterKind`
- `limits: DeviceLimits`
- `features: Features`

### `AdapterKind`

```
export open enum AdapterKind
```

What kind of processor an adapter is.

Variants:

- `Discrete`
- `Integrated`
- `Software`: WARP on D3D12; a CPU device on Vulkan.
- `Other`

### `Adapters`

```
export Adapters(a: &Arena, io: Io, backend: Backend): Error!List[AdapterInfo] from a
```

- Flows: The result borrows from a.
- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Lists the adapters of backend (Auto: every backend present), best first.
An adapter missing a requirement or a guaranteed format capability is
not listed. Errors: Unsupported off Windows; Canceled while queued.
O(adapters × formats) driver queries on the blocking pool; allocates
the list and the names in a.

### `Address`

```
export open enum Address
```

What a sampler reads outside 0..1.

Variants:

- `Repeat`
- `MirrorRepeat`
- `ClampToEdge`

### `Aspect`

```
export open enum Aspect
```

The part of a depth-stencil format a view or copy covers.

Variants:

- `All`
- `Depth`
- `Stencil`

### `Backend`

```
export open enum Backend
```

Which graphics API a device runs on.

Variants:

- `Auto`: D3D12 first on Windows, then Vulkan; Vulkan elsewhere.
- `D3d12`
- `Vulkan`

### `BindEntry`

```
export struct BindEntry
```

One entry of a bind group.

Fields:

- `binding: int`
- `bound: Bound`

### `BindGroup`

```
export linear struct BindGroup
```

Resources bound together; each lives while the group does.

### `BindGroup.Close`

```
export BindGroup.Close(self, dev: mut Device)
```

Gives the group back. O(1).

### `BindGroupLayout`

```
export linear struct BindGroupLayout
```

The shape of a bind group.

### `BindGroupLayout.Close`

```
export BindGroupLayout.Close(self, dev: mut Device)
```

Gives the layout back. O(1).

### `BindingKind`

```
export open enum BindingKind
```

What one binding of a group holds.

Variants:

- `Uniform(dynamic: bool, min_size: int)`: A constant buffer. dynamic: its offset is given at SetBindGroup, and
  the buffer needs uniform_dynamic. min_size bounds the shader's block.
- `Storage(read_only: bool, min_size: int, stride: int)`: stride: 0 for ByteAddressBuffer, sizeof(T) for StructuredBuffer<T>.
  A writable one is visible to compute only.
- `Texture(sample: SampleType, dimension: ViewDimension, multisampled: bool)`
- `Sampler(comparison: bool)`

### `Blend`

```
export struct Blend
```

Colour and alpha blend equations.

Fields:

- `color: BlendComponent`
- `alpha: BlendComponent`

### `BlendComponent`

```
export struct BlendComponent
```

One blend equation.

Fields:

- `src: BlendFactor`
- `dst: BlendFactor`
- `op: BlendOp`

### `BlendFactor`

```
export open enum BlendFactor
```

A blend factor.

Variants:

- `Zero`
- `One`
- `Src`
- `OneMinusSrc`
- `SrcAlpha`
- `OneMinusSrcAlpha`
- `Dst`
- `OneMinusDst`
- `DstAlpha`
- `OneMinusDstAlpha`
- `Constant`
- `OneMinusConstant`

### `BlendOp`

```
export open enum BlendOp
```

A blend operation.

Variants:

- `Add`
- `Subtract`
- `ReverseSubtract`
- `Min`
- `Max`

### `BlendOver`

```
export const BlendOver: Blend = Blend(color: BlendComponent(src: BlendFactor.One, dst: BlendFactor.OneMinusSrcAlpha, op: BlendOp.Add), alpha: BlendComponent(src: BlendFactor.One, dst: BlendFactor.OneMinusSrcAlpha, op: BlendOp.Add))
```

Premultiplied-alpha "over".

### `Bound`

```
export enum Bound
```

The resource a bind entry points at.

Variants:

- `Buffer(b: &Buffer, offset: int, size: int)`
- `View(v: &TextureView)`
- `Sampler(s: &Sampler)`

### `Buffer`

```
export linear struct Buffer
```

A GPU buffer its device owns. Linear: end it with Close.

### `Buffer.Close`

```
export Buffer.Close(self, dev: mut Device)
```

Gives the buffer back; the native buffer goes when the GPU and every
bind group are done with it. Panics (code 12) given another device. O(1).

### `BufferUsage`

```
export struct BufferUsage
```

What a buffer may be used for. Start from NoBufferUsage and set fields.

Fields:

- `vertex: bool`
- `index: bool`
- `uniform: bool`: Bound to a non-dynamic Uniform entry.
- `uniform_dynamic: bool`: Bound to a dynamic Uniform entry. The D3D12 resource is 64 KiB larger
  than asked (zeroed, never addressable through the API).
- `storage: bool`
- `copy_src: bool`
- `copy_dst: bool`
- `map_read: bool`: Readable by MapRead; only with copy_dst.
- `map_write: bool`: Writable by MapWrite; only with copy_src.

### `Call`

```
export open enum Call
```

The driver call a Driver error came from.

Variants:

- `CreateFactory`
- `EnumAdapters`
- `CreateDevice`
- `CreateHeap`
- `CreateResource`
- `CreateRootSignature`
- `CreatePipeline`
- `CreateSwapchain`
- `ResizeBuffers`
- `Present`
- `ExecuteCommandLists`
- `Signal`
- `CreateInstance`
- `EnumeratePhysicalDevices`
- `AllocateMemory`
- `CreateImage`
- `CreateBuffer`
- `CreateDescriptorPool`
- `CreateSemaphore`
- `AcquireNextImage`
- `QueueSubmit`
- `QueuePresent`
- `WaitSemaphores`
- `Map`

### `CheckShader`

```
export CheckShader(code: ShaderCode, stage: Stage, entry: str): Error!void
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Checks code's SPIR-V by the module-level rules (bounded full walk, the
binding convention, no counters or combined samplers, the immediates
layout) with no device: what tools/gpushaders runs at build time.
Errors: Invalid(MissingSpirv), Invalid(BadShader), Invalid(ShaderMismatch).
O(words); allocates nothing.

### `ColorAttachment`

```
export struct ColorAttachment
```

One colour attachment of a pass.

Fields:

- `target: &TextureView`
- `resolve: ?&TextureView`
- `load: Load`
- `store: Store`

### `ColorOf`

```
export ColorOf(format: TextureFormat): ColorTarget
```

A target of format with no blending, writing every channel. O(1).

### `ColorTarget`

```
export struct ColorTarget
```

One colour target of a pipeline.

Fields:

- `format: TextureFormat`
- `blend: ?Blend`
- `write: ColorWrites`

### `ColorWrites`

```
export struct ColorWrites
```

Channels a target writes.

Fields:

- `r: bool`
- `g: bool`
- `b: bool`
- `a: bool`

### `Compare`

```
export open enum Compare
```

A comparison function for depth, stencil and comparison samplers.

Variants:

- `Never`
- `Less`
- `Equal`
- `LessEqual`
- `Greater`
- `NotEqual`
- `GreaterEqual`
- `Always`

### `ComputePass`

```
export linear view struct ComputePass
```

A compute pass being recorded. Linear: End it.

### `ComputePass.Dispatch`

```
export ComputePass.Dispatch(mut self, x: int, y: int, z: int)
```

Dispatches x by y by z workgroups, each at most the limit. O(1).

### `ComputePass.End`

```
export ComputePass.End(self)
```

Ends the pass. O(1).

### `ComputePass.PopDebugGroup`

```
export ComputePass.PopDebugGroup(mut self)
```

Closes the innermost debug group. O(1).

### `ComputePass.PushDebugGroup`

```
export ComputePass.PushDebugGroup(mut self, label: str)
```

Opens a debug group. O(label).

### `ComputePass.SetBindGroup`

```
export ComputePass.SetBindGroup(mut self, index: int, g: &BindGroup, dynamic: []u32)
```

Binds group g at index, as RenderPass.SetBindGroup.

### `ComputePass.SetImmediates`

```
export ComputePass.SetImmediates(mut self, offset: int, data: []u8)
```

Sets immediates bytes at offset. O(bytes).

### `ComputePass.SetPipeline`

```
export ComputePass.SetPipeline(mut self, p: &ComputePipeline)
```

Binds a compute pipeline. O(1).

### `ComputePipeline`

```
export linear struct ComputePipeline
```

A compiled compute pipeline.

### `ComputePipeline.Close`

```
export ComputePipeline.Close(self, dev: mut Device)
```

Gives the pipeline back. O(1).

### `Cull`

```
export open enum Cull
```

Which faces are culled.

Variants:

- `None`
- `Front`
- `Back`

### `DataLayout`

```
export struct DataLayout
```

Where texel rows sit in a buffer. bytes_per_row is a multiple of 256.

Fields:

- `offset: int`
- `bytes_per_row: int`
- `rows_per_layer: int`

### `DebugMode`

```
export open enum DebugMode
```

What the device checks beyond the portable layer's own validation,
which is always on.

Variants:

- `Off`: Portable validation only.
- `Validate`: Also the D3D12 debug layer or the Vulkan validation layers when
  present; their errors become the device's sticky error.
- `Strict`: As Validate, the layer required (Missing(DebugLayer) without it), and
  warnings outside the documented performance-only filter fail too.

### `DefaultDeviceOptions`

```
export const DefaultDeviceOptions: DeviceOptions
```

Auto backend, default power, no software, DebugMode.Off, two frames in
flight, no optional features, 65,536 descriptors.

### `DefaultRenderPipeline`

```
export const DefaultRenderPipeline: RenderPipelineDesc
```

Triangle lists, no culling, counter-clockwise front, no targets, no
depth, one sample.

### `DefaultSamplerDesc`

```
export const DefaultSamplerDesc: SamplerDesc
```

Linear filtering, clamp to edge, every mip, no anisotropy, no compare.

### `DefaultSwapchainOptions`

```
export const DefaultSwapchainOptions: SwapchainOptions
```

Bgra8UnormSrgb, Fifo, 3 buffers, no copy_src.

### `DefaultTextureDesc`

```
export const DefaultTextureDesc: TextureDesc
```

A 1 by 1 Rgba8Unorm texture, one layer, one mip, one sample, sampled
and copy_dst.

### `DefaultViewDesc`

```
export const DefaultViewDesc: ViewDesc
```

The whole texture in its own format.

### `DepthAttachment`

```
export struct DepthAttachment
```

The depth-stencil attachment of a pass. read_only: depth and stencil
are only tested, so the same view may also be bound for sampling.

Fields:

- `target: &TextureView`
- `depth_load: LoadDepth`
- `depth_store: Store`
- `stencil_load: LoadStencil`
- `stencil_store: Store`
- `read_only: bool`

### `DepthBias`

```
export struct DepthBias
```

Depth bias. clamp other than 0 needs Vulkan's depthBiasClamp (refused
as Unsupported where absent).

Fields:

- `constant: i32`
- `slope: f32`
- `clamp: f32`

### `DepthOf`

```
export DepthOf(format: TextureFormat, compare: Compare): DepthStencil
```

Depth testing with compare, writing depth, no stencil, no bias. O(1).

### `DepthStencil`

```
export struct DepthStencil
```

Depth and stencil state of a pipeline.

Fields:

- `format: TextureFormat`
- `write: bool`
- `compare: Compare`
- `stencil: ?Stencil`
- `bias: DepthBias`

### `Device`

```
export linear view struct Device
```

A device and its one queue. Linear: end it with Close. Send, not Share.

### `Device.Abandon`

```
export Device.Abandon(self, io: Io)
```

As Close, without the report: the error-path ending. Never fails.

### `Device.Acquire`

```
export Device.Acquire(mut self, io: Io, sc: mut Swapchain): Error!Acquired from sc
```

- Flows: The result borrows from sc.
- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Waits until a frame may start (frames in flight and the surface's
latency) and takes the next image. Errors: DeviceLost, SurfaceLost,
Canceled (repeatable: nothing taken). Suspends. Allocates nothing.

### `Device.Backend`

```
export Device.Backend(&self): Backend
```

The backend this device runs on. O(1).

### `Device.BufferSize`

```
export Device.BufferSize(&self, b: &Buffer): int
```

The size NewBuffer was given. O(1).

### `Device.Close`

```
export Device.Close(self, io: Io): Leaks
```

Waits for the GPU (not cancellable; after device loss the wait ends
at once), releases every object the device still owns, and the
device. O(objects); never fails.

### `Device.Encoder`

```
export Device.Encoder(mut self, label: str): Error!Encoder from self
```

- Flows: The result borrows from self.
- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Starts an encoder on a recycled command stream. Errors: DeviceLost. O(1);
allocates only while the stream pool warms up.

### `Device.Features`

```
export Device.Features(&self): Features
```

The optional features enabled at Open. O(1).

### `Device.FormatCaps`

```
export Device.FormatCaps(&self, f: TextureFormat): FormatCaps
```

What f supports on this device, as queried at Open. O(1).

### `Device.Info`

```
export Device.Info(&self): &AdapterInfo from self
```

- Flows: The result borrows from self.

The adapter this device runs on. O(1).

### `Device.Limits`

```
export Device.Limits(&self): DeviceLimits
```

The limits this device enforces. O(1).

### `Device.MapRead`

```
export Device.MapRead(mut self, io: Io, b: &Buffer, at: int, n: int): Error!Mapped from self, b
```

- Flows: The result borrows from self and b.
- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Waits until the GPU's last use of b is done and maps n bytes at at.
Errors: Invalid(BadUsage) without map_read, Invalid(RangeOutOfBounds),
DeviceLost, Canceled (repeatable). Suspends.

### `Device.MapWrite`

```
export Device.MapWrite(mut self, io: Io, b: &Buffer, at: int, n: int): Error!MappedMut from self, b
```

- Flows: The result borrows from self and b.
- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Waits until the GPU's last use of b is done and maps n bytes at at for
writing. Errors: as MapRead, with map_write.

### `Device.NewBindGroup`

```
export Device.NewBindGroup(mut self, layout: &BindGroupLayout, entries: []BindEntry, label: str): Error!BindGroup
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Makes a group of entries, one per layout entry. Errors:
Invalid(BindMismatch, at: entry) for a missing, extra or wrong-kind
entry, a missing usage, an unaligned offset or a range past the buffer.
O(entries) descriptor writes; allocates descriptor space.

### `Device.NewBindGroupLayout`

```
export Device.NewBindGroupLayout(mut self, entries: []LayoutEntry, label: str): Error!BindGroupLayout
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Makes a layout. Binding numbers are unique in the group across kinds and
below 16. Errors: Invalid(BadLayout), Invalid(LimitExceeded),
Invalid(StorageWriteInRender). O(entries log entries); allocates the
sorted entries from the device's heap.

### `Device.NewBuffer`

```
export Device.NewBuffer(mut self, size: int, usage: BufferUsage, label: str): Error!Buffer
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Makes a zeroed buffer of size bytes. Errors: Invalid(BadUsage) for a
usage set the rules forbid, Invalid(LimitExceeded), OutOfDeviceMemory,
DeviceLost. O(1) driver calls; may allocate a memory block; allocates a
slot entry from the device's heap when its table grows.

### `Device.NewComputePipeline`

```
export Device.NewComputePipeline(mut self, io: Io, layout: &PipelineLayout, cs: &Shader, label: str): Error!ComputePipeline
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Makes a compute pipeline, as NewRenderPipeline. Also checks the
workgroup size against the limits.

### `Device.NewPipelineLayout`

```
export Device.NewPipelineLayout(mut self, groups: []&BindGroupLayout, immediates: int, label: str): Error!PipelineLayout
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Makes a pipeline layout; its group layouts live while it does. Errors:
Invalid(LimitExceeded) (groups, per-stage counts, immediates). O(entries);
one root signature or VkPipelineLayout.

### `Device.NewRenderPipeline`

```
export Device.NewRenderPipeline(mut self, io: Io, layout: &PipelineLayout, vs: &Shader, fs: ?&Shader, d: RenderPipelineDesc): Error!RenderPipeline
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Makes a render pipeline; layout lives while it does. fs none: depth
only. Checks the shaders' interfaces (from SPIR-V) against layout and
d. Errors: Invalid (ShaderMismatch, BlockTooLarge, StageMismatch,
FormatCapability, ...), OutOfDeviceMemory, DeviceLost, Canceled (only
while queued). The driver compiles through runtime.Blocking.

### `Device.NewSampler`

```
export Device.NewSampler(mut self, d: SamplerDesc): Error!Sampler
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Makes a sampler. Errors: Invalid(BadUsage) (anisotropy rules),
Invalid(LimitExceeded) (the 2,048 sampler descriptors). O(1).

### `Device.NewShader`

```
export Device.NewShader(mut self, code: ShaderCode, stage: Stage, entry: str, label: str): Error!Shader
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Checks code (the SPIR-V reader walks the whole module, bounded) and keeps
the blob of this device's backend. Errors: Invalid(MissingSpirv),
Unsupported (no DXIL on D3D12), Invalid(BadShader),
Invalid(UnsignedDxil), Invalid(StageMismatch). O(blob words); Vulkan
makes a VkShaderModule, D3D12 copies the DXIL into the device's heap.

### `Device.NewSwapchain`

```
export Device.NewSwapchain(mut self, io: Io, w: &window.Window, o: SwapchainOptions): Error!Swapchain
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Makes a swapchain on w's client area at its current size. Errors:
Unsupported (format), SurfaceLost (the window is closed), DeviceLost.
Runs on the system stack. Counts as a user of w's native window.

### `Device.NewTexture`

```
export Device.NewTexture(mut self, d: TextureDesc): Error!Texture
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Makes a zeroed texture by d. Errors: Invalid(FormatCapability) for a
usage the format lacks on this device, Invalid(LimitExceeded),
OutOfDeviceMemory, DeviceLost. O(1) driver calls.

### `Device.NewView`

```
export Device.NewView(mut self, t: &Texture, d: ViewDesc): Error!TextureView
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Makes a view of t. Errors: Invalid(BadView), Invalid(ViewFormatNotListed),
StaleHandle. O(1); writes descriptors.

### `Device.Present`

```
export Device.Present(mut self, io: Io, t: SurfaceTexture): Error!Presented
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Presents t, after the last submission that rendered to it. Errors:
DeviceLost, SurfaceLost; on an error t is still the caller's. Not a
cancellation point. O(1) driver calls on the system stack.

### `Device.ReadTexture`

```
export Device.ReadTexture(mut self, a: &Arena, io: Io, src: TextureRegion, size: Extent): Error![]u8 from a
```

- Flows: The result borrows from a.
- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Reads a texture region into a, tight rows: a staging buffer, one
submission and a wait. For tests and screenshots. Errors: Invalid,
DeviceLost, Canceled (repeatable). O(bytes); allocates the result in a.

### `Device.Resize`

```
export Device.Resize(mut self, io: Io, sc: mut Swapchain, width: int, height: int): Error!void
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Recreates the images at width by height once the GPU and presentation
engine are done with them. Errors: DeviceLost, SurfaceLost, Canceled
(repeatable). Suspends.

### `Device.SwapchainInfo`

```
export Device.SwapchainInfo(&self, sc: &Swapchain): SwapchainInfo
```

What sc was granted. O(1).

### `Device.TakeMessages`

```
export Device.TakeMessages(mut self, a: &Arena): OutOfMemory!List[Message] from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

Takes the messages recorded so far (at most 256 kept; Leaks counts the
rest). O(messages); allocates their text in a.

### `Device.WaitIdle`

```
export Device.WaitIdle(mut self, io: Io): Error!void
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Waits until the GPU has finished every submission. Errors: DeviceLost,
Canceled (repeatable). Suspends; holds no thread.

### `Device.WriteBuffer`

```
export Device.WriteBuffer(mut self, b: &Buffer, at: int, data: []u8): Error!void
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Copies data into the upload ring; the copy runs before the next
submission's commands. Errors: Invalid (range, usage), OutOfDeviceMemory.
O(bytes).

### `Device.WriteTexture`

```
export Device.WriteTexture(mut self, dst: TextureRegion, data: []u8, size: Extent): Error!void
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Copies tight rows of data into a texture region before the next
submission's commands. O(bytes): repacks rows to 256-byte pitch.

### `DeviceLimits`

```
export struct DeviceLimits
```

Limits the portable layer enforces: the smaller of the adapter's and
the portable maxima. Plain.

Fields:

- `max_texture_size: int`
- `max_texture_layers: int`
- `max_bind_groups: int`
- `max_bindings_per_group: int`
- `max_dynamic_uniforms: int`
- `max_immediates: int`
- `max_vertex_buffers: int`
- `max_vertex_attributes: int`
- `max_vertex_stride: int`
- `max_color_targets: int`
- `uniform_offset_align: int`
- `storage_offset_align: int`
- `max_uniform_binding: int`
- `max_storage_binding: int`
- `max_buffer_size: int`
- `max_uniforms_per_stage: int`
- `max_storage_per_stage: int`
- `max_textures_per_stage: int`
- `max_samplers_per_stage: int`
- `max_sampler_descriptors: int`
- `max_workgroup_size_x: int`
- `max_workgroup_size_y: int`
- `max_workgroup_size_z: int`
- `max_workgroup_invocations: int`
- `max_workgroups_per_dimension: int`
- `max_workgroup_storage: int`

### `DeviceLost`

```
export error DeviceLost(reason: LossReason)
```

The device stopped: removed, reset, hung, or a driver fault. Every later
fallible call of the device returns it; every Close still releases.

### `DeviceOptions`

```
export struct DeviceOptions
```

How Open chooses and makes a device. Start from DefaultDeviceOptions
and assign the fields you change.

Fields:

- `backend: Backend`
- `power: Power`
- `software: bool`: Only a software adapter qualifies.
- `adapter: ?AdapterId`: some: exactly this adapter (from Adapters); NoAdapter when it is gone.
- `debug: DebugMode`
- `frames_in_flight: int`: Frames the CPU may record ahead of the GPU before Acquire waits: 1 to 3.
- `features: Features`: Optional features; Open fails with Missing when the adapter lacks one.
- `descriptors: int`: Shader-visible CBV/SRV/UAV descriptors the device reserves (D3D12)
  or sizes its pools for (Vulkan).

### `Driver`

```
export error Driver(code: i64, call: Call)
```

A driver result no other member covers: the HRESULT or VkResult, and the call.

### `Encoder`

```
export linear view struct Encoder
```

Records commands for one submission. Linear: Submit or Abandon.

### `Encoder.Abandon`

```
export Encoder.Abandon(self)
```

Drops what was recorded and recycles the stream. Never fails. O(1).

### `Encoder.ClearBuffer`

```
export Encoder.ClearBuffer(mut self, b: &Buffer, at: int, n: int)
```

Fills n bytes with zeros. O(1).

### `Encoder.ComputePass`

```
export Encoder.ComputePass(mut self, label: str): ComputePass from self
```

- Flows: The result borrows from self.

Begins a compute pass. O(1).

### `Encoder.CopyBuffer`

```
export Encoder.CopyBuffer(mut self, src: &Buffer, src_at: int, dst: &Buffer, dst_at: int, n: int)
```

Copies n bytes. A misuse is recorded and returned by Submit. O(1).

### `Encoder.CopyBufferToTexture`

```
export Encoder.CopyBufferToTexture(mut self, src: &Buffer, layout: DataLayout, dst: TextureRegion, size: Extent)
```

Copies buffer rows into a texture region. O(1).

### `Encoder.CopyTexture`

```
export Encoder.CopyTexture(mut self, src: TextureRegion, dst: TextureRegion, size: Extent)
```

Copies between textures of one format. O(1).

### `Encoder.CopyTextureToBuffer`

```
export Encoder.CopyTextureToBuffer(mut self, src: TextureRegion, dst: &Buffer, layout: DataLayout, size: Extent)
```

Copies a texture region into buffer rows. O(1).

### `Encoder.PopDebugGroup`

```
export Encoder.PopDebugGroup(mut self)
```

Closes the innermost debug group. O(1).

### `Encoder.PushDebugGroup`

```
export Encoder.PushDebugGroup(mut self, label: str)
```

Opens a debug group (a PIX event, a debug-utils label). O(label).

### `Encoder.RenderPass`

```
export Encoder.RenderPass(mut self, d: &PassDesc): RenderPass from self
```

- Flows: The result borrows from self.

Begins a render pass; viewport and scissor are the attachments' extent,
blend constant 0, stencil reference 0. Misuse (ExtentMismatch, stale
views) is recorded and returned by Submit. O(attachments). Allocates
nothing.

### `Encoder.Submit`

```
export Encoder.Submit(self): Error!void
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Hands the stream to the driver: replays it into a native command list
on the system stack and executes it after every Device write made since
the last submission. Fails only before it changes anything: with the
first misuse recorded (Invalid(misuse, at: command index)), StaleHandle,
or DeviceLost; the encoder is then still the caller's. Driver errors
found after execution become the device's sticky error. O(commands);
never waits; allocates nothing after warm-up.

### `Encoder.WriteBuffer`

```
export Encoder.WriteBuffer(mut self, dst: &Buffer, at: int, data: []u8): Error!void
```

- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Copies data into the upload ring now, and records its copy at this
point of the stream. Errors: OutOfDeviceMemory. O(bytes).

### `Error`

```
export open type Error = Unsupported | NoAdapter | Missing | DeviceLost | OutOfDeviceMemory | OutOfMemory | Invalid | SurfaceLost | Driver | Canceled
```

Every error std.gpu returns.

### `Extent`

```
export struct Extent
```

A copy's size in texels and layers.

Fields:

- `width: int`
- `height: int`
- `layers: int`

### `Feature`

```
export open enum Feature
```

Optional capabilities DeviceOptions.features can ask for.

Variants:

- `TextureCompressionBc`
- `DepthClipControl`
- `Float32Filterable`
- `SamplerAnisotropy`
- `DebugLayer`: The D3D12 debug layer or the Vulkan validation layers, for DebugMode.Strict.

### `Features`

```
export struct Features
```

Optional capabilities, each false unless asked for and present.
Start from NoFeatures and set fields.

Fields:

- `texture_compression_bc: bool`
- `depth_clip_control: bool`
- `float32_filterable: bool`
- `sampler_anisotropy: bool`

### `Filter`

```
export open enum Filter
```

Texel filtering.

Variants:

- `Nearest`
- `Linear`

### `FormatCaps`

```
export struct FormatCaps
```

What one format supports on one device, read from the driver at Open.

Fields:

- `sampled: bool`
- `filterable: bool`
- `render_target: bool`
- `blendable: bool`
- `multisample: bool`
- `resolve: bool`
- `depth_stencil: bool`

### `FrontFace`

```
export open enum FrontFace
```

Which winding is the front, in framebuffer space with Y down.

Variants:

- `Ccw`
- `Cw`

### `IndexFormat`

```
export open enum IndexFormat
```

The index formats.

Variants:

- `Uint16`
- `Uint32`

### `Invalid`

```
export error Invalid(misuse: Misuse, at: int)
```

A use the rules forbid, found before anything reached the driver. at is
the entry index or command index the misuse was found at, or -1.

### `LayoutEntry`

```
export struct LayoutEntry
```

One binding of a layout: its number (the HLSL register number), its
stages and its kind.

Fields:

- `binding: int`
- `visibility: Visibility`
- `kind: BindingKind`

### `Leaks`

```
export struct Leaks
```

What a device still held at Close, and the debug messages it saw.

Fields:

- `native: int`: Native objects the backend still counted after releasing.
- `errors: int`
- `warnings: int`

### `Load`

```
export enum Load
```

What a colour attachment holds when the pass begins. Clear values are
f64 so every R32Uint and R32Sint value clears exactly.

Variants:

- `Clear(r: f64, g: f64, b: f64, a: f64)`
- `Keep`
- `DontCare`

### `LoadDepth`

```
export enum LoadDepth
```

What a depth aspect holds when the pass begins.

Variants:

- `Clear(depth: f32)`
- `Keep`
- `DontCare`

### `LoadStencil`

```
export enum LoadStencil
```

What a stencil aspect holds when the pass begins.

Variants:

- `Clear(value: u32)`
- `Keep`
- `DontCare`

### `LossReason`

```
export open enum LossReason
```

Why a device was lost, as far as the driver says.

Variants:

- `Hung`
- `Reset`
- `Removed`
- `DriverInternal`
- `OutOfMemory`
- `Unknown`

### `Mapped`

```
export linear view struct Mapped
```

A readable mapped range. Linear: Unmap it.

### `Mapped.Bytes`

```
export Mapped.Bytes(&self): []u8 from self
```

- Flows: The result borrows from self.

The mapped bytes, valid until Unmap. O(1).

### `Mapped.Unmap`

```
export Mapped.Unmap(self)
```

Ends the mapping. O(1).

### `MappedMut`

```
export linear view struct MappedMut
```

A writable mapped range. Linear: Unmap it.

### `MappedMut.Bytes`

```
export MappedMut.Bytes(mut self): mut []u8 from self
```

- Flows: The result borrows from self.

The mapped bytes, writable until Unmap. O(1).

### `MappedMut.Unmap`

```
export MappedMut.Unmap(self)
```

Ends the mapping; the writes are visible to the next submission. O(1).

### `Message`

```
export struct Message
```

One debug-layer or messenger message.

Fields:

- `severity: Severity`
- `id: i64`
- `text: str`

### `Missing`

```
export error Missing(feature: Feature)
```

An optional feature was asked for and the adapter lacks it.

### `Misuse`

```
export open enum Misuse
```

The rule a misuse broke. Each value's Format line names the rule.

Variants:

- `StaleHandle`
- `NoPipeline`
- `PipelineKind`
- `GroupMissing`
- `GroupIncompatible`
- `GroupIndex`
- `DynamicOffsetCount`
- `DynamicOffsetAlignment`
- `VertexBufferMissing`
- `IndexBufferMissing`
- `RangeOutOfBounds`
- `BadUsage`
- `AlignmentOffset`
- `AlignmentRowPitch`
- `FormatMismatch`
- `FormatCapability`
- `SamplesMismatch`
- `ExtentMismatch`
- `AttachmentAlsoBound`
- `WriteReadConflict`
- `ShaderMismatch`
- `StageMismatch`
- `BlockTooLarge`
- `UnsignedDxil`
- `MissingSpirv`
- `BadShader`
- `BadLayout`
- `BadView`
- `BadCopyAspect`
- `BindMismatch`
- `LimitExceeded`
- `ImmediatesRange`
- `DebugGroupUnbalanced`
- `ViewFormatNotListed`
- `StorageWriteInRender`
- `TooManyCommands`
- `DebugLayer`

### `NoAdapter`

```
export error NoAdapter
```

No adapter qualifies for the options given.

### `NoBufferUsage`

```
export const NoBufferUsage: BufferUsage
```

No usage at all.

### `NoFeatures`

```
export const NoFeatures: Features
```

No optional feature.

### `NoTextureUsage`

```
export const NoTextureUsage: TextureUsage
```

No usage at all.

### `Open`

```
export Open(heap: &Heap, io: Io, o: DeviceOptions): Error!Device from heap
```

- Flows: The result borrows from heap.
- Errors: Error (Canceled, OutOfMemory, DeviceLost(reason: LossReason), Driver(code: i64, call: Call), Invalid(misuse: Misuse, at: int), Missing(feature: Feature), NoAdapter, OutOfDeviceMemory, SurfaceLost, Unsupported)

Makes a device by o. Errors: Unsupported (target), NoAdapter,
Missing(feature), DeviceLost, OutOfDeviceMemory, OutOfMemory, Canceled
(only while queued). Runs creation and the format queries through
runtime.Blocking. Allocates the device's tables from heap.

### `OutOfDeviceMemory`

```
export error OutOfDeviceMemory
```

The device's memory is exhausted.

### `PassDesc`

```
export struct PassDesc
```

The attachments of a render pass; all share one extent.

Fields:

- `colors: []ColorAttachment`
- `depth: ?DepthAttachment`
- `label: str`

### `PipelineLayout`

```
export linear struct PipelineLayout
```

The layout of up to four groups (group i is HLSL space i) and of
immediates bytes (a multiple of 4, at most 128) at b0, space8.

### `PipelineLayout.Close`

```
export PipelineLayout.Close(self, dev: mut Device)
```

Gives the pipeline layout back. O(1).

### `Power`

```
export open enum Power
```

Which adapter Open prefers when several qualify.

Variants:

- `Default`
- `HighPerformance`
- `LowPower`

### `PresentMode`

```
export open enum PresentMode
```

When a presented frame reaches the screen.

Variants:

- `Fifo`: At vertical blank; never tears; always available.
- `Mailbox`: The newest frame at vertical blank; never tears; Fifo where absent.
- `Immediate`: At once; may tear; Mailbox, then Fifo, where absent.

### `Presented`

```
export struct Presented
```

What a present reported.

Fields:

- `suboptimal: bool`: The surface would present better recreated: Resize when convenient.

### `RenderPass`

```
export linear view struct RenderPass
```

A render pass being recorded. Linear: End it.

### `RenderPass.Draw`

```
export RenderPass.Draw(mut self, vertices: int, instances: int, first_vertex: int, first_instance: int)
```

Draws vertices. SV_VertexID excludes first_vertex and SV_InstanceID
excludes first_instance on both backends, as on D3D12. O(1); see the per-draw budget.

### `RenderPass.DrawIndexed`

```
export RenderPass.DrawIndexed(mut self, indices: int, instances: int, first_index: int, base_vertex: int, first_instance: int)
```

Draws indexed vertices; first_index + indices must fit the index buffer.

### `RenderPass.End`

```
export RenderPass.End(self)
```

Ends the pass. O(1).

### `RenderPass.PopDebugGroup`

```
export RenderPass.PopDebugGroup(mut self)
```

Closes the innermost debug group. O(1).

### `RenderPass.PushDebugGroup`

```
export RenderPass.PushDebugGroup(mut self, label: str)
```

Opens a debug group. O(label).

### `RenderPass.SetBindGroup`

```
export RenderPass.SetBindGroup(mut self, index: int, g: &BindGroup, dynamic: []u32)
```

Binds group g at index; dynamic holds one offset per dynamic entry, in
binding order. O(dynamic).

### `RenderPass.SetBlendConstant`

```
export RenderPass.SetBlendConstant(mut self, r: f32, g: f32, b: f32, a: f32)
```

Sets the blend constant. O(1).

### `RenderPass.SetImmediates`

```
export RenderPass.SetImmediates(mut self, offset: int, data: []u8)
```

Sets immediates bytes at offset (multiples of 4). O(bytes).

### `RenderPass.SetIndexBuffer`

```
export RenderPass.SetIndexBuffer(mut self, b: &Buffer, format: IndexFormat, offset: int)
```

Binds the index buffer. O(1).

### `RenderPass.SetPipeline`

```
export RenderPass.SetPipeline(mut self, p: &RenderPipeline)
```

Binds a pipeline; bindings and immediates of an incompatible layout
become unbound. O(1).

### `RenderPass.SetScissor`

```
export RenderPass.SetScissor(mut self, x: int, y: int, w: int, h: int)
```

Sets the scissor rectangle, clamped to the attachments. O(1).

### `RenderPass.SetStencilReference`

```
export RenderPass.SetStencilReference(mut self, value: u32)
```

Sets the stencil reference. O(1).

### `RenderPass.SetVertexBuffer`

```
export RenderPass.SetVertexBuffer(mut self, slot: int, b: &Buffer, offset: int)
```

Binds a vertex buffer slot from offset to the buffer's end. O(1).

### `RenderPass.SetViewport`

```
export RenderPass.SetViewport(mut self, x: f32, y: f32, w: f32, h: f32, min_depth: f32, max_depth: f32)
```

Sets the viewport, in framebuffer pixels with Y down. O(1).

### `RenderPipeline`

```
export linear struct RenderPipeline
```

A compiled render pipeline.

### `RenderPipeline.Close`

```
export RenderPipeline.Close(self, dev: mut Device)
```

Gives the pipeline back. O(1).

### `RenderPipelineDesc`

```
export struct RenderPipelineDesc
```

Everything a render pipeline fixes besides its shaders and layout.
Start from DefaultRenderPipeline and assign the fields you change.

Fields:

- `vertex_layouts: []VertexLayout`
- `topology: Topology`
- `strip_index_format: ?IndexFormat`: For strip topologies with indexed draws: the index format whose
  all-ones value restarts the strip.
- `cull: Cull`
- `front: FrontFace`
- `targets: []ColorTarget`
- `depth: ?DepthStencil`
- `samples: int`
- `alpha_to_coverage: bool`
- `label: str`

### `SampleType`

```
export open enum SampleType
```

What a sampled texture binding returns.

Variants:

- `Float`
- `UnfilterableFloat`
- `Depth`
- `Sint`
- `Uint`

### `Sampler`

```
export linear struct Sampler
```

A sampler its device owns.

### `Sampler.Close`

```
export Sampler.Close(self, dev: mut Device)
```

Gives the sampler back. O(1).

### `SamplerDesc`

```
export struct SamplerDesc
```

How a sampler filters and addresses. Start from DefaultSamplerDesc.

Fields:

- `min: Filter`
- `mag: Filter`
- `mip: Filter`
- `address_u: Address`
- `address_v: Address`
- `address_w: Address`
- `lod_min: f32`
- `lod_max: f32`
- `anisotropy: int`: 1 to 16; above 1 needs every filter Linear and sampler_anisotropy.
- `compare: ?Compare`: some: a comparison sampler for shadow maps.

### `Severity`

```
export open enum Severity
```

How bad a Message is.

Variants:

- `Info`
- `Warning`
- `Error`

### `Shader`

```
export linear struct Shader
```

A checked shader module.

### `Shader.Close`

```
export Shader.Close(self, dev: mut Device)
```

Gives the shader back. O(1).

### `ShaderCode`

```
export struct ShaderCode
```

One logical shader. spirv is always required: it is the interface the
library checks for both backends. dxil is required on D3D12 and must
come from the same source and compiler run.

Fields:

- `spirv: []u8`
- `dxil: []u8`

### `Stage`

```
export open enum Stage
```

A shader stage.

Variants:

- `Vertex`
- `Fragment`
- `Compute`

### `Stencil`

```
export struct Stencil
```

Stencil state: two faces, one read mask and one write mask for both
(D3D12 has one pair).

Fields:

- `front: StencilFace`
- `back: StencilFace`
- `read_mask: u8`
- `write_mask: u8`

### `StencilFace`

```
export struct StencilFace
```

Stencil state of one face.

Fields:

- `compare: Compare`
- `on_fail: StencilOp`
- `on_depth_fail: StencilOp`
- `on_pass: StencilOp`

### `StencilOp`

```
export open enum StencilOp
```

A stencil operation.

Variants:

- `Keep`
- `Zero`
- `Replace`
- `IncrementClamp`
- `DecrementClamp`
- `Invert`
- `IncrementWrap`
- `DecrementWrap`

### `StepMode`

```
export open enum StepMode
```

How a vertex buffer steps.

Variants:

- `Vertex`
- `Instance`

### `Store`

```
export enum Store
```

What an attachment keeps when the pass ends.

Variants:

- `Store`
- `Discard`

### `SurfaceLost`

```
export error SurfaceLost
```

The window under a swapchain is gone.

### `SurfaceTexture`

```
export linear view struct SurfaceTexture
```

One image of a swapchain to render into and present. Holds the swapchain
exclusively. Linear: Present it, or Abandon it.

### `SurfaceTexture.Abandon`

```
export SurfaceTexture.Abandon(self)
```

Gives the image back unpresented. The next Acquire answers Outdated and
Resize recreates the swapchain. Never fails; needs no device. O(1).

### `SurfaceTexture.View`

```
export SurfaceTexture.View(&self): &TextureView from self
```

- Flows: The result borrows from self.

The image's render-target view, valid while t lives. O(1).

### `Swapchain`

```
export linear struct Swapchain
```

The images a window shows. Linear: end it with Close.

### `Swapchain.Close`

```
export Swapchain.Close(self, io: Io, dev: mut Device)
```

Waits until the GPU and the presentation engine are done with the
images, then releases them. Not cancellable; never fails.

### `SwapchainInfo`

```
export struct SwapchainInfo
```

What a swapchain was granted.

Fields:

- `format: TextureFormat`
- `mode: PresentMode`
- `width: int`
- `height: int`
- `images: int`: Images the driver made; may exceed SwapchainOptions.buffers.

### `SwapchainOptions`

```
export struct SwapchainOptions
```

How NewSwapchain configures the surface. format is one of Bgra8Unorm,
Bgra8UnormSrgb, Rgba8Unorm, Rgba8UnormSrgb. Start from
DefaultSwapchainOptions.

Fields:

- `format: TextureFormat`
- `mode: PresentMode`
- `buffers: int`: Back buffers asked for: 2 or 3.
- `copy_src: bool`: Also copy_src, where the surface allows it (screenshots).

### `Texture`

```
export linear struct Texture
```

A texture its device owns. Linear: end it with Close.

### `Texture.Close`

```
export Texture.Close(self, dev: mut Device)
```

Gives the texture back, as Buffer.Close. O(1).

### `TextureDesc`

```
export struct TextureDesc
```

A two-dimensional texture, an array of them, or a cube. Start from
DefaultTextureDesc.

Fields:

- `format: TextureFormat`
- `width: int`
- `height: int`
- `layers: int`: Layers: 1, or 6 and more for a cube (width == height).
- `mips: int`
- `samples: int`: 1 or 4.
- `usage: TextureUsage`
- `view_formats: []TextureFormat`: Other formats views of it may take (sRGB or linear twins only).
- `label: str`

### `TextureFormat`

```
export open enum TextureFormat
```

The texel formats. Depth24PlusStencil8 is D24S8 where the device has it
and D32S8 where it does not; its depth aspect cannot be copied.

Variants:

- `R8Unorm`
- `R8Snorm`
- `R8Uint`
- `R8Sint`
- `Rg8Unorm`
- `Rg8Snorm`
- `Rg8Uint`
- `Rg8Sint`
- `Rgba8Unorm`
- `Rgba8UnormSrgb`
- `Rgba8Snorm`
- `Rgba8Uint`
- `Rgba8Sint`
- `Bgra8Unorm`
- `Bgra8UnormSrgb`
- `Rgb10a2Unorm`
- `Rg11b10Ufloat`
- `R16Float`
- `Rg16Float`
- `Rgba16Float`
- `R16Uint`
- `R16Sint`
- `Rgba16Uint`
- `Rgba16Sint`
- `R32Float`
- `Rg32Float`
- `Rgba32Float`
- `R32Uint`
- `R32Sint`
- `Rg32Uint`
- `Rgba32Uint`
- `Depth16Unorm`
- `Depth32Float`
- `Depth24PlusStencil8`
- `Depth32FloatStencil8`
- `Bc1Unorm`
- `Bc1UnormSrgb`
- `Bc3Unorm`
- `Bc3UnormSrgb`
- `Bc4Unorm`
- `Bc5Unorm`
- `Bc6hUfloat`
- `Bc7Unorm`
- `Bc7UnormSrgb`

### `TextureRegion`

```
export struct TextureRegion
```

One mip of a range of layers of a texture, from an origin.

Fields:

- `texture: &Texture`
- `mip: int`
- `layer: int`
- `x: int`
- `y: int`
- `aspect: Aspect`

### `TextureUsage`

```
export struct TextureUsage
```

What a texture may be used for. Start from NoTextureUsage.

Fields:

- `copy_src: bool`
- `copy_dst: bool`
- `sampled: bool`
- `render_target: bool`

### `TextureView`

```
export linear struct TextureView
```

A view of a texture. The texture lives while the view does.

### `TextureView.Close`

```
export TextureView.Close(self, dev: mut Device)
```

Gives the view back. O(1).

### `Topology`

```
export open enum Topology
```

Primitive topology.

Variants:

- `PointList`
- `LineList`
- `LineStrip`
- `TriangleList`
- `TriangleStrip`

### `Unsupported`

```
export error Unsupported
```

This target, backend, adapter or format cannot do it.

### `VertexAttribute`

```
export struct VertexAttribute
```

One vertex attribute: HLSL semantic LOC<location>, [[vk::location]].

Fields:

- `location: int`
- `format: VertexFormat`
- `offset: int`

### `VertexFormat`

```
export open enum VertexFormat
```

The vertex attribute formats: WebGPU's set, which has no 3-component 8-
or 16-bit formats (D3D12 has none).

Variants:

- `Uint8x2`
- `Uint8x4`
- `Sint8x2`
- `Sint8x4`
- `Unorm8x2`
- `Unorm8x4`
- `Snorm8x2`
- `Snorm8x4`
- `Uint16x2`
- `Uint16x4`
- `Sint16x2`
- `Sint16x4`
- `Unorm16x2`
- `Unorm16x4`
- `Snorm16x2`
- `Snorm16x4`
- `Float16x2`
- `Float16x4`
- `Float32`
- `Float32x2`
- `Float32x3`
- `Float32x4`
- `Uint32`
- `Uint32x2`
- `Uint32x3`
- `Uint32x4`
- `Sint32`
- `Sint32x2`
- `Sint32x3`
- `Sint32x4`

### `VertexLayout`

```
export struct VertexLayout
```

The layout of one vertex buffer slot.

Fields:

- `stride: int`
- `step: StepMode`
- `attributes: []VertexAttribute`

### `ViewDesc`

```
export struct ViewDesc
```

A view's format, dimension and subresource range. Start from
DefaultViewDesc.

Fields:

- `format: ?TextureFormat`: none: the texture's format; else one of its view_formats.
- `dimension: ?ViewDimension`: none: D2 for one layer, D2Array for several.
- `aspect: Aspect`
- `first_mip: int`
- `mips: ?int`: none: every mip from first_mip.
- `first_layer: int`
- `layers: ?int`: none: every layer from first_layer.

### `ViewDimension`

```
export open enum ViewDimension
```

How a view sees its texture.

Variants:

- `D2`
- `D2Array`
- `Cube`
- `CubeArray`

### `Visibility`

```
export struct Visibility
```

The stages a binding is visible to.

Fields:

- `vertex: bool`
- `fragment: bool`
- `compute: bool`

### `WriteAll`

```
export const WriteAll: ColorWrites = ColorWrites(r: true, g: true, b: true, a: true)
```

Every channel.

<!-- api:end -->
