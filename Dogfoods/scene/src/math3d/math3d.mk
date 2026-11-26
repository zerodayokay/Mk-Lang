// Why: 3D rendering needs row-major transforms, camera projections, vector
// math, and little-endian byte packing for GPU uniform and vertex buffers.
//
// What: Vec3, Vec4, Mat4 (16 row-major f32s), LookAt, Perspective, Ortho,
// MulMat4, TRS transforms, and byte-buffer packing helpers.
//
// How: pure f32/f64 math using std.math and std.mem; row-major Mat4 matches
// DXC's -Zpr matrix layout on both D3D12 and Vulkan.

import std.math
import std.mem

/// A 3D vector.
export struct Vec3 {
    export x: f32
    export y: f32
    export z: f32
}

/// A 4D vector.
export struct Vec4 {
    export x: f32
    export y: f32
    export z: f32
    export w: f32
}

/// A 4x4 row-major matrix of f32 values.
export struct Mat4 {
    export m: [16]f32
}

/// Constructs a Vec3. O(1); allocates nothing.
export V3(x: f32, y: f32, z: f32): Vec3 {
    return Vec3(x: x, y: y, z: z)
}

/// Constructs a Vec4. O(1); allocates nothing.
export V4(x: f32, y: f32, z: f32, w: f32): Vec4 {
    return Vec4(x: x, y: y, z: z, w: w)
}

/// Adds two Vec3 values. O(1); allocates nothing.
export Add3(a: Vec3, b: Vec3): Vec3 {
    return Vec3(x: a.x + b.x, y: a.y + b.y, z: a.z + b.z)
}

/// Subtracts b from a. O(1); allocates nothing.
export Sub3(a: Vec3, b: Vec3): Vec3 {
    return Vec3(x: a.x - b.x, y: a.y - b.y, z: a.z - b.z)
}

/// Scales a Vec3 by s. O(1); allocates nothing.
export Scale3(a: Vec3, s: f32): Vec3 {
    return Vec3(x: a.x * s, y: a.y * s, z: a.z * s)
}

/// Dot product of two Vec3 values. O(1); allocates nothing.
export Dot3(a: Vec3, b: Vec3): f32 {
    return a.x * b.x + a.y * b.y + a.z * b.z
}

/// Cross product of two Vec3 values. O(1); allocates nothing.
export Cross3(a: Vec3, b: Vec3): Vec3 {
    return Vec3(
        x: a.y * b.z - a.z * b.y,
        y: a.z * b.x - a.x * b.z,
        z: a.x * b.y - a.y * b.x,
    )
}

/// Squared length of v. O(1); allocates nothing.
export LenSq3(v: Vec3): f32 {
    return Dot3(v, v)
}

/// Unit-length vector in the direction of v, or (0, 1, 0) if near zero. O(1); allocates nothing.
export Normalize3(v: Vec3): Vec3 {
    len_sq := LenSq3(v)
    if len_sq <= 1.0e-12 {
        return Vec3(x: 0.0, y: 1.0, z: 0.0)
    }
    inv := 1.0 / math.SqrtF32(len_sq)
    return Scale3(v, inv)
}

/// The 4x4 identity matrix. O(1); allocates nothing.
export Identity(): Mat4 {
    return Mat4(m: [
        1.0, 0.0, 0.0, 0.0,
        0.0, 1.0, 0.0, 0.0,
        0.0, 0.0, 1.0, 0.0,
        0.0, 0.0, 0.0, 1.0,
    ])
}

/// Multiplies two row-major 4x4 matrices (a * b). O(1); allocates nothing.
export MulMat4(a: Mat4, b: Mat4): Mat4 {
    var out: [16]f32 = [0.0; 16]
    for r in 0..4 {
        for c in 0..4 {
            var acc: f32 = 0.0
            for k in 0..4 {
                acc += a.m[r * 4 + k] * b.m[k * 4 + c]
            }
            out[r * 4 + c] = acc
        }
    }
    return Mat4(m: out)
}

/// Translation matrix by (tx, ty, tz) for column-vector multiplication mul(M, v). O(1); allocates nothing.
export Translate(tx: f32, ty: f32, tz: f32): Mat4 {
    return Mat4(m: [
        1.0, 0.0, 0.0, tx,
        0.0, 1.0, 0.0, ty,
        0.0, 0.0, 1.0, tz,
        0.0, 0.0, 0.0, 1.0,
    ])
}

/// Uniform scale matrix by (sx, sy, sz). O(1); allocates nothing.
export ScaleMat(sx: f32, sy: f32, sz: f32): Mat4 {
    return Mat4(m: [
        sx, 0.0, 0.0, 0.0,
        0.0, sy, 0.0, 0.0,
        0.0, 0.0, sz, 0.0,
        0.0, 0.0, 0.0, 1.0,
    ])
}

