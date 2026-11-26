// Why: Vulkan timeline semaphores must suspend Mk tasks on a Waker without
// holding a carrier or pool thread, and support cancellation and device loss.
//
// What: VkWaiterShared lifecycle (StartWaiterThread, StopWaiterThread,
// WaitForSerial) using one OS thread per device and vkWaitSemaphores with
// VK_SEMAPHORE_WAIT_ANY_BIT over {device timeline, control timeline}.
//
// How: the waiter thread loops with a 1s finite timeout, publishes the
// hardware counter to completed, calls Waker.Wake when target is reached or
// device loss occurs, and exits cleanly when stop is signalled on Close.

import c.vulkan
import std.core
import std.mem
import std.raw
import std.runtime
import std.sys

const WaiterTimeoutNs: u64 = 1_000_000_000 // 1 second finite timeout
const WaiterStackBytes: int = 262144       // 256 KiB stack

PublishCompleted(completed: &raw.Atomic[u64], serial: u64): u64 {
    var prev := completed.Load()
    for prev < serial {
        if completed.Cas(prev, serial) {
            return serial
        }
        prev = completed.Load()
    }
    return prev
}

WaiterThreadMain(arg: *u8) {
    unsafe "arg is a live VkWaiterShared until StopWaiterThread joins this thread" {
        sh := raw.PtrCast[u8, VkWaiterShared](arg)
        dev := HandleFrom[vulkan.VkDeviceT]((*sh).device)
        tsem := HandleFrom[vulkan.VkSemaphoreT]((*sh).timeline_sem)
        csem := HandleFrom[vulkan.VkSemaphoreT]((*sh).control_sem)
        wait_fn := raw.FnFromAddr[vulkan.PfnVkWaitSemaphores]((*sh).wait_fn_addr).Must()
        get_fn := raw.FnFromAddr[vulkan.PfnVkGetSemaphoreCounterValue]((*sh).get_counter_fn_addr).Must()

        var sems: [2]vulkan.VkSemaphore = [csem, tsem]
        for (*sh).stop.Load() == 0 {
            ctrl_seq := (*sh).control_seq.Load()
            tgt := (*sh).target.Load()
            var cur_val: u64 = 0
            gr := get_fn(dev, tsem, raw.OfMut(mut cur_val))
            if gr != vulkan.VkSuccess {
                (*sh).lost.Store(1)
                (*sh).waker.Wake()
                break
            }
            cur_val = PublishCompleted(&(*sh).completed, cur_val)
            if tgt > 0 && cur_val >= tgt {
                (*sh).waker.Wake()
            }
            var vals: [2]u64 = [ctrl_seq + 1, tgt]
            var wi := mem.Zeroed[vulkan.VkSemaphoreWaitInfo]()
            wi.s_type = vulkan.VkStructureTypeSemaphoreWaitInfo
            wi.flags = Cast[u32](vulkan.VkSemaphoreWaitAnyBit)
            wi.semaphore_count = if cur_val < tgt { 2 } else { 1 }
            wi.p_semaphores = raw.Data(sems[..])
            wi.p_values = raw.Data(vals[..])
            wr := wait_fn(dev, raw.Of(&wi), WaiterTimeoutNs)
            if wr != vulkan.VkSuccess && wr != vulkan.VkTimeout {
                (*sh).lost.Store(1)
                (*sh).waker.Wake()
                break
            }
        }
    }
}

SignalControlSem(sh: *VkWaiterShared) {
    unsafe "sh is a live VkWaiterShared" {
        seq := (*sh).control_seq.Add(1) + 1
        sig_fn := raw.FnFromAddr[vulkan.PfnVkSignalSemaphore]((*sh).signal_fn_addr).Must()
        var si := mem.Zeroed[vulkan.VkSemaphoreSignalInfo]()
        si.s_type = vulkan.VkStructureTypeSemaphoreSignalInfo
        si.semaphore = HandleFrom[vulkan.VkSemaphoreT]((*sh).control_sem)
        si.value = seq
        _ = sig_fn(HandleFrom[vulkan.VkDeviceT]((*sh).device), raw.Of(&si))
    }
}

