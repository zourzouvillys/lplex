package lplex

import "time"

// fastPacketPGNs lists PGNs that use fast-packet transfer (multi-frame).
// Comprehensive list from canboat database.
var fastPacketPGNs = map[uint32]bool{
	// Navigation
	126208: true, // NMEA Request/Command/Acknowledge Group Function
	126464: true, // PGN List (Transmit and Receive)
	126720: true, // Proprietary fast-packet
	126983: true, // Alert
	126984: true, // Alert Response
	126985: true, // Alert Text
	126986: true, // Alert Configuration
	126987: true, // Alert Threshold
	126988: true, // Alert Value
	126996: true, // Product Information
	126998: true, // Configuration Information

	// GNSS
	129029: true, // GNSS Position Data
	129038: true, // AIS Class A Position Report
	129039: true, // AIS Class B Position Report
	129040: true, // AIS Class B Extended Position Report
	129041: true, // AIS Aids to Navigation (AtoN) Report
	129044: true, // Datum
	129045: true, // User Datum Settings
	129284: true, // Navigation Route/WP Information
	129285: true, // Navigation Route - WP Name & Position
	129301: true, // Time to/from Mark
	129302: true, // Bearing and Distance between two Marks
	129538: true, // GNSS Control Status
	129540: true, // GNSS Sats in View
	129541: true, // GPS Almanac Data
	129542: true, // GNSS Pseudorange Noise Statistics
	129545: true, // GNSS RAIM Output
	129547: true, // GNSS Pseudorange Error Statistics
	129549: true, // DGNSS Corrections
	129551: true, // GNSS Differential Correction Receiver Signal

	// AIS
	129792: true, // AIS DGNSS Broadcast Binary Message
	129793: true, // AIS UTC and Date Report
	129794: true, // AIS Class A Static and Voyage Related Data
	129795: true, // AIS Addressed Binary Message
	129796: true, // AIS Acknowledge
	129797: true, // AIS Binary Broadcast Message
	129798: true, // AIS SAR Aircraft Position Report
	129799: true, // Radio Frequency/Mode/Power
	129800: true, // AIS UTC/Date Inquiry
	129801: true, // AIS Addressed Safety Related Message
	129802: true, // AIS Safety Related Broadcast Message
	129803: true, // AIS Interrogation
	129804: true, // AIS Assignment Mode Command
	129805: true, // AIS Data Link Management Message
	129806: true, // AIS Channel Management
	129807: true, // AIS Group Assignment
	129808: true, // DSC Call Information
	129809: true, // AIS Class B CS Static Data Report, Part A
	129810: true, // AIS Class B CS Static Data Report, Part B

	// Meteorological
	130052: true, // Loran-C TD Data
	130053: true, // Loran-C Range Data
	130054: true, // Loran-C Signal Data
	130060: true, // Label
	130061: true, // Channel Source Configuration
	130064: true, // Route and WP Service - Database List
	130065: true, // Route and WP Service - Route List
	130066: true, // Route and WP Service - Route/WP-Name & Position
	130067: true, // Route and WP Service - Route/WP-Name
	130068: true, // Route and WP Service - XTE Limit & Navigation Method
	130069: true, // Route and WP Service - WP Comment
	130070: true, // Route and WP Service - Route Comment
	130071: true, // Route and WP Service - Database Comment
	130072: true, // Route and WP Service - Radius of Turn
	130073: true, // Route and WP Service - WP List - WP Name & Position
	130074: true, // Route and WP Service - WP List - Database List

	// Environment
	130323: true, // Meteorological Station Data
	130567: true, // Watermaker Input Setting and Status
	130577: true, // Direction Data (fast)
	130578: true, // Vessel Speed Components
}

// IsFastPacket returns true if the PGN uses fast-packet transfer.
func IsFastPacket(pgn uint32) bool {
	return fastPacketPGNs[pgn]
}

// reassemblyKey uniquely identifies a fast-packet transfer in progress.
type reassemblyKey struct {
	PGN    uint32
	Source uint8
}

// reassemblyState holds the state of an in-progress fast-packet reassembly.
type reassemblyState struct {
	SeqID     uint8
	TotalLen  int
	Data      []byte
	NextFrame int
	StartedAt time.Time
}

