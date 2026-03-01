package lplex

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/sixfathoms/lplex/journal"
)

// ErrFallenBehind is returned by Consumer.Next when the consumer's cursor
// has fallen behind both the ring buffer and available journal files.
var ErrFallenBehind = errors.New("consumer fallen behind: data no longer available")

// ConsumerConfig configures a new Consumer.
type ConsumerConfig struct {
	Cursor uint64       // starting position (next seq to read)
	Filter *EventFilter // nil = all frames
}

// Consumer is a pull-based reader that iterates frames at its own pace.
// It reads from a tiered log: journal files on disk (oldest), ring buffer
// in memory (recent), and live notification (current head, blocking wait).
type Consumer struct {
	broker *Broker
	cursor uint64          // next seq to read
	filter *resolvedFilter // pre-resolved for efficiency
	notify chan struct{}    // buffered(1), broker signals on new frame
	done   chan struct{}    // closed on Close()
	once   sync.Once

	// journal fallback (lazy-init when cursor < ring tail)
	jFiles  []string        // sorted .lpj paths (nil = not discovered)
	jIdx    int             // current file index in jFiles
	jReader *journal.Reader // current journal reader
	jFile   *os.File        // current open file handle
}

// Frame is a single CAN frame returned by Consumer.Next.
type Frame struct {
	Seq       uint64
	Timestamp time.Time
	Header    CANHeader
	Data      []byte // raw CAN payload
	json      []byte // cached pre-serialized JSON
}

