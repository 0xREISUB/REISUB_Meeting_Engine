package database

import (
	"log"
	"reisub-backend/models" // Kendi modül adını yaz

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var DB *gorm.DB

func ConnectDB() {
	var err error
	DB, err = gorm.Open(sqlite.Open("reisub.db"), &gorm.Config{})
	if err != nil {
		log.Fatal("Veritabanına bağlanılamadı: " + err.Error())
	}

	// Tabloları oluştur
	DB.AutoMigrate(&models.User{}, &models.Room{})
}
