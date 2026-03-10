package lplex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sixfathoms/lplex/journal"
	pb "github.com/sixfathoms/lplex/proto/replication/v1"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// InstanceState tracks the replication state for a single boat instance
// on the cloud side. Thread-safe.
type InstanceState struct {
	mu sync.Mutex

	ID               string     `json:"id"`
	Cursor           uint64     `json:"cursor"`             // continuous data through this seq
	BoatHeadSeq      uint64     `json:"boat_head_seq"`      // last reported by boat
	BoatJournalBytes uint64     `json:"boat_journal_bytes"`
	LastSeen         time.Time  `json:"last_seen"`
	Connected        bool       `json:"-"`
	HoleTracker      *HoleTracker `json:"-"`

	// Persisted hole state
	PersistedHoles []SeqRange `json:"holes,omitempty"`

	// Runtime (not persisted)
	events         *EventLog
	broker         *Broker
	journalDir     string
	journalCh      chan RxFrame        // connects broker to its JournalWriter
	journalWriter  *JournalWriter      // live journal writer (nil when broker not running)
	journalDone    chan struct{}        // closed when journal writer goroutine exits
	cancelFunc     context.CancelFunc  // stops the broker's journal writer
	onRotate       func(RotatedFile)   // optional callback for keeper
	rotateDuration time.Duration       // journal rotation interval for live writer
	rotateSize     int64               // journal rotation size cap for live writer
	logger         *slog.Logger
}

// instanceStatePersist is the JSON shape written to state.json.
type instanceStatePersist struct {
	ID               string     `json:"id"`
	Cursor           uint64     `json:"cursor"`
	Holes            []SeqRange `json:"holes,omitempty"`
	BoatHeadSeq      uint64     `json:"boat_head_seq"`
	BoatJournalBytes uint64     `json:"boat_journal_bytes"`
	LastSeen         time.Time  `json:"last_seen"`
}

// persist writes state to disk. Caller must hold s.mu.
func (s *InstanceState) persist(dir string) error {
	return writePersistState(dir, s.snapshotLocked())
}

// snapshotLocked captures the persist-state. Caller must hold s.mu.
func (s *InstanceState) snapshotLocked() instanceStatePersist {
	return instanceStatePersist{
		ID:               s.ID,
		Cursor:           s.Cursor,
		Holes:            s.HoleTracker.Holes(),
		BoatHeadSeq:      s.BoatHeadSeq,
		BoatJournalBytes: s.BoatJournalBytes,
		LastSeen:         s.LastSeen,
	}
}

func writePersistState(dir string, p instanceStatePersist) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	path := filepath.Join(dir, "state.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return os.Rename(tmp, path)
}

