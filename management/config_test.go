package management

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsViewMasksCredentials(t *testing.T) {
	view := (Settings{
		LiveKitAPIKey:      "devkey",
		LiveKitAPISecret:   "devsecret",
		LiveKitPublicURL:   "wss://rtc.example.test",
		Port:               8080,
		CORSAllowedOrigins: "https://meeting.example.test",
	}).View()
	if !view.LiveKitAPIKeyConfigured || !view.LiveKitAPISecretConfigured {
		t.Fatalf("credentials should be reported configured: %+v", view)
	}
	if view.LiveKitPublicURL != "wss://rtc.example.test" || view.Port != 8080 {
		t.Fatalf("non-secret settings missing: %+v", view)
	}
}

func TestValidateSettingsRejectsUnsafeValues(t *testing.T) {
	settings := Settings{
		LiveKitAPIKey:      "devkey",
		LiveKitAPISecret:   "devsecret",
		LiveKitPublicURL:   "wss://rtc.example.test",
		Port:               8080,
		CORSAllowedOrigins: "https://meeting.example.test",
	}
	tests := []struct {
		name   string
		change func(*Settings)
	}{
		{name: "secret interpolation", change: func(value *Settings) { value.LiveKitAPISecret = "abc$def" }},
		{name: "http livekit URL", change: func(value *Settings) { value.LiveKitPublicURL = "https://rtc.example.test" }},
		{name: "out of range port", change: func(value *Settings) { value.Port = 70000 }},
		{name: "cors path", change: func(value *Settings) { value.CORSAllowedOrigins = "https://meeting.example.test/path" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := settings
			test.change(&invalid)
			if err := ValidateSettings(invalid); err == nil {
				t.Fatal("expected invalid settings to be rejected")
			}
		})
	}
}

func TestWriteSettingsBacksUpAndPreservesManagerPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stack.env")
	original := "LIVEKIT_API_KEY=devkey\nLIVEKIT_API_SECRET=devsecret\nLIVEKIT_PUBLIC_URL=wss://rtc.example.test\nPORT=8080\nCORS_ALLOWED_ORIGINS=https://meeting.example.test\nMANAGER_ADMIN_PASSWORD=initial-secret\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	settings, values, err := ReadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	settings.Port = 8090
	if err := WriteSettings(path, values, settings); err != nil {
		t.Fatal(err)
	}
	updated, values, err := ReadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Port != 8090 || values["MANAGER_ADMIN_PASSWORD"] != "initial-secret" {
		t.Fatalf("settings not written or unrelated value lost: %+v %#v", updated, values)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != original {
		t.Fatal("backup does not contain the original config")
	}
}
