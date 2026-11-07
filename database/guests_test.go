package database

import (
	"testing"
	"time"

	"reisub-backend/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestDeleteInactiveGuestsKeepsActiveUsersAndHistory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:guest-cleanup?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ago := func(days int) *time.Time { v := now.AddDate(0, 0, -days); return &v }
	later := now.Add(time.Hour)
	users := []models.User{
		{ID: 1, Nick: "guest-active", IsGuest: true, LastSeenAt: ago(1), TokenExpiresAt: &later},
		{ID: 2, Nick: "guest-idle", IsGuest: true, LastSeenAt: ago(20), TokenExpiresAt: &later},
		{ID: 3, Nick: "guest-never-used", IsGuest: true, CreatedAt: *ago(20), TokenExpiresAt: &later},
		{ID: 4, Nick: "guest-expired", IsGuest: true, LastSeenAt: ago(1), TokenExpiresAt: ago(0)},
		{ID: 5, Nick: "old-account", Password: "x", CreatedAt: *ago(400), LastSeenAt: ago(400)},
	}
	for _, user := range users {
		if err = db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uint{1, 2} {
		db.Create(&models.RoomRole{RoomID: "123-456-789", UserID: id, Role: models.RoleOwner})
		db.Create(&models.RoomMicrophoneLock{RoomID: "123-456-789", UserID: id, Locked: true})
		db.Create(&models.RoomMember{RoomID: "123-456-789", UserID: id, DisplayName: "x"})
		db.Create(&models.ChatMessage{RoomID: "123-456-789", SenderID: id, Sender: "x", Content: "hi"})
	}

	deleted, err := DeleteInactiveGuests(db, now.AddDate(0, 0, -14))
	if err != nil || deleted != 3 {
		t.Fatalf("deleted %d (%v), want 3", deleted, err)
	}
	var remaining []uint
	db.Model(&models.User{}).Order("id").Pluck("id", &remaining)
	if len(remaining) != 2 || remaining[0] != 1 || remaining[1] != 5 {
		t.Fatalf("remaining users %v, want [1 5]", remaining)
	}
	for _, table := range []any{&models.RoomRole{}, &models.RoomMicrophoneLock{}, &models.RoomMember{}} {
		var userIDs []uint
		db.Model(table).Pluck("user_id", &userIDs)
		if len(userIDs) != 1 || userIDs[0] != 1 {
			t.Fatalf("%T rows for users %v, want only the active guest", table, userIDs)
		}
	}
	var messages int64
	db.Model(&models.ChatMessage{}).Count(&messages)
	if messages != 2 {
		t.Fatalf("chat history lost: %d messages", messages)
	}
	// IDs are never reused after deletion.
	fresh := models.User{Nick: "guest-new", IsGuest: true}
	db.Create(&fresh)
	if fresh.ID <= 5 {
		t.Fatalf("deleted user ID reused: %d", fresh.ID)
	}
}
