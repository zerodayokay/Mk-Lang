// Why: shaders arrive as precompiled bytecode; SPIR-V describes the
// interface for both backends, so both get the same checks.
//
// What: ShaderCode, Stage, Shader, Device.NewShader, Shader.Close,
// CheckShader.
//
// How: CheckShader calls CheckSpirv with zero allocation; NewShader checks
// SPIR-V (and DXIL on D3D12), builds reflection metadata in the device's
// heap, creates the backend shader handle, and returns a generation-checked
// Shader handle.

/// One logical shader. spirv is always required: it is the interface the
/// library checks for both backends. dxil is required on D3D12 and must
/// come from the same source and compiler run.
export struct ShaderCode {
    export spirv: []u8
    export dxil: []u8
}

/// A shader stage.
export open enum Stage {
    Vertex
    Fragment
    Compute
}

/// A checked shader module.
export linear struct Shader {
    slot: u32
    generation: u32
    device: u32
}

struct ShaderEntry {
    stage: Stage
    refl: SpirvReflection
    native_handle: u64
}

ShaderEntry.Free(self, a: &Alloc) {
    ShaderEntry(stage: _, refl, native_handle: _) := self
    refl.Free(a)
}

/// Checks code (the SPIR-V reader walks the whole module, bounded) and keeps
/// the blob of this device's backend. Errors: Invalid(MissingSpirv),
/// Unsupported (no DXIL on D3D12), Invalid(BadShader),
/// Invalid(UnsignedDxil), Invalid(StageMismatch). O(blob words); Vulkan
/// makes a VkShaderModule, D3D12 copies the DXIL into the device's heap.
export Device.NewShader(mut self, code: ShaderCode, stage: Stage, entry: str, label: str): Error!Shader {
    _ = label
    try self.CheckAlive()
    if code.spirv.Len() == 0 {
        fail Invalid(misuse: Misuse.MissingSpirv, at: -1)
    }
    a := self.heap.Alloc()
    refl := try ReflectSpirv(a, code.spirv, stage, entry)
    if refl.uses_draw_parameters && !self.state.Ref().draw_params_ok {
        refl.Free(a)
        fail Unsupported
    }
    if self.Backend() == Backend.D3d12 {
        if code.dxil.Len() == 0 {
            refl.Free(a)
            fail Unsupported
        }
        CheckDxil(code.dxil, stage) else e {
            refl.Free(a)
            fail e
        }
    }
    nh := self.NativeCreateShader(code, stage, entry) else e {
        refl.Free(a)
        fail e
    }
    se := ShaderEntry(stage: stage, refl: refl, native_handle: nh)
    st := self.state.Mut()
    match st.shaders.Alloc(a, se) {
        .Ok(slot, gen_) {
            return Shader(slot: slot, generation: gen_, device: self.serial)
        }
        .Oom(val) {
            val.Free(a)
            st.native.DestroyShader(nh)
            fail OutOfMemory
        }
    }
}

/// Gives the shader back. O(1).
export Shader.Close(self, dev: mut Device) {
    Shader(slot, generation, device) := self
    if device != dev.serial {
        Panic("gpu: a handle closed with another device")
    }
    dev.CloseShaderSlot(slot, generation)
}

Device.CloseShaderSlot(mut self, slot: u32, gen_: u32) {
    st := self.state.Mut()
    if !st.shaders.MarkUserClosed(slot, gen_) {
        return
    }
    lu := st.shaders.LastUse(slot)
    self.QueueOrRelease(DefShader, slot, lu)
}

Device.DestroyShaderSlot(mut self, slot: u32) {
    a := self.heap.Alloc()
    st := self.state.Mut()
    match st.shaders.Release(a, slot) {
        some(e) {
            nh := e.native_handle
            e.Free(a)
            st.native.DestroyShader(nh)
        }
        none {}
    }
}

/// Checks code's SPIR-V by the module-level rules (bounded full walk, the
/// binding convention, no counters or combined samplers, the immediates
/// layout) with no device: what tools/gpushaders runs at build time.
/// Errors: Invalid(MissingSpirv), Invalid(BadShader), Invalid(ShaderMismatch).
/// O(words); allocates nothing.
export CheckShader(code: ShaderCode, stage: Stage, entry: str): Error!void {
    if code.spirv.Len() == 0 {
        fail Invalid(misuse: Misuse.MissingSpirv, at: -1)
    }
    try CheckSpirv(code.spirv, stage, entry)
    if code.dxil.Len() > 0 {
        try CheckDxil(code.dxil, stage)
    }
}
