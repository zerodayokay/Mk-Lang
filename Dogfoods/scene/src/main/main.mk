// Why: the `scene` CLI provides offline scene/shader validation, adapter
// probing, headless GPU rendering to PNG with image.Compare verification, and
// an interactive orbit-camera viewer window.
//
// What: Main, HelpText, CmdCheck, CmdCheckShaders, CmdProbe, CmdRender, and
// CmdView, with the error formatting and the image tolerance rule they share.
//
// How: dispatches subcommands, catches scene/GPU/window errors cleanly with
// exit code 1 on input/runtime failure and 2 on CLI usage errors.

import std.encoding
import std.fs
import std.gpu
import std.image
import std.window
import src.obj
import src.renderer
import src.scenedoc
import src.shaders

const ExitOk: int = 0
const ExitFail: int = 1
const ExitUsage: int = 2

const HelpText: str = """
    usage: scene <command> [options] [arguments]

    commands:
      check SCENE                 parse SCENE, load OBJ/PNG assets, check shaders
      check-shaders               verify HLSL/SPIR-V/DXIL hashes and CheckShader
      probe [--backend B] [--list]
                                  check shaders and probe GPU adapters
      render SCENE OUT [options]  render SCENE offscreen to PNG OUT
      view SCENE [options]        open an interactive orbit-camera window
      help                        print this help text

    render and view options:
      --backend auto|d3d12|vulkan select GPU backend (default auto)
      --warp                      prefer the software adapter (WARP)
      --validate                  enable the debug layer when present
      --frames N                  render N frames (view: close after N presented)

    render options:
      --strict                    require the debug layer; its errors fail
      --compare FILE.png          compare the image with FILE.png
      --compare-warp              also render on WARP and compare
      --tol N                     channel tolerance for comparisons (default 2)

    view options:
      --mode fifo|mailbox|immediate  swapchain present mode (default fifo)

    view keys: drag to orbit, wheel to zoom, M toggles 4x MSAA,
    F toggles fullscreen, Esc closes.
    """

PrintUsageErr(sys: &System, msg: str): int {
    _ = sys.PrintErr("scene: {m}\n", (m: msg)) else {}
    return ExitUsage
}

ParseBackend(s: str): ?gpu.Backend {
    if s.Eq(&"auto") { return some(gpu.Backend.Auto) }
    if s.Eq(&"d3d12") { return some(gpu.Backend.D3d12) }
    if s.Eq(&"vulkan") { return some(gpu.Backend.Vulkan) }
    return none
}

ParsePresentMode(s: str): ?gpu.PresentMode {
    if s.Eq(&"fifo") { return some(gpu.PresentMode.Fifo) }
    if s.Eq(&"mailbox") { return some(gpu.PresentMode.Mailbox) }
    if s.Eq(&"immediate") { return some(gpu.PresentMode.Immediate) }
    return none
}

FormatSceneError(a: &Arena, e: scenedoc.Error | OutOfMemory): OutOfMemory!str from a {
    match e {
        scenedoc.BadScene(line, msg) {
            return try a.Fmt("line {l}: {m}", (l: line, m: msg))
        }
        scenedoc.ObjFail(line, err: oe) {
            match oe {
                obj.BadObj(line: ol, msg: omsg) {
                    return try a.Fmt("line {l}: obj line {ol}: {m}", (l: line, ol: ol, m: omsg))
                }
                _ { return try a.Fmt("line {l}: obj error", (l: line)) }
            }
        }
        scenedoc.ImageFail(line, err: _) {
            return try a.Fmt("line {l}: invalid PNG texture", (l: line))
        }
        scenedoc.FsFail(err: fe) {
            match fe {
                fs.NotFound { return "file not found" }
                fs.Denied { return "permission denied" }
                _ { return "filesystem error" }
            }
        }
        OutOfMemory { return "out of memory" }
        _ { return "scene error" }
    }
}

