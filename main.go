package main

import (
	"log"
	"reisub-backend/database"
	"reisub-backend/routes"

	"github.com/gin-gonic/gin"
)

func main() {
	// Veritabanını başlat
	database.ConnectDB()

	// Web sunucusunu başlat
	r := gin.Default()

	// Rotaları (Endpointleri) yükle
	routes.SetupRoutes(r)

	// PRODUCTION UYARISI
	log.Println("==================================================================")
	log.Println(" UYARI: /users UÇ NOKTASI ŞU AN AKTİF DURUMDA.")
	log.Println(" PRODUCTION'A ÇIKMADAN ÖNCE routes.go İÇİNDEN KESİNLİKLE SİLİN!")
	log.Println("==================================================================")

	r.Run(":8080")
}
