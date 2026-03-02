package lplex

import (
	"testing"
)

func TestHoleTracker_AddAndHoles(t *testing.T) {
	h := NewHoleTracker()

	if got := h.Holes(); got != nil {
		t.Fatalf("new tracker should have no holes, got %v", got)
	}

	h.Add(10, 20)
	assertHoles(t, h, []SeqRange{{10, 20}})

	// Non-overlapping hole after
	h.Add(30, 40)
	assertHoles(t, h, []SeqRange{{10, 20}, {30, 40}})

	// Non-overlapping hole before
	h.Add(1, 5)
	assertHoles(t, h, []SeqRange{{1, 5}, {10, 20}, {30, 40}})
}

func TestHoleTracker_AddMergesOverlapping(t *testing.T) {
	h := NewHoleTracker()
	h.Add(10, 20)
	h.Add(30, 40)

	// Overlapping: bridges the two holes
	h.Add(15, 35)
	assertHoles(t, h, []SeqRange{{10, 40}})
}

func TestHoleTracker_AddMergesAdjacent(t *testing.T) {
	h := NewHoleTracker()
	h.Add(10, 20)
	h.Add(20, 30)
	assertHoles(t, h, []SeqRange{{10, 30}})
}

func TestHoleTracker_AddNoop(t *testing.T) {
	h := NewHoleTracker()
	h.Add(10, 10) // empty range
	h.Add(20, 15) // start > end
	if got := h.Holes(); got != nil {
		t.Fatalf("no-op adds should leave no holes, got %v", got)
	}
}

func TestHoleTracker_AddSubsumed(t *testing.T) {
	h := NewHoleTracker()
	h.Add(10, 40)
	h.Add(15, 25) // fully inside existing
	assertHoles(t, h, []SeqRange{{10, 40}})
}

func TestHoleTracker_FillEmpty(t *testing.T) {
	h := NewHoleTracker()
	if h.Fill(0, 100) {
		t.Fatal("filling empty tracker should return false")
	}
}

func TestHoleTracker_FillNoOverlap(t *testing.T) {
	h := NewHoleTracker()
	h.Add(10, 20)
	if h.Fill(25, 30) {
		t.Fatal("filling non-overlapping range should return false")
	}
	assertHoles(t, h, []SeqRange{{10, 20}})
}

func TestHoleTracker_FillEntireHole(t *testing.T) {
	h := NewHoleTracker()
	h.Add(10, 20)
	h.Add(30, 40)

	if !h.Fill(10, 20) {
		t.Fatal("should report affected")
	}
	assertHoles(t, h, []SeqRange{{30, 40}})
}

func TestHoleTracker_FillSplitsHole(t *testing.T) {
	h := NewHoleTracker()
	h.Add(10, 40)

	if !h.Fill(20, 25) {
		t.Fatal("should report affected")
	}
	assertHoles(t, h, []SeqRange{{10, 20}, {25, 40}})
}

func TestHoleTracker_FillLeftTrim(t *testing.T) {
	h := NewHoleTracker()
	h.Add(10, 30)

	h.Fill(10, 20)
	assertHoles(t, h, []SeqRange{{20, 30}})
}

func TestHoleTracker_FillRightTrim(t *testing.T) {
	h := NewHoleTracker()
	h.Add(10, 30)

	h.Fill(20, 30)
	assertHoles(t, h, []SeqRange{{10, 20}})
}

func TestHoleTracker_FillSpansMultipleHoles(t *testing.T) {
	h := NewHoleTracker()
	h.Add(10, 20)
	h.Add(30, 40)
	h.Add(50, 60)

	// Fill covers middle hole entirely and trims the others
	h.Fill(15, 55)
	assertHoles(t, h, []SeqRange{{10, 15}, {55, 60}})
}

func TestHoleTracker_FillInvalidRange(t *testing.T) {
	h := NewHoleTracker()
	h.Add(10, 20)

	if h.Fill(15, 15) {
		t.Fatal("empty fill range should return false")
	}
	if h.Fill(20, 10) {
		t.Fatal("inverted fill range should return false")
	}
	assertHoles(t, h, []SeqRange{{10, 20}})
}

func TestHoleTracker_ContinuousThrough_NoHoles(t *testing.T) {
	h := NewHoleTracker()
	if got := h.ContinuousThrough(100); got != ^uint64(0) {
		t.Fatalf("no holes: expected max uint64, got %d", got)
	}
}

func TestHoleTracker_ContinuousThrough_HoleAfterCursor(t *testing.T) {
	h := NewHoleTracker()
	h.Add(200, 300)

	// Continuous from cursor=100 up to 199 (just before hole starts)
	if got := h.ContinuousThrough(100); got != 199 {
		t.Fatalf("expected 199, got %d", got)
	}
}

func TestHoleTracker_ContinuousThrough_HoleAtCursor(t *testing.T) {
	h := NewHoleTracker()
	h.Add(100, 200)

	// Hole starts at cursor, so continuous only through cursor-1
	if got := h.ContinuousThrough(100); got != 99 {
		t.Fatalf("expected 99, got %d", got)
	}
}

func TestHoleTracker_ContinuousThrough_AllHolesBefore(t *testing.T) {
	h := NewHoleTracker()
	h.Add(10, 20)
	h.Add(30, 40)

	// Cursor is past all holes
	if got := h.ContinuousThrough(50); got != ^uint64(0) {
		t.Fatalf("expected max uint64, got %d", got)
	}
}

func TestHoleTracker_TotalMissing(t *testing.T) {
	h := NewHoleTracker()
	if got := h.TotalMissing(); got != 0 {
		t.Fatalf("empty: expected 0, got %d", got)
	}

	h.Add(10, 20)
	h.Add(30, 50)
	if got := h.TotalMissing(); got != 30 {
		t.Fatalf("expected 30, got %d", got)
	}
}

func TestHoleTracker_RealisticReconnect(t *testing.T) {
	// Simulate: boat connected, streamed seqs 1-5000, disconnected,
	// reconnects with head=8000. Cloud had continuous through 5000,
	// last live frame was 7000. Hole is (5001, 7001).
	h := NewHoleTracker()
	h.Add(5001, 7001)

	// Continuous through: should be 5000 (just before hole)
	if got := h.ContinuousThrough(1); got != 5000 {
		t.Fatalf("expected 5000, got %d", got)
	}

	// Backfill first chunk: seqs 6000-7000
	h.Fill(6001, 7001)
	assertHoles(t, h, []SeqRange{{5001, 6001}})

	// Backfill remaining
	h.Fill(5001, 6001)
	assertHoles(t, h, nil)

	if got := h.ContinuousThrough(1); got != ^uint64(0) {
		t.Fatalf("all filled: expected max, got %d", got)
	}
}

func assertHoles(t *testing.T, h *HoleTracker, want []SeqRange) {
	t.Helper()
	got := h.Holes()
	if len(got) != len(want) {
		t.Fatalf("holes: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("hole[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}