// FastPacketAssembler reassembles multi-frame fast-packet PGNs.
//
// Fast-packet protocol:
//   - Frame 0: byte[0] = seq_counter(3 bits) | frame_number(5 bits),
//     byte[1] = total_bytes, bytes[2:8] = first 6 data bytes
//   - Frame N: byte[0] = seq_counter(3 bits) | frame_number(5 bits),
//     bytes[1:8] = next 7 data bytes
type FastPacketAssembler struct {
	inProgress map[reassemblyKey]*reassemblyState
	timeout    time.Duration
}

// NewFastPacketAssembler creates a new assembler with the given reassembly timeout.
func NewFastPacketAssembler(timeout time.Duration) *FastPacketAssembler {
	return &FastPacketAssembler{
		inProgress: make(map[reassemblyKey]*reassemblyState),
		timeout:    timeout,
	}
}

// Process handles a CAN frame that is part of a fast-packet transfer.
// Returns the complete reassembled payload when all frames are received, nil otherwise.
func (a *FastPacketAssembler) Process(pgn uint32, source uint8, data []byte, now time.Time) []byte {
	if len(data) < 2 {
		return nil
	}

	key := reassemblyKey{PGN: pgn, Source: source}
	seqID := data[0] >> 5
	frameNum := data[0] & 0x1F

	if frameNum == 0 {
		totalLen := int(data[1])
		if totalLen == 0 {
			return nil
		}

		state := &reassemblyState{
			SeqID:     seqID,
			TotalLen:  totalLen,
			Data:      make([]byte, 0, totalLen),
			NextFrame: 1,
			StartedAt: now,
		}

		payload := data[2:]
		if len(payload) > totalLen {
			payload = payload[:totalLen]
		}
		state.Data = append(state.Data, payload...)

		if len(state.Data) >= totalLen {
			delete(a.inProgress, key)
			return state.Data[:totalLen]
		}

		a.inProgress[key] = state
		return nil
	}

	state, ok := a.inProgress[key]
	if !ok {
		return nil
	}

	if state.SeqID != seqID || int(frameNum) != state.NextFrame {
		delete(a.inProgress, key)
		return nil
	}

	if now.Sub(state.StartedAt) > a.timeout {
		delete(a.inProgress, key)
		return nil
	}

	payload := data[1:]
	remaining := state.TotalLen - len(state.Data)
	if len(payload) > remaining {
		payload = payload[:remaining]
	}
	state.Data = append(state.Data, payload...)
	state.NextFrame++

	if len(state.Data) >= state.TotalLen {
		delete(a.inProgress, key)
		return state.Data[:state.TotalLen]
	}

	return nil
}

// PurgeStale removes any in-progress assemblies older than the timeout.
func (a *FastPacketAssembler) PurgeStale(now time.Time) {
	for key, state := range a.inProgress {
		if now.Sub(state.StartedAt) > a.timeout {
			delete(a.inProgress, key)
		}
	}
}

// FragmentFastPacket splits a payload into CAN frames for fast-packet TX.
// seqCounter should be incremented per transfer (wraps at 7, 3-bit field).
// Returns a slice of 8-byte CAN frame payloads.
func FragmentFastPacket(data []byte, seqCounter uint8) [][]byte {
	seqBits := (seqCounter & 0x07) << 5
	totalLen := len(data)

	var frames [][]byte

	// Frame 0: [seq|0] [totalLen] [up to 6 data bytes]
	frame0 := make([]byte, 8)
	frame0[0] = seqBits
	frame0[1] = byte(totalLen)
	n := copy(frame0[2:], data)
	frames = append(frames, frame0)

	offset := n
	frameNum := byte(1)

	for offset < totalLen {
		frame := make([]byte, 8)
		frame[0] = seqBits | (frameNum & 0x1F)
		copied := copy(frame[1:], data[offset:])
		// Pad remaining bytes with 0xFF (NMEA 2000 convention)
		for i := 1 + copied; i < 8; i++ {
			frame[i] = 0xFF
		}
		frames = append(frames, frame)
		offset += copied
		frameNum++
	}

	return frames
}
