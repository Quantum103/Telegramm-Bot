package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/config"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/database"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/httpserver"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/repository"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("приложение завершилось с ошибкой", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	pool, err := database.Open(startupCtx, cfg.Database)
	cancelStartup()
	if err != nil {
		return err
	}
	defer pool.Close()

	catalogService := service.NewCatalogService(
		repository.NewCatalogRepository(pool),
		repository.NewContentRepository(pool),
	)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpserver.NewHandler(pool, logger, catalogService),
		ReadTimeout:       cfg.HTTPReadTimeout,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("HTTP-сервер запускается", "address", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
		close(serverErrors)
	}()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err, ok := <-serverErrors:
		if ok && err != nil {
			return err
		}
	case <-signalCtx.Done():
		logger.Info("получен сигнал завершения, останавливаем HTTP-сервер")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return err
	}
	logger.Info("HTTP-сервер остановлен")
	return nil
}
