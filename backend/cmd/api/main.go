package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/config"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/database"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/httpserver"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/repository"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/telegrambot"
)

const tokenPlaceholder = "replace-with-your-telegram-bot-token"

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

	studentRepository := repository.NewStudentRepository(pool)
	catalogRepository := repository.NewCatalogRepository(pool)
	contentRepository := repository.NewContentRepository(pool)
	enrollmentRepository := repository.NewEnrollmentRepository(pool)
	answerRepository := repository.NewAnswerRepository(pool)

	studentService := service.NewStudentService(studentRepository)
	catalogService := service.NewCatalogService(catalogRepository, contentRepository)
	learningService := service.NewLearningService(
		studentRepository, catalogRepository, contentRepository,
		enrollmentRepository, answerRepository,
	)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpserver.NewHandler(pool, logger, catalogService),
		ReadTimeout:       cfg.HTTPReadTimeout,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	appCtx, cancelApp := context.WithCancel(signalCtx)
	defer cancelApp()

	serverErrors := make(chan error, 1)
	go func() {
		defer close(serverErrors)
		logger.Info("HTTP-сервер запускается", "address", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	token := strings.TrimSpace(cfg.TelegramBotToken)
	telegramEnabled := token != "" && token != tokenPlaceholder
	botResult := make(chan error, 1)
	if telegramEnabled {
		api := telegrambot.NewAPIClient(token)
		stateRepository := repository.NewTelegramDialogRepository(pool)
		handler := telegrambot.NewHandler(api, studentService, catalogService, learningService, stateRepository, logger)
		bot := telegrambot.NewBot(api, handler, logger)
		go func() {
			botResult <- bot.Run(appCtx)
		}()
	} else {
		logger.Info("Telegram-бот отключён: TELEGRAM_BOT_TOKEN не задан; HTTP API продолжает работу")
	}

	var runErr error
	botFinished := false
	select {
	case err, ok := <-serverErrors:
		if ok && err != nil {
			runErr = err
		}
	case err := <-botResult:
		botFinished = true
		if err != nil {
			runErr = err
		}
	case <-signalCtx.Done():
		logger.Info("получен сигнал завершения приложения")
	}

	cancelApp()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		if runErr == nil {
			runErr = err
		}
	}
	if telegramEnabled && !botFinished {
		select {
		case botErr := <-botResult:
			if runErr == nil && botErr != nil {
				runErr = botErr
			}
		case <-shutdownCtx.Done():
			if runErr == nil {
				runErr = errors.New("не удалось дождаться остановки Telegram-бота")
			}
		}
	}
	logger.Info("HTTP-сервер остановлен")
	return runErr
}
