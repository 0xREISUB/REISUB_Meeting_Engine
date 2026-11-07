package database

import (
	"testing"

	"reisub-backend/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCountUntrimmedNicks(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:count-untrimmed?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err = db.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	for _, nick := range []string{"alice", "bo b", " carol", "dave ", "\terin", "frank\r\n"} {
		if err = db.Create(&models.User{Nick: nick, Password: "x"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	count, err := CountUntrimmedNicks(db)
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("CountUntrimmedNicks = %d, want 4", count)
	}
}

func TestMigrateTurnsOwnerIDIntoOwnerRoleOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:migrate-roles?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	// A database from before room roles existed.
	if err = db.AutoMigrate(&models.User{}, &models.Room{}); err != nil {
		t.Fatal(err)
	}
	for i, owner := range []uint{7, 8} {
		room := models.Room{RoomID: []string{"111-111-111", "222-222-222"}[i], Name: "r", OwnerID: owner, Status: models.RoomStatusActive}
		if err = db.Create(&room).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	var roles []models.RoomRole
	db.Order("room_id").Find(&roles)
	if len(roles) != 2 || roles[0].UserID != 7 || roles[0].Role != models.RoleOwner || roles[1].UserID != 8 || roles[0].GrantedAt.IsZero() {
		t.Fatalf("unexpected roles after migration: %+v", roles)
	}

	// A later role change must survive another startup.
	db.Model(&models.RoomRole{}).Where("user_id = ?", 7).Update("role", models.RoleModerator)
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&models.RoomRole{}).Count(&count)
	var first models.RoomRole
	db.Where("user_id = ?", 7).First(&first)
	if count != 2 || first.Role != models.RoleModerator {
		t.Fatalf("second migration changed roles: count=%d role=%s", count, first.Role)
	}
}
