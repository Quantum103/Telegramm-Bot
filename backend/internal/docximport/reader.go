package docximport

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	maxArchiveBytes = 8 << 20
	maxEntries = 256
	maxEntryBytes = 4 << 20
	maxTotalBytes = 16 << 20
	maxExtractedBytes = 1 << 20
	documentPart = "word/document.xml"
)

var (
	ErrInvalidDOCX = errors.New("invalid or unsupported DOCX document")
	ErrTooLarge = errors.New("DOCX document exceeds safety limits")
)

type Document struct { Text string }

// Parse extracts plain paragraph text from a bounded DOCX archive without writing
// archive entries to disk or resolving external relationships.
func Parse(input io.Reader) (Document, error) {
	if input == nil { return Document{}, ErrInvalidDOCX }
	raw, err := io.ReadAll(io.LimitReader(input, maxArchiveBytes+1))
	if err != nil { return Document{}, fmt.Errorf("%w: read archive", ErrInvalidDOCX) }
	if len(raw) > maxArchiveBytes { return Document{}, ErrTooLarge }
	if len(raw) < 4 || !bytes.Equal(raw[:2], []byte{'P', 'K'}) { return Document{}, ErrInvalidDOCX }
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil { return Document{}, fmt.Errorf("%w: malformed ZIP", ErrInvalidDOCX) }
	if len(zr.File) == 0 { return Document{}, ErrInvalidDOCX }
	if len(zr.File) > maxEntries { return Document{}, ErrTooLarge }

	var doc *zip.File
	var total uint64
	for _, f := range zr.File {
		name := f.Name
		validationName := strings.TrimSuffix(name, "/")
		clean := path.Clean(validationName)
		if name == "" || strings.Contains(name, "\\") || path.IsAbs(name) ||
			validationName == "" || clean == ".." || strings.HasPrefix(clean, "../") || clean != validationName {
			return Document{}, ErrInvalidDOCX
		}
		if f.Flags&1 != 0 || f.Mode()&0o170000 == 0o120000 { return Document{}, ErrInvalidDOCX }
		lower := strings.ToLower(name)
		if strings.Contains(lower, "vbaproject.bin") || strings.EqualFold(path.Ext(name), ".docm") { return Document{}, ErrInvalidDOCX }
		if f.UncompressedSize64 > maxEntryBytes { return Document{}, ErrTooLarge }
		total += f.UncompressedSize64
		if total > maxTotalBytes { return Document{}, ErrTooLarge }
		if name == documentPart {
			if doc != nil { return Document{}, ErrInvalidDOCX }
			doc = f
		}
	}
	if doc == nil { return Document{}, ErrInvalidDOCX }
	rc, err := doc.Open()
	if err != nil { return Document{}, ErrInvalidDOCX }
	part, readErr := io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil { return Document{}, ErrInvalidDOCX }
	if len(part) > maxEntryBytes { return Document{}, ErrTooLarge }
	text, err := extractText(part)
	if err != nil { return Document{}, err }
	if strings.TrimSpace(text) == "" { return Document{}, ErrInvalidDOCX }
	return Document{Text: text}, nil
}

func extractText(content []byte) (string, error) {
	d := xml.NewDecoder(bytes.NewReader(content))
	d.Strict = true
	var out strings.Builder
	inText, inParagraph := false, false
	for {
		tok, err := d.Token()
		if err == io.EOF { break }
		if err != nil { return "", fmt.Errorf("%w: malformed document XML", ErrInvalidDOCX) }
		switch v := tok.(type) {
		case xml.Directive:
			// Reject DTDs and entity declarations.
			return "", ErrInvalidDOCX
		case xml.ProcInst:
			// The XML declaration is allowed; all other processing instructions are rejected.
			if !strings.EqualFold(v.Target, "xml") { return "", ErrInvalidDOCX }
		case xml.StartElement:
			switch v.Name.Local {
			case "p": inParagraph = true
			case "t": inText = inParagraph
			case "tab": if inParagraph { out.WriteByte('\t') }
			case "br", "cr": if inParagraph { out.WriteByte('\n') }
			}
		case xml.CharData:
			if inText { out.Write([]byte(v)) }
		case xml.EndElement:
			switch v.Name.Local {
			case "t": inText = false
			case "p":
				if inParagraph { out.WriteByte('\n') }
				inParagraph = false
			}
		}
		if out.Len() > maxExtractedBytes { return "", ErrTooLarge }
	}
	text := strings.TrimSpace(out.String())
	if !utf8.ValidString(text) { return "", ErrInvalidDOCX }
	return text, nil
}
