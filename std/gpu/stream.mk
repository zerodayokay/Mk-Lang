// Why: command recording runs on any task with zero driver calls and zero
// allocation after warm-up, packing commands into 8-byte-aligned records for
// single-switch replay on the system stack.
//
// What: stream opcodes, CommandStream, 64 KiB chunk recording, BarrierRef
// patching, and stream reset/recycling.
//
// How: packed u64 words in 64 KiB chunks from ChunkPool, plus a side
// BarrierItem arena and byte arena for debug labels.

const MaxStreamCommands: int = 1048576

const OpNop: u8 = 0
const OpBarrierRef: u8 = 1
const OpBeginRendering: u8 = 2
const OpEndRendering: u8 = 3
const OpSetPipeline: u8 = 4
const OpSetGroup: u8 = 5
const OpSetVertexBuffers: u8 = 6
const OpSetIndexBuffer: u8 = 7
const OpSetViewport: u8 = 8
const OpSetScissor: u8 = 9
const OpSetBlendConst: u8 = 10
const OpSetStencilRef: u8 = 11
const OpSetImmediates: u8 = 12
const OpDraw: u8 = 13
const OpDrawIndexed: u8 = 14
const OpDispatch: u8 = 15
const OpCopyBuffer: u8 = 16
const OpCopyBufferToTexture: u8 = 17
const OpCopyTextureToBuffer: u8 = 18
const OpCopyTexture: u8 = 19
const OpClearBuffer: u8 = 20
const OpPushDebugGroup: u8 = 21
const OpPopDebugGroup: u8 = 22

PackHdr(op: u8, words: u8, flags: u16, arg: u32): u64 {
    lo := u32(op) | (u32(words) << 8) | (u32(flags) << 16)
    return u64(lo) | (u64(arg) << 32)
}

PackU32Pair(lo: u32, hi: u32): u64 {
    return u64(lo) | (u64(hi) << 32)
}

UnpackOp(w0: u64): u8 {
    return Trunc[u8](w0 & 0xFF)
}

UnpackWords(w0: u64): int {
    return Cast[int]((w0 >> 8) & 0xFF)
}

UnpackFlags(w0: u64): u16 {
    return Trunc[u16]((w0 >> 16) & 0xFFFF)
}

UnpackArg(w0: u64): u32 {
    return Trunc[u32](w0 >> 32)
}

F32ToU32(v: f32): u32 {
    return v.Bits()
}

struct StreamLoc {
    chunk_idx: int
    word_idx: int
}

struct PassColorStreamRec {
    view_nh: u64
    tex_nh: u64
    subresource: u32
    resolve_view_nh: u64
    resolve_tex_nh: u64
    resolve_sub: u32
    dxgi_format: u32
    load_op: u8
    store_op: u8
    clear_r: f32
    clear_g: f32
    clear_b: f32
    clear_a: f32
}

struct PassDepthStreamRec {
    has_depth: bool
    view_nh: u64
    read_only: bool
    depth_load: u8
    depth_store: u8
    stencil_load: u8
    stencil_store: u8
    depth_clear: f32
    stencil_clear: u8
    has_stencil: bool
}

struct PassStreamRec {
    width: int
    height: int
    color_count: int
    colors: [8]PassColorStreamRec
    depth: PassDepthStreamRec
}

struct CommandStream {
    chunk_ids: HeapList[int]
    cur_chunk: int
    cur_word: int
    cmd_count: int
    first_error: ?Invalid
    barriers: HeapList[BarrierItem]
    passes: HeapList[PassStreamRec]
    labels: HeapList[u8]
    undo: HeapList[UndoOp]
    touched_slots: HeapList[u64]
    debug_depth: int
    pass_debug_base: int
}

CommandStream.New(a: &Alloc): CommandStream {
    return CommandStream(
        chunk_ids: HeapList[int].New(a),
        cur_chunk: -1,
        cur_word: 0,
        cmd_count: 0,
        first_error: none,
        barriers: HeapList[BarrierItem].New(a),
        passes: HeapList[PassStreamRec].New(a),
        labels: HeapList[u8].New(a),
        undo: HeapList[UndoOp].New(a),
        touched_slots: HeapList[u64].New(a),
        debug_depth: 0,
        pass_debug_base: 0,
    )
}

CommandStream.RecordError(mut self, misuse: Misuse, at: int) {
    match self.first_error {
        none { self.first_error = some(Invalid(misuse: misuse, at: at)) }
        some(_) {}
    }
}

CommandStream.HasError(&self): bool {
    return self.first_error.IsSome()
}

CommandStream.EnsureSpace(mut self, a: &Alloc into pool, pool: mut ChunkPool, words: int): OutOfMemory!bool {
    if self.cmd_count >= MaxStreamCommands {
        self.RecordError(Misuse.TooManyCommands, self.cmd_count)
        return false
    }
    if self.cur_chunk < 0 || self.cur_word + words > StreamChunkWords {
        cid := try pool.AllocChunk(a)
        self.chunk_ids.Push(a, cid) else e {
            pool.ReturnFree(a, cid)
            fail e
        }
        self.cur_chunk = self.chunk_ids.Len() - 1
        self.cur_word = 0
    }
    return true
}

