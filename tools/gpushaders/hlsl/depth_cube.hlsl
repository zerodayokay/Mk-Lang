struct Transform {
    float4x4 mvp;
};

struct PushTint {
    float4 tint;
};

ConstantBuffer<Transform> ubo : register(b0, space0);
[[vk::push_constant]] ConstantBuffer<PushTint> pc : register(b0, space8);

struct VsIn {
    [[vk::location(0)]] float3 pos : LOC0;
    [[vk::location(1)]] float3 col : LOC1;
};

struct VsOut {
    float4 pos : SV_Position;
    float3 col : COLOR0;
};

VsOut VsMain(VsIn i) {
    VsOut o;
    o.pos = mul(ubo.mvp, float4(i.pos, 1.0));
    o.col = i.col;
    return o;
}

float4 PsMain(VsOut i) : SV_Target0 {
    return float4(i.col, 1.0) * pc.tint;
}
