package lplex

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"
)

// RxFrame is a reassembled CAN frame ready for the broker.
type RxFrame struct {
	Timestamp time.Time
	Header    CANHeader
	Data      []byte
}

// ringEntry is a pre-serialized frame stored in the ring buffer.
type ringEntry struct {
	Seq    uint64
	Header CANHeader // original header, used for filtered replay
	Data   []byte    // pre-serialized SSE JSON line (without "data: " prefix)
}

// ClientSession tracks a connected or recently-disconnected client.
type ClientSession struct {
	ID            string
	Cursor        uint64        // last ACK'd sequence number (0 = never ACK'd)
	BufferTimeout time.Duration // how long to keep buffering after disconnect
	LastActivity  time.Time
	Ch            chan []byte   // buffered channel for SSE fan-out
	Connected     bool
	Filter        *EventFilter // nil = receive all frames
}

// EventFilter specifies which CAN frames a session receives.
// Categories are AND'd (all set categories must match), values within
// a category are OR'd (any value in the list matches).
type EventFilter struct {
	PGNs          []uint32
	Manufacturers []string
	Instances     []uint8
	Names         []uint64 // 64-bit CAN NAMEs
}

// IsEmpty returns true if no filter criteria are set.
func (f *EventFilter) IsEmpty() bool {
	return f == nil || (len(f.PGNs) == 0 && len(f.Manufacturers) == 0 &&
		len(f.Instances) == 0 && len(f.Names) == 0)
}

// matches checks if a frame passes this filter. For device-based criteria
// (manufacturer, instance, name), the device registry is consulted.
func (f *EventFilter) matches(header CANHeader, devices *DeviceRegistry) bool {
	if f.IsEmpty() {
		return true
	}

	if len(f.PGNs) > 0 && !slices.Contains(f.PGNs, header.PGN) {
		return false
	}

	if len(f.Manufacturers) > 0 || len(f.Instances) > 0 || len(f.Names) > 0 {
		dev := devices.Get(header.Source)
		if dev == nil {
			return false
		}
		if !f.matchesDevice(dev) {
			return false
		}
	}

	return true
}

// matchesManufacturer checks if a device matches any of the manufacturer filter
// values. Each value can be a name ("Garmin") or a numeric code ("229").
func (f *EventFilter) matchesManufacturer(dev *Device) bool {
	codeStr := strconv.FormatUint(uint64(dev.ManufacturerCode), 10)
	for _, m := range f.Manufacturers {
		if m == dev.Manufacturer || m == codeStr {
			return true
		}
	}
	return false
}

// matchesDevice checks if a device matches the device-based filter criteria.
func (f *EventFilter) matchesDevice(dev *Device) bool {
	if len(f.Manufacturers) > 0 && !f.matchesManufacturer(dev) {
		return false
	}
	if len(f.Instances) > 0 && !slices.Contains(f.Instances, dev.DeviceInstance) {
		return false
	}
	if len(f.Names) > 0 && !slices.Contains(f.Names, dev.NAME) {
		return false
	}
	return true
}

// resolvedFilter is a pre-resolved EventFilter where device-based criteria
// have been flattened to source addresses. Used during replay to avoid
// holding the ring buffer lock while querying the device registry.
type resolvedFilter struct {
	pgns    map[uint32]struct{} // nil = all PGNs
	sources map[uint8]struct{}  // nil = all sources
}

// resolve snapshots the device registry and converts device-based filter
// criteria into a set of source addresses. Call before taking the ring lock.
func (f *EventFilter) resolve(devices *DeviceRegistry) *resolvedFilter {
	if f.IsEmpty() {
		return nil
	}

	r := &resolvedFilter{}

	if len(f.PGNs) > 0 {
		r.pgns = make(map[uint32]struct{}, len(f.PGNs))
		for _, pgn := range f.PGNs {
			r.pgns[pgn] = struct{}{}
		}
	}

	if len(f.Manufacturers) > 0 || len(f.Instances) > 0 || len(f.Names) > 0 {
		r.sources = make(map[uint8]struct{})
		for _, dev := range devices.Snapshot() {
			if f.matchesDevice(&dev) {
				r.sources[dev.Source] = struct{}{}
			}
		}
	}

	return r
}

