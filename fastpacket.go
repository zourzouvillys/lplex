package lplex

import (
	"time"

	"github.com/sixfathoms/lplex/pgn"
)

// IsFastPacket returns true if the PGN uses fast-packet transfer.
func IsFastPacket(pgnNum uint32) bool {
	if info, ok := pgn.Registry[pgnNum]; ok {
		return info.FastPacket
	}
	return false
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
