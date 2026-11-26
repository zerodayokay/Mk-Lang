// Why: the D3D12 presentation queue can still hold a back buffer after the
// rendering fence signals.
//
// What: a validated swapchain present-and-close test.
//
// How: closes two swapchains after presenting, on the same device, and
// verifies the presentation fence uses a new serial each time.

import std.gpu.dx

test "device dx close waits for the queued present" (sys: &System) {
    if Target.os != Os.Windows {
        return
    }
    var dev := try OpenDxPortable(sys.heap, sys.io, DebugMode.Strict)
    onerr dev.Abandon(sys.io)
    hwnd := dx.CreateHiddenTestHwnd()
    Assert(hwnd != 0)
    defer dx.DestroyHiddenTestHwnd(hwnd)

    for _ in 0..2 {
        var sc := try dev.NewSwapchainFromNative(sys.io, hwnd, 0, 64, 64, DefaultSwapchainOptions)
        onerr sc.Close(sys.io, mut dev)
        match try dev.Acquire(sys.io, mut sc) {
            .Ready(frame) {
                onerr frame.Abandon()
                var enc := try dev.Encoder("present fence")
                onerr enc.Abandon()
                colors := [ColorAttachment(
                    target: frame.View(),
                    resolve: none,
                    load: Load.Clear(r: 0.0, g: 0.0, b: 0.0, a: 1.0),
                    store: Store.Store,
                )]
                desc := PassDesc(colors: colors[..], depth: none, label: "present fence")
                var pass := enc.RenderPass(&desc)
                pass.End()
                try enc.Submit()
                _ = try dev.Present(sys.io, frame)
            }
            _ { Assert(false) }
        }
        last := dev.state.Ref().serials.LastSubmitted()
        sc.Close(sys.io, mut dev)
        Assert(dev.state.Ref().serials.LastSubmitted() == last + 1)
    }
    AssertCleanLeaks(sys, dev)
}
