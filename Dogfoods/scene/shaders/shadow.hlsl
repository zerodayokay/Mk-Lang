struct ShadowFrame {
    float4x4 light_vp;
};

struct DrawModel {
    float4x4 model;
    float4 color;
    float4 params;
};

ConstantBuffer<ShadowFrame> frame : register(b0, space0);
ConstantBuffer<DrawModel> draw_ubo : register(b1, space0);

struct VsIn {
    [[vk::location(0)]] float3 pos : LOC0;
    [[vk::location(1)]] float3 norm : LOC1;
    [[vk::location(2)]] float2 uv : LOC2;
};

float4 VsMain(VsIn i) : SV_Position {
    float4 wp = mul(draw_ubo.model, float4(i.pos, 1.0));
    return mul(frame.light_vp, wp);
}
