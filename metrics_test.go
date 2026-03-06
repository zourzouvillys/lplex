package lplex

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsHandler(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	// Inject a frame so counters are nonzero.
	injectFrame(b, 60928, 1, make([]byte, 8))
	time.Sleep(50 * time.Millisecond) // let broker process

	handler := MetricsHandler(b, nil)
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type: got %q, want text/plain", ct)
	}

	body := w.Body.String()
	for _, metric := range []string{
		"lplex_frames_total",
		"lplex_ring_buffer_entries",
		"lplex_ring_buffer_capacity",
		"lplex_ring_buffer_utilization",
		"lplex_broker_head_seq",
		"lplex_active_sessions",
		"lplex_active_subscribers",
		"lplex_active_consumers",
		"lplex_devices_total",
		"lplex_last_frame_timestamp_seconds",
	} {
		if !strings.Contains(body, metric) {
			t.Errorf("missing metric %q in output", metric)
		}
	}

	// Should not contain replication metrics when replStatus is nil.
	if strings.Contains(body, "lplex_replication_connected") {
		t.Error("unexpected replication metric when replStatus is nil")
	}
}

func TestMetricsHandlerWithReplication(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	replFn := func() *ReplicationStatus {
		return &ReplicationStatus{
			Connected:             true,
			LiveLag:               42,
			BackfillRemainingSeqs: 100,
			LastAck:               time.Now(),
		}
	}

	handler := MetricsHandler(b, replFn)
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	body := w.Body.String()
	for _, metric := range []string{
		"lplex_replication_connected 1",
		"lplex_replication_live_lag_seqs 42",
		"lplex_replication_backfill_remaining_seqs 100",
		"lplex_replication_last_ack_timestamp_seconds",
	} {
		if !strings.Contains(body, metric) {
			t.Errorf("missing replication metric %q in output", metric)
		}
	}
}
