package lplex

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/sixfathoms/lplex/journal"
	pb "github.com/sixfathoms/lplex/proto/replication/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// ReplicationClientConfig configures the boat-side replication client.
type ReplicationClientConfig struct {
	Target     string // cloud gRPC address (host:port)
	InstanceID string
	CertFile   string // client certificate
	KeyFile    string // client private key
	CAFile     string // CA certificate for verifying server
	Logger     *slog.Logger
}

// ReplicationClient streams frames from the local broker to a cloud
// replication server over gRPC. It runs two independent streams: one for
// live frames (from the broker's head forward) and one for backfilling
// historical gaps (from journal files). On disconnect, it reconnects with
// exponential backoff and resumes both streams.
type ReplicationClient struct {
	cfg    ReplicationClientConfig
	broker *Broker
	logger *slog.Logger

	// Sync state (protected by mu)
	mu           sync.Mutex
	cloudCursor  uint64
	holes        []SeqRange
	connected    bool
	lastAck      time.Time
	liveStartSeq uint64
}

// NewReplicationClient creates a new replication client. Call Run to start.
func NewReplicationClient(cfg ReplicationClientConfig, broker *Broker) *ReplicationClient {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &ReplicationClient{
		cfg:    cfg,
		broker: broker,
		logger: cfg.Logger.With("component", "replication"),
	}
}

// Run is the main loop. Connects to the cloud, performs handshake, and starts
// live + backfill streams. Reconnects on failure with exponential backoff.
// Blocks until ctx is cancelled.
func (c *ReplicationClient) Run(ctx context.Context) error {
	backoff := time.Second
	const maxBackoff = 60 * time.Second

	for {
		err := c.connectAndStream(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()

		c.logger.Warn("replication disconnected, reconnecting",
			"error", err,
			"backoff", backoff,
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		backoff = min(backoff*2, maxBackoff)
	}
}

// Status returns the current replication state for status reporting.
func (c *ReplicationClient) Status() ReplicationStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	headSeq := c.broker.CurrentSeq()
	var liveLag uint64
	if headSeq > c.cloudCursor {
		liveLag = headSeq - c.cloudCursor
	}

	var backfillRemaining uint64
	for _, h := range c.holes {
		backfillRemaining += h.End - h.Start
	}

	return ReplicationStatus{
		Connected:            c.connected,
		InstanceID:           c.cfg.InstanceID,
		LocalHeadSeq:         headSeq,
		CloudCursor:          c.cloudCursor,
		Holes:                append([]SeqRange(nil), c.holes...),
		LiveLag:              liveLag,
		BackfillRemainingSeqs: backfillRemaining,
		LastAck:              c.lastAck,
	}
}

// ReplicationStatus is the boat-side view of replication state.
type ReplicationStatus struct {
	Connected            bool       `json:"connected"`
	InstanceID           string     `json:"instance_id"`
	LocalHeadSeq         uint64     `json:"local_head_seq"`
	CloudCursor          uint64     `json:"cloud_cursor"`
	Holes                []SeqRange `json:"holes,omitzero"`
	LiveLag              uint64     `json:"live_lag"`
	BackfillRemainingSeqs uint64    `json:"backfill_remaining_seqs"`
	LastAck              time.Time  `json:"last_ack,omitempty"`
}

func (c *ReplicationClient) connectAndStream(ctx context.Context) error {
	// Build TLS config
	dialOpts, err := c.buildDialOptions()
	if err != nil {
		return fmt.Errorf("build dial options: %w", err)
	}

	conn, err := grpc.NewClient(c.cfg.Target, dialOpts...)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewReplicationClient(conn)

	// Handshake
	headSeq := c.broker.CurrentSeq()
	journalBytes := c.totalJournalBytes()

	resp, err := client.Handshake(ctx, &pb.HandshakeRequest{
		InstanceId:   c.cfg.InstanceID,
		HeadSeq:      headSeq,
		JournalBytes: journalBytes,
	})
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}

	c.mu.Lock()
	c.cloudCursor = resp.Cursor
	c.holes = make([]SeqRange, len(resp.Holes))
	for i, h := range resp.Holes {
		c.holes[i] = SeqRange{Start: h.Start, End: h.End}
	}
	c.connected = true
	c.liveStartSeq = resp.LiveStartFrom
	c.mu.Unlock()

	c.logger.Info("handshake complete",
		"cursor", resp.Cursor,
		"holes", len(resp.Holes),
		"live_start_from", resp.LiveStartFrom,
	)

	// Run live and backfill streams concurrently
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	// Live stream
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.runLiveStream(streamCtx, client); err != nil {
			if streamCtx.Err() == nil {
				c.logger.Error("live stream failed", "error", err)
				cancel()
			}
		}
	}()

	// Backfill stream (only if there are holes)
	c.mu.Lock()
	hasHoles := len(c.holes) > 0
	c.mu.Unlock()

	if hasHoles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.runBackfillStream(streamCtx, client); err != nil {
				if streamCtx.Err() == nil {
					c.logger.Error("backfill stream failed", "error", err)
					cancel()
				}
			}
		}()
	}

	wg.Wait()
	return streamCtx.Err()
}

