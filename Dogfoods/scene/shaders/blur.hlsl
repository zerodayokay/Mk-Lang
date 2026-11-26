StructuredBuffer<uint> src_pixels : register(t0, space0);
RWStructuredBuffer<uint> dst_pixels : register(u1, space0);

struct BlurPush {
    uint4 dims;
};

[[vk::push_constant]] ConstantBuffer<BlurPush> pc : register(b0, space8);

float4 UnpackRgba(uint p) {
    return float4(
        float(p & 255u),
        float((p >> 8) & 255u),
        float((p >> 16) & 255u),
        float((p >> 24) & 255u)
    ) / 255.0;
}

uint PackRgba(float4 c) {
    uint r = clamp(uint(c.r * 255.0 + 0.5), 0u, 255u);
    uint g = clamp(uint(c.g * 255.0 + 0.5), 0u, 255u);
    uint b = clamp(uint(c.b * 255.0 + 0.5), 0u, 255u);
    uint a = clamp(uint(c.a * 255.0 + 0.5), 0u, 255u);
    return r | (g << 8) | (b << 16) | (a << 24);
}

[numthreads(8, 8, 1)]
void CsMain(uint3 tid : SV_DispatchThreadID) {
    int w = int(pc.dims.x);
    int h = int(pc.dims.y);
    int stride = int(pc.dims.z);
    int vertical = int(pc.dims.w);
    int x = int(tid.x);
    int y = int(tid.y);
    if (x >= w || y >= h) {
        return;
    }
    static const float weights[5] = { 1.0 / 16.0, 4.0 / 16.0, 6.0 / 16.0, 4.0 / 16.0, 1.0 / 16.0 };
    float4 acc = float4(0.0, 0.0, 0.0, 0.0);
    for (int k = -2; k <= 2; k++) {
        int sx = vertical == 0 ? clamp(x + k, 0, w - 1) : x;
        int sy = vertical != 0 ? clamp(y + k, 0, h - 1) : y;
        uint px = src_pixels[sy * stride + sx];
        acc += UnpackRgba(px) * weights[k + 2];
    }
    dst_pixels[y * stride + x] = PackRgba(acc);
}