func (r *resolvedFilter) matches(header CANHeader) bool {
	if r == nil {
		return true
	}
	if r.pgns != nil {
		if _, ok := r.pgns[header.PGN]; !ok {
			return false
		}
	}
	if r.sources != nil {
		if _, ok := r.sources[header.Source]; !ok {
			return false
		}
	}
	return true
}

// subscriber is an ephemeral fan-out target with no session state.
// Created by Subscribe, removed by the returned cleanup function.
type subscriber struct {
	ch     chan []byte
	filter *EventFilter
}

// Broker is the central coordinator. Single goroutine reads from rxFrames,
// assigns sequence numbers, appends to ring buffer, updates device registry,
// and fans out to client sessions and ephemeral subscribers.
type Broker struct {
	rxFrames   chan RxFrame
	txFrames   chan TxRequest
	devices    *DeviceRegistry
	logger     *slog.Logger

	// ring buffer (protected by mu for replay reads)
	mu       sync.RWMutex
	ring     []ringEntry
	ringMask int    // ring size - 1 (power of 2)
	head     uint64 // next write position (also next seq number)
	tail     uint64 // oldest valid position

	// client sessions (only accessed by broker goroutine, except for
	// Replay which holds mu.RLock)
	sessionMu sync.RWMutex
	sessions  map[string]*ClientSession

	// ephemeral subscribers (no session state, no replay, no ACK)
	subscriberMu sync.RWMutex
	subscribers  map[*subscriber]struct{}

	maxBufferDuration time.Duration

	// journal channel (nil = journaling disabled)
	journal chan<- RxFrame
}

// TxRequest is a frame to write to the CAN bus.
type TxRequest struct {
	Header CANHeader
	Data   []byte
}

// BrokerConfig holds broker configuration.
type BrokerConfig struct {
	RingSize          int           // must be power of 2
	MaxBufferDuration time.Duration // cap on client buffer_timeout
	Logger            *slog.Logger
}

// NewBroker creates a new broker with the given config.
func NewBroker(cfg BrokerConfig) *Broker {
	if cfg.RingSize == 0 {
		cfg.RingSize = 65536
	}
	// Ensure power of 2
	if cfg.RingSize&(cfg.RingSize-1) != 0 {
		panic("ring size must be a power of 2")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxBufferDuration == 0 {
		cfg.MaxBufferDuration = 5 * time.Minute
	}

	return &Broker{
		rxFrames:          make(chan RxFrame, 256),
		txFrames:          make(chan TxRequest, 64),
		devices:           NewDeviceRegistry(),
		logger:            cfg.Logger,
		ring:              make([]ringEntry, cfg.RingSize),
		ringMask:          cfg.RingSize - 1,
		head:              1, // seq starts at 1 (0 means "never ACK'd")
		tail:              1,
		sessions:          make(map[string]*ClientSession),
		subscribers:       make(map[*subscriber]struct{}),
		maxBufferDuration: cfg.MaxBufferDuration,
	}
}

// Run is the broker's main loop. Call in its own goroutine.
// Exits when rxFrames is closed.
func (b *Broker) Run() {
	// Broadcast ISO Request for Address Claim so devices already on the bus identify themselves.
	b.sendISORequest(0xFF, 60928)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case frame, ok := <-b.rxFrames:
			if !ok {
				return
			}
			b.handleFrame(frame)

		case <-ticker.C:
			b.expireSessions()
		}
	}
}

// sendISORequest sends an ISO Request (PGN 59904) asking the target to transmit
// the specified PGN. dst=0xFF for broadcast, or a specific source address.
func (b *Broker) sendISORequest(dst uint8, pgn uint32) {
	header := CANHeader{
		Priority:    6,
		PGN:         59904,
		Source:      254, // null/tool address
		Destination: dst,
	}
	data := []byte{byte(pgn), byte(pgn >> 8), byte(pgn >> 16)}
	select {
	case b.txFrames <- TxRequest{Header: header, Data: data}:
	default:
		// tx queue full, don't block the broker loop
	}
}

