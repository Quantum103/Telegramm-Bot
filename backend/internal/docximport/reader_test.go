package docximport

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func makeDOCX(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, value := range entries {
		w, err := zw.Create(name)
		if err != nil { t.Fatal(err) }
		if _, err := io.WriteString(w, value); err != nil { t.Fatal(err) }
	}
	if err := zw.Close(); err != nil { t.Fatal(err) }
	return buf.Bytes()
}

const validDocument = `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body><w:p><w:r><w:t>Первый абзац</w:t></w:r></w:p>
<w:p><w:r><w:t>Второй</w:t><w:tab/><w:t>абзац</w:t><w:br/><w:t>строка</w:t></w:r></w:p></w:body></w:document>`

func TestParseExtractsParagraphText(t *testing.T) {
	input := makeDOCX(t, map[string]string{"word/": "", documentPart: validDocument})
	got, err := Parse(bytes.NewReader(input))
	if err != nil { t.Fatalf("Parse() error = %v", err) }
	want := "Первый абзац\nВторой\tабзац\nстрока"
	if got.Text != want { t.Fatalf("Parse().Text = %q, want %q", got.Text, want) }
}

func TestParseRejectsMalformedAndUnsupportedArchives(t *testing.T) {
	dtd := "<!DOC" + "TYPE document [<!ENTITY x \"bad\">]><document><p><t>&x;</t></p></document>"
	cases := []struct { name string; data []byte }{
		{"not zip", []byte("not a zip archive")},
		{"missing document", makeDOCX(t, map[string]string{"word/styles.xml": "<styles/>"})},
		{"malformed xml", makeDOCX(t, map[string]string{documentPart: "<w:document><w:p>"})},
		{"DTD", makeDOCX(t, map[string]string{documentPart: dtd})},
		{"empty text", makeDOCX(t, map[string]string{documentPart: "<document><body><p><r><t>  </t></r></p></body></document>"})},
		{"macro", makeDOCX(t, map[string]string{documentPart: validDocument, "word/vbaProject.bin": "macro"})},
		{"traversal", makeDOCX(t, map[string]string{documentPart: validDocument, "../outside.txt": "no"})},
		{"backslash path", makeDOCX(t, map[string]string{documentPart: validDocument, "word\\..\\outside.txt": "no"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(bytes.NewReader(tc.data)); !errors.Is(err, ErrInvalidDOCX) {
				t.Fatalf("Parse() error = %v, want ErrInvalidDOCX", err)
			}
		})
	}
}

func TestParseRejectsOversizedInput(t *testing.T) {
	input := bytes.NewReader(bytes.Repeat([]byte("x"), maxArchiveBytes+1))
	if _, err := Parse(input); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Parse() error = %v, want ErrTooLarge", err)
	}
}

func TestParseRejectsTooManyEntries(t *testing.T) {
	entries := map[string]string{documentPart: validDocument}
	for i := 0; i < maxEntries; i++ { entries[fmt.Sprintf("extra/%03d.txt", i)] = "" }
	input := makeDOCX(t, entries)
	if _, err := Parse(bytes.NewReader(input)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Parse() error = %v, want ErrTooLarge", err)
	}
}

func TestParseRejectsTextLimit(t *testing.T) {
	xmlText := "<document><body><p><t>" + strings.Repeat("x", maxExtractedBytes+1) + "</t></p></body></document>"
	input := makeDOCX(t, map[string]string{documentPart: xmlText})
	if _, err := Parse(bytes.NewReader(input)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Parse() error = %v, want ErrTooLarge", err)
	}
}
