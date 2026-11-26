// Why: rendered GPU output must match committed WARP golden baselines on
// WARP and stay within bounded perceptual tolerance on hardware and across
// D3D12 vs Vulkan, including winding and vertex/instance ID pinning scenes.
//
// What: device golden comparison tests, cross-backend D3D12 vs Vulkan tests,
// and CW/CCW culling + nonzero base_vertex/first_instance pinning tests.
//
// How: uses RenderGoldenScene from tests_device_scenes.mk, DecodeGoldenImage
// from tests_goldens.mk, and std.image.Compare for exact/tolerance checks.

import std.gpu.dx
import std.image

// The over-tolerance budget in pixels per thousand: 1 (0.1 %), and 5
// (0.5 %) for the MSAA scene after resolve, whose edge coverage differs most.
SceneBudget(idx: int): int {
    return if idx == GoldenMsaaScene { 5 } else { 1 }
}

const GoldenMsaaScene: int = 4

// Reports whether the scene idx should run on dev: MSAA needs standard sample
// locations for its result to be comparable, which Vulkan reports per device.
SceneComparable(dev: &Device, idx: int): bool {
    if idx != GoldenMsaaScene || dev.Backend() != Backend.Vulkan {
        return true
    }
    return dev.state.Ref().standard_sample_locations
}

// CompareScene prints one line naming the image and the pairing with the
// measured difference, then reports whether it is within budget: at most
// budget pixels per thousand over tol, none of them isolated from a
// within-tolerance 4-neighbour.
CompareScene(sys: &System, name: str, how: str, want: &image.Image, got: &image.Image, tol: int, budget: int): image.Error!bool {
    d := try image.Compare(want, got, tol)
    limit := (d.pixels * budget) / 1000
    ok := d.over <= limit && d.isolated == 0
    _ = sys.Print("golden {n} {h}: tol={t} max={m} over={o}/{l} isolated={i} {r}\n", (n: name, h: how, t: tol, m: d.max, o: d.over, l: limit, i: d.isolated, r: if ok { "ok" } else { "FAIL" })) else {}
    return ok
}

test "device goldens: 9 scenes verified against WARP baselines" (sys: &System) {
    kind := match ActiveAdapterKind(sys.arena, sys.io) {
        some(k) { k }
        none {
            _ = sys.Print("skip: no GPU adapter available\n") else {}
            return
        }
    }
    var dev := try OpenDeviceForKind(sys.heap, sys.io, kind, DebugMode.Strict)
    defer AssertCleanLeaks(sys, dev)
    _ = sys.Print("adapter {k}: {n}\n", (k: kind, n: dev.Info().name)) else {}

    // WARP renders compare byte for byte with the goldens when the
    // d3d10warp.dll version is the one that made them. Another version is
    // measured and printed but does not fail: its bound is not yet accepted.
    is_warp := kind.Eq(&"warp")
    host_ver := if is_warp { dx.WarpDllVersion(sys.arena, sys.io) } else { "" }
    exact := is_warp && host_ver.Eq(&GoldenWarpVersion)
    if is_warp {
        Assert(host_ver.Len() >= 7)
        _ = sys.Print("d3d10warp.dll golden={g} host={h} mode={m}\n", (g: GoldenWarpVersion, h: host_ver, m: if exact { "exact" } else { "measure-only" })) else {}
    }

    // Hardware renders compare with WARP rendering the same scene in this run.
    var warp_dev: ?Device = if is_warp {
        none
    } else {
        some(try OpenDeviceForKind(sys.heap, sys.io, "warp", DebugMode.Strict))
    }
    defer {
        match warp_dev {
            some(w) { AssertCleanLeaks(sys, w) }
            none {}
        }
    }

    var failed := 0
    for i in 0..GoldenSceneCount {
        if !SceneComparable(&dev, i) {
            _ = sys.Print("golden {n}: skipped, non-standard sample locations\n", (n: GoldenSceneName(i))) else {}
            continue
        }
        got := try RenderGoldenScene(sys.arena, sys.io, mut dev, i)
        match mut warp_dev {
            some(w) {
                ref_img := try RenderGoldenScene(sys.arena, sys.io, w, i)
                if !(try CompareScene(sys, GoldenSceneName(i), "hw-vs-warp", &ref_img, &got, 2, SceneBudget(i))) {
                    failed += 1
                }
            }
            none {
                want := try DecodeGoldenImage(sys.arena, i)
                if exact {
                    if !(try CompareScene(sys, GoldenSceneName(i), "warp-exact", &want, &got, 0, 0)) {
                        failed += 1
                    }
                } else {
                    _ = try CompareScene(sys, GoldenSceneName(i), "warp-measured", &want, &got, 2, SceneBudget(i))
                }
            }
        }
    }
    Assert(failed == 0)
}

