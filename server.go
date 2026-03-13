package lplex

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// Server handles HTTP API requests for lplex.
type Server struct {
	broker     *Broker
	logger     *slog.Logger
	mux        *http.ServeMux
	sendPolicy SendPolicy
}

// NewServer creates a new HTTP server wired to the given broker.
func NewServer(broker *Broker, logger *slog.Logger, policy SendPolicy) *Server {
	s := &Server{
		broker:     broker,
		logger:     logger,
		mux:        http.NewServeMux(),
		sendPolicy: policy,
	}
	s.mux.HandleFunc("GET /events", s.HandleEphemeralSSE)
	s.mux.HandleFunc("PUT /clients/{clientId}", s.handleCreateSession)
	s.mux.HandleFunc("GET /clients/{clientId}/events", s.handleSSE)
	s.mux.HandleFunc("PUT /clients/{clientId}/ack", s.handleAck)
	s.mux.HandleFunc("POST /send", s.handleSend)
	s.mux.HandleFunc("POST /query", s.handleQuery)
	s.mux.HandleFunc("GET /devices", s.handleDevices)
	s.mux.HandleFunc("GET /values", s.handleValues)
	s.mux.HandleFunc("GET /values/decoded", s.handleDecodedValues)
	return s
}

// HandleFunc registers an additional HTTP handler on the server's mux.
func (s *Server) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	s.mux.HandleFunc(pattern, handler)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Expose-Headers", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Max-Age", "86400")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// clientIDPattern validates client IDs: alphanumeric, hyphens, underscores, 1-64 chars.
var clientIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// PUT /clients/{clientId}
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	clientID := r.PathValue("clientId")
	if !clientIDPattern.MatchString(clientID) {
		http.Error(w, "invalid client ID", http.StatusBadRequest)
		return
	}

	var req struct {
		BufferTimeout string `json:"buffer_timeout"`
		Filter        *struct {
			PGN          []uint32 `json:"pgn"`
			ExcludePGN   []uint32 `json:"exclude_pgn"`
			Manufacturer []string `json:"manufacturer"`
			Instance     []uint8  `json:"instance"`
			Name         []string `json:"name"`
			ExcludeName  []string `json:"exclude_name"`
		} `json:"filter"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var bufTimeout time.Duration
	if req.BufferTimeout != "" {
		parsed, err := ParseISO8601Duration(req.BufferTimeout)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid buffer_timeout: %v", err), http.StatusBadRequest)
			return
		}
		bufTimeout = parsed
	}

	var filter *EventFilter
	if req.Filter != nil {
		filter = &EventFilter{
			PGNs:          req.Filter.PGN,
			ExcludePGNs:   req.Filter.ExcludePGN,
			Manufacturers: req.Filter.Manufacturer,
			Instances:     req.Filter.Instance,
		}
		for _, nameHex := range req.Filter.Name {
			name, err := strconv.ParseUint(nameHex, 16, 64)
			if err != nil {
				http.Error(w, fmt.Sprintf("invalid CAN name %q: must be hex", nameHex), http.StatusBadRequest)
				return
			}
			filter.Names = append(filter.Names, name)
		}
		for _, nameHex := range req.Filter.ExcludeName {
			name, err := strconv.ParseUint(nameHex, 16, 64)
			if err != nil {
				http.Error(w, fmt.Sprintf("invalid exclude CAN name %q: must be hex", nameHex), http.StatusBadRequest)
				return
			}
			filter.ExcludeNames = append(filter.ExcludeNames, name)
		}
	}

	session, seq := s.broker.CreateSession(clientID, bufTimeout, filter)

	resp := struct {
		ClientID string   `json:"client_id"`
		Seq      uint64   `json:"seq"`
		Cursor   uint64   `json:"cursor,omitempty"`
		Devices  []Device `json:"devices"`
	}{
		ClientID: session.ID,
		Seq:      seq,
		Cursor:   session.Cursor,
		Devices:  s.broker.devices.Snapshot(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Error("failed to encode session response", "error", err)
	}
}

// GET /clients/{clientId}/events
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	clientID := r.PathValue("clientId")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	session := s.broker.GetSession(clientID)
	if session == nil {
		http.Error(w, "session not found, create it first with PUT /clients/{id}", http.StatusNotFound)
		return
	}

	// Determine starting cursor: resume from last ACK, or live-only.
	cursor := s.broker.CurrentSeq() + 1 // default: live only
	if session.Cursor > 0 {
		cursor = session.Cursor + 1 // resume after last ACK'd seq
	}

	consumer := s.broker.NewConsumer(ConsumerConfig{
		Cursor: cursor,
		Filter: session.Filter,
	})
	defer func() { _ = consumer.Close() }()

	s.broker.TouchSession(clientID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	ctx := r.Context()
	for {
		frame, err := consumer.Next(ctx)
		if err != nil {
			return
		}
		jsonBytes, err := frame.JSON()
		if err != nil {
			s.logger.Error("failed to serialize frame", "error", err)
			continue
		}
		fmt.Fprintf(w, "data: %s\n\n", jsonBytes)
		flusher.Flush()
	}
}

// HandleEphemeralSSE handles GET /events.
// Ephemeral SSE stream, no session, no ACK, no replay.
// Optional query params for filtering: pgn, manufacturer, instance, name (hex).
func (s *Server) HandleEphemeralSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	filter, err := ParseFilterParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sub, cleanup := s.broker.Subscribe(filter)
	defer cleanup()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-sub.ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// ParseFilterParams reads optional filter query params from a request.
// Supported params: pgn (decimal), manufacturer (name or code), instance (decimal),
// name (hex CAN NAME). Returns nil filter if no params are set.
func ParseFilterParams(r *http.Request) (*EventFilter, error) {
	q := r.URL.Query()
	pgns := q["pgn"]
	excludePGNs := q["exclude_pgn"]
	manufacturers := q["manufacturer"]
	instances := q["instance"]
	names := q["name"]
	excludeNames := q["exclude_name"]

	if len(pgns) == 0 && len(excludePGNs) == 0 && len(manufacturers) == 0 && len(instances) == 0 && len(names) == 0 && len(excludeNames) == 0 {
		return nil, nil
	}

	f := &EventFilter{
		Manufacturers: manufacturers,
	}

	for _, s := range pgns {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid pgn %q: %w", s, err)
		}
		f.PGNs = append(f.PGNs, uint32(v))
	}

	for _, s := range excludePGNs {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid exclude_pgn %q: %w", s, err)
		}
		f.ExcludePGNs = append(f.ExcludePGNs, uint32(v))
	}

	for _, s := range instances {
		v, err := strconv.ParseUint(s, 10, 8)
		if err != nil {
			return nil, fmt.Errorf("invalid instance %q: %w", s, err)
		}
		f.Instances = append(f.Instances, uint8(v))
	}

	for _, s := range names {
		v, err := strconv.ParseUint(s, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid name %q: must be hex", s)
		}
		f.Names = append(f.Names, v)
	}

	for _, s := range excludeNames {
		v, err := strconv.ParseUint(s, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid exclude_name %q: must be hex", s)
		}
		f.ExcludeNames = append(f.ExcludeNames, v)
	}

	if f.IsEmpty() {
		return nil, nil
	}
	return f, nil
}

// PUT /clients/{clientId}/ack
func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	clientID := r.PathValue("clientId")

	var req struct {
		Seq uint64 `json:"seq"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := s.broker.AckSession(clientID, req.Seq); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// overrideSource replaces the outgoing source address with the first claimed
// virtual device address, if virtual devices are configured. Returns false
// (and writes an HTTP error) if virtual devices exist but none are ready yet.
func (s *Server) overrideSource(w http.ResponseWriter, src *uint8) bool {
	vdm := s.broker.VirtualDevices()
	if vdm == nil {
		return true
	}
	addr, ok := vdm.ClaimedSource()
	if !ok || !vdm.Ready() {
		http.Error(w, "virtual device not ready (claiming address)", http.StatusServiceUnavailable)
		return false
	}
	*src = addr
	return true
}

// checkSendPolicy validates that the send policy allows a frame with the given
// PGN and destination address. Returns true if allowed, false if the request
// was rejected (with an appropriate HTTP error written).
func (s *Server) checkSendPolicy(w http.ResponseWriter, pgn uint32, dst uint8) bool {
	if !s.sendPolicy.Enabled {
		http.Error(w, "send is disabled", http.StatusForbidden)
		return false
	}

	// No rules = allow all (backwards compatible).
	if len(s.sendPolicy.Rules) == 0 {
		return true
	}

	// Resolve destination NAME for rule matching.
	var dstNAME uint64
	var nameKnown bool
	if dst != 0xFF {
		if dev := s.broker.devices.Get(dst); dev != nil {
			dstNAME = dev.NAME
			nameKnown = true
		}
	} else {
		// Broadcast: NAME-based rules don't constrain broadcasts.
		nameKnown = false
	}

	// Evaluate rules top-to-bottom, first match wins.
	for _, rule := range s.sendPolicy.Rules {
		if rule.Matches(pgn, dstNAME, nameKnown) {
			if rule.Allow {
				return true
			}
			http.Error(w, fmt.Sprintf("send denied by rule (pgn=%d, dst=%d)", pgn, dst), http.StatusForbidden)
			return false
		}
	}

	// No matching rule = deny.
	http.Error(w, fmt.Sprintf("no send rule matched (pgn=%d, dst=%d)", pgn, dst), http.StatusForbidden)
	return false
}

// POST /send
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PGN  uint32 `json:"pgn"`
		Src  uint8  `json:"src"`
		Dst  uint8  `json:"dst"`
		Prio uint8  `json:"prio"`
		Data string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !s.checkSendPolicy(w, req.PGN, req.Dst) {
		return
	}

	data, err := hex.DecodeString(req.Data)
	if err != nil {
		http.Error(w, "invalid hex data", http.StatusBadRequest)
		return
	}

	src := req.Src
	if !s.overrideSource(w, &src) {
		return
	}

	header := CANHeader{
		Priority:    req.Prio,
		PGN:         req.PGN,
		Source:      src,
		Destination: req.Dst,
	}

	if !s.broker.QueueTx(TxRequest{Header: header, Data: data}) {
		http.Error(w, "tx queue full", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// POST /query
// Sends an ISO Request (PGN 59904) asking devices to transmit the specified PGN,
// then waits for a matching response and returns the value.
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PGN     uint32 `json:"pgn"`
		Dst     uint8  `json:"dst"`
		Timeout string `json:"timeout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.PGN == 0 {
		http.Error(w, "pgn is required", http.StatusBadRequest)
		return
	}
	if req.Dst == 0 {
		req.Dst = 0xFF // broadcast
	}
	if !s.checkSendPolicy(w, req.PGN, req.Dst) {
		return
	}

	timeout := 2 * time.Second
	if req.Timeout != "" {
		d, err := ParseISO8601Duration(req.Timeout)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid timeout: %v", err), http.StatusBadRequest)
			return
		}
		timeout = d
	}

	// Check virtual device holdoff before sending.
	if vdm := s.broker.VirtualDevices(); vdm != nil && !vdm.Ready() {
		http.Error(w, "virtual device not ready (claiming address)", http.StatusServiceUnavailable)
		return
	}

	// Subscribe to matching frames before sending the request.
	filter := &EventFilter{PGNs: []uint32{req.PGN}}
	sub, cleanup := s.broker.Subscribe(filter)
	defer cleanup()

	// Send the ISO Request.
	if err := s.broker.SendISORequest(req.Dst, req.PGN); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Wait for a response frame.
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	select {
	case data := <-sub.ch:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	case <-ctx.Done():
		http.Error(w, "timeout waiting for response", http.StatusGatewayTimeout)
	}
}

// GET /devices
func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(s.broker.devices.SnapshotJSON()); err != nil {
		s.logger.Error("failed to write devices response", "error", err)
	}
}

// GET /values
func (s *Server) handleValues(w http.ResponseWriter, r *http.Request) {
	filter, err := ParseFilterParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(s.broker.values.SnapshotJSON(s.broker.devices, filter)); err != nil {
		s.logger.Error("failed to write values response", "error", err)
	}
}

// GET /values/decoded
func (s *Server) handleDecodedValues(w http.ResponseWriter, r *http.Request) {
	filter, err := ParseFilterParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(s.broker.values.DecodedSnapshotJSON(s.broker.devices, filter)); err != nil {
		s.logger.Error("failed to write decoded values response", "error", err)
	}
}

// ParseISO8601Duration parses a subset of ISO 8601 durations (PT format).
// Supports hours (H), minutes (M), and seconds (S).
// Examples: "PT5M", "PT1H30M", "PT30S", "PT1H"
func ParseISO8601Duration(s string) (time.Duration, error) {
	if len(s) < 2 || s[0] != 'P' {
		return 0, fmt.Errorf("must start with P")
	}

	var total time.Duration
	rest := s[1:]
	parsed := false
	inTime := false

	for len(rest) > 0 {
		if rest[0] == 'T' {
			inTime = true
			rest = rest[1:]
			continue
		}

		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 0 || i >= len(rest) {
			return 0, fmt.Errorf("invalid duration format: %s", s)
		}

		val := 0
		for _, c := range rest[:i] {
			val = val*10 + int(c-'0')
		}

		unit := rest[i]
		if inTime {
			switch unit {
			case 'H':
				total += time.Duration(val) * time.Hour
			case 'M':
				total += time.Duration(val) * time.Minute
			case 'S':
				total += time.Duration(val) * time.Second
			default:
				return 0, fmt.Errorf("unknown time unit: %c", unit)
			}
		} else {
			switch unit {
			case 'D':
				total += time.Duration(val) * 24 * time.Hour
			case 'W':
				total += time.Duration(val) * 7 * 24 * time.Hour
			default:
				return 0, fmt.Errorf("unknown date unit: %c (months/years not supported)", unit)
			}
		}
		parsed = true
		rest = rest[i+1:]
	}

	if !parsed {
		return 0, fmt.Errorf("invalid duration format: %s", s)
	}
	return total, nil
}
