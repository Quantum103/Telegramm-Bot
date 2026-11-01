package httpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

const (
	readinessTimeout       = 2 * time.Second
	maxStaffLoginBodyBytes = 8 << 10
	maxStaffAdminBodyBytes = 16 << 10
	staffSessionCookieName = "staff_session"
	staffCSRFCookieName    = "staff_csrf"
)

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

type StaffAuthAPI interface {
	Login(context.Context, string, string) (service.StaffLoginResult, error)
	AuthenticateSession(context.Context, string) (domain.StaffSessionIdentity, error)
	Logout(context.Context, string) error
}

type StaffAuthoringAPI interface {
	ListCoursesForStaff(context.Context) ([]domain.Course, error)
	ListLessonsForStaffByCourseID(context.Context, int64) ([]domain.StaffLesson, error)
	ListQuestionsForStaffVersion(context.Context, int64) ([]domain.LessonQuestion, error)
	CreateDraftCourse(context.Context, domain.CreateCourseParams) (domain.Course, error)
	CreateDraftLesson(context.Context, domain.CreateLessonParams) (domain.Lesson, error)
	UpdateDraftLessonVersion(context.Context, int64, string, *string, int64) error
	CreateDraftRevision(context.Context, int64, int64) (domain.Lesson, error)
	CreateQuestion(context.Context, domain.CreateQuestionParams) (domain.LessonQuestion, error)
	ImportDraftLesson(context.Context, domain.ImportDraftLessonParams) (domain.Lesson, error)
	UpdateQuestion(context.Context, int64, domain.CreateQuestionParams, int64) (domain.LessonQuestion, error)
	PublishLessonVersion(context.Context, int64, int64) error
	PublishCourse(context.Context, int64, int64) error
}

type staffLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type staffCoursesResponse struct {
	Courses []domain.Course `json:"courses"`
}

type staffLessonsResponse struct {
	Lessons []domain.StaffLesson `json:"lessons"`
}

type staffQuestionsResponse struct {
	Questions []staffQuestionResponse `json:"questions"`
}

type staffQuestionResponse struct {
	ID                  int64                         `json:"id"`
	LessonVersionID     int64                         `json:"lesson_version_id"`
	QuestionNumber      int                           `json:"question_number"`
	Prompt              string                        `json:"prompt"`
	ExpectedAnswer      string                        `json:"expected_answer"`
	AnswerType          string                        `json:"answer_type"`
	ExpectedExplanation *string                       `json:"expected_explanation,omitempty"`
	Note                *string                       `json:"note,omitempty"`
	Criteria            []domain.QuestionCriterion    `json:"criteria"`
	Options             []staffQuestionOptionResponse `json:"options"`
	CommonMistakes      []domain.CommonMistake        `json:"common_mistakes"`
	BiblePassages       []domain.BiblePassage         `json:"bible_passages"`
	CreatedAt           time.Time                     `json:"created_at"`
	UpdatedAt           time.Time                     `json:"updated_at"`
}

type staffQuestionOptionResponse struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Text      string `json:"text"`
	IsCorrect bool   `json:"is_correct"`
}

