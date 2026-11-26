# sketch

A paint program: drag to draw strokes in eight colours and any brush size, with undo, redo, clear, a typed caption, and files that save the strokes rather than the pixels. The canvas is painted on the CPU and shown through a `std.gpu` swapchain in a `std.window` window. A script drives it the way a person would. Headless, on any target, it feeds the program the window events a script implies. On Windows, it posts the real messages to a real window. The two runs must print the same picture hashes. It shows `std.window` (events, held state, cursor, fullscreen, title, text input, focus, close), swapchain frames in every present mode with `Hidden` and `Outdated` handled, and `import c.windows` from a package's own `unsafe` module.

## Running it

From the program's folder:

```
cd Dogfoods/sketch
mk run -- check-script cases/scripts/close.script
```

```text
strokes=1 undone=0 hash=9da36a06a1182115e000d7da73f5f2f85e9ccdfe20a7701936e367fda13507ba caption=""
```

```
sketch check-script SCRIPT          run SCRIPT against the document alone (any target)
sketch script SCRIPT [options]      run SCRIPT through a real window (Windows)
sketch draw [FILE] [options]        paint with the mouse; FILE is loaded, and saved on close
sketch export FILE OUT.png          paint a saved FILE and write it as a PNG
```

Window options: `--backend auto|d3d12|vulkan`, `--warp` (the software adapter), `--validate` (the debug layer, where installed), `--mode fifo|mailbox|immediate`, and `--visible` to show the window while a script runs; a script's window is hidden otherwise, so the real mouse cannot reach it.

Keys in a window: 1 to 8 pick a colour, B and V make the brush larger and smaller, U undoes, R redoes, C clears, and Enter starts typing the caption (Enter or Esc ends it, Backspace deletes). The caption shows in the title bar.

Exit codes: 0 done; 1 a failed step, a bad script or file, or a GPU failure; 2 a usage error.

### Scripts

One step per line; `#` starts a comment. The first step is `canvas W H` (16 to 2048 each); points are canvas pixels. A script's file names are relative to the working directory.

| Step | Does |
|---|---|
| `color R G B`, `brush R` | Set the brush directly |
| `move X Y`, `down`, `up` | Move the pointer to canvas pixel (X, Y); press or release the left button there |
| `key undo\|redo\|clear\|bigger\|smaller\|type\|escape\|backspace\|1`..`8` | Press and release a key |
| `text TEXT` | Type TEXT, one character at a time |
| `resize W H` | Resize the window's client area to W by H pixels |
| `minimize`, `restore` | Report the window minimised (its frames must be `Hidden`) and restored |
| `focus on\|off` | Gain or lose focus; losing it mid-stroke drops the stroke |
| `cursor arrow\|crosshair\|hand\|text\|hidden`, `fullscreen on\|off` | Window calls; nothing headless |
| `save FILE`, `load FILE` | Save the document, or replace it with a saved one |
| `snapshot FILE.png` | Write the canvas as a PNG; in a window, also draw it offscreen on the GPU and check the readback equals the canvas byte for byte |
| `expect strokes N undone M`, `expect hash HEX`, `expect held left\|none`, `expect caption TEXT`, `expect saved FILE` | Fail unless the document matches; `saved` compares what Save would write with FILE byte for byte |
| `print` | Print the counts, the picture's SHA-256 and the caption, after checking the picture painted step by step equals a fresh paint |
| `close` | Request the window's close; the run ends |

In a window, each step posts its messages and then waits, up to 5 seconds, for the event the window reports for it, so pointer motion is never coalesced. After a resize or a fullscreen change the run waits for a marker message, so every size change has reached the program before the next point is mapped.

### Files

```text
sketch 1 WIDTH HEIGHT
stroke R G B RADIUS X,Y X,Y ...
clear
caption TEXT
```

The strokes are saved in order, `clear` included, so a reloaded file can be undone stroke by stroke; the redo list and a stroke still being drawn are not saved.

## Design

