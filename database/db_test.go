package database

import (
	"net/url"
	"os"
	"testing"
	"time"

	"reisub-backend/models"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
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
	db.Model(&models.RoomRole{}).Where("user_id = ?", 7).Updates(map[string]interface{}{"role": models.RoleParticipant, "permissions": "end_meeting"})
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&models.RoomRole{}).Count(&count)
	var first models.RoomRole
	db.Where("user_id = ?", 7).First(&first)
	if count != 2 || first.Role != models.RoleParticipant || first.Permissions != "end_meeting" {
		t.Fatalf("second migration changed roles: count=%d %+v", count, first)
	}
}

// legacyRoomRole is room_roles before permissions: several owners per room,
// moderators, and acting owners remembering their previous role.
type legacyRoomRole struct {
	ID           uint   `gorm:"primaryKey"`
	RoomID       string `gorm:"uniqueIndex:room_user_role;not null"`
	UserID       uint   `gorm:"uniqueIndex:room_user_role;not null"`
	Role         string `gorm:"not null"`
	GrantedAt    time.Time
	GrantedBy    uint
	Suspended    bool `gorm:"not null;default:false"`
	Acting       bool `gorm:"not null;default:false"`
	PreviousRole string
}

func (legacyRoomRole) TableName() string { return "room_roles" }

func TestMigrateConvertsRolesToPermissionsOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:migrate-permissions?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err = db.AutoMigrate(&models.User{}, &models.Room{}, &legacyRoomRole{}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Hour)
	at := func(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }
	for _, row := range []legacyRoomRole{
		// Room A: two connected owners and a moderator.
		{RoomID: "A", UserID: 1, Role: "owner", GrantedAt: at(0)},
		{RoomID: "A", UserID: 2, Role: "owner", GrantedAt: at(1)},
		{RoomID: "A", UserID: 3, Role: "moderator", GrantedAt: at(2)},
		// Room B: the owner is away; two acting owners, a former moderator first.
		{RoomID: "B", UserID: 4, Role: "owner", GrantedAt: at(0), Suspended: true},
		{RoomID: "B", UserID: 5, Role: "owner", GrantedAt: at(1), Acting: true, PreviousRole: "moderator"},
		{RoomID: "B", UserID: 6, Role: "owner", GrantedAt: at(2), Acting: true},
		// Room C: an away owner and a connected one; a stale acting owner.
		{RoomID: "C", UserID: 7, Role: "owner", GrantedAt: at(0), Suspended: true},
		{RoomID: "C", UserID: 8, Role: "owner", GrantedAt: at(1)},
		{RoomID: "C", UserID: 9, Role: "owner", GrantedAt: at(2), Acting: true, PreviousRole: "moderator"},
	} {
		if err = db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	const moderator = "share_screen,mute_others,request_unmute,admit_members,end_meeting"
	const all = "share_screen,mute_others,request_unmute,admit_members,remove_members,end_meeting,grant_permissions"
	want := map[uint]models.RoomRole{
		1: {Role: "owner"},
		2: {Role: "participant", Permissions: all},
		3: {Role: "participant", Permissions: moderator},
		4: {Role: "owner", Suspended: true},
		5: {Role: "owner", Acting: true, PreviousPermissions: moderator},
		7: {Role: "participant", Permissions: all},
		8: {Role: "owner"},
		9: {Role: "participant", Permissions: moderator},
	}
	check := func(when string) {
		t.Helper()
		var rows []models.RoomRole
		db.Order("user_id").Find(&rows)
		if len(rows) != len(want) {
			t.Fatalf("%s: %d rows, want %d: %+v", when, len(rows), len(want), rows)
		}
		for _, got := range rows {
			w, ok := want[got.UserID]
			if !ok || got.Role != w.Role || got.Permissions != w.Permissions || got.Suspended != w.Suspended || got.Acting != w.Acting || got.PreviousPermissions != w.PreviousPermissions {
				t.Fatalf("%s: user %d = %+v, want %+v", when, got.UserID, got, w)
			}
		}
	}
	check("after migration")
	// Later changes survive another startup.
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	check("after a second startup")
}

func TestMigrateHashesPlainTokensAndDropsThem(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:hash-tokens?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	// An older database, which kept sessions plain.
	if err = db.Exec(`CREATE TABLE users (id integer PRIMARY KEY, nick text NOT NULL UNIQUE, password text NOT NULL, token text, is_guest numeric NOT NULL DEFAULT false, created_at datetime)`).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`CREATE INDEX idx_users_token ON users(token)`).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(`INSERT INTO users (id, nick, password, token) VALUES (1, 'alice', 'x', 'alice-session'), (2, 'bob', 'x', '')`).Error; err != nil {
		t.Fatal(err)
	}

	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasColumn("users", "token") {
		t.Fatal("the plain token column is still there")
	}
	var users []models.User
	db.Order("id").Find(&users)
	if users[0].TokenHash != models.HashToken("alice-session") || users[1].TokenHash != "" {
		t.Fatalf("hashes: %q, %q", users[0].TokenHash, users[1].TokenHash)
	}
	// Running again changes nothing.
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	var again models.User
	db.First(&again, 1)
	if again.TokenHash != users[0].TokenHash {
		t.Fatalf("hash changed on a second run: %q", again.TokenHash)
	}
}

func TestCountUntrimmedNicksOnPostgres(t *testing.T) {
	server := os.Getenv("TEST_DATABASE_URL")
	if server == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	admin, err := gorm.Open(postgres.Open(server), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	admin.Exec(`DROP SCHEMA IF EXISTS count_untrimmed CASCADE`)
	admin.Exec(`CREATE SCHEMA count_untrimmed`)
	t.Cleanup(func() { admin.Exec(`DROP SCHEMA count_untrimmed CASCADE`) })
	// The schema for every pooled connection, not just one.
	dsn, err := url.Parse(server)
	if err != nil {
		t.Fatal(err)
	}
	query := dsn.Query()
	query.Set("search_path", "count_untrimmed")
	dsn.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(dsn.String()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
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