CommandStream.Write1(mut self, a: &Alloc into pool, pool: mut ChunkPool, w0: u64): OutOfMemory!void {
    if !try self.EnsureSpace(a, pool, 1) {
        return
    }
    idx := self.cur_word
    cid := self.chunk_ids[self.cur_chunk]
    pool.chunks[cid].Mut().words[idx] = w0
    self.cur_word = idx + 1
    self.cmd_count += 1
}

CommandStream.Write2(mut self, a: &Alloc into pool, pool: mut ChunkPool, w0: u64, w1: u64): OutOfMemory!StreamLoc {
    if !try self.EnsureSpace(a, pool, 2) {
        return StreamLoc(chunk_idx: -1, word_idx: -1)
    }
    ci := self.cur_chunk
    wi := self.cur_word
    ch := pool.chunks[self.chunk_ids[ci]].Mut()
    ch.words[wi] = w0
    ch.words[wi + 1] = w1
    self.cur_word = wi + 2
    self.cmd_count += 1
    return StreamLoc(chunk_idx: ci, word_idx: wi)
}

CommandStream.Write3(mut self, a: &Alloc into pool, pool: mut ChunkPool, w0: u64, w1: u64, w2: u64): OutOfMemory!void {
    if !try self.EnsureSpace(a, pool, 3) {
        return
    }
    wi := self.cur_word
    ch := pool.chunks[self.chunk_ids[self.cur_chunk]].Mut()
    ch.words[wi] = w0
    ch.words[wi + 1] = w1
    ch.words[wi + 2] = w2
    self.cur_word = wi + 3
    self.cmd_count += 1
}

CommandStream.Write4(mut self, a: &Alloc into pool, pool: mut ChunkPool, w0: u64, w1: u64, w2: u64, w3: u64): OutOfMemory!void {
    if !try self.EnsureSpace(a, pool, 4) {
        return
    }
    wi := self.cur_word
    ch := pool.chunks[self.chunk_ids[self.cur_chunk]].Mut()
    ch.words[wi] = w0
    ch.words[wi + 1] = w1
    ch.words[wi + 2] = w2
    ch.words[wi + 3] = w3
    self.cur_word = wi + 4
    self.cmd_count += 1
}

CommandStream.WriteSlice(mut self, a: &Alloc into pool, pool: mut ChunkPool, words: []u64): OutOfMemory!StreamLoc {
    n := words.Len()
    if !try self.EnsureSpace(a, pool, n) {
        return StreamLoc(chunk_idx: -1, word_idx: -1)
    }
    ci := self.cur_chunk
    wi := self.cur_word
    ch := pool.chunks[self.chunk_ids[ci]].Mut()
    for i in 0..n {
        ch.words[wi + i] = words[i]
    }
    self.cur_word = wi + n
    self.cmd_count += 1
    return StreamLoc(chunk_idx: ci, word_idx: wi)
}

CommandStream.PatchBarrierRef(mut self, pool: mut ChunkPool, loc: StreamLoc, start: u32, count: u32, flags: u16) {
    if loc.chunk_idx < 0 || loc.chunk_idx >= self.chunk_ids.Len() {
        return
    }
    ch := pool.chunks[self.chunk_ids[loc.chunk_idx]].Mut()
    ch.words[loc.word_idx] = PackHdr(OpBarrierRef, 2, flags, start)
    ch.words[loc.word_idx + 1] = PackU32Pair(count, 0)
}

CommandStream.PushLabel(mut self, a: &Alloc, s: str): OutOfMemory!(offset: u32, len: u32) {
    off := Cast[u32](self.labels.Len())
    bs := s.AsBytes()
    try self.labels.Extend(a, bs)
    return (offset: off, len: Cast[u32](bs.Len()))
}

CommandStream.ResetKeepChunks(mut self) {
    self.cur_chunk = if self.chunk_ids.Len() > 0 { 0 } else { -1 }
    self.cur_word = 0
    self.cmd_count = 0
    self.first_error = none
    self.barriers.Clear()
    self.passes.Clear()
    self.labels.Clear()
    self.undo.Clear()
    self.touched_slots.Clear()
    self.debug_depth = 0
    self.pass_debug_base = 0
}

CommandStream.RecycleToPool(mut self, a: &Alloc, pool: mut ChunkPool, serial: u64, free_now: bool) {
    for self.chunk_ids.Len() > 0 {
        cid := self.chunk_ids.Pop().Must()
        if free_now {
            pool.ReturnFree(a, cid)
        } else {
            pool.ReturnInflight(a, serial, cid)
        }
    }
    self.ResetKeepChunks()
}

CommandStream.FreeAll(self, a: &Alloc) {
    CommandStream(chunk_ids, cur_chunk: _, cur_word: _, cmd_count: _, first_error: _, barriers, passes, labels, undo, touched_slots, debug_depth: _, pass_debug_base: _) := self
    chunk_ids.Free(a)
    barriers.Free(a)
    passes.Free(a)
    labels.Free(a)
    undo.Free(a)
    touched_slots.Free(a)
}

CommandStream.ChunkEndWord(&self, ci: int): int {
    if ci < self.cur_chunk {
        return StreamChunkWords
    }
    return self.cur_word
}

CommandStream.IsEmpty(&self): bool {
    return self.cmd_count == 0
}
