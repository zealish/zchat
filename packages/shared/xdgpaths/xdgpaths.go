// Package xdgpaths resolves the XDG base directories ZChat stores data in.
package xdgpaths

import (
	"fmt"
	"os"
	"path/filepath"
)

const appDir = "zchat"

// dirMode is mandated by the PRD: all ZChat directories are user-private.
const dirMode = 0o700

func ensure(base string, elem ...string) (string, error) {
	path := filepath.Join(append([]string{base, appDir}, elem...)...)
	if err := os.MkdirAll(path, dirMode); err != nil {
		return "", fmt.Errorf("create %s: %w", path, err)
	}
	return path, nil
}

// ConfigDir returns ~/.config/zchat.
func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return ensure(base)
}

// DataDir returns ~/.local/share/zchat.
func DataDir() (string, error) {
	base, err := dataHome()
	if err != nil {
		return "", err
	}
	return ensure(base)
}

// CacheDir returns ~/.cache/zchat.
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return ensure(base)
}

// LogDir returns ~/.local/share/zchat/logs.
func LogDir() (string, error) {
	base, err := dataHome()
	if err != nil {
		return "", err
	}
	return ensure(base, "logs")
}

// MediaDir returns ~/.local/share/zchat/media.
func MediaDir() (string, error) {
	base, err := dataHome()
	if err != nil {
		return "", err
	}
	return ensure(base, "media")
}

// DatabasePath returns the path of the shared SQLite database.
func DatabasePath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "zchat.db"), nil
}

// SocketPath returns the daemon's Unix socket path. The PRD forbids TCP, so
// this always lives under the per-user runtime directory.
func SocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "zchat.sock")
	}
	return fmt.Sprintf("/run/user/%d/zchat.sock", os.Getuid())
}

func dataHome() (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}
