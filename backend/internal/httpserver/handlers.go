package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

const readinessTimeout = 2 * time.Second

type databasePinger interface {
	Ping(context.Context) error
}

type healthResponse struct {
	Status string `json:"status"`
}

type handlers struct {
	database databasePinger
	logger   *slog.Logger
}

// NewHandler собирает HTTP-маршруты приложения.
func NewHandler(database databasePinger, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := handlers{database: database, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", h.live)
	mux.HandleFunc("GET /health/ready", h.ready)
	return mux
}

func (h handlers) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func (h handlers) ready(w http.ResponseWriter, r *http.Request) {
	if h.database == nil {
		h.logger.Error("проверка готовности невозможна: пул PostgreSQL не настроен")
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()
	if err := h.database.Ping(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			h.logger.Warn("проверка готовности PostgreSQL прервана", "error", err)
		} else {
			h.logger.Error("проверка готовности PostgreSQL завершилась ошибкой", "error", err)
		}
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func writeJSON(w http.ResponseWriter, status int, value healthResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
