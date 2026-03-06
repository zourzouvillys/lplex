package lplexc

import (
	"context"
	"time"
)

// SubscribeReconnect opens an auto-reconnecting SSE stream with the given filter.
// Returns a channel of events that is closed when the context is cancelled.
// On disconnect, it reconnects with exponential backoff using the client's
// configured BackoffConfig.
func (c *Client) SubscribeReconnect(ctx context.Context, filter *Filter) <-chan Event {
	ch := make(chan Event, 64)
	go c.reconnectLoop(ctx, filter, ch)
	return ch
}

func (c *Client) reconnectLoop(ctx context.Context, filter *Filter, ch chan<- Event) {
	defer close(ch)

	interval := c.backoff.InitialInterval
	retries := 0

	for {
		if ctx.Err() != nil {
			return
		}

		sub, err := c.Subscribe(ctx, filter)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logger.Debug("reconnect: subscribe failed", "error", err, "backoff", interval)
			if !c.sleepBackoff(ctx, &interval, &retries) {
				return
			}
			continue
		}

		// Reset backoff on successful connection.
		interval = c.backoff.InitialInterval
		retries = 0

		c.drainSubscription(ctx, sub, ch)
		_ = sub.Close()

		if ctx.Err() != nil {
			return
		}

		c.logger.Debug("reconnect: stream ended, reconnecting", "backoff", interval)
		if !c.sleepBackoff(ctx, &interval, &retries) {
			return
		}
	}
}

func (c *Client) drainSubscription(ctx context.Context, sub *Subscription, ch chan<- Event) {
	for {
		if ctx.Err() != nil {
			return
		}

		ev, err := sub.Next()
		if err != nil {
			return
		}

		select {
		case ch <- ev:
		case <-ctx.Done():
			return
		}
	}
}

// sleepDuration is exported for testing.
func nextBackoff(current time.Duration, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		next = max
	}
	return next
}