StartWaiterThread(a: &Alloc, st: mut VkDeviceState): Error!void {
    unsafe "st.device and dev_d are live" {
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        var sci := mem.Zeroed[vulkan.VkSemaphoreCreateInfo]()
        sci.s_type = vulkan.VkStructureTypeSemaphoreCreateInfo
        var stci := mem.Zeroed[vulkan.VkSemaphoreTypeCreateInfo]()
        stci.s_type = vulkan.VkStructureTypeSemaphoreTypeCreateInfo
        stci.semaphore_type = vulkan.VkSemaphoreTypeTimeline
        stci.initial_value = 0
        sci.p_next = raw.PtrCast[vulkan.VkSemaphoreTypeCreateInfo, void](raw.Of(&stci))

        var tsem: vulkan.VkSemaphore = raw.Null[vulkan.VkSemaphoreT]()
        r1 := st.dev_d.vk_create_semaphore.Must()(dev, raw.Of(&sci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut tsem))
        if r1 != vulkan.VkSuccess {
            fail MapVkError(r1, CallCreateSemaphore)
        }
        var csem: vulkan.VkSemaphore = raw.Null[vulkan.VkSemaphoreT]()
        r2 := st.dev_d.vk_create_semaphore.Must()(dev, raw.Of(&sci), raw.Null[vulkan.VkAllocationCallbacks](), raw.OfMut(mut csem))
        if r2 != vulkan.VkSuccess {
            st.dev_d.vk_destroy_semaphore.Must()(dev, tsem, raw.Null[vulkan.VkAllocationCallbacks]())
            fail MapVkError(r2, CallCreateSemaphore)
        }

        var wfn := st.dev_d.vk_wait_semaphores.Must()
        var sfn := st.dev_d.vk_signal_semaphore.Must()
        var gfn := st.dev_d.vk_get_semaphore_counter_value.Must()
        waddr := raw.Read(raw.PtrCast[fn(*vulkan.VkDeviceT, *vulkan.VkSemaphoreWaitInfo, u64): vulkan.VkResult, uint](raw.Of(&wfn)))
        saddr := raw.Read(raw.PtrCast[fn(*vulkan.VkDeviceT, *vulkan.VkSemaphoreSignalInfo): vulkan.VkResult, uint](raw.Of(&sfn)))
        gaddr := raw.Read(raw.PtrCast[fn(*vulkan.VkDeviceT, *vulkan.VkSemaphoreT, *u64): vulkan.VkResult, uint](raw.Of(&gfn)))

        sh := VkWaiterShared(
            device: st.device,
            timeline_sem: HandleAddr(tsem),
            control_sem: HandleAddr(csem),
            wait_fn_addr: waddr,
            signal_fn_addr: saddr,
            get_counter_fn_addr: gaddr,
            target: raw.Atomic[u64].New(0),
            completed: raw.Atomic[u64].New(0),
            control_seq: raw.Atomic[u64].New(0),
            stop: raw.Atomic[u32].New(0),
            lost: raw.Atomic[u32].New(0),
            waker: runtime.Waker.New(),
            thread_handle: 0,
            started: false,
        )
        box := Box[VkWaiterShared].New(a, sh) else e {
            st.dev_d.vk_destroy_semaphore.Must()(dev, csem, raw.Null[vulkan.VkAllocationCallbacks]())
            st.dev_d.vk_destroy_semaphore.Must()(dev, tsem, raw.Null[vulkan.VkAllocationCallbacks]())
            fail e
        }
        sh_ptr := raw.Of(&*box.Ref())
        sh_addr := raw.Addr(sh_ptr)
        raw.Forget(box)

        res := sys.ThreadStartJoin(WaiterThreadMain, raw.FromAddr[u8](sh_addr), WaiterStackBytes)
        if res.err != core.SysErr.None {
            core.FreeBytes(a, raw.FromAddr[u8](sh_addr), SizeOf[VkWaiterShared](), AlignOf[VkWaiterShared]())
            st.dev_d.vk_destroy_semaphore.Must()(dev, csem, raw.Null[vulkan.VkAllocationCallbacks]())
            st.dev_d.vk_destroy_semaphore.Must()(dev, tsem, raw.Null[vulkan.VkAllocationCallbacks]())
            fail OutOfMemory
        }
        (*raw.FromAddr[VkWaiterShared](sh_addr)).thread_handle = res.value
        (*raw.FromAddr[VkWaiterShared](sh_addr)).started = true
        st.timeline_sem = HandleAddr(tsem)
        st.waiter_ptr = sh_addr
    }
}

