package management

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Settings struct {
	LiveKitAPIKey      string `json:"livekit_api_key,omitempty"`
	LiveKitAPISecret   string `json:"livekit_api_secret,omitempty"`
	LiveKitPublicURL   string `json:"livekit_public_url"`
	Port               int    `json:"port"`
	CORSAllowedOrigins string `json:"cors_allowed_origins"`
}

type SettingsView struct {
	LiveKitAPIKeyConfigured    bool   `json:"livekit_api_key_configured"`
	LiveKitAPISecretConfigured bool   `json:"livekit_api_secret_configured"`
	LiveKitPublicURL           string `json:"livekit_public_url"`
	Port                       int    `json:"port"`
	CORSAllowedOrigins         string `json:"cors_allowed_origins"`
}

var safeCredentialPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,256}$`)

func ReadSettings(path string) (Settings, map[string]string, error) {
	values, err := readEnvFile(path)
	if err != nil {
		return Settings{}, nil, err
	}
	port, err := strconv.Atoi(values["PORT"])
	if err != nil {
		return Settings{}, nil, errors.New("PORT yapılandırması geçersiz")
	}
	settings := Settings{
		LiveKitAPIKey:      values["LIVEKIT_API_KEY"],
		LiveKitAPISecret:   values["LIVEKIT_API_SECRET"],
		LiveKitPublicURL:   values["LIVEKIT_PUBLIC_URL"],
		Port:               port,
		CORSAllowedOrigins: values["CORS_ALLOWED_ORIGINS"],
	}
	return settings, values, nil
}

func (settings Settings) View() SettingsView {
	return SettingsView{
		LiveKitAPIKeyConfigured:    settings.LiveKitAPIKey != "",
		LiveKitAPISecretConfigured: settings.LiveKitAPISecret != "",
		LiveKitPublicURL:           settings.LiveKitPublicURL,
		Port:                       settings.Port,
		CORSAllowedOrigins:         settings.CORSAllowedOrigins,
	}
}

func ValidateSettings(settings Settings) error {
	if !safeCredentialPattern.MatchString(settings.LiveKitAPIKey) {
		return errors.New("LiveKit API key yalnızca harf, rakam, tire ve alt çizgi içermeli")
	}
	if !safeCredentialPattern.MatchString(settings.LiveKitAPISecret) {
		return errors.New("LiveKit API secret yalnızca harf, rakam, tire ve alt çizgi içermeli")
	}
	publicURL, err := url.Parse(settings.LiveKitPublicURL)
	if err != nil || (publicURL.Scheme != "ws" && publicURL.Scheme != "wss") || publicURL.Host == "" || publicURL.User != nil {
		return errors.New("LiveKit public URL ws:// veya wss:// ile başlayan geçerli bir adres olmalı")
	}
	if settings.Port < 1 || settings.Port > 65535 {
		return errors.New("PORT 1 ile 65535 arasında olmalı")
	}
	origins := strings.Split(settings.CORSAllowedOrigins, ",")
	if len(origins) == 0 || strings.TrimSpace(settings.CORSAllowedOrigins) == "" {
		return errors.New("En az bir CORS origin gerekli")
	}
	for _, origin := range origins {
		parsed, err := url.Parse(strings.TrimSpace(origin))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("CORS origin geçersiz: %q", strings.TrimSpace(origin))
		}
	}
	return nil
}

func WriteSettings(path string, oldValues map[string]string, settings Settings) error {
	if err := ValidateSettings(settings); err != nil {
		return err
	}
	values := make(map[string]string, len(oldValues)+5)
	for key, value := range oldValues {
		values[key] = value
	}
	values["LIVEKIT_API_KEY"] = settings.LiveKitAPIKey
	values["LIVEKIT_API_SECRET"] = settings.LiveKitAPISecret
	values["LIVEKIT_PUBLIC_URL"] = settings.LiveKitPublicURL
	values["PORT"] = strconv.Itoa(settings.Port)
	values["CORS_ALLOWED_ORIGINS"] = settings.CORSAllowedOrigins

	content, err := marshalEnv(values)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := atomicWrite(path+".bak", current, 0600); err != nil {
		return fmt.Errorf("config yedeği yazılamadı: %w", err)
	}
	return atomicWrite(path, content, 0600)
}

func removeEnvValue(path, key string) error {
	values, err := readEnvFile(path)
	if err != nil {
		return err
	}
	if _, exists := values[key]; !exists {
		return nil
	}
	delete(values, key)
	content, err := marshalEnv(values)
	if err != nil {
		return err
	}
	return atomicWrite(path, content, 0600)
}

func marshalEnv(values map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var content strings.Builder
	for _, key := range keys {
		if strings.ContainsAny(key, "=\r\n") || strings.ContainsAny(values[key], "\r\n") {
			return nil, errors.New("Yapılandırma satırı geçersiz")
		}
		fmt.Fprintf(&content, "%s=%s\n", key, values[key])
	}
	return []byte(content.String()), nil
}

func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, errors.New("Config dosyasında geçersiz satır")
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		values[strings.TrimSpace(key)] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".config-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