func loadInstanceState(dir string) (*instanceStatePersist, error) {
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var p instanceStatePersist
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ensureBroker lazily starts the instance's broker + journal writer.
// Caller must hold s.mu.
func (s *InstanceState) ensureBroker() {
	if s.broker != nil {
		return
	}

	journalDir := filepath.Join(s.journalDir, "journal")
	initialHead := max(s.Cursor+1, 1)

	b := NewBroker(BrokerConfig{
		RingSize:    65536,
		ReplicaMode: true,
		InitialHead: initialHead,
		JournalDir:  journalDir,
		Logger:      s.logger.With("instance", s.ID),
	})

	// Set up journal writer for live frames
	s.journalCh = make(chan RxFrame, 16384)
	b.SetJournal(s.journalCh)

	ctx, cancel := context.WithCancel(context.Background())
	s.cancelFunc = cancel

	jw, err := NewJournalWriter(JournalConfig{
		Dir:            journalDir,
		Prefix:         "nmea2k",
		BlockSize:      262144,
		Compression:    journal.CompressionZstd,
		RotateDuration: s.rotateDuration,
		RotateSize:     s.rotateSize,
		OnRotate:       s.onRotate,
		Logger:         s.logger.With("instance", s.ID, "component", "journal"),
	}, b.Devices(), s.journalCh)
	if err != nil {
		s.logger.Error("failed to create journal writer", "instance", s.ID, "error", err)
		cancel()
		return
	}

	s.journalWriter = jw
	s.journalDone = make(chan struct{})

	go b.Run()
	go func() {
		defer close(s.journalDone)
		if err := jw.Run(ctx); err != nil && ctx.Err() == nil {
			s.logger.Error("journal writer failed", "instance", s.ID, "error", err)
		}
	}()

	s.broker = b
	s.logger.Info("broker started", "instance", s.ID, "initial_head", initialHead)
}

// stopBroker stops the instance's broker and journal writer. Blocks until
// the journal writer's finalize completes (including OnRotate callbacks).
// Caller must hold s.mu.
func (s *InstanceState) stopBroker() {
	if s.broker == nil {
		return
	}
	b := s.broker
	journalDone := s.journalDone
	s.broker = nil

	// Signal the broker to stop, then wait for Run() to exit so it's no
	// longer sending on the journal channel before we close it.
	b.CloseRx()
	<-b.Done()

	if s.journalCh != nil {
		close(s.journalCh)
	}
	if s.cancelFunc != nil {
		s.cancelFunc()
	}

	// Wait for the journal writer goroutine to finish. This ensures
	// finalize() has run and OnRotate has fired before we return.
	// Release the lock while waiting to avoid deadlock if OnRotate
	// needs to acquire other locks.
	s.mu.Unlock()
	if journalDone != nil {
		<-journalDone
	}
	s.mu.Lock()

	s.journalCh = nil
	s.journalWriter = nil
	s.journalDone = nil
	s.cancelFunc = nil
	s.logger.Info("broker stopped", "instance", s.ID)
}

// InstanceManager manages per-instance state on the cloud side.
type InstanceManager struct {
	mu              sync.Mutex
	instances       map[string]*InstanceState
	dataDir         string
	logger          *slog.Logger
	onRotate        func(instanceID string, rf RotatedFile) // optional callback for keeper
	rotateDuration  time.Duration                           // journal rotation interval for live writers
	rotateSize      int64                                   // journal rotation size cap for live writers
}

// NewInstanceManager creates a new instance manager, loading any persisted state.
func NewInstanceManager(dataDir string, logger *slog.Logger) (*InstanceManager, error) {
	im := &InstanceManager{
		instances: make(map[string]*InstanceState),
		dataDir:   dataDir,
		logger:    logger,
	}

	// Load existing instances from disk
	instancesDir := filepath.Join(dataDir, "instances")
	entries, err := os.ReadDir(instancesDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read instances dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		dir := filepath.Join(instancesDir, id)
		persisted, err := loadInstanceState(dir)
		if err != nil {
			logger.Warn("failed to load instance state", "id", id, "error", err)
			continue
		}

		ht := NewHoleTracker()
		if persisted != nil {
			for _, h := range persisted.Holes {
				ht.Add(h.Start, h.End)
			}
		}

		state := &InstanceState{
			ID:          id,
			HoleTracker: ht,
			events:      NewEventLog(),
			journalDir:  dir,
			logger:      logger,
		}
		if persisted != nil {
			state.Cursor = persisted.Cursor
			state.BoatHeadSeq = persisted.BoatHeadSeq
			state.BoatJournalBytes = persisted.BoatJournalBytes
			state.LastSeen = persisted.LastSeen
		}

		im.instances[id] = state
		logger.Info("loaded instance", "id", id, "cursor", state.Cursor, "holes", ht.Len())
	}

	return im, nil
}

// SetOnRotate sets a callback invoked when any instance's journal or backfill
// file is rotated. Used by the cloud binary to feed the JournalKeeper.
// Must be called before any connections are accepted. Retroactively updates
// all existing instances loaded at startup.
func (im *InstanceManager) SetOnRotate(fn func(instanceID string, rf RotatedFile)) {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.onRotate = fn
	for id, s := range im.instances {
		s.mu.Lock()
		s.onRotate = im.makeOnRotate(id)
		s.mu.Unlock()
	}
}

// SetJournalRotation configures rotation for live journal writers.
// Must be called before any connections are accepted. Retroactively updates
// all existing instances loaded at startup.
func (im *InstanceManager) SetJournalRotation(duration time.Duration, size int64) {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.rotateDuration = duration
	im.rotateSize = size
	for _, s := range im.instances {
		s.mu.Lock()
		s.rotateDuration = duration
		s.rotateSize = size
		s.mu.Unlock()
	}
}

// makeOnRotate returns an instance-scoped OnRotate callback, or nil if no
// manager-level callback is set. Caller must hold im.mu.
func (im *InstanceManager) makeOnRotate(id string) func(RotatedFile) {
	if im.onRotate == nil {
		return nil
	}
	fn := im.onRotate
	return func(rf RotatedFile) {
		fn(id, rf)
	}
}

// SetInstancePaused pauses or unpauses journal writing for a specific instance.
// Used by the JournalKeeper overflow policy to stop/resume writes.
func (im *InstanceManager) SetInstancePaused(instanceID string, paused bool) {
	im.mu.Lock()
	s, ok := im.instances[instanceID]
	im.mu.Unlock()
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.journalWriter != nil {
		s.journalWriter.SetPaused(paused)
	}
}

// GetOrCreate returns the instance state, creating it if necessary.
func (im *InstanceManager) GetOrCreate(id string) *InstanceState {
	im.mu.Lock()
	defer im.mu.Unlock()

	if s, ok := im.instances[id]; ok {
		return s
	}

	dir := filepath.Join(im.dataDir, "instances", id)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "journal"), 0o755)

	s := &InstanceState{
		ID:             id,
		HoleTracker:    NewHoleTracker(),
		events:         NewEventLog(),
		journalDir:     dir,
		onRotate:       im.makeOnRotate(id),
		rotateDuration: im.rotateDuration,
		rotateSize:     im.rotateSize,
		logger:         im.logger,
	}
	im.instances[id] = s
	im.logger.Info("created instance", "id", id)
	return s
}