func (b *Broker) handleFrame(frame RxFrame) {
	src := frame.Header.Source

	// Track per-source packet stats for every frame.
	newSource := b.devices.RecordPacket(src, frame.Timestamp, len(frame.Data))

	switch frame.Header.PGN {
	case 60928:
		if dev := b.devices.HandleAddressClaim(src, frame.Data); dev != nil {
			b.logger.Info("device discovered",
				"src", dev.Source,
				"manufacturer", dev.Manufacturer,
				"function", dev.DeviceFunction,
				"class", dev.DeviceClass,
			)
			b.fanOutDevice(dev)
			b.sendISORequest(src, 126996)
		}
	case 126996:
		if dev := b.devices.HandleProductInfo(src, frame.Data); dev != nil {
			b.logger.Info("product info",
				"src", dev.Source,
				"model", dev.ModelID,
				"serial", dev.ModelSerial,
				"sw", dev.SoftwareVersion,
			)
			b.fanOutDevice(dev)
		}
	default:
		if newSource {
			b.sendISORequest(src, 60928)
		}
	}

	// Serialize frame to JSON
	msg := frameJSON{
		Seq:  b.head,
		Ts:   frame.Timestamp.UTC().Format(time.RFC3339Nano),
		Prio: frame.Header.Priority,
		PGN:  frame.Header.PGN,
		Src:  frame.Header.Source,
		Dst:  frame.Header.Destination,
		Data: hex.EncodeToString(frame.Data),
	}

	jsonBytes, err := json.Marshal(msg)
	if err != nil {
		b.logger.Error("failed to marshal frame", "error", err)
		return
	}

	// Append to ring buffer
	b.mu.Lock()
	idx := b.head & uint64(b.ringMask)
	b.ring[idx] = ringEntry{Seq: b.head, Header: frame.Header, Data: jsonBytes}
	b.head++
	// Advance tail if ring is full
	if b.head-b.tail > uint64(b.ringMask+1) {
		b.tail = b.head - uint64(b.ringMask+1)
	}
	b.mu.Unlock()

	// Fan out to connected clients (filters checked per-session)
	b.fanOut(frame.Header, jsonBytes)

	// Send to journal writer (non-blocking)
	if b.journal != nil {
		select {
		case b.journal <- frame:
		default:
			b.logger.Warn("journal buffer full, dropping frame")
		}
	}
}

type frameJSON struct {
	Seq  uint64 `json:"seq"`
	Ts   string `json:"ts"`
	Prio uint8  `json:"prio"`
	PGN  uint32 `json:"pgn"`
	Src  uint8  `json:"src"`
	Dst  uint8  `json:"dst"`
	Data string `json:"data"`
}

// Subscribe creates an ephemeral fan-out channel with the given filter.
// Returns the subscriber and a cleanup function that must be called on disconnect.
func (b *Broker) Subscribe(filter *EventFilter) (*subscriber, func()) {
	if filter != nil && filter.IsEmpty() {
		filter = nil
	}
	sub := &subscriber{
		ch:     make(chan []byte, 128),
		filter: filter,
	}
	b.subscriberMu.Lock()
	b.subscribers[sub] = struct{}{}
	b.subscriberMu.Unlock()

	cleanup := func() {
		b.subscriberMu.Lock()
		delete(b.subscribers, sub)
		b.subscriberMu.Unlock()
	}
	return sub, cleanup
}

// fanOut sends pre-serialized JSON to all connected client channels
// and ephemeral subscribers, skipping those whose filter doesn't match.
func (b *Broker) fanOut(header CANHeader, data []byte) {
	b.sessionMu.RLock()
	for _, s := range b.sessions {
		if !s.Connected {
			continue
		}
		if !s.Filter.matches(header, b.devices) {
			continue
		}
		select {
		case s.Ch <- data:
		default:
		}
	}
	b.sessionMu.RUnlock()

	b.subscriberMu.RLock()
	for sub := range b.subscribers {
		if !sub.filter.matches(header, b.devices) {
			continue
		}
		select {
		case sub.ch <- data:
		default:
		}
	}
	b.subscriberMu.RUnlock()
}

// fanOutDevice sends a device discovery event to all connected clients
// and ephemeral subscribers.
func (b *Broker) fanOutDevice(dev *Device) {
	msg := struct {
		Type string `json:"type"`
		Device
	}{
		Type:   "device",
		Device: *dev,
	}

	jsonBytes, err := json.Marshal(msg)
	if err != nil {
		return
	}

	b.sessionMu.RLock()
	for _, s := range b.sessions {
		if !s.Connected {
			continue
		}
		select {
		case s.Ch <- jsonBytes:
		default:
		}
	}
	b.sessionMu.RUnlock()

	b.subscriberMu.RLock()
	for sub := range b.subscribers {
		select {
		case sub.ch <- jsonBytes:
		default:
		}
	}
	b.subscriberMu.RUnlock()
}

