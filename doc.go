// Package lplex is a CAN bus HTTP bridge for NMEA 2000.
//
// It reads raw CAN frames from a SocketCAN interface, reassembles
// fast-packets, tracks device discovery, and streams frames to clients
// over SSE with session management, filtering, and replay.
//
// The package can be embedded into other Go services. Create a [Broker]
// to manage frame routing, a [Server] to expose the HTTP API, and
// optionally a [JournalWriter] to record frames to disk.
//
//	broker := lplex.NewBroker(lplex.BrokerConfig{
//	    RingSize:          65536,
//	    MaxBufferDuration: 5 * time.Minute,
//	    Logger:            logger,
//	})
//	go broker.Run()
//
//	srv := lplex.NewServer(broker, logger)
//	mux.Handle("/nmea/", http.StripPrefix("/nmea", srv))
//
// Feed frames into the broker via [Broker.RxFrames]:
//
//	broker.RxFrames() <- lplex.RxFrame{
//	    Timestamp: time.Now(),
//	    Header:    lplex.CANHeader{Priority: 2, PGN: 129025, Source: 10, Destination: 0xFF},
//	    Data:      payload,
//	}
//
// When done, close the rx channel and the broker goroutine exits:
//
//	broker.CloseRx()
package lplex
