package lplexc

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sixfathoms/lplex/pgn"
)

func TestNewClientDefaults(t *testing.T) {
	c := NewClient("http://localhost:8089")
	if c.baseURL != "http://localhost:8089" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
	if c.httpClient == nil {
		t.Fatal("httpClient is nil")
	}
	if c.logger == nil {
		t.Fatal("logger is nil")
	}
}

func TestNewClientTrimsSlash(t *testing.T) {
	c := NewClient("http://localhost:8089/")
	if c.baseURL != "http://localhost:8089" {
		t.Errorf("baseURL = %q, want without trailing slash", c.baseURL)
	}
}

func TestNewClientWithOptions(t *testing.T) {
	custom := &http.Client{Timeout: 5 * time.Second}
	logger := slog.Default()

	c := NewClient("http://localhost:8089",
		WithHTTPClient(custom),
		WithLogger(logger),
		WithBackoff(BackoffConfig{
			InitialInterval: 500 * time.Millisecond,
			MaxInterval:     10 * time.Second,
			MaxRetries:      3,
		}),
	)

	if c.httpClient != custom {
		t.Error("httpClient not set")
	}
	if c.backoff.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", c.backoff.MaxRetries)
	}
}

func TestWithPoolSize(t *testing.T) {
	c := NewClient("http://localhost:8089", WithPoolSize(20))
	tr, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport is not *http.Transport")
	}
	if tr.MaxIdleConnsPerHost != 20 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 20", tr.MaxIdleConnsPerHost)
	}
}

func TestDevices(t *testing.T) {
	devices := []Device{
		{Src: 1, Manufacturer: "Garmin", ModelID: "GPS 200"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/devices" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(devices)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Manufacturer != "Garmin" {
		t.Errorf("got %+v", got)
	}
}

func TestSubscribeAndNext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"seq\":1,\"ts\":\"2024-01-01T00:00:00Z\",\"prio\":2,\"pgn\":129025,\"src\":1,\"dst\":255,\"data\":\"abcd\"}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	sub, err := c.Subscribe(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	ev, err := sub.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Frame == nil {
		t.Fatal("expected frame")
	}
	if ev.Frame.PGN != 129025 {
		t.Errorf("PGN = %d, want 129025", ev.Frame.PGN)
	}
}

func TestSubscribeReconnect(t *testing.T) {
	var connectCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := connectCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"seq\":%d,\"ts\":\"2024-01-01T00:00:00Z\",\"prio\":2,\"pgn\":129025,\"src\":1,\"dst\":255,\"data\":\"abcd\"}\n\n", n)
		flusher.Flush()
		// Close after one event to force reconnect.
	}))
	defer srv.Close()

	c := NewClient(srv.URL, WithBackoff(BackoffConfig{
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     50 * time.Millisecond,
		MaxRetries:      0,
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := c.SubscribeReconnect(ctx, nil)

	// Read at least 2 events (proving reconnect happened).
	seen := 0
	for ev := range ch {
		if ev.Frame != nil {
			seen++
			if seen >= 2 {
				cancel()
				break
			}
		}
	}

	if seen < 2 {
		t.Errorf("saw %d events, want >= 2 (reconnect should produce more)", seen)
	}
	if connectCount.Load() < 2 {
		t.Errorf("connect count = %d, want >= 2", connectCount.Load())
	}
}

func TestSubscribeReconnectMaxRetries(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", WithBackoff(BackoffConfig{
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		MaxRetries:      2,
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := c.SubscribeReconnect(ctx, nil)

	// Channel should close after max retries exhausted.
	count := 0
	for range ch {
		count++
	}
	if count != 0 {
		t.Errorf("expected 0 events from unreachable server, got %d", count)
	}
}

func TestWatch(t *testing.T) {
	// Build a valid PositionRapidUpdate frame (PGN 129025).
	data := make([]byte, 8)
	binary.LittleEndian.PutUint32(data[0:4], uint32(int32(476062000)))  // 47.6062° lat
	lonRaw := int32(-1223321000)
	binary.LittleEndian.PutUint32(data[4:8], uint32(lonRaw)) // -122.3321° lon
	dataHex := hex.EncodeToString(data)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"seq\":1,\"ts\":\"2024-01-01T00:00:00Z\",\"prio\":2,\"pgn\":129025,\"src\":1,\"dst\":255,\"data\":\"%s\"}\n\n", dataHex)
		flusher.Flush()
	}))
	defer srv.Close()

	c := NewClient(srv.URL, WithBackoff(BackoffConfig{
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     50 * time.Millisecond,
		MaxRetries:      1,
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := c.Watch(ctx, 129025)
	if err != nil {
		t.Fatal(err)
	}

	wv := <-ch
	cancel()

	pos, ok := wv.Value.(pgn.PositionRapidUpdate)
	if !ok {
		t.Fatalf("expected PositionRapidUpdate, got %T", wv.Value)
	}
	if pos.Latitude < 47.0 || pos.Latitude > 48.0 {
		t.Errorf("latitude = %f, want ~47.6", pos.Latitude)
	}
	if pos.Longitude > -122.0 || pos.Longitude < -123.0 {
		t.Errorf("longitude = %f, want ~-122.3", pos.Longitude)
	}
}

func TestWatchUnknownPGN(t *testing.T) {
	c := NewClient("http://localhost:8089")
	_, err := c.Watch(context.Background(), 999999)
	if err == nil {
		t.Fatal("expected error for unknown PGN")
	}
}

func TestNextBackoff(t *testing.T) {
	tests := []struct {
		current, max, want time.Duration
	}{
		{1 * time.Second, 30 * time.Second, 2 * time.Second},
		{16 * time.Second, 30 * time.Second, 30 * time.Second},
		{30 * time.Second, 30 * time.Second, 30 * time.Second},
	}
	for _, tt := range tests {
		got := nextBackoff(tt.current, tt.max)
		if got != tt.want {
			t.Errorf("nextBackoff(%v, %v) = %v, want %v", tt.current, tt.max, got, tt.want)
		}
	}
}

func TestFilterQueryParams(t *testing.T) {
	f := &Filter{
		PGNs:          []uint32{129025, 130306},
		Manufacturers: []string{"Garmin"},
	}
	params := filterQueryParams(f)
	if params == "" {
		t.Fatal("empty params")
	}
	// Check that pgn values are present.
	if !containsSubstring(params, "pgn=129025") {
		t.Errorf("missing pgn=129025 in %q", params)
	}
	if !containsSubstring(params, "manufacturer=Garmin") {
		t.Errorf("missing manufacturer=Garmin in %q", params)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsHelper(s, sub))
}

func containsHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
