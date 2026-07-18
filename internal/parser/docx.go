package parser

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"log/slog"
	"strings"

	"github.com/amantur/docs-helper/internal/models"
	"github.com/nguyenthenguyen/docx"
)

// parseDOCX extracts paragraphs from a DOCX byte slice using the docx library.
// All text is placed on a single page. Sections are detected from
// heading styles (Heading1, Heading2, …) and bold runs.
func parseDOCX(log *slog.Logger, data []byte) (*models.ParsedDocument, error) {
	size := int64(len(data))

	r, err := docx.ReadDocxFromMemory(bytes.NewReader(data), size)
	if err != nil {
		return nil, fmt.Errorf("opening DOCX: %w", err)
	}
	defer r.Close()

	editable := r.Editable()
	content := editable.GetContent()

	paras := extractParagraphsFromXML([]byte(content))
	if len(paras) == 0 {
		return nil, fmt.Errorf("DOCX has no content")
	}

	var textBuf strings.Builder
	var sections []string

	for i, p := range paras {
		if i > 0 {
			textBuf.WriteByte('\n')
		}
		textBuf.WriteString(p.text)

		if p.isHeading {
			sections = append(sections, p.text)
		}
	}

	log.Debug("parsed DOCX", "paragraphs", len(paras), "sections", len(sections))

	return &models.ParsedDocument{
		Pages: []models.Page{{
			Number:   1,
			Text:     textBuf.String(),
			Sections: sections,
		}},
	}, nil
}

// paraInfo holds a parsed paragraph's text and heading status.
type paraInfo struct {
	text      string
	isHeading bool
}

// extractParagraphsFromXML parses word/document.xml and collects paragraphs.
// It detects headings by w:pStyle values (Heading1, Heading2, …) and
// by bold runs (w:b elements).
func extractParagraphsFromXML(xmlData []byte) []paraInfo {
	decoder := xml.NewDecoder(bytes.NewReader(xmlData))

	var (
		paras      []paraInfo
		inPara     bool
		inTextRun  bool
		isBold     bool
		paraStyle  string
		paraBuf    strings.Builder
	)

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				inPara = true
				paraStyle = ""
				paraBuf.Reset()
				isBold = false

			case "pStyle":
				for _, attr := range t.Attr {
					if attr.Name.Local == "val" {
						paraStyle = attr.Value
					}
				}

			case "b", "bCs":
				isBold = true

			case "t":
				inTextRun = true

			case "tab":
				if inPara {
					paraBuf.WriteByte('\t')
				}
			}

		case xml.EndElement:
			switch t.Name.Local {
			case "p":
				if inPara {
					text := strings.TrimSpace(paraBuf.String())
					if text != "" {
						heading := isHeadingStyle(paraStyle) || isBold
						paras = append(paras, paraInfo{
							text:      text,
							isHeading: heading,
						})
					}
				}
				inPara = false

			case "t":
				inTextRun = false
			}

		case xml.CharData:
			if inTextRun && inPara {
				paraBuf.Write(t)
			}
		}
	}

	return paras
}

// isHeadingStyle reports whether a pStyle value is a heading style.
func isHeadingStyle(style string) bool {
	s := strings.ToLower(style)
	return strings.HasPrefix(s, "heading")
}
