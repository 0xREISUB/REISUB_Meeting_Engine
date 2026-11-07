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

	if err := DB.AutoMigrate(&models.User{}, &models.Room{}, &models.ChatMessage{}, &models.RoomMicrophoneLock{}); err != nil {
		log.Fatal("Veritabanı tabloları güncellenemedi: " + err.Error())
	}

	if count, err := CountUntrimmedNicks(DB); err != nil {
		log.Printf("warning: could not check stored nicks for whitespace: %v", err)
	} else if count > 0 {
		log.Printf("warning: %d user(s) have leading/trailing whitespace in their nick and cannot log in with trimmed input; migrate them manually", count)
	}
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
