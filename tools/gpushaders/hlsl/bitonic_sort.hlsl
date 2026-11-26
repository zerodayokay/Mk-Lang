struct SortParams {
    uint k;
    uint j;
    uint count;
    uint pad;
};

[[vk::push_constant]] ConstantBuffer<SortParams> pc : register(b0, space8);
RWStructuredBuffer<uint> keys : register(u0, space0);

[numthreads(64, 1, 1)]
void CsMain(uint3 tid : SV_DispatchThreadID) {
    uint i = tid.x;
    if (i >= pc.count) {
        return;
    }
    uint ixj = i ^ pc.j;
    if (ixj > i) {
        uint a = keys[i];
        uint b = keys[ixj];
        bool asc = (i & pc.k) == 0u;
        if ((asc && a > b) || (!asc && a < b)) {
            keys[i] = b;
            keys[ixj] = a;
        }
    }
}
