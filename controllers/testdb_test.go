package controllers

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var schemaUnsafe = regexp.MustCompile(`[^a-z0-9_]+`)

// openTestDB opens a fresh database for one test: in-memory SQLite, or,
// with TEST_DATABASE_URL set, a schema of its own in that PostgreSQL,
// dropped afterwards, so the whole suite can run against PostgreSQL too.
func openTestDB(t *testing.T, name string) (*gorm.DB, error) {
	t.Helper()
	// A fresh config for each database: gorm.Open writes its dialect in.
	config := func() *gorm.Config { return &gorm.Config{TranslateError: true} }
	server := os.Getenv("TEST_DATABASE_URL")
	if server == "" {
		return gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), config())
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	schema := schemaUnsafe.ReplaceAllString(strings.ToLower(name), "_")
	if len(schema) > 40 {
		schema = schema[:40]
	}
	schema = "t_" + schema + "_" + hex.EncodeToString(suffix)
	admin, err := gorm.Open(postgres.Open(server), config())
	if err != nil {
		return nil, err
	}
	if err := admin.Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		if sqlDB, err := admin.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	dsn, err := url.Parse(server)
	if err != nil {
		return nil, err
	}
	query := dsn.Query()
	query.Set("search_path", schema)
	dsn.RawQuery = query.Encode()
	return gorm.Open(postgres.Open(dsn.String()), config())
}
