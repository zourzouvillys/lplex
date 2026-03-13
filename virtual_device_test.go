package lplex

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

// testVDM sets up a VirtualDeviceManager with a captured tx log.
type testVDM struct {
	mgr      *VirtualDeviceManager
	registry *DeviceRegistry
	txLog    []TxRequest
}

func newTestVDM() *testVDM {
	t := &testVDM{
		registry: NewDeviceRegistry(),
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	t.mgr = NewVirtualDeviceManager(func(req TxRequest) {
		t.txLog = append(t.txLog, req)
	}, t.registry, logger, 0, 0)
	return t
}

// registerDevice adds a device to the registry at the given source address.
func (t *testVDM) registerDevice(src uint8, name uint64) {
	data := make([]byte, 8)
	putUint64LE(data, name)
	t.registry.HandleAddressClaim(src, data)
}

func putUint64LE(b []byte, v uint64) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
	b[4] = byte(v >> 32)
	b[5] = byte(v >> 40)
	b[6] = byte(v >> 48)
	b[7] = byte(v >> 56)
}

func TestVirtualDeviceClaimLifecycle(t *testing.T) {
	tv := newTestVDM()

	// Add a virtual device.
	tv.mgr.Add(VirtualDeviceConfig{
		NAME:        0x00E0170001000004,
		ProductInfo: VirtualProductInfo{ModelID: "lplex-test"},
	})

	// No devices on the bus, so address 252 should be picked.
	tv.mgr.StartAfterDiscovery(0)

	if len(tv.txLog) != 1 {
		t.Fatalf("expected 1 tx (claim), got %d", len(tv.txLog))
	}
	claim := tv.txLog[0]
	if claim.Header.PGN != 60928 {
		t.Errorf("expected PGN 60928, got %d", claim.Header.PGN)
	}
	if claim.Header.Source != 252 {
		t.Errorf("expected source 252, got %d", claim.Header.Source)
	}

	// Not ready yet (in_progress, holdoff hasn't elapsed).
	if tv.mgr.Ready() {
		t.Error("should not be ready during holdoff")
	}

	// Simulate echo of our claim. This transitions to ClaimHeld.
	handled := tv.mgr.HandleBusClaim(252, 0x00E0170001000004)
	if !handled {
		t.Error("echo should be handled")
	}

	// ClaimedSource works immediately after echo (state is Held).
	src, ok := tv.mgr.ClaimedSource()
	if !ok || src != 252 {
		t.Errorf("expected claimed source 252, got %d (ok=%v)", src, ok)
	}

	// Still not Ready because the 250ms holdoff hasn't elapsed.
	if tv.mgr.Ready() {
		t.Error("should not be ready during holdoff even after echo")
	}

	// Fast-forward past the holdoff.
	tv.mgr.mu.Lock()
	tv.mgr.devices[0].readyAt = time.Now().Add(-1 * time.Second)
	tv.mgr.mu.Unlock()

	if !tv.mgr.Ready() {
		t.Error("should be ready after holdoff expires")
	}
}

func TestVirtualDeviceClaimedSource(t *testing.T) {
	tv := newTestVDM()
	tv.mgr.Add(VirtualDeviceConfig{NAME: 0x1234})

	// Not claimed yet.
	if _, ok := tv.mgr.ClaimedSource(); ok {
		t.Error("should not have a claimed source before start")
	}

	tv.mgr.StartAfterDiscovery(0)

	// Simulate echo to transition to Held.
	tv.mgr.HandleBusClaim(252, 0x1234)

	src, ok := tv.mgr.ClaimedSource()
	if !ok || src != 252 {
		t.Errorf("expected claimed source 252, got %d (ok=%v)", src, ok)
	}
}

