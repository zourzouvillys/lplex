package lplex

import "sort"

// SeqRange represents a half-open interval [Start, End) of sequence numbers.
type SeqRange struct {
	Start uint64 // inclusive
	End   uint64 // exclusive
}

// HoleTracker tracks gaps (holes) in a sequence number space. Holes are
// non-overlapping, sorted by Start, and represent ranges of missing data.
//
// Typical case: 0-3 holes. All operations are linear on the slice, which
// is perfectly fine for the expected cardinality.
type HoleTracker struct {
	holes []SeqRange
}

// NewHoleTracker creates an empty hole tracker.
func NewHoleTracker() *HoleTracker {
	return &HoleTracker{}
}

// Add inserts a new hole [start, end). Merges with any overlapping or
// adjacent holes. No-op if start >= end.
func (h *HoleTracker) Add(start, end uint64) {
	if start >= end {
		return
	}

	// Find insertion point and merge with any overlapping/adjacent holes.
	merged := SeqRange{Start: start, End: end}
	var result []SeqRange

	inserted := false
	for _, hole := range h.holes {
		if hole.End < merged.Start {
			// hole is entirely before merged
			result = append(result, hole)
		} else if hole.Start > merged.End {
			// hole is entirely after merged
			if !inserted {
				result = append(result, merged)
				inserted = true
			}
			result = append(result, hole)
		} else {
			// overlapping or adjacent, absorb into merged
			merged.Start = min(merged.Start, hole.Start)
			merged.End = max(merged.End, hole.End)
		}
	}
	if !inserted {
		result = append(result, merged)
	}

	h.holes = result
}

// Fill marks [start, end) as received, removing that range from any holes.
// Returns true if any holes were actually affected.
func (h *HoleTracker) Fill(start, end uint64) bool {
	if start >= end || len(h.holes) == 0 {
		return false
	}

	var result []SeqRange
	affected := false

	for _, hole := range h.holes {
		if hole.End <= start || hole.Start >= end {
			// No overlap
			result = append(result, hole)
			continue
		}

		affected = true

		// Left remnant: hole starts before the fill range
		if hole.Start < start {
			result = append(result, SeqRange{Start: hole.Start, End: start})
		}
		// Right remnant: hole extends past the fill range
		if hole.End > end {
			result = append(result, SeqRange{Start: end, End: hole.End})
		}
	}

	h.holes = result
	return affected
}

// Holes returns a copy of current holes, sorted by Start.
func (h *HoleTracker) Holes() []SeqRange {
	if len(h.holes) == 0 {
		return nil
	}
	out := make([]SeqRange, len(h.holes))
	copy(out, h.holes)
	return out
}

// Len returns the number of holes.
func (h *HoleTracker) Len() int {
	return len(h.holes)
}

// TotalMissing returns the total number of missing sequence numbers across
// all holes.
func (h *HoleTracker) TotalMissing() uint64 {
	var total uint64
	for _, hole := range h.holes {
		total += hole.End - hole.Start
	}
	return total
}

// ContinuousThrough returns the highest sequence number with no holes below
// it. Given a base cursor and the hole set, this is the seq just before the
// first hole (or the cursor itself if no holes exist before it).
//
// Example: cursor=100, holes=[(200,300)] -> returns 199 (continuous through 199)
// Example: cursor=100, holes=[(100,200)] -> returns 99 (hole starts at cursor)
// Example: cursor=100, no holes -> returns max uint64 (no bound from holes)
func (h *HoleTracker) ContinuousThrough(cursor uint64) uint64 {
	if len(h.holes) == 0 {
		// No holes, everything from 0 to infinity is continuous.
		return ^uint64(0)
	}

	// Find the first hole at or after cursor
	idx := sort.Search(len(h.holes), func(i int) bool {
		return h.holes[i].End > cursor
	})

	if idx >= len(h.holes) {
		// All holes end before cursor
		return ^uint64(0)
	}

	hole := h.holes[idx]
	if hole.Start <= cursor {
		// Hole covers the cursor position, continuous up to cursor-1
		if cursor == 0 {
			return 0
		}
		return cursor - 1
	}

	// Hole starts after cursor, continuous through hole.Start-1
	return hole.Start - 1
}

// SyncState captures the replication state for an instance, used for
// persistence and handshake responses.
type SyncState struct {
	Cursor         uint64     // continuous data through this seq
	Holes          []SeqRange // sorted gaps
	BoatHeadSeq    uint64     // last reported by boat
	BoatJournalBytes uint64
}
