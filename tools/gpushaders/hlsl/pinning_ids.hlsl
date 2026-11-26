struct VsIn {
    [[vk::location(0)]] float2 pos : LOC0;
    [[vk::location(1)]] float4 attr_col : LOC1;
};

struct VsOut {
    float4 pos : SV_Position;
    float4 col : COLOR0;
};

VsOut VsMain(VsIn i, uint vid : SV_VertexID, uint iid : SV_InstanceID) {
    VsOut o;
    o.pos = float4(i.pos, 0.0, 1.0);
    o.col = float4(float(vid) / 255.0, float(iid) / 255.0, i.attr_col.b, 1.0);
    return o;
}

float4 PsMain(VsOut i) : SV_Target0 {
    return i.col;
}
