package lplexc

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/sixfathoms/lplex/pgn"
)

// WatchValue is a decoded PGN value delivered by Watch.
type WatchValue struct {
	// Frame is the raw frame metadata.
	Frame Frame

	// Value is the decoded PGN struct (e.g. pgn.PositionRapidUpdate).
	// Use a type assertion to access the typed fields.
	Value any
}

// Watch opens an auto-reconnecting SSE stream filtered to the given PGN
// and returns a channel of decoded values. The channel is closed when the
// context is cancelled. Decode errors are silently skipped (logged at debug level).
//
// Example:
//
//	ch := client.Watch(ctx, 129025) // Position Rapid Update
//	for wv := range ch {
//	    pos := wv.Value.(pgn.PositionRapidUpdate)
//	    fmt.Printf("lat=%f lon=%f\n", pos.Latitude, pos.Longitude)
//	}
func (c *Client) Watch(ctx context.Context, pgnNumber uint32) (<-chan WatchValue, error) {
	info, ok := pgn.Registry[pgnNumber]
	if !ok {
		return nil, fmt.Errorf("PGN %d is not in the decoder registry", pgnNumber)
	}

	ch := make(chan WatchValue, 64)
	go c.watchLoop(ctx, pgnNumber, info, ch)
	return ch, nil
}

func (c *Client) watchLoop(ctx context.Context, pgnNumber uint32, info pgn.PGNInfo, ch chan<- WatchValue) {
	defer close(ch)

	filter := &Filter{PGNs: []uint32{pgnNumber}}
	interval := c.backoff.InitialInterval
	retries := 0

	for {
		if err := ctx.Err(); err != nil {
			return
		}

		sub, err := c.Subscribe(ctx, filter)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logger.Debug("watch: subscribe failed, retrying", "pgn", pgnNumber, "error", err, "backoff", interval)
			if !c.sleepBackoff(ctx, &interval, &retries) {
				return
			}
			continue
		}

		// Reset backoff on successful connection.
		interval = c.backoff.InitialInterval
		retries = 0

		c.readSubscription(ctx, sub, info, ch)
		_ = sub.Close()

		// If context is done, exit without retrying.
		if ctx.Err() != nil {
			return
		}

		c.logger.Debug("watch: stream ended, reconnecting", "pgn", pgnNumber, "backoff", interval)
		if !c.sleepBackoff(ctx, &interval, &retries) {
			return
		}
	}
}

func (c *Client) readSubscription(ctx context.Context, sub *Subscription, info pgn.PGNInfo, ch chan<- WatchValue) {
	for {
		if ctx.Err() != nil {
			return
		}

		ev, err := sub.Next()
		if err != nil {
			return
		}

		if ev.Frame == nil {
			continue
		}

		data, err := hex.DecodeString(ev.Frame.Data)
		if err != nil {
			c.logger.Debug("watch: hex decode failed", "error", err)
			continue
		}

		decoded, err := info.Decode(data)
		if err != nil {
			c.logger.Debug("watch: pgn decode failed", "pgn", info.PGN, "error", err)
			continue
		}

		select {
		case ch <- WatchValue{Frame: *ev.Frame, Value: decoded}:
		case <-ctx.Done():
			return
		}
	}
}

// sleepBackoff waits for the current backoff interval, then advances the
// interval for next time. Returns false if the context was cancelled or
// max retries exceeded.
func (c *Client) sleepBackoff(ctx context.Context, interval *time.Duration, retries *int) bool {
	if c.backoff.MaxRetries > 0 && *retries >= c.backoff.MaxRetries {
		return false
	}

	t := time.NewTimer(*interval)
	defer t.Stop()

	select {
	case <-t.C:
	case <-ctx.Done():
		return false
	}

	*retries++
	*interval *= 2
	if *interval > c.backoff.MaxInterval {
		*interval = c.backoff.MaxInterval
	}
	return true
}
