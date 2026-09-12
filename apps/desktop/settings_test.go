package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
)

// withConfigHome points XDG_CONFIG_HOME at a temporary directory so the tests
// never touch the developer's real preferences.
func withConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "zchat", settingsFile)
}

func TestLoadSettingsDefaultsWhenMissing(t *testing.T) {
	withConfigHome(t)

	got := loadSettings()
	if got != defaultSettings() {
		t.Fatalf("got %+v, want %+v", got, defaultSettings())
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	withConfigHome(t)

	want := settings{Theme: themeDark, Notifications: false}
	if err := want.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := loadSettings(); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLoadSettingsRejectsUnknownTheme(t *testing.T) {
	path := withConfigHome(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"neon","notifications":false}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := loadSettings()
	if got.Theme != themeSystem {
		t.Errorf("theme = %q, want %q", got.Theme, themeSystem)
	}
	// An unusable theme must not discard the rest of the file.
	if got.Notifications {
		t.Error("notifications = true, want false")
	}
}

func TestLoadSettingsFallsBackOnCorruptFile(t *testing.T) {
	path := withConfigHome(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := loadSettings(); got != defaultSettings() {
		t.Fatalf("got %+v, want %+v", got, defaultSettings())
	}
}

func TestThemeColorScheme(t *testing.T) {
	for _, tc := range []struct {
		theme theme
		want  adw.ColorScheme
	}{
		{themeSystem, adw.ColorSchemeDefault},
		{themeLight, adw.ColorSchemeForceLight},
		{themeDark, adw.ColorSchemeForceDark},
		{theme("bogus"), adw.ColorSchemeDefault},
	} {
		if got := tc.theme.colorScheme(); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.theme, got, tc.want)
		}
	}
}
