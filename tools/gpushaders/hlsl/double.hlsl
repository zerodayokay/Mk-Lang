RWStructuredBuffer<float> data : register(u0, space0);

[numthreads(64, 1, 1)]
void CsMain(uint3 tid : SV_DispatchThreadID) {
    data[tid.x] = data[tid.x] * 2.0;
}
