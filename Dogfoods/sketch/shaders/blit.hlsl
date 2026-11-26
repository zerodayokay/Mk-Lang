// The canvas texture stretched over the target by one triangle that covers it.

Texture2D canvas_tex : register(t0, space0);
SamplerState canvas_samp : register(s1, space0);

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
    return canvas_tex.SampleLevel(canvas_samp, i.uv, 0.0);
}
