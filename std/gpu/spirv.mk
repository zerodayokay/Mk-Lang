// Why: SPIR-V is the single shader interface for both D3D12 and Vulkan, and
// shader blobs may arrive from untrusted files or callers.
//
// What: bounded O(words) SPIR-V parser and validator (CheckSpirv and
// ReflectSpirv), checking header, logical layout, ID bounds, NUL-terminated
// strings, iterative type layout (depth <= 64), duplicate bindings, counter
// buffers, combined samplers, push-constant row straddling, and dynamic array
// non-constant indexing.
//
// How: two-pass zero-panic walker over little-endian SPIR-V words; CheckShader
// runs with fixed stack tables (allocates nothing), while NewShader allocates
// reflection data onto the device heap.

import std.mem

const SpirvMagic: u32 = 0x07230203
const SpirvMaxBound: u32 = 4194304
const SpirvMaxTypeDepth: int = 64

// SPIR-V opcodes
const SpvOpSourceContinued: u16 = 2
const SpvOpSource: u16 = 3
const SpvOpSourceExtension: u16 = 4
const SpvOpName: u16 = 5
const SpvOpMemberName: u16 = 6
const SpvOpString: u16 = 7
const SpvOpExtension: u16 = 10
const SpvOpExtInstImport: u16 = 11
const SpvOpMemoryModel: u16 = 14
const SpvOpEntryPoint: u16 = 15
const SpvOpExecutionMode: u16 = 16
const SpvOpCapability: u16 = 17
const SpvOpTypeVoid: u16 = 19
const SpvOpTypeBool: u16 = 20
const SpvOpTypeInt: u16 = 21
const SpvOpTypeFloat: u16 = 22
const SpvOpTypeVector: u16 = 23
const SpvOpTypeMatrix: u16 = 24
const SpvOpTypeImage: u16 = 25
const SpvOpTypeSampler: u16 = 26
const SpvOpTypeSampledImage: u16 = 27
const SpvOpTypeArray: u16 = 28
const SpvOpTypeRuntimeArray: u16 = 29
const SpvOpTypeStruct: u16 = 30
const SpvOpTypePointer: u16 = 32
const SpvOpConstantTrue: u16 = 41
const SpvOpConstantFalse: u16 = 42
const SpvOpConstant: u16 = 43
const SpvOpConstantComposite: u16 = 44
const SpvOpConstantNull: u16 = 46
const SpvOpSpecConstantTrue: u16 = 48
const SpvOpSpecConstantFalse: u16 = 49
const SpvOpSpecConstant: u16 = 50
const SpvOpSpecConstantComposite: u16 = 51
const SpvOpSpecConstantOp: u16 = 52
const SpvOpFunction: u16 = 54
const SpvOpFunctionEnd: u16 = 56
const SpvOpVariable: u16 = 59
const SpvOpAccessChain: u16 = 65
const SpvOpInBoundsAccessChain: u16 = 66
const SpvOpPtrAccessChain: u16 = 67
const SpvOpDecorate: u16 = 71
const SpvOpMemberDecorate: u16 = 72
const SpvOpDecorationGroup: u16 = 73
const SpvOpGroupDecorate: u16 = 74
const SpvOpGroupMemberDecorate: u16 = 75
const SpvOpModuleProcessed: u16 = 330
const SpvOpExecutionModeId: u16 = 331
const SpvOpDecorateId: u16 = 332
const SpvOpDecorateString: u16 = 5632
const SpvOpMemberDecorateString: u16 = 5633

// Storage classes
const SpvScUniformConstant: u32 = 0
const SpvScInput: u32 = 1
const SpvScUniform: u32 = 2
const SpvScOutput: u32 = 3
const SpvScPushConstant: u32 = 9
const SpvScStorageBuffer: u32 = 12

// Decorations
const SpvDecBlock: u32 = 2
const SpvDecBufferBlock: u32 = 3
const SpvDecRowMajor: u32 = 4
const SpvDecColMajor: u32 = 5
const SpvDecArrayStride: u32 = 6
const SpvDecMatrixStride: u32 = 7
const SpvDecBuiltIn: u32 = 11
const SpvDecNonWritable: u32 = 24
const SpvDecLocation: u32 = 30
const SpvDecBinding: u32 = 33
const SpvDecDescriptorSet: u32 = 34
const SpvDecOffset: u32 = 35
const SpvDecCounterBuffer: u32 = 5634

// Execution models / modes / caps
const SpvExecVertex: u32 = 0
const SpvExecFragment: u32 = 4
const SpvExecGlCompute: u32 = 5
const SpvModeLocalSize: u32 = 17
const SpvCapDrawParameters: u32 = 4427

const SpvBindUniform: u8 = 1
const SpvBindStorage: u8 = 2
const SpvBindTexture: u8 = 3
const SpvBindSampler: u8 = 4

struct SpvBinding {
    set: u32
    binding: u32
    kind: u8
    read_only: bool
    block_size: int
    dim: ViewDimension
    multisampled: bool
    sample_class: u8
    is_block_array: bool
    non_const_index: bool
}

