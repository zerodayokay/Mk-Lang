/* A C function that uses a known depth of stack and then compares its
   frame with the thread's NT_TIB StackLimit (gs:0x10), the lowest committed
   byte of the stack the thread says it runs on. Windows grows a stack by
   moving its guard page down and lowering StackLimit, and a stack probe
   trusts every page below StackLimit to be unused: MSVC's __chkstk stores
   a zero in each one down to the new frame. So a frame below StackLimit is
   a frame the next probe writes over. deep_run returns 1 from such a frame
   and 0 from one at or above StackLimit. */
typedef unsigned long long u64;

static u64 stack_limit(void) {
    u64 v;
    __asm__("movq %%gs:0x10, %0" : "=r"(v));
    return v;
}

/* Each frame holds just under one page, so the stack is touched a page at a
   time and the guard page is never jumped. */
__declspec(noinline) u64 deep_run(u64 frames) {
    volatile char frame[3900];
    frame[0] = (char)frames;
    if (frames > 0) {
        u64 below = deep_run(frames - 1);
        return below + (u64)(frame[0] & 0);
    }
    return stack_limit() > (u64)frame ? 1 : 0;
}
