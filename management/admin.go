package management

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const adminPasswordCost = 12

type adminAuth struct {
	mutex        sync.Mutex
	passwordHash []byte
	sessions     map[string]session
	loginTries   map[string]loginAttempt
	cookieSecure bool
}

type session struct {
	csrf      string
	expiresAt time.Time
}

type loginAttempt struct {
	count   int
	resetAt time.Time
}

func newAdminAuth(hashPath, bootstrapPassword string, cookieSecure bool) (*adminAuth, error) {
	hash, err := os.ReadFile(hashPath)
	if err == nil {
		return &adminAuth{
			passwordHash: hash,
			sessions:     make(map[string]session),
			loginTries:   make(map[string]loginAttempt),
			cookieSecure: cookieSecure,
		}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(bootstrapPassword) < 16 {
		return nil, errors.New("İlk admin parolası en az 16 karakter olmalı")
	}
	hash, err = bcrypt.GenerateFromPassword([]byte(bootstrapPassword), adminPasswordCost)
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(hashPath, hash, 0600); err != nil {
		return nil, err
	}
	return &adminAuth{
		passwordHash: hash,
		sessions:     make(map[string]session),
		loginTries:   make(map[string]loginAttempt),
		cookieSecure: cookieSecure,
	}, nil
}

func (auth *adminAuth) verifyPassword(password string) bool {
	return bcrypt.CompareHashAndPassword(auth.passwordHash, []byte(password)) == nil
}

func (auth *adminAuth) changePassword(currentPassword, newPassword, hashPath string) error {
	if !auth.verifyPassword(currentPassword) {
		return errors.New("Mevcut parola yanlış")
	}
	if len(newPassword) < 16 {
		return errors.New("Yeni parola en az 16 karakter olmalı")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), adminPasswordCost)
	if err != nil {
		return err
	}
	if err := atomicWrite(hashPath, hash, 0600); err != nil {
		return err
	}
	auth.mutex.Lock()
	auth.passwordHash = hash
	auth.sessions = make(map[string]session)
	auth.mutex.Unlock()
	return nil
}

func (auth *adminAuth) newSession() (string, string, error) {
	sessionID, err := randomToken()
	if err != nil {
		return "", "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", "", err
	}
	auth.mutex.Lock()
	auth.sessions[sessionID] = session{csrf: csrf, expiresAt: time.Now().Add(8 * time.Hour)}
	auth.mutex.Unlock()
	return sessionID, csrf, nil
}

func (auth *adminAuth) getSession(sessionID string) (session, bool) {
	auth.mutex.Lock()
	defer auth.mutex.Unlock()
	value, ok := auth.sessions[sessionID]
	if !ok {
		return session{}, false
	}
	if time.Now().After(value.expiresAt) {
		delete(auth.sessions, sessionID)
		return session{}, false
	}
	return value, true
}

func (auth *adminAuth) deleteSession(sessionID string) {
	auth.mutex.Lock()
	delete(auth.sessions, sessionID)
	auth.mutex.Unlock()
}

func (auth *adminAuth) allowLogin(client string) bool {
	auth.mutex.Lock()
	defer auth.mutex.Unlock()
	now := time.Now()
	attempt := auth.loginTries[client]
	if now.After(attempt.resetAt) {
		attempt = loginAttempt{resetAt: now.Add(time.Minute)}
	}
	if attempt.count >= 5 {
		auth.loginTries[client] = attempt
		return false
	}
	attempt.count++
	auth.loginTries[client] = attempt
	return true
}

func (auth *adminAuth) clearLoginAttempts(client string) {
	auth.mutex.Lock()
	delete(auth.loginTries, client)
	auth.mutex.Unlock()
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func equalToken(first, second string) bool {
	if len(first) != len(second) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(first), []byte(second)) == 1
}

func removeBootstrapPassword(path string) error {
	return removeEnvValue(path, "MANAGER_ADMIN_PASSWORD")
}
