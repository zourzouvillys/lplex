package lplex

import (
	"fmt"
	"io"
	"net/http"
)

// MetricsHandler returns an http.HandlerFunc that serves Prometheus-format
// metrics from the broker's Stats(). Optional ReplicationStatus can be
// provided via a callback for replication-aware deployments.
func MetricsHandler(broker *Broker, replStatus func() *ReplicationStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats := broker.Stats()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeMetrics(w, stats, replStatus)
	}
}

func writeMetrics(w io.Writer, s BrokerStats, replStatus func() *ReplicationStatus) {
	// Frame throughput
	fmt.Fprintf(w, "# HELP lplex_frames_total Total CAN frames processed by the broker.\n")
	fmt.Fprintf(w, "# TYPE lplex_frames_total counter\n")
	fmt.Fprintf(w, "lplex_frames_total %d\n", s.FramesTotal)

	// Ring buffer
	fmt.Fprintf(w, "# HELP lplex_ring_buffer_entries Current number of entries in the ring buffer.\n")
	fmt.Fprintf(w, "# TYPE lplex_ring_buffer_entries gauge\n")
	fmt.Fprintf(w, "lplex_ring_buffer_entries %d\n", s.RingEntries)

	fmt.Fprintf(w, "# HELP lplex_ring_buffer_capacity Total capacity of the ring buffer.\n")
	fmt.Fprintf(w, "# TYPE lplex_ring_buffer_capacity gauge\n")
	fmt.Fprintf(w, "lplex_ring_buffer_capacity %d\n", s.RingCapacity)

	utilization := float64(0)
	if s.RingCapacity > 0 {
		utilization = float64(s.RingEntries) / float64(s.RingCapacity)
	}
	fmt.Fprintf(w, "# HELP lplex_ring_buffer_utilization Ring buffer utilization ratio (0-1).\n")
	fmt.Fprintf(w, "# TYPE lplex_ring_buffer_utilization gauge\n")
	fmt.Fprintf(w, "lplex_ring_buffer_utilization %.6f\n", utilization)

	// Head sequence
	fmt.Fprintf(w, "# HELP lplex_broker_head_seq Next sequence number to be assigned.\n")
	fmt.Fprintf(w, "# TYPE lplex_broker_head_seq gauge\n")
	fmt.Fprintf(w, "lplex_broker_head_seq %d\n", s.HeadSeq)

	// Connections
	fmt.Fprintf(w, "# HELP lplex_active_sessions Number of buffered client sessions.\n")
	fmt.Fprintf(w, "# TYPE lplex_active_sessions gauge\n")
	fmt.Fprintf(w, "lplex_active_sessions %d\n", s.ActiveSessions)

	fmt.Fprintf(w, "# HELP lplex_active_subscribers Number of ephemeral SSE subscribers.\n")
	fmt.Fprintf(w, "# TYPE lplex_active_subscribers gauge\n")
	fmt.Fprintf(w, "lplex_active_subscribers %d\n", s.ActiveSubscribers)

	fmt.Fprintf(w, "# HELP lplex_active_consumers Number of pull-based consumers.\n")
	fmt.Fprintf(w, "# TYPE lplex_active_consumers gauge\n")
	fmt.Fprintf(w, "lplex_active_consumers %d\n", s.ActiveConsumers)

	// Devices
	fmt.Fprintf(w, "# HELP lplex_devices_total Number of discovered NMEA 2000 devices.\n")
	fmt.Fprintf(w, "# TYPE lplex_devices_total gauge\n")
	fmt.Fprintf(w, "lplex_devices_total %d\n", s.DeviceCount)

	// Last frame timestamp
	fmt.Fprintf(w, "# HELP lplex_last_frame_timestamp_seconds Unix timestamp of the most recent frame.\n")
	fmt.Fprintf(w, "# TYPE lplex_last_frame_timestamp_seconds gauge\n")
	if s.LastFrameTime.IsZero() {
		fmt.Fprintf(w, "lplex_last_frame_timestamp_seconds 0\n")
	} else {
		fmt.Fprintf(w, "lplex_last_frame_timestamp_seconds %.3f\n", float64(s.LastFrameTime.UnixNano())/1e9)
	}

	// Replication metrics (optional)
	if replStatus != nil {
		rs := replStatus()
		if rs != nil {
			connected := 0
			if rs.Connected {
				connected = 1
			}
			fmt.Fprintf(w, "# HELP lplex_replication_connected Whether the replication client is connected (0/1).\n")
			fmt.Fprintf(w, "# TYPE lplex_replication_connected gauge\n")
			fmt.Fprintf(w, "lplex_replication_connected %d\n", connected)

			fmt.Fprintf(w, "# HELP lplex_replication_live_lag_seqs Number of sequences the cloud is behind the local head.\n")
			fmt.Fprintf(w, "# TYPE lplex_replication_live_lag_seqs gauge\n")
			fmt.Fprintf(w, "lplex_replication_live_lag_seqs %d\n", rs.LiveLag)

			fmt.Fprintf(w, "# HELP lplex_replication_backfill_remaining_seqs Number of sequences remaining in backfill holes.\n")
			fmt.Fprintf(w, "# TYPE lplex_replication_backfill_remaining_seqs gauge\n")
			fmt.Fprintf(w, "lplex_replication_backfill_remaining_seqs %d\n", rs.BackfillRemainingSeqs)

			fmt.Fprintf(w, "# HELP lplex_replication_last_ack_timestamp_seconds Unix timestamp of the last replication ACK.\n")
			fmt.Fprintf(w, "# TYPE lplex_replication_last_ack_timestamp_seconds gauge\n")
			if rs.LastAck.IsZero() {
				fmt.Fprintf(w, "lplex_replication_last_ack_timestamp_seconds 0\n")
			} else {
				fmt.Fprintf(w, "lplex_replication_last_ack_timestamp_seconds %.3f\n", float64(rs.LastAck.UnixNano())/1e9)
			}
		}
	}
}
