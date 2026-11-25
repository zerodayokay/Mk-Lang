/* A package's C source that faults inside __try, so its own __except
   handles the fault: in ntdll's RtlMoveMemory (a pc outside the image), and
   in this image's own code, a C callee with no handler of its own; and the
   first on a thread of its own. Each returns 1 when its __except ran, 0
   when the access succeeded. */
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

__declspec(dllimport) void *__stdcall CreateThread(void *attr, u64 stack, unsigned long(__stdcall *start)(void *),
                                                   void *arg, unsigned long flags, unsigned long *id);
__declspec(dllimport) unsigned long __stdcall WaitForSingleObject(void *h, unsigned long ms);
__declspec(dllimport) int __stdcall CloseHandle(void *h);

static volatile int thread_answer = 2;

static unsigned long __stdcall thread_copy(void *src) {
    thread_answer = host_copy_try(src);
    return 0;
}

/* host_copy_try on a thread of the C code's own, which Mk did not make:
   its fault is on no stack Mk runs on. Returns host_copy_try's answer, or
   2 when the thread cannot be made. */
int thread_copy_try(const void *src) {
    void *h = CreateThread(0, 0, thread_copy, (void *)src, 0, 0);
    if (h == 0) {
        return 2;
    }
    WaitForSingleObject(h, 0xFFFFFFFF);
    CloseHandle(h);
    return thread_answer;
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
