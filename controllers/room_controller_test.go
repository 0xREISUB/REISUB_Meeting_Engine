package controllers

import (
	"testing"
)

func TestNewRoomIDIsWellFormedAndUnpredictable(t *testing.T) {
	useTestDB(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id, err := newRoomID()
		if err != nil {
			t.Fatal(err)
		}
		if !roomIDPattern.MatchString(id) {
			t.Fatalf("room ID %q does not match NNN-NNN-NNN", id)
		}
		seen[id] = true
	}
	// Back-to-back time-derived codes used to collide or differ only slightly.
	if len(seen) < 49 {
		t.Fatalf("only %d distinct codes out of 50", len(seen))
	}
}
