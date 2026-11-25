/* A package's C source that faults inside __try, so its own __except
   handles the fault: in ntdll's RtlMoveMemory (a pc outside the image), and
   in this image's own code, a C callee with no handler of its own. Each returns 1 when its __except ran, 0 when the
   access succeeded. */
typedef unsigned long long u64;
__declspec(dllimport) void __stdcall RtlMoveMemory(void *dst, const void *src, u64 n);

int host_copy_try(const void *src) {
    volatile u64 v = 0;
    __try {
        RtlMoveMemory((void *)&v, src, 8);
    } __except (1) {
        return 1;
    }
    return 0;
}

/* The read is in a callee: clang's __try covers a fault in a call it
   makes, not one in its own function's instructions. */
__declspec(noinline) static u64 load(const volatile u64 *src) {
    return *src;
}

int own_read_try(const volatile u64 *src) {
    __try {
        volatile u64 v = load(src);
        (void)v;
    } __except (1) {
        return 1;
    }
    return 0;
}