// FormatGpuError describes e in one line; an Invalid names its misuse and
// the command or entry index it reports.
FormatGpuError(a: &Arena, e: gpu.Error): str from a {
    match e {
        gpu.Unsupported { return "unsupported on this target" }
        gpu.NoAdapter { return "no matching GPU adapter" }
        gpu.Missing(feature: _) { return "missing required GPU feature" }
        gpu.DeviceLost(reason: _) { return "GPU device lost" }
        gpu.OutOfDeviceMemory { return "out of GPU memory" }
        gpu.SurfaceLost { return "window surface lost" }
        gpu.Invalid(misuse, at) {
            return a.Fmt("invalid GPU use: {m} (at {at})", (m: misuse, at: at)) else { "invalid GPU use" }
        }
        gpu.Driver(code, call: _) {
            return a.Fmt("GPU driver error {c}", (c: code)) else { "GPU driver error" }
        }
        Canceled { return "canceled" }
        OutOfMemory { return "out of memory" }
        _ { return "GPU error" }
    }
}

// FormatCheckError describes a failure of shaders.VerifyAll.
FormatCheckError(a: &Arena, e: gpu.Error | encoding.Error | OutOfMemory): str from a {
    match e {
        OutOfMemory { return "out of memory" }
        gpu.Invalid(misuse, at) {
            return a.Fmt("{m} (at {at})", (m: misuse, at: at)) else { "invalid shader" }
        }
        gpu.Unsupported { return "unsupported on this target" }
        _ { return "hash encoding failed" }
    }
}

CmdCheckShaders(sys: &System): int {
    n := shaders.VerifyAll(sys.arena) else e {
        _ = sys.PrintErr("scene: shader verification failed: {e}\n", (e: FormatCheckError(sys.arena, e))) else {}
        return ExitFail
    }
    _ = sys.Print("shaders: {n} ok\n", (n)) else {}
    return ExitOk
}

CmdCheck(sys: &System, args: []str): int {
    if args.Len() != 1 {
        return PrintUsageErr(sys, "check requires SCENE")
    }
    sc := scenedoc.LoadScene(sys.arena, sys.io, args[0]) else e {
        match e {
            OutOfMemory {
                _ = sys.PrintErr("scene: out of memory\n") else {}
            }
            _ {
                msg := FormatSceneError(sys.arena, e) else { "scene error" }
                _ = sys.PrintErr("scene: {p}: {m}\n", (p: args[0], m: msg)) else {}
            }
        }
        return ExitFail
    }
    n_sh := shaders.VerifyAll(sys.arena) else {
        _ = sys.PrintErr("scene: shader check failed\n") else {}
        return ExitFail
    }
    _ = sys.Print(
        "scene ok: {w}x{h} msaa={m} shadows={sh} blur={bl} overlay={ov} meshes={nm} textures={nt} objects={no} instances={ni} shaders={ns}\n",
        (
            w: sc.width,
            h: sc.height,
            m: sc.msaa,
            sh: sc.shadows,
            bl: sc.blur,
            ov: sc.overlay,
            nm: sc.meshes.Len(),
            nt: sc.textures.Len(),
            no: sc.objects.Len(),
            ni: sc.instances,
            ns: n_sh,
        ),
    ) else {}
    return ExitOk
}

