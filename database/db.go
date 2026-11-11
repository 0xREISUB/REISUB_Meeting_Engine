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
// role changes are never overwritten by a restart. Likewise, the first time
// room_roles gains its permissions column, the former roles are converted to
// permissions once (see convertRolesToPermissions).
func Migrate(db *gorm.DB) error {
	legacyRoles := db.Migrator().HasTable(&models.RoomRole{}) && !db.Migrator().HasColumn(&models.RoomRole{}, "permissions")
	if !db.Migrator().HasTable(&models.RoomRole{}) {
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&models.Room{}, &models.RoomRole{}); err != nil {
				return err
			}
			return tx.Exec(`INSERT INTO room_roles (room_id, user_id, role, permissions, granted_at, granted_by, suspended, acting, previous_permissions)
				SELECT room_id, owner_id, ?, '', created_at, 0, false, false, '' FROM rooms WHERE owner_id <> 0`, models.RoleOwner).Error
		})
		if err != nil {
			return err
		}
	}
	if legacyRoles {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&models.RoomRole{}); err != nil {
				return err
			}
			if err := convertRolesToPermissions(tx); err != nil {
				return err
			}
			return tx.AutoMigrate(&models.User{}, &models.Room{}, &models.RoomMember{}, &models.ChatMessage{}, &models.RoomMicrophoneLock{})
		})
	}
	return db.AutoMigrate(&models.User{}, &models.Room{}, &models.RoomRole{}, &models.RoomMember{}, &models.ChatMessage{}, &models.RoomMicrophoneLock{})
}

// legacyModerator is the removed "moderator" role. Moderators could do what
// these permissions allow, but not change roles.
const legacyModerator = "moderator"

var moderatorPermissions = []string{
	models.PermissionShareScreen,
	models.PermissionMuteOthers,
	models.PermissionRequestUnmute,
	models.PermissionEndMeeting,
}

// convertRolesToPermissions turns the former multi-owner/moderator roles into
// one owner per room plus participant permissions:
//   - every moderator becomes a participant with moderatorPermissions, and an
//     acting owner who was a moderator gets them back when stepping down;
//   - of several real owners, the one connected (not suspended) and granted
//     earliest stays owner; the others keep every permission as participants;
//   - an acting owner stays only while the real owner is away, and only the
//     earliest one; the others step back.
func convertRolesToPermissions(tx *gorm.DB) error {
	moderator := models.JoinPermissions(moderatorPermissions)
	if err := tx.Exec(`UPDATE room_roles SET role = ?, permissions = ? WHERE role = ?`, models.RoleParticipant, moderator, legacyModerator).Error; err != nil {
		return err
	}
	if err := tx.Exec(`UPDATE room_roles SET previous_permissions = ? WHERE acting AND previous_role = ?`, moderator, legacyModerator).Error; err != nil {
		return err
	}
	var owners []models.RoomRole
	if err := tx.Where("role = ?", models.RoleOwner).Order("room_id, suspended, granted_at, user_id").Find(&owners).Error; err != nil {
		return err
	}
	byRoom := map[string][]models.RoomRole{}
	var rooms []string
	for _, owner := range owners {
		if _, seen := byRoom[owner.RoomID]; !seen {
			rooms = append(rooms, owner.RoomID)
		}
		byRoom[owner.RoomID] = append(byRoom[owner.RoomID], owner)
	}
	all := models.JoinPermissions(models.AllPermissions)
	for _, roomID := range rooms {
		var real, acting []models.RoomRole
		for _, owner := range byRoom[roomID] {
			if owner.Acting {
				acting = append(acting, owner)
			} else {
				real = append(real, owner)
			}
		}
		for _, extra := range tail(real) {
			if err := tx.Model(&extra).Updates(map[string]interface{}{"role": models.RoleParticipant, "permissions": all, "suspended": false}).Error; err != nil {
				return err
			}
		}
		keepActing := len(real) == 0 || real[0].Suspended
		for i, owner := range acting {
			if keepActing && i == 0 {
				continue
			}
			if owner.PreviousPermissions == "" {
				if err := tx.Delete(&owner).Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Model(&owner).Updates(map[string]interface{}{"role": models.RoleParticipant, "permissions": owner.PreviousPermissions, "acting": false, "previous_permissions": ""}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func tail(rows []models.RoomRole) []models.RoomRole {
	if len(rows) < 2 {
		return nil
	}
	return rows[1:]
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
