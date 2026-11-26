struct Tint {
    float4 color;
};

ConstantBuffer<Tint> tint : register(b0, space0);

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
    return tint.color;
}
