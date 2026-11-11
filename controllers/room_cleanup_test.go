package controllers

import (
	"testing"
	"time"

	"reisub-backend/database"
	"reisub-backend/models"
)

func setRoomTimes(t *testing.T, lastUsed *time.Time, created time.Time) {
	t.Helper()
	if err := database.DB.Model(&models.Room{}).Where("room_id = ?", "123-456-789").
		Updates(map[string]interface{}{"last_used_at": lastUsed, "created_at": created}).Error; err != nil {
		t.Fatal(err)
	}
}

func loadTestRoom(t *testing.T) models.Room {
	t.Helper()
	var room models.Room
	if err := database.DB.Where("room_id = ?", "123-456-789").First(&room).Error; err != nil {
		t.Fatal(err)
	}
	return room
}

func ago(d time.Duration) *time.Time {
	at := time.Now().Add(-d)
	return &at
}

func TestIdleRoomIsClosedWithItsGrants(t *testing.T) {
	r, f := moderationTestRouter(t)
	addMember(t, f, modID, models.PermissionEndMeeting, time.Now(), false)
	removeRequest(r, "owner-token", 22) // a ban
	database.DB.Create(&models.WaitingRequest{RequestID: "w", RoomID: "123-456-789", UserID: 11, Name: "n", Status: models.WaitingStatusWaiting, LastPolledAt: time.Now()})
	f.participants = nil
	setRoomTimes(t, ago(25*time.Hour), time.Now().Add(-48*time.Hour))

	closed, err := CloseIdleRooms(t.Context(), time.Now().Add(-24*time.Hour))
	if err != nil || closed != 1 {
		t.Fatalf("closed %d (%v)", closed, err)
	}
	if room := loadTestRoom(t); room.Status != models.RoomStatusClosed || f.deletes != 1 {
		t.Fatalf("room not closed: %s, deletes=%d", room.Status, f.deletes)
	}
	var left int64
	for _, table := range []any{&models.RoomBan{}, &models.WaitingRequest{}} {
		database.DB.Model(table).Count(&left)
		if left != 0 {
			t.Fatalf("%T survived the room", table)
		}
	}
	database.DB.Model(&models.RoomRole{}).Where("role = ?", models.RoleParticipant).Count(&left)
	if left != 0 {
		t.Fatal("permissions survived the room")
	}
	if w := joinRequest(r, 11, "x"); w.Code != 404 {
		t.Fatal("closed room can still be joined:", w.Code)
	}
}

func TestRoomsInUseAreKept(t *testing.T) {
	t.Run("someone connected", func(t *testing.T) {
		_, f := moderationTestRouter(t)
		setRoomTimes(t, ago(25*time.Hour), time.Now().Add(-48*time.Hour))
		if closed, err := CloseIdleRooms(t.Context(), time.Now().Add(-24*time.Hour)); err != nil || closed != 0 {
			t.Fatalf("closed %d (%v)", closed, err)
		}
		room := loadTestRoom(t)
		if room.Status != models.RoomStatusActive || f.deletes != 0 || room.LastUsedAt == nil || time.Since(*room.LastUsedAt) > time.Minute {
			t.Fatalf("connected room closed or not marked used: %+v", room)
		}
	})
	t.Run("used recently", func(t *testing.T) {
		_, f := moderationTestRouter(t)
		f.participants = nil
		setRoomTimes(t, ago(23*time.Hour), time.Now().Add(-48*time.Hour))
		if closed, _ := CloseIdleRooms(t.Context(), time.Now().Add(-24*time.Hour)); closed != 0 || f.lists != 0 {
			t.Fatal("recently used room considered idle")
		}
	})
	t.Run("never used since an old creation", func(t *testing.T) {
		_, f := moderationTestRouter(t)
		f.participants = nil
		setRoomTimes(t, nil, time.Now().Add(-25*time.Hour))
		if closed, _ := CloseIdleRooms(t.Context(), time.Now().Add(-24*time.Hour)); closed != 1 {
			t.Fatal("room from before LastUsedAt was not closed")
		}
	})
}

func TestJoiningAndPollingMarkTheRoomUsed(t *testing.T) {
	r, _ := moderationTestRouter(t)
	setRoomTimes(t, ago(48*time.Hour), time.Now().Add(-48*time.Hour))
	if w := joinRequest(r, 22, "x"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if used := loadTestRoom(t).LastUsedAt; used == nil || time.Since(*used) > time.Minute {
		t.Fatal("join did not mark the room used")
	}
	// Polls write at most hourly.
	recent := ago(10 * time.Minute)
	setRoomTimes(t, recent, time.Now().Add(-48*time.Hour))
	stateRequest(r, "guest-token")
	if used := loadTestRoom(t).LastUsedAt; !used.Equal(*recent) {
		t.Fatal("poll wrote again within the hour")
	}
	setRoomTimes(t, ago(2*time.Hour), time.Now().Add(-48*time.Hour))
	stateRequest(r, "guest-token")
	if used := loadTestRoom(t).LastUsedAt; time.Since(*used) > time.Minute {
		t.Fatal("poll did not mark the room used")
	}
}
