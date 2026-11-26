// Why: scene files describe camera, lighting, meshes, textures, object
// placements, and post-processing options in a readable text format.
//
// What: Scene, Camera, Light, TextureAsset, MeshAsset, ObjectDesc, Error and LoadScene
// validating directives and loading referenced OBJ meshes and PNG textures.
//
// How: resolves relative asset paths against the scene file's directory, parses
// line by line with line-numbered errors, and decodes PNG textures via std.image.

import std.fs
import std.image
import std.math
import src.math3d
import src.obj

/// Camera settings for a scene.
export struct Camera {
    export eye: math3d.Vec3
    export target: math3d.Vec3
    export fov: f32
}

/// Directional light settings.
export struct Light {
    export dir: math3d.Vec3
    export color: math3d.Vec3
    export ambient: math3d.Vec3
}

/// A named decoded PNG texture asset.
export struct TextureAsset {
    export name: str
    export img: image.Image
}

/// A named parsed OBJ mesh asset.
export struct MeshAsset {
    export name: str
    export mesh: obj.Mesh
}

/// One placed mesh instance in the scene.
export struct ObjectDesc {
    export name: str
    export mesh_idx: int
    export tex_idx: int
    export tx: f32
    export ty: f32
    export tz: f32
    export rot_y: f32
    export scale: f32
    export color: math3d.Vec4
}

/// Complete parsed scene ready for the GPU renderer.
export struct Scene {
    export width: int
    export height: int
    export clear_r: f64
    export clear_g: f64
    export clear_b: f64
    export clear_a: f64
    export msaa: int
    export shadows: bool
    export blur: bool
    export overlay: bool
    export instances: int
    export camera: Camera
    export light: Light
    export textures: List[TextureAsset]
    export meshes: List[MeshAsset]
    export objects: List[ObjectDesc]
}

/// Errors returned while parsing or loading a scene file.
export open type Error = BadScene | ObjFail | ImageFail | FsFail

/// Syntax or semantic error in the scene text.
export error BadScene(line: int, msg: str)

/// OBJ loading failure.
export error ObjFail(line: int, err: obj.Error)

/// PNG decoding failure.
export error ImageFail(line: int, err: image.Error)

/// Filesystem failure loading scene or assets.
export error FsFail(err: fs.Error)

const ByteSpace: u8 = 32
const ByteTab: u8 = 9
const ByteCr: u8 = 13
const ByteSlash: u8 = 47
const ByteBackslash: u8 = 92

DirName(path: str): str {
    var last := -1
    for i in 0..path.Len() {
        b := path.Byte(i)
        if b == ByteSlash || b == ByteBackslash {
            last = i
        }
    }
    if last < 0 {
        return ""
    }
    return path[0..last]
}

JoinPath(a: &Arena, dir: str, rel: str): OutOfMemory!str from a, rel {
    if dir.Len() == 0 {
        return rel
    }
    return try a.Fmt("{d}/{r}", (d: dir, r: rel))
}

SplitLine(a: &Arena, line: str into a): OutOfMemory!List[str] from a, line {
    var out := List[str].New(a)
    var i := 0
    for i < line.Len() {
        for i < line.Len() && (line.Byte(i) == ByteSpace || line.Byte(i) == ByteTab || line.Byte(i) == ByteCr) {
            i += 1
        }
        start := i
        for i < line.Len() && line.Byte(i) != ByteSpace && line.Byte(i) != ByteTab && line.Byte(i) != ByteCr {
            i += 1
        }
        if i > start {
            try out.Push(line[start..i])
        }
    }
    return out
}

ParseF(s: str, line_no: int): Error!f32 {
    v := s.ParseFloat() else {
        fail BadScene(line: line_no, msg: "expected number")
    }
    if math.IsNan(v) || math.IsInf(v) {
        fail BadScene(line: line_no, msg: "non-finite number")
    }
    return Cast[f32](v)
}

ParseI(s: str, line_no: int): Error!int {
    v := s.ParseInt() else {
        fail BadScene(line: line_no, msg: "expected integer")
    }
    return int(v)
}

ParseBoolWord(s: str, line_no: int): Error!bool {
    if s.Eq(&"on") || s.Eq(&"true") || s.Eq(&"1") {
        return true
    }
    if s.Eq(&"off") || s.Eq(&"false") || s.Eq(&"0") {
        return false
    }
    fail BadScene(line: line_no, msg: "expected on|off")
}