func (c *ReplicationClient) streamContext(ctx context.Context) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("x-instance-id", c.cfg.InstanceID))
}

func (c *ReplicationClient) runLiveStream(ctx context.Context, client pb.ReplicationClient) error {
	stream, err := client.Live(c.streamContext(ctx))
	if err != nil {
		return fmt.Errorf("open live stream: %w", err)
	}

	// Start the consumer from the position the cloud requested, so we replay
	// any frames the cloud hasn't seen yet. On a fresh instance this is seq 1;
	// on reconnect it's the cloud's current head.
	c.mu.Lock()
	startSeq := c.liveStartSeq
	c.mu.Unlock()

	// If the cloud is already caught up past our current head, start from
	// our next frame instead of waiting for old data.
	if head := c.broker.CurrentSeq(); startSeq == 0 || startSeq > head+1 {
		startSeq = head + 1
	}

	consumer := c.broker.NewConsumer(ConsumerConfig{
		Cursor: startSeq,
	})
	defer func() { _ = consumer.Close() }()

	// ACK reader goroutine
	go func() {
		for {
			resp, err := stream.Recv()
			if err != nil {
				return
			}
			c.mu.Lock()
			c.cloudCursor = resp.AckedThrough
			c.lastAck = time.Now()
			c.mu.Unlock()
		}
	}()

	// Status ticker
	statusTicker := time.NewTicker(5 * time.Second)
	defer statusTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-statusTicker.C:
			headSeq := c.broker.CurrentSeq()
			if err := stream.Send(&pb.LiveUpstream{
				Msg: &pb.LiveUpstream_Status{
					Status: &pb.LiveStatus{
						HeadSeq:      headSeq,
						JournalBytes: c.totalJournalBytes(),
					},
				},
			}); err != nil {
				return err
			}
		default:
		}

		frame, err := consumer.Next(ctx)
		if err != nil {
			return err
		}

		canID := BuildCANID(frame.Header)
		if err := stream.Send(&pb.LiveUpstream{
			Msg: &pb.LiveUpstream_Frame{
				Frame: &pb.LiveFrame{
					Seq:         frame.Seq,
					TimestampUs: frame.Timestamp.UnixMicro(),
					CanId:       canID,
					Data:        frame.Data,
				},
			},
		}); err != nil {
			return err
		}
	}
}

func (c *ReplicationClient) runBackfillStream(ctx context.Context, client pb.ReplicationClient) error {
	c.mu.Lock()
	holes := append([]SeqRange(nil), c.holes...)
	c.mu.Unlock()

	if len(holes) == 0 {
		return nil
	}

	stream, err := client.Backfill(c.streamContext(ctx))
	if err != nil {
		return fmt.Errorf("open backfill stream: %w", err)
	}

	// Process holes newest-first (most relevant data first)
	sort.Slice(holes, func(i, j int) bool {
		return holes[i].Start > holes[j].Start
	})

	journalDir := c.broker.journalDir
	if journalDir == "" {
		c.logger.Warn("no journal dir configured, cannot backfill")
		return nil
	}

	// ACK reader goroutine
	go func() {
		for {
			resp, err := stream.Recv()
			if err != nil {
				return
			}
			c.mu.Lock()
			c.cloudCursor = resp.ContinuousThrough
			c.holes = make([]SeqRange, len(resp.Remaining))
			for i, h := range resp.Remaining {
				c.holes[i] = SeqRange{Start: h.Start, End: h.End}
			}
			c.lastAck = time.Now()
			c.mu.Unlock()
		}
	}()

	for _, hole := range holes {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := c.backfillHole(ctx, stream, journalDir, hole); err != nil {
			c.logger.Error("backfill hole failed",
				"start", hole.Start,
				"end", hole.End,
				"error", err,
			)
			continue
		}
	}

	return stream.CloseSend()
}

