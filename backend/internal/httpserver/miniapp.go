package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
)

const (
	miniAppInitDataHeader = "X-Telegram-Init-Data"
	miniAppMaxInitDataAge = 24 * time.Hour
	miniAppMaxBodyBytes   = 16 << 10
)

type MiniAppStudentRegistrar interface {
	EnsureByTelegramID(context.Context, domain.EnsureStudentParams) (domain.Student, error)
}

type MiniAppCabinetReader interface {
	GetStudentCabinet(context.Context, int64) (domain.StudentCabinet, error)
}

type MiniAppLearningAPI interface {
	StartCourse(context.Context, int64, int64, string) (domain.CourseEnrollmentProgress, error)
	ActiveCourse(context.Context, int64) (domain.CourseEnrollmentProgress, error)
	CourseLessons(context.Context, int64, int64) ([]domain.CourseEnrollmentLesson, error)
	StartLessonForCourse(context.Context, int64, int64, int64, int64) (domain.Progress, error)
	ListLessonQuestions(context.Context, int64, int64) (service.LessonQuestionsState, error)
	GetProgress(context.Context, int64, int64) (domain.Progress, error)
	OpenQuestion(context.Context, int64, int64, int64) (service.NavigationState, error)
	NextQuestion(context.Context, int64, int64) (service.NavigationState, error)
	PreviousQuestion(context.Context, int64, int64) (service.NavigationState, error)
	SubmitAnswer(context.Context, domain.AnswerSubmission) (service.SubmissionResult, error)
	CompleteLesson(context.Context, int64, int64) (domain.Enrollment, error)
}

// MiniAppDependencies reuses the same student, catalogue and learning services as the Telegram bot.
type MiniAppConversationAPI interface {
	GetOrCreateForStudent(context.Context, int64) (domain.Conversation, error)
	ListMessagesForStudent(context.Context, int64, int64, int64, int) ([]domain.ConversationMessage, error)
	CreateStudentMiniAppMessage(context.Context, domain.CreateStudentMiniAppConversationMessageParams) (domain.ConversationMessage, error)
	MarkReadForStudent(context.Context, int64, int64) (int64, error)
	UnreadCountForStudent(context.Context, int64) (int64, error)
}

type MiniAppDependencies struct {
	BotToken      string
	Students      MiniAppStudentRegistrar
	Cabinet       MiniAppCabinetReader
	Catalog       CatalogAPI
	Learning      MiniAppLearningAPI
	Conversations MiniAppConversationAPI
}

type miniAppHandler struct {
	deps MiniAppDependencies
}

type telegramWebAppUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type miniAppStudentResponse struct {
	DisplayName string `json:"display_name"`
}

type miniAppCabinetCourseResponse struct {
	CourseID             int64  `json:"course_id"`
	Title                string `json:"title"`
	Mode                 string `json:"mode"`
	Status               string `json:"status"`
	TotalLessons         int    `json:"total_lessons"`
	CompletedLessons     int    `json:"completed_lessons"`
	PendingReviews       int    `json:"pending_reviews"`
	NeedsRevisionAnswers int    `json:"needs_revision_answers"`
}

type miniAppCabinetResponse struct {
	RegisteredAt               time.Time                      `json:"registered_at"`
	CompletedCourses           int                            `json:"completed_courses"`
	CompletedLessons           int                            `json:"completed_lessons"`
	UnreadConversationMessages int                            `json:"unread_conversation_messages"`
	Courses                    []miniAppCabinetCourseResponse `json:"courses"`
}

type miniAppBootstrapResponse struct {
	Student      miniAppStudentResponse           `json:"student"`
	Courses      []domain.Course                  `json:"courses"`
	Cabinet      miniAppCabinetResponse           `json:"cabinet"`
	ActiveCourse *domain.CourseEnrollmentProgress `json:"active_course,omitempty"`
}

