struct Params {
    float4 tint;
};

ConstantBuffer<Params> params : register(b0, space0);
Texture2D tex : register(t1, space0);
SamplerState samp : register(s2, space0);

struct VsIn {
    [[vk::location(0)]] float2 pos : LOC0;
    [[vk::location(1)]] float2 uv : LOC1;
};

struct VsOut {
    float4 pos : SV_Position;
    float2 uv : TEXCOORD0;
};

VsOut VsMain(VsIn i) {
    VsOut o;
    o.pos = float4(i.pos, 0.0, 1.0);
    o.uv = i.uv;
    return o;
}

float4 PsMain(VsOut i) : SV_Target0 {
    return tex.SampleLevel(samp, i.uv, 0.0) * params.tint;
}