/// Ensures an RGBA8 image copy for GPU upload (converts Gray8/Rgb8 to Rgba8).
ToRgba8(a: &Arena, img: image.Image): OutOfMemory!image.Image from a {
    match img.pixels {
        .Rgba8 {
            dst := try a.Slice[u8](img.data.Len())
            dst.CopyFrom(img.data)
            return image.Image(width: img.width, height: img.height, pixels: image.Pixels.Rgba8, data: dst[..])
        }
        .Rgb8 {
            n := img.width * img.height
            dst := try a.Slice[u8](n * 4)
            for i in 0..n {
                dst[i * 4] = img.data[i * 3]
                dst[i * 4 + 1] = img.data[i * 3 + 1]
                dst[i * 4 + 2] = img.data[i * 3 + 2]
                dst[i * 4 + 3] = 255
            }
            return image.Image(width: img.width, height: img.height, pixels: image.Pixels.Rgba8, data: dst[..])
        }
        .Gray8 {
            n := img.width * img.height
            dst := try a.Slice[u8](n * 4)
            for i in 0..n {
                g := img.data[i]
                dst[i * 4] = g
                dst[i * 4 + 1] = g
                dst[i * 4 + 2] = g
                dst[i * 4 + 3] = 255
            }
            return image.Image(width: img.width, height: img.height, pixels: image.Pixels.Rgba8, data: dst[..])
        }
        _ { return img }
    }
}

MapFsErr(e: fs.Error | OutOfMemory): Error | OutOfMemory {
    match e {
        OutOfMemory { return OutOfMemory }
        fs.NotFound { return FsFail(err: fs.NotFound) }
        fs.Denied { return FsFail(err: fs.Denied) }
        _ { return FsFail(err: fs.Os(code: 0)) }
    }
}