type miniAppCourseResponse struct {
	Course       domain.Course                    `json:"course"`
	Lessons      []domain.Lesson                  `json:"lessons"`
	ActiveCourse *domain.CourseEnrollmentProgress `json:"active_course,omitempty"`
}

type miniAppAnswerState struct {
	AnswerText        string    `json:"answer_text"`
	SelectedOptionIDs []int64   `json:"selected_option_ids"`
	ProcessingStatus  string    `json:"processing_status"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type miniAppReviewState struct {
	ProcessingStatus string  `json:"processing_status"`
	ReviewDecision   *string `json:"review_decision,omitempty"`
	ReviewFeedback   *string `json:"review_feedback,omitempty"`
}

type miniAppOption struct {
	ID   int64  `json:"id"`
	Key  string `json:"key"`
	Text string `json:"text"`
}

type miniAppQuestion struct {
	ID            int64                 `json:"id"`
	Number        int                   `json:"number"`
	Prompt        string                `json:"prompt"`
	AnswerType    string                `json:"answer_type"`
	Options       []miniAppOption       `json:"options"`
	BiblePassages []domain.BiblePassage `json:"bible_passages"`
	SavedAnswer   *miniAppAnswerState   `json:"saved_answer,omitempty"`
	LatestAttempt *miniAppReviewState   `json:"latest_attempt,omitempty"`
}

type miniAppQuestionResponse struct {
	Question *miniAppQuestion `json:"question"`
	Progress domain.Progress  `json:"progress"`
	AtEnd    bool             `json:"at_end"`
}

type miniAppLessonQuestionsResponse struct {
	Questions         []miniAppQuestion `json:"questions"`
	Progress          domain.Progress   `json:"progress"`
	LessonDescription *string           `json:"lesson_description,omitempty"`
	AtEnd             bool              `json:"at_end"`
}

type miniAppStartCourseRequest struct {
	Mode string `json:"mode"`
}

type miniAppAnswerRequest struct {
	AnswerText        string  `json:"answer_text"`
	SelectedOptionIDs []int64 `json:"selected_option_ids"`
	IdempotencyKey    string  `json:"idempotency_key"`
}

type miniAppNavigateRequest struct {
	Direction string `json:"direction"`
}

type miniAppCompleteResponse struct {
	Enrollment domain.Enrollment `json:"enrollment"`
}

type miniAppConversationInfo struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

type miniAppConversationMessage struct {
	ID          int64     `json:"id"`
	SenderType  string    `json:"sender_type"`
	MessageType string    `json:"message_type"`
	BodyText    *string   `json:"body_text,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type miniAppConversationResponse struct {
	Conversation miniAppConversationInfo      `json:"conversation"`
	Messages      []miniAppConversationMessage `json:"messages"`
	HasMore       bool                         `json:"has_more"`
	UnreadCount   int64                        `json:"unread_count"`
}

type miniAppConversationMessagesResponse struct {
	Messages []miniAppConversationMessage `json:"messages"`
	HasMore  bool                         `json:"has_more"`
}

type miniAppConversationMessageRequest struct {
	BodyText       string `json:"body_text"`
	IdempotencyKey string `json:"idempotency_key"`
}

func NewHandlerWithTelegramMiniApp(next http.Handler, deps MiniAppDependencies) http.Handler {
	mux := http.NewServeMux()
	h := &miniAppHandler{deps: deps}
	h.register(mux)
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux.Handle("/", next)
	return securityHeaders(mux)
}