// backfillHole reads raw blocks from journal files that cover the given hole
// and sends them to the cloud.
func (c *ReplicationClient) backfillHole(ctx context.Context, stream pb.Replication_BackfillClient, journalDir string, hole SeqRange) error {
	files, err := filepath.Glob(filepath.Join(journalDir, "*.lpj"))
	if err != nil || len(files) == 0 {
		return fmt.Errorf("no journal files found in %s", journalDir)
	}
	sort.Strings(files)

	for _, path := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := c.backfillFromFile(ctx, stream, path, hole); err != nil {
			c.logger.Warn("backfill file error", "path", path, "error", err)
			continue
		}
	}

	return nil
}

// backfillFromFile reads blocks from a single journal file and sends any
// that overlap with the target hole.
func (c *ReplicationClient) backfillFromFile(ctx context.Context, stream pb.Replication_BackfillClient, path string, hole SeqRange) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	r, err := journal.NewReader(f)
	if err != nil {
		return err
	}
	if r.Version() != journal.Version2 {
		return nil // skip v1 files
	}

	blockSize := r.BlockSize()
	compression := r.Compression()

	for i := range r.BlockCount() {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		info, err := r.InspectBlock(i)
		if err != nil {
			continue
		}

		// Skip blocks entirely outside the hole
		blockEnd := info.BaseSeq + uint64(info.FrameCount)
		if blockEnd <= hole.Start || info.BaseSeq >= hole.End {
			continue
		}

		// Read the raw block data from disk
		blockData, err := c.readRawBlock(f, r, i)
		if err != nil {
			c.logger.Warn("failed to read raw block", "path", path, "block", i, "error", err)
			continue
		}

		compressed := compression != journal.CompressionNone

		if err := stream.Send(&pb.BackfillUpstream{
			Block: &pb.Block{
				BaseSeq:    info.BaseSeq,
				BaseTimeUs: info.BaseTime.UnixMicro(),
				FrameCount: uint32(info.FrameCount),
				Data:       blockData,
				Compressed: compressed,
				BlockSize:  uint32(blockSize),
			},
		}); err != nil {
			return err
		}
	}

	return nil
}

// readRawBlock reads the raw bytes of block i from a journal file.
// For uncompressed files, this is the full block. For compressed files,
// this is the compressed payload (without the header).
func (c *ReplicationClient) readRawBlock(f *os.File, r *journal.Reader, blockIdx int) ([]byte, error) {
	info, err := r.InspectBlock(blockIdx)
	if err != nil {
		return nil, err
	}

	if r.Compression() == journal.CompressionNone {
		// Uncompressed: read the full block at offset
		buf := make([]byte, r.BlockSize())
		if _, err := f.ReadAt(buf, info.Offset); err != nil {
			return nil, err
		}
		return buf, nil
	}

	// Compressed: read the compressed payload (skip the header).
	// Header for zstd v2: BaseTime(8) + BaseSeq(8) + CompressedLen(4) = 20 bytes
	payloadOffset := info.Offset + 20
	if info.CompressedLen <= 0 {
		return nil, fmt.Errorf("block %d has no compressed data", blockIdx)
	}
	buf := make([]byte, info.CompressedLen)
	if _, err := f.ReadAt(buf, payloadOffset); err != nil {
		return nil, err
	}
	return buf, nil
}

func (c *ReplicationClient) buildDialOptions() ([]grpc.DialOption, error) {
	if c.cfg.CertFile == "" {
		// No TLS (for testing)
		return []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, nil
	}

	cert, err := tls.LoadX509KeyPair(c.cfg.CertFile, c.cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load client cert: %w", err)
	}

	caCert, err := os.ReadFile(c.cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA cert")
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
	}

	return []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	}, nil
}

func (c *ReplicationClient) totalJournalBytes() uint64 {
	if c.broker.journalDir == "" {
		return 0
	}
	files, err := filepath.Glob(filepath.Join(c.broker.journalDir, "*.lpj"))
	if err != nil {
		return 0
	}
	var total uint64
	for _, f := range files {
		info, err := os.Stat(f)
		if err == nil {
			total += uint64(info.Size())
		}
	}
	return total
}

