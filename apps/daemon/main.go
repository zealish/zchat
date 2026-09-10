// Command zchat-daemon runs the ZChat WhatsApp backend and serves the desktop
// client over a Unix socket.
package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"

	"github.com/zealish/zchat/apps/daemon/service"
	"github.com/zealish/zchat/apps/daemon/store"
	"github.com/zealish/zchat/apps/daemon/wa"
	"github.com/zealish/zchat/packages/ipc"
	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
	"github.com/zealish/zchat/packages/shared/logging"
	"github.com/zealish/zchat/packages/shared/xdgpaths"
)

func main() {
	debug := flag.Bool("debug", false, "enable debug logging")
	socket := flag.String("socket", xdgpaths.SocketPath(), "unix socket path")
	flag.Parse()

	log := logging.Setup("daemon", *debug)

	lis, err := ipc.Listen(*socket)
	if err != nil {
		if errors.Is(err, ipc.ErrAlreadyRunning) {
			log.Info().Str("socket", *socket).Msg("daemon already running")
			return
		}
		log.Fatal().Err(err).Msg("bind socket")
	}
	defer os.Remove(*socket)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dbPath, err := xdgpaths.DatabasePath()
	if err != nil {
		log.Fatal().Err(err).Msg("resolve database path")
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		log.Fatal().Err(err).Msg("open store")
	}
	defer st.Close()

	broker := service.NewBroker(log)
	session, err := wa.New(ctx, st.DB(), st, log, logging.WhatsmeowAdapter(log), broker)
	if err != nil {
		log.Fatal().Err(err).Msg("create session")
	}
	if err := session.Start(ctx); err != nil {
		log.Error().Err(err).Msg("start whatsapp session")
	}

	server := grpc.NewServer()
	zchatv1.RegisterChatServiceServer(server, service.New(log, st, session, broker))

	go func() {
		<-ctx.Done()
		log.Info().Msg("shutting down")
		server.GracefulStop()
		session.Stop()
	}()

	log.Info().Str("socket", *socket).Str("database", dbPath).Msg("daemon ready")
	if err := server.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("serve")
	}
}
