package parser

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"

	"github.com/amantur/docs-helper/internal/models"
	"github.com/ledongthuc/pdf"
)

// parsePDF extracts text from a PDF byte slice page by page.
// Sections are detected by heuristics on font size and bold-looking font names.
func parsePDF(log *slog.Logger, data []byte) (*models.ParsedDocument, error) {
	reader := bytes.NewReader(data)
	size := int64(len(data))

	pdfReader, err := pdf.NewReader(reader, size)
	if err != nil {
		// Detect encrypted PDFs.
		if isEncryptedErr(err) {
			return nil, fmt.Errorf("encrypted PDF: %w", err)
		}
		return nil, fmt.Errorf("opening PDF: %w", err)
	}

	numPages := pdfReader.NumPage()
	if numPages == 0 {
		return nil, fmt.Errorf("PDF has no pages")
	}

	pages := make([]models.Page, 0, numPages)

	for i := 1; i <= numPages; i++ {
		page := pdfReader.Page(i)
		if page.V.IsNull() {
			continue
		}

		content := page.Content()
		pageText, sections := buildPage(content.Text)

		pages = append(pages, models.Page{
			Number:   i,
			Text:     pageText,
			Sections: sections,
		})

		log.Debug("parsed PDF page", "page", i, "rows", len(content.Text), "sections", len(sections))
	}

	return &models.ParsedDocument{
		Pages: pages,
	}, nil
}

// buildPage concatenates text rows into a single string and detects
// section headers from rows with large or bold fonts.
func buildPage(rows []pdf.Text) (text string, sections []string) {
	if len(rows) == 0 {
		return "", nil
	}

	// Compute median font size for the page to use as baseline.
	medianSize := medianFontSize(rows)
	// Threshold: 20% larger than median, or at least 14pt.
	headerThreshold := max(medianSize*1.2, 14)

	var buf strings.Builder
	for i, row := range rows {
		trimmed := strings.TrimSpace(row.S)
		if trimmed == "" {
			continue
		}

		buf.WriteString(trimmed)

		// Detect section header.
		if isHeaderRow(row, headerThreshold) {
			sections = append(sections, trimmed)
		}

		if i < len(rows)-1 {
			buf.WriteByte('\n')
		}
	}

	return buf.String(), sections
}

// isHeaderRow checks whether a text row looks like a section header.
// Heuristics: large font size or bold-looking font name.
func isHeaderRow(row pdf.Text, threshold float64) bool {
	if row.FontSize >= threshold {
		return true
	}
	fontLower := strings.ToLower(row.Font)
	if strings.Contains(fontLower, "bold") || strings.Contains(fontLower, "heavy") {
		return true
	}
	return false
}

// medianFontSize computes an approximate median from text rows.
func medianFontSize(rows []pdf.Text) float64 {
	if len(rows) == 0 {
		return 12
	}

	sizes := make([]float64, len(rows))
	for i, r := range rows {
		sizes[i] = r.FontSize
	}
	// Simple approximation: sort and pick middle.
	sortFloat64s(sizes)
	return sizes[len(sizes)/2]
}

// sortFloat64s is a simple insertion sort for small slices.
func sortFloat64s(a []float64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// isEncryptedErr detects password-protected PDF errors.
func isEncryptedErr(err error) bool {
	return err == pdf.ErrInvalidPassword ||
		strings.Contains(err.Error(), "encrypted") ||
		strings.Contains(err.Error(), "password")
}