func TestVirtualDeviceConflictWeLose(t *testing.T) {
	tv := newTestVDM()

	// Our NAME is higher (worse priority).
	tv.mgr.Add(VirtualDeviceConfig{NAME: 0xFFFFFFFFFFFFFFFF})
	tv.mgr.StartAfterDiscovery(0)

	if len(tv.txLog) != 1 {
		t.Fatalf("expected 1 tx, got %d", len(tv.txLog))
	}
	claimedAddr := tv.txLog[0].Header.Source

	// Another device claims same address with a lower NAME (they win).
	handled := tv.mgr.HandleBusClaim(claimedAddr, 0x0000000000000001)
	if handled {
		t.Error("conflict should not suppress broker processing of the winner's claim")
	}

	// We should have sent a new claim on a different address.
	if len(tv.txLog) < 2 {
		t.Fatalf("expected re-claim tx, got %d total", len(tv.txLog))
	}
	newClaim := tv.txLog[len(tv.txLog)-1]
	if newClaim.Header.Source == claimedAddr {
		t.Error("should have picked a different address after losing conflict")
	}
}

func TestVirtualDeviceConflictWeWin(t *testing.T) {
	tv := newTestVDM()

	// Our NAME is lower (we win conflicts).
	tv.mgr.Add(VirtualDeviceConfig{NAME: 0x0000000000000001})
	tv.mgr.StartAfterDiscovery(0)

	claimedAddr := tv.txLog[0].Header.Source
	txBefore := len(tv.txLog)

	// Another device claims same address with a higher NAME (we win).
	handled := tv.mgr.HandleBusClaim(claimedAddr, 0xFFFFFFFFFFFFFFFF)
	if handled {
		t.Error("conflict should not suppress broker processing")
	}

	// We should re-assert our claim on the same address.
	if len(tv.txLog) <= txBefore {
		t.Fatal("expected re-assertion tx")
	}
	reassert := tv.txLog[len(tv.txLog)-1]
	if reassert.Header.Source != claimedAddr {
		t.Errorf("re-assertion should be on same address %d, got %d", claimedAddr, reassert.Header.Source)
	}
}

func TestVirtualDeviceAddressAvoidance(t *testing.T) {
	tv := newTestVDM()

	// Populate the registry with a device at address 252.
	tv.registerDevice(252, 0xAAAA)

	tv.mgr.Add(VirtualDeviceConfig{NAME: 0xBBBB})
	tv.mgr.StartAfterDiscovery(0)

	if len(tv.txLog) == 0 {
		t.Fatal("expected at least one claim tx")
	}
	if tv.txLog[0].Header.Source == 252 {
		t.Error("should not claim address 252, it's already taken")
	}
	if tv.txLog[0].Header.Source != 251 {
		t.Errorf("expected address 251 (next free below 252), got %d", tv.txLog[0].Header.Source)
	}
}

func TestVirtualDeviceMultiDevice(t *testing.T) {
	tv := newTestVDM()

	tv.mgr.Add(VirtualDeviceConfig{NAME: 0x1111})
	tv.mgr.Add(VirtualDeviceConfig{NAME: 0x2222})
	tv.mgr.StartAfterDiscovery(0)

	if len(tv.txLog) != 2 {
		t.Fatalf("expected 2 claim txs, got %d", len(tv.txLog))
	}

	// They should claim different addresses.
	addr1 := tv.txLog[0].Header.Source
	addr2 := tv.txLog[1].Header.Source
	if addr1 == addr2 {
		t.Errorf("two virtual devices claimed the same address: %d", addr1)
	}
}

func TestVirtualDeviceISORequestAddressClaim(t *testing.T) {
	tv := newTestVDM()
	tv.mgr.Add(VirtualDeviceConfig{NAME: 0xDEAD})
	tv.mgr.StartAfterDiscovery(0)

	// Force to held state.
	tv.mgr.mu.Lock()
	tv.mgr.devices[0].state = ClaimHeld
	tv.mgr.devices[0].readyAt = time.Now().Add(-1 * time.Second)
	tv.mgr.mu.Unlock()

	txBefore := len(tv.txLog)
	tv.mgr.HandleISORequest(252, 60928, 100) // someone at src=100 requests PGN 60928

	if len(tv.txLog) <= txBefore {
		t.Fatal("expected address claim response")
	}
	resp := tv.txLog[len(tv.txLog)-1]
	if resp.Header.PGN != 60928 {
		t.Errorf("expected PGN 60928, got %d", resp.Header.PGN)
	}
}

