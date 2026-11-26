// Why: QUIC dial, accept, stream open and stream read/write operations are
// cancellable under structured concurrency; cancelling any of them mid-wait must
// return Canceled (or finish cleanly), leave no leaked resources or hangs, and
// allow healthy peers or subsequent repeatable reads to continue.
//
// What: TestStdQuic_SimCancel, testing cancel-safety over std.sim at 1 and 16
// carriers.
//
// How: copies std into a temp workspace, adds quic/zz_cancel_sim_test.mk with
// cut-point cancel sweeps over Connector.Dial / MuxListener.Accept and paused
// stream Reads, and runs `mk test --std=std.quic -run CANCEL-SIM:`.
// Tools/gate/safety.go lists TestStdQuic_SimCancel as a cancel-safety test.

package std

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const quicCancelSimSrc = `// Generated fixture from std/std-quic_cancel_test.go.
import std.net
import std.raw
import std.sim
import std.time
import std.tls

SimCancelAt(r: &Scope, io: Io, nap_us: i64) {
    _ = io.Sleep(time.Micros(nap_us)) else {}
    r.Cancel()
}

SimCancelServeTask(l: &net.MuxListener, io: Io, cut: bool, nap_us: i64, stage: &raw.Atomic[u32], failed: &raw.Atomic[u32]) {
    scope ss := io.Scope() {
        if cut {
            ss.go SimCancelAt(&ss, io, nap_us)
        }
        var sm := l.Accept(io) else e {
            match e {
                Canceled { stage.Store(300) }
                _ { stage.Store(400) }
            }
            ss.Cancel()
            return
        }
        var st := sm.Accept(io, false, true) else e {
            sm.Abandon()
            match e {
                Canceled { stage.Store(300) }
                _ { stage.Store(400) }
            }
            ss.Cancel()
            return
        }
        match st {
            none {
                sm.Abandon()
                stage.Store(400)
            }
            some(s0) {
                var rbuf: [16]u8 = [0; 16]
                _ = sm.Read(io, &s0, mut rbuf[..], true) else e {
                    sm.Release(s0)
                    sm.Abandon()
                    match e {
                        Canceled { stage.Store(300) }
                        _ { stage.Store(400) }
                    }
                    ss.Cancel()
                    return
                }
                _ = sm.Write(io, &s0, b"pong", true) else {}
                sm.Release(s0)
                sm.Close(io, 0) else {
                    sm.Abandon()
                }
                stage.Store(1)
            }
        }
        ss.Cancel()
    }
    _ = failed
}

SimCancelClientTask(h: &Heap, io: Io, ccfg: &Config, sa: net.Addr, cut: bool, nap_us: i64, stage: &raw.Atomic[u32], took_out: &raw.Atomic[u64]) {
    t0 := time.Now(io)
    scope cs := io.Scope() {
        if cut {
            cs.go SimCancelAt(&cs, io, nap_us)
        }
        var cn := Connector(ccfg)
        var cm := cn.Dial(h, io, sa, "localhost", "h3") else e {
            match e {
                Canceled { stage.Store(300) }
                _ { stage.Store(400) }
            }
            cs.Cancel()
            return
        }
        var st := cm.Open(io, false, true) else e {
            cm.Abandon()
            match e {
                Canceled { stage.Store(300) }
                _ { stage.Store(400) }
            }
            cs.Cancel()
            return
        }
        match st {
            none {
                cm.Abandon()
                stage.Store(400)
            }
            some(s0) {
                _ = cm.Write(io, &s0, b"ping", true) else {}
                var rbuf: [16]u8 = [0; 16]
                _ = cm.Read(io, &s0, mut rbuf[..], true) else { none }
                cm.Release(s0)
                cm.Close(io, 0) else {
                    cm.Abandon()
                }
                stage.Store(1)
            }
        }
        cs.Cancel()
    }
    took := time.Now(io).Since(t0).Nanos() / 1000
    if took > 0 {
        took_out.Store(Trunc[u64](took))
    }
}

struct CutRes {
    cli: u32
    srv: u32
    took: i64
}

RunHandshakeCut(h: &Heap, a: &Arena, seed: u64, cut_client: bool, nap_us: i64): net.Error|OutOfMemory|tls.Error!CutRes {
    var s := try sim.Sim.New(a, seed)
    defer s.Free()
    var nf := sim.DefaultNetFaults
    nf.drop = 0
    nf.duplicate = 0
    nf.latency_lo = time.Millis(1)
    nf.latency_hi = time.Millis(2)
    s.SetNetFaults(nf)
    io := s.Io()
    mat := BuildSimTlsConfigs(a, false, QtLeafPem, QtKeyPem, QtRootPem).Must()
    var roots := mat.roots
    var key := mat.key
    ids: [1]tls.Identity = [tls.Identity(chain: mat.chain_buf[0..mat.chain_len], key: &key)]
    var tls_srv := tls.DefaultConfig
    tls_srv.identities = ids[..]
    var tls_cli := tls.DefaultConfig
    tls_cli.roots = some(&roots)

    var scfg := DefaultConfig
    scfg.tls = some(&tls_srv)
    var ccfg := DefaultConfig
    ccfg.tls = some(&tls_cli)

    var ac := Acceptor(&scfg)
    var l := try ac.Listen(h, io, "127.0.0.1:0", "h3")
    defer l.Close()
    sa := net.Addr(ip: [127, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], port: Trunc[u16](l.Port()), v6: false)

    cli_st := raw.Atomic[u32].New(0)
    srv_st := raw.Atomic[u32].New(0)
    failed := raw.Atomic[u32].New(0)
    took_out := raw.Atomic[u64].New(0)

    scope outer := io.Scope() {
        outer.CancelAfter(time.Seconds(15))
        outer.go SimRunListenerTask(&l, io)
        outer.go SimCancelServeTask(&l, io, !cut_client && nap_us >= 0, nap_us, &srv_st, &failed)
        outer.go SimCancelClientTask(h, io, &ccfg, sa, cut_client && nap_us >= 0, nap_us, &cli_st, &took_out)
        for {
            done := if nap_us < 0 {
                cli_st.Load() != 0 && srv_st.Load() != 0
            } else if cut_client {
                cli_st.Load() != 0
            } else {
                srv_st.Load() != 0
            }
            if done {
                break
            }
            _ = io.Sleep(time.Micros(100)) else { break }
        }
        outer.Cancel()
    }
    Assert(failed.Load() == 0)
    return CutRes(cli: cli_st.Load(), srv: srv_st.Load(), took: Cast[i64](took_out.Load()))
}

SimPacedServeTask(l: &net.MuxListener, io: Io, total: int, failed: &raw.Atomic[u32]) {
    var sm := l.Accept(io) else {
        failed.Store(1)
        return
    }
    var st := sm.Accept(io, false, true) else {
        sm.Abandon()
        failed.Store(2)
        return
    }
    match st {
        none {
            sm.Abandon()
            failed.Store(3)
        }
        some(s0) {
            var chunk: [1000]u8 = [0; 1000]
            var off := 0
            for off < total {
                var n := total - off
                if n > 1000 {
                    n = 1000
                }
                for i in 0..n {
                    chunk[i] = Trunc[u8](((off + i) * 7 + 13) & 0xff)
                }
                off += n
                last := off == total
                sm.Write(io, &s0, chunk[0..n], last) else {
                    sm.Release(s0)
                    sm.Abandon()
                    failed.Store(4)
                    return
                }
                if !last {
                    _ = io.Sleep(time.Millis(2)) else {}
                }
            }
            var wait_close: [8]u8 = [0; 8]
            _ = sm.Read(io, &s0, mut wait_close[..], true) else { none }
            sm.Release(s0)
            sm.Close(io, 0) else {
                sm.Abandon()
            }
        }
    }
}

SimReadOneCancelled(cm: &net.Mux, st: &net.MuxStream, io: Io, out: mut []u8, got_n: &raw.Atomic[u32], cancelled: &raw.Atomic[u32]) {
    var pos := 0
    for pos < out.Len() {
        r := cm.Read(io, st, mut out[pos..], true) else e {
            match e {
                Canceled { cancelled.Store(1) }
                _ { cancelled.Store(2) }
            }
            got_n.Store(Trunc[u32](pos))
            return
        }
        match r {
            none {
                got_n.Store(Trunc[u32](pos))
                return
            }
            some(v) {
                if v == 0 {
                    got_n.Store(Trunc[u32](pos))
                    return
                }
                pos += v
            }
        }
    }
    got_n.Store(Trunc[u32](pos))
}

SimCancelStreamSweep(cm: &net.Mux, io: Io, total: int, failed: &raw.Atomic[u32], done: &raw.Atomic[u32]) {
    var mst := cm.Open(io, false, true) else {
        failed.Store(10)
        done.Store(1)
        return
    }
    match mst {
        none {
            failed.Store(11)
        }
        some(cst) {
            cm.Write(io, &cst, b"go", true) else {
                cm.Release(cst)
                failed.Store(12)
                done.Store(1)
                return
            }
            var acc: [6000]u8 = [0; 6000]
            var off := 0
            var saw_cancel := 0
            for off < total {
                got_n := raw.Atomic[u32].New(0)
                cancelled := raw.Atomic[u32].New(0)
                scope rsc := io.Scope() {
                    rsc.go SimCancelAt(&rsc, io, 1500)
                    SimReadOneCancelled(cm, &cst, io, mut acc[off..], &got_n, &cancelled)
                    rsc.Cancel()
                }
                if cancelled.Load() == 1 {
                    saw_cancel += 1
                }
                if cancelled.Load() == 2 {
                    failed.Store(13)
                    cm.Release(cst)
                    done.Store(1)
                    return
                }
                off += int(got_n.Load())
            }
            if off != total || saw_cancel < 1 {
                failed.Store(14)
            }
            for i in 0..total {
                want := Trunc[u8]((i * 7 + 13) & 0xff)
                if acc[i] != want {
                    failed.Store(15)
                    break
                }
            }
            cm.Release(cst)
        }
    }
    done.Store(1)
}

test "CANCEL-SIM: Connector.Dial and Acceptor.Accept cancel cleanly across cut points" (sys: &System) {
    h := sys.heap
    a := sys.arena
    whole := try RunHandshakeCut(h, a, 7101, true, -1)
    Assert(whole.cli == 1 && whole.srv == 1)
    Assert(whole.took > 0)
    step := whole.took / 12 + 1
    var cli_cuts := 0
    var srv_cuts := 0
    var nap: i64 = 0
    for nap <= whole.took + step {
        rc := try RunHandshakeCut(h, a, 7102, true, nap)
        Assert(rc.cli == 1 || rc.cli == 300)
        if rc.cli == 300 {
            cli_cuts += 1
        }
        rs := try RunHandshakeCut(h, a, 7103, false, nap)
        Assert(rs.srv == 1 || rs.srv == 300 || rs.srv == 400)
        if rs.srv == 300 {
            srv_cuts += 1
        }
        nap += step
    }
    Assert(cli_cuts >= 3)
    Assert(srv_cuts >= 3)
}

test "CANCEL-SIM: Mux stream Read cancelled repeatedly delivers complete byte stream" (sys: &System) {
    h := sys.heap
    a := sys.arena
    var s := try sim.Sim.New(a, 7201)
    defer s.Free()
    io := s.Io()
    mat := BuildSimTlsConfigs(a, false, QtLeafPem, QtKeyPem, QtRootPem).Must()
    var roots := mat.roots
    var key := mat.key
    ids: [1]tls.Identity = [tls.Identity(chain: mat.chain_buf[0..mat.chain_len], key: &key)]
    var tls_srv := tls.DefaultConfig
    tls_srv.identities = ids[..]
    var tls_cli := tls.DefaultConfig
    tls_cli.roots = some(&roots)

    var scfg := DefaultConfig
    scfg.tls = some(&tls_srv)
    var ccfg := DefaultConfig
    ccfg.tls = some(&tls_cli)

    var ac := Acceptor(&scfg)
    var l := try ac.Listen(h, io, "127.0.0.1:0", "h3")
    defer l.Close()
    sa := net.Addr(ip: [127, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], port: Trunc[u16](l.Port()), v6: false)

    total := 6000
    failed := raw.Atomic[u32].New(0)
    done := raw.Atomic[u32].New(0)
    scope sc := io.Scope() {
        sc.CancelAfter(time.Seconds(15))
        sc.go SimRunListenerTask(&l, io)
        sc.go SimPacedServeTask(&l, io, total, &failed)
        var cn := Connector(&ccfg)
        var cm := try cn.Dial(h, io, sa, "localhost", "h3")
        SimCancelStreamSweep(&cm, io, total, &failed, &done)
        cm.Close(io, 0) else {
            cm.Abandon()
        }
        sc.Cancel()
    }
    Assert(done.Load() == 1)
    Assert(failed.Load() == 0)
}
`

// TestStdQuic_SimCancel runs std.quic cancel-safety simulation tests at 1 and 16 carriers.
func TestStdQuic_SimCancel(t *testing.T) {
	for _, c := range []int{1, 16} {
		t.Run(fmt.Sprintf("carriers=%d", c), func(t *testing.T) {
			root := t.TempDir()
			stdDir := copyStd(t, root, map[string]string{
				"quic/zz_cancel_sim_test.mk": quicCancelSimSrc,
			})
			t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
			t.Setenv("MK_CARRIERS", fmt.Sprint(c))
			out, status := mkCmd(t, root, stdDir, "test", "--std=std.quic", "-run", "CANCEL-SIM:")
			if strings.Contains(out, "no usable LLVM") {
				t.Skipf("needs the LLVM toolchain: %s", out)
			}
			if status != 0 || !strings.Contains(out, "2 passed, 0 failed") {
				t.Fatalf("status %d\n%s", status, out)
			}
		})
	}
}