struct SpvVertexInput {
    location: int
    num_class: u8
}

struct SpirvReflection {
    stage: Stage
    uses_draw_parameters: bool
    local_x: int
    local_y: int
    local_z: int
    push_size: int
    bindings: HeapList[SpvBinding]
    inputs: HeapList[SpvVertexInput]
}

SpirvReflection.Free(self, a: &Alloc) {
    SpirvReflection(stage: _, uses_draw_parameters: _, local_x: _, local_y: _, local_z: _, push_size: _, bindings, inputs) := self
    bindings.Free(a)
    inputs.Free(a)
}

ReadU32Le(b: []u8, byte_off: int): u32 {
    return u32(b[byte_off]) | (u32(b[byte_off + 1]) << 8) | (u32(b[byte_off + 2]) << 16) | (u32(b[byte_off + 3]) << 24)
}

ReadWord(b: []u8, word_idx: int): u32 {
    return ReadU32Le(b, word_idx * 4)
}

// Checks NUL-terminated UTF-8-like string starting at word_idx within [word_idx, end_word).
// Returns word index right after the string, or -1 if malformed.
SpvReadStringEnd(b: []u8, word_idx: int, end_word: int): int {
    var wi := word_idx
    for wi < end_word {
        w := ReadWord(b, wi)
        b0 := w & 0xFF
        b1 := (w >> 8) & 0xFF
        b2 := (w >> 16) & 0xFF
        b3 := (w >> 24) & 0xFF
        if b0 == 0 || b1 == 0 || b2 == 0 || b3 == 0 {
            return wi + 1
        }
        wi += 1
    }
    return -1
}

SpvStringEq(b: []u8, word_idx: int, end_word: int, target: str): bool {
    tb := target.AsBytes()
    tlen := tb.Len()
    max_bytes := (end_word - word_idx) * 4
    if tlen >= max_bytes {
        return false
    }
    base := word_idx * 4
    for i in 0..tlen {
        if b[base + i] != tb[i] {
            return false
        }
    }
    return b[base + tlen] == 0
}

SpvStageToExecModel(s: Stage): u32 {
    return match s {
        .Vertex { SpvExecVertex }
        .Fragment { SpvExecFragment }
        .Compute { SpvExecGlCompute }
    }
}

SpvHasResultId(op: u16): bool {
    return match op {
        11 | 19 | 20 | 21 | 22 | 23 | 24 | 25 | 26 | 27 | 28 | 29 | 30 | 32 |
            41 | 42 | 43 | 44 | 46 | 48 | 49 | 50 | 51 | 52 | 54 | 59 | 65 | 66 | 67 |
            7 | 73 { true }
        _ { false }
    }
}

SpvResultIdWord(op: u16): int {
    // OpString(7), OpExtInstImport(11), OpType*(19..39), OpDecorationGroup(73) have result ID at word 1.
    // OpConstant*(41..52), OpFunction(54), OpVariable(59), OpAccessChain(65..67) have result ID at word 2.
    if (op >= 19 && op <= 39) || op == 7 || op == 11 || op == 73 {
        return 1
    }
    return 2
}

SpvTypeIdWord(op: u16): bool {
    return op >= 19 && op <= 39
}

// Compact preamble record stored in a fixed-size or heap-backed table.
const SpvHashCap: int = 512

struct SpvTypeRec {
    id: u32
    op: u16
    w1: u32
    w2: u32
    w3: u32
    w4: u32
    w5: u32
    w6: u32
    word_pos: int
    wc: int
}

struct SpvVarRec {
    id: u32
    ptr_type: u32
    sc: u32
    set: u32
    binding: u32
    has_set: bool
    has_binding: bool
    location: i32
    has_builtin: bool
    non_writable: bool
    non_const_index: bool
}

struct SpvDecRec {
    target: u32
    member: i32
    dec: u32
    val: u32
}

struct SpvScratch {
    types: [512]SpvTypeRec
    type_count: int
    type_hash: [1024]i16
    vars: [64]SpvVarRec
    var_count: int
    decs: [1024]SpvDecRec
    dec_count: int
    const_ids: [256]u32
    const_vals: [256]u32
    const_count: int
    const_hash: [512]i16
}

SpvHashU32(x: u32, mask: int): int {
    h := x ^ (x >> 16)
    return int(h) & mask
}

SpvScratchInit(s: mut SpvScratch) {
    s.type_count = 0
    for i in 0..1024 {
        s.type_hash[i] = -1
    }
    s.var_count = 0
    s.dec_count = 0
    s.const_count = 0
    for i in 0..512 {
        s.const_hash[i] = -1
    }
}

SpvAddType(s: mut SpvScratch, rec: SpvTypeRec): Invalid!void {
    if s.type_count >= 512 {
        fail Invalid(misuse: Misuse.LimitExceeded, at: rec.word_pos)
    }
    idx := s.type_count
    s.types[idx] = rec
    s.type_count += 1
    var h := SpvHashU32(rec.id, 1023)
    for _ in 0..1024 {
        if s.type_hash[h] < 0 {
            s.type_hash[h] = Cast[i16](idx)
            return
        }
        h = (h + 1) & 1023
    }
}

