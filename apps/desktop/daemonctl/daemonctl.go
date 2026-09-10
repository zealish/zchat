// Package daemonctl starts the ZChat daemon on demand so the desktop client is
// a single command to run.
package daemonctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zealish/zchat/packages/ipc"
	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// EnsureRunning returns once a daemon answers on socketPath, spawning one if
// necessary.
func EnsureRunning(ctx context.Context, socketPath string) error {
	if probe(ctx, socketPath, 300*time.Millisecond) == nil {
		return nil
	}

	bin, err := resolveBinary()
	if err != nil {
		return err
	}

	cmd := exec.Command(bin, "--socket", socketPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// Detach so the daemon outlives this client process.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon %s: %w", bin, err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release daemon process: %w", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if probe(ctx, socketPath, 300*time.Millisecond) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("daemon did not become ready within 10s")
}

func probe(ctx context.Context, socketPath string, timeout time.Duration) error {
	conn, err := ipc.Dial(ctx, socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	_, err = zchatv1.NewChatServiceClient(conn).GetConnectionState(callCtx, &zchatv1.GetConnectionStateRequest{})
	return err
}

func resolveBinary() (string, error) {
	if bin := os.Getenv("ZCHAT_DAEMON"); bin != "" {
		return bin, nil
	}
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), "zchat-daemon")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	bin, err := exec.LookPath("zchat-daemon")
	if err != nil {
		return "", fmt.Errorf("locate zchat-daemon: %w", err)
	}
	return bin, nil
}