CmdProbe(sys: &System, args: []str): int {
    var backend := gpu.Backend.Auto
    var list := false
    var i := 0
    for i < args.Len() {
        a := args[i]
        if a.Eq(&"--backend") {
            if i + 1 >= args.Len() {
                return PrintUsageErr(sys, "--backend requires a value")
            }
            if b := ParseBackend(args[i + 1]) {
                backend = b
            } else {
                return PrintUsageErr(sys, "unknown backend")
            }
            i += 2
        } else if a.Eq(&"--list") {
            list = true
            i += 1
        } else {
            return PrintUsageErr(sys, "unexpected argument to probe")
        }
    }
    n_sh := shaders.VerifyAll(sys.arena) else {
        _ = sys.PrintErr("scene: shader verification failed\n") else {}
        return ExitFail
    }
    ads := gpu.Adapters(sys.arena, sys.io, backend) else e {
        match e {
            gpu.Unsupported {
                _ = sys.Print("probe: shaders={ns} adapters=unsupported\n", (ns: n_sh)) else {}
                return ExitOk
            }
            _ {
                _ = sys.PrintErr("scene: probe failed: {m}\n", (m: FormatGpuError(sys.arena, e))) else {}
                return ExitFail
            }
        }
    }
    // The counts depend on the machine, so they print only with --list.
    word := if ads.Len() > 0 { "available" } else { "none" }
    _ = sys.Print("probe: shaders={ns} adapters={w}\n", (ns: n_sh, w: word)) else {}
    if list {
        for k in 0..ads.Len() {
            ad := &ads[k]
            kind := if ad.kind == gpu.AdapterKind.Software { "software" } else { "hardware" }
            be := if ad.id.backend == gpu.Backend.Vulkan { "vulkan" } else { "d3d12" }
            _ = sys.Print("  {b} {k} {n}\n", (b: be, k: kind, n: ad.name)) else {}
        }
    }
    return ExitOk
}

// WithinTolerance applies the hardware-against-WARP rule: no pixel over the
// tolerance without a neighbour within it, and at most 0.1 % of pixels over
// it (0.5 % after an MSAA resolve, whose sample positions differ).
WithinTolerance(diff: image.Difference, msaa: int): bool {
    if diff.isolated > 0 {
        return false
    }
    per_mille_x10 := if msaa > 1 { 5 } else { 1 }
    return diff.over * 1000 <= diff.pixels * per_mille_x10
}

RenderAndCompare(
    sys: &System,
    sc: &scenedoc.Scene,
    opts: renderer.RenderOptions,
    frames: int,
): gpu.Error|OutOfMemory!image.Image from sys.arena {
    var r := try renderer.NewRenderer(sys.heap, sys.io, sc, opts, none)
    onerr {
        _ = r.Close(sys.io)
    }
    var img := try r.RenderOffscreen(sys.arena, sys.io, 0.0)
    for f in 1..frames {
        _ = try r.RenderOffscreen(sys.arena, sys.io, Cast[f32](f) * 3.0)
    }
    msgs := try r.TakeMessages(sys.arena)
    for i in 0..msgs.Len() {
        if msgs[i].severity == gpu.Severity.Error {
            _ = sys.PrintErr("gpu msg: {t}\n", (t: msgs[i].text)) else {}
        }
    }
    leaks := r.Close(sys.io)
    if leaks.native != 0 || leaks.errors != 0 {
        _ = sys.PrintErr("scene: leaks native={n} errors={e} warnings={w}\n", (n: leaks.native, e: leaks.errors, w: leaks.warnings)) else {}
        fail gpu.Invalid(misuse: gpu.Misuse.DebugLayer, at: leaks.native + leaks.errors)
    }
    return img
}

ImageHasContent(img: &image.Image): bool {
    if img.width <= 0 || img.height <= 0 || img.data.Len() < 4 {
        return false
    }
    r0 := img.data[0]
    g0 := img.data[1]
    b0 := img.data[2]
    n := img.width * img.height
    for i in 1..n {
        o := i * 4
        if img.data[o] != r0 || img.data[o + 1] != g0 || img.data[o + 2] != b0 {
            return true
        }
    }
    return false
}