func (h *miniAppHandler) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/miniapp/bootstrap", h.bootstrap)
	mux.HandleFunc("GET /api/v1/miniapp/conversation", h.studentConversation)
	mux.HandleFunc("GET /api/v1/miniapp/conversation/messages", h.studentConversationMessages)
	mux.HandleFunc("POST /api/v1/miniapp/conversation/messages", h.studentSendConversationMessage)
	mux.HandleFunc("GET /api/v1/miniapp/courses/{courseID}", h.course)
	mux.HandleFunc("POST /api/v1/miniapp/courses/{courseID}/start", h.startCourse)
	mux.HandleFunc("GET /api/v1/miniapp/course-runs/{runID}/lessons", h.courseRunLessons)
	mux.HandleFunc("POST /api/v1/miniapp/course-runs/{runID}/lessons/{lessonID}/start", h.startLesson)
	mux.HandleFunc("GET /api/v1/miniapp/lesson-enrollments/{enrollmentID}/questions/{questionID}", h.openQuestion)
	mux.HandleFunc("POST /api/v1/miniapp/lesson-enrollments/{enrollmentID}/questions/{questionID}/answer", h.submitAnswer)
	mux.HandleFunc("POST /api/v1/miniapp/lesson-enrollments/{enrollmentID}/questions/navigate", h.navigateQuestion)
	mux.HandleFunc("POST /api/v1/miniapp/lesson-enrollments/{enrollmentID}/complete", h.completeLesson)
}

func (h *miniAppHandler) registerStudent(w http.ResponseWriter, r *http.Request) (domain.Student, bool) {
	setNoStoreHeaders(w)
	if h.deps.BotToken == "" || h.deps.Students == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{Error: "mini_app_unavailable"})
		return domain.Student{}, false
	}

	initData := r.Header.Get(miniAppInitDataHeader)
	user, err := validateTelegramWebAppInitData(initData, h.deps.BotToken, time.Now())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiErrorResponse{Error: "telegram_auth_invalid"})
		return domain.Student{}, false
	}

	displayName := strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " "))
	if displayName == "" {
		displayName = "Ученик"
	}
	params := domain.EnsureStudentParams{TelegramID: user.ID, DisplayName: displayName}
	if username := strings.TrimSpace(user.Username); username != "" {
		params.TelegramUsername = &username
	}
	student, err := h.deps.Students.EnsureByTelegramID(r.Context(), params)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return domain.Student{}, false
	}
	return student, true
}

func (h *miniAppHandler) studentConversation(w http.ResponseWriter, r *http.Request) {
	student, ok := h.registerStudent(w, r)
	if !ok {
		return
	}
	if h.deps.Conversations == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{Error: "mini_app_unavailable"})
		return
	}
	conversation, err := h.deps.Conversations.GetOrCreateForStudent(r.Context(), student.ID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	if _, err := h.deps.Conversations.MarkReadForStudent(r.Context(), student.ID, conversation.ID); err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	messages, err := h.deps.Conversations.ListMessagesForStudent(r.Context(), student.ID, conversation.ID, 0, 51)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	hasMore := len(messages) > 50
	if hasMore {
		messages = messages[:50]
	}
	if messages == nil {
		messages = []domain.ConversationMessage{}
	}
	unread, err := h.deps.Conversations.UnreadCountForStudent(r.Context(), student.ID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	response := miniAppConversationResponse{
		Conversation: miniAppConversationInfo{ID: conversation.ID, Status: conversation.Status},
		Messages: miniAppConversationMessagesFromDomain(messages),
		HasMore: hasMore,
		UnreadCount: unread,
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *miniAppHandler) studentConversationMessages(w http.ResponseWriter, r *http.Request) {
	student, ok := h.registerStudent(w, r)
	if !ok {
		return
	}
	if h.deps.Conversations == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{Error: "mini_app_unavailable"})
		return
	}
	beforeID, err := parseNonNegativeQueryID(r.URL.Query().Get("before_message_id"))
	if err != nil || beforeID == 0 {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_before_message_id"})
		return
	}
	conversation, err := h.deps.Conversations.GetOrCreateForStudent(r.Context(), student.ID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	if _, err := h.deps.Conversations.MarkReadForStudent(r.Context(), student.ID, conversation.ID); err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	messages, err := h.deps.Conversations.ListMessagesForStudent(r.Context(), student.ID, conversation.ID, beforeID, 51)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	hasMore := len(messages) > 50
	if hasMore {
		messages = messages[:50]
	}
	if messages == nil {
		messages = []domain.ConversationMessage{}
	}
	writeJSON(w, http.StatusOK, miniAppConversationMessagesResponse{
		Messages: miniAppConversationMessagesFromDomain(messages),
		HasMore: hasMore,
	})
}

func (h *miniAppHandler) studentSendConversationMessage(w http.ResponseWriter, r *http.Request) {
	student, ok := h.registerStudent(w, r)
	if !ok {
		return
	}
	if h.deps.Conversations == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{Error: "mini_app_unavailable"})
		return
	}
	var input miniAppConversationMessageRequest
	if !decodeMiniAppJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" || len(input.IdempotencyKey) > 128 {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_message"})
		return
	}
	conversation, err := h.deps.Conversations.GetOrCreateForStudent(r.Context(), student.ID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	message, err := h.deps.Conversations.CreateStudentMiniAppMessage(r.Context(), domain.CreateStudentMiniAppConversationMessageParams{
		StudentID: student.ID,
		ConversationID: conversation.ID,
		ClientMessageID: input.IdempotencyKey,
		BodyText: input.BodyText,
	})
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, miniAppConversationMessageFromDomain(message))
}

