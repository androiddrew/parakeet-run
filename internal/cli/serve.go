package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/androiddrew/parakeet-run/internal/assets"
	"github.com/androiddrew/parakeet-run/internal/audio"
	"github.com/androiddrew/parakeet-run/internal/engine"
	"github.com/androiddrew/parakeet-run/internal/server"
	"github.com/spf13/cobra"
)

func serveCommand() *cobra.Command {
	return &cobra.Command{Use: "serve", Short: "Serve the transcription API", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		l, err := resolve(cmd)
		if err != nil {
			return err
		}
		c := l.Config
		log := logger(c, cmd.ErrOrStderr())
		decoder := audio.Decoder{Config: c.Audio}
		if err := decoder.Check(); err != nil {
			return err
		}
		if err := assets.VerifyAll(cmd.Context(), c.Model.Dir); err != nil {
			return fmt.Errorf("verify model assets (run pull): %w", err)
		}
		if c.APIKey == "" {
			host, _, _ := net.SplitHostPort(c.Server.Listen)
			if host != "localhost" && !net.ParseIP(host).IsLoopback() {
				log.Warn("API authentication disabled on a non-loopback listener")
			}
		}
		pool := server.NewPool(c.Server.Replicas, c.Server.MaxQueue)
		loadCtx, cancelLoad := context.WithCancel(cmd.Context())
		defer cancelLoad()
		load := func() error {
			for i := range c.Server.Replicas {
				if err := loadCtx.Err(); err != nil {
					return err
				}
				start := time.Now()
				e, err := engine.Load(c)
				if err == nil {
					err = e.Warm(loadCtx)
				}
				if err != nil {
					if e != nil {
						err = errors.Join(err, e.Close())
					}
					pool.Fail()
					log.Error("replica initialization failed", "replica", i, "error", err)
					if c.Server.Preload {
						return err
					}
					continue
				}
				if !pool.Add(e) {
					return errors.Join(server.ErrUnavailable, e.Close())
				}
				log.Info("replica ready", "replica", i, "seconds", time.Since(start).Seconds(), "runtime", e.Initialization().RuntimeVersion, "provider", c.Runtime.Provider)
			}
			return nil
		}
		if c.Server.Preload {
			if err := load(); err != nil {
				return errors.Join(err, pool.Close(context.Background()))
			}
		}
		listener, err := net.Listen("tcp", c.Server.Listen)
		if err != nil {
			return errors.Join(err, pool.Close(context.Background()))
		}
		loaded := make(chan struct{})
		if c.Server.Preload {
			close(loaded)
		} else {
			go func() {
				defer close(loaded)
				if err := load(); err != nil {
					log.Error("replica loader stopped", "error", err)
				}
			}()
		}
		httpServer := &http.Server{Handler: server.New(c, pool, decoder, log), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: c.Server.UploadTimeout, IdleTimeout: time.Minute, MaxHeaderBytes: 16 * 1024}
		served := make(chan error, 1)
		go func() { served <- httpServer.Serve(listener) }()
		log.Info("listening", "address", listener.Addr().String(), "model", c.Model.ID)
		var serveErr error
		select {
		case <-cmd.Context().Done():
		case serveErr = <-served:
		}
		pool.Stop()
		cancelLoad()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), c.Server.ShutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			// Returning exits the CLI process. Do not destroy sessions under an
			// uninterruptible native run after the drain deadline.
			return fmt.Errorf("shutdown deadline exceeded; native sessions retained until process exit: %w", err)
		}
		select {
		case <-loaded:
		case <-shutdownCtx.Done():
			return fmt.Errorf("replica loader did not stop: %w", shutdownCtx.Err())
		}
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(serveErr, pool.Close(shutdownCtx))
	}}
}