// JSON returns the pre-serialized JSON for this frame (SSE format).
// If not cached (e.g. from journal replay), it serializes on demand.
func (f *Frame) JSON() ([]byte, error) {
	if f.json != nil {
		return f.json, nil
	}
	msg := frameJSON{
		Seq:  f.Seq,
		Ts:   f.Timestamp.UTC().Format(time.RFC3339Nano),
		Prio: f.Header.Priority,
		PGN:  f.Header.PGN,
		Src:  f.Header.Source,
		Dst:  f.Header.Destination,
		Data: hex.EncodeToString(f.Data),
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	f.json = b
	return b, nil
}

// Next returns the next matching frame, blocking until one is available.
// Returns ErrFallenBehind if the consumer's cursor has fallen behind all
// available data. Returns ctx.Err() if the context is cancelled.
func (c *Consumer) Next(ctx context.Context) (*Frame, error) {
	for {
		select {
		case <-c.done:
			return nil, errors.New("consumer closed")
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		tail, head := c.broker.ringRange()

		// If cursor is behind ring tail, try journal fallback
		if c.cursor < tail {
			frame, err := c.readFromJournal(ctx)
			if err != nil {
				return nil, err
			}
			if frame != nil {
				return frame, nil
			}
			// frame == nil means we caught up to ring, loop to read from ring
			continue
		}

		// Read from ring buffer if cursor is in range
		if c.cursor < head {
			entry, ok := c.broker.readEntry(c.cursor)
			if !ok {
				// Entry was overwritten between ringRange and readEntry
				return nil, ErrFallenBehind
			}
			c.cursor++

			if !c.filter.matches(entry.Header) {
				continue
			}

			return &Frame{
				Seq:       entry.Seq,
				Timestamp: entry.Timestamp,
				Header:    entry.Header,
				Data:      entry.RawData,
				json:      entry.JSON,
			}, nil
		}

		// cursor >= head: wait for notification
		select {
		case <-c.done:
			return nil, errors.New("consumer closed")
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.notify:
			// New frame available, loop back to read it
		}
	}
}

// Cursor returns the consumer's current position (next seq to read).
func (c *Consumer) Cursor() uint64 {
	return c.cursor
}

// Close stops the consumer and removes it from the broker.
// Safe to call multiple times.
func (c *Consumer) Close() error {
	c.once.Do(func() {
		close(c.done)
		c.broker.removeConsumer(c)
		c.closeJournal()
	})
	return nil
}

// readFromJournal reads frames from journal files until the consumer catches
// up to the ring buffer tail. Returns a matching frame, or (nil, nil) when
// the cursor has reached the ring and the caller should switch to ring reads.
func (c *Consumer) readFromJournal(ctx context.Context) (*Frame, error) {
	if c.broker.journalDir == "" {
		return nil, ErrFallenBehind
	}

	// Lazy-init: discover journal files
	if c.jFiles == nil {
		files, err := filepath.Glob(filepath.Join(c.broker.journalDir, "*.lpj"))
		if err != nil {
			return nil, ErrFallenBehind
		}
		sort.Strings(files) // chronological order (timestamp in filename)
		if len(files) == 0 {
			return nil, ErrFallenBehind
		}
		c.jFiles = files
		c.jIdx = 0

		// Find the right starting file via binary search on first block's BaseSeq.
		startIdx, err := c.findJournalFile(c.cursor)
		if err != nil {
			c.jFiles = nil
			return nil, ErrFallenBehind
		}
		c.jIdx = startIdx

		if err := c.openJournalFile(c.jIdx); err != nil {
			c.jFiles = nil
			return nil, ErrFallenBehind
		}

		// Seek to the block containing our cursor
		if err := c.jReader.SeekToSeq(c.cursor); err != nil {
			c.closeJournal()
			c.jFiles = nil
			return nil, ErrFallenBehind
		}
	}

	for {
		select {
		case <-c.done:
			return nil, errors.New("consumer closed")
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Check if we've caught up to the ring
		tail, _ := c.broker.ringRange()
		if c.cursor >= tail {
			c.closeJournal()
			return nil, nil // signal: switch to ring
		}

		if c.jReader != nil && c.jReader.Next() {
			seq := c.jReader.FrameSeq()
			if seq == 0 {
				// v1 file, can't determine seq; skip
				c.closeCurrentFile()
				continue
			}
			if seq < c.cursor {
				continue // skip frames before our cursor
			}
			if seq > c.cursor {
				// Gap in sequence. Could be a file boundary issue or data loss.
				// Re-check ring in case we've caught up during journal read.
				tail, _ = c.broker.ringRange()
				if c.cursor >= tail {
					c.closeJournal()
					return nil, nil
				}
				return nil, ErrFallenBehind
			}

			entry := c.jReader.Frame()
			header := CANHeader(entry.Header)
			c.cursor++

			if !c.filter.matches(header) {
				continue
			}

			return &Frame{
				Seq:       seq,
				Timestamp: entry.Timestamp,
				Header:    header,
				Data:      entry.Data,
			}, nil
		}

		// Check for reader error
		if c.jReader != nil && c.jReader.Err() != nil {
			c.closeCurrentFile()
			continue
		}

		// Current file exhausted, try the next one
		c.jIdx++
		if c.jIdx >= len(c.jFiles) {
			// No more journal files. Check if ring covers us now.
			tail, _ = c.broker.ringRange()
			if c.cursor >= tail {
				c.closeJournal()
				return nil, nil
			}
			return nil, ErrFallenBehind
		}

		if err := c.openJournalFile(c.jIdx); err != nil {
			c.closeJournal()
			return nil, ErrFallenBehind
		}
	}
}

// readFirstSeq opens a journal file and reads the first frame's sequence number.
// Returns (0, false) if the file is not v2, empty, or can't be read.
func readFirstSeq(path string) (uint64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()

	r, err := journal.NewReader(f)
	if err != nil || r.Version() != journal.Version2 || r.BlockCount() == 0 {
		return 0, false
	}
	if err := r.SeekBlock(0); err != nil {
		return 0, false
	}
	if !r.Next() {
		return 0, false
	}
	seq := r.FrameSeq()
	if seq == 0 {
		return 0, false
	}
	return seq, true
}

// findJournalFile finds the journal file index that should contain the given
// sequence number. Opens each file's first v2 block header to read BaseSeq.
func (c *Consumer) findJournalFile(seq uint64) (int, error) {
	// Read the first block's BaseSeq from each file to find the right one.
	// We want the last file whose first block BaseSeq <= seq.
	type fileSeq struct {
		idx     int
		baseSeq uint64
	}
	var candidates []fileSeq

	for i, path := range c.jFiles {
		firstSeq, ok := readFirstSeq(path)
		if !ok {
			continue
		}

		if firstSeq > 0 {
			candidates = append(candidates, fileSeq{idx: i, baseSeq: firstSeq})
		}
	}

	if len(candidates) == 0 {
		return 0, ErrFallenBehind
	}

	// Find the last file whose first frame seq <= our target seq
	result := candidates[0].idx
	for _, c := range candidates {
		if c.baseSeq <= seq {
			result = c.idx
		} else {
			break
		}
	}
	return result, nil
}

// openJournalFile opens a journal file at the given index.
func (c *Consumer) openJournalFile(idx int) error {
	c.closeCurrentFile()

	f, err := os.Open(c.jFiles[idx])
	if err != nil {
		return err
	}
	r, err := journal.NewReader(f)
	if err != nil {
		_ = f.Close()
		return err
	}
	if r.Version() != journal.Version2 {
		_ = f.Close()
		return errors.New("journal v1 not supported for seq-based reading")
	}

	c.jFile = f
	c.jReader = r
	return nil
}

// closeCurrentFile closes the current journal file and reader.
func (c *Consumer) closeCurrentFile() {
	c.jReader = nil
	if c.jFile != nil {
		_ = c.jFile.Close()
		c.jFile = nil
	}
}

// closeJournal closes all journal state.
func (c *Consumer) closeJournal() {
	c.closeCurrentFile()
	c.jFiles = nil
}

// NewConsumer creates a pull-based consumer starting at the given cursor.
// The consumer is registered with the broker for live notifications.
func (b *Broker) NewConsumer(cfg ConsumerConfig) *Consumer {
	filter := cfg.Filter.resolve(b.devices)

	c := &Consumer{
		broker: b,
		cursor: cfg.Cursor,
		filter: filter,
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}

	b.consumerMu.Lock()
	b.consumers[c] = struct{}{}
	b.consumerMu.Unlock()

	return c
}
