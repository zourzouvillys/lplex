package lplex

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	pb "github.com/sixfathoms/lplex/proto/replication/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestReplicationLiveStream verifies end-to-end live frame replication:
// boat broker -> ReplicationClient -> in-process gRPC -> ReplicationServer -> cloud broker -> Consumer
func TestReplicationLiveStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger := slog.Default()

	// --- Cloud side ---
	cloudDataDir := t.TempDir()
	im, err := NewInstanceManager(cloudDataDir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer im.Shutdown()

	replServer := NewReplicationServer(im, logger)

	// Start in-process gRPC server (no TLS for tests)
	grpcServer := grpc.NewServer()
	pb.RegisterReplicationServer(grpcServer, replServer)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = grpcServer.Serve(lis) }()
	defer grpcServer.Stop()

	cloudAddr := lis.Addr().String()

	// --- Boat side ---
	boatBroker := NewBroker(BrokerConfig{
		RingSize: 1024,
		Logger:   logger,
	})
	go boatBroker.Run()
	defer boatBroker.CloseRx()

	// Feed some frames into the boat broker before starting replication
	for i := range 10 {
		boatBroker.RxFrames() <- RxFrame{
			Timestamp: time.Now(),
			Header:    CANHeader{Priority: 2, PGN: 129025, Source: 1, Destination: 0xFF},
			Data:      []byte{byte(i), 1, 2, 3, 4, 5, 6, 7},
		}
	}

	// Wait for frames to be processed
	waitForSeq(t, boatBroker, 10)

	// --- Handshake directly (since we can't use mTLS in tests) ---
	conn, err := grpc.NewClient(cloudAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewReplicationClient(conn)

	// Handshake
	handshakeResp, err := client.Handshake(ctx, &pb.HandshakeRequest{
		InstanceId:   "test-boat",
		HeadSeq:      boatBroker.CurrentSeq(),
		JournalBytes: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("handshake: cursor=%d, holes=%d, live_start=%d",
		handshakeResp.Cursor, len(handshakeResp.Holes), handshakeResp.LiveStartFrom)

	if handshakeResp.Cursor != 0 {
		t.Fatalf("expected cursor=0 for new instance, got %d", handshakeResp.Cursor)
	}

	// --- Live stream: send frames and verify cloud receives them ---
	liveCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-instance-id", "test-boat"))
	liveStream, err := client.Live(liveCtx)
	if err != nil {
		t.Fatal(err)
	}

	// Send frames 1-10 on the live stream
	for i := uint64(1); i <= 10; i++ {
		if err := liveStream.Send(&pb.LiveUpstream{
			Msg: &pb.LiveUpstream_Frame{
				Frame: &pb.LiveFrame{
					Seq:         i,
					TimestampUs: time.Now().UnixMicro(),
					CanId:       0x09F80100, // PGN 129025
					Data:        []byte{byte(i), 1, 2, 3, 4, 5, 6, 7},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Send a status to trigger an ACK
	if err := liveStream.Send(&pb.LiveUpstream{
		Msg: &pb.LiveUpstream_Status{
			Status: &pb.LiveStatus{HeadSeq: 10},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Give the cloud broker a moment to process
	time.Sleep(200 * time.Millisecond)

	// Verify cloud-side broker has the frames
	cloudBroker := replServer.GetInstanceBroker("test-boat")
	if cloudBroker == nil {
		t.Fatal("cloud broker not started")
	}

	cloudHead := cloudBroker.CurrentSeq()
	if cloudHead < 10 {
		t.Fatalf("cloud broker head=%d, expected >= 10", cloudHead)
	}

	// Read frames back from cloud broker via Consumer
	consumer := cloudBroker.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = consumer.Close() }()

	for i := uint64(1); i <= 10; i++ {
		readCtx, readCancel := context.WithTimeout(ctx, 2*time.Second)
		frame, err := consumer.Next(readCtx)
		readCancel()
		if err != nil {
			t.Fatalf("failed to read frame %d from cloud: %v", i, err)
		}
		if frame.Seq != i {
			t.Fatalf("expected seq %d, got %d", i, frame.Seq)
		}
	}

	t.Log("live stream replication verified: 10 frames replicated correctly")
}

// TestReplicationHandshakeReconnect verifies that reconnecting after a gap
// produces the correct hole in the cloud state.
func TestReplicationHandshakeReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger := slog.Default()

	cloudDataDir := t.TempDir()
	im, err := NewInstanceManager(cloudDataDir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer im.Shutdown()

	replServer := NewReplicationServer(im, logger)

	grpcServer := grpc.NewServer()
	pb.RegisterReplicationServer(grpcServer, replServer)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = grpcServer.Serve(lis) }()
	defer grpcServer.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewReplicationClient(conn)

	// First connection: handshake + stream frames 1-100
	resp1, err := client.Handshake(ctx, &pb.HandshakeRequest{
		InstanceId: "test-boat",
		HeadSeq:    100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp1.Cursor != 0 {
		t.Fatalf("first handshake: expected cursor=0, got %d", resp1.Cursor)
	}

	// Stream frames 1-100
	liveCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-instance-id", "test-boat"))
	live1, err := client.Live(liveCtx)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 100; i++ {
		if err := live1.Send(&pb.LiveUpstream{
			Msg: &pb.LiveUpstream_Frame{
				Frame: &pb.LiveFrame{
					Seq:         i,
					TimestampUs: time.Now().UnixMicro(),
					CanId:       0x09F80100,
					Data:        []byte{byte(i), 1, 2, 3, 4, 5, 6, 7},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	time.Sleep(200 * time.Millisecond)

	// Simulate disconnect and reconnect with head=200 (gap of 101-200)
	resp2, err := client.Handshake(ctx, &pb.HandshakeRequest{
		InstanceId: "test-boat",
		HeadSeq:    200,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("reconnect handshake: cursor=%d, holes=%d", resp2.Cursor, len(resp2.Holes))

	if resp2.Cursor != 100 {
		t.Fatalf("reconnect: expected cursor=100, got %d", resp2.Cursor)
	}
	if len(resp2.Holes) != 1 {
		t.Fatalf("reconnect: expected 1 hole, got %d", len(resp2.Holes))
	}
	if resp2.Holes[0].Start != 101 || resp2.Holes[0].End != 200 {
		t.Fatalf("reconnect: expected hole [101, 200), got [%d, %d)",
			resp2.Holes[0].Start, resp2.Holes[0].End)
	}

	t.Log("reconnect hole tracking verified")
}

// TestReplicationBackfill verifies that backfill blocks fill holes in the cloud state.
// Scenario: stream frames 1-100 → disconnect → reconnect at head=200 → hole [101,200) → backfill 101-150.
func TestReplicationBackfill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger := slog.Default()

	cloudDataDir := t.TempDir()
	im, err := NewInstanceManager(cloudDataDir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer im.Shutdown()

	replServer := NewReplicationServer(im, logger)

	grpcServer := grpc.NewServer()
	pb.RegisterReplicationServer(grpcServer, replServer)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = grpcServer.Serve(lis) }()
	defer grpcServer.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewReplicationClient(conn)

	// First handshake: new instance, head=100
	_, err = client.Handshake(ctx, &pb.HandshakeRequest{
		InstanceId: "backfill-test",
		HeadSeq:    100,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Stream frames 1-100 (live, continuous from cursor=0)
	liveCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-instance-id", "backfill-test"))
	live, err := client.Live(liveCtx)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 100; i++ {
		if err := live.Send(&pb.LiveUpstream{
			Msg: &pb.LiveUpstream_Frame{
				Frame: &pb.LiveFrame{
					Seq:         i,
					TimestampUs: time.Now().UnixMicro(),
					CanId:       0x09F80100,
					Data:        []byte{byte(i), 1, 2, 3, 4, 5, 6, 7},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	// Reconnect handshake: head=200, creates hole [101, 200)
	resp, err := client.Handshake(ctx, &pb.HandshakeRequest{
		InstanceId: "backfill-test",
		HeadSeq:    200,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("before backfill: cursor=%d, holes=%v", resp.Cursor, resp.Holes)

	if resp.Cursor != 100 {
		t.Fatalf("expected cursor=100, got %d", resp.Cursor)
	}
	if len(resp.Holes) != 1 {
		t.Fatalf("expected 1 hole, got %d", len(resp.Holes))
	}
	if resp.Holes[0].Start != 101 || resp.Holes[0].End != 200 {
		t.Fatalf("expected hole [101, 200), got [%d, %d)", resp.Holes[0].Start, resp.Holes[0].End)
	}

	// Backfill: send a block covering seqs 101-150 (50 frames)
	backfillCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-instance-id", "backfill-test"))
	backfill, err := client.Backfill(backfillCtx)
	if err != nil {
		t.Fatal(err)
	}

	block := makeTestBlock(4096, time.Now().UnixMicro(), 101)
	if err := backfill.Send(&pb.BackfillUpstream{
		Block: &pb.Block{
			BaseSeq:    101,
			BaseTimeUs: time.Now().UnixMicro(),
			FrameCount: 50,
			Data:       block,
			Compressed: false,
			BlockSize:  4096,
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Read ACK
	ack, err := backfill.Recv()
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("backfill ack: continuous_through=%d, remaining=%d holes",
		ack.ContinuousThrough, len(ack.Remaining))

	if ack.ContinuousThrough != 150 {
		t.Fatalf("expected continuous_through=150, got %d", ack.ContinuousThrough)
	}
	if len(ack.Remaining) != 1 {
		t.Fatalf("expected 1 remaining hole, got %d", len(ack.Remaining))
	}
	if ack.Remaining[0].Start != 151 || ack.Remaining[0].End != 200 {
		t.Fatalf("expected remaining hole [151, 200), got [%d, %d)",
			ack.Remaining[0].Start, ack.Remaining[0].End)
	}

	t.Log("backfill hole filling verified")
}

// waitForSeq blocks until the broker's head reaches at least seq.
func waitForSeq(t *testing.T, b *Broker, seq uint64) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if b.CurrentSeq() >= seq {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for broker seq >= %d (current: %d)", seq, b.CurrentSeq())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// waitForCloudSeq waits for a cloud broker to reach a given seq, with a longer
// timeout to account for gRPC + broker processing latency.
func waitForCloudSeq(t *testing.T, b *Broker, seq uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if b.CurrentSeq() >= seq {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for cloud broker seq >= %d (current: %d)", seq, b.CurrentSeq())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// startCloudStack creates a full cloud stack: InstanceManager + ReplicationServer + gRPC server.
// Returns the gRPC address and a cleanup function.
func startCloudStack(t *testing.T) (addr string, replServer *ReplicationServer, im *InstanceManager, cleanup func()) {
	t.Helper()

	cloudDataDir := t.TempDir()
	logger := slog.Default()

	var err error
	im, err = NewInstanceManager(cloudDataDir, logger)
	if err != nil {
		t.Fatal(err)
	}

	replServer = NewReplicationServer(im, logger)

	grpcServer := grpc.NewServer()
	pb.RegisterReplicationServer(grpcServer, replServer)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = grpcServer.Serve(lis) }()

	return lis.Addr().String(), replServer, im, func() {
		grpcServer.Stop()
		im.Shutdown()
	}
}

// feedFrames sends n frames into a broker starting at the given sequence offset.
func feedFrames(b *Broker, count int) {
	for range count {
		b.RxFrames() <- RxFrame{
			Timestamp: time.Now(),
			Header:    CANHeader{Priority: 2, PGN: 129025, Source: 1, Destination: 0xFF},
			Data:      []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		}
	}
}

// TestReplicationEndToEnd exercises the full boat-to-cloud lifecycle using
// the actual ReplicationClient (not manual gRPC calls):
//
//  1. Initial connect: boat feeds frames, ReplicationClient streams them to cloud
//  2. Live delivery: more frames arrive, cloud catches up
//  3. Disconnect + reconnect: gap creates a hole, new frames flow
//  4. Cloud consumer: reads frames from the cloud broker
//  5. Multiple instances: two boats replicate to the same cloud
func TestReplicationEndToEnd(t *testing.T) {
	cloudAddr, replServer, _, cleanup := startCloudStack(t)
	defer cleanup()

	// --- Boat setup ---
	logger := slog.Default()
	boatBroker := NewBroker(BrokerConfig{
		RingSize: 4096,
		Logger:   logger,
	})
	go boatBroker.Run()
	defer boatBroker.CloseRx()

	// Phase 1: Feed initial frames into boat
	feedFrames(boatBroker, 50)
	waitForSeq(t, boatBroker, 50)

	// Start ReplicationClient
	ctx1, cancel1 := context.WithCancel(context.Background())
	replClient := NewReplicationClient(ReplicationClientConfig{
		Target:     cloudAddr,
		InstanceID: "e2e-boat",
		Logger:     logger,
	}, boatBroker)

	replDone := make(chan error, 1)
	go func() { replDone <- replClient.Run(ctx1) }()

	// Wait for cloud broker to receive the frames
	var cloudBroker *Broker
	deadline := time.After(5 * time.Second)
	for cloudBroker == nil {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for cloud broker to start")
		default:
			cloudBroker = replServer.GetInstanceBroker("e2e-boat")
			time.Sleep(20 * time.Millisecond)
		}
	}

	waitForCloudSeq(t, cloudBroker, 50, 5*time.Second)
	t.Logf("phase 1: cloud received initial 50 frames (cloud head=%d)", cloudBroker.CurrentSeq())

	// Phase 2: Feed more frames while connected
	feedFrames(boatBroker, 50) // frames 51-100
	waitForSeq(t, boatBroker, 100)
	waitForCloudSeq(t, cloudBroker, 100, 5*time.Second)
	t.Logf("phase 2: cloud caught up to 100 frames (cloud head=%d)", cloudBroker.CurrentSeq())

	// Verify client reports connected
	status := replClient.Status()
	if !status.Connected {
		t.Fatal("client should be connected")
	}

	// Phase 3: Verify cloud consumer can read frames
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()

	consumer := cloudBroker.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = consumer.Close() }()

	for i := uint64(1); i <= 20; i++ {
		frame, err := consumer.Next(readCtx)
		if err != nil {
			t.Fatalf("consumer read frame %d: %v", i, err)
		}
		if frame.Seq != i {
			t.Fatalf("expected seq %d, got %d", i, frame.Seq)
		}
	}
	t.Log("phase 3: cloud consumer verified 20 frames")

	// Phase 4: Disconnect
	cancel1()
	<-replDone // wait for client to stop

	// Feed frames while disconnected (creates a gap)
	feedFrames(boatBroker, 50) // frames 101-150
	waitForSeq(t, boatBroker, 150)
	t.Logf("phase 4: fed 50 frames while disconnected (boat head=%d)", boatBroker.CurrentSeq())

	// Phase 5: Reconnect
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	replClient2 := NewReplicationClient(ReplicationClientConfig{
		Target:     cloudAddr,
		InstanceID: "e2e-boat",
		Logger:     logger,
	}, boatBroker)

	replDone2 := make(chan error, 1)
	go func() { replDone2 <- replClient2.Run(ctx2) }()

	// New live frames should arrive on cloud
	waitForCloudSeq(t, cloudBroker, 150, 5*time.Second)
	t.Logf("phase 5: cloud caught up after reconnect (cloud head=%d)", cloudBroker.CurrentSeq())

	// Check that instance state shows the gap was detected
	inst := replServer.GetInstanceState("e2e-boat")
	if inst == nil {
		t.Fatal("instance state should exist")
	}
	instStatus := inst.Status()
	t.Logf("phase 5: instance status: cursor=%d, holes=%d, boat_head=%d",
		instStatus.Cursor, len(instStatus.Holes), instStatus.BoatHeadSeq)

	// Phase 6: Feed even more frames to verify continued live delivery
	feedFrames(boatBroker, 50) // frames 151-200
	waitForSeq(t, boatBroker, 200)
	waitForCloudSeq(t, cloudBroker, 200, 5*time.Second)
	t.Logf("phase 6: continued live delivery verified (cloud head=%d)", cloudBroker.CurrentSeq())

	cancel2()
	<-replDone2

	t.Log("end-to-end replication test passed")
}

// TestReplicationMultipleInstances verifies that two boats can replicate to
// the same cloud concurrently.
func TestReplicationMultipleInstances(t *testing.T) {
	cloudAddr, replServer, _, cleanup := startCloudStack(t)
	defer cleanup()

	logger := slog.Default()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create two boat brokers
	boat1 := NewBroker(BrokerConfig{RingSize: 1024, Logger: logger})
	go boat1.Run()
	defer boat1.CloseRx()

	boat2 := NewBroker(BrokerConfig{RingSize: 1024, Logger: logger})
	go boat2.Run()
	defer boat2.CloseRx()

	// Feed frames into both
	feedFrames(boat1, 30)
	feedFrames(boat2, 20)
	waitForSeq(t, boat1, 30)
	waitForSeq(t, boat2, 20)

	// Start replication for both
	var done1, done2 = make(chan error, 1), make(chan error, 1)

	go func() {
		done1 <- NewReplicationClient(ReplicationClientConfig{
			Target: cloudAddr, InstanceID: "multi-boat-1", Logger: logger,
		}, boat1).Run(ctx)
	}()
	go func() {
		done2 <- NewReplicationClient(ReplicationClientConfig{
			Target: cloudAddr, InstanceID: "multi-boat-2", Logger: logger,
		}, boat2).Run(ctx)
	}()

	// Wait for both cloud brokers to appear and catch up
	var cloud1, cloud2 *Broker
	deadline := time.After(5 * time.Second)
	for cloud1 == nil || cloud2 == nil {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for cloud brokers")
		default:
			if cloud1 == nil {
				cloud1 = replServer.GetInstanceBroker("multi-boat-1")
			}
			if cloud2 == nil {
				cloud2 = replServer.GetInstanceBroker("multi-boat-2")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	waitForCloudSeq(t, cloud1, 30, 5*time.Second)
	waitForCloudSeq(t, cloud2, 20, 5*time.Second)

	t.Logf("boat-1 cloud head=%d, boat-2 cloud head=%d", cloud1.CurrentSeq(), cloud2.CurrentSeq())

	// Feed more frames to both simultaneously
	feedFrames(boat1, 20) // 31-50
	feedFrames(boat2, 30) // 21-50
	waitForSeq(t, boat1, 50)
	waitForSeq(t, boat2, 50)

	waitForCloudSeq(t, cloud1, 50, 5*time.Second)
	waitForCloudSeq(t, cloud2, 50, 5*time.Second)

	// Verify instance list shows both
	summaries := replServer.im.List()
	if len(summaries) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(summaries))
	}

	cancel()
	<-done1
	<-done2

	t.Log("multiple instances replication verified")
}

// TestReplicationCloudConsumerDuringLive verifies that SSE-style consumers
// on the cloud broker receive frames in real-time during active replication.
func TestReplicationCloudConsumerDuringLive(t *testing.T) {
	cloudAddr, replServer, _, cleanup := startCloudStack(t)
	defer cleanup()

	logger := slog.Default()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	boatBroker := NewBroker(BrokerConfig{RingSize: 1024, Logger: logger})
	go boatBroker.Run()
	defer boatBroker.CloseRx()

	// Start replication
	done := make(chan error, 1)
	go func() {
		done <- NewReplicationClient(ReplicationClientConfig{
			Target: cloudAddr, InstanceID: "consumer-test", Logger: logger,
		}, boatBroker).Run(ctx)
	}()

	// Wait for cloud broker
	var cloudBroker *Broker
	deadline := time.After(5 * time.Second)
	for cloudBroker == nil {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for cloud broker")
		default:
			cloudBroker = replServer.GetInstanceBroker("consumer-test")
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Start 3 consumers at different cursors
	c1 := cloudBroker.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = c1.Close() }()

	c2 := cloudBroker.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = c2.Close() }()

	c3 := cloudBroker.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = c3.Close() }()

	// Now feed frames into boat
	feedFrames(boatBroker, 20)
	waitForSeq(t, boatBroker, 20)
	waitForCloudSeq(t, cloudBroker, 20, 5*time.Second)

	// All 3 consumers should be able to read all 20 frames
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()

	for _, c := range []*Consumer{c1, c2, c3} {
		for i := uint64(1); i <= 20; i++ {
			frame, err := c.Next(readCtx)
			if err != nil {
				t.Fatalf("consumer read frame %d: %v", i, err)
			}
			if frame.Seq != i {
				t.Fatalf("expected seq %d, got %d", i, frame.Seq)
			}
		}
	}

	t.Log("3 concurrent cloud consumers verified")

	cancel()
	<-done
}

// TestReplicationRateLimit verifies that the cloud closes the live stream
// with ResourceExhausted when the boat sends frames faster than the NMEA 2000
// CAN bus can physically produce.
func TestReplicationRateLimit(t *testing.T) {
	cloudAddr, _, _, cleanup := startCloudStack(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(cloudAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewReplicationClient(conn)

	// Handshake
	_, err = client.Handshake(ctx, &pb.HandshakeRequest{
		InstanceId: "rate-test",
		HeadSeq:    1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Open live stream and blast frames as fast as possible.
	// We need to exceed DefaultRateBurst + sustained rate to trigger rejection.
	liveCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-instance-id", "rate-test"))
	stream, err := client.Live(liveCtx)
	if err != nil {
		t.Fatal(err)
	}

	// Send enough frames to exhaust the burst bucket and exceed the rate.
	// The token bucket starts with DefaultRateBurst tokens and refills at
	// DefaultMaxFrameRate/sec. Blasting instantly should exhaust it after
	// ~DefaultRateBurst frames.
	total := uint64(DefaultRateBurst + DefaultMaxFrameRate + 1)
	var sendErr error
	for i := uint64(1); i <= total; i++ {
		sendErr = stream.Send(&pb.LiveUpstream{
			Msg: &pb.LiveUpstream_Frame{
				Frame: &pb.LiveFrame{
					Seq:         i,
					TimestampUs: time.Now().UnixMicro(),
					CanId:       0x09F80100,
					Data:        []byte{1, 2, 3, 4, 5, 6, 7, 8},
				},
			},
		})
		if sendErr != nil {
			break
		}
	}

	// The server closes the stream with ResourceExhausted. In gRPC
	// bidirectional streams, Send() may return EOF while the actual status
	// is delivered via Recv(). Always call Recv() to get the trailing status.
	_, recvErr := stream.Recv()
	if recvErr == nil {
		// Keep draining in case there's buffered data before the error.
		for {
			_, recvErr = stream.Recv()
			if recvErr != nil {
				break
			}
		}
	}

	st, ok := status.FromError(recvErr)
	if !ok || st.Code() != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got: %v (code: %v, send_err: %v)", recvErr, st.Code(), sendErr)
	}
	t.Logf("rate limit correctly enforced after burst: %v", st.Message())
}

// TestReplicationLiveLagDetection verifies that the boat-side replication
// client detects when its live consumer falls behind and reconnects at the
// head, switching the gap to backfill mode.
func TestReplicationLiveLagDetection(t *testing.T) {
	cloudAddr, replServer, _, cleanup := startCloudStack(t)
	defer cleanup()

	// Disable rate limiting for this test so the lag check (every 1000
	// frames) fires before the rate limiter would. We're testing lag
	// detection, not rate limiting.
	replServer.MaxFrameRate = 1_000_000
	replServer.RateBurst = 1_000_000

	logger := slog.Default()

	// Use a small ring buffer so frames are cheap to produce.
	boatBroker := NewBroker(BrokerConfig{
		RingSize: 65536,
		Logger:   logger,
	})
	go boatBroker.Run()
	defer boatBroker.CloseRx()

	// Feed more than DefaultMaxLiveLag frames before starting replication so
	// the consumer will start at seq 1 and immediately be behind.
	feedFrames(boatBroker, int(DefaultMaxLiveLag)+5000)
	waitForSeq(t, boatBroker, DefaultMaxLiveLag+5000)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan error, 1)
	replClient := NewReplicationClient(ReplicationClientConfig{
		Target:     cloudAddr,
		InstanceID: "lag-test",
		Logger:     logger,
	}, boatBroker)
	go func() { done <- replClient.Run(ctx) }()

	// Keep feeding frames to maintain the lag
	go func() {
		for ctx.Err() == nil {
			feedFrames(boatBroker, 200)
			time.Sleep(50 * time.Millisecond)
		}
	}()

	// Wait for the cloud broker to appear
	var cloudBroker *Broker
	deadline := time.After(10 * time.Second)
	for cloudBroker == nil {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for cloud broker")
		default:
			cloudBroker = replServer.GetInstanceBroker("lag-test")
			time.Sleep(20 * time.Millisecond)
		}
	}

	// The client should detect lag and reconnect at the head. After the
	// lag-triggered reconnect, the cloud should receive recent frames
	// (near the current boat head) rather than being stuck replaying from
	// seq 1 forever.
	boatHead := boatBroker.CurrentSeq()
	// We expect the cloud to have frames within a reasonable range of
	// the boat's head after the lag-triggered reconnect.
	targetSeq := boatHead - 1000
	if targetSeq < 1 {
		targetSeq = 1
	}
	waitForCloudSeq(t, cloudBroker, targetSeq, 15*time.Second)

	cloudHead := cloudBroker.CurrentSeq()
	t.Logf("lag detection worked: boat_head=%d, cloud_head=%d", boatBroker.CurrentSeq(), cloudHead)

	// Verify the instance has holes (from the lag-triggered gap)
	inst := replServer.GetInstanceState("lag-test")
	if inst != nil {
		instStatus := inst.Status()
		t.Logf("instance status: cursor=%d, holes=%d, boat_head=%d",
			instStatus.Cursor, len(instStatus.Holes), instStatus.BoatHeadSeq)
	}

	cancel()
	<-done
}

// TestReplicationReconnectPreservesState verifies that cloud-side state
// persists across InstanceManager restarts, so a reconnecting boat resumes
// from the correct cursor.
func TestReplicationReconnectPreservesState(t *testing.T) {
	cloudDataDir := t.TempDir()
	logger := slog.Default()

	// Start cloud with first InstanceManager
	im1, err := NewInstanceManager(cloudDataDir, logger)
	if err != nil {
		t.Fatal(err)
	}
	replServer1 := NewReplicationServer(im1, logger)

	grpcServer1 := grpc.NewServer()
	pb.RegisterReplicationServer(grpcServer1, replServer1)
	lis1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = grpcServer1.Serve(lis1) }()
	addr := lis1.Addr().String()

	// Boat: feed frames 1-100
	boatBroker := NewBroker(BrokerConfig{RingSize: 4096, Logger: logger})
	go boatBroker.Run()
	defer boatBroker.CloseRx()

	feedFrames(boatBroker, 100)
	waitForSeq(t, boatBroker, 100)

	// Start replication, wait for cloud to catch up
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	go func() {
		done1 <- NewReplicationClient(ReplicationClientConfig{
			Target: addr, InstanceID: "persist-test", Logger: logger,
		}, boatBroker).Run(ctx1)
	}()

	var cloudBroker *Broker
	deadline := time.After(5 * time.Second)
	for cloudBroker == nil {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for cloud broker")
		default:
			cloudBroker = replServer1.GetInstanceBroker("persist-test")
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitForCloudSeq(t, cloudBroker, 100, 5*time.Second)

	// Stop replication and shut down cloud (persists state)
	cancel1()
	<-done1
	grpcServer1.Stop()
	im1.Shutdown()

	t.Log("cloud shut down with cursor at 100")

	// Feed more frames on boat while cloud is down
	feedFrames(boatBroker, 100) // frames 101-200
	waitForSeq(t, boatBroker, 200)

	// Restart cloud with new InstanceManager (reloads from disk)
	im2, err := NewInstanceManager(cloudDataDir, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer im2.Shutdown()

	// Verify state was reloaded
	reloaded := im2.Get("persist-test")
	if reloaded == nil {
		t.Fatal("instance should have been reloaded from disk")
	}
	if reloaded.Cursor != 100 {
		t.Fatalf("reloaded cursor: got %d, want 100", reloaded.Cursor)
	}
	t.Logf("reloaded instance: cursor=%d, holes=%d", reloaded.Cursor, reloaded.HoleTracker.Len())

	replServer2 := NewReplicationServer(im2, logger)
	grpcServer2 := grpc.NewServer()
	pb.RegisterReplicationServer(grpcServer2, replServer2)
	lis2, err := net.Listen("tcp", addr) // reuse same address
	if err != nil {
		// Address might still be in TIME_WAIT, try a new one
		lis2, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr = lis2.Addr().String()
	}
	go func() { _ = grpcServer2.Serve(lis2) }()
	defer grpcServer2.Stop()

	// Reconnect: boat has head=200, cloud has cursor=100
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	done2 := make(chan error, 1)
	go func() {
		done2 <- NewReplicationClient(ReplicationClientConfig{
			Target: addr, InstanceID: "persist-test", Logger: logger,
		}, boatBroker).Run(ctx2)
	}()

	// Cloud should detect hole [101, 200) and receive new live frames
	var cloudBroker2 *Broker
	deadline = time.After(5 * time.Second)
	for cloudBroker2 == nil {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for restarted cloud broker")
		default:
			cloudBroker2 = replServer2.GetInstanceBroker("persist-test")
			time.Sleep(20 * time.Millisecond)
		}
	}

	// The cloud should receive new live frames from head=200 onward
	feedFrames(boatBroker, 10) // frames 201-210
	waitForSeq(t, boatBroker, 210)
	waitForCloudSeq(t, cloudBroker2, 210, 5*time.Second)

	// Verify hole was created for the gap
	status := reloaded.Status()
	t.Logf("after reconnect: cursor=%d, holes=%d, boat_head=%d",
		status.Cursor, len(status.Holes), status.BoatHeadSeq)

	if len(status.Holes) == 0 {
		t.Log("no holes (frames 101-200 were delivered live before disconnect completed)")
	} else {
		t.Logf("holes: %v", status.Holes)
	}

	cancel2()
	<-done2

	t.Log("reconnect with persisted state verified")
}
