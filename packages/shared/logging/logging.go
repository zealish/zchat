// Package logging configures the zerolog logger shared by both ZChat binaries.
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/rs/zerolog"

	"github.com/zealish/zchat/packages/shared/xdgpaths"
)

// Setup builds a logger writing to stderr (picked up by journald) and to
// ~/.local/share/zchat/logs/<component>.log. Logging problems never abort
// startup: the file sink is simply dropped.
func Setup(component string, debug bool) zerolog.Logger {
	level := zerolog.InfoLevel
	if debug {
		level = zerolog.DebugLevel
	}

	console := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}
	var w io.Writer = console

	if dir, err := xdgpaths.LogDir(); err != nil {
		fmt.Fprintf(os.Stderr, "zchat: log directory unavailable: %v\n", err)
	} else {
		path := filepath.Join(dir, component+".log")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "zchat: cannot open %s: %v\n", path, err)
		} else {
			w = io.MultiWriter(console, f)
		}
	}

	return zerolog.New(w).Level(level).With().Timestamp().Str("component", component).Logger()
}

// WhatsmeowAdapter adapts a zerolog logger to whatsmeow's logger interface.
func WhatsmeowAdapter(l zerolog.Logger) waLog.Logger {
	return &waAdapter{log: l}
}

type waAdapter struct {
	log zerolog.Logger
}

func (a *waAdapter) Errorf(msg string, args ...any) {
	a.log.Error().Msg(fmt.Sprintf(msg, args...))
}

func (a *waAdapter) Warnf(msg string, args ...any) {
	a.log.Warn().Msg(fmt.Sprintf(msg, args...))
}

func (a *waAdapter) Infof(msg string, args ...any) {
	a.log.Info().Msg(fmt.Sprintf(msg, args...))
}

func (a *waAdapter) Debugf(msg string, args ...any) {
	a.log.Debug().Msg(fmt.Sprintf(msg, args...))
}

func (a *waAdapter) Sub(module string) waLog.Logger {
	return &waAdapter{log: a.log.With().Str("module", module).Logger()}
}