func miniAppConversationMessagesFromDomain(messages []domain.ConversationMessage) []miniAppConversationMessage {
	result := make([]miniAppConversationMessage, 0, len(messages))
	for _, message := range messages {
		result = append(result, miniAppConversationMessageFromDomain(message))
	}
	return result
}

func miniAppConversationMessageFromDomain(message domain.ConversationMessage) miniAppConversationMessage {
	return miniAppConversationMessage{
		ID: message.ID,
		SenderType: message.SenderType,
		MessageType: message.MessageType,
		BodyText: message.BodyText,
		CreatedAt: message.CreatedAt,
	}
}

func (h *miniAppHandler) bootstrap(w http.ResponseWriter, r *http.Request) {
	student, ok := h.registerStudent(w, r)
	if !ok {
		return
	}
	if h.deps.Catalog == nil || h.deps.Cabinet == nil || h.deps.Learning == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiErrorResponse{Error: "mini_app_unavailable"})
		return
	}

	courses, err := h.deps.Catalog.ListCourses(r.Context())
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	if courses == nil {
		courses = make([]domain.Course, 0)
	}
	cabinet, err := h.deps.Cabinet.GetStudentCabinet(r.Context(), student.ID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	response := miniAppBootstrapResponse{
		Student: miniAppStudentResponse{DisplayName: student.DisplayName},
		Courses: courses,
		Cabinet: miniAppCabinetFromDomain(cabinet),
	}
	active, err := h.deps.Learning.ActiveCourse(r.Context(), student.ID)
	if err == nil {
		response.ActiveCourse = &active
	} else if !errors.Is(err, domain.ErrNotFound) {
		writeMiniAppDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *miniAppHandler) course(w http.ResponseWriter, r *http.Request) {
	student, ok := h.registerStudent(w, r)
	if !ok {
		return
	}
	courseID, ok := miniAppPathID(w, r, "courseID")
	if !ok {
		return
	}
	course, err := h.deps.Catalog.GetCourse(r.Context(), courseID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	lessons, err := h.deps.Catalog.ListCourseLessons(r.Context(), courseID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	if lessons == nil {
		lessons = make([]domain.Lesson, 0)
	}
	response := miniAppCourseResponse{Course: course, Lessons: lessons}
	if active, activeErr := h.deps.Learning.ActiveCourse(r.Context(), student.ID); activeErr == nil {
		response.ActiveCourse = &active
	} else if !errors.Is(activeErr, domain.ErrNotFound) {
		writeMiniAppDomainError(w, activeErr)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *miniAppHandler) startCourse(w http.ResponseWriter, r *http.Request) {
	student, ok := h.registerStudent(w, r)
	if !ok {
		return
	}
	courseID, ok := miniAppPathID(w, r, "courseID")
	if !ok {
		return
	}
	var input miniAppStartCourseRequest
	if !decodeMiniAppJSON(w, r, &input) {
		return
	}
	if input.Mode != "curator" && input.Mode != "ai" {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_learning_mode"})
		return
	}
	progress, err := h.deps.Learning.StartCourse(r.Context(), student.ID, courseID, input.Mode)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

func (h *miniAppHandler) courseRunLessons(w http.ResponseWriter, r *http.Request) {
	student, ok := h.registerStudent(w, r)
	if !ok {
		return
	}
	runID, ok := miniAppPathID(w, r, "runID")
	if !ok {
		return
	}
	lessons, err := h.deps.Learning.CourseLessons(r.Context(), student.ID, runID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	if lessons == nil {
		lessons = make([]domain.CourseEnrollmentLesson, 0)
	}
	writeJSON(w, http.StatusOK, map[string]any{"lessons": lessons})
}

func (h *miniAppHandler) startLesson(w http.ResponseWriter, r *http.Request) {
	student, ok := h.registerStudent(w, r)
	if !ok {
		return
	}
	runID, ok := miniAppPathID(w, r, "runID")
	if !ok {
		return
	}
	lessonID, ok := miniAppPathID(w, r, "lessonID")
	if !ok {
		return
	}
	active, err := h.deps.Learning.ActiveCourse(r.Context(), student.ID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	if active.Enrollment.ID != runID || active.Enrollment.Status != "in_progress" {
		writeMiniAppDomainError(w, domain.ErrInvalidState)
		return
	}
	progress, err := h.deps.Learning.StartLessonForCourse(
		r.Context(), student.ID, runID, active.Enrollment.CourseID, lessonID,
	)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	lessonState, err := h.deps.Learning.ListLessonQuestions(r.Context(), student.ID, progress.Enrollment.ID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	response := miniAppLessonQuestionsResponse{
		Progress:          lessonState.Progress,
		LessonDescription: lessonState.LessonDescription,
		Questions:         make([]miniAppQuestion, 0, len(lessonState.Questions)),
		AtEnd:             len(lessonState.Questions) == 0,
	}
	for _, state := range lessonState.Questions {
		response.Questions = append(response.Questions, miniAppQuestionFromState(state))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *miniAppHandler) openQuestion(w http.ResponseWriter, r *http.Request) {
	student, ok := h.registerStudent(w, r)
	if !ok {
		return
	}
	enrollmentID, ok := miniAppPathID(w, r, "enrollmentID")
	if !ok {
		return
	}
	questionID, ok := miniAppPathID(w, r, "questionID")
	if !ok {
		return
	}
	state, err := h.deps.Learning.OpenQuestion(r.Context(), student.ID, enrollmentID, questionID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, miniAppStateResponse(state, false))
}

func (h *miniAppHandler) submitAnswer(w http.ResponseWriter, r *http.Request) {
	student, ok := h.registerStudent(w, r)
	if !ok {
		return
	}
	enrollmentID, ok := miniAppPathID(w, r, "enrollmentID")
	if !ok {
		return
	}
	questionID, ok := miniAppPathID(w, r, "questionID")
	if !ok {
		return
	}
	var input miniAppAnswerRequest
	if !decodeMiniAppJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" || len(input.IdempotencyKey) > 128 || len(input.AnswerText) > 10000 {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_answer"})
		return
	}
	_, err := h.deps.Learning.SubmitAnswer(r.Context(), domain.AnswerSubmission{
		StudentID: student.ID, EnrollmentID: enrollmentID, QuestionID: questionID,
		AnswerText: input.AnswerText, SelectedOptionIDs: input.SelectedOptionIDs,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	state, err := h.deps.Learning.OpenQuestion(r.Context(), student.ID, enrollmentID, questionID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, miniAppStateResponse(state, false))
}

func (h *miniAppHandler) navigateQuestion(w http.ResponseWriter, r *http.Request) {
	student, ok := h.registerStudent(w, r)
	if !ok {
		return
	}
	enrollmentID, ok := miniAppPathID(w, r, "enrollmentID")
	if !ok {
		return
	}
	var input miniAppNavigateRequest
	if !decodeMiniAppJSON(w, r, &input) {
		return
	}

	var state service.NavigationState
	var err error
	switch input.Direction {
	case "next":
		state, err = h.deps.Learning.NextQuestion(r.Context(), student.ID, enrollmentID)
	case "previous":
		state, err = h.deps.Learning.PreviousQuestion(r.Context(), student.ID, enrollmentID)
	default:
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_direction"})
		return
	}
	if errors.Is(err, domain.ErrNoNextQuestion) {
		progress, progressErr := h.deps.Learning.GetProgress(r.Context(), student.ID, enrollmentID)
		if progressErr != nil {
			writeMiniAppDomainError(w, progressErr)
			return
		}
		writeJSON(w, http.StatusOK, miniAppQuestionResponse{Progress: progress, AtEnd: true})
		return
	}
	if errors.Is(err, domain.ErrNoPreviousQuestion) {
		progress, progressErr := h.deps.Learning.GetProgress(r.Context(), student.ID, enrollmentID)
		if progressErr != nil {
			writeMiniAppDomainError(w, progressErr)
			return
		}
		if progress.Enrollment.CurrentQuestionID == nil {
			writeMiniAppDomainError(w, domain.ErrInvalidState)
			return
		}
		state, err = h.deps.Learning.OpenQuestion(r.Context(), student.ID, enrollmentID, *progress.Enrollment.CurrentQuestionID)
	}
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, miniAppStateResponse(state, false))
}

func (h *miniAppHandler) completeLesson(w http.ResponseWriter, r *http.Request) {
	student, ok := h.registerStudent(w, r)
	if !ok {
		return
	}
	enrollmentID, ok := miniAppPathID(w, r, "enrollmentID")
	if !ok {
		return
	}
	if r.Body != nil {
		_ = r.Body.Close()
	}
	enrollment, err := h.deps.Learning.CompleteLesson(r.Context(), student.ID, enrollmentID)
	if err != nil {
		writeMiniAppDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, miniAppCompleteResponse{Enrollment: enrollment})
}

func miniAppCabinetFromDomain(cabinet domain.StudentCabinet) miniAppCabinetResponse {
	response := miniAppCabinetResponse{
		RegisteredAt:               cabinet.RegisteredAt,
		CompletedCourses:           cabinet.CompletedCourses,
		CompletedLessons:           cabinet.CompletedLessons,
		UnreadConversationMessages: cabinet.UnreadConversationMessages,
		Courses:                    make([]miniAppCabinetCourseResponse, 0, len(cabinet.Courses)),
	}
	for _, course := range cabinet.Courses {
		response.Courses = append(response.Courses, miniAppCabinetCourseResponse{
			CourseID:             course.CourseID,
			Title:                course.Title,
			Mode:                 course.Mode,
			Status:               course.Status,
			TotalLessons:         course.TotalLessons,
			CompletedLessons:     course.CompletedLessons,
			PendingReviews:       course.PendingReviews,
			NeedsRevisionAnswers: course.NeedsRevisionAnswers,
		})
	}
	return response
}

func miniAppQuestionFromState(state service.NavigationState) miniAppQuestion {
	question := miniAppQuestion{
		ID: state.Question.ID, Number: state.Question.QuestionNumber,
		Prompt: state.Question.Prompt, AnswerType: state.Question.AnswerType,
		Options:       make([]miniAppOption, 0, len(state.Question.Options)),
		BiblePassages: state.Question.BiblePassages,
	}
	for _, option := range state.Question.Options {
		question.Options = append(question.Options, miniAppOption{ID: option.ID, Key: option.Key, Text: option.Text})
	}
	if question.BiblePassages == nil {
		question.BiblePassages = make([]domain.BiblePassage, 0)
	}
	if state.SavedAnswer != nil {
		question.SavedAnswer = &miniAppAnswerState{
			AnswerText:        state.SavedAnswer.AnswerText,
			SelectedOptionIDs: state.SavedAnswer.SelectedOptionIDs,
			ProcessingStatus:  state.SavedAnswer.ProcessingStatus,
			UpdatedAt:         state.SavedAnswer.UpdatedAt,
		}
	}
	if state.LatestAttempt != nil {
		question.LatestAttempt = &miniAppReviewState{
			ProcessingStatus: state.LatestAttempt.ProcessingStatus,
			ReviewDecision:   state.LatestAttempt.ReviewDecision,
			ReviewFeedback:   state.LatestAttempt.ReviewFeedback,
		}
	}
	return question
}

func miniAppStateResponse(state service.NavigationState, atEnd bool) miniAppQuestionResponse {
	question := miniAppQuestionFromState(state)
	return miniAppQuestionResponse{Question: &question, Progress: state.Progress, AtEnd: atEnd}
}

func miniAppPathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	value, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || value <= 0 {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_id"})
		return 0, false
	}
	return value, true
}

func decodeMiniAppJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, miniAppMaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
		return false
	}
	return true
}

func writeMiniAppDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiErrorResponse{Error: "not_found"})
	case errors.Is(err, domain.ErrInvalidInput):
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_request"})
	case errors.Is(err, domain.ErrNoCurator):
		writeJSON(w, http.StatusConflict, apiErrorResponse{Error: "no_curator_available"})
	case errors.Is(err, domain.ErrActiveCourse):
		writeJSON(w, http.StatusConflict, apiErrorResponse{Error: "active_course_exists"})
	case errors.Is(err, domain.ErrLegacyProgress):
		writeJSON(w, http.StatusConflict, apiErrorResponse{Error: "legacy_progress"})
	case errors.Is(err, domain.ErrInvalidState), errors.Is(err, domain.ErrConflict):
		writeJSON(w, http.StatusConflict, apiErrorResponse{Error: "invalid_state"})
	default:
		writeJSON(w, http.StatusInternalServerError, apiErrorResponse{Error: "internal_error"})
	}
}

func validateTelegramWebAppInitData(raw, botToken string, now time.Time) (telegramWebAppUser, error) {
	if strings.TrimSpace(raw) == "" || len(raw) > 8<<10 || strings.TrimSpace(botToken) == "" {
		return telegramWebAppUser{}, errors.New("missing Telegram Web App data")
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return telegramWebAppUser{}, errors.New("invalid Telegram Web App data")
	}
	providedHash, err := hex.DecodeString(values.Get("hash"))
	if err != nil || len(providedHash) != sha256.Size {
		return telegramWebAppUser{}, errors.New("invalid Telegram Web App hash")
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "hash" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		items := values[key]
		if len(items) == 0 {
			pairs = append(pairs, key+"=")
			continue
		}
		pairs = append(pairs, key+"="+items[0])
	}
	dataCheckString := strings.Join(pairs, "\n")

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	check := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = check.Write([]byte(dataCheckString))
	if subtle.ConstantTimeCompare(check.Sum(nil), providedHash) != 1 {
		return telegramWebAppUser{}, errors.New("Telegram Web App hash mismatch")
	}

	authDate, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || authDate <= 0 {
		return telegramWebAppUser{}, errors.New("invalid Telegram Web App auth date")
	}
	issuedAt := time.Unix(authDate, 0)
	age := now.Sub(issuedAt)
	if age > miniAppMaxInitDataAge || age < -time.Minute {
		return telegramWebAppUser{}, errors.New("Telegram Web App data is expired or from the future")
	}

	var user telegramWebAppUser
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID <= 0 {
		return telegramWebAppUser{}, errors.New("invalid Telegram Web App user")
	}
	return user, nil
}