/// Parses a scene file and loads all referenced OBJ meshes and PNG textures into a.
/// Errors: Error (BadScene, ObjFail, ImageFail, FsFail), OutOfMemory. O(file + asset bytes).
export LoadScene(a: &Arena, io: Io, path: str): Error|OutOfMemory!Scene from a {
    src := fs.ReadText(a, io, path) else e {
        fail MapFsErr(e)
    }
    base_dir := DirName(path)
    var sc := Scene(
        width: 256,
        height: 256,
        clear_r: 0.08,
        clear_g: 0.1,
        clear_b: 0.14,
        clear_a: 1.0,
        msaa: 1,
        shadows: false,
        blur: false,
        overlay: false,
        instances: 0,
        camera: Camera(eye: math3d.V3(0.0, 2.0, 5.0), target: math3d.V3(0.0, 0.0, 0.0), fov: 50.0),
        light: Light(dir: math3d.V3(-0.5, -1.0, -0.4), color: math3d.V3(1.0, 0.96, 0.9), ambient: math3d.V3(0.18, 0.2, 0.24)),
        textures: List[TextureAsset].New(a),
        meshes: List[MeshAsset].New(a),
        objects: List[ObjectDesc].New(a),
    )

    var sub := try a.Sub()
    defer sub.Free()

    var line_no := 0
    for raw in src.Lines() {
        line_no += 1
        sub.Reset()
        hash_pos := raw.Find("#")
        line := match hash_pos {
            some(k) { raw[0..k] }
            none { raw }
        }
        toks := try SplitLine(&sub, line)
        if toks.Len() == 0 {
            continue
        }
        cmd := toks[0]
        if cmd.Eq(&"size") {
            if toks.Len() != 3 { fail BadScene(line: line_no, msg: "size requires W H") }
            w := try ParseI(toks[1], line_no)
            h := try ParseI(toks[2], line_no)
            if w < 16 || w > 4096 || h < 16 || h > 4096 {
                fail BadScene(line: line_no, msg: "size out of range 16..4096")
            }
            sc.width = w
            sc.height = h
        } else if cmd.Eq(&"clear") {
            if toks.Len() != 5 { fail BadScene(line: line_no, msg: "clear requires R G B A") }
            sc.clear_r = Cast[f64](try ParseF(toks[1], line_no))
            sc.clear_g = Cast[f64](try ParseF(toks[2], line_no))
            sc.clear_b = Cast[f64](try ParseF(toks[3], line_no))
            sc.clear_a = Cast[f64](try ParseF(toks[4], line_no))
        } else if cmd.Eq(&"msaa") {
            if toks.Len() != 2 { fail BadScene(line: line_no, msg: "msaa requires 1 or 4") }
            m := try ParseI(toks[1], line_no)
            if m != 1 && m != 4 { fail BadScene(line: line_no, msg: "msaa must be 1 or 4") }
            sc.msaa = m
        } else if cmd.Eq(&"shadows") {
            if toks.Len() != 2 { fail BadScene(line: line_no, msg: "shadows requires on|off") }
            sc.shadows = try ParseBoolWord(toks[1], line_no)
        } else if cmd.Eq(&"blur") {
            if toks.Len() != 2 { fail BadScene(line: line_no, msg: "blur requires on|off") }
            sc.blur = try ParseBoolWord(toks[1], line_no)
        } else if cmd.Eq(&"overlay") {
            if toks.Len() != 2 { fail BadScene(line: line_no, msg: "overlay requires on|off") }
            sc.overlay = try ParseBoolWord(toks[1], line_no)
        } else if cmd.Eq(&"instances") {
            if toks.Len() != 2 { fail BadScene(line: line_no, msg: "instances requires count") }
            n := try ParseI(toks[1], line_no)
            if n < 0 || n > 16384 { fail BadScene(line: line_no, msg: "instances out of range 0..16384") }
            sc.instances = n
        } else if cmd.Eq(&"camera") {
            if toks.Len() != 8 { fail BadScene(line: line_no, msg: "camera requires EX EY EZ TX TY TZ FOV") }
            ex := try ParseF(toks[1], line_no)
            ey := try ParseF(toks[2], line_no)
            ez := try ParseF(toks[3], line_no)
            tx := try ParseF(toks[4], line_no)
            ty := try ParseF(toks[5], line_no)
            tz := try ParseF(toks[6], line_no)
            fov := try ParseF(toks[7], line_no)
            if fov <= 1.0 || fov >= 179.0 { fail BadScene(line: line_no, msg: "fov out of range (1..179)") }
            sc.camera = Camera(eye: math3d.V3(ex, ey, ez), target: math3d.V3(tx, ty, tz), fov: fov)
        } else if cmd.Eq(&"light") {
            if toks.Len() != 7 { fail BadScene(line: line_no, msg: "light requires DX DY DZ R G B") }
            dx := try ParseF(toks[1], line_no)
            dy := try ParseF(toks[2], line_no)
            dz := try ParseF(toks[3], line_no)
            r := try ParseF(toks[4], line_no)
            g := try ParseF(toks[5], line_no)
            b := try ParseF(toks[6], line_no)
            sc.light = Light(dir: math3d.V3(dx, dy, dz), color: math3d.V3(r, g, b), ambient: sc.light.ambient)
        } else if cmd.Eq(&"texture") {
            if toks.Len() != 3 { fail BadScene(line: line_no, msg: "texture requires NAME PATH") }
            tname := try a.Fmt("{s}", (s: toks[1]))
            rel_tex := try a.Fmt("{s}", (s: toks[2]))
            full := try JoinPath(a, base_dir, rel_tex)
            png_bytes := fs.ReadBytes(&sub, io, full) else e {
                fail MapFsErr(e)
            }
            decoded := image.DecodePng(a, png_bytes, image.DefaultDecodeLimits) else e {
                fail ImageFail(line: line_no, err: e)
            }
            rgba := try ToRgba8(a, decoded)
            try sc.textures.Push(TextureAsset(name: tname, img: rgba))
        } else if cmd.Eq(&"mesh") {
            if toks.Len() != 3 { fail BadScene(line: line_no, msg: "mesh requires NAME PATH") }
            mname := try a.Fmt("{s}", (s: toks[1]))
            rel_mesh := try a.Fmt("{s}", (s: toks[2]))
            full := try JoinPath(a, base_dir, rel_mesh)
            obj_src := fs.ReadText(&sub, io, full) else e {
                fail MapFsErr(e)
            }
            parsed := obj.ParseObj(a, obj_src) else e {
                match e {
                    obj.BadObj(line: ol, msg: omsg) { fail ObjFail(line: line_no, err: obj.BadObj(line: ol, msg: omsg)) }
                    _ { fail OutOfMemory }
                }
            }
            try sc.meshes.Push(MeshAsset(name: mname, mesh: parsed))
        } else if cmd.Eq(&"object") {
            if toks.Len() != 12 { fail BadScene(line: line_no, msg: "object requires NAME MESH TEX X Y Z ROT SCALE R G B") }
            oname := try a.Fmt("{s}", (s: toks[1]))
            var m_idx := -1
            for i in 0..sc.meshes.Len() {
                if sc.meshes[i].name.Eq(&toks[2]) {
                    m_idx = i
                    break
                }
            }
            if m_idx < 0 { fail BadScene(line: line_no, msg: "unknown mesh") }
            var t_idx := -1
            for i in 0..sc.textures.Len() {
                if sc.textures[i].name.Eq(&toks[3]) {
                    t_idx = i
                    break
                }
            }
            if t_idx < 0 { fail BadScene(line: line_no, msg: "unknown texture") }
            ox := try ParseF(toks[4], line_no)
            oy := try ParseF(toks[5], line_no)
            oz := try ParseF(toks[6], line_no)
            rot := try ParseF(toks[7], line_no)
            scl := try ParseF(toks[8], line_no)
            cr := try ParseF(toks[9], line_no)
            cg := try ParseF(toks[10], line_no)
            cb := try ParseF(toks[11], line_no)
            try sc.objects.Push(ObjectDesc(
                name: oname,
                mesh_idx: m_idx,
                tex_idx: t_idx,
                tx: ox, ty: oy, tz: oz,
                rot_y: rot, scale: scl,
                color: math3d.V4(cr, cg, cb, 1.0),
            ))
        } else {
            fail BadScene(line: line_no, msg: "unknown directive")
        }
    }
    if sc.objects.Len() == 0 && sc.instances == 0 {
        fail BadScene(line: line_no, msg: "scene has no objects or instances")
    }
    if sc.instances > 0 && sc.meshes.Len() == 0 {
        fail BadScene(line: line_no, msg: "instances require at least one mesh")
    }
    return sc
}