// Get returns the instance state, or nil if not found.
func (im *InstanceManager) Get(id string) *InstanceState {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.instances[id]
}

// List returns a snapshot of all instance IDs and their basic state.
func (im *InstanceManager) List() []InstanceSummary {
	im.mu.Lock()
	defer im.mu.Unlock()

	var result []InstanceSummary
	for _, s := range im.instances {
		s.mu.Lock()
		result = append(result, InstanceSummary{
			ID:          s.ID,
			Connected:   s.Connected,
			Cursor:      s.Cursor,
			BoatHeadSeq: s.BoatHeadSeq,
			Holes:       s.HoleTracker.Len(),
			LagSeqs:     s.BoatHeadSeq - s.Cursor,
			LastSeen:    s.LastSeen,
		})
		s.mu.Unlock()
	}
	return result
}

// Shutdown stops all instance brokers and persists state.
func (im *InstanceManager) Shutdown() {
	im.mu.Lock()
	defer im.mu.Unlock()

	for _, s := range im.instances {
		s.mu.Lock()
		s.stopBroker()
		_ = s.persist(s.journalDir)
		s.mu.Unlock()
	}
}

// InstanceSummary is a snapshot of an instance for listing.
type InstanceSummary struct {
	ID          string    `json:"id"`
	Connected   bool      `json:"connected"`
	Cursor      uint64    `json:"cursor"`
	BoatHeadSeq uint64    `json:"boat_head_seq"`
	Holes       int       `json:"holes"`
	LagSeqs     uint64    `json:"lag_seqs"`
	LastSeen    time.Time `json:"last_seen"`
}

