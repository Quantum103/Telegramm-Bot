package httpserver

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/auth"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

const previewDOCXXML = `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Учебный материал</w:t></w:r></w:p></w:body></w:document>`

const importDOCXXML = `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Название урока: Урок импорта</w:t></w:r></w:p>
<w:p><w:r><w:t>Вводный текст: Вводная часть урока.</w:t></w:r></w:p>
<w:p><w:r><w:t>Вопрос 1: Что сказано в стихе?</w:t></w:r></w:p>
<w:p><w:r><w:t>[] Первый вариант</w:t></w:r></w:p>
<w:p><w:r><w:t>[правильный ответ] Верный вариант</w:t></w:r></w:p>
<w:p><w:r><w:t>Библейские ссылки: Иоанна 17:17</w:t></w:r></w:p>
<w:p><w:r><w:t>Текст Писания: Освяти их истиною Твоею; слово Твое есть истина.</w:t></w:r></w:p>
<w:p><w:r><w:t>Объяснение: Пояснение к ответу.</w:t></w:r></w:p>
<w:p><w:r><w:t>Пометка: Для ведущего.</w:t></w:r></w:p>
</w:body></w:document>`

func previewDOCXBytes(t *testing.T) []byte {
	return docxBytes(t, previewDOCXXML)
}

func docxBytes(t *testing.T, documentXML string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	part, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, documentXML); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func previewMultipart(t *testing.T, filename string, file []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("document", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(file); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

func TestStaffDOCXPreviewRequiresAdminAndReturnsTextWithoutPersistence(t *testing.T) {
	api := &fakeStaffAuthAPI{authIdentity: domain.StaffSessionIdentity{
		StaffID: 7, DisplayName: "Admin", Email: "admin@example.test",
		Roles: []string{"administrator"}, CSRFTokenHash: auth.HashToken("csrf-token"),
		ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(fakePinger{}, testLogger(), nil, api, &fakeStaffAuthoringAPI{}, nil, true)
	body, contentType := previewMultipart(t, "lesson.docx", previewDOCXBytes(t))
	request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/admin/import/docx/preview", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Origin", "https://admin.example.test")
	request.Header.Set("X-CSRF-Token", "csrf-token")
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "session"})
	request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: "csrf-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response staffDOCXPreviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Text != "Учебный материал" || response.Characters != len([]rune("Учебный материал")) {
		t.Fatalf("unexpected preview response: %+v", response)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("preview response must not be cacheable")
	}
}

func TestStaffDOCXPreviewRejectsCuratorAndInvalidFile(t *testing.T) {
	tests := []struct {
		name       string
		roles      []string
		filename   string
		content    []byte
		wantStatus int
	}{
		{name: "curator cannot upload", roles: []string{"curator"}, filename: "lesson.docx", content: []byte("anything"), wantStatus: http.StatusForbidden},
		{name: "wrong extension", roles: []string{"administrator"}, filename: "lesson.docm", content: []byte("anything"), wantStatus: http.StatusBadRequest},
		{name: "invalid docx", roles: []string{"administrator"}, filename: "lesson.docx", content: []byte("not a docx"), wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeStaffAuthAPI{authIdentity: domain.StaffSessionIdentity{
				StaffID: 7, Email: "staff@example.test", Roles: tt.roles,
				CSRFTokenHash: auth.HashToken("csrf-token"), ExpiresAt: time.Now().Add(time.Hour),
			}}
			handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(fakePinger{}, testLogger(), nil, api, &fakeStaffAuthoringAPI{}, nil, true)
			body, contentType := previewMultipart(t, tt.filename, tt.content)
			request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/admin/import/docx/preview", body)
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Origin", "https://admin.example.test")
			request.Header.Set("X-CSRF-Token", "csrf-token")
			request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "session"})
			request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: "csrf-token"})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestStaffDOCXPreviewRejectsOversizedUpload(t *testing.T) {
	api := &fakeStaffAuthAPI{authIdentity: domain.StaffSessionIdentity{
		StaffID: 7, Email: "admin@example.test", Roles: []string{"administrator"},
		CSRFTokenHash: auth.HashToken("csrf-token"), ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(fakePinger{}, testLogger(), nil, api, &fakeStaffAuthoringAPI{}, nil, true)
	body, contentType := previewMultipart(t, "large.docx", bytes.Repeat([]byte("x"), maxDOCXUploadBytes+1))
	request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/admin/import/docx/preview", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Origin", "https://admin.example.test")
	request.Header.Set("X-CSRF-Token", "csrf-token")
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "session"})
	request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: "csrf-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusRequestEntityTooLarge, recorder.Body.String())
	}
}

