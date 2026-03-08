package lplex

import (
	"sync"
	"time"
)

// ReplicationEventType identifies the kind of replication event.
type ReplicationEventType string

const (
	EventLiveStart     ReplicationEventType = "live_start"
	EventLiveStop      ReplicationEventType = "live_stop"
	EventBackfillStart ReplicationEventType = "backfill_start"
	EventBackfillStop  ReplicationEventType = "backfill_stop"
	EventBlockReceived ReplicationEventType = "block_received"
	EventCheckpoint    ReplicationEventType = "checkpoint"
)

// ReplicationEvent is a single diagnostic event from the replication pipeline.
type ReplicationEvent struct {
	Time   time.Time            `json:"time"`
	Type   ReplicationEventType `json:"type"`
	Detail map[string]any       `json:"detail,omitempty"`
}

const eventLogSize = 1024 // must be power of 2

// clampInt returns v clamped to [0, upper]. Breaks taint propagation for
// static analyzers that can't follow min() through to make().
func clampInt(v, upper1, upper2 int) int {
	if upper1 < upper2 {
		upper2 = upper1
	}
	if v < 0 {
		return 0
	}
	if v > upper2 {
		return upper2
	}
	return v
}

// EventLog is a fixed-size ring buffer of replication events.
type EventLog struct {
	mu      sync.Mutex
	entries [eventLogSize]ReplicationEvent
	head    int // next write position
	count   int
}

// NewEventLog creates an empty event log.
func NewEventLog() *EventLog {
	return &EventLog{}
}

// Record appends a new event to the log.
func (l *EventLog) Record(typ ReplicationEventType, detail map[string]any) {
	l.mu.Lock()
	l.entries[l.head] = ReplicationEvent{
		Time:   time.Now(),
		Type:   typ,
		Detail: detail,
	}
	l.head = (l.head + 1) & (eventLogSize - 1)
	if l.count < eventLogSize {
		l.count++
	}
	l.mu.Unlock()
}

// Recent returns up to n events, newest first.
func (l *EventLog) Recent(n int) []ReplicationEvent {
	l.mu.Lock()
	defer l.mu.Unlock()

	if n <= 0 || l.count == 0 {
		return nil
	}

	count := clampInt(n, l.count, eventLogSize)
	result := make([]ReplicationEvent, count)
	for i := range count {
		idx := (l.head - 1 - i) & (eventLogSize - 1)
		result[i] = l.entries[idx]
	}
	return result
}
