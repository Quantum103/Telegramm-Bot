package main

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/Quantum103/Telegramm-Bot/backend/internal/telegrambot"
	"github.com/redis/go-redis/v9"
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

	redisClient := redis.NewClient(&redis.Options{
		Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB,
		DialTimeout: cfg.Redis.ConnectTimeout, ReadTimeout: 2 * time.Second,
		WriteTimeout: 2 * time.Second, PoolSize: 16, MinIdleConns: 2,
		PoolTimeout: time.Second, MaxRetries: -1,
	})
	redisCtx, cancelRedis := context.WithTimeout(context.Background(), cfg.Redis.ConnectTimeout)
	if err := redisClient.Ping(redisCtx).Err(); err != nil {
		cancelRedis()
		_ = redisClient.Close()
		return fmt.Errorf("подключиться к Redis для лимитера входа: %w", err)
	}
	cancelRedis()
	defer redisClient.Close()
	loginLimiter := httpserver.NewRedisStaffLoginLimiter(redisClient)

	studentRepository := repository.NewStudentRepository(pool)
	studentService := service.NewStudentService(studentRepository)
	catalogRepository := repository.NewCatalogRepository(pool)
	contentRepository := repository.NewContentRepository(pool)
	catalogService := service.NewCatalogService(catalogRepository, contentRepository)
	authoringService := service.NewAuthoringService(catalogRepository)
	learningService := service.NewLearningService(
		studentService,
		catalogRepository,
		contentRepository,
		repository.NewEnrollmentRepository(pool),
		repository.NewAnswerRepository(pool),
	)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpserver.NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(pool, logger, catalogService, service.NewStaffAuthService(repository.NewStaffAuthRepository(pool)), authoringService, loginLimiter, true),
		ReadTimeout:       cfg.HTTPReadTimeout,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	appCtx, cancelApp := context.WithCancel(signalCtx)
	defer cancelApp()

	componentErrors := make(chan error, 2)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		logger.Info("HTTP-сервер запускается", "address", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			componentErrors <- fmt.Errorf("HTTP-сервер: %w", err)
		}
	}()

	var botDone chan struct{}
	if cfg.TelegramBotToken == "" {
		logger.Info("Telegram-бот отключён: переменная TELEGRAM_BOT_TOKEN не задана")
	} else {
		botDone = make(chan struct{})
		bot := telegrambot.NewBot(telegrambot.NewHTTPClient(cfg.TelegramBotToken), studentService, catalogService, learningService, logger)
		go func() {
			defer close(botDone)
			if err := bot.Run(appCtx); err != nil {
				componentErrors <- fmt.Errorf("Telegram-бот: %w", err)
			}
		}()
		logger.Info("Telegram-бот запускает проверку подключения и long polling")
	}

	var componentErr error
	select {
	case componentErr = <-componentErrors:
		logger.Error("один из компонентов завершился с ошибкой", "error", componentErr)
	case <-signalCtx.Done():
		logger.Info("получен сигнал завершения")
	}

	cancelApp()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()

	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = server.Close()
	}

	select {
	case <-serverDone:
	case <-shutdownCtx.Done():
		if shutdownErr == nil {
			shutdownErr = shutdownCtx.Err()
		}
	}
	if botDone != nil {
		select {
		case <-botDone:
		case <-shutdownCtx.Done():
			if shutdownErr == nil {
				shutdownErr = shutdownCtx.Err()
			}
		}
	}
	if shutdownErr != nil {
		return fmt.Errorf("остановка приложения: %w", shutdownErr)
	}
	logger.Info("HTTP-сервер и Telegram-бот остановлены")
	return componentErr
}
