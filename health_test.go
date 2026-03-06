package lplex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthHandler_OK(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	injectFrame(b, 129025, 1, make([]byte, 8))
	time.Sleep(50 * time.Millisecond)

	handler := HealthHandler(HealthConfig{Broker: b})
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var h HealthStatus
	if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}

	if h.Status != "ok" {
		t.Errorf("status: got %q, want %q", h.Status, "ok")
	}
	if h.Broker.Status != "ok" {
		t.Errorf("broker status: got %q, want %q", h.Broker.Status, "ok")
	}
	if h.Broker.FramesTotal != 1 {
		t.Errorf("frames_total: got %d, want 1", h.Broker.FramesTotal)
	}
	if h.Replication != nil {
		t.Error("unexpected replication field when not configured")
	}
}

func TestHealthHandler_BusSilence(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	// Inject a frame with a timestamp in the past.
	b.rxFrames <- RxFrame{
		Timestamp: time.Now().Add(-2 * time.Minute),
		Header:    CANHeader{PGN: 129025, Source: 1},
		Data:      make([]byte, 8),
	}
	time.Sleep(50 * time.Millisecond)

	handler := HealthHandler(HealthConfig{
		Broker:              b,
		BusSilenceThreshold: 30 * time.Second,
	})
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	var h HealthStatus
	if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}

	if h.Status != "degraded" {
		t.Errorf("status: got %q, want %q", h.Status, "degraded")
	}
	comp, ok := h.Components["can_bus"]
	if !ok {
		t.Fatal("missing can_bus component")
	}
	if comp.Status != "silent" {
		t.Errorf("can_bus status: got %q, want %q", comp.Status, "silent")
	}
}

func TestHealthHandler_ReplicationDisconnected(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	replFn := func() *ReplicationStatus {
		return &ReplicationStatus{Connected: false, LiveLag: 500}
	}

	handler := HealthHandler(HealthConfig{
		Broker:     b,
		ReplStatus: replFn,
	})
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	var h HealthStatus
	if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}

	if h.Status != "degraded" {
		t.Errorf("status: got %q, want %q", h.Status, "degraded")
	}
	if h.Replication == nil {
		t.Fatal("missing replication field")
	}
	if h.Replication.Status != "disconnected" {
		t.Errorf("replication status: got %q, want %q", h.Replication.Status, "disconnected")
	}
}

func TestBrokerStats(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	// Stats before any frames.
	s := b.Stats()
	if s.FramesTotal != 0 {
		t.Errorf("FramesTotal before frames: got %d, want 0", s.FramesTotal)
	}
	if s.RingCapacity != 1024 {
		t.Errorf("RingCapacity: got %d, want 1024", s.RingCapacity)
	}

	// Inject frames.
	for i := range 5 {
		injectFrame(b, 129025, uint8(i), make([]byte, 8))
	}
	time.Sleep(50 * time.Millisecond)

	s = b.Stats()
	if s.FramesTotal != 5 {
		t.Errorf("FramesTotal: got %d, want 5", s.FramesTotal)
	}
	if s.RingEntries != 5 {
		t.Errorf("RingEntries: got %d, want 5", s.RingEntries)
	}
	if s.LastFrameTime.IsZero() {
		t.Error("LastFrameTime should not be zero after frames")
	}
}
