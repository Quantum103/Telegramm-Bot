package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

const readinessTimeout = 2 * time.Second

type databasePinger interface {
	Ping(context.Context) error
}

type CatalogAPI interface {
	ListCourses(context.Context) ([]domain.Course, error)
	GetCourse(context.Context, int64) (domain.Course, error)
	ListCourseLessons(context.Context, int64) ([]domain.Lesson, error)
	GetLessonDetail(context.Context, int64) (domain.LessonDetail, error)
	GetLessonDetailInCourse(context.Context, int64, int64) (domain.LessonDetail, error)
}

type healthResponse struct {
	Status string `json:"status"`
}

type handlers struct {
	database databasePinger
	logger   *slog.Logger
	catalog  CatalogAPI
}

// NewHandler собирает маршруты HTTP и сохраняет обратную совместимость health-тестов.
func NewHandler(database databasePinger, logger *slog.Logger, catalogs ...CatalogAPI) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := handlers{database: database, logger: logger}
	if len(catalogs) > 0 {
		h.catalog = catalogs[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", h.live)
	mux.HandleFunc("GET /health/ready", h.ready)
	if h.catalog != nil {
		mux.HandleFunc("GET /api/v1/courses", h.listCourses)
		mux.HandleFunc("GET /api/v1/courses/{courseID}", h.getCourse)
		mux.HandleFunc("GET /api/v1/courses/{courseID}/lessons", h.listCourseLessons)
		mux.HandleFunc("GET /api/v1/courses/{courseID}/lessons/{lessonID}", h.getLessonInCourse)
		mux.HandleFunc("GET /api/v1/lessons/{lessonID}", h.getLesson)
	}
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

type apiErrorResponse struct {
	Error string `json:"error"`
}

type coursesResponse struct {
	Courses []courseResponse `json:"courses"`
}

type courseResponse struct {
	ID          int64      `json:"id"`
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Description *string    `json:"description,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	LessonCount int        `json:"lesson_count"`
}

type lessonsResponse struct {
	Lessons []lessonResponse `json:"lessons"`
}

type lessonResponse struct {
	ID              int64              `json:"id"`
	CourseID        int64              `json:"course_id"`
	CourseTitle     string             `json:"course_title"`
	Position        int                `json:"position"`
	LessonVersionID int64              `json:"lesson_version_id"`
	VersionNumber   int                `json:"version_number"`
	Title           string             `json:"title"`
	Description     *string            `json:"description,omitempty"`
	PublishedAt     *time.Time         `json:"published_at,omitempty"`
	QuestionCount   int                `json:"question_count"`
	Questions       []questionResponse `json:"questions"`
}

type questionResponse struct {
	ID             int64                  `json:"id"`
	QuestionNumber int                    `json:"question_number"`
	Prompt         string                 `json:"prompt"`
	AnswerType     string                 `json:"answer_type"`
	Options        []optionResponse       `json:"options"`
	BiblePassages  []biblePassageResponse `json:"bible_passages"`
}

type optionResponse struct {
	ID   int64  `json:"id"`
	Key  string `json:"key"`
	Text string `json:"text"`
}

type biblePassageResponse struct {
	ID              int64   `json:"id"`
	BookName        string  `json:"book_name"`
	Chapter         int     `json:"chapter"`
	StartVerse      int     `json:"start_verse"`
	EndVerse        int     `json:"end_verse"`
	TranslationName *string `json:"translation_name,omitempty"`
	PassageText     *string `json:"passage_text,omitempty"`
}

func (h handlers) listCourses(w http.ResponseWriter, r *http.Request) {
	courses, err := h.catalog.ListCourses(r.Context())
	if err != nil {
		h.writeAPIError(w, err, "получить каталог курсов")
		return
	}
	response := coursesResponse{Courses: make([]courseResponse, 0, len(courses))}
	for _, course := range courses {
		response.Courses = append(response.Courses, toCourseResponse(course))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h handlers) getCourse(w http.ResponseWriter, r *http.Request) {
	courseID, ok := parsePositiveID(w, r.PathValue("courseID"))
	if !ok {
		return
	}
	course, err := h.catalog.GetCourse(r.Context(), courseID)
	if err != nil {
		h.writeAPIError(w, err, "получить курс")
		return
	}
	writeJSON(w, http.StatusOK, toCourseResponse(course))
}

func (h handlers) listCourseLessons(w http.ResponseWriter, r *http.Request) {
	courseID, ok := parsePositiveID(w, r.PathValue("courseID"))
	if !ok {
		return
	}
	lessons, err := h.catalog.ListCourseLessons(r.Context(), courseID)
	if err != nil {
		h.writeAPIError(w, err, "получить уроки курса")
		return
	}
	response := lessonsResponse{Lessons: make([]lessonResponse, 0, len(lessons))}
	for _, lesson := range lessons {
		response.Lessons = append(response.Lessons, toLessonResponse(lesson))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h handlers) getLesson(w http.ResponseWriter, r *http.Request) {
	lessonID, ok := parsePositiveID(w, r.PathValue("lessonID"))
	if !ok {
		return
	}
	detail, err := h.catalog.GetLessonDetail(r.Context(), lessonID)
	if err != nil {
		h.writeAPIError(w, err, "получить урок")
		return
	}
	response := toLessonResponse(detail.Lesson)
	response.Questions = make([]questionResponse, 0, len(detail.Questions))
	for _, question := range detail.Questions {
		response.Questions = append(response.Questions, toQuestionResponse(question))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h handlers) getLessonInCourse(w http.ResponseWriter, r *http.Request) {
	courseID, ok := parsePositiveID(w, r.PathValue("courseID"))
	if !ok {
		return
	}
	lessonID, ok := parsePositiveID(w, r.PathValue("lessonID"))
	if !ok {
		return
	}
	detail, err := h.catalog.GetLessonDetailInCourse(r.Context(), courseID, lessonID)
	if err != nil {
		h.writeAPIError(w, err, "получить урок в рамках курса")
		return
	}
	response := toLessonResponse(detail.Lesson)
	response.Questions = make([]questionResponse, 0, len(detail.Questions))
	for _, question := range detail.Questions {
		response.Questions = append(response.Questions, toQuestionResponse(question))
	}
	writeJSON(w, http.StatusOK, response)
}

func toCourseResponse(course domain.Course) courseResponse {
	return courseResponse{
		ID: course.ID, Slug: course.Slug, Title: course.Title,
		Description: course.Description, PublishedAt: course.PublishedAt,
		LessonCount: course.LessonCount,
	}
}

func toLessonResponse(lesson domain.Lesson) lessonResponse {
	return lessonResponse{
		ID: lesson.ID, CourseID: lesson.CourseID, CourseTitle: lesson.CourseTitle,
		Position: lesson.Position, LessonVersionID: lesson.LessonVersionID,
		VersionNumber: lesson.VersionNumber, Title: lesson.Title,
		Description: lesson.Description, PublishedAt: lesson.PublishedAt,
		QuestionCount: lesson.QuestionCount, Questions: make([]questionResponse, 0),
	}
}

func toQuestionResponse(question domain.LessonQuestion) questionResponse {
	response := questionResponse{
		ID: question.ID, QuestionNumber: question.QuestionNumber,
		Prompt: question.Prompt, AnswerType: question.AnswerType,
		Options:       make([]optionResponse, 0, len(question.Options)),
		BiblePassages: make([]biblePassageResponse, 0, len(question.BiblePassages)),
	}
	for _, option := range question.Options {
		response.Options = append(response.Options, optionResponse{
			ID: option.ID, Key: option.Key, Text: option.Text,
		})
	}
	for _, passage := range question.BiblePassages {
		response.BiblePassages = append(response.BiblePassages, biblePassageResponse{
			ID: passage.ID, BookName: passage.BookName, Chapter: passage.Chapter,
			StartVerse: passage.StartVerse, EndVerse: passage.EndVerse,
			TranslationName: passage.TranslationName, PassageText: passage.PassageText,
		})
	}
	return response
}

func parsePositiveID(w http.ResponseWriter, raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_id"})
		return 0, false
	}
	return id, true
}

func (h handlers) writeAPIError(w http.ResponseWriter, err error, operation string) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_input"})
	case errors.Is(err, domain.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiErrorResponse{Error: "not_found"})
	case errors.Is(err, domain.ErrConflict):
		writeJSON(w, http.StatusConflict, apiErrorResponse{Error: "conflict"})
	case errors.Is(err, domain.ErrInvalidState):
		writeJSON(w, http.StatusConflict, apiErrorResponse{Error: "invalid_state"})
	default:
		h.logger.Error("ошибка API каталога", "operation", operation, "error", err)
		writeJSON(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal_error"})
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