CmdRender(sys: &System, args: []str): int {
    if args.Len() < 2 {
        return PrintUsageErr(sys, "render requires SCENE OUT")
    }
    scene_path := args[0]
    out_path := args[1]
    var opts := renderer.DefaultRenderOptions
    var frames := 1
    var cmp_path := ""
    var cmp_warp := false
    var tol := 2

    var i := 2
    for i < args.Len() {
        a := args[i]
        if a.Eq(&"--backend") {
            if i + 1 >= args.Len() { return PrintUsageErr(sys, "--backend requires a value") }
            if b := ParseBackend(args[i + 1]) { opts.backend = b } else { return PrintUsageErr(sys, "unknown backend") }
            i += 2
        } else if a.Eq(&"--warp") {
            opts.software = true
            i += 1
        } else if a.Eq(&"--strict") {
            opts.debug = gpu.DebugMode.Strict
            i += 1
        } else if a.Eq(&"--validate") {
            opts.debug = gpu.DebugMode.Validate
            i += 1
        } else if a.Eq(&"--frames") {
            if i + 1 >= args.Len() { return PrintUsageErr(sys, "--frames requires N") }
            n := args[i + 1].ParseInt() else { return PrintUsageErr(sys, "invalid --frames") }
            if n < 1 || n > 1000 { return PrintUsageErr(sys, "--frames out of range 1..1000") }
            frames = int(n)
            i += 2
        } else if a.Eq(&"--compare") {
            if i + 1 >= args.Len() { return PrintUsageErr(sys, "--compare requires FILE.png") }
            cmp_path = args[i + 1]
            i += 2
        } else if a.Eq(&"--compare-warp") {
            cmp_warp = true
            i += 1
        } else if a.Eq(&"--tol") {
            if i + 1 >= args.Len() { return PrintUsageErr(sys, "--tol requires N") }
            n := args[i + 1].ParseInt() else { return PrintUsageErr(sys, "invalid --tol") }
            if n < 0 || n > 255 { return PrintUsageErr(sys, "--tol out of range 0..255") }
            tol = int(n)
            i += 2
        } else {
            return PrintUsageErr(sys, "unknown render option")
        }
    }

    sc := scenedoc.LoadScene(sys.arena, sys.io, scene_path) else e {
        msg := FormatSceneError(sys.arena, e) else { "scene error" }
        _ = sys.PrintErr("scene: {p}: {m}\n", (p: scene_path, m: msg)) else {}
        return ExitFail
    }

    img := RenderAndCompare(sys, &sc, opts, frames) else e {
        _ = sys.PrintErr("scene: render failed: {m}\n", (m: FormatGpuError(sys.arena, e))) else {}
        return ExitFail
    }
    if !ImageHasContent(&img) {
        _ = sys.PrintErr("scene: rendered image is uniform/blank\n") else {}
        return ExitFail
    }

    png := image.EncodePng(sys.arena, img) else {
        _ = sys.PrintErr("scene: PNG encode failed\n") else {}
        return ExitFail
    }
    fs.WriteAll(sys.io, out_path, png) else {
        _ = sys.PrintErr("scene: failed writing {p}\n", (p: out_path)) else {}
        return ExitFail
    }

    // Round-trip PNG decode + Compare self-check.
    rt_img := image.DecodePng(sys.arena, png, image.DefaultDecodeLimits) else {
        _ = sys.PrintErr("scene: PNG round-trip decode failed\n") else {}
        return ExitFail
    }
    rt_diff := image.Compare(&img, &rt_img, 0) else {
        _ = sys.PrintErr("scene: PNG round-trip compare failed\n") else {}
        return ExitFail
    }
    if rt_diff.max != 0 {
        _ = sys.PrintErr("scene: PNG round-trip mismatch\n") else {}
        return ExitFail
    }

    if cmp_path.Len() > 0 {
        ref_bytes := fs.ReadBytes(sys.arena, sys.io, cmp_path) else {
            _ = sys.PrintErr("scene: failed reading reference {p}\n", (p: cmp_path)) else {}
            return ExitFail
        }
        ref_img := image.DecodePng(sys.arena, ref_bytes, image.DefaultDecodeLimits) else {
            _ = sys.PrintErr("scene: invalid reference PNG {p}\n", (p: cmp_path)) else {}
            return ExitFail
        }
        diff := image.Compare(&img, &ref_img, tol) else {
            _ = sys.PrintErr("scene: compare size mismatch\n") else {}
            return ExitFail
        }
        if !WithinTolerance(diff, sc.msaa) {
            _ = sys.PrintErr("scene: compare failed max={m} over={o} isolated={i}\n", (m: diff.max, o: diff.over, i: diff.isolated)) else {}
            return ExitFail
        }
    }

    if cmp_warp {
        var wopts := renderer.DefaultRenderOptions
        wopts.backend = gpu.Backend.D3d12
        wopts.software = true
        wimg := RenderAndCompare(sys, &sc, wopts, 1) else e {
            _ = sys.PrintErr("scene: warp compare failed: {m}\n", (m: FormatGpuError(sys.arena, e))) else {}
            return ExitFail
        }
        diff := image.Compare(&img, &wimg, tol) else {
            _ = sys.PrintErr("scene: warp compare size mismatch\n") else {}
            return ExitFail
        }
        if !WithinTolerance(diff, sc.msaa) {
            _ = sys.PrintErr("scene: hw vs warp mismatch max={m} over={o} isolated={i}\n", (m: diff.max, o: diff.over, i: diff.isolated)) else {}
            return ExitFail
        }
    }

    _ = sys.Print(
        "rendered {w}x{h} frames={f} msaa={m} shadows={sh} blur={bl} instances={ni}\n",
        (w: img.width, h: img.height, f: frames, m: sc.msaa, sh: sc.shadows, bl: sc.blur, ni: sc.instances),
    ) else {}
    return ExitOk
}