// InstanceStatus is a detailed snapshot of an instance for status reporting.
type InstanceStatus struct {
	ID               string     `json:"id"`
	Connected        bool       `json:"connected"`
	Cursor           uint64     `json:"cursor"`
	BoatHeadSeq      uint64     `json:"boat_head_seq"`
	BoatJournalBytes uint64     `json:"boat_journal_bytes"`
	Holes            []SeqRange `json:"holes,omitzero"`
	LagSeqs          uint64     `json:"lag_seqs"`
	LastSeen         time.Time  `json:"last_seen"`
}

// Status returns a thread-safe snapshot of this instance's replication state.
func (s *InstanceState) Status() InstanceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return InstanceStatus{
		ID:               s.ID,
		Connected:        s.Connected,
		Cursor:           s.Cursor,
		BoatHeadSeq:      s.BoatHeadSeq,
		BoatJournalBytes: s.BoatJournalBytes,
		Holes:            s.HoleTracker.Holes(),
		LagSeqs:          s.BoatHeadSeq - s.Cursor,
		LastSeen:         s.LastSeen,
	}
}

// RecordEvent appends a diagnostic event to this instance's event log.
func (s *InstanceState) RecordEvent(typ ReplicationEventType, detail map[string]any) {
	s.events.Record(typ, detail)
}

// RecentEvents returns up to n recent replication events, newest first.
func (s *InstanceState) RecentEvents(n int) []ReplicationEvent {
	return s.events.Recent(n)
}

// ReplicationServer implements the gRPC Replication service.
type ReplicationServer struct {
	pb.UnimplementedReplicationServer
	im     *InstanceManager
	logger *slog.Logger

	// Resource protection tuning. Zero values use defaults from
	// replication_limits.go.
	MaxFrameRate float64 // max frames/sec per live stream (default: DefaultMaxFrameRate)
	RateBurst    int     // burst allowance for transient spikes (default: DefaultRateBurst)
	MaxLiveLag   uint64  // max frames live can lag before closing stream (default: DefaultMaxLiveLag)
}

// NewReplicationServer creates a new replication gRPC server.
func NewReplicationServer(im *InstanceManager, logger *slog.Logger) *ReplicationServer {
	return &ReplicationServer{
		im:     im,
		logger: logger,
	}
}

// Handshake exchanges sync state between boat and cloud.
func (s *ReplicationServer) Handshake(ctx context.Context, req *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	// Verify mTLS identity matches instance_id
	if err := s.verifyIdentity(ctx, req.InstanceId); err != nil {
		return nil, err
	}

	inst := s.im.GetOrCreate(req.InstanceId)

	inst.mu.Lock()
	defer inst.mu.Unlock()

	inst.BoatHeadSeq = req.HeadSeq
	inst.BoatJournalBytes = req.JournalBytes
	inst.LastSeen = time.Now()
	inst.Connected = true

	// If this is a reconnect and there's a gap between what we last
	// received live and what the boat is now streaming from, add a hole.
	if inst.Cursor > 0 && req.HeadSeq > inst.Cursor+1 {
		inst.HoleTracker.Add(inst.Cursor+1, req.HeadSeq)
	}

	// Start broker if needed
	inst.ensureBroker()

	// Build response
	holes := inst.HoleTracker.Holes()
	pbHoles := make([]*pb.SeqRange, len(holes))
	for i, h := range holes {
		pbHoles[i] = &pb.SeqRange{Start: h.Start, End: h.End}
	}

	liveStartFrom := req.HeadSeq
	if inst.Cursor == 0 {
		liveStartFrom = 1
	}

	resp := &pb.HandshakeResponse{
		Cursor:        inst.Cursor,
		Holes:         pbHoles,
		LiveStartFrom: liveStartFrom,
	}

	_ = inst.persist(inst.journalDir)

	s.logger.Info("handshake",
		"instance", req.InstanceId,
		"boat_head", req.HeadSeq,
		"cursor", inst.Cursor,
		"holes", len(holes),
		"live_start_from", liveStartFrom,
	)

	return resp, nil
}

