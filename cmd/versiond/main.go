// Command versiond serves one endpoint at four versions at once.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/usmanvirk/api-versioning/internal/handler"
)

// store is a stand in for the real one. The point of the service is the
// versioning, so there is one listing and it is enough to see every shape.
type store struct{}

func (store) Get(id string) (handler.Listing, bool) {
	if id != "lst_1" {
		return handler.Listing{}, false
	}
	orig := handler.Money{Cents: 62900, Currency: "EUR"}
	return handler.Listing{
		ID:       "lst_1",
		Title:    "iPhone 13 Pro 256 GB Graphite",
		Grade:    "excellent",
		Price:    handler.Money{Cents: 48900, Currency: "EUR"},
		Original: &orig,
		Images: []handler.Image{
			{URL: "https://img.example.com/a/720.avif", Width: 720},
			{URL: "https://img.example.com/b/720.avif", Width: 720},
		},
		Stock:    handler.Stock{Available: true, Count: 2},
		Seller:   handler.Seller{ID: "sel_9", Name: "Renewed Electronics GmbH", Rating: 4.7},
		Warranty: handler.Warranty{Months: 12},
		Battery:  93,
	}, true
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	reg, err := handler.Versions(time.Now())
	if err != nil {
		log.Error("version registry", "err", err)
		os.Exit(1)
	}
	chain, err := handler.Chain()
	if err != nil {
		log.Error("migration chain", "err", err)
		os.Exit(1)
	}

	// This is the check worth having at startup. A missing migration means a
	// request for that version is answered with a newer shape and nobody
	// finds out until a client reports it, so the service refuses to start.
	h, err := handler.New(reg, chain, store{}, log)
	if err != nil {
		log.Error("refusing to start", "err", err)
		os.Exit(1)
	}

	for _, v := range reg.Live() {
		s, _ := reg.Spec(v)
		attrs := []any{"version", v.String(), "status", string(s.Status)}
		if !s.SunsetOn.IsZero() {
			attrs = append(attrs, "sunset", s.SunsetOn.Format("2006-01-02"))
		}
		log.Info("speaking", attrs...)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           h.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Info("listening", "addr", *addr, "current", reg.Current().String(), "default", reg.Default().String())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("serve", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
}