SpvFindType(s: &SpvScratch, id: u32): int {
    var h := SpvHashU32(id, 1023)
    for _ in 0..1024 {
        slot := int(s.type_hash[h])
        if slot < 0 {
            return -1
        }
        if s.types[slot].id == id {
            return slot
        }
        h = (h + 1) & 1023
    }
    return -1
}

SpvAddConst(s: mut SpvScratch, id: u32, val: u32) {
    if s.const_count >= 256 {
        return
    }
    idx := s.const_count
    s.const_ids[idx] = id
    s.const_vals[idx] = val
    s.const_count += 1
    var h := SpvHashU32(id, 511)
    for _ in 0..512 {
        if s.const_hash[h] < 0 {
            s.const_hash[h] = Cast[i16](idx)
            return
        }
        h = (h + 1) & 511
    }
}

SpvFindConst(s: &SpvScratch, id: u32): ?u32 {
    var h := SpvHashU32(id, 511)
    for _ in 0..512 {
        slot := int(s.const_hash[h])
        if slot < 0 {
            return none
        }
        if s.const_ids[slot] == id {
            return some(s.const_vals[slot])
        }
        h = (h + 1) & 511
    }
    return none
}

SpvHasDec(s: &SpvScratch, target: u32, dec: u32): bool {
    for i in 0..s.dec_count {
        d := &s.decs[i]
        if d.target == target && d.member < 0 && d.dec == dec {
            return true
        }
    }
    return false
}

SpvGetDecVal(s: &SpvScratch, target: u32, dec: u32): ?u32 {
    for i in 0..s.dec_count {
        d := &s.decs[i]
        if d.target == target && d.member < 0 && d.dec == dec {
            return some(d.val)
        }
    }
    return none
}

SpvGetMemberDecVal(s: &SpvScratch, target: u32, member: int, dec: u32): ?u32 {
    for i in 0..s.dec_count {
        d := &s.decs[i]
        if d.target == target && int(d.member) == member && d.dec == dec {
            return some(d.val)
        }
    }
    return none
}

SpvHasMemberDec(s: &SpvScratch, target: u32, member: int, dec: u32): bool {
    return SpvGetMemberDecVal(s, target, member, dec).IsSome()
}

// Iterative type byte size calculation with explicit stack (depth <= 64).
struct SpvSizeFrame {
    type_id: u32
    step: int
    acc_size: int
    member_idx: int
}