// Live handles the realtime frame stream from a boat.
func (s *ReplicationServer) Live(stream pb.Replication_LiveServer) error {
	var inst *InstanceState
	var ackedThrough uint64
	var framesReceived uint64

	// Token bucket rate limiter: NMEA 2000 CAN bus can produce at most
	// ~1800 frames/sec. If a boat exceeds that, something is wrong
	// (uncapped replay, buggy client). Close the stream immediately
	// rather than applying backpressure.
	rl := rate.Limit(DefaultMaxFrameRate)
	if s.MaxFrameRate > 0 {
		rl = rate.Limit(s.MaxFrameRate)
	}
	burst := DefaultRateBurst
	if s.RateBurst > 0 {
		burst = s.RateBurst
	}
	limiter := rate.NewLimiter(rl, burst)

	maxLag := DefaultMaxLiveLag
	if s.MaxLiveLag > 0 {
		maxLag = s.MaxLiveLag
	}

	// Track the highest seq received on this live stream (not the cursor,
	// which only advances on continuous frames and stays stuck when holes
	// exist from backfill). Used for cloud-side lag detection.
	var lastLiveSeq uint64

	ackTicker := time.NewTicker(5 * time.Second)
	defer ackTicker.Stop()

	defer func() {
		if inst != nil {
			detail := map[string]any{"frames_received": framesReceived}
			inst.RecordEvent(EventLiveStop, detail)
		}
	}()

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if inst != nil {
				inst.mu.Lock()
				inst.Connected = false
				inst.mu.Unlock()
			}
			return err
		}

		switch m := msg.Msg.(type) {
		case *pb.LiveUpstream_Frame:
			frame := m.Frame
			if inst == nil {
				// Lazy resolve: we need the instance from context.
				// In practice, Handshake was called first. Find via peer cert.
				instanceID, err := s.extractInstanceID(stream.Context())
				if err != nil {
					return err
				}
				inst = s.im.Get(instanceID)
				if inst == nil {
					return status.Errorf(codes.FailedPrecondition, "handshake required before live stream")
				}
				inst.RecordEvent(EventLiveStart, map[string]any{
					"boat_head_seq": inst.BoatHeadSeq,
				})
			}

			framesReceived++

			// Rate limit: reject streams that exceed CAN bus physics.
			if !limiter.Allow() {
				s.logger.Warn("live frame rate exceeded, closing stream",
					"instance", inst.ID,
					"frames_received", framesReceived,
					"rate_limit", int(rl),
				)
				inst.RecordEvent(EventLiveStop, map[string]any{
					"frames_received": framesReceived,
					"reason":          "rate_limit_exceeded",
				})
				return status.Errorf(codes.ResourceExhausted,
					"live frame rate exceeded (%d/s max)", int(rl))
			}

			if frame.Seq > lastLiveSeq {
				lastLiveSeq = frame.Seq
			}

			inst.mu.Lock()
			inst.ensureBroker()
			inst.LastSeen = time.Now()

			// Feed frame into the replica broker
			rxFrame := RxFrame{
				Timestamp: time.UnixMicro(frame.TimestampUs),
				Header:    ParseCANID(frame.CanId),
				Data:      frame.Data,
				Seq:       frame.Seq,
			}

			select {
			case inst.broker.RxFrames() <- rxFrame:
			default:
				s.logger.Warn("live frame dropped (broker full)", "instance", inst.ID, "seq", frame.Seq)
			}

			// Update cursor if this extends our continuous range
			if frame.Seq == inst.Cursor+1 {
				inst.Cursor = frame.Seq
			}
			ackedThrough = inst.Cursor

			inst.mu.Unlock()

			// Checkpoint every 50k frames
			if framesReceived%50000 == 0 {
				inst.RecordEvent(EventCheckpoint, map[string]any{
					"frames_received": framesReceived,
					"cursor":          ackedThrough,
					"boat_head_seq":   inst.BoatHeadSeq,
				})
			}

			// Periodic ACK
			select {
			case <-ackTicker.C:
				if err := stream.Send(&pb.LiveDownstream{AckedThrough: ackedThrough}); err != nil {
					return err
				}
				inst.mu.Lock()
				_ = inst.persist(inst.journalDir)
				inst.mu.Unlock()
			default:
			}

		case *pb.LiveUpstream_Status:
			if inst != nil {
				inst.mu.Lock()
				inst.BoatHeadSeq = m.Status.HeadSeq
				inst.BoatJournalBytes = m.Status.JournalBytes
				inst.LastSeen = time.Now()
				inst.mu.Unlock()

				// Cloud-side lag detection: if the boat reports a head
				// far ahead of what we've received on this live stream,
				// the stream isn't keeping up. Force reconnect so the
				// boat jumps to head and backfills the gap.
				//
				// We compare against lastLiveSeq (not inst.Cursor)
				// because the cursor only advances on continuous frames
				// and stays stuck when backfill holes exist.
				if lastLiveSeq > 0 && m.Status.HeadSeq > lastLiveSeq && m.Status.HeadSeq-lastLiveSeq > maxLag {
					lag := m.Status.HeadSeq - lastLiveSeq
					s.logger.Warn("cloud-side live lag exceeded, closing stream",
						"instance", inst.ID,
						"boat_head", m.Status.HeadSeq,
						"last_live_seq", lastLiveSeq,
						"lag", lag,
						"threshold", maxLag,
					)
					inst.RecordEvent(EventLiveStop, map[string]any{
						"frames_received": framesReceived,
						"reason":          "cloud_lag_exceeded",
						"lag":             lag,
					})
					return status.Errorf(codes.ResourceExhausted,
						"live lag %d exceeds threshold %d", lag, maxLag)
				}
			}
		}
	}
}

