package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// User Modeli (Veritabanı Tablosu)
type User struct {
	ID        uint      `gorm:"primaryKey"`
	Nick      string    `gorm:"uniqueIndex;not null"`
	Password  string    `gorm:"not null"` // Hashlenmiş şifre
	Token     string    // Güncel oturum tokeni
	CreatedAt time.Time // Üye olma tarihi
}

var db *gorm.DB

func main() {
	var err error
	// Veritabanı bağlantısı (Otomotik olarak reisub.db dosyasını oluşturur)
	db, err = gorm.Open(sqlite.Open("reisub.db"), &gorm.Config{})
	if err != nil {
		panic("Veritabanına bağlanılamadı!")
	}

	// Tabloları oluşturur/günceller (Migration)
	db.AutoMigrate(&User{})

	r := gin.Default()

	r.POST("/register", register)
	r.POST("/login", login)
	r.POST("/logout", logout)
	r.GET("/users", getUsers)

	// PRODUCTION UYARISI
	log.Println("==================================================================")
	log.Println(" UYARI: GÜVENLİK AÇIĞI RİSKİ!")
	log.Println(" /users UÇ NOKTASI ŞU AN AKTİF DURUMDA.")
	log.Println(" PRODUCTION'A ÇIKMADAN ÖNCE r.GET(\"/users\") SATIRINI VE")
	log.Println(" getUsers FONKSİYONUNU KESİNLİKLE SİLİN!")
	log.Println("==================================================================")

	r.Run(":8080") // Sunucuyu başlat
}

// Rastgele güvenli token oluşturucu
func generateToken() string {
	bytes := make([]byte, 32)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func register(c *gin.Context) {
	var input struct {
		Nick     string `json:"nick" binding:"required"`
		Password string `json:"password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Eksik bilgi gönderildi"})
		return
	}

	// Şifreyi Hash'le (Güvenlik için bcrypt)
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Şifreleme hatası"})
		return
	}

	user := User{
		Nick:     input.Nick,
		Password: string(hashedPassword),
	}

	if err := db.Create(&user).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bu nick zaten kullanılıyor olabilir"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Kayıt başarılı"})
}

func login(c *gin.Context) {
	var input struct {
		Nick     string `json:"nick" binding:"required"`
		Password string `json:"password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Eksik bilgi gönderildi"})
		return
	}

	var user User
	if err := db.Where("nick = ?", input.Nick).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Kullanıcı bulunamadı"})
		return
	}

	// Hashlenmiş şifre ile girilen şifreyi karşılaştır
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Hatalı şifre"})
		return
	}

	// BAŞARILI GİRİŞ: Yeni token oluştur.
	// Bu sayede eski token geçersiz kalır ve sadece son giriş yapılan cihaz aktif olur.
	newToken := generateToken()
	db.Model(&user).Update("token", newToken)

	c.JSON(http.StatusOK, gin.H{
		"message": "Giriş başarılı",
		"token":   newToken,
	})
}

func logout(c *gin.Context) {
	// Flutter uygulamasından Authorization header veya body içinde token gönderilir
	token := c.GetHeader("Authorization")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Token eksik"})
		return
	}

	var user User
	if err := db.Where("token = ?", token).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Geçersiz token"})
		return
	}

	// ÇIKIŞ: Tokeni veritabanında tamamen sıfırla.
	// Bu sayede Flutter uygulamasındaki eski token artık veritabanında eşleşmeyecek.
	db.Model(&user).Update("token", generateToken())

	c.JSON(http.StatusOK, gin.H{"message": "Başarıyla çıkış yapıldı"})
}

func getUsers(c *gin.Context) {
	var users []User
	// Veritabanındaki tüm kullanıcıları çeker
	if err := db.Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Kullanıcılar getirilemedi"})
		return
	}

	// Şifre hash'lerini ekranda görmemek için gizleyebilirsin ama
	// test aşamasında her şeyi görmek adına doğrudan döndürüyoruz.
	c.JSON(http.StatusOK, gin.H{"users": users})
}
