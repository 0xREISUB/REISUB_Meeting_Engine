package controllers

import (
	"encoding/json"
	"log"
	"sync"
	"time"
)

// realtimeQueue is how many events wait for one slow device before it is
// let go; it reconnects and catches up.
const realtimeQueue = 64

// realtimeClient is one device's open connection.
type realtimeClient struct {
	userID uint
	send   chan []byte
	// gone is closed once the hub has let the client go.
	gone chan struct{}
	once sync.Once
}

func (c *realtimeClient) close() {
	c.once.Do(func() { close(c.gone) })
}

// realtimeHub knows every open connection, by user, and sends events to
// them. One server keeps them in memory.
type realtimeHub struct {
	mu      sync.Mutex
	clients map[uint]map[*realtimeClient]struct{}
	// left is when users' last device went away, for a little while.
	left map[uint]time.Time
	// onPresence is told when a user's first device connects (online) or
	// their last one leaves, and when.
	onPresence func(userID uint, online bool, at time.Time)
}

func newRealtimeHub() *realtimeHub {
	return &realtimeHub{
		clients: map[uint]map[*realtimeClient]struct{}{},
		left:    map[uint]time.Time{},
	}
}

// leftAt answers when the user's last device went away, if lately.
func (h *realtimeHub) leftAt(userID uint) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	at, ok := h.left[userID]
	return at, ok
}

// hub is the server's realtime hub.
var hub = newRealtimeHub()

func (h *realtimeHub) add(c *realtimeClient) {
	h.mu.Lock()
	devices := h.clients[c.userID]
	if devices == nil {
		devices = map[*realtimeClient]struct{}{}
		h.clients[c.userID] = devices
	}
	devices[c] = struct{}{}
	first := len(devices) == 1
	delete(h.left, c.userID)
	onPresence := h.onPresence
	h.mu.Unlock()
	if first && onPresence != nil {
		onPresence(c.userID, true, time.Now())
	}
}

func (h *realtimeHub) remove(c *realtimeClient) {
	h.mu.Lock()
	devices := h.clients[c.userID]
	_, known := devices[c]
	delete(devices, c)
	last := known && len(devices) == 0
	now := time.Now()
	if last {
		delete(h.clients, c.userID)
		// Past the timeout their last request no longer counts anyway.
		for id, at := range h.left {
			if now.Sub(at) > presenceTimeout {
				delete(h.left, id)
			}
		}
		h.left[c.userID] = now
	}
	onPresence := h.onPresence
	h.mu.Unlock()
	c.close()
	if last && onPresence != nil {
		onPresence(c.userID, false, now)
	}
}

// online reports whether the user has a device connected.
func (h *realtimeHub) online(userID uint) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients[userID]) > 0
}

// send hands an event to every device of the user. A device whose queue
// is full is let go rather than holding the others up.
func (h *realtimeHub) send(userID uint, event any) {
	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("warning: could not encode realtime event: %v", err)
		return
	}
	h.mu.Lock()
	devices := make([]*realtimeClient, 0, len(h.clients[userID]))
	for c := range h.clients[userID] {
		devices = append(devices, c)
	}
	h.mu.Unlock()
	for _, c := range devices {
		select {
		case c.send <- data:
		default:
			h.remove(c)
		}
	}
}

// realtimeEvent tells a device something changed.
type realtimeEvent struct {
	// Type is "message" (Message holds it), "social" (read the social
	// overview again) or "room" (see Room and What).
	Type    string               `json:"type"`
	Message *notificationMessage `json:"message,omitempty"`
	// Room and What, for "room" events: which meeting, and what to read
	// again in it (roomChat or roomState).
	Room string `json:"room,omitempty"`
	What string `json:"what,omitempty"`
}

// socialChanged tells the users their social overview changed: a friend
// request, a team, someone coming online and the like.
func socialChanged(userIDs ...uint) {
	seen := map[uint]bool{}
	for _, id := range userIDs {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		hub.send(id, realtimeEvent{Type: "social"})
	}
}
