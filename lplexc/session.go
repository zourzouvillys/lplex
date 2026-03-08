package lplexc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SessionConfig configures a buffered client session.
type SessionConfig struct {
	ClientID      string
	BufferTimeout string // ISO 8601 duration, e.g. "PT5M"
	Filter        *Filter
	AckInterval   time.Duration // how often to auto-ACK (0 = manual only)
}

// SessionInfo is the response from creating or reconnecting a session.
type SessionInfo struct {
	ClientID string   `json:"client_id"`
	Seq      uint64   `json:"seq"`
	Cursor   uint64   `json:"cursor"`
	Devices  []Device `json:"devices"`
}

// Session is a buffered connection to an lplex server with cursor tracking
// and automatic reconnect replay.
type Session struct {
	client    *Client
	config    SessionConfig
	info      SessionInfo
	lastAcked uint64
}

// CreateSession creates or reconnects a buffered session on the server.
func (c *Client) CreateSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	sessionURL := c.baseURL + "/clients/" + cfg.ClientID

	putBody := map[string]any{"buffer_timeout": cfg.BufferTimeout}
	if cfg.Filter != nil && !cfg.Filter.IsEmpty() {
		putBody["filter"] = filterSessionJSON(cfg.Filter)
	}
	body, _ := json.Marshal(putBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PUT /clients/%s returned %d: %s", cfg.ClientID, resp.StatusCode, errBody)
	}

	var info SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decoding session: %w", err)
	}

	return &Session{
		client: c,
		config: cfg,
		info:   info,
	}, nil
}

// Info returns the session metadata from the server.
func (s *Session) Info() SessionInfo {
	return s.info
}

// Subscribe opens the SSE stream for this session (GET /clients/{id}/events).
// Replays any buffered frames from the cursor, then streams live.
func (s *Session) Subscribe(ctx context.Context) (*Subscription, error) {
	eventsURL := s.client.baseURL + "/clients/" + s.config.ClientID + "/events"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, eventsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := s.client.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET /clients/%s/events returned %d: %s", s.config.ClientID, resp.StatusCode, body)
	}

	return newSubscription(resp.Body), nil
}

// Ack advances the cursor for this session to the given sequence number.
func (s *Session) Ack(ctx context.Context, seq uint64) error {
	ackURL := s.client.baseURL + "/clients/" + s.config.ClientID + "/ack"
	body, _ := json.Marshal(map[string]uint64{"seq": seq})

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, ackURL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.httpClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("ack returned %d", resp.StatusCode)
	}
	s.lastAcked = seq
	return nil
}

// LastAcked returns the last sequence number that was successfully acknowledged.
func (s *Session) LastAcked() uint64 {
	return s.lastAcked
}

// filterSessionJSON converts a Filter to the JSON body format used by PUT /clients/{id}.
func filterSessionJSON(f *Filter) map[string]any {
	m := map[string]any{}
	if len(f.PGNs) > 0 {
		m["pgn"] = f.PGNs
	}
	if len(f.ExcludePGNs) > 0 {
		m["exclude_pgn"] = f.ExcludePGNs
	}
	if len(f.Manufacturers) > 0 {
		m["manufacturer"] = f.Manufacturers
	}
	if len(f.Instances) > 0 {
		m["instance"] = f.Instances
	}
	if len(f.Names) > 0 {
		m["name"] = f.Names
	}
	if len(f.ExcludeNames) > 0 {
		m["exclude_name"] = f.ExcludeNames
	}
	return m
}
