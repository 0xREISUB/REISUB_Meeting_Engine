package controllers

import (
	"encoding/json"
	"log"
	"sync"
)

// What changed in a meeting, as told to the devices watching it.
const (
	// roomChat: a new chat message; read the messages again.
	roomChat = "chat"
	// roomState: the screen share, waiting room or someone's rights
	// changed; read /state (or one's waiting request) again.
	roomState = "state"
)

// roomWatchers knows the open connections watching each meeting. Events
// carry no data, only what to read again; reading it checks who may.
type roomWatchers struct {
	mu    sync.Mutex
	rooms map[string]map[*realtimeClient]struct{}
}

var watchers = &roomWatchers{rooms: map[string]map[*realtimeClient]struct{}{}}

func (w *roomWatchers) add(roomID string, c *realtimeClient) {
	w.mu.Lock()
	defer w.mu.Unlock()
	clients := w.rooms[roomID]
	if clients == nil {
		clients = map[*realtimeClient]struct{}{}
		w.rooms[roomID] = clients
	}
	clients[c] = struct{}{}
}

func (w *roomWatchers) remove(roomID string, c *realtimeClient) {
	w.mu.Lock()
	clients := w.rooms[roomID]
	delete(clients, c)
	if len(clients) == 0 {
		delete(w.rooms, roomID)
	}
	w.mu.Unlock()
	c.close()
}

// count answers how many connections watch the room.
func (w *roomWatchers) count(roomID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.rooms[roomID])
}

// roomChanged tells everyone watching the room what to read again. A
// connection too slow to take it is let go; it reconnects and catches up.
func roomChanged(roomID, what string) {
	data, err := json.Marshal(realtimeEvent{Type: "room", Room: roomID, What: what})
	if err != nil {
		log.Printf("warning: could not encode room event: %v", err)
		return
	}
	watchers.mu.Lock()
	clients := make([]*realtimeClient, 0, len(watchers.rooms[roomID]))
	for c := range watchers.rooms[roomID] {
		clients = append(clients, c)
	}
	watchers.mu.Unlock()
	for _, c := range clients {
		select {
		case c.send <- data:
		default:
			watchers.remove(roomID, c)
		}
	}
}