func TestVirtualDeviceISORequestProductInfo(t *testing.T) {
	tv := newTestVDM()
	tv.mgr.Add(VirtualDeviceConfig{
		NAME: 0xBEEF,
		ProductInfo: VirtualProductInfo{
			ModelID:         "lplex-test",
			SoftwareVersion: "1.0.0",
			ProductCode:     42,
		},
	})
	tv.mgr.StartAfterDiscovery(0)

	tv.mgr.mu.Lock()
	tv.mgr.devices[0].state = ClaimHeld
	tv.mgr.mu.Unlock()

	txBefore := len(tv.txLog)
	tv.mgr.HandleISORequest(252, 126996, 100)

	if len(tv.txLog) <= txBefore {
		t.Fatal("expected product info response")
	}
	resp := tv.txLog[len(tv.txLog)-1]
	if resp.Header.PGN != 126996 {
		t.Errorf("expected PGN 126996, got %d", resp.Header.PGN)
	}
	if len(resp.Data) != 134 {
		t.Errorf("expected 134 bytes product info, got %d", len(resp.Data))
	}
}

func TestVirtualDeviceISORequestWrongDst(t *testing.T) {
	tv := newTestVDM()
	tv.mgr.Add(VirtualDeviceConfig{NAME: 0xCAFE})
	tv.mgr.StartAfterDiscovery(0)

	tv.mgr.mu.Lock()
	tv.mgr.devices[0].state = ClaimHeld
	claimedSrc := tv.mgr.devices[0].source
	tv.mgr.mu.Unlock()

	txBefore := len(tv.txLog)
	// Request addressed to a different device.
	wrongDst := claimedSrc - 1
	tv.mgr.HandleISORequest(wrongDst, 60928, 100)

	if len(tv.txLog) != txBefore {
		t.Error("should not respond to ISO request addressed to another device")
	}
}

func TestVirtualDeviceISORequestBroadcast(t *testing.T) {
	tv := newTestVDM()
	tv.mgr.Add(VirtualDeviceConfig{NAME: 0xFACE})
	tv.mgr.StartAfterDiscovery(0)

	tv.mgr.mu.Lock()
	tv.mgr.devices[0].state = ClaimHeld
	tv.mgr.mu.Unlock()

	txBefore := len(tv.txLog)
	tv.mgr.HandleISORequest(255, 60928, 100) // broadcast

	if len(tv.txLog) <= txBefore {
		t.Fatal("should respond to broadcast ISO request")
	}
}

func TestVirtualDeviceNoClaimedSource(t *testing.T) {
	tv := newTestVDM()
	// No devices added.
	_, ok := tv.mgr.ClaimedSource()
	if ok {
		t.Error("should return false when no devices configured")
	}
}

func TestVirtualDeviceNotReadyDuringHoldoff(t *testing.T) {
	tv := newTestVDM()
	tv.mgr.Add(VirtualDeviceConfig{NAME: 0x1234})
	tv.mgr.StartAfterDiscovery(0)

	// Device is in ClaimInProgress, not ready.
	if tv.mgr.Ready() {
		t.Error("should not be ready during claim in-progress")
	}
}

