package lplex

import (
	"context"
	"log/slog"
	"time"

	"go.einride.tech/can"
	"go.einride.tech/can/pkg/socketcan"
)

// CANReader reads frames from SocketCAN, reassembles fast-packets,
// and sends completed frames to the broker's rxFrames channel.
func CANReader(ctx context.Context, iface string, rxFrames chan<- RxFrame, logger *slog.Logger) error {
	conn, err := socketcan.DialContext(ctx, "can", iface)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	receiver := socketcan.NewReceiver(conn)
	assembler := NewFastPacketAssembler(750 * time.Millisecond)

	logger.Info("CAN reader started", "interface", iface)

	// Periodically purge stale assemblies
	purgeTicker := time.NewTicker(5 * time.Second)
	defer purgeTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("CAN reader stopping")
			return ctx.Err()
		case <-purgeTicker.C:
			assembler.PurgeStale(time.Now())
		default:
		}

		if !receiver.Receive() {
			if err := receiver.Err(); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
			return nil // EOF
		}

		if receiver.HasErrorFrame() {
			continue
		}

		frame := receiver.Frame()
		if !frame.IsExtended {
			continue // NMEA 2000 uses 29-bit extended IDs only
		}

		now := time.Now()
		header := ParseCANID(frame.ID)
		data := frame.Data[:frame.Length]

		if IsFastPacket(header.PGN) {
			assembled := assembler.Process(header.PGN, header.Source, data, now)
			if assembled == nil {
				continue
			}
			data = assembled
		}

		select {
		case rxFrames <- RxFrame{Timestamp: now, Header: header, Data: data}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// txPaceInterval is the minimum time between transmitted CAN frames.
// Prevents flooding the bus when multiple requests queue up (e.g. product
// info requests for every device at startup).
const txPaceInterval = 50 * time.Millisecond

// CANWriter reads from the broker's txFrames channel and writes to SocketCAN.
// Handles fast-packet fragmentation for payloads > 8 bytes.
func CANWriter(ctx context.Context, iface string, txFrames <-chan TxRequest, logger *slog.Logger) error {
	conn, err := socketcan.DialContext(ctx, "can", iface)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	transmitter := socketcan.NewTransmitter(conn)
	var seqCounter uint8
	var lastTx time.Time

	logger.Info("CAN writer started", "interface", iface)

	for {
		select {
		case <-ctx.Done():
			logger.Info("CAN writer stopping")
			return ctx.Err()
		case req, ok := <-txFrames:
			if !ok {
				return nil
			}

			// Pace outgoing frames so we don't flood the bus.
			if since := time.Since(lastTx); since < txPaceInterval {
				time.Sleep(txPaceInterval - since)
			}

			canID := BuildCANID(req.Header)

			if IsFastPacket(req.Header.PGN) && len(req.Data) > 8 {
				// Fragment as fast-packet
				fragments := FragmentFastPacket(req.Data, seqCounter)
				seqCounter = (seqCounter + 1) & 0x07

				for _, frag := range fragments {
					f := can.Frame{
						ID:         canID,
						Length:     8,
						IsExtended: true,
					}
					copy(f.Data[:], frag)

					if err := transmitter.TransmitFrame(ctx, f); err != nil {
						logger.Error("CAN TX failed", "error", err, "pgn", req.Header.PGN)
						break
					}
				}
			} else {
				// Single frame
				f := can.Frame{
					ID:         canID,
					Length:     uint8(len(req.Data)),
					IsExtended: true,
				}
				copy(f.Data[:], req.Data)

				if err := transmitter.TransmitFrame(ctx, f); err != nil {
					logger.Error("CAN TX failed", "error", err, "pgn", req.Header.PGN)
				}
			}
			lastTx = time.Now()
		}
	}
}