- `src/doc` is the document: strokes in an arena (the one being drawn is the last, until the release commits it), undo and redo as moves between two lists, the file format, and integer-only painting (Bresenham steps stamping a disc), so a picture is the same bytes everywhere.
- `src/app` is a function of window events: the same `App.Apply` runs whether the events came from the OS or were made up by a headless run. `App.Sync` paints only the newest segment while drawing and repaints everything after an undo, redo, clear or load.
- `src/script` parses and checks a script before it runs.
- `src/screen` uploads the canvas to a texture and draws it with one triangle into a `Bgra8Unorm` swapchain image (no sRGB conversion, so the bytes reach the screen), and into an `Rgba8Unorm` texture of the canvas's size for the readback check.
- `src/plat` is chosen per target in `pkg.mk`. On Windows it is the package's one `unsafe` module and posts `WM_MOUSEMOVE`, button, key, `WM_CHAR`, `WM_SIZE`, focus, `WM_MOVE` and `WM_CLOSE` messages with `PostMessageW`, and resizes with `SetWindowPos`. On Linux every call reports false and `script` reports `unsupported on this target`.
- Error payloads hold only static text, so a failed check prints its detail (the hash it got, the counts) on standard error before the failure line.

## Limits

- A Direct3D 12 swapchain does not report `Hidden` for a window reported minimised (`std.gpu` passes the swapchain's size, not the window's, to the backend), so `script-hidden` runs on Vulkan.
- `--validate` on Direct3D 12 ends the process during the first present, a `std.gpu` defect; the window cases use it on Vulkan only.
- `fullscreen on` shows a script's hidden window, after which the real mouse can move over it: a script keeps fullscreen steps after its last stroke.
- Scripts are tested with a client area at least the canvas's size: a smaller window maps several canvas pixels to one client pixel.
- `draw` is interactive and has no case.

## Section 18 lines

The standard-library lines this program uses, as `doc/reference.md` lists them; `go run ./tools/dogfood cover` reads this block.

```
// std.window
window.Open(heap: &Heap, io: Io, o: WindowOptions): Error!Window from heap
Window.Close(self)
Window.Poll(&self): ?Event
Window.Next(&self, io: Io): Error!Event
Window.Wait(&self, io: Io): Error!void
Window.Size(&self): (width: int, height: int)
Window.Held(&self): InputState
Window.SetTitle(&self, title: str): Error!void
Window.SetFullscreen(&self, f: Fullscreen): Error!void
Window.SetCursor(&self, c: Cursor): Error!void
Window.SetCursorMode(&self, m: CursorMode): Error!void
Window.Surface(&self): Error!Surface
Surface.Native(&self): Native
Surface.Close(self)
InputState.IsButtonDown(&self, b: Button): bool

// std.gpu
gpu.Open(heap: &Heap, io: Io, o: DeviceOptions): Error!Device from heap
gpu.CheckShader(code: ShaderCode, stage: Stage, entry: str): Error!void
gpu.ColorOf(format: TextureFormat): ColorTarget
Device.Abandon(self, io: Io)
Device.Acquire(mut self, io: Io, sc: mut Swapchain): Error!Acquired from sc
Device.Close(self, io: Io): Leaks
Device.Encoder(mut self, label: str): Error!Encoder from self
Device.NewBindGroup(mut self, layout: &BindGroupLayout, entries: []BindEntry, label: str): Error!BindGroup
Device.NewBindGroupLayout(mut self, entries: []LayoutEntry, label: str): Error!BindGroupLayout
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
Device.WriteTexture(mut self, dst: TextureRegion, data: []u8, size: Extent): Error!void
Encoder.Abandon(self)
Encoder.PopDebugGroup(mut self)
Encoder.PushDebugGroup(mut self, label: str)
Encoder.RenderPass(mut self, d: &PassDesc): RenderPass from self
Encoder.Submit(self): Error!void
RenderPass.Draw(mut self, vertices: int, instances: int, first_vertex: int, first_instance: int)
RenderPass.End(self)
RenderPass.SetBindGroup(mut self, index: int, g: &BindGroup, dynamic: []u32)
RenderPass.SetPipeline(mut self, p: &RenderPipeline)
SurfaceTexture.Abandon(self)
SurfaceTexture.View(&self): &TextureView from self
Swapchain.Close(self, io: Io, dev: mut Device)
BindGroup.Close(self, dev: mut Device)
BindGroupLayout.Close(self, dev: mut Device)
PipelineLayout.Close(self, dev: mut Device)
RenderPipeline.Close(self, dev: mut Device)
Sampler.Close(self, dev: mut Device)
Shader.Close(self, dev: mut Device)
Texture.Close(self, dev: mut Device)
TextureView.Close(self, dev: mut Device)

// std.image
image.EncodePng(a: &Arena, img: Image): Error![]u8 from a
```