// Backfill handles bulk block transfer for filling gaps.
func (s *ReplicationServer) Backfill(stream pb.Replication_BackfillServer) error {
	instanceID, err := s.extractInstanceID(stream.Context())
	if err != nil {
		return err
	}

	inst := s.im.Get(instanceID)
	if inst == nil {
		return status.Errorf(codes.FailedPrecondition, "handshake required before backfill")
	}

	inst.mu.Lock()
	journalDir := filepath.Join(inst.journalDir, "journal")
	onRotate := inst.onRotate
	holes := inst.HoleTracker.Len()
	inst.mu.Unlock()

	inst.RecordEvent(EventBackfillStart, map[string]any{"holes": holes})

	// Create a block writer for this backfill session
	bw, err := NewBlockWriter(BlockWriterConfig{
		Dir:         journalDir,
		Prefix:      "backfill",
		BlockSize:   262144, // will be overridden by first block
		Compression: journal.CompressionNone,
		OnRotate:    onRotate,
	})
	if err != nil {
		return status.Errorf(codes.Internal, "create block writer: %v", err)
	}
	defer func() { _ = bw.Close() }()

	blocksReceived := 0
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		block := msg.Block
		if block == nil {
			continue
		}

		// Update block writer config from the block metadata
		if block.BlockSize > 0 {
			bw.cfg.BlockSize = int(block.BlockSize)
		}
		if block.Compressed {
			bw.cfg.Compression = journal.CompressionZstd
		}

		if err := bw.AppendBlock(block.BaseSeq, block.BaseTimeUs, block.Data, block.Compressed); err != nil {
			s.logger.Error("backfill block write failed",
				"instance", instanceID,
				"base_seq", block.BaseSeq,
				"error", err,
			)
			continue
		}

		blocksReceived++

		inst.RecordEvent(EventBlockReceived, map[string]any{
			"base_seq":    block.BaseSeq,
			"frame_count": block.FrameCount,
			"bytes":       len(block.Data),
			"compressed":  block.Compressed,
		})

		// Mark the filled range in the hole tracker
		endSeq := block.BaseSeq + uint64(block.FrameCount)
		inst.mu.Lock()
		inst.HoleTracker.Fill(block.BaseSeq, endSeq)
		inst.LastSeen = time.Now()

		// Update cursor: continuous from 0 means cursor advances
		continuous := inst.HoleTracker.ContinuousThrough(0)
		if continuous != ^uint64(0) && continuous > inst.Cursor {
			inst.Cursor = continuous
		} else if continuous == ^uint64(0) && inst.BoatHeadSeq > 0 {
			inst.Cursor = inst.BoatHeadSeq
		}

		// Build response
		remaining := inst.HoleTracker.Holes()
		pbRemaining := make([]*pb.SeqRange, len(remaining))
		for i, h := range remaining {
			pbRemaining[i] = &pb.SeqRange{Start: h.Start, End: h.End}
		}

		resp := &pb.BackfillDownstream{
			ContinuousThrough: inst.Cursor,
			Remaining:         pbRemaining,
		}
		inst.mu.Unlock()

		if err := stream.Send(resp); err != nil {
			return err
		}

		// Persist periodically
		if blocksReceived%10 == 0 {
			inst.mu.Lock()
			_ = inst.persist(inst.journalDir)
			inst.mu.Unlock()
		}
	}

	// Final persist
	inst.mu.Lock()
	_ = inst.persist(inst.journalDir)
	inst.mu.Unlock()

	inst.RecordEvent(EventBackfillStop, map[string]any{
		"blocks_received": blocksReceived,
	})

	s.logger.Info("backfill complete",
		"instance", instanceID,
		"blocks", blocksReceived,
	)

	return nil
}