SpvTypeSize(b: []u8, s: &SpvScratch, root_id: u32): Invalid!int {
    var stack: [64]SpvSizeFrame = [SpvSizeFrame(type_id: 0, step: 0, acc_size: 0, member_idx: 0); 64]
    var sp := 0
    stack[0] = SpvSizeFrame(type_id: root_id, step: 0, acc_size: 0, member_idx: 0)
    var last_result := 0
    for sp >= 0 {
        ti := SpvFindType(s, stack[sp].type_id)
        if ti < 0 {
            fail Invalid(misuse: Misuse.BadShader, at: -1)
        }
        t := &s.types[ti]
        if t.op == SpvOpTypeBool {
            last_result = 4
            sp -= 1
            continue
        }
        if t.op == SpvOpTypeInt || t.op == SpvOpTypeFloat {
            w := int(t.w1)
            if w <= 0 || w > 64 || w % 8 != 0 {
                fail Invalid(misuse: Misuse.BadShader, at: t.word_pos)
            }
            last_result = w / 8
            sp -= 1
            continue
        }
        if t.op == SpvOpTypeVector {
            if stack[sp].step == 0 {
                if sp + 1 >= SpirvMaxTypeDepth {
                    fail Invalid(misuse: Misuse.BadShader, at: t.word_pos)
                }
                stack[sp].step = 1
                sp += 1
                stack[sp] = SpvSizeFrame(type_id: t.w1, step: 0, acc_size: 0, member_idx: 0)
                continue
            }
            cnt := int(t.w2)
            if cnt < 2 || cnt > 4 {
                fail Invalid(misuse: Misuse.BadShader, at: t.word_pos)
            }
            last_result = last_result * cnt
            sp -= 1
            continue
        }
        if t.op == SpvOpTypeMatrix {
            if stack[sp].step == 0 {
                if sp + 1 >= SpirvMaxTypeDepth {
                    fail Invalid(misuse: Misuse.BadShader, at: t.word_pos)
                }
                stack[sp].step = 1
                sp += 1
                stack[sp] = SpvSizeFrame(type_id: t.w1, step: 0, acc_size: 0, member_idx: 0)
                continue
            }
            cols := int(t.w2)
            if cols < 2 || cols > 4 {
                fail Invalid(misuse: Misuse.BadShader, at: t.word_pos)
            }
            // Conservative 16-byte stride per column/row when not overridden by MatrixStride.
            col_sz := if last_result < 16 { 16 } else { last_result }
            last_result = col_sz * cols
            sp -= 1
            continue
        }
        if t.op == SpvOpTypeArray {
            if stack[sp].step == 0 {
                if sp + 1 >= SpirvMaxTypeDepth {
                    fail Invalid(misuse: Misuse.BadShader, at: t.word_pos)
                }
                stack[sp].step = 1
                sp += 1
                stack[sp] = SpvSizeFrame(type_id: t.w1, step: 0, acc_size: 0, member_idx: 0)
                continue
            }
            arr_len := SpvFindConst(s, t.w2) else { u32(1) }
            stride := int(SpvGetDecVal(s, t.id, SpvDecArrayStride) else { Trunc[u32](last_result) })
            last_result = stride * int(arr_len)
            sp -= 1
            continue
        }
        if t.op == SpvOpTypeRuntimeArray {
            if stack[sp].step == 0 {
                if sp + 1 >= SpirvMaxTypeDepth {
                    fail Invalid(misuse: Misuse.BadShader, at: t.word_pos)
                }
                stack[sp].step = 1
                sp += 1
                stack[sp] = SpvSizeFrame(type_id: t.w1, step: 0, acc_size: 0, member_idx: 0)
                continue
            }
            stride := int(SpvGetDecVal(s, t.id, SpvDecArrayStride) else { Trunc[u32](last_result) })
            last_result = stride
            sp -= 1
            continue
        }
        if t.op == SpvOpTypeStruct {
            mcount := t.wc - 2
            if stack[sp].step == 0 {
                if mcount == 0 {
                    last_result = 0
                    sp -= 1
                    continue
                }
                if sp + 1 >= SpirvMaxTypeDepth {
                    fail Invalid(misuse: Misuse.BadShader, at: t.word_pos)
                }
                stack[sp].step = 1
                stack[sp].member_idx = 0
                stack[sp].acc_size = 0
                mid := ReadWord(b, t.word_pos + 2)
                sp += 1
                stack[sp] = SpvSizeFrame(type_id: mid, step: 0, acc_size: 0, member_idx: 0)
                continue
            }
            mi := stack[sp].member_idx
            moff := int(SpvGetMemberDecVal(s, t.id, mi, SpvDecOffset) else { Trunc[u32](stack[sp].acc_size) })
            msz := last_result
            end_off := moff + msz
            if end_off > stack[sp].acc_size {
                stack[sp].acc_size = end_off
            }
            next_mi := mi + 1
            if next_mi >= mcount {
                last_result = stack[sp].acc_size
                sp -= 1
                continue
            }
            if sp + 1 >= SpirvMaxTypeDepth {
                fail Invalid(misuse: Misuse.BadShader, at: t.word_pos)
            }
            stack[sp].member_idx = next_mi
            mid := ReadWord(b, t.word_pos + 2 + next_mi)
            sp += 1
            stack[sp] = SpvSizeFrame(type_id: mid, step: 0, acc_size: 0, member_idx: 0)
            continue
        }
        last_result = 0
        sp -= 1
    }
    return last_result
}

// Checks push-constant block rules: no array or struct members, and no member
// straddles a 16-byte row.
SpvCheckPushConstant(b: []u8, s: &SpvScratch, struct_id: u32): Invalid!int {
    ti := SpvFindType(s, struct_id)
    if ti < 0 || s.types[ti].op != SpvOpTypeStruct {
        fail Invalid(misuse: Misuse.ShaderMismatch, at: -1)
    }
    t := &s.types[ti]
    mcount := t.wc - 2
    var max_end := 0
    for mi in 0..mcount {
        mid := ReadWord(b, t.word_pos + 2 + mi)
        mti := SpvFindType(s, mid)
        if mti < 0 {
            fail Invalid(misuse: Misuse.BadShader, at: t.word_pos)
        }
        mt := &s.types[mti]
        if mt.op == SpvOpTypeArray || mt.op == SpvOpTypeRuntimeArray || mt.op == SpvOpTypeStruct {
            fail Invalid(misuse: Misuse.ShaderMismatch, at: mt.word_pos)
        }
        off := int(SpvGetMemberDecVal(s, struct_id, mi, SpvDecOffset) else { Trunc[u32](max_end) })
        msz := try SpvTypeSize(b, s, mid)
        if msz > 0 {
            if mt.op == SpvOpTypeMatrix {
                cols := int(mt.w2)
                mstride := int(SpvGetMemberDecVal(s, struct_id, mi, SpvDecMatrixStride) else { u32(16) })
                col_sz := try SpvTypeSize(b, s, mt.w1)
                if mstride <= 0 || col_sz <= 0 || col_sz > 16 {
                    fail Invalid(misuse: Misuse.ShaderMismatch, at: mt.word_pos)
                }
                for c in 0..cols {
                    coff := off + c * mstride
                    if (coff / 16) != ((coff + col_sz - 1) / 16) {
                        fail Invalid(misuse: Misuse.ShaderMismatch, at: mt.word_pos)
                    }
                }
            } else {
                if (off / 16) != ((off + msz - 1) / 16) {
                    fail Invalid(misuse: Misuse.ShaderMismatch, at: mt.word_pos)
                }
            }
        }
        if off + msz > max_end {
            max_end = off + msz
        }
    }
    return max_end
}

