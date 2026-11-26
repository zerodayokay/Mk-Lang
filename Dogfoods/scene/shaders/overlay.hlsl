struct OverlayPush {
    float4 rect;
    float4 color;
};

[[vk::push_constant]] ConstantBuffer<OverlayPush> pc : register(b0, space8);

float4 VsMain(uint vid : SV_VertexID) : SV_Position {
    static const float2 corners[6] = {
        float2(0.0, 0.0), float2(1.0, 0.0), float2(1.0, 1.0),
        float2(0.0, 0.0), float2(1.0, 1.0), float2(0.0, 1.0)
    };
    float2 c = corners[vid % 6];
    float2 p = lerp(pc.rect.xy, pc.rect.zw, c);
    return float4(p, 0.0, 1.0);
}

float4 PsMain() : SV_Target0 {
    return pc.color;
}
