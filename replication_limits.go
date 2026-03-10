package lplex

import (
	"errors"
	"time"
)

// NMEA 2000 runs on CAN 2.0B at 250 kbit/s. An extended frame (29-bit ID,
// 8-byte payload) is roughly 131-157 bits depending on bit stuffing.
// Theoretical max is ~1800 frames/sec. We allow 2000 to give ~10% headroom
// for measurement jitter and bit-stuffing variance.
const DefaultMaxFrameRate = 2000

// DefaultRateBurst is the burst allowance for transient spikes. Power-on
// storms (every device announces simultaneously) can briefly exceed the
// sustained rate. 500 frames absorbs a ~250ms burst at max bus load.
const DefaultRateBurst = 500

// DefaultMaxLiveLag is the frame count threshold for live lag detection. If
// the live stream falls this far behind the broker head (boat-side) or the
// boat's reported head (cloud-side), the stream is killed and the gap switches
// to backfill mode. ~5 seconds at max bus rate.
const DefaultMaxLiveLag uint64 = 10_000

// DefaultLagCheckInterval controls how often the boat checks for lag in the
// live send loop. Checked every N frames sent rather than by wall clock
// because when lagging, consumer.Next() returns instantly and the loop spins
// at CPU speed. At max bus rate (2000 fps) this checks roughly every 0.5s.
const DefaultLagCheckInterval = 1000

// DefaultMinLagReconnectInterval prevents thrashing when the system is
// persistently overloaded. If lag keeps recurring, we wait at least this
// long between lag-triggered reconnects.
const DefaultMinLagReconnectInterval = 30 * time.Second

// errLiveLagExceeded is returned by runLiveStream when the consumer falls
// too far behind the broker head. The Run() loop uses this to skip
// exponential backoff and reconnect immediately (jump to head, backfill
// the gap).
var errLiveLagExceeded = errors.New("live stream lag exceeded threshold")
