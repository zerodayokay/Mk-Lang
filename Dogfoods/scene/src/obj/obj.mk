// Why: 3D scenes need indexed triangle meshes loaded from Wavefront OBJ files
// with positions, normals, and UVs ready for vertex/index GPU buffers.
//
// What: Vertex, Mesh, Error, ParseObj, PackVertices and PackIndices.
//
// How: parses v, vt, vn, and f lines (triangles or convex polygon fans, with
// v, v/t, v//n, or v/t/n indices including negative relative indices); builds
// face normals automatically when vn is absent.

import std.math
import src.math3d

/// A single GPU mesh vertex (32 bytes: pos f32x3, norm f32x3, uv f32x2).
export struct Vertex {
    export px: f32
    export py: f32
    export pz: f32
    export nx: f32
    export ny: f32
    export nz: f32
    export u: f32
    export v: f32
}

/// An indexed triangle mesh in memory.
export struct Mesh {
    export vertices: List[Vertex]
    export indices: List[u32]
}

/// Errors returned while parsing an OBJ file.
export open type Error = BadObj

/// Malformed OBJ syntax or out-of-range face index.
export error BadObj(line: int, msg: str)

const ByteSpace: u8 = 32
const ByteTab: u8 = 9
const ByteCr: u8 = 13
const ByteSlash: u8 = 47