/// Rotation matrix around +Y by angle_deg degrees. O(1); allocates nothing.
export RotateY(angle_deg: f32): Mat4 {
    rad := angle_deg * (3.14159265 / 180.0)
    c := math.CosF32(rad)
    s := math.SinF32(rad)
    return Mat4(m: [
        c, 0.0, s, 0.0,
        0.0, 1.0, 0.0, 0.0,
        -s, 0.0, c, 0.0,
        0.0, 0.0, 0.0, 1.0,
    ])
}

/// Rotation matrix around +X by angle_deg degrees. O(1); allocates nothing.
export RotateX(angle_deg: f32): Mat4 {
    rad := angle_deg * (3.14159265 / 180.0)
    c := math.CosF32(rad)
    s := math.SinF32(rad)
    return Mat4(m: [
        1.0, 0.0, 0.0, 0.0,
        0.0, c, -s, 0.0,
        0.0, s, c, 0.0,
        0.0, 0.0, 0.0, 1.0,
    ])
}

/// Builds a model matrix T * R_y * S. O(1); allocates nothing.
export ModelMatrix(tx: f32, ty: f32, tz: f32, rot_y_deg: f32, scale: f32): Mat4 {
    t := Translate(tx, ty, tz)
    r := RotateY(rot_y_deg)
    s := ScaleMat(scale, scale, scale)
    return MulMat4(t, MulMat4(r, s))
}

/// Right-handed LookAt view matrix for column vectors. O(1); allocates nothing.
export LookAt(eye: Vec3, target: Vec3, up: Vec3): Mat4 {
    f := Normalize3(Sub3(target, eye))
    s := Normalize3(Cross3(f, up))
    u := Cross3(s, f)
    return Mat4(m: [
        s.x, s.y, s.z, -Dot3(s, eye),
        u.x, u.y, u.z, -Dot3(u, eye),
        -f.x, -f.y, -f.z, Dot3(f, eye),
        0.0, 0.0, 0.0, 1.0,
    ])
}

/// Right-handed perspective projection with D3D depth range [0, 1]. O(1); allocates nothing.
export Perspective(fov_deg: f32, aspect: f32, near: f32, far: f32): Mat4 {
    rad := fov_deg * (3.14159265 / 180.0)
    half_tan := math.TanF32(rad * 0.5)
    f := if half_tan > 1.0e-6 { 1.0 / half_tan } else { 1.0 }
    a := if aspect > 1.0e-6 { aspect } else { 1.0 }
    q := far / (near - far)
    return Mat4(m: [
        f / a, 0.0, 0.0, 0.0,
        0.0, f, 0.0, 0.0,
        0.0, 0.0, q, near * q,
        0.0, 0.0, -1.0, 0.0,
    ])
}

/// Right-handed orthographic projection with D3D depth range [0, 1]. O(1); allocates nothing.
export Ortho(left: f32, right: f32, bottom: f32, top: f32, near: f32, far: f32): Mat4 {
    rl := right - left
    tb := top - bottom
    nf := near - far
    return Mat4(m: [
        2.0 / rl, 0.0, 0.0, -(right + left) / rl,
        0.0, 2.0 / tb, 0.0, -(top + bottom) / tb,
        0.0, 0.0, 1.0 / nf, near / nf,
        0.0, 0.0, 0.0, 1.0,
    ])
}

/// Writes a little-endian u32 into dst starting at byte_offset. O(1); allocates nothing.
export PutU32(dst: mut []u8, byte_offset: int, v: u32) {
    dst[byte_offset] = Trunc[u8](v & 0xFF)
    dst[byte_offset + 1] = Trunc[u8]((v >> 8) & 0xFF)
    dst[byte_offset + 2] = Trunc[u8]((v >> 16) & 0xFF)
    dst[byte_offset + 3] = Trunc[u8]((v >> 24) & 0xFF)
}

/// Writes an f32 as little-endian IEEE 754 bytes into dst at byte_offset. O(1); allocates nothing.
export PutF32(dst: mut []u8, byte_offset: int, v: f32) {
    var x := v
    b := mem.AsBytes(&x)
    dst[byte_offset] = b[0]
    dst[byte_offset + 1] = b[1]
    dst[byte_offset + 2] = b[2]
    dst[byte_offset + 3] = b[3]
}

/// Writes a Mat4 (64 bytes, row-major) into dst at byte_offset. O(1); allocates nothing.
export PutMat4(dst: mut []u8, byte_offset: int, m: Mat4) {
    for i in 0..16 {
        PutF32(dst, byte_offset + i * 4, m.m[i])
    }
}

/// Writes a Vec4 (16 bytes) into dst at byte_offset. O(1); allocates nothing.
export PutVec4(dst: mut []u8, byte_offset: int, v: Vec4) {
    PutF32(dst, byte_offset, v.x)
    PutF32(dst, byte_offset + 4, v.y)
    PutF32(dst, byte_offset + 8, v.z)
    PutF32(dst, byte_offset + 12, v.w)
}

/// Rounds n up to a multiple of align (power of two). O(1); allocates nothing.
export AlignUp(n: int, align: int): int {
    return (n + align - 1) & ~(align - 1)
}