// CreateSession creates or retrieves a client session.
// Returns the session and the current head sequence number.
//
// When bufferTimeout is 0, the session cursor is reset so no frames
// are replayed on the next connect (fresh start).
func (b *Broker) CreateSession(id string, bufferTimeout time.Duration, filter *EventFilter) (*ClientSession, uint64) {
	if bufferTimeout > b.maxBufferDuration {
		bufferTimeout = b.maxBufferDuration
	}
	if filter != nil && filter.IsEmpty() {
		filter = nil
	}

	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()

	b.mu.RLock()
	seq := b.head - 1
	b.mu.RUnlock()

	if s, ok := b.sessions[id]; ok {
		s.BufferTimeout = bufferTimeout
		s.Filter = filter
		s.LastActivity = time.Now()
		if bufferTimeout == 0 {
			s.Cursor = 0
			// Drain stale channel data
			for {
				select {
				case <-s.Ch:
				default:
					return s, seq
				}
			}
		}
		return s, seq
	}

	s := &ClientSession{
		ID:            id,
		BufferTimeout: bufferTimeout,
		LastActivity:  time.Now(),
		Ch:            make(chan []byte, 128),
		Filter:        filter,
	}
	b.sessions[id] = s
	return s, seq
}

// ConnectSession marks a session as connected and returns its channel.
func (b *Broker) ConnectSession(id string) (*ClientSession, bool) {
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()

	s, ok := b.sessions[id]
	if !ok {
		return nil, false
	}
	s.Connected = true
	s.LastActivity = time.Now()

	// Drain any stale data in the channel from before
	for {
		select {
		case <-s.Ch:
		default:
			return s, true
		}
	}
}

// DisconnectSession marks a session as disconnected.
func (b *Broker) DisconnectSession(id string) {
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()

	if s, ok := b.sessions[id]; ok {
		s.Connected = false
		s.LastActivity = time.Now()
	}
}

// AckSession updates the cursor for a session.
func (b *Broker) AckSession(id string, seq uint64) error {
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()

	s, ok := b.sessions[id]
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}
	s.Cursor = seq
	s.LastActivity = time.Now()
	return nil
}

// Replay returns buffered entries from afterSeq+1 up to the current head,
// filtered by the given EventFilter. Device-based filter criteria are resolved
// to source addresses before taking the ring lock to avoid deadlocks.
func (b *Broker) Replay(afterSeq uint64, filter *EventFilter) [][]byte {
	// Pre-resolve device filters while we don't hold the ring lock.
	resolved := filter.resolve(b.devices)

	b.mu.RLock()
	defer b.mu.RUnlock()

	startSeq := max(afterSeq+1, b.tail)
	if startSeq >= b.head {
		return nil
	}

	count := int(b.head - startSeq)
	result := make([][]byte, 0, count)

	for seq := startSeq; seq < b.head; seq++ {
		idx := seq & uint64(b.ringMask)
		entry := b.ring[idx]
		if entry.Seq != seq {
			continue
		}
		if !resolved.matches(entry.Header) {
			continue
		}
		result = append(result, entry.Data)
	}

	return result
}

// expireSessions removes sessions that have been disconnected
// longer than their buffer timeout.
func (b *Broker) expireSessions() {
	now := time.Now()

	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()

	for id, s := range b.sessions {
		if !s.Connected && now.Sub(s.LastActivity) > s.BufferTimeout {
			b.logger.Info("expiring client session", "client", id)
			close(s.Ch)
			delete(b.sessions, id)
		}
	}
}

// CurrentSeq returns the most recently assigned sequence number.
func (b *Broker) CurrentSeq() uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.head == 0 {
		return 0
	}
	return b.head - 1
}

// RxFrames returns the channel for submitting received CAN frames to the broker.
func (b *Broker) RxFrames() chan<- RxFrame {
	return b.rxFrames
}

// TxFrames returns the channel for reading CAN frames to transmit.
func (b *Broker) TxFrames() <-chan TxRequest {
	return b.txFrames
}

// CloseRx closes the rxFrames channel, signaling the broker to stop processing.
func (b *Broker) CloseRx() {
	close(b.rxFrames)
}

// SetJournal sets the journal channel. Must be called before Run.
func (b *Broker) SetJournal(ch chan<- RxFrame) {
	b.journal = ch
}

// Devices returns the broker's device registry.
func (b *Broker) Devices() *DeviceRegistry {
	return b.devices
}
