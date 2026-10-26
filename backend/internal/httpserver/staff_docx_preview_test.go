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

func previewDOCXBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	part, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, previewDOCXXML); err != nil {
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
