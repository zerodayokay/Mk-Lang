# scene

A 3D renderer over a small text scene format: OBJ meshes, PNG textures, a shadow map, 4× MSAA, 4,096 instances and a compute blur, rendered offscreen to a PNG on Direct3D 12 or Vulkan, or shown in a window with an orbit camera. It shows `std.gpu` as a program outside the library uses it: linear device objects closed in order, a frame recorded into one `Encoder`, and images compared with `image.Compare` across backends.

## Running it

From the program's folder, on Windows:

```
cd Dogfoods/scene
mk run -- render cases/scenes/shadow.scene shadow.png --warp
```

```text
rendered 128x128 frames=1 msaa=1 shadows=true blur=false instances=0
```

```
scene check SCENE                   parse SCENE, load its OBJ and PNG files, check the shaders
scene check-shaders                 check the embedded shaders against their recorded hashes
scene probe [--backend B] [--list]  check the shaders, then report the GPU adapters
scene render SCENE OUT [options]    render offscreen and write OUT as a PNG
scene view SCENE [options]          open a window with an orbit camera
```

| Option | Effect |
|---|---|
| `--backend auto\|d3d12\|vulkan` | The backend; `auto` is Direct3D 12 first on Windows |
| `--warp` | Use the software adapter (WARP) |
| `--validate` | Turn the debug layer on where it is installed (`gpu.DebugMode.Validate`) |
| `--strict` | Require the debug layer and fail on its errors (`render` only) |
| `--frames N` | Render N frames; `view` closes after N presented frames |
| `--compare FILE.png` | Compare the image with FILE.png (`render`) |
| `--compare-warp` | Render again on WARP and compare (`render`) |
| `--tol N` | Channel tolerance for both comparisons, default 2 |
| `--mode fifo\|mailbox\|immediate` | The swapchain's present mode (`view`) |

A comparison passes when no pixel over the tolerance lacks a neighbour within it, and at most 0.1 % of pixels are over it (0.5 % for an MSAA scene, whose sample positions differ between adapters). In `view`, drag to orbit, use the wheel to zoom; M toggles MSAA, F toggles borderless fullscreen and Esc closes.

Exit codes: 0 done; 1 a scene, file or GPU failure, or a failed comparison; 2 a usage error. Off Windows, `probe` reports `adapters=unsupported` and the GPU commands fail with `unsupported on this target`.

### Scene files

One directive per line; `#` starts a comment. Paths are relative to the scene file.

| Directive | Meaning |
|---|---|
| `size W H` | Image size, 16 to 4096 each (default 256 256) |
| `clear R G B A` | Clear colour |
| `camera EX EY EZ TX TY TZ FOV` | Eye, target, vertical field of view in degrees |
| `light DX DY DZ R G B` | Directional light and its colour |
| `msaa 1\|4`, `shadows on\|off`, `blur on\|off`, `overlay on\|off` | Features; the blur applies to `render` only |
| `instances N` | Draw the first mesh N times from a storage buffer, 0 to 16384 |
| `texture NAME PATH`, `mesh NAME PATH` | A PNG texture, an OBJ mesh |
| `object NAME MESH TEXTURE X Y Z ROT SCALE R G B` | Place a mesh; the first 64 are drawn |

## Shaders

The shaders are HLSL in `shaders/`. `tools/shaders.ps1` compiles each to both SPIR-V and DXIL, and both blobs are committed in `shaders/bin`. `src/shaders/manifest.mk` embeds them with `build.File` and records the SHA-256 of each source (line endings normalised) and each blob; `scene check-shaders` checks the hashes and runs `gpu.CheckShader` on every pair, on any target and without DXC. After editing a shader, regenerate both with the DXC release the manifest names:

```
pwsh -NoProfile -File tools/shaders.ps1 -dxc <path to dxc.exe> -mk <path to mk>
```

## Design

- `src/scenedoc` parses the scene and loads its assets into an arena; `src/obj` turns an OBJ file's faces into triangle fans with one vertex per face corner, and makes face normals where the file has none; `src/math3d` writes row-major matrices, matching the shaders' `-Zpr` layout.
- `src/renderer` owns every device object in one linear `SceneRenderer`. Creation registers an `onerr` close for each object in turn, so a failure part way releases what exists; `Close` releases them in reverse dependency order and returns the device's `Leaks`, which `render` and `view` check are zero.
- Recording passes the renderer's fields to `RecordFramePasses` one by one rather than `self`, so the encoder's loan of the device and the loans of the views and pipelines come from different fields.
- Each frame: a depth-only shadow pass into a 512 by 512 map; the main pass, into the 4× target with a resolve when MSAA is on, drawing each object with its 256-byte slice of one dynamic uniform buffer and the instances from a storage buffer; a blended overlay; then, for `blur`, a copy into a storage buffer, two compute dispatches (horizontal, then vertical) and a copy back. Mip levels are made once, by one render pass per level.
- Immediates carry the per-pass flags, so no uniform buffer is written between draws.

## Limits

- The blur runs on offscreen renders only; `view` draws the scene without it.
- `render-cube-vulkan` needs a Vulkan driver, and `--strict` on Vulkan needs the validation layers of the Vulkan SDK.

## Section 18 lines

The standard-library lines this program uses, as `doc/reference.md` lists them; `go run ./tools/dogfood cover` reads this block.

