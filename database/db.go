package database

import (
	"log"
	"os"
	"path/filepath"
	"reisub-backend/models" // Kendi modül adını yaz

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

	// Tabloları oluştur
	if err := DB.AutoMigrate(&models.User{}, &models.Room{}, &models.ChatMessage{}); err != nil {
		log.Fatal("Veritabanı tabloları güncellenemedi: " + err.Error())
	}
}