SpvScalarClass(s: &SpvScratch, type_id: u32): u8 {
    var cur := type_id
    for _ in 0..8 {
        ti := SpvFindType(s, cur)
        if ti < 0 {
            return NumClassFloat
        }
        t := &s.types[ti]
        if t.op == SpvOpTypeVector || t.op == SpvOpTypeArray {
            cur = t.w1
            continue
        }
        if t.op == SpvOpTypeInt {
            return if t.w2 != 0 { NumClassSint } else { NumClassUint }
        }
        return NumClassFloat
    }
    return NumClassFloat
}

SpvImageDim(dim: u32, arrayed: u32): ?ViewDimension {
    if dim == 1 {
        return if arrayed != 0 { some(ViewDimension.D2Array) } else { some(ViewDimension.D2) }
    }
    if dim == 3 {
        return if arrayed != 0 { some(ViewDimension.CubeArray) } else { some(ViewDimension.Cube) }
    }
    return none
}

// Logical layout section index (1..5), or 0 for error.
SpvSectionOf(op: u16): int {
    return match op {
        17 | 10 | 11 | 14 | 15 | 16 | 331 { 1 }
        7 | 2 | 3 | 4 | 5 | 6 | 330 { 2 }
        71 | 72 | 73 | 74 | 75 | 332 | 5632 | 5633 { 3 }
        19 | 20 | 21 | 22 | 23 | 24 | 25 | 26 | 27 | 28 | 29 | 30 | 32 |
            41 | 42 | 43 | 44 | 46 | 48 | 49 | 50 | 51 | 52 | 59 { 4 }
        _ { 5 }
    }
}