test "device cross-backend: 9 golden scenes D3D12 vs Vulkan within tolerance 2" (sys: &System) {
    if RequestedGpuAdapter().Eq(&"warp") {
        _ = sys.Print("skip: MK_GPU_ADAPTER=warp\n") else {}
        return
    }
    if !HasNonSoftwareDx(sys.arena, sys.io) || !HasVulkanAdapter(sys.arena, sys.io) {
        _ = sys.Print("skip: requires both hardware D3D12 and Vulkan adapters\n") else {}
        return
    }
    var dx_dev := try OpenDeviceForKind(sys.heap, sys.io, "d3d12", DebugMode.Strict)
    defer AssertCleanLeaks(sys, dx_dev)
    var vk_dev := try OpenDeviceForKind(sys.heap, sys.io, "vulkan", DebugMode.Validate)
    defer AssertCleanLeaks(sys, vk_dev)
    _ = sys.Print("d3d12: {a} (vendor {av}, device {ad}); vulkan: {b} (vendor {bv}, device {bd})\n", (a: dx_dev.Info().name, av: dx_dev.Info().vendor, ad: dx_dev.Info().device, b: vk_dev.Info().name, bv: vk_dev.Info().vendor, bd: vk_dev.Info().device)) else {}

    var failed := 0
    for i in 0..GoldenSceneCount {
        if !SceneComparable(&vk_dev, i) {
            _ = sys.Print("golden {n}: skipped, non-standard sample locations\n", (n: GoldenSceneName(i))) else {}
            continue
        }
        img_dx := try RenderGoldenScene(sys.arena, sys.io, mut dx_dev, i)
        img_vk := try RenderGoldenScene(sys.arena, sys.io, mut vk_dev, i)
        if !(try CompareScene(sys, GoldenSceneName(i), "d3d12-vs-vulkan", &img_dx, &img_vk, 2, SceneBudget(i))) {
            failed += 1
        }
    }
    Assert(failed == 0)
}

IsPinOrange(px: []u8): bool {
    return px[0] >= 250 && px[1] >= 60 && px[1] <= 68 && px[2] == 0 && px[3] == 255
}

IsPinBlack(px: []u8): bool {
    return px[0] == 0 && px[1] == 0 && px[2] == 0 && px[3] == 255
}

VerifyPinningOnDevice(a: &Arena, io: Io, dev: mut Device): Error!(ccw: image.Image, cw: image.Image, ids: image.Image) from a {
    // Framebuffer space has Y down. The left triangle runs bottom-left,
    // bottom-right, top: counter-clockwise on screen. The right triangle runs
    // bottom-left, top, bottom-right: clockwise. Back-face culling keeps the
    // front winding only, so each FrontFace keeps exactly one of the two.
    ccw := try RenderPinningCullScene(a, io, dev, FrontFace.Ccw)
    lo := (32 * 64 + 16) * 4
    ro := (32 * 64 + 48) * 4
    Assert(IsPinOrange(ccw.data[lo..lo + 4]) && IsPinBlack(ccw.data[ro..ro + 4]))

    cw := try RenderPinningCullScene(a, io, dev, FrontFace.Cw)
    Assert(IsPinBlack(cw.data[lo..lo + 4]) && IsPinOrange(cw.data[ro..ro + 4]))

    ids := try RenderPinningIdsScene(a, io, dev)
    // DrawIndexed(3, 2, 2, 5, 7): the second instance draws last, so G holds
    // SV_InstanceID 1 (first_instance not added) and R an SV_VertexID in 0..2
    // (base_vertex not added), while B shows the attribute fetched from
    // vertices 5..7 (base_vertex applied to the fetch).
    co := (32 * 64 + 32) * 4
    Assert(ids.data[co] <= 2 && ids.data[co + 1] == 1 && ids.data[co + 2] >= 200 && ids.data[co + 2] <= 208 && ids.data[co + 3] == 255)
    return (ccw: ccw, cw: cw, ids: ids)
}

test "device pinning: CW/CCW culling and base_vertex/first_instance IDs" (sys: &System) {
    kind := match ActiveAdapterKind(sys.arena, sys.io) {
        some(k) { k }
        none {
            _ = sys.Print("skip: no GPU adapter available\n") else {}
            return
        }
    }
    var dev := try OpenDeviceForKind(sys.heap, sys.io, kind, DebugMode.Strict)
    defer AssertCleanLeaks(sys, dev)
    _ = try VerifyPinningOnDevice(sys.arena, sys.io, mut dev)

    req := RequestedGpuAdapter()
    if !req.Eq(&"warp") && HasNonSoftwareDx(sys.arena, sys.io) && HasVulkanAdapter(sys.arena, sys.io) {
        var dx_dev := try OpenDeviceForKind(sys.heap, sys.io, "d3d12", DebugMode.Strict)
        defer AssertCleanLeaks(sys, dx_dev)
        var vk_dev := try OpenDeviceForKind(sys.heap, sys.io, "vulkan", DebugMode.Validate)
        defer AssertCleanLeaks(sys, vk_dev)

        r_dx := try VerifyPinningOnDevice(sys.arena, sys.io, mut dx_dev)
        r_vk := try VerifyPinningOnDevice(sys.arena, sys.io, mut vk_dev)
        Assert(try CompareScene(sys, "pin_ccw", "d3d12-vs-vulkan", &r_dx.ccw, &r_vk.ccw, 2, 0))
        Assert(try CompareScene(sys, "pin_cw", "d3d12-vs-vulkan", &r_dx.cw, &r_vk.cw, 2, 0))
        Assert(try CompareScene(sys, "pin_ids", "d3d12-vs-vulkan", &r_dx.ids, &r_vk.ids, 2, 0))
    }
}