type staffCreateDraftCourseRequest struct {
	Slug        string  `json:"slug"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
}

type staffCreateDraftLessonRequest struct {
	Title       string  `json:"title"`
	Description *string `json:"description"`
}

type staffUpdateDraftLessonVersionRequest struct {
	Title       string  `json:"title"`
	Description *string `json:"description"`
}

type staffCreateDraftRevisionRequest struct{}

type staffPublishLessonVersionRequest struct{}

type staffPublishCourseRequest struct{}

type staffDraftRevisionResponse struct {
	ID              int64   `json:"id"`
	CourseID        int64   `json:"course_id"`
	CourseTitle     string  `json:"course_title"`
	Position        int     `json:"position"`
	LessonVersionID int64   `json:"lesson_version_id"`
	VersionNumber   int     `json:"version_number"`
	VersionStatus   string  `json:"version_status"`
	Title           string  `json:"title"`
	Description     *string `json:"description,omitempty"`
	QuestionCount   int     `json:"question_count"`
}

type staffCreateQuestionRequest struct {
	QuestionNumber      int                             `json:"question_number"`
	Prompt              string                          `json:"prompt"`
	ExpectedAnswer      string                          `json:"expected_answer"`
	AnswerType          string                          `json:"answer_type"`
	ExpectedExplanation *string                         `json:"expected_explanation"`
	Note                *string                         `json:"note"`
	Criteria            []domain.QuestionCriterionInput `json:"criteria"`
	Options             []domain.QuestionOptionInput    `json:"options"`
	CommonMistakes      []domain.CommonMistake          `json:"common_mistakes"`
	BiblePassages       []domain.BiblePassageInput      `json:"bible_passages"`
}

type staffCreatedQuestionResponse struct {
	ID              int64     `json:"id"`
	LessonVersionID int64     `json:"lesson_version_id"`
	QuestionNumber  int       `json:"question_number"`
	AnswerType      string    `json:"answer_type"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type staffUpdatedQuestionResponse struct {
	ID              int64     `json:"id"`
	LessonVersionID int64     `json:"lesson_version_id"`
	QuestionNumber  int       `json:"question_number"`
	AnswerType      string    `json:"answer_type"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type staffIdentityResponse struct {
	ID                 int64    `json:"id"`
	DisplayName        string   `json:"display_name"`
	Email              string   `json:"email"`
	Roles              []string `json:"roles"`
	MustChangePassword bool     `json:"must_change_password"`
}

type staffLoginResponse struct {
	Staff staffIdentityResponse `json:"staff"`
}

type staffMeResponse struct {
	Staff     staffIdentityResponse `json:"staff"`
	ExpiresAt time.Time             `json:"expires_at"`
}

type staffSessionContextKey struct{}

type healthResponse struct {
	Status string `json:"status"`
}

type handlers struct {
	database      databasePinger
	logger        *slog.Logger
	catalog       CatalogAPI
	staffAuth     StaffAuthAPI
	authoring     StaffAuthoringAPI
	management    StaffManagementAPI
	conversations ConversationAPI
	staffTelegram StaffTelegramAPI
	cookieSecure  bool
	loginLimiter  StaffLoginRateLimiter
}

// NewHandler собирает публичные маршруты и сохраняет обратную совместимость тестов.
func NewHandler(database databasePinger, logger *slog.Logger, catalogs ...CatalogAPI) http.Handler {
	var catalog CatalogAPI
	if len(catalogs) > 0 {
		catalog = catalogs[0]
	}
	return newHandler(database, logger, catalog, nil, nil, false, newStaffLoginLimiter())
}

// NewHandlerWithStaffAuth adds staff login/logout endpoints. Production should keep cookieSecure enabled.
func NewHandlerWithStaffAuth(database databasePinger, logger *slog.Logger, catalog CatalogAPI, staffAuth StaffAuthAPI, cookieSecure bool) http.Handler {
	return newHandler(database, logger, catalog, staffAuth, nil, cookieSecure, newStaffLoginLimiter())
}

// NewHandlerWithStaffAuthAndLoginLimiter configures staff authentication with a shared login limiter.
func NewHandlerWithStaffAuthAndLoginLimiter(database databasePinger, logger *slog.Logger, catalog CatalogAPI, staffAuth StaffAuthAPI, limiter StaffLoginRateLimiter, cookieSecure bool) http.Handler {
	return newHandler(database, logger, catalog, staffAuth, nil, cookieSecure, limiter)
}

// NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter enables the first protected staff authoring operation.
func NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(database databasePinger, logger *slog.Logger, catalog CatalogAPI, staffAuth StaffAuthAPI, authoring StaffAuthoringAPI, limiter StaffLoginRateLimiter, cookieSecure bool) http.Handler {
	return newHandler(database, logger, catalog, staffAuth, authoring, cookieSecure, limiter)
}

func newHandler(database databasePinger, logger *slog.Logger, catalog CatalogAPI, staffAuth StaffAuthAPI, authoring StaffAuthoringAPI, cookieSecure bool, limiter StaffLoginRateLimiter) http.Handler {
	return newHandlerWithManagement(database, logger, catalog, staffAuth, authoring, nil, cookieSecure, limiter)
}

// NewHandlerWithStaffAuthAndAuthoringAndManagementAndLoginLimiterAndConversations configures the protected curator conversation API.
func NewHandlerWithStaffAuthAndAuthoringAndManagementAndLoginLimiterAndConversations(database databasePinger, logger *slog.Logger, catalog CatalogAPI, staffAuth StaffAuthAPI, authoring StaffAuthoringAPI, management StaffManagementAPI, conversations ConversationAPI, limiter StaffLoginRateLimiter, cookieSecure bool) http.Handler {
	return newHandlerWithManagementAndConversations(database, logger, catalog, staffAuth, authoring, management, conversations, cookieSecure, limiter)
}

// NewHandlerWithStaffAuthAndAuthoringAndManagementAndLoginLimiterAndConversationsAndTelegram adds the curator Telegram endpoints.
func NewHandlerWithStaffAuthAndAuthoringAndManagementAndLoginLimiterAndConversationsAndTelegram(database databasePinger, logger *slog.Logger, catalog CatalogAPI, staffAuth StaffAuthAPI, authoring StaffAuthoringAPI, management StaffManagementAPI, conversations ConversationAPI, staffTelegram StaffTelegramAPI, limiter StaffLoginRateLimiter, cookieSecure bool) http.Handler {
	return newHandlerWithManagementAndConversationsAndTelegram(database, logger, catalog, staffAuth, authoring, management, conversations, staffTelegram, cookieSecure, limiter)
}

// NewHandlerWithStaffAuthAndAuthoringAndManagementAndLoginLimiter configures the production staff API.
func NewHandlerWithStaffAuthAndAuthoringAndManagementAndLoginLimiter(database databasePinger, logger *slog.Logger, catalog CatalogAPI, staffAuth StaffAuthAPI, authoring StaffAuthoringAPI, management StaffManagementAPI, limiter StaffLoginRateLimiter, cookieSecure bool) http.Handler {
	return newHandlerWithManagement(database, logger, catalog, staffAuth, authoring, management, cookieSecure, limiter)
}

func newHandlerWithManagement(database databasePinger, logger *slog.Logger, catalog CatalogAPI, staffAuth StaffAuthAPI, authoring StaffAuthoringAPI, management StaffManagementAPI, cookieSecure bool, limiter StaffLoginRateLimiter) http.Handler {
	return newHandlerWithManagementAndConversations(database, logger, catalog, staffAuth, authoring, management, nil, cookieSecure, limiter)
}

func newHandlerWithManagementAndConversations(database databasePinger, logger *slog.Logger, catalog CatalogAPI, staffAuth StaffAuthAPI, authoring StaffAuthoringAPI, management StaffManagementAPI, conversations ConversationAPI, cookieSecure bool, limiter StaffLoginRateLimiter) http.Handler {
	return newHandlerWithManagementAndConversationsAndTelegram(database, logger, catalog, staffAuth, authoring, management, conversations, nil, cookieSecure, limiter)
}

func newHandlerWithManagementAndConversationsAndTelegram(database databasePinger, logger *slog.Logger, catalog CatalogAPI, staffAuth StaffAuthAPI, authoring StaffAuthoringAPI, management StaffManagementAPI, conversations ConversationAPI, staffTelegram StaffTelegramAPI, cookieSecure bool, limiter StaffLoginRateLimiter) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if limiter == nil {
		limiter = newStaffLoginLimiter()
	}
	h := handlers{
		database: database, logger: logger, catalog: catalog,
		staffAuth: staffAuth, authoring: authoring, management: management, conversations: conversations, staffTelegram: staffTelegram, cookieSecure: cookieSecure, loginLimiter: limiter,
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
	if h.staffAuth != nil {
		mux.HandleFunc("POST /api/v1/staff/auth/login", h.staffLogin)
		mux.Handle("GET /api/v1/staff/auth/me", h.staffSessionMiddleware(http.HandlerFunc(h.staffMe)))
		mux.Handle("POST /api/v1/staff/auth/password", h.staffSessionMiddleware(http.HandlerFunc(h.staffChangePassword)))
		mux.HandleFunc("POST /api/v1/staff/auth/logout", h.staffLogout)
		// All future staff data routes must be registered on this subrouter.
		// The whole namespace is protected, so route handlers cannot accidentally bypass auth/CSRF.
		staffAdminMux := http.NewServeMux()
		if h.authoring != nil {
			staffAdminMux.HandleFunc("POST /api/v1/staff/admin/import/docx/preview", h.staffPreviewDOCX)
			staffAdminMux.HandleFunc("POST /api/v1/staff/admin/courses/{courseID}/import/docx", h.staffImportDOCX)
			staffAdminMux.HandleFunc("POST /api/v1/staff/admin/staff/{staffID}/roles", h.staffAssignRole)
			staffAdminMux.HandleFunc("DELETE /api/v1/staff/admin/staff/{staffID}/roles/{roleCode}", h.staffRevokeRole)
			staffAdminMux.HandleFunc("GET /api/v1/staff/admin/courses", h.staffListCourses)
			staffAdminMux.HandleFunc("GET /api/v1/staff/admin/courses/{courseID}/lessons", h.staffListLessonsForCourse)
			staffAdminMux.HandleFunc("GET /api/v1/staff/admin/lesson-versions/{versionID}/questions", h.staffListQuestionsForVersion)
			staffAdminMux.HandleFunc("POST /api/v1/staff/admin/courses", h.staffCreateDraftCourse)
			staffAdminMux.HandleFunc("POST /api/v1/staff/admin/courses/{courseID}/lessons", h.staffCreateDraftLesson)
			staffAdminMux.HandleFunc("PUT /api/v1/staff/admin/lesson-versions/{versionID}", h.staffUpdateDraftLessonVersion)
			staffAdminMux.HandleFunc("POST /api/v1/staff/admin/courses/{courseID}/publish", h.staffPublishCourse)
			staffAdminMux.HandleFunc("POST /api/v1/staff/admin/lessons/{lessonID}/revisions", h.staffCreateDraftRevision)
			staffAdminMux.HandleFunc("POST /api/v1/staff/admin/lesson-versions/{versionID}/questions", h.staffCreateQuestion)
			staffAdminMux.HandleFunc("PUT /api/v1/staff/admin/lesson-versions/{versionID}/questions/{questionID}", h.staffUpdateQuestion)
			staffAdminMux.HandleFunc("POST /api/v1/staff/admin/lesson-versions/{versionID}/publish", h.staffPublishLessonVersion)
		}
		if h.management != nil {
			staffAdminMux.HandleFunc("GET /api/v1/staff/admin/staff", h.staffListAccounts)
			staffAdminMux.HandleFunc("POST /api/v1/staff/admin/staff", h.staffCreateAccount)
			staffAdminMux.HandleFunc("PATCH /api/v1/staff/admin/staff/{staffID}/status", h.staffSetAccountStatus)
			staffAdminMux.HandleFunc("POST /api/v1/staff/admin/staff/{staffID}/reset-password", h.staffResetPassword)
			staffAdminMux.HandleFunc("GET /api/v1/staff/admin/students", h.staffListStudents)
			staffAdminMux.HandleFunc("PUT /api/v1/staff/admin/students/{studentID}/curator", h.staffAssignStudentCurator)
			staffAdminMux.HandleFunc("DELETE /api/v1/staff/admin/students/{studentID}/curator", h.staffEndStudentCuratorAssignment)
		}
		mux.Handle("/api/v1/staff/admin/", h.staffAdminMiddleware(staffAdminMux))
		if h.conversations != nil {
			staffConversationMux := http.NewServeMux()
			staffConversationMux.HandleFunc("GET /api/v1/staff/conversations", h.staffListConversations)
			staffConversationMux.HandleFunc("GET /api/v1/staff/conversations/unread", h.staffConversationUnreadCount)
			staffConversationMux.HandleFunc("GET /api/v1/staff/conversations/events", h.staffConversationEvents)
			staffConversationMux.HandleFunc("GET /api/v1/staff/conversations/{conversationID}/messages", h.staffListConversationMessages)
			staffConversationMux.HandleFunc("POST /api/v1/staff/conversations/{conversationID}/messages", h.staffSendConversationMessage)
			staffConversationMux.HandleFunc("POST /api/v1/staff/conversations/{conversationID}/read", h.staffMarkConversationRead)
			protectedConversations := h.staffCuratorMiddleware(staffConversationMux)
			mux.Handle("/api/v1/staff/conversations", protectedConversations)
			mux.Handle("/api/v1/staff/conversations/", protectedConversations)
		}
		if h.staffTelegram != nil {
			staffTelegramMux := http.NewServeMux()
			staffTelegramMux.HandleFunc("GET /api/v1/staff/telegram/status", h.staffTelegramStatus)
			staffTelegramMux.HandleFunc("GET /api/v1/staff/telegram/notifications", h.staffTelegramNotifications)
			staffTelegramMux.HandleFunc("GET /api/v1/staff/telegram/preferences", h.staffTelegramPreferences)
			staffTelegramMux.HandleFunc("PUT /api/v1/staff/telegram/preferences", h.staffTelegramPreferences)
			staffTelegramMux.HandleFunc("POST /api/v1/staff/telegram/link-token", h.staffTelegramLinkToken)
			mux.Handle("/api/v1/staff/telegram/", h.staffNotificationMiddleware(staffTelegramMux))
		}
	}
	return securityHeaders(mux)
}

func (h handlers) staffLogin(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, apiErrorResponse{Error: "origin_not_allowed"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}

	var input staffLoginRequest
	if err := decodeJSONBody(w, r, &input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
		}
		return
	}

	if h.loginLimiter == nil {
		h.logger.Error("ограничитель попыток входа не настроен")
		writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{Error: "temporarily_unavailable"})
		return
	}
	finish, retryAfter, allowed, limitErr := h.loginLimiter.Begin(r.Context(), loginClientIP(r), input.Email)
	if limitErr != nil {
		h.logger.Error("ограничитель попыток входа недоступен", "error", limitErr)
		writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{Error: "temporarily_unavailable"})
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", formatRetryAfter(retryAfter))
		writeJSON(w, http.StatusTooManyRequests, apiErrorResponse{Error: "rate_limited"})
		return
	}
	completed := false
	complete := func(outcome StaffLoginAttemptOutcome) bool {
		if completed {
			return true
		}
		completed = true
		if err := finish(outcome); err != nil {
			h.logger.Error("не удалось завершить учёт попытки входа", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{Error: "temporarily_unavailable"})
			return false
		}
		return true
	}
	defer func() {
		if !completed {
			if err := finish(LoginOutcomeNeutral); err != nil {
				h.logger.Error("не удалось освободить резерв попытки входа", "error", err)
			}
		}
	}()

	result, err := h.staffAuth.Login(r.Context(), input.Email, input.Password)
	if errors.Is(err, service.ErrInvalidStaffCredentials) {
		if !complete(LoginOutcomeFailure) {
			return
		}
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "invalid_credentials"})
		return
	}
	if err != nil {
		if !complete(LoginOutcomeNeutral) {
			return
		}
		h.logger.Error("ошибка входа сотрудника", "error", err)
		writeJSON(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal_error"})
		return
	}
	if result.SessionToken == "" || result.CSRFToken == "" || !result.ExpiresAt.After(time.Now()) {
		if !complete(LoginOutcomeNeutral) {
			return
		}
		h.logger.Error("сервис авторизации вернул неполные данные сессии")
		writeJSON(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal_error"})
		return
	}
	if !complete(LoginOutcomeSuccess) {
		return
	}

	setStaffAuthCookies(w, result.SessionToken, result.CSRFToken, result.ExpiresAt, h.cookieSecure)
	writeJSON(w, http.StatusOK, staffLoginResponse{Staff: staffIdentityResponse{
		ID: result.StaffID, DisplayName: result.DisplayName, Email: result.Email,
		Roles: result.Roles, MustChangePassword: result.MustChangePassword,
	}})
}

func (h handlers) staffListCourses(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	courses, err := h.authoring.ListCoursesForStaff(r.Context())
	if err != nil {
		h.writeAPIError(w, err, "получить список курсов для сотрудника")
		return
	}
	if courses == nil {
		courses = make([]domain.Course, 0)
	}
	writeJSON(w, http.StatusOK, staffCoursesResponse{Courses: courses})
}

func (h handlers) staffListLessonsForCourse(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	courseID, ok := parsePositiveID(w, r.PathValue("courseID"))
	if !ok {
		return
	}
	lessons, err := h.authoring.ListLessonsForStaffByCourseID(r.Context(), courseID)
	if err != nil {
		h.writeAPIError(w, err, "получить список уроков для сотрудника")
		return
	}
	if lessons == nil {
		lessons = make([]domain.StaffLesson, 0)
	}
	writeJSON(w, http.StatusOK, staffLessonsResponse{Lessons: lessons})
}

func (h handlers) staffListQuestionsForVersion(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	versionID, ok := parsePositiveID(w, r.PathValue("versionID"))
	if !ok {
		return
	}
	questions, err := h.authoring.ListQuestionsForStaffVersion(r.Context(), versionID)
	if err != nil {
		h.writeAPIError(w, err, "получить вопросы версии для сотрудника")
		return
	}
	response := staffQuestionsResponse{Questions: make([]staffQuestionResponse, 0, len(questions))}
	for _, question := range questions {
		item := staffQuestionResponse{
			ID: question.ID, LessonVersionID: question.LessonVersionID,
			QuestionNumber: question.QuestionNumber, Prompt: question.Prompt,
			ExpectedAnswer: question.ExpectedAnswer, AnswerType: question.AnswerType,
			ExpectedExplanation: question.ExpectedExplanation, Note: question.Note,
			Criteria: question.Criteria, Options: make([]staffQuestionOptionResponse, 0, len(question.Options)),
			CommonMistakes: question.CommonMistakes, BiblePassages: question.BiblePassages,
			CreatedAt: question.CreatedAt, UpdatedAt: question.UpdatedAt,
		}
		if item.Criteria == nil {
			item.Criteria = make([]domain.QuestionCriterion, 0)
		}
		if item.CommonMistakes == nil {
			item.CommonMistakes = make([]domain.CommonMistake, 0)
		}
		if item.BiblePassages == nil {
			item.BiblePassages = make([]domain.BiblePassage, 0)
		}
		for _, option := range question.Options {
			item.Options = append(item.Options, staffQuestionOptionResponse{
				ID: option.ID, Key: option.Key, Text: option.Text, IsCorrect: option.IsCorrect,
			})
		}
		response.Questions = append(response.Questions, item)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h handlers) staffCreateDraftCourse(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input staffCreateDraftCourseRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
		}
		return
	}

	course, err := h.authoring.CreateDraftCourse(r.Context(), domain.CreateCourseParams{
		Slug: input.Slug, Title: input.Title, Description: input.Description,
		CreatedByStaffID: identity.StaffID,
	})
	if err != nil {
		h.writeAPIError(w, err, "создать черновик курса")
		return
	}
	writeJSON(w, http.StatusCreated, course)
}

func (h handlers) staffCreateDraftLesson(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	courseID, ok := parsePositiveID(w, r.PathValue("courseID"))
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input staffCreateDraftLessonRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
		}
		return
	}

	lesson, err := h.authoring.CreateDraftLesson(r.Context(), domain.CreateLessonParams{
		CourseID: courseID, Title: input.Title, Description: input.Description,
		CreatedByStaffID: identity.StaffID,
	})
	if err != nil {
		h.writeAPIError(w, err, "создать черновик урока")
		return
	}
	writeJSON(w, http.StatusCreated, toLessonResponse(lesson))
}

func (h handlers) staffUpdateDraftLessonVersion(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	versionID, ok := parsePositiveID(w, r.PathValue("versionID"))
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input staffUpdateDraftLessonVersionRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
		}
		return
	}
	if err := h.authoring.UpdateDraftLessonVersion(
		r.Context(), versionID, input.Title, input.Description, identity.StaffID,
	); err != nil {
		h.writeAPIError(w, err, "обновить черновую версию урока")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handlers) staffCreateDraftRevision(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	lessonID, ok := parsePositiveID(w, r.PathValue("lessonID"))
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input staffCreateDraftRevisionRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
		}
		return
	}

	lesson, err := h.authoring.CreateDraftRevision(r.Context(), lessonID, identity.StaffID)
	if err != nil {
		h.writeAPIError(w, err, "создать черновую версию урока")
		return
	}
	writeJSON(w, http.StatusCreated, staffDraftRevisionResponse{
		ID: lesson.ID, CourseID: lesson.CourseID, CourseTitle: lesson.CourseTitle,
		Position: lesson.Position, LessonVersionID: lesson.LessonVersionID,
		VersionNumber: lesson.VersionNumber, VersionStatus: "draft", Title: lesson.Title,
		Description: lesson.Description, QuestionCount: lesson.QuestionCount,
	})
}

func (h handlers) staffCreateQuestion(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	versionID, ok := parsePositiveID(w, r.PathValue("versionID"))
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input staffCreateQuestionRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
		}
		return
	}

	question, err := h.authoring.CreateQuestion(r.Context(), domain.CreateQuestionParams{
		LessonVersionID: versionID,
		QuestionNumber:  input.QuestionNumber, Prompt: input.Prompt, ExpectedAnswer: input.ExpectedAnswer,
		AnswerType: input.AnswerType, ExpectedExplanation: input.ExpectedExplanation, Note: input.Note,
		Criteria: input.Criteria, Options: input.Options, CommonMistakes: input.CommonMistakes,
		BiblePassages: input.BiblePassages, CreatedByStaffID: identity.StaffID,
	})
	if err != nil {
		h.writeAPIError(w, err, "создать вопрос черновой версии урока")
		return
	}
	writeJSON(w, http.StatusCreated, staffCreatedQuestionResponse{
		ID: question.ID, LessonVersionID: question.LessonVersionID,
		QuestionNumber: question.QuestionNumber, AnswerType: question.AnswerType,
		CreatedAt: question.CreatedAt, UpdatedAt: question.UpdatedAt,
	})
}

func (h handlers) staffUpdateQuestion(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	versionID, ok := parsePositiveID(w, r.PathValue("versionID"))
	if !ok {
		return
	}
	questionID, ok := parsePositiveID(w, r.PathValue("questionID"))
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input staffCreateQuestionRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
		}
		return
	}

	question, err := h.authoring.UpdateQuestion(r.Context(), questionID, domain.CreateQuestionParams{
		LessonVersionID: versionID,
		QuestionNumber:  input.QuestionNumber, Prompt: input.Prompt, ExpectedAnswer: input.ExpectedAnswer,
		AnswerType: input.AnswerType, ExpectedExplanation: input.ExpectedExplanation, Note: input.Note,
		Criteria: input.Criteria, Options: input.Options, CommonMistakes: input.CommonMistakes,
		BiblePassages: input.BiblePassages,
	}, identity.StaffID)
	if err != nil {
		h.writeAPIError(w, err, "обновить вопрос черновой версии урока")
		return
	}
	writeJSON(w, http.StatusOK, staffUpdatedQuestionResponse{
		ID: question.ID, LessonVersionID: question.LessonVersionID,
		QuestionNumber: question.QuestionNumber, AnswerType: question.AnswerType,
		UpdatedAt: question.UpdatedAt,
	})
}

func (h handlers) staffPublishCourse(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	courseID, ok := parsePositiveID(w, r.PathValue("courseID"))
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input staffPublishCourseRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
		}
		return
	}

	if err := h.authoring.PublishCourse(r.Context(), courseID, identity.StaffID); err != nil {
		h.writeAPIError(w, err, "опубликовать курс")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handlers) staffPublishLessonVersion(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok || identity.StaffID <= 0 {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	versionID, ok := parsePositiveID(w, r.PathValue("versionID"))
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_json"})
		return
	}
	var input staffPublishLessonVersionRequest
	if err := decodeJSONBodyWithLimit(w, r, &input, maxStaffAdminBodyBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
		}
		return
	}

	if err := h.authoring.PublishLessonVersion(r.Context(), versionID, identity.StaffID); err != nil {
		h.writeAPIError(w, err, "опубликовать версию урока")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handlers) staffLogout(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, apiErrorResponse{Error: "origin_not_allowed"})
		return
	}

	sessionCookie, err := r.Cookie(staffSessionCookieName)
	if err != nil || sessionCookie.Value == "" {
		clearStaffAuthCookies(w, h.cookieSecure)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	identity, err := h.staffAuth.AuthenticateSession(r.Context(), sessionCookie.Value)
	if errors.Is(err, service.ErrInvalidStaffSession) {
		clearStaffAuthCookies(w, h.cookieSecure)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		h.logger.Error("ошибка проверки сессии при выходе сотрудника", "error", err)
		clearStaffAuthCookies(w, h.cookieSecure)
		writeJSON(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal_error"})
		return
	}
	if !csrfMatches(r, identity) {
		writeJSON(w, http.StatusForbidden, apiErrorResponse{Error: "csrf_token_invalid"})
		return
	}

	if err := h.staffAuth.Logout(r.Context(), sessionCookie.Value); err != nil {
		h.logger.Error("ошибка выхода сотрудника", "error", err)
		clearStaffAuthCookies(w, h.cookieSecure)
		writeJSON(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal_error"})
		return
	}
	clearStaffAuthCookies(w, h.cookieSecure)
	w.WriteHeader(http.StatusNoContent)
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, destination any) error {
	return decodeJSONBodyWithLimit(w, r, destination, maxStaffLoginBodyBytes)
}

func decodeJSONBodyWithLimit(w http.ResponseWriter, r *http.Request, destination any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values in request body")
		}
		return err
	}
	return nil
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func sameOriginOrNoOrigin(r *http.Request) bool {
	return r.Header.Get("Origin") == "" || sameOrigin(r)
}

func csrfMatches(r *http.Request, identity domain.StaffSessionIdentity) bool {
	csrfCookie, err := r.Cookie(staffCSRFCookieName)
	if err != nil || csrfCookie.Value == "" {
		return false
	}
	headerToken := r.Header.Get("X-CSRF-Token")
	if headerToken == "" || len(headerToken) > 128 || len(csrfCookie.Value) > 128 {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(csrfCookie.Value), []byte(headerToken)) != 1 {
		return false
	}
	digest := auth.HashToken(headerToken)
	return subtle.ConstantTimeCompare(digest[:], identity.CSRFTokenHash[:]) == 1
}

func isUnsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func staffHasRole(identity domain.StaffSessionIdentity, roleCode string) bool {
	for _, role := range identity.Roles {
		if role == roleCode {
			return true
		}
	}
	return false
}

// staffAdminMiddleware applies permissions for the whole /staff/admin/ namespace
// after normal session and CSRF validation. New routes therefore inherit
// default-deny for unassigned staff and administrator-only access for mutations.
func (h handlers) staffAdminMiddleware(next http.Handler) http.Handler {
	roleChecked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
		if !ok || identity.StaffID <= 0 {
			writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
			return
		}

		allowed := staffHasRole(identity, "administrator")
		if !isUnsafeMethod(r.Method) {
			allowed = allowed || staffHasRole(identity, "curator")
		}
		if !allowed {
			writeJSON(w, http.StatusForbidden, apiErrorResponse{Error: "insufficient_permissions"})
			return
		}
		next.ServeHTTP(w, r)
	})
	return h.staffSessionMiddleware(roleChecked)
}

func (h handlers) staffSessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setNoStoreHeaders(w)
		sessionCookie, err := r.Cookie(staffSessionCookieName)
		if err != nil || sessionCookie.Value == "" {
			writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
			return
		}

		identity, err := h.staffAuth.AuthenticateSession(r.Context(), sessionCookie.Value)
		if errors.Is(err, service.ErrInvalidStaffSession) {
			writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
			return
		}
		if err != nil {
			h.logger.Error("ошибка проверки сессии сотрудника", "error", err)
			writeJSON(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal_error"})
			return
		}
		if identity.StaffID <= 0 || identity.Email == "" || identity.ExpiresAt.IsZero() || !identity.ExpiresAt.After(time.Now()) {
			writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
			return
		}

		if isUnsafeMethod(r.Method) {
			if !sameOrigin(r) {
				writeJSON(w, http.StatusForbidden, apiErrorResponse{Error: "origin_not_allowed"})
				return
			}
			if !csrfMatches(r, identity) {
				writeJSON(w, http.StatusForbidden, apiErrorResponse{Error: "csrf_token_invalid"})
				return
			}
		}
		if identity.MustChangePassword &&
			r.URL.Path != "/api/v1/staff/auth/me" &&
			r.URL.Path != "/api/v1/staff/auth/password" {
			writeJSON(w, http.StatusForbidden, apiErrorResponse{Error: "password_change_required"})
			return
		}
		ctx := context.WithValue(r.Context(), staffSessionContextKey{}, identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h handlers) staffMe(w http.ResponseWriter, r *http.Request) {
	identity, ok := r.Context().Value(staffSessionContextKey{}).(domain.StaffSessionIdentity)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, staffMeResponse{
		Staff: staffIdentityResponse{
			ID: identity.StaffID, DisplayName: identity.DisplayName, Email: identity.Email,
			Roles: identity.Roles, MustChangePassword: identity.MustChangePassword,
		},
		ExpiresAt: identity.ExpiresAt,
	})
}

func setNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func setStaffAuthCookies(w http.ResponseWriter, sessionToken, csrfToken string, expiresAt time.Time, secure bool) {
	maxAge := int(time.Until(expiresAt).Seconds())
	w.Header().Add("Set-Cookie", (&http.Cookie{
		Name: staffSessionCookieName, Value: sessionToken, Path: "/",
		Expires: expiresAt, MaxAge: maxAge, HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode,
	}).String())
	w.Header().Add("Set-Cookie", (&http.Cookie{
		Name: staffCSRFCookieName, Value: csrfToken, Path: "/",
		Expires: expiresAt, MaxAge: maxAge, HttpOnly: false,
		Secure: secure, SameSite: http.SameSiteStrictMode,
	}).String())
}

func clearStaffAuthCookies(w http.ResponseWriter, secure bool) {
	expired := time.Unix(1, 0).UTC()
	w.Header().Add("Set-Cookie", (&http.Cookie{
		Name: staffSessionCookieName, Value: "", Path: "/",
		Expires: expired, MaxAge: -1, HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode,
	}).String())
	w.Header().Add("Set-Cookie", (&http.Cookie{
		Name: staffCSRFCookieName, Value: "", Path: "/",
		Expires: expired, MaxAge: -1, HttpOnly: false,
		Secure: secure, SameSite: http.SameSiteStrictMode,
	}).String())
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
	if h.loginLimiter != nil {
		if err := h.loginLimiter.Ping(ctx); err != nil {
			h.logger.Error("проверка готовности Redis-лимитера завершилась ошибкой", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
			return
		}
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
