// Why: GPU resources and command-stream chunks cannot be freed while an
// in-flight submission still references them.
//
// What: SerialTracker, DeferredKind, DeferredItem, DeferredQueue (min-heap by
// serial), and ChunkPool (recycled 64 KiB command-stream chunks).
//
// How: O(1) serial tracking and O(log N) deferred-release min-heap so objects
// closed in any order are released as soon as their last_use serial finishes.

import std.mem

const StreamChunkWords: int = 8192

struct StreamChunk {
    words: [8192]u64
}

struct SerialTracker {
    next: u64
    completed: u64
}

SerialTracker.New(): SerialTracker {
    return SerialTracker(next: 1, completed: 0)
}

SerialTracker.LastSubmitted(&self): u64 {
    if self.next <= 1 {
        return 0
    }
    return self.next - 1
}

SerialTracker.Advance(mut self): u64 {
    s := self.next
    self.next += 1
    return s
}

SerialTracker.Complete(mut self, s: u64) {
    if s > self.completed {
        self.completed = s
    }
}

SerialTracker.ClampWait(&self, target: u64): u64 {
    last := self.LastSubmitted()
    if target > last {
        return last
    }
    return target
}

const DefBuffer: u8 = 1
const DefTexture: u8 = 2
const DefView: u8 = 3
const DefSampler: u8 = 4
const DefBindGroupLayout: u8 = 5
const DefPipelineLayout: u8 = 6
const DefBindGroup: u8 = 7
const DefShader: u8 = 8
const DefRenderPipeline: u8 = 9
const DefComputePipeline: u8 = 10

struct DeferredItem {
    serial: u64
    kind: u8
    slot: u32
}

struct DeferredQueue {
    heap: HeapList[DeferredItem]
}

DeferredQueue.New(a: &Alloc): DeferredQueue {
    return DeferredQueue(heap: HeapList[DeferredItem].New(a))
}

DeferredQueue.Push(mut self, a: &Alloc, item: DeferredItem): OutOfMemory!void {
    try self.heap.Push(a, item)
    var i := self.heap.Len() - 1
    for i > 0 {
        p := (i - 1) / 2
        if self.heap[p].serial <= self.heap[i].serial {
            break
        }
        tmp := self.heap[p]
        self.heap[p] = self.heap[i]
        self.heap[i] = tmp
        i = p
    }
}

DeferredQueue.PopReady(mut self, completed: u64): ?DeferredItem {
    if self.heap.Len() == 0 || self.heap[0].serial > completed {
        return none
    }
    top := self.heap[0]
    last := self.heap.Pop().Must()
    n := self.heap.Len()
    if n > 0 {
        self.heap[0] = last
        var i := 0
        for {
            l := i * 2 + 1
            if l >= n {
                break
            }
            var m := l
            r := l + 1
            if r < n && self.heap[r].serial < self.heap[l].serial {
                m = r
            }
            if self.heap[i].serial <= self.heap[m].serial {
                break
            }
            tmp := self.heap[i]
            self.heap[i] = self.heap[m]
            self.heap[m] = tmp
            i = m
        }
    }
    return some(top)
}

DeferredQueue.Len(&self): int {
    return self.heap.Len()
}

DeferredQueue.Free(self, a: &Alloc) {
    DeferredQueue(heap) := self
    heap.Free(a)
}

struct InflightChunk {
    serial: u64
    chunk_idx: int
}

struct ChunkPool {
    chunks: HeapList[Box[StreamChunk]]
    free_ids: HeapList[int]
    inflight: HeapList[InflightChunk]
}

ChunkPool.New(a: &Alloc): ChunkPool {
    return ChunkPool(chunks: HeapList[Box[StreamChunk]].New(a), free_ids: HeapList[int].New(a), inflight: HeapList[InflightChunk].New(a))
}

ChunkPool.AllocChunk(mut self, a: &Alloc into self): OutOfMemory!int {
    if self.free_ids.Len() > 0 {
        return self.free_ids.Pop().Must()
    }
    b := try Box[StreamChunk].New(a, mem.Zeroed[StreamChunk]())
    idx := self.chunks.Len()
    self.chunks.Push(a, b) else e {
        b.Free(a)
        fail e
    }
    return idx
}

ChunkPool.ReturnFree(mut self, a: &Alloc, chunk_idx: int) {
    self.free_ids.Push(a, chunk_idx) else {}
}

ChunkPool.ReturnInflight(mut self, a: &Alloc, serial: u64, chunk_idx: int) {
    self.inflight.Push(a, InflightChunk(serial: serial, chunk_idx: chunk_idx)) else {
        self.ReturnFree(a, chunk_idx)
    }
}

ChunkPool.Recycle(mut self, a: &Alloc, completed: u64) {
    var i := 0
    for i < self.inflight.Len() {
        if self.inflight[i].serial <= completed {
            item := self.inflight.Remove(i)
            self.ReturnFree(a, item.chunk_idx)
        } else {
            i += 1
        }
    }
}

ChunkPool.FreeAll(self, a: &Alloc) {
    ChunkPool(chunks: chunks0, free_ids, inflight) := self
    free_ids.Free(a)
    inflight.Free(a)
    var chunks := chunks0
    for chunks.Len() > 0 {
        chunks.Pop().Must().Free(a)
    }
    chunks.FreeEmpty(a)
}
