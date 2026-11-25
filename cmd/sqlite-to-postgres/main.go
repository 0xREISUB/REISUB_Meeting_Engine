// Command sqlite-to-postgres copies a REISUB SQLite database into an empty
// PostgreSQL one, keeping every id.
//
//	DATABASE_URL=postgres://... sqlite-to-postgres -from /data/reisub.db
//
// The SQLite database is first brought up to date (session tokens hashed),
// so nothing plain reaches PostgreSQL. Everything is copied in one
// transaction, the id sequences moved past the copied rows, and the row
// counts compared. A PostgreSQL database that already has users is left
// alone.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"reisub-backend/database"
	"reisub-backend/models"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	from := flag.String("from", "reisub.db", "the SQLite database to copy")
	flag.Parse()
	to := os.Getenv("DATABASE_URL")
	if to == "" {
		log.Fatal("DATABASE_URL (the PostgreSQL database to fill) is not set")
	}
	if _, err := os.Stat(*from); err != nil {
		log.Fatalf("no SQLite database at %s: %v", *from, err)
	}
	src, err := gorm.Open(sqlite.Open(*from), quietConfig())
	if err != nil {
		log.Fatal(err)
	}
	dst, err := gorm.Open(postgres.Open(to), quietConfig())
	if err != nil {
		log.Fatal(err)
	}
	if err := copyDatabase(src, dst); err != nil {
		log.Fatal(err)
	}
}

// quietConfig is a fresh config for each database: gorm.Open writes its
// dialect into the config, so a shared one would turn SQLite's SQL into
// PostgreSQL's.
func quietConfig() *gorm.Config {
	return &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), TranslateError: true}
}

// errNotEmpty refuses to copy over data already in PostgreSQL.
var errNotEmpty = errors.New("the PostgreSQL database already has users; nothing copied")

func copyDatabase(src, dst *gorm.DB) error {
	if err := database.Migrate(src); err != nil {
		return fmt.Errorf("bringing the SQLite database up to date: %w", err)
	}
	if err := database.Migrate(dst); err != nil {
		return fmt.Errorf("creating the PostgreSQL tables: %w", err)
	}
	var users int64
	if err := dst.Model(&models.User{}).Count(&users).Error; err != nil {
		return err
	}
	if users > 0 {
		return errNotEmpty
	}
	copies := []func(src, tx *gorm.DB) (string, int, error){
		copyTable[models.User],
		copyTable[models.Room],
		copyTable[models.RoomRole],
		copyTable[models.RoomMember],
		copyTable[models.ChatMessage],
		copyTable[models.RoomMicrophoneLock],
		copyTable[models.RoomBan],
		copyTable[models.WaitingRequest],
		copyTable[models.MeetingCooldown],
		copyTable[models.Friendship],
		copyTable[models.Team],
		copyTable[models.TeamMember],
		copyTable[models.SocialMessage],
		copyTable[models.MeetingPresence],
		copyTable[models.ConversationRead],
	}
	return dst.Transaction(func(tx *gorm.DB) error {
		for _, copy := range copies {
			table, count, err := copy(src, tx)
			if err != nil {
				return fmt.Errorf("copying %s: %w", table, err)
			}
			// The next id after the copied ones, as in SQLite.
			if err := tx.Exec(`SELECT setval(pg_get_serial_sequence(?, 'id'), (SELECT COALESCE(MAX(id), 0) + 1 FROM `+table+`), false)`, table).Error; err != nil {
				return fmt.Errorf("moving the id sequence of %s: %w", table, err)
			}
			var copied int64
			if err := tx.Table(table).Count(&copied).Error; err != nil {
				return err
			}
			if copied != int64(count) {
				return fmt.Errorf("%s: %d rows in SQLite, %d in PostgreSQL", table, count, copied)
			}
			fmt.Printf("%-22s %6d rows\n", table, count)
		}
		return nil
	})
}

// copyTable copies every row of one model, keeping ids and every value as
// it is.
func copyTable[T any](src, dst *gorm.DB) (string, int, error) {
	var model T
	statement := &gorm.Statement{DB: dst}
	if err := statement.Parse(&model); err != nil {
		return "", 0, err
	}
	table := statement.Schema.Table
	var rows []T
	if err := src.Find(&rows).Error; err != nil {
		return table, 0, err
	}
	if len(rows) == 0 {
		return table, 0, nil
	}
	// Every column as it is, defaults and zero values alike.
	if err := dst.Session(&gorm.Session{SkipHooks: true}).Select("*").CreateInBatches(rows, 500).Error; err != nil {
		return table, 0, err
	}
	return table, len(rows), nil
}
