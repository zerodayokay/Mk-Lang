struct FrameUbo {
    float4x4 view_proj;
    float4x4 light_vp;
    float4 light_dir;
    float4 light_col;
    float4 ambient_col;
};

struct InstPush {
    float4 tint;
};

ConstantBuffer<FrameUbo> frame : register(b0, space0);
StructuredBuffer<float4> instances : register(t1, space0);
[[vk::push_constant]] ConstantBuffer<InstPush> pc : register(b0, space8);

struct VsIn {
    [[vk::location(0)]] float3 pos : LOC0;
    [[vk::location(1)]] float3 norm : LOC1;
    [[vk::location(2)]] float2 uv : LOC2;
};

struct VsOut {
    float4 pos : SV_Position;
    float3 world_norm : NORMAL0;
    float4 col : COLOR0;
};

VsOut VsMain(VsIn i, uint iid : SV_InstanceID) {
    float4 xform = instances[iid * 2];
    float4 icol = instances[iid * 2 + 1];
    float3 wp = i.pos * xform.w + xform.xyz;
    VsOut o;
    o.pos = mul(frame.view_proj, float4(wp, 1.0));
    o.world_norm = i.norm;
    o.col = icol * pc.tint;
    return o;
}

float4 PsMain(VsOut i) : SV_Target0 {
    float3 n = normalize(i.world_norm);
    float3 l = normalize(-frame.light_dir.xyz);
    float ndotl = max(dot(n, l), 0.0);
    float3 lit = i.col.rgb * (frame.ambient_col.rgb + frame.light_col.rgb * ndotl);
    return float4(lit, i.col.a);
}
