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
