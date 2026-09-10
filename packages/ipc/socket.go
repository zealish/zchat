// Package ipc holds the Unix-socket conventions shared by the ZChat daemon and
// desktop client. The PRD forbids TCP transport entirely.
package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ErrAlreadyRunning is returned by Listen when another daemon already owns the
// socket and answers on it.
var ErrAlreadyRunning = errors.New("zchat daemon already running")

// Listen binds the daemon's Unix socket, clearing a stale socket file first.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}

	if _, err := os.Stat(path); err == nil {
		conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil, ErrAlreadyRunning
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}

	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		lis.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return lis, nil
}

// Dial creates a gRPC client connection to the daemon socket. grpc's built-in
// unix resolver handles the target, so no custom dialer is needed.
func Dial(ctx context.Context, path string) (*grpc.ClientConn, error) {
	_ = ctx
	return grpc.NewClient("unix://"+path, grpc.WithTransportCredentials(insecure.NewCredentials()))
}