```
// std.gpu
gpu.Adapters(a: &Arena, io: Io, backend: Backend): Error!List[AdapterInfo] from a
gpu.Open(heap: &Heap, io: Io, o: DeviceOptions): Error!Device from heap
gpu.CheckShader(code: ShaderCode, stage: Stage, entry: str): Error!void
gpu.ColorOf(format: TextureFormat): ColorTarget
gpu.DepthOf(format: TextureFormat, compare: Compare): DepthStencil
Device.Abandon(self, io: Io)
Device.Acquire(mut self, io: Io, sc: mut Swapchain): Error!Acquired from sc
Device.Backend(&self): Backend
Device.Close(self, io: Io): Leaks
Device.Encoder(mut self, label: str): Error!Encoder from self
Device.NewBindGroup(mut self, layout: &BindGroupLayout, entries: []BindEntry, label: str): Error!BindGroup
Device.NewBindGroupLayout(mut self, entries: []LayoutEntry, label: str): Error!BindGroupLayout
Device.NewBuffer(mut self, size: int, usage: BufferUsage, label: str): Error!Buffer
Device.NewComputePipeline(mut self, io: Io, layout: &PipelineLayout, cs: &Shader, label: str): Error!ComputePipeline
Device.NewPipelineLayout(mut self, groups: []&BindGroupLayout, immediates: int, label: str): Error!PipelineLayout
Device.NewRenderPipeline(mut self, io: Io, layout: &PipelineLayout, vs: &Shader, fs: ?&Shader, d: RenderPipelineDesc): Error!RenderPipeline
Device.NewSampler(mut self, d: SamplerDesc): Error!Sampler
Device.NewShader(mut self, code: ShaderCode, stage: Stage, entry: str, label: str): Error!Shader
Device.NewSwapchain(mut self, io: Io, w: &window.Window, o: SwapchainOptions): Error!Swapchain
Device.NewTexture(mut self, d: TextureDesc): Error!Texture
Device.NewView(mut self, t: &Texture, d: ViewDesc): Error!TextureView
Device.Present(mut self, io: Io, t: SurfaceTexture): Error!Presented
Device.ReadTexture(mut self, a: &Arena, io: Io, src: TextureRegion, size: Extent): Error![]u8 from a
Device.Resize(mut self, io: Io, sc: mut Swapchain, width: int, height: int): Error!void
Device.SwapchainInfo(&self, sc: &Swapchain): SwapchainInfo
Device.TakeMessages(mut self, a: &Arena): OutOfMemory!List[Message] from a
Device.WriteBuffer(mut self, b: &Buffer, at: int, data: []u8): Error!void
Device.WriteTexture(mut self, dst: TextureRegion, data: []u8, size: Extent): Error!void
Encoder.Abandon(self)
Encoder.ComputePass(mut self, label: str): ComputePass from self
Encoder.CopyBufferToTexture(mut self, src: &Buffer, layout: DataLayout, dst: TextureRegion, size: Extent)
Encoder.CopyTextureToBuffer(mut self, src: TextureRegion, dst: &Buffer, layout: DataLayout, size: Extent)
Encoder.PopDebugGroup(mut self)
Encoder.PushDebugGroup(mut self, label: str)
Encoder.RenderPass(mut self, d: &PassDesc): RenderPass from self
Encoder.Submit(self): Error!void
RenderPass.Draw(mut self, vertices: int, instances: int, first_vertex: int, first_instance: int)
RenderPass.DrawIndexed(mut self, indices: int, instances: int, first_index: int, base_vertex: int, first_instance: int)
RenderPass.End(self)
RenderPass.PopDebugGroup(mut self)
RenderPass.PushDebugGroup(mut self, label: str)
RenderPass.SetBindGroup(mut self, index: int, g: &BindGroup, dynamic: []u32)
RenderPass.SetImmediates(mut self, offset: int, data: []u8)
RenderPass.SetIndexBuffer(mut self, b: &Buffer, format: IndexFormat, offset: int)
RenderPass.SetPipeline(mut self, p: &RenderPipeline)
RenderPass.SetVertexBuffer(mut self, slot: int, b: &Buffer, offset: int)
ComputePass.Dispatch(mut self, x: int, y: int, z: int)
ComputePass.End(self)
ComputePass.PopDebugGroup(mut self)
ComputePass.PushDebugGroup(mut self, label: str)
ComputePass.SetBindGroup(mut self, index: int, g: &BindGroup, dynamic: []u32)
ComputePass.SetImmediates(mut self, offset: int, data: []u8)
ComputePass.SetPipeline(mut self, p: &ComputePipeline)
SurfaceTexture.Abandon(self)
SurfaceTexture.View(&self): &TextureView from self
Swapchain.Close(self, io: Io, dev: mut Device)
BindGroup.Close(self, dev: mut Device)
BindGroupLayout.Close(self, dev: mut Device)
Buffer.Close(self, dev: mut Device)
ComputePipeline.Close(self, dev: mut Device)
PipelineLayout.Close(self, dev: mut Device)
RenderPipeline.Close(self, dev: mut Device)
Sampler.Close(self, dev: mut Device)
Shader.Close(self, dev: mut Device)
Texture.Close(self, dev: mut Device)
TextureView.Close(self, dev: mut Device)

// std.window
window.Open(heap: &Heap, io: Io, o: WindowOptions): Error!Window from heap
Window.Close(self)
Window.Poll(&self): ?Event
Window.SetFullscreen(&self, f: Fullscreen): Error!void
Window.Size(&self): (width: int, height: int)
Window.Wait(&self, io: Io): Error!void

// std.image
image.Compare(a: &Image, b: &Image, tolerance: int): Error!Difference
image.DecodePng(a: &Arena, bytes: []u8, limits: DecodeLimits): Error!Image from a
image.EncodePng(a: &Arena, img: Image): Error![]u8 from a
```
