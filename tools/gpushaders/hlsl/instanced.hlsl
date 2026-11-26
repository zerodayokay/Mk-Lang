StructuredBuffer<float4> instances : register(t0, space0);

struct VsOut {
    float4 pos : SV_Position;
    float4 col : COLOR0;
};

VsOut VsMain(uint vid : SV_VertexID, uint iid : SV_InstanceID) {
    float4 inst = instances[iid];
    float2 ofs = float2(
        (vid & 1) == 0 ? -0.05 : 0.05,
        (vid & 2) == 0 ? -0.05 : 0.05
    );
    VsOut o;
    o.pos = float4(inst.xy + ofs, 0.0, 1.0);
    o.col = float4(float(vid) * 0.25, float(iid) * 0.0625, inst.z, 1.0);
    return o;
}

float4 PsMain(VsOut i) : SV_Target0 {
    return i.col;
}
