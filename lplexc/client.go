package lplexc

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Client communicates with an lplex server over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new lplex client pointing at the given server URL.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: http.DefaultClient,
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
	for _, m := range f.Manufacturers {
		v.Add("manufacturer", m)
	}
	for _, i := range f.Instances {
		v.Add("instance", strconv.FormatUint(uint64(i), 10))
	}
	for _, n := range f.Names {
		v.Add("name", n)
	}
	return v.Encode()
}
