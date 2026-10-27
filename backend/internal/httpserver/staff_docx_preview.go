package httpserver

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/docximport"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
)

const (
	maxDOCXUploadBytes       = 8 << 20
	maxDOCXMultipartOverhead = 64 << 10
	maxDOCXImportErrorRunes  = 300
)

type staffDOCXPreviewResponse struct {
	Text       string `json:"text"`
	Characters int    `json:"characters"`
}

type staffDOCXImportErrorResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

// staffPreviewDOCX returns extracted text only and never persists or publishes the upload.
func (h handlers) staffPreviewDOCX(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	document, ok := readStaffDOCXUpload(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, staffDOCXPreviewResponse{
		Text: document.Text, Characters: utf8.RuneCountInString(document.Text),
	})
}

// staffImportDOCX validates a Bible lesson and saves it as a draft in one transaction.
func (h handlers) staffImportDOCX(w http.ResponseWriter, r *http.Request) {
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
	document, ok := readStaffDOCXUpload(w, r)
	if !ok {
		return
	}
	parsed, err := docximport.ParseLesson(document.Text)
	if err != nil {
		detail := []rune(err.Error())
		if len(detail) > maxDOCXImportErrorRunes {
			detail = detail[:maxDOCXImportErrorRunes]
		}
		writeJSON(w, http.StatusBadRequest, staffDOCXImportErrorResponse{
			Error: "invalid_lesson_document", Detail: string(detail),
		})
		return
	}

	params := domain.ImportDraftLessonParams{
		CourseID: courseID, Title: parsed.Title, Description: parsed.Description,
		CreatedByStaffID: identity.StaffID,
		Questions:        make([]domain.ImportDraftQuestionParams, 0, len(parsed.Questions)),
	}
	for _, imported := range parsed.Questions {
		question := domain.ImportDraftQuestionParams{
			Number: imported.Number, Prompt: imported.Prompt, AnswerType: imported.AnswerType,
			ExpectedAnswer: imported.ExpectedAnswer, ExpectedExplanation: imported.ExpectedExplanation,
			Note: imported.Note, Options: make([]domain.ImportDraftOptionInput, 0, len(imported.Options)),
			BiblePassages: make([]domain.BiblePassageInput, 0, len(imported.BiblePassages)),
		}
		for _, option := range imported.Options {
			question.Options = append(question.Options, domain.ImportDraftOptionInput{
				Text: option.Text, IsCorrect: option.IsCorrect,
			})
		}
		for _, passage := range imported.BiblePassages {
			question.BiblePassages = append(question.BiblePassages, domain.BiblePassageInput{
				BookName: passage.BookName, Chapter: passage.Chapter,
				StartVerse: passage.StartVerse, EndVerse: passage.EndVerse,
				PassageText: passage.PassageText,
			})
		}
		params.Questions = append(params.Questions, question)
	}

	lesson, err := h.authoring.ImportDraftLesson(r.Context(), params)
	if err != nil {
		h.writeAPIError(w, err, "импортировать черновик урока")
		return
	}
	writeJSON(w, http.StatusCreated, toLessonResponse(lesson))
}

// readStaffDOCXUpload applies the bounded, single-file upload policy to preview and import.
func readStaffDOCXUpload(w http.ResponseWriter, r *http.Request) (docximport.Document, bool) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_multipart"})
		return docximport.Document{}, false
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxDOCXUploadBytes+maxDOCXMultipartOverhead)
	reader, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_multipart"})
		return docximport.Document{}, false
	}
	part, err := reader.NextPart()
	if err != nil {
		writeDOCXUploadError(w, err)
		return docximport.Document{}, false
	}
	if part.FormName() != "document" || part.FileName() == "" || !strings.EqualFold(filepath.Ext(part.FileName()), ".docx") {
		_ = part.Close()
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "docx_file_required"})
		return docximport.Document{}, false
	}
	raw, readErr := io.ReadAll(io.LimitReader(part, maxDOCXUploadBytes+1))
	closeErr := part.Close()
	if readErr != nil {
		writeDOCXUploadError(w, readErr)
		return docximport.Document{}, false
	}
	if closeErr != nil {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_multipart"})
		return docximport.Document{}, false
	}
	if len(raw) > maxDOCXUploadBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "file_too_large"})
		return docximport.Document{}, false
	}
	if _, err := reader.NextPart(); err != io.EOF {
		if err != nil {
			writeDOCXUploadError(w, err)
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "exactly_one_file_required"})
		}
		return docximport.Document{}, false
	}

	document, err := docximport.Parse(bytes.NewReader(raw))
	if err != nil {
		if errors.Is(err, docximport.ErrTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "file_too_large"})
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_docx"})
		}
		return docximport.Document{}, false
	}
	return document, true
}

func writeDOCXUploadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		return
	}
	writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_multipart"})
}