func TestStaffDOCXPreviewRequiresMultipartContentType(t *testing.T) {
	api := &fakeStaffAuthAPI{authIdentity: domain.StaffSessionIdentity{
		StaffID: 7, Email: "admin@example.test", Roles: []string{"administrator"},
		CSRFTokenHash: auth.HashToken("csrf-token"), ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(fakePinger{}, testLogger(), nil, api, &fakeStaffAuthoringAPI{}, nil, true)
	request := httptest.NewRequest(http.MethodPost, "https://admin.example.test/api/v1/staff/admin/import/docx/preview", strings.NewReader("payload"))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Origin", "https://admin.example.test")
	request.Header.Set("X-CSRF-Token", "csrf-token")
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "session"})
	request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: "csrf-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnsupportedMediaType)
	}
}

func TestStaffDOCXImportParsesLessonAndSavesDraft(t *testing.T) {
	api := &fakeStaffAuthAPI{authIdentity: domain.StaffSessionIdentity{
		StaffID: 7, DisplayName: "Admin", Email: "admin@example.test",
		Roles: []string{"administrator"}, CSRFTokenHash: auth.HashToken("csrf-token"),
		ExpiresAt: time.Now().Add(time.Hour),
	}}
	authoring := &fakeStaffAuthoringAPI{importLessonResult: domain.Lesson{
		ID: 41, CourseID: 12, LessonVersionID: 42, VersionNumber: 1,
		Title: "Урок импорта", QuestionCount: 1,
	}}
	handler := NewHandlerWithStaffAuthAndAuthoringAndLoginLimiter(
		fakePinger{}, testLogger(), nil, api, authoring, nil, true,
	)
	body, contentType := previewMultipart(t, "lesson.docx", docxBytes(t, importDOCXXML))
	request := httptest.NewRequest(
		http.MethodPost, "https://admin.example.test/api/v1/staff/admin/courses/12/import/docx", body,
	)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Origin", "https://admin.example.test")
	request.Header.Set("X-CSRF-Token", "csrf-token")
	request.AddCookie(&http.Cookie{Name: staffSessionCookieName, Value: "session"})
	request.AddCookie(&http.Cookie{Name: staffCSRFCookieName, Value: "csrf-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if authoring.importLessonCalls != 1 {
		t.Fatalf("ImportDraftLesson calls = %d, want 1", authoring.importLessonCalls)
	}
	got := authoring.importLessonParams
	if got.CourseID != 12 || got.CreatedByStaffID != 7 || got.Title != "Урок импорта" ||
		got.Description == nil || *got.Description != "Вводная часть урока." || len(got.Questions) != 1 {
		t.Fatalf("unexpected import params: %+v", got)
	}
	question := got.Questions[0]
	if question.Number != 1 || question.AnswerType != "single_choice" ||
		question.ExpectedAnswer != "" || question.Note == nil || *question.Note != "Для ведущего." ||
		question.ExpectedExplanation == nil || *question.ExpectedExplanation != "Пояснение к ответу." ||
		len(question.Options) != 2 || question.Options[0].IsCorrect || !question.Options[1].IsCorrect ||
		len(question.BiblePassages) != 1 || question.BiblePassages[0].BookName != "Иоанна" ||
		question.BiblePassages[0].Chapter != 17 || question.BiblePassages[0].StartVerse != 17 ||
		question.BiblePassages[0].EndVerse != 17 {
		t.Fatalf("unexpected imported question: %+v", question)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("import response must not be cacheable")
	}
}
