package management

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdminBootstrapHashesAndRemovesPlaintext(t *testing.T) {
	directory := t.TempDir()
	hashPath := filepath.Join(directory, "admin.hash")
	bootstrap := "a-long-initial-admin-password"
	auth, err := newAdminAuth(hashPath, bootstrap, true)
	if err != nil {
		t.Fatal(err)
	}
	if !auth.verifyPassword(bootstrap) || auth.verifyPassword("wrong-password") {
		t.Fatal("password hash verification failed")
	}
	if err := os.WriteFile(filepath.Join(directory, "stack.env"), []byte("MANAGER_ADMIN_PASSWORD="+bootstrap+"\nPORT=8080\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeBootstrapPassword(filepath.Join(directory, "stack.env")); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, "stack.env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "PORT=8080\n" {
		t.Fatalf("bootstrap password remains in config: %q", contents)
	}
}

func TestAdminLoginRateLimit(t *testing.T) {
	auth := &adminAuth{loginTries: make(map[string]loginAttempt)}
	for attempt := 0; attempt < 5; attempt++ {
		if !auth.allowLogin("127.0.0.1") {
			t.Fatalf("attempt %d unexpectedly blocked", attempt+1)
		}
	}
	if auth.allowLogin("127.0.0.1") {
		t.Fatal("sixth login attempt should be rate limited")
	}
}