// verifyIdentity checks that the mTLS peer certificate CN matches the
// requested instance ID. Returns nil if TLS is not configured (testing).
func (s *ReplicationServer) verifyIdentity(ctx context.Context, instanceID string) error {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil // no peer info (e.g. in tests without TLS)
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil // no TLS
	}
	if len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return status.Errorf(codes.Unauthenticated, "no verified client certificate")
	}

	cert := tlsInfo.State.VerifiedChains[0][0]
	if cert.Subject.CommonName != instanceID {
		return status.Errorf(codes.PermissionDenied,
			"cert CN %q does not match instance_id %q", cert.Subject.CommonName, instanceID)
	}
	return nil
}

// extractInstanceID gets the instance ID from the mTLS peer certificate CN,
// falling back to the x-instance-id gRPC metadata header.
func (s *ReplicationServer) extractInstanceID(ctx context.Context) (string, error) {
	// Try mTLS first
	if p, ok := peer.FromContext(ctx); ok {
		if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			if len(tlsInfo.State.VerifiedChains) > 0 && len(tlsInfo.State.VerifiedChains[0]) > 0 {
				return tlsInfo.State.VerifiedChains[0][0].Subject.CommonName, nil
			}
		}
	}

	// Fall back to metadata (used in tests and when mTLS is not configured)
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if ids := md.Get("x-instance-id"); len(ids) > 0 {
			return ids[0], nil
		}
	}

	return "", status.Errorf(codes.Unauthenticated, "no instance ID: provide mTLS cert or x-instance-id metadata")
}

// GetInstanceBroker returns the broker for an instance (starting it if needed).
// Used by HTTP handlers to serve SSE from a cloud instance.
func (s *ReplicationServer) GetInstanceBroker(instanceID string) *Broker {
	inst := s.im.Get(instanceID)
	if inst == nil {
		return nil
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.ensureBroker()
	return inst.broker
}

// GetInstanceState returns the instance state for status reporting.
func (s *ReplicationServer) GetInstanceState(instanceID string) *InstanceState {
	return s.im.Get(instanceID)
}
