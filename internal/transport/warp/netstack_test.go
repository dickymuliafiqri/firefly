package warp

import (
	"context"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

// teardownTun builds a bare userspace stack: no WireGuard device, no Cloudflare
// registration, no network. Everything these tests exercise happens inside the
// tun's packet handoff.
func teardownTun(t *testing.T) (*netTun, *Net) {
	t.Helper()
	dev, tnet, err := CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr("172.16.0.2")},
		[]netip.Addr{netip.MustParseAddr("1.1.1.1")},
		1420,
	)
	if err != nil {
		t.Fatalf("CreateNetTUN: %v", err)
	}
	return dev.(*netTun), tnet
}

// inboundUDP builds a valid IPv4+UDP datagram from the internet to the tunnel
// address. The stack answers each one with an ICMP port-unreachable, i.e. an
// outbound packet handed to the tun reader — the same handoff WireGuard's
// receiver performs.
func inboundUDP() [][]byte {
	pkt := make([]byte, header.IPv4MinimumSize+header.UDPMinimumSize)
	ip := header.IPv4(pkt)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(len(pkt)),
		TTL:         64,
		Protocol:    uint8(header.UDPProtocolNumber),
		SrcAddr:     tcpip.AddrFrom4([4]byte{1, 1, 1, 1}),
		DstAddr:     tcpip.AddrFrom4([4]byte{172, 16, 0, 2}),
	})
	ip.SetChecksum(0)
	ip.SetChecksum(^ip.CalculateChecksum())
	udp := header.UDP(ip.Payload())
	udp.Encode(&header.UDPFields{SrcPort: 5353, DstPort: 9999, Length: header.UDPMinimumSize})
	udp.SetChecksum(0) // zero == no checksum for UDP; the stack accepts it
	return [][]byte{pkt}
}

// TestNetTunTeardownUnderTrafficIsPanicFree is the regression for
// "panic: send on closed channel" in netTun.WriteNotify. Teardown used to run
// while goroutines were parked on the unbuffered incomingPacket handoff, which
// took the whole process down; it is fatal and unrecoverable because the
// panicking goroutine is not one Firefly owns.
func TestNetTunTeardownUnderTrafficIsPanicFree(t *testing.T) {
	dev, _ := teardownTun(t)

	// The only consumer of the handoff, and deliberately slow so that senders
	// have to queue — the state that used to panic.
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		bufs := make([][]byte, 1)
		bufs[0] = make([]byte, 65535)
		sizes := make([]int, 1)
		for {
			if _, err := dev.Read(bufs, sizes, 0); err != nil {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	stop := make(chan struct{})
	var senders sync.WaitGroup
	var sent atomic.Int64
	for i := 0; i < 8; i++ {
		senders.Add(1)
		go func() {
			defer senders.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := dev.Write(inboundUDP(), 0); err != nil {
					return
				}
				sent.Add(1)
			}
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sent.Load() < 50 {
		time.Sleep(10 * time.Millisecond)
	}
	if sent.Load() == 0 {
		close(stop)
		senders.Wait()
		t.Fatal("no packet could be injected; the test would not prove anything")
	}

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		_ = dev.Close()
	}()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("teardown blocked behind a packet handoff (deadlock regression)")
	}

	close(stop)
	senders.Wait()
	readers.Wait()
}

// TestNetTunTeardownWithoutReaderReturns is the regression for the teardown
// deadlock: Session.Close stops the WireGuard routines first, so the stack
// teardown that follows emits RSTs with nobody left to read them.
func TestNetTunTeardownWithoutReaderReturns(t *testing.T) {
	dev, tnet := teardownTun(t)

	// One dialling client: its SYN is handed to the tun, which has no reader.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		if c, err := tnet.DialContext(ctx, "tcp", "1.1.1.1:80"); err == nil {
			_ = c.Close()
		}
	}()
	time.Sleep(100 * time.Millisecond)

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		_ = dev.Close()
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("teardown never returned: the session close would hang drainWG.Wait()")
	}
}

// TestNetTunCloseIsIdempotent covers several draining goroutines racing the same
// teardown, which is exactly what a rotation plus a shutdown produces.
func TestNetTunCloseIsIdempotent(t *testing.T) {
	dev, _ := teardownTun(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = dev.Close()
		}()
	}
	wg.Wait()
}

// TestNetTunClosedDeviceFailsClosed proves a torn-down tunnel reports a clean
// error instead of touching the stack, so a racing request cannot reach it.
func TestNetTunClosedDeviceFailsClosed(t *testing.T) {
	dev, tnet := teardownTun(t)
	if err := dev.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := dev.Write(inboundUDP(), 0); err == nil {
		t.Fatal("Write after Close must fail")
	} else if err != os.ErrClosed {
		t.Fatalf("Write after Close: got %v, want %v", err, os.ErrClosed)
	}

	bufs := [][]byte{make([]byte, 1600)}
	sizes := make([]int, 1)
	if _, err := dev.Read(bufs, sizes, 0); err != os.ErrClosed {
		t.Fatalf("Read after Close: got %v, want %v", err, os.ErrClosed)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := tnet.DialContext(ctx, "tcp", "1.1.1.1:80"); err == nil {
		t.Fatal("dial after Close must fail")
	}
}

// TestNetTunResolvesNumericHost guards the vendored resolver path that dialling
// depends on: the gateway dials provider hostnames through this stack.
func TestNetTunResolvesNumericHost(t *testing.T) {
	_, tnet := teardownTun(t)
	addrs, err := tnet.LookupHost("1.1.1.1")
	if err != nil {
		t.Fatalf("LookupHost: %v", err)
	}
	if len(addrs) != 1 || addrs[0] != "1.1.1.1" {
		t.Fatalf("LookupHost(1.1.1.1) = %v", addrs)
	}
}
