struct ColorPush {
    float4 color;
};

[[vk::push_constant]] ConstantBuffer<ColorPush> pc : register(b0, space8);

struct VsIn {
    [[vk::location(0)]] float2 pos : LOC0;
};

struct VsOut {
    float4 pos : SV_Position;
};

VsOut VsMain(VsIn i) {
    VsOut o;
    o.pos = float4(i.pos, 0.0, 1.0);
    return o;
}

float4 PsMain(VsOut i) : SV_Target0 {
    return pc.color;
}