SplitWs(a: &Arena, line: str into a): OutOfMemory!List[str] from a, line {
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

ParseF32Tok(s: str, line_no: int): Error!f32 {
    v := s.ParseFloat() else {
        fail BadObj(line: line_no, msg: "invalid float")
    }
    if math.IsNan(v) || math.IsInf(v) {
        fail BadObj(line: line_no, msg: "non-finite float")
    }
    return Cast[f32](v)
}

ResolveIdx(raw: int, count: int, line_no: int): Error!int {
    idx := if raw > 0 { raw - 1 } else if raw < 0 { count + raw } else { -1 }
    if idx < 0 || idx >= count {
        fail BadObj(line: line_no, msg: "index out of range")
    }
    return idx
}

struct Corner {
    vi: int
    ti: int
    ni: int
}

ParseCorner(tok: str, nv: int, nt: int, nn: int, line_no: int): Error!Corner {
    var s1 := -1
    var s2 := -1
    for i in 0..tok.Len() {
        if tok.Byte(i) == ByteSlash {
            if s1 < 0 {
                s1 = i
            } else if s2 < 0 {
                s2 = i
            } else {
                fail BadObj(line: line_no, msg: "too many slashes in face corner")
            }
        }
    }
    v_str := if s1 < 0 { tok } else { tok[0..s1] }
    v_raw := v_str.ParseInt() else {
        fail BadObj(line: line_no, msg: "invalid vertex index")
    }
    vi := try ResolveIdx(int(v_raw), nv, line_no)
    var ti := -1
    var ni := -1
    if s1 >= 0 {
        t_str := if s2 < 0 { tok[s1 + 1..tok.Len()] } else { tok[s1 + 1..s2] }
        if t_str.Len() > 0 {
            t_raw := t_str.ParseInt() else {
                fail BadObj(line: line_no, msg: "invalid texcoord index")
            }
            ti = try ResolveIdx(int(t_raw), nt, line_no)
        }
        if s2 >= 0 {
            n_str := tok[s2 + 1..tok.Len()]
            if n_str.Len() > 0 {
                n_raw := n_str.ParseInt() else {
                    fail BadObj(line: line_no, msg: "invalid normal index")
                }
                ni = try ResolveIdx(int(n_raw), nn, line_no)
            }
        }
    }
    return Corner(vi: vi, ti: ti, ni: ni)
}

/// Parses Wavefront OBJ text into an indexed Mesh allocated in a.
/// Errors: Error (BadObj), OutOfMemory. O(bytes); allocates lists in a.
export ParseObj(a: &Arena, src: str): Error|OutOfMemory!Mesh from a {
    var pos := List[math3d.Vec3].New(a)
    var uvs := List[(u: f32, v: f32)].New(a)
    var norms := List[math3d.Vec3].New(a)
    var verts := List[Vertex].New(a)
    var indices := List[u32].New(a)

    var sub := try a.Sub()
    defer sub.Free()

    var line_no := 0
    for raw_line in src.Lines() {
        line_no += 1
        sub.Reset()
        hash_pos := raw_line.Find("#")
        line := match hash_pos {
            some(k) { raw_line[0..k] }
            none { raw_line }
        }
        toks := try SplitWs(&sub, line)
        if toks.Len() == 0 {
            continue
        }
        head := toks[0]
        if head.Eq(&"v") {
            if toks.Len() < 4 {
                fail BadObj(line: line_no, msg: "v needs 3 coordinates")
            }
            x := try ParseF32Tok(toks[1], line_no)
            y := try ParseF32Tok(toks[2], line_no)
            z := try ParseF32Tok(toks[3], line_no)
            try pos.Push(math3d.V3(x, y, z))
        } else if head.Eq(&"vt") {
            if toks.Len() < 3 {
                fail BadObj(line: line_no, msg: "vt needs 2 coordinates")
            }
            u := try ParseF32Tok(toks[1], line_no)
            v := try ParseF32Tok(toks[2], line_no)
            try uvs.Push((u: u, v: 1.0 - v))
        } else if head.Eq(&"vn") {
            if toks.Len() < 4 {
                fail BadObj(line: line_no, msg: "vn needs 3 coordinates")
            }
            x := try ParseF32Tok(toks[1], line_no)
            y := try ParseF32Tok(toks[2], line_no)
            z := try ParseF32Tok(toks[3], line_no)
            try norms.Push(math3d.Normalize3(math3d.V3(x, y, z)))
        } else if head.Eq(&"f") {
            if toks.Len() < 4 {
                fail BadObj(line: line_no, msg: "f needs at least 3 corners")
            }
            var corners := List[Corner].New(&sub)
            for k in 1..toks.Len() {
                c := try ParseCorner(toks[k], pos.Len(), uvs.Len(), norms.Len(), line_no)
                try corners.Push(c)
            }
            for k in 1..(corners.Len() - 1) {
                c0 := corners[0]
                c1 := corners[k]
                c2 := corners[k + 1]
                p0 := pos[c0.vi]
                p1 := pos[c1.vi]
                p2 := pos[c2.vi]
                face_n := math3d.Normalize3(math3d.Cross3(math3d.Sub3(p1, p0), math3d.Sub3(p2, p0)))
                tri := [c0, c1, c2]
                for ci in 0..3 {
                    cc := tri[ci]
                    p := pos[cc.vi]
                    n := if cc.ni >= 0 { norms[cc.ni] } else { face_n }
                    uv := if cc.ti >= 0 { uvs[cc.ti] } else { (u: 0.0, v: 0.0) }
                    base_idx := verts.Len()
                    try verts.Push(Vertex(
                        px: p.x, py: p.y, pz: p.z,
                        nx: n.x, ny: n.y, nz: n.z,
                        u: uv.u, v: uv.v,
                    ))
                    try indices.Push(Trunc[u32](base_idx))
                }
            }
        }
    }
    if verts.Len() == 0 || indices.Len() == 0 {
        fail BadObj(line: line_no, msg: "mesh has no faces")
    }
    return Mesh(vertices: verts, indices: indices)
}

/// Serializes mesh vertices into packed 32-byte Vertex records in a.
/// Errors: OutOfMemory. O(vertices); allocates byte slice in a.
export PackVertices(a: &Arena, m: &Mesh): OutOfMemory![]u8 from a {
    n := m.vertices.Len()
    buf := try a.Slice[u8](n * 32)
    for i in 0..n {
        v := m.vertices[i]
        o := i * 32
        math3d.PutF32(buf, o, v.px)
        math3d.PutF32(buf, o + 4, v.py)
        math3d.PutF32(buf, o + 8, v.pz)
        math3d.PutF32(buf, o + 12, v.nx)
        math3d.PutF32(buf, o + 16, v.ny)
        math3d.PutF32(buf, o + 20, v.nz)
        math3d.PutF32(buf, o + 24, v.u)
        math3d.PutF32(buf, o + 28, v.v)
    }
    return buf[..]
}

/// Serializes mesh indices into packed little-endian u32 bytes in a.
/// Errors: OutOfMemory. O(indices); allocates byte slice in a.
export PackIndices(a: &Arena, m: &Mesh): OutOfMemory![]u8 from a {
    n := m.indices.Len()
    buf := try a.Slice[u8](n * 4)
    for i in 0..n {
        math3d.PutU32(buf, i * 4, m.indices[i])
    }
    return buf[..]
}