StopWaiterThread(a: &Alloc, st: mut VkDeviceState) {
    if st.waiter_ptr == 0 {
        return
    }
    unsafe "waiter_ptr is a live VkWaiterShared owned by st" {
        sh := raw.FromAddr[VkWaiterShared](st.waiter_ptr)
        (*sh).stop.Store(1)
        SignalControlSem(sh)
        if (*sh).started {
            sys.ThreadJoin((*sh).thread_handle)
        }
        dev := HandleFrom[vulkan.VkDeviceT](st.device)
        st.dev_d.vk_destroy_semaphore.Must()(dev, HandleFrom[vulkan.VkSemaphoreT]((*sh).control_sem), raw.Null[vulkan.VkAllocationCallbacks]())
        st.dev_d.vk_destroy_semaphore.Must()(dev, HandleFrom[vulkan.VkSemaphoreT]((*sh).timeline_sem), raw.Null[vulkan.VkAllocationCallbacks]())
        core.FreeBytes(a, raw.FromAddr[u8](st.waiter_ptr), SizeOf[VkWaiterShared](), AlignOf[VkWaiterShared]())
        st.waiter_ptr = 0
        st.timeline_sem = 0
    }
}

PollCompletedSerial(st: mut VkDeviceState): u64 {
    if st.waiter_ptr == 0 || st.timeline_sem == 0 {
        return 0
    }
    unsafe "waiter_ptr and timeline_sem are live" {
        sh := raw.FromAddr[VkWaiterShared](st.waiter_ptr)
        var val: u64 = 0
        r := st.dev_d.vk_get_semaphore_counter_value.Must()(HandleFrom[vulkan.VkDeviceT](st.device), HandleFrom[vulkan.VkSemaphoreT](st.timeline_sem), raw.OfMut(mut val))
        if r == vulkan.VkSuccess {
            return PublishCompleted(&(*sh).completed, val)
        }
        if IsDeviceLostResult(r) {
            st.lost = true
            (*sh).lost.Store(1)
        }
        return (*sh).completed.Load()
    }
}

WaitForSerial(st: mut VkDeviceState, io: Io, serial: u64): Error!void {
    if serial == 0 {
        return
    }
    if st.lost {
        fail DeviceLost(reason: st.loss_reason)
    }
    if PollCompletedSerial(st) >= serial {
        return
    }
    if st.waiter_ptr == 0 {
        fail DeviceLost(reason: st.loss_reason)
    }
    unsafe "waiter_ptr is a live VkWaiterShared" {
        sh := raw.FromAddr[VkWaiterShared](st.waiter_ptr)
        if (*sh).lost.Load() != 0 {
            st.lost = true
            fail DeviceLost(reason: st.loss_reason)
        }
        (*sh).target.Store(serial)
        SignalControlSem(sh)
        for {
            if PollCompletedSerial(st) >= serial {
                (*sh).target.Store(0)
                return
            }
            if (*sh).lost.Load() != 0 || st.lost {
                (*sh).target.Store(0)
                st.lost = true
                fail DeviceLost(reason: st.loss_reason)
            }
            (*sh).waker.Wait(io) else e {
                (*sh).target.Store(0)
                SignalControlSem(sh)
                fail e
            }
        }
    }
}

/// Retargets the waiter thread cleanly to a higher/cancelled target (used by tests).
export Device.WaiterSetTargetForTest(mut self, target: u64) {
    st := self.state.Mut()
    if st.waiter_ptr == 0 {
        return
    }
    unsafe "waiter_ptr is live" {
        sh := raw.FromAddr[VkWaiterShared](st.waiter_ptr)
        (*sh).target.Store(target)
        SignalControlSem(sh)
    }
}