ValidateAndParseSpirv(b: []u8, stage: Stage, entry: str, a: ?&Alloc): Error!?SpirvReflection from a {
    if b.Len() < 20 || b.Len() % 4 != 0 {
        fail Invalid(misuse: Misuse.BadShader, at: -1)
    }
    magic := ReadWord(b, 0)
    if magic != SpirvMagic {
        fail Invalid(misuse: Misuse.BadShader, at: 0)
    }
    ver := ReadWord(b, 1)
    major := (ver >> 16) & 0xFF
    minor := (ver >> 8) & 0xFF
    if major != 1 || minor > 6 || (ver & 0xFF0000FF) != 0 {
        fail Invalid(misuse: Misuse.BadShader, at: 1)
    }
    bound := ReadWord(b, 3)
    if bound == 0 || bound > SpirvMaxBound {
        fail Invalid(misuse: Misuse.BadShader, at: 3)
    }
    if ReadWord(b, 4) != 0 {
        fail Invalid(misuse: Misuse.BadShader, at: 4)
    }

    var scr: SpvScratch = mem.Zeroed[SpvScratch]()
    SpvScratchInit(mut scr)

    total_words := b.Len() / 4
    want_model := SpvStageToExecModel(stage)
    var seen_any_entry := false
    var seen_stage_entry := false
    var seen_matching_entry := false
    var target_entry_id: u32 = 0
    var uses_draw_params := false
    var local_x := 1
    var local_y := 1
    var local_z := 1
    var seen_function := false
    var max_section := 1

    var pos := 5
    for pos < total_words {
        opword := ReadWord(b, pos)
        wc := int(opword >> 16)
        op := Trunc[u16](opword & 0xFFFF)
        if wc == 0 || pos + wc > total_words {
            fail Invalid(misuse: Misuse.BadShader, at: pos)
        }

        // Logical layout ordering: preamble sections 1..4 must precede OpFunction (section 5).
        if op == SpvOpFunction {
            seen_function = true
        }
        sec := SpvSectionOf(op)
        is_fn_var := op == SpvOpVariable && wc >= 4 && ReadWord(b, pos + 3) == 7
        if seen_function && sec < 5 && !is_fn_var {
            fail Invalid(misuse: Misuse.BadShader, at: pos)
        }
        if !seen_function {
            if sec == 1 && max_section > 1 {
                // OpExecutionMode(16) and OpEntryPoint(15) live in section 1.
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            if sec == 3 && max_section > 3 {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            if sec > max_section {
                max_section = sec
            }
        }

        // Check minimum word count and result ID bounds for known preamble ops.
        if SpvHasResultId(op) {
            id_w := SpvResultIdWord(op)
            if wc <= id_w {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            rid := ReadWord(b, pos + id_w)
            if rid == 0 || rid >= bound {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
        }

        if op == SpvOpCapability {
            if wc < 2 {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            if ReadWord(b, pos + 1) == SpvCapDrawParameters {
                uses_draw_params = true
            }
        } else if op == SpvOpExtension || op == SpvOpSourceExtension || op == SpvOpModuleProcessed {
            if wc < 2 || SpvReadStringEnd(b, pos + 1, pos + wc) < 0 {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
        } else if op == SpvOpExtInstImport || op == SpvOpString || op == SpvOpName {
            if wc < 3 || SpvReadStringEnd(b, pos + 2, pos + wc) < 0 {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            id0 := ReadWord(b, pos + 1)
            if id0 == 0 || id0 >= bound {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
        } else if op == SpvOpMemberName {
            if wc < 4 || SpvReadStringEnd(b, pos + 3, pos + wc) < 0 {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
        } else if op == SpvOpEntryPoint {
            if wc < 4 {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            model := ReadWord(b, pos + 1)
            eid := ReadWord(b, pos + 2)
            if eid == 0 || eid >= bound {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            name_end := SpvReadStringEnd(b, pos + 3, pos + wc)
            if name_end < 0 {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            // Remaining words are interface variable IDs.
            for wi in name_end..(pos + wc) {
                if ReadWord(b, wi) >= bound {
                    fail Invalid(misuse: Misuse.BadShader, at: pos)
                }
            }
            seen_any_entry = true
            if model == want_model {
                seen_stage_entry = true
                if SpvStringEq(b, pos + 3, name_end, entry) {
                    seen_matching_entry = true
                    target_entry_id = eid
                }
            }
        } else if op == SpvOpExecutionMode {
            if wc < 3 {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            eid := ReadWord(b, pos + 1)
            if eid >= bound {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            mode := ReadWord(b, pos + 2)
            if mode == SpvModeLocalSize {
                if wc < 6 {
                    fail Invalid(misuse: Misuse.BadShader, at: pos)
                }
                if !seen_matching_entry || eid == target_entry_id {
                    local_x = int(ReadWord(b, pos + 3))
                    local_y = int(ReadWord(b, pos + 4))
                    local_z = int(ReadWord(b, pos + 5))
                }
            }
        } else if op == SpvOpDecorate {
            if wc < 3 {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            tgt := ReadWord(b, pos + 1)
            if tgt >= bound {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            dec := ReadWord(b, pos + 2)
            if dec == SpvDecCounterBuffer {
                fail Invalid(misuse: Misuse.ShaderMismatch, at: pos)
            }
            val := if wc >= 4 { ReadWord(b, pos + 3) } else { u32(0) }
            if scr.dec_count < 1024 {
                scr.decs[scr.dec_count] = SpvDecRec(target: tgt, member: -1, dec: dec, val: val)
                scr.dec_count += 1
            }
        } else if op == SpvOpMemberDecorate {
            if wc < 4 {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            tgt := ReadWord(b, pos + 1)
            if tgt >= bound {
                fail Invalid(misuse: Misuse.BadShader, at: pos)
            }
            mem := Trunc[i32](ReadWord(b, pos + 2))
            dec := ReadWord(b, pos + 3)
            if dec == SpvDecCounterBuffer {
                fail Invalid(misuse: Misuse.ShaderMismatch, at: pos)
            }
            val := if wc >= 5 { ReadWord(b, pos + 4) } else { u32(0) }
            if scr.dec_count < 1024 {
                scr.decs[scr.dec_count] = SpvDecRec(target: tgt, member: mem, dec: dec, val: val)
                scr.dec_count += 1
            }
        } else if SpvTypeIdWord(op) {
            rid := ReadWord(b, pos + 1)
            w1 := if wc >= 3 { ReadWord(b, pos + 2) } else { u32(0) }
            w2 := if wc >= 4 { ReadWord(b, pos + 3) } else { u32(0) }
            w3 := if wc >= 5 { ReadWord(b, pos + 4) } else { u32(0) }
            w4 := if wc >= 6 { ReadWord(b, pos + 5) } else { u32(0) }
            w5 := if wc >= 7 { ReadWord(b, pos + 6) } else { u32(0) }
            w6 := if wc >= 8 { ReadWord(b, pos + 7) } else { u32(0) }
            // Check type operand ID bounds for pointer, vector, matrix, image, array, struct.
            if op == SpvOpTypeVector || op == SpvOpTypeMatrix || op == SpvOpTypeRuntimeArray {
                if w1 >= bound { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            } else if op == SpvOpTypePointer {
                if w2 >= bound { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            } else if op == SpvOpTypeImage {
                if w1 >= bound { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            } else if op == SpvOpTypeArray {
                if w1 >= bound || w2 >= bound { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            } else if op == SpvOpTypeStruct {
                for wi in 2..wc {
                    if ReadWord(b, pos + wi) >= bound { fail Invalid(misuse: Misuse.BadShader, at: pos) }
                }
            }
            try SpvAddType(mut scr, SpvTypeRec(id: rid, op: op, w1: w1, w2: w2, w3: w3, w4: w4, w5: w5, w6: w6, word_pos: pos, wc: wc))
        } else if op == SpvOpConstant {
            if wc < 4 { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            ty := ReadWord(b, pos + 1)
            rid := ReadWord(b, pos + 2)
            if ty >= bound { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            SpvAddConst(mut scr, rid, ReadWord(b, pos + 3))
        } else if op == SpvOpConstantTrue || op == SpvOpConstantFalse || op == SpvOpConstantNull || op == SpvOpSpecConstant || op == SpvOpSpecConstantTrue || op == SpvOpSpecConstantFalse {
            if wc < 3 { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            if ReadWord(b, pos + 1) >= bound { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            if op == SpvOpSpecConstant && wc >= 4 {
                SpvAddConst(mut scr, ReadWord(b, pos + 2), ReadWord(b, pos + 3))
            }
        } else if op == SpvOpVariable {
            if wc < 4 { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            pty := ReadWord(b, pos + 1)
            vid := ReadWord(b, pos + 2)
            sc := ReadWord(b, pos + 3)
            if pty >= bound { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            if wc >= 5 && ReadWord(b, pos + 4) >= bound { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            if !seen_function {
                if scr.var_count >= 64 {
                    fail Invalid(misuse: Misuse.LimitExceeded, at: pos)
                }
                set_v := SpvGetDecVal(&scr, vid, SpvDecDescriptorSet)
                bind_v := SpvGetDecVal(&scr, vid, SpvDecBinding)
                loc_v := SpvGetDecVal(&scr, vid, SpvDecLocation)
                loc_i := match loc_v {
                    some(l) { Trunc[i32](l) }
                    none { -1 }
                }
                scr.vars[scr.var_count] = SpvVarRec(
                    id: vid,
                    ptr_type: pty,
                    sc: sc,
                    set: set_v else { u32(0) },
                    binding: bind_v else { u32(0) },
                    has_set: set_v.IsSome(),
                    has_binding: bind_v.IsSome(),
                    location: loc_i,
                    has_builtin: SpvHasDec(&scr, vid, SpvDecBuiltIn),
                    non_writable: SpvHasDec(&scr, vid, SpvDecNonWritable),
                    non_const_index: false,
                )
                scr.var_count += 1
            }
        } else if op == SpvOpAccessChain || op == SpvOpInBoundsAccessChain || op == SpvOpPtrAccessChain {
            if wc < 4 { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            rty := ReadWord(b, pos + 1)
            base_id := ReadWord(b, pos + 3)
            if rty >= bound || base_id >= bound { fail Invalid(misuse: Misuse.BadShader, at: pos) }
            first_idx_w := if op == SpvOpPtrAccessChain { 5 } else { 4 }
            if wc > first_idx_w {
                idx_id := ReadWord(b, pos + first_idx_w)
                if idx_id >= bound { fail Invalid(misuse: Misuse.BadShader, at: pos) }
                if SpvFindConst(&scr, idx_id).IsNone() {
                    for vi in 0..scr.var_count {
                        if scr.vars[vi].id == base_id {
                            scr.vars[vi].non_const_index = true
                        }
                    }
                }
            }
        }

        pos += wc
    }

    if !seen_any_entry {
        fail Invalid(misuse: Misuse.BadShader, at: -1)
    }
    if !seen_stage_entry {
        fail Invalid(misuse: Misuse.StageMismatch, at: -1)
    }
    if !seen_matching_entry {
        fail Invalid(misuse: Misuse.ShaderMismatch, at: -1)
    }

    // Resolve global variables, check duplicate (set, binding), push constants, and inputs.
    var seen_push := false
    var push_size := 0

    // When a is some, populate reflection lists; otherwise validate in place with stack checks.
    var has_refl := false
    init_alloc := match a {
        some(al) { al }
        none { NullAllocFallback() }
    }
    var out_bindings := HeapList[SpvBinding].New(init_alloc)
    onerr out_bindings.Free(init_alloc)
    var out_inputs := HeapList[SpvVertexInput].New(init_alloc)
    onerr out_inputs.Free(init_alloc)
    match a {
        some(_) { has_refl = true }
        none {}
    }

    var seen_sets: [64]u32 = [0; 64]
    var seen_binds: [64]u32 = [0; 64]
    var seen_sb_count := 0

    for vi in 0..scr.var_count {
        v := &scr.vars[vi]
        pti := SpvFindType(&scr, v.ptr_type)
        if pti < 0 || scr.types[pti].op != SpvOpTypePointer {
            fail Invalid(misuse: Misuse.BadShader, at: -1)
        }
        pointee := scr.types[pti].w2
        var elem_ty := pointee
        var is_arr := false
        eti := SpvFindType(&scr, pointee)
        if eti >= 0 && (scr.types[eti].op == SpvOpTypeArray || scr.types[eti].op == SpvOpTypeRuntimeArray) {
            elem_ty = scr.types[eti].w1
            is_arr = true
        }

        if v.sc == SpvScPushConstant {
            if seen_push {
                fail Invalid(misuse: Misuse.ShaderMismatch, at: -1)
            }
            seen_push = true
            push_size = try SpvCheckPushConstant(b, &scr, elem_ty)
            continue
        }

        if v.sc == SpvScInput && stage == Stage.Vertex && !v.has_builtin && v.location >= 0 {
            cls := SpvScalarClass(&scr, pointee)
            if has_refl {
                try out_inputs.Push(a.Must(), SpvVertexInput(location: int(v.location), num_class: cls))
            }
            continue
        }

        if v.sc == SpvScUniform || v.sc == SpvScStorageBuffer || v.sc == SpvScUniformConstant {
            if !v.has_set || !v.has_binding {
                continue
            }
            // Duplicate (set, binding) check: DXC ignores register class when assigning SPIR-V bindings.
            for k in 0..seen_sb_count {
                if seen_sets[k] == v.set && seen_binds[k] == v.binding {
                    fail Invalid(misuse: Misuse.ShaderMismatch, at: int(v.binding))
                }
            }
            if seen_sb_count < 64 {
                seen_sets[seen_sb_count] = v.set
                seen_binds[seen_sb_count] = v.binding
                seen_sb_count += 1
            }

            el_idx := SpvFindType(&scr, elem_ty)
            if el_idx < 0 {
                fail Invalid(misuse: Misuse.BadShader, at: -1)
            }
            el := &scr.types[el_idx]

            if v.sc == SpvScUniformConstant {
                if el.op == SpvOpTypeSampler {
                    if has_refl {
                        try out_bindings.Push(a.Must(), SpvBinding(set: v.set, binding: v.binding, kind: SpvBindSampler, read_only: true, block_size: 0, dim: ViewDimension.D2, multisampled: false, sample_class: NumClassFloat, is_block_array: false, non_const_index: false))
                    }
                } else if el.op == SpvOpTypeImage {
                    dim_opt := SpvImageDim(el.w2, el.w4)
                    dim := match dim_opt {
                        some(d) { d }
                        none { fail Invalid(misuse: Misuse.ShaderMismatch, at: el.word_pos) }
                    }
                    // el.w6 == 1 means sampled image, 2 means storage image (cut).
                    if el.w6 == 2 {
                        fail Invalid(misuse: Misuse.ShaderMismatch, at: el.word_pos)
                    }
                    scls := SpvScalarClass(&scr, el.w1)
                    if has_refl {
                        try out_bindings.Push(a.Must(), SpvBinding(set: v.set, binding: v.binding, kind: SpvBindTexture, read_only: true, block_size: 0, dim: dim, multisampled: el.w5 != 0, sample_class: scls, is_block_array: false, non_const_index: false))
                    }
                } else {
                    fail Invalid(misuse: Misuse.ShaderMismatch, at: el.word_pos)
                }
                continue
            }

            // Buffer binding: Uniform+Block = uniform; Uniform+BufferBlock or StorageBuffer+Block = storage.
            is_block := SpvHasDec(&scr, elem_ty, SpvDecBlock)
            is_buf_block := SpvHasDec(&scr, elem_ty, SpvDecBufferBlock)
            var bind_kind := SpvBindStorage
            if v.sc == SpvScUniform && is_block {
                bind_kind = SpvBindUniform
            } else if (v.sc == SpvScUniform && is_buf_block) || (v.sc == SpvScStorageBuffer && is_block) {
                bind_kind = SpvBindStorage
            } else {
                fail Invalid(misuse: Misuse.ShaderMismatch, at: el.word_pos)
            }
            blk_sz := try SpvTypeSize(b, &scr, elem_ty)
            if has_refl {
                try out_bindings.Push(a.Must(), SpvBinding(
                    set: v.set,
                    binding: v.binding,
                    kind: bind_kind,
                    read_only: v.non_writable,
                    block_size: blk_sz,
                    dim: ViewDimension.D2,
                    multisampled: false,
                    sample_class: NumClassFloat,
                    is_block_array: is_arr,
                    non_const_index: v.non_const_index,
                ))
            }
        }
    }

    if has_refl {
        return some(SpirvReflection(
            stage: stage,
            uses_draw_parameters: uses_draw_params,
            local_x: local_x,
            local_y: local_y,
            local_z: local_z,
            push_size: push_size,
            bindings: out_bindings,
            inputs: out_inputs,
        ))
    }
    out_bindings.Free(init_alloc)
    out_inputs.Free(init_alloc)
    return none
}

// Dummy fallback allocator never used when has_refl is false (HeapList.New allocates nothing).
NullAllocFallback(): &Alloc {
    return mem.Null()
}

CheckSpirv(b: []u8, stage: Stage, entry: str): Error!void {
    r_opt := try ValidateAndParseSpirv(b, stage, entry, none)
    match r_opt {
        some(r) { r.Free(mem.Null()) }
        none {}
    }
}

ReflectSpirv(a: &Alloc, b: []u8, stage: Stage, entry: str): Error!SpirvReflection from a {
    r := try ValidateAndParseSpirv(b, stage, entry, some(a))
    return r.Must()
}
