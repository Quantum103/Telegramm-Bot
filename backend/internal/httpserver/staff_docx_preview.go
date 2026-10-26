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
)

const (
	maxDOCXUploadBytes = 8 << 20
	maxDOCXMultipartOverhead = 64 << 10
)

type staffDOCXPreviewResponse struct {
	Text       string `json:"text"`
	Characters int    `json:"characters"`
}

// staffPreviewDOCX parses a single multipart file in memory. It never persists the
// upload, extracts archive entries to disk, or publishes imported content.
func (h handlers) staffPreviewDOCX(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiErrorResponse{Error: "content_type_must_be_multipart"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxDOCXUploadBytes+maxDOCXMultipartOverhead)
	reader, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_multipart"})
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		writeDOCXUploadError(w, err)
		return
	}
	if part.FormName() != "document" || part.FileName() == "" || !strings.EqualFold(filepath.Ext(part.FileName()), ".docx") {
		_ = part.Close()
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "docx_file_required"})
		return
	}
	raw, readErr := io.ReadAll(io.LimitReader(part, maxDOCXUploadBytes+1))
	closeErr := part.Close()
	if readErr != nil {
		writeDOCXUploadError(w, readErr)
		return
	}
	if closeErr != nil {
		writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_multipart"})
		return
	}
	if len(raw) > maxDOCXUploadBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "file_too_large"})
		return
	}
	if _, err := reader.NextPart(); err != io.EOF {
		if err != nil {
			writeDOCXUploadError(w, err)
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "exactly_one_file_required"})
		}
		return
	}

	document, err := docximport.Parse(bytes.NewReader(raw))
	if err != nil {
		if errors.Is(err, docximport.ErrTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "file_too_large"})
		} else {
			writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_docx"})
		}
		return
	}
	writeJSON(w, http.StatusOK, staffDOCXPreviewResponse{
		Text: document.Text, Characters: utf8.RuneCountInString(document.Text),
	})
}

func writeDOCXUploadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, apiErrorResponse{Error: "request_too_large"})
		return
	}
	writeJSON(w, http.StatusBadRequest, apiErrorResponse{Error: "invalid_multipart"})
}
