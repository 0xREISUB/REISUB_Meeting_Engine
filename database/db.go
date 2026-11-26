package database

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"reisub-backend/models"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

// ConnectDB opens the database: PostgreSQL when DATABASE_URL is set (as in
// production), otherwise the SQLite file at DB_PATH (default reisub.db),
// for development and small servers.
func ConnectDB() {
	var err error
	DB, err = gorm.Open(dialectorFromEnv(), &gorm.Config{
		// Duplicate keys and the like come back the same from either
		// database (gorm.ErrDuplicatedKey).
		TranslateError: true,
	})
	if err != nil {
		log.Fatal("Veritabanına bağlanılamadı: " + err.Error())
	}
	if DB.Dialector.Name() == "postgres" {
		sqlDB, err := DB.DB()
		if err != nil {
			log.Fatal("Veritabanına bağlanılamadı: " + err.Error())
		}
		sqlDB.SetMaxOpenConns(envInt("DATABASE_MAX_CONNECTIONS", 25))
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
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

// Migrate creates or updates all tables (see migrateTables), then hashes
// the session tokens older databases stored plain (see hashStoredTokens).
//
// The first time the room_roles table
// is created, every existing room's OwnerID becomes an owner row; this runs
// exactly once, in the same transaction as the table creation, so later
// role changes are never overwritten by a restart. Likewise, the first time
// room_roles gains its permissions column, the former roles are converted to
// permissions once (see convertRolesToPermissions).
func Migrate(db *gorm.DB) error {
	if err := migrateTables(db); err != nil {
		return err
	}
	return hashStoredTokens(db)
}

// hashStoredTokens replaces the plain session tokens older databases kept
// in users.token with their hashes (models.HashToken) and drops the column,
// so nothing in the database can be used as a session. Sessions stay valid:
// a token's hash is what it is looked up by now. Runs once, while the
// column is there.
func hashStoredTokens(db *gorm.DB) error {
	if !db.Migrator().HasColumn("users", "token") {
		return nil
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			ID    uint
			Token string
		}
		if err := tx.Table("users").Select("id, token").Where("token <> ''").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := tx.Table("users").Where("id = ?", row.ID).Update("token_hash", models.HashToken(row.Token)).Error; err != nil {
				return err
			}
		}
		// Plain SQL, which SQLite and PostgreSQL both take.
		if err := tx.Exec(`DROP INDEX IF EXISTS idx_users_token`).Error; err != nil {
			return err
		}
		return tx.Exec(`ALTER TABLE users DROP COLUMN token`).Error
	})
	if err != nil {
		return err
	}
	// SQLite keeps freed pages, old tokens in them, in the file until it is
	// rewritten.
	if db.Dialector.Name() == "sqlite" {
		return db.Exec(`VACUUM`).Error
	}
	return nil
}

// migrateTables creates or updates all tables.
func migrateTables(db *gorm.DB) error {
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
			return tx.AutoMigrate(append([]any{&models.User{}, &models.Room{}, &models.RoomMember{}, &models.ChatMessage{}, &models.RoomMicrophoneLock{}, &models.RoomBan{}, &models.WaitingRequest{}, &models.MeetingCooldown{}}, socialModels...)...)
		})
	}
	return db.AutoMigrate(append([]any{&models.User{}, &models.Room{}, &models.RoomRole{}, &models.RoomMember{}, &models.ChatMessage{}, &models.RoomMicrophoneLock{}, &models.RoomBan{}, &models.WaitingRequest{}, &models.MeetingCooldown{}}, socialModels...)...)
}

// socialModels are the tables behind profiles, friends, teams and their
// chats, and behind signed-in devices and their push notifications.
var socialModels = []any{&models.Friendship{}, &models.Team{}, &models.TeamMember{}, &models.SocialMessage{}, &models.MeetingPresence{}, &models.ConversationRead{}, &models.Session{}, &models.PushDevice{}}

// legacyModerator is the removed "moderator" role. Moderators could do what
// these permissions allow (admit_members because they could already manage
// who joins), but neither change roles nor remove members.
const legacyModerator = "moderator"

var moderatorPermissions = []string{
	models.PermissionShareScreen,
	models.PermissionMuteOthers,
	models.PermissionRequestUnmute,
	models.PermissionAdmitMembers,
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

// trimWhitespace is SQL for column without leading or trailing ASCII
// whitespace, in the database's own dialect.
func trimWhitespace(db *gorm.DB, column string) string {
	if db.Dialector.Name() == "postgres" {
		return "btrim(" + column + `, E' \t\n\v\f\r')`
	}
	return "trim(" + column + ", ' ' || char(9) || char(10) || char(11) || char(12) || char(13))"
}

// CountUntrimmedNicks counts users whose stored nick has leading or trailing
// ASCII whitespace. Login and registration trim nicks, so such accounts can
// no longer be reached. It only reports; it never modifies data.
func CountUntrimmedNicks(db *gorm.DB) (int64, error) {
	var count int64
	err := db.Model(&models.User{}).
		Where("nick <> " + trimWhitespace(db, "nick")).
		Count(&count).Error
	return count, err
}

// dialectorFromEnv picks the database: DATABASE_URL (PostgreSQL, e.g.
// postgres://user:pass@host:5432/db?sslmode=disable) or the SQLite file at
// DB_PATH.
func dialectorFromEnv() gorm.Dialector {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return postgres.Open(url)
	}
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "reisub.db"
	}
	if dir := filepath.Dir(dbPath); dir != "." {
		if err := os.MkdirAll(dir, 0750); err != nil {
			log.Fatal("Veritabanı dizini oluşturulamadı: " + err.Error())
		}
	}
	return sqlite.Open(dbPath)
}

func envInt(name string, fallback int) int {
	if value, err := strconv.Atoi(os.Getenv(name)); err == nil && value > 0 {
		return value
	}
	return fallback
}