CmdView(sys: &System, args: []str): int {
    if args.Len() < 1 {
        return PrintUsageErr(sys, "view requires SCENE")
    }
    scene_path := args[0]
    var opts := renderer.DefaultRenderOptions
    var max_frames := 0
    var i := 1
    for i < args.Len() {
        a := args[i]
        if a.Eq(&"--backend") {
            if i + 1 >= args.Len() { return PrintUsageErr(sys, "--backend requires a value") }
            if b := ParseBackend(args[i + 1]) { opts.backend = b } else { return PrintUsageErr(sys, "unknown backend") }
            i += 2
        } else if a.Eq(&"--warp") {
            opts.software = true
            i += 1
        } else if a.Eq(&"--validate") {
            opts.debug = gpu.DebugMode.Validate
            i += 1
        } else if a.Eq(&"--frames") {
            if i + 1 >= args.Len() { return PrintUsageErr(sys, "--frames requires N") }
            n := args[i + 1].ParseInt() else { return PrintUsageErr(sys, "invalid --frames") }
            if n < 1 || n > 100000 { return PrintUsageErr(sys, "--frames out of range 1..100000") }
            max_frames = int(n)
            i += 2
        } else if a.Eq(&"--mode") {
            if i + 1 >= args.Len() { return PrintUsageErr(sys, "--mode requires fifo|mailbox|immediate") }
            if pm := ParsePresentMode(args[i + 1]) { opts.present_mode = pm } else { return PrintUsageErr(sys, "unknown present mode") }
            i += 2
        } else {
            return PrintUsageErr(sys, "unknown view option")
        }
    }

    sc := scenedoc.LoadScene(sys.arena, sys.io, scene_path) else e {
        msg := FormatSceneError(sys.arena, e) else { "scene error" }
        _ = sys.PrintErr("scene: {p}: {m}\n", (p: scene_path, m: msg)) else {}
        return ExitFail
    }

    var wo := window.DefaultWindowOptions
    wo.title = "scene"
    wo.width = sc.width
    wo.height = sc.height
    win := window.Open(sys.heap, sys.io, wo) else {
        _ = sys.PrintErr("scene: window.Open failed\n") else {}
        return ExitFail
    }
    onerr win.Close()

    var r := renderer.NewRenderer(sys.heap, sys.io, &sc, opts, some(&win)) else e {
        _ = sys.PrintErr("scene: renderer init failed: {m}\n", (m: FormatGpuError(sys.arena, e))) else {}
        win.Close()
        return ExitFail
    }
    onerr {
        _ = r.Close(sys.io)
    }

    var dragging := false
    var last_x: f32 = 0.0
    var last_y: f32 = 0.0
    var full := false
    var running := true
    var deg: f32 = 0.0
    var presented := 0
    var status := ExitOk

    for running {
        for ev := win.Poll() {
            match ev.kind {
                .CloseRequested { running = false }
                .Resized(width, height) {
                    r.ResizeTargets(sys.io, width, height) else {
                        running = false
                    }
                }
                .Button(button, down, x, y, mods: _) {
                    if button == window.Button.Left {
                        dragging = down
                        last_x = x
                        last_y = y
                    }
                }
                .Pointer(x, y) {
                    if dragging {
                        dx := x - last_x
                        dy := y - last_y
                        last_x = x
                        last_y = y
                        r.Orbit(dx * 0.4, dy * 0.4, 0.0)
                    }
                }
                .Wheel(dx: _, dy) {
                    r.Orbit(0.0, 0.0, -dy * 0.1)
                }
                .Key(key, layout: _, scancode: _, down, repeat, mods: _) {
                    if down && !repeat {
                        if key == window.Key.Escape {
                            running = false
                        } else if key == window.Key.KeyM {
                            r.ToggleMsaa()
                        } else if key == window.Key.KeyF {
                            full = !full
                            _ = win.SetFullscreen(if full { window.Fullscreen.Borderless } else { window.Fullscreen.Windowed }) else {}
                        }
                    }
                }
                _ {}
            }
        }
        if !running {
            break
        }
        (width, height) := win.Size()
        drawn := r.DrawSwapchainFrame(sys.io, width, height, deg) else e {
            _ = sys.PrintErr("scene: frame error: {m}\n", (m: FormatGpuError(sys.arena, e))) else {}
            status = ExitFail
            break
        }
        if drawn {
            presented += 1
            if max_frames > 0 && presented >= max_frames {
                running = false
            }
        } else {
            win.Wait(sys.io) else { break }
        }
        deg += 0.5
    }

    backend := if r.Backend() == gpu.Backend.Vulkan { "vulkan" } else { "d3d12" }
    leaks := r.Close(sys.io)
    win.Close()
    if leaks.native != 0 || leaks.errors != 0 {
        _ = sys.PrintErr("scene: leaks native={n} errors={e}\n", (n: leaks.native, e: leaks.errors)) else {}
        return ExitFail
    }
    if max_frames > 0 && status == ExitOk {
        _ = sys.Print("view: {b} presented {n} frames\n", (b: backend, n: presented)) else {}
    }
    return status
}

export Main(sys: &System): int {
    args := sys.args
    if args.Len() < 2 {
        _ = sys.Print("{h}", (h: HelpText)) else {}
        return ExitOk
    }
    cmd := args[1]
    if cmd.Eq(&"help") || cmd.Eq(&"--help") || cmd.Eq(&"-h") {
        _ = sys.Print("{h}", (h: HelpText)) else {}
        return ExitOk
    }
    rest := args[2..args.Len()]
    if cmd.Eq(&"check-shaders") {
        return CmdCheckShaders(sys)
    }
    if cmd.Eq(&"check") {
        return CmdCheck(sys, rest)
    }
    if cmd.Eq(&"probe") {
        return CmdProbe(sys, rest)
    }
    if cmd.Eq(&"render") {
        return CmdRender(sys, rest)
    }
    if cmd.Eq(&"view") {
        return CmdView(sys, rest)
    }
    return PrintUsageErr(sys, "unknown command")
}
