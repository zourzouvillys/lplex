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
		_ = json.NewEncoder(w).Encode(devices)
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
	defer func() { _ = sub.Close() }()

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

func TestValues(t *testing.T) {
	values := []DeviceValues{
		{
			Name:   "0x0000000000e50000",
			Source: 1,
			Values: []PGNValue{{PGN: 129025, Ts: "2024-01-01T00:00:00Z", Data: "abcd", Seq: 1}},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/values" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(values)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.Values(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Source != 1 {
		t.Errorf("got %+v", got)
	}
	if len(got[0].Values) != 1 || got[0].Values[0].PGN != 129025 {
		t.Errorf("values = %+v", got[0].Values)
	}
}

func TestValuesWithFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/values" {
			http.NotFound(w, r)
			return
		}
		// Verify filter params were passed.
		if r.URL.Query().Get("pgn") != "129025" {
			t.Errorf("expected pgn=129025, got %q", r.URL.Query().Get("pgn"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]DeviceValues{})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.Values(context.Background(), &Filter{PGNs: []uint32{129025}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDecodedValues(t *testing.T) {
	values := []DecodedDeviceValues{
		{
			Source: 1,
			Values: []DecodedPGNValue{{PGN: 129025, Description: "Position Rapid Update", Ts: "2024-01-01T00:00:00Z", Seq: 1, Fields: map[string]any{"latitude": 47.6}}},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/values/decoded" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(values)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.DecodedValues(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Source != 1 {
		t.Errorf("got %+v", got)
	}
}

func TestRequestPGN(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/query" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			PGN uint32 `json:"pgn"`
			Dst uint8  `json:"dst"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if req.PGN != 129025 {
			t.Errorf("expected pgn=129025, got %d", req.PGN)
		}

		frame := Frame{
			Seq:  42,
			Ts:   "2024-01-01T00:00:00Z",
			Prio: 2,
			PGN:  129025,
			Src:  1,
			Dst:  255,
			Data: "abcd1234",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(frame)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	f, err := c.RequestPGN(context.Background(), 129025, 0xFF)
	if err != nil {
		t.Fatal(err)
	}
	if f.PGN != 129025 {
		t.Errorf("PGN = %d, want 129025", f.PGN)
	}
	if f.Src != 1 {
		t.Errorf("Src = %d, want 1", f.Src)
	}
}

func TestRequestPGNTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "timeout waiting for response", http.StatusGatewayTimeout)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.RequestPGN(context.Background(), 129025, 0xFF)
	if err == nil {
		t.Fatal("expected error for timeout")
	}
}

func TestFilterQueryParams(t *testing.T) {
	f := &Filter{
		PGNs:          []uint32{129025, 130306},
		ExcludePGNs:   []uint32{60928, 126996},
		Manufacturers: []string{"Garmin"},
	}
	params := filterQueryParams(f)
	if params == "" {
		t.Fatal("empty params")
	}
	for _, want := range []string{
		"pgn=129025",
		"pgn=130306",
		"exclude_pgn=60928",
		"exclude_pgn=126996",
		"manufacturer=Garmin",
	} {
		if !containsSubstring(params, want) {
			t.Errorf("missing %q in %q", want, params)
		}
	}
}

func TestFilterSessionJSON(t *testing.T) {
	f := &Filter{
		PGNs:          []uint32{129025},
		ExcludePGNs:   []uint32{60928, 126996},
		Manufacturers: []string{"Garmin"},
		Instances:     []uint8{2},
		Names:         []string{"deadbeef"},
	}
	m := filterSessionJSON(f)

	if pgns, ok := m["pgn"]; !ok {
		t.Error("missing pgn")
	} else if got := pgns.([]uint32); len(got) != 1 || got[0] != 129025 {
		t.Errorf("pgn = %v, want [129025]", got)
	}

	if ep, ok := m["exclude_pgn"]; !ok {
		t.Error("missing exclude_pgn")
	} else if got := ep.([]uint32); len(got) != 2 || got[0] != 60928 || got[1] != 126996 {
		t.Errorf("exclude_pgn = %v, want [60928 126996]", got)
	}

	if mfr, ok := m["manufacturer"]; !ok {
		t.Error("missing manufacturer")
	} else if got := mfr.([]string); len(got) != 1 || got[0] != "Garmin" {
		t.Errorf("manufacturer = %v, want [Garmin]", got)
	}

	if inst, ok := m["instance"]; !ok {
		t.Error("missing instance")
	} else if got := inst.([]uint8); len(got) != 1 || got[0] != 2 {
		t.Errorf("instance = %v, want [2]", got)
	}

	if names, ok := m["name"]; !ok {
		t.Error("missing name")
	} else if got := names.([]string); len(got) != 1 || got[0] != "deadbeef" {
		t.Errorf("name = %v, want [deadbeef]", got)
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
