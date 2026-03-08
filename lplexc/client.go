package lplexc

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Client communicates with an lplex server over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
	backoff    BackoffConfig
}

// NewClient creates a new lplex client pointing at the given server URL.
// Options configure connection pooling, logging, and reconnect behavior.
func NewClient(baseURL string, opts ...ClientOption) *Client {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: cfg.httpClient,
		logger:     cfg.logger,
		backoff:    cfg.reconnectBackoff,
	}
}

// Devices returns a snapshot of all NMEA 2000 devices discovered by the server.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/devices", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /devices returned %d: %s", resp.StatusCode, body)
	}

	var devices []Device
	if err := json.NewDecoder(resp.Body).Decode(&devices); err != nil {
		return nil, fmt.Errorf("decoding devices: %w", err)
	}
	return devices, nil
}

// Subscribe opens an ephemeral SSE stream (GET /events) with the given filter.
// Returns a Subscription that yields events until closed or the context is cancelled.
func (c *Client) Subscribe(ctx context.Context, filter *Filter) (*Subscription, error) {
	eventsURL := c.baseURL + "/events"

	if filter != nil && !filter.IsEmpty() {
		eventsURL += "?" + filterQueryParams(filter)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, eventsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET /events returned %d: %s", resp.StatusCode, body)
	}

	return newSubscription(resp.Body), nil
}

// Send transmits a CAN frame through the server.
func (c *Client) Send(ctx context.Context, pgn uint32, src, dst, prio uint8, data []byte) error {
	body := struct {
		PGN  uint32 `json:"pgn"`
		Src  uint8  `json:"src"`
		Dst  uint8  `json:"dst"`
		Prio uint8  `json:"prio"`
		Data string `json:"data"`
	}{
		PGN:  pgn,
		Src:  src,
		Dst:  dst,
		Prio: prio,
		Data: hex.EncodeToString(data),
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/send", strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("POST /send returned %d", resp.StatusCode)
	}
	return nil
}

// Values returns a snapshot of last-known values, optionally filtered.
func (c *Client) Values(ctx context.Context, filter *Filter) ([]DeviceValues, error) {
	u := c.baseURL + "/values"
	if filter != nil && !filter.IsEmpty() {
		u += "?" + filterQueryParams(filter)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /values returned %d: %s", resp.StatusCode, body)
	}

	var result []DeviceValues
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding values: %w", err)
	}
	return result, nil
}

// DecodedValues returns a snapshot of last-known values with PGN fields decoded.
func (c *Client) DecodedValues(ctx context.Context, filter *Filter) ([]DecodedDeviceValues, error) {
	u := c.baseURL + "/values/decoded"
	if filter != nil && !filter.IsEmpty() {
		u += "?" + filterQueryParams(filter)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /values/decoded returned %d: %s", resp.StatusCode, body)
	}

	var result []DecodedDeviceValues
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding values: %w", err)
	}
	return result, nil
}

// RequestPGN sends an ISO Request (PGN 59904) asking devices to transmit the
// specified PGN, and waits for the response. dst is the destination address
// (use 0xFF for broadcast). The server blocks until a matching frame arrives
// or the timeout expires.
func (c *Client) RequestPGN(ctx context.Context, pgn uint32, dst uint8) (*Frame, error) {
	body := struct {
		PGN uint32 `json:"pgn"`
		Dst uint8  `json:"dst"`
	}{
		PGN: pgn,
		Dst: dst,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/query", strings.NewReader(string(jsonBody)))
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
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST /query returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var f Frame
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &f, nil
}

// Subscription reads events from an SSE stream.
type Subscription struct {
	scanner *bufio.Scanner
	body    io.ReadCloser
}

func newSubscription(body io.ReadCloser) *Subscription {
	return &Subscription{
		scanner: bufio.NewScanner(body),
		body:    body,
	}
}

// Next blocks until the next event is available, the stream closes,
// or an error occurs. Returns io.EOF when the stream ends.
func (s *Subscription) Next() (Event, error) {
	for s.scanner.Scan() {
		line := s.scanner.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}

		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(data), &raw) != nil {
			continue
		}

		if _, hasType := raw["type"]; hasType {
			var dev Device
			if json.Unmarshal([]byte(data), &dev) == nil {
				return Event{Device: &dev}, nil
			}
			continue
		}

		var f Frame
		if json.Unmarshal([]byte(data), &f) == nil {
			return Event{Frame: &f}, nil
		}
	}

	if err := s.scanner.Err(); err != nil {
		return Event{}, err
	}
	return Event{}, io.EOF
}

// Close terminates the SSE stream.
func (s *Subscription) Close() error {
	return s.body.Close()
}

// filterQueryParams encodes a Filter as URL query parameters.
func filterQueryParams(f *Filter) string {
	v := url.Values{}
	for _, p := range f.PGNs {
		v.Add("pgn", strconv.FormatUint(uint64(p), 10))
	}
	for _, p := range f.ExcludePGNs {
		v.Add("exclude_pgn", strconv.FormatUint(uint64(p), 10))
	}
	for _, m := range f.Manufacturers {
		v.Add("manufacturer", m)
	}
	for _, i := range f.Instances {
		v.Add("instance", strconv.FormatUint(uint64(i), 10))
	}
	for _, n := range f.Names {
		v.Add("name", n)
	}
	for _, n := range f.ExcludeNames {
		v.Add("exclude_name", n)
	}
	return v.Encode()
}
