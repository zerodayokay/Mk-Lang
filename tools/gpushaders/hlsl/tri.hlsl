struct VsOut {
    float4 pos : SV_Position;
    float3 col : COLOR0;
};

VsOut VsMain(uint vid : SV_VertexID) {
    static const float2 positions[3] = {
        float2( 0.0,  0.5),
        float2( 0.5, -0.5),
        float2(-0.5, -0.5)
    };
    static const float3 colors[3] = {
        float3(1.0, 0.0, 0.0),
        float3(0.0, 1.0, 0.0),
        float3(0.0, 0.0, 1.0)
    };
    VsOut o;
    o.pos = float4(positions[vid % 3], 0.0, 1.0);
    o.col = colors[vid % 3];
    return o;
}

float4 PsMain(VsOut i) : SV_Target0 {
    return float4(i.col, 1.0);
}
