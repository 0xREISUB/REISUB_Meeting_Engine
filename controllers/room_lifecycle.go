package controllers

import "sync"

// Serialize creation/recreation and closure of the same room. Entries are
// removed when unused so expired room codes do not accumulate in memory.
var roomLifecycle = struct {
	sync.Mutex
	entries map[string]*roomLifecycleEntry
}{entries: make(map[string]*roomLifecycleEntry)}

type roomLifecycleEntry struct {
	mutex      sync.Mutex
	references int
}

func lockRoomLifecycle(roomID string) func() {
	roomLifecycle.Lock()
	entry := roomLifecycle.entries[roomID]
	if entry == nil {
		entry = &roomLifecycleEntry{}
		roomLifecycle.entries[roomID] = entry
	}
	entry.references++
	roomLifecycle.Unlock()
	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		roomLifecycle.Lock()
		entry.references--
		if entry.references == 0 {
			delete(roomLifecycle.entries, roomID)
		}
		roomLifecycle.Unlock()
	}
}
