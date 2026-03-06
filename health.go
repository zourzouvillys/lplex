package lplex

import (
	"encoding/json"
	"net/http"
	"time"
)

// HealthStatus is the structured response from the /healthz endpoint.
type HealthStatus struct {
	Status      string                    `json:"status"` // "ok", "degraded", or "unhealthy"
	Broker      BrokerHealth              `json:"broker"`
	Replication *ReplicationHealth        `json:"replication,omitempty"`
	Components  map[string]ComponentHealth `json:"components,omitempty"`
}

// BrokerHealth reports the broker's health.
type BrokerHealth struct {
	Status        string    `json:"status"` // "ok" or "unhealthy"
	FramesTotal   uint64    `json:"frames_total"`
	HeadSeq       uint64    `json:"head_seq"`
	LastFrameTime time.Time `json:"last_frame_time,omitempty"`
	DeviceCount   int       `json:"device_count"`
	RingEntries   uint64    `json:"ring_entries"`
	RingCapacity  int       `json:"ring_capacity"`
}

// ReplicationHealth reports the replication client's health.
type ReplicationHealth struct {
	Status               string    `json:"status"` // "ok", "degraded", or "disconnected"
	Connected            bool      `json:"connected"`
	LiveLag              uint64    `json:"live_lag"`
	BackfillRemaining    uint64    `json:"backfill_remaining_seqs"`
	LastAck              time.Time `json:"last_ack,omitempty"`
}

// ComponentHealth is a generic component status.
type ComponentHealth struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// HealthConfig configures the health check endpoint.
type HealthConfig struct {
	Broker     *Broker
	ReplStatus func() *ReplicationStatus // nil if replication not configured

	// BusSilenceThreshold is the duration after which no frames indicates
	// a CAN bus problem. Zero disables bus silence detection.
	BusSilenceThreshold time.Duration
}

// HealthHandler returns an http.HandlerFunc that serves the /healthz endpoint.
func HealthHandler(cfg HealthConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats := cfg.Broker.Stats()
		status := buildHealth(stats, cfg.ReplStatus, cfg.BusSilenceThreshold)

		w.Header().Set("Content-Type", "application/json")
		switch status.Status {
		case "ok":
			w.WriteHeader(http.StatusOK)
		case "degraded":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(status)
	}
}

func buildHealth(stats BrokerStats, replStatus func() *ReplicationStatus, silenceThreshold time.Duration) HealthStatus {
	h := HealthStatus{
		Status: "ok",
		Broker: BrokerHealth{
			Status:        "ok",
			FramesTotal:   stats.FramesTotal,
			HeadSeq:       stats.HeadSeq,
			LastFrameTime: stats.LastFrameTime,
			DeviceCount:   stats.DeviceCount,
			RingEntries:   stats.RingEntries,
			RingCapacity:  stats.RingCapacity,
		},
	}

	// Check bus silence.
	if silenceThreshold > 0 && stats.FramesTotal > 0 && !stats.LastFrameTime.IsZero() {
		if time.Since(stats.LastFrameTime) > silenceThreshold {
			h.Status = "degraded"
			if h.Components == nil {
				h.Components = make(map[string]ComponentHealth)
			}
			h.Components["can_bus"] = ComponentHealth{
				Status:  "silent",
				Message: "no frames received within silence threshold",
			}
		}
	}

	// Replication health.
	if replStatus != nil {
		rs := replStatus()
		if rs != nil {
			rh := &ReplicationHealth{
				Connected:         rs.Connected,
				LiveLag:           rs.LiveLag,
				BackfillRemaining: rs.BackfillRemainingSeqs,
				LastAck:           rs.LastAck,
			}
			if rs.Connected {
				rh.Status = "ok"
			} else {
				rh.Status = "disconnected"
				if h.Status == "ok" {
					h.Status = "degraded"
				}
			}
			h.Replication = rh
		}
	}

	return h
}
