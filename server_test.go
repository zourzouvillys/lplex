package lplex

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestServer() (*Server, *Broker) {
	b := newTestBroker()
	go b.Run()
	s := NewServer(b, b.logger)
	return s, b
}

func TestCreateSession(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	req := httptest.NewRequest("PUT", "/clients/helm", strings.NewReader(`{"buffer_timeout":"PT5M"}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var resp struct {
		ClientID string   `json:"client_id"`
		Seq      uint64   `json:"seq"`
		Devices  []Device `json:"devices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ClientID != "helm" {
		t.Errorf("client_id: got %q, want %q", resp.ClientID, "helm")
	}
}

func TestCreateSessionBadID(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	// Use URL-encoded space, which is a valid URL but should fail validation
	req := httptest.NewRequest("PUT", "/clients/bad%20client", strings.NewReader(`{"buffer_timeout":"PT5M"}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}

func TestCreateSessionBadDuration(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	req := httptest.NewRequest("PUT", "/clients/helm", strings.NewReader(`{"buffer_timeout":"invalid"}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}

func TestCreateSessionDefaultDuration(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	req := httptest.NewRequest("PUT", "/clients/helm", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
}

func TestSSEStream(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	// Create session first
	createReq := httptest.NewRequest("PUT", "/clients/sse-test", strings.NewReader(`{"buffer_timeout":"PT1M"}`))
	createW := httptest.NewRecorder()
	srv.ServeHTTP(createW, createReq)
	if createW.Code != http.StatusOK {
		t.Fatalf("create session: got %d", createW.Code)
	}

	// Start SSE in a goroutine since it blocks
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/clients/sse-test/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content-type: got %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}

	// Inject a frame
	injectFrame(b, 129025, 1, []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22})
	time.Sleep(50 * time.Millisecond)

	// Read from SSE stream
	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(bufio.ScanLines)

	done := make(chan string, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				done <- line[6:] // strip "data: "
				return
			}
		}
	}()

	select {
	case data := <-done:
		var msg frameJSON
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			t.Fatal(err)
		}
		if msg.PGN != 129025 {
			t.Errorf("PGN: got %d, want 129025", msg.PGN)
		}
		if msg.Seq == 0 {
			t.Error("seq should not be 0")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for SSE event")
	}
}