func TestVirtualDeviceHeartbeat(t *testing.T) {
	tv := newTestVDM()
	tv.mgr.Add(VirtualDeviceConfig{
		NAME:        0x1234,
		ProductInfo: VirtualProductInfo{ModelID: "hb-test"},
	})
	tv.mgr.StartAfterDiscovery(0)

	// Transition to Held via echo.
	tv.mgr.HandleBusClaim(252, 0x1234)

	txBefore := len(tv.txLog)

	// Heartbeat should be a no-op right after claim (intervals haven't elapsed).
	tv.mgr.Heartbeat()
	if len(tv.txLog) != txBefore {
		t.Errorf("heartbeat too early: got %d txs, want %d", len(tv.txLog), txBefore)
	}

	// Wind back the claim timer past the 60s threshold.
	tv.mgr.lastClaimAt = time.Now().Add(-61 * time.Second)
	tv.mgr.Heartbeat()

	// Should have sent one address claim.
	if len(tv.txLog) != txBefore+1 {
		t.Fatalf("expected 1 claim heartbeat tx, got %d new", len(tv.txLog)-txBefore)
	}
	if tv.txLog[len(tv.txLog)-1].Header.PGN != 60928 {
		t.Errorf("expected PGN 60928, got %d", tv.txLog[len(tv.txLog)-1].Header.PGN)
	}

	// Product info should not have been sent yet (5min interval).
	txBefore = len(tv.txLog)
	tv.mgr.lastProductInfoAt = time.Now().Add(-4 * time.Minute) // not expired yet
	tv.mgr.Heartbeat()
	if len(tv.txLog) != txBefore {
		t.Errorf("product info sent too early: got %d txs, want %d", len(tv.txLog), txBefore)
	}

	// Wind back the product info timer past 5min.
	tv.mgr.lastProductInfoAt = time.Now().Add(-6 * time.Minute)
	tv.mgr.lastClaimAt = time.Now() // reset claim so we only get product info
	tv.mgr.Heartbeat()

	if len(tv.txLog) != txBefore+1 {
		t.Fatalf("expected 1 product info heartbeat tx, got %d new", len(tv.txLog)-txBefore)
	}
	if tv.txLog[len(tv.txLog)-1].Header.PGN != 126996 {
		t.Errorf("expected PGN 126996, got %d", tv.txLog[len(tv.txLog)-1].Header.PGN)
	}
}

func TestVirtualDeviceHeartbeatSkipsNonHeld(t *testing.T) {
	tv := newTestVDM()
	tv.mgr.Add(VirtualDeviceConfig{NAME: 0x5678})
	tv.mgr.StartAfterDiscovery(0)

	// Device is in ClaimInProgress (no echo yet). Heartbeat should not send anything.
	tv.mgr.lastClaimAt = time.Time{} // force interval to have elapsed
	txBefore := len(tv.txLog)

	tv.mgr.Heartbeat()
	if len(tv.txLog) != txBefore {
		t.Error("heartbeat should skip devices that aren't in ClaimHeld state")
	}
}

func TestVirtualDeviceHeartbeatMultiDevice(t *testing.T) {
	tv := newTestVDM()
	tv.mgr.Add(VirtualDeviceConfig{NAME: 0xAAAA})
	tv.mgr.Add(VirtualDeviceConfig{NAME: 0xBBBB})
	tv.mgr.StartAfterDiscovery(0)

	// Transition both to Held.
	tv.mgr.HandleBusClaim(252, 0xAAAA)
	tv.mgr.HandleBusClaim(251, 0xBBBB)

	txBefore := len(tv.txLog)

	// Wind back both timers.
	tv.mgr.lastClaimAt = time.Time{}
	tv.mgr.lastProductInfoAt = time.Time{}
	tv.mgr.Heartbeat()

	// Should send claim + product info for each device = 4 txs.
	newTxs := len(tv.txLog) - txBefore
	if newTxs != 4 {
		t.Fatalf("expected 4 heartbeat txs (2 claims + 2 product infos), got %d", newTxs)
	}
}

func TestVirtualDeviceDiagnostics(t *testing.T) {
	tv := newTestVDM()
	tv.mgr.Add(VirtualDeviceConfig{
		NAME:        0xABCD,
		ProductInfo: VirtualProductInfo{ModelID: "test-model"},
	})
	tv.mgr.StartAfterDiscovery(0)

	devices := tv.mgr.Devices()
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0].NAME != "000000000000abcd" {
		t.Errorf("unexpected NAME: %s", devices[0].NAME)
	}
	if devices[0].ModelID != "test-model" {
		t.Errorf("unexpected ModelID: %s", devices[0].ModelID)
	}
}
