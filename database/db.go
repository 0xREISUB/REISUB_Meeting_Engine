package database

import (
	"log"
	"os"
	"path/filepath"

	"reisub-backend/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var DB *gorm.DB

func ConnectDB() {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "reisub.db"
	}
	if dir := filepath.Dir(dbPath); dir != "." {
		if err := os.MkdirAll(dir, 0750); err != nil {
			log.Fatal("Veritabanı dizini oluşturulamadı: " + err.Error())
		}
	}

	var err error
	DB, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		log.Fatal("Veritabanına bağlanılamadı: " + err.Error())
	}

	if err := Migrate(DB); err != nil {
		log.Fatal("Veritabanı tabloları güncellenemedi: " + err.Error())
	}

	if count, err := CountUntrimmedNicks(DB); err != nil {
		log.Printf("warning: could not check stored nicks for whitespace: %v", err)
	} else if count > 0 {
		log.Printf("warning: %d user(s) have leading/trailing whitespace in their nick and cannot log in with trimmed input; migrate them manually", count)
	}
}

// Migrate creates or updates all tables. The first time the room_roles table
// is created, every existing room's OwnerID becomes an owner row; this runs
// exactly once, in the same transaction as the table creation, so later
// role changes are never overwritten by a restart.
func Migrate(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.RoomRole{}) {
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&models.Room{}, &models.RoomRole{}); err != nil {
				return err
			}
			return tx.Exec(`INSERT INTO room_roles (room_id, user_id, role, granted_at, granted_by, suspended, acting, previous_role)
				SELECT room_id, owner_id, ?, created_at, 0, false, false, '' FROM rooms WHERE owner_id <> 0`, models.RoleOwner).Error
		})
		if err != nil {
			return err
		}
	}
	return db.AutoMigrate(&models.User{}, &models.Room{}, &models.RoomRole{}, &models.RoomMember{}, &models.ChatMessage{}, &models.RoomMicrophoneLock{})
}

// CountUntrimmedNicks counts users whose stored nick has leading or trailing
// ASCII whitespace. Login and registration trim nicks, so such accounts can
// no longer be reached. It only reports; it never modifies data.
func CountUntrimmedNicks(db *gorm.DB) (int64, error) {
	var count int64
	err := db.Model(&models.User{}).
		Where("nick <> trim(nick, ' ' || char(9) || char(10) || char(11) || char(12) || char(13))").
		Count(&count).Error
	return count, err
}
