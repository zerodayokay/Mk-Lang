struct FrameUbo {
    float4x4 view_proj;
    float4x4 light_vp;
    float4 light_dir;
    float4 light_col;
    float4 ambient_col;
};

struct DrawUbo {
    float4x4 model;
    float4 color;
    float4 params;
};

struct ScenePush {
    float4 flags;
};

ConstantBuffer<FrameUbo> frame : register(b0, space0);
ConstantBuffer<DrawUbo> draw_ubo : register(b1, space0);
Texture2D albedo_tex : register(t2, space0);
SamplerState albedo_samp : register(s3, space0);
Texture2D<float> shadow_tex : register(t4, space0);
SamplerComparisonState shadow_samp : register(s5, space0);
[[vk::push_constant]] ConstantBuffer<ScenePush> pc : register(b0, space8);

struct VsIn {
    [[vk::location(0)]] float3 pos : LOC0;
    [[vk::location(1)]] float3 norm : LOC1;
    [[vk::location(2)]] float2 uv : LOC2;
};

struct VsOut {
    float4 pos : SV_Position;
    float3 world_norm : NORMAL0;
    float2 uv : TEXCOORD0;
    float4 shadow_pos : TEXCOORD1;
};

VsOut VsMain(VsIn i) {
    VsOut o;
    float4 wp = mul(draw_ubo.model, float4(i.pos, 1.0));
    o.pos = mul(frame.view_proj, wp);
    o.world_norm = mul((float3x3)draw_ubo.model, i.norm);
    o.uv = i.uv;
    o.shadow_pos = mul(frame.light_vp, wp);
    return o;
}

float4 PsMain(VsOut i) : SV_Target0 {
    float3 n = normalize(i.world_norm);
    float3 l = normalize(-frame.light_dir.xyz);
    float ndotl = max(dot(n, l), 0.0);
    float4 texel = albedo_tex.Sample(albedo_samp, i.uv);
    float3 base_col = texel.rgb * draw_ubo.color.rgb;
    float vis = 1.0;
    if (pc.flags.x > 0.5) {
        float3 sp = i.shadow_pos.xyz / i.shadow_pos.w;
        float2 suv = float2(sp.x * 0.5 + 0.5, 0.5 - sp.y * 0.5);
        if (suv.x >= 0.0 && suv.x <= 1.0 && suv.y >= 0.0 && suv.y <= 1.0 && sp.z >= 0.0 && sp.z <= 1.0) {
            vis = shadow_tex.SampleCmpLevelZero(shadow_samp, suv, sp.z - pc.flags.y);
        }
    }
    float3 lit = base_col * (frame.ambient_col.rgb + frame.light_col.rgb * ndotl * vis);
    return float4(lit, draw_ubo.color.a * pc.flags.z);
}