func TestSSENotFound(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/clients/nonexistent/events")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

func TestAck(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	// Create session
	createReq := httptest.NewRequest("PUT", "/clients/ack-test", strings.NewReader(`{"buffer_timeout":"PT1M"}`))
	srv.ServeHTTP(httptest.NewRecorder(), createReq)

	// ACK
	ackReq := httptest.NewRequest("PUT", "/clients/ack-test/ack", strings.NewReader(`{"seq":42}`))
	ackW := httptest.NewRecorder()
	srv.ServeHTTP(ackW, ackReq)

	if ackW.Code != http.StatusNoContent {
		t.Fatalf("ack status: got %d, want 204", ackW.Code)
	}
}

func TestAckNotFound(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	ackReq := httptest.NewRequest("PUT", "/clients/ghost/ack", strings.NewReader(`{"seq":42}`))
	ackW := httptest.NewRecorder()
	srv.ServeHTTP(ackW, ackReq)

	if ackW.Code != http.StatusNotFound {
		t.Fatalf("ack status: got %d, want 404", ackW.Code)
	}
}

func TestSend(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	body := `{"pgn":59904,"src":254,"dst":255,"prio":6,"data":"00ee00"}`
	req := httptest.NewRequest("POST", "/send", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("send status: got %d, want 202", w.Code)
	}

	// Check that the frame landed in txFrames
	select {
	case tx := <-b.txFrames:
		if tx.Header.PGN != 59904 {
			t.Errorf("PGN: got %d, want 59904", tx.Header.PGN)
		}
		if tx.Header.Source != 254 {
			t.Errorf("source: got %d, want 254", tx.Header.Source)
		}
		if len(tx.Data) != 3 {
			t.Errorf("data length: got %d, want 3", len(tx.Data))
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for tx frame")
	}
}

func TestSendBadHex(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	body := `{"pgn":59904,"src":254,"dst":255,"prio":6,"data":"not-hex"}`
	req := httptest.NewRequest("POST", "/send", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("send status: got %d, want 400", w.Code)
	}
}

func TestDevices(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	// Add a device to the registry
	data := make([]byte, 8)
	var name uint64
	name |= uint64(229) << 21 // Garmin
	binary.LittleEndian.PutUint64(data, name)
	b.devices.HandleAddressClaim(1, data)

	req := httptest.NewRequest("GET", "/devices", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("devices status: got %d, want 200", w.Code)
	}

	var devices []Device
	if err := json.Unmarshal(w.Body.Bytes(), &devices); err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0].Manufacturer != "Garmin" {
		t.Errorf("manufacturer: got %q, want Garmin", devices[0].Manufacturer)
	}
}

func TestParseISO8601Duration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
		err   bool
	}{
		{"PT5M", 5 * time.Minute, false},
		{"PT1H", time.Hour, false},
		{"PT30S", 30 * time.Second, false},
		{"PT1H30M", 90 * time.Minute, false},
		{"PT2H15M30S", 2*time.Hour + 15*time.Minute + 30*time.Second, false},
		{"", 0, true},
		{"5M", 0, true},
		{"PT", 0, true},
		{"PT0S", 0, false},
		{"PT0H", 0, false},
		{"PT0H0M0S", 0, false},
		{"PTXM", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseISO8601Duration(tt.input)
			if tt.err {
				if err == nil {
					t.Errorf("expected error for %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSSEReplay(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Create session
	createReq := httptest.NewRequest("PUT", "/clients/replay-test", strings.NewReader(`{"buffer_timeout":"PT1M"}`))
	srv.ServeHTTP(httptest.NewRecorder(), createReq)

	// Inject 5 frames
	for i := range 5 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	// ACK seq 3 (session cursor = 3)
	if err := b.AckSession("replay-test", 3); err != nil {
		t.Fatalf("ack: %v", err)
	}

	// Inject 3 more frames (seq 6, 7, 8)
	for i := range 3 {
		injectFrame(b, 129025, 1, []byte{byte(i + 10), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	// Connect via SSE. Consumer should start from cursor+1=4 and replay through ring.
	resp, err := http.Get(ts.URL + "/clients/replay-test/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	scanner := bufio.NewScanner(resp.Body)
	var seqs []uint64

	done := make(chan struct{})
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var msg frameJSON
				_ = json.Unmarshal([]byte(line[6:]), &msg)
				if msg.Seq > 0 {
					seqs = append(seqs, msg.Seq)
					if len(seqs) >= 5 {
						close(done)
						return
					}
				}
			}
		}
	}()

	select {
	case <-done:
		// Should have replayed 4, 5, 6, 7, 8
		if seqs[0] != 4 {
			t.Errorf("first replayed seq: got %d, want 4", seqs[0])
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout, only got %d events: %v", len(seqs), seqs)
	}
}

func TestCreateSessionWithFilter(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	body := `{"buffer_timeout":"PT5M","filter":{"pgn":[129025,129026],"manufacturer":["Garmin"]}}`
	req := httptest.NewRequest("PUT", "/clients/filtered", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var resp struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ClientID != "filtered" {
		t.Errorf("client_id: got %q, want %q", resp.ClientID, "filtered")
	}

	// Verify filter was stored on the session.
	b.sessionMu.RLock()
	session := b.sessions["filtered"]
	b.sessionMu.RUnlock()

	if session.Filter == nil {
		t.Fatal("filter should not be nil")
	}
	if len(session.Filter.PGNs) != 2 {
		t.Errorf("PGNs: got %d, want 2", len(session.Filter.PGNs))
	}
	if len(session.Filter.Manufacturers) != 1 || session.Filter.Manufacturers[0] != "Garmin" {
		t.Errorf("manufacturers: got %v, want [Garmin]", session.Filter.Manufacturers)
	}
}

func TestCreateSessionWithCANNameFilter(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	body := `{"buffer_timeout":"PT1M","filter":{"name":["001c6e4000200000"]}}`
	req := httptest.NewRequest("PUT", "/clients/name-filter", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	b.sessionMu.RLock()
	session := b.sessions["name-filter"]
	b.sessionMu.RUnlock()

	if session.Filter == nil || len(session.Filter.Names) != 1 {
		t.Fatal("expected 1 CAN NAME in filter")
	}
	if session.Filter.Names[0] != 0x001c6e4000200000 {
		t.Errorf("name: got %016x, want 001c6e4000200000", session.Filter.Names[0])
	}
}

func TestCreateSessionBadCANName(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	body := `{"buffer_timeout":"PT1M","filter":{"name":["not-hex"]}}`
	req := httptest.NewRequest("PUT", "/clients/bad-name", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}

func TestCreateSessionBufferTimeoutZero(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	// Create session with normal timeout and ACK some frames.
	req := httptest.NewRequest("PUT", "/clients/reset-me", strings.NewReader(`{"buffer_timeout":"PT1M"}`))
	srv.ServeHTTP(httptest.NewRecorder(), req)

	if err := b.AckSession("reset-me", 42); err != nil {
		t.Fatalf("ack: %v", err)
	}

	// Re-create with buffer_timeout=0 -> should reset cursor.
	req = httptest.NewRequest("PUT", "/clients/reset-me", strings.NewReader(`{"buffer_timeout":"PT0S"}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	b.sessionMu.RLock()
	session := b.sessions["reset-me"]
	b.sessionMu.RUnlock()

	if session.Cursor != 0 {
		t.Errorf("cursor should be 0 after PT0S reset, got %d", session.Cursor)
	}
}

func TestEphemeralSSE(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content-type: got %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}

	// Inject a frame.
	injectFrame(b, 129025, 1, []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22})
	time.Sleep(50 * time.Millisecond)

	scanner := bufio.NewScanner(resp.Body)
	done := make(chan string, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				done <- line[6:]
				return
			}
		}
	}()

	select {
	case data := <-done:
		var msg frameJSON
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			t.Fatal(err)
		}
		if msg.PGN != 129025 {
			t.Errorf("PGN: got %d, want 129025", msg.PGN)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ephemeral SSE event")
	}
}

func TestEphemeralSSEWithFilter(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/events?pgn=129025")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Inject matching and non-matching frames.
	injectFrame(b, 129025, 1, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129026, 2, []byte{0xBB, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	scanner := bufio.NewScanner(resp.Body)
	done := make(chan frameJSON, 5)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var msg frameJSON
				if json.Unmarshal([]byte(line[6:]), &msg) == nil && msg.Seq > 0 {
					done <- msg
				}
			}
		}
	}()

	select {
	case msg := <-done:
		if msg.PGN != 129025 {
			t.Errorf("PGN: got %d, want 129025", msg.PGN)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for filtered frame")
	}

	// Should not get the 129026 frame.
	select {
	case msg := <-done:
		t.Errorf("should not receive PGN %d through filter", msg.PGN)
	case <-time.After(200 * time.Millisecond):
		// good
	}
}

func TestCreateSessionDefaultDurationIsZero(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	req := httptest.NewRequest("PUT", "/clients/zero-default", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	b.sessionMu.RLock()
	session := b.sessions["zero-default"]
	b.sessionMu.RUnlock()

	if session.BufferTimeout != 0 {
		t.Errorf("default buffer_timeout should be 0, got %v", session.BufferTimeout)
	}
}

func TestCreateSessionEmptyFilter(t *testing.T) {
	srv, b := newTestServer()
	defer close(b.rxFrames)

	// Empty filter object should be treated as nil (no filtering).
	body := `{"buffer_timeout":"PT1M","filter":{}}`
	req := httptest.NewRequest("PUT", "/clients/empty-filter", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	b.sessionMu.RLock()
	session := b.sessions["empty-filter"]
	b.sessionMu.RUnlock()

	if session.Filter != nil {
		t.Errorf("empty filter should be normalized to nil, got %+v", session.Filter)
	}
}
