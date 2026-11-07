package controllers

import (
	"sync"
	"time"

	"reisub-backend/models"
)

// shareCheckInterval bounds how often GetMeetingState asks LiveKit whether
// the screen-share holder is still sharing. Every participant polls /state
// every 2 s; remembering a successful check for one poll interval caps
// LiveKit traffic at about one call per sharing room per interval, however
// many participants poll. A holder that disappears is therefore reported
// at most one interval late. Variable so tests can change it.
var shareCheckInterval = 2 * time.Second

type shareCheck struct {
	identity string
	until    time.Time
}

// shareChecks remembers, per room ID, the holder that LiveKit last confirmed
// and until when that confirmation is trusted.
var shareChecks = struct {
	sync.Mutex
	verified map[string]shareCheck
}{verified: make(map[string]shareCheck)}

func shareRecentlyVerified(room models.Room) bool {
	shareChecks.Lock()
	defer shareChecks.Unlock()
	check, ok := shareChecks.verified[room.RoomID]
	return ok && check.identity == room.ScreenShareIdentity && time.Now().Before(check.until)
}

func markShareVerified(room models.Room) {
	if shareCheckInterval <= 0 {
		return
	}
	shareChecks.Lock()
	defer shareChecks.Unlock()
	shareChecks.verified[room.RoomID] = shareCheck{identity: room.ScreenShareIdentity, until: time.Now().Add(shareCheckInterval)}
}

// forgetShareCheck drops the cached confirmation; call it whenever the
// room's slot changes hands or the room closes.
func forgetShareCheck(roomID string) {
	shareChecks.Lock()
	defer shareChecks.Unlock()
	delete(shareChecks.verified, roomID)
}
