Texture2D src_tex : register(t0, space0);
SamplerState src_samp : register(s1, space0);

struct MipPush {
    float4 texel_size;
};

[[vk::push_constant]] ConstantBuffer<MipPush> pc : register(b0, space8);

struct VsOut {
    float4 pos : SV_Position;
    float2 uv : TEXCOORD0;
};

VsOut VsMain(uint vid : SV_VertexID) {
    float2 uv = float2((vid << 1) & 2, vid & 2);
    VsOut o;
    o.pos = float4(uv * float2(2.0, -2.0) + float2(-1.0, 1.0), 0.0, 1.0);
    o.uv = uv;
    return o;
}

float4 PsMain(VsOut i) : SV_Target0 {
    float2 d = pc.texel_size.xy * 0.5;
    float4 c0 = src_tex.SampleLevel(src_samp, i.uv + float2(-d.x, -d.y), 0.0);
    float4 c1 = src_tex.SampleLevel(src_samp, i.uv + float2( d.x, -d.y), 0.0);
    float4 c2 = src_tex.SampleLevel(src_samp, i.uv + float2(-d.x,  d.y), 0.0);
    float4 c3 = src_tex.SampleLevel(src_samp, i.uv + float2( d.x,  d.y), 0.0);
    return (c0 + c1 + c2 + c3) * 0.25;
}
