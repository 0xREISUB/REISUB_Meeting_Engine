package controllers

import (
	"context"
	"log"
	"time"

	"reisub-backend/database"
	"reisub-backend/models"
)

// defaultRoomIdleAfter is how long a room may go unused before it is closed
// (ROOM_IDLE_AFTER).
const defaultRoomIdleAfter = 24 * time.Hour

// roomTouchResolution limits LastUsedAt writes from state polls to one per
// room per hour.
const roomTouchResolution = time.Hour

// markRoomUsed records that someone is using the room. Polls call it every
// two seconds, so it writes at most once per roomTouchResolution unless
// force is set.
func markRoomUsed(room *models.Room, force bool) {
	now := time.Now()
	if !force && room.LastUsedAt != nil && now.Sub(*room.LastUsedAt) < roomTouchResolution {
		return
	}
	if err := database.DB.Model(&models.Room{}).Where("id = ?", room.ID).UpdateColumn("last_used_at", now).Error; err != nil {
		log.Printf("warning: could not record use of room %s: %v", room.RoomID, err)
		return
	}
	room.LastUsedAt = &now
}

// CloseIdleRooms closes active rooms nobody has created, joined or been in
// since cutoff, so their codes stop working. A room that still has someone
// connected in LiveKit is kept and marked used. It returns how many rooms
// were closed.
func CloseIdleRooms(ctx context.Context, cutoff time.Time) (int, error) {
	var idle []models.Room
	if err := database.DB.Where("status = ? AND COALESCE(last_used_at, created_at) < ?", models.RoomStatusActive, cutoff).Find(&idle).Error; err != nil {
		return 0, err
	}
	closed := 0
	for _, candidate := range idle {
		ok, err := closeIfIdle(ctx, candidate.RoomID, cutoff)
		if err != nil {
			log.Printf("warning: could not close idle room %s: %v", candidate.RoomID, err)
			continue
		}
		if ok {
			closed++
		}
	}
	return closed, nil
}

func closeIfIdle(ctx context.Context, roomID string, cutoff time.Time) (bool, error) {
	release := lockRoomLifecycle(roomID)
	defer release()
	// Re-read under the lock: someone may have joined meanwhile.
	var room models.Room
	if err := database.DB.Where("room_id = ? AND status = ? AND COALESCE(last_used_at, created_at) < ?", roomID, models.RoomStatusActive, cutoff).Limit(1).Find(&room).Error; err != nil || room.ID == 0 {
		return false, err
	}
	ctx, svc, err := connectLiveKit(ctx, roomID)
	if err != nil {
		return false, err
	}
	ps, err := connectedParticipants(ctx, svc, roomID)
	if err != nil {
		return false, err
	}
	if len(ps) > 0 {
		markRoomUsed(&room, true)
		return false, nil
	}
	_, err = closeRoom(ctx, svc, &room)
	return err == nil, err
}

// StartRoomCleanup closes idle rooms now and then hourly.
func StartRoomCleanup() {
	idleAfter := envDuration("ROOM_IDLE_AFTER", defaultRoomIdleAfter)
	go func() {
		for {
			closed, err := CloseIdleRooms(context.Background(), time.Now().Add(-idleAfter))
			if err != nil {
				log.Printf("warning: room cleanup failed: %v", err)
			} else if closed > 0 {
				log.Printf("room cleanup: closed %d idle room(s)", closed)
			}
			time.Sleep(time.Hour)
		}
	}()
}
