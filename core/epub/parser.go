// Package epub parses EPUB files into the format-neutral model.Book.
//
// EPUB is a ZIP archive containing an OPF manifest and XHTML chapters.
// This parser supports EPUB 2.0 and 3.0, extracting metadata, structure,
// and converting XHTML to model.Book blocks.
package epub

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"litagent/core/model"
)

// ErrNotEPUB is returned when the file is not a valid EPUB.
var ErrNotEPUB = errors.New("epub: not a valid EPUB file")

// ErrDRMProtected is returned when the EPUB is encrypted.
var ErrDRMProtected = errors.New("epub: файл защищён DRM и не может быть открыт")

// Parse reads an EPUB document and returns the parsed Book.
func Parse(r io.Reader) (*model.Book, error) {
	// Read entire EPUB into memory for ZIP parsing
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("epub: не является ZIP архивом: %w", err)
	}

	// Check for DRM encryption
	if hasDRM(zr) {
		return nil, ErrDRMProtected
	}

	// Find OPF file path from container.xml
	opfPath, err := findOPFPath(zr)
	if err != nil {
		return nil, err
	}

	// Parse OPF for metadata and structure
	opf, err := parseOPF(zr, opfPath)
	if err != nil {
		return nil, err
	}

	// Build Book from OPF
	book := &model.Book{
		Meta: model.Metadata{
			Title:     opf.Metadata.Title,
			Lang:      opf.Metadata.Language,
			Publisher: opf.Metadata.Publisher,
			Date:      opf.Metadata.Date,
			ISBN:      opf.Metadata.ISBN,
			Keywords:  opf.Metadata.Keywords,
			CoverHref: opf.Metadata.CoverID,
		},
	}

	// Parse authors
	for _, creator := range opf.Metadata.Creators {
		book.Meta.Authors = append(book.Meta.Authors, parsePerson(creator))
	}

	// Parse translators
	for _, contrib := range opf.Metadata.Contributors {
		if strings.Contains(strings.ToLower(contrib.Role), "trl") ||
			strings.Contains(strings.ToLower(contrib.Role), "translator") {
			book.Meta.Translators = append(book.Meta.Translators, parsePerson(contrib.Text))
		}
	}

	// Parse series from calibre metadata
	if opf.Metadata.SeriesName != "" {
		book.Meta.Sequence = &model.Sequence{
			Name:   opf.Metadata.SeriesName,
			Number: opf.Metadata.SeriesIndex,
		}
	}

	// Extract cover image
	if opf.Metadata.CoverID != "" {
		if coverData, ct := extractBinary(zr, opf, opf.Metadata.CoverID); coverData != nil {
			book.Binaries = append(book.Binaries, model.Binary{
				ID:          opf.Metadata.CoverID,
				ContentType: ct,
				Data:        coverData,
			})
		}
	}

	// Parse spine (chapters in reading order)
	opfDir := path.Dir(opfPath)
	body := model.Body{Name: "main"}

	for _, itemref := range opf.Spine.Items {
		item := findManifestItem(opf, itemref.IDRef)
		if item == nil {
			continue
		}

		chapterPath := path.Join(opfDir, item.Href)
		section, err := parseChapter(zr, chapterPath)
		if err != nil {
			// Skip broken chapters gracefully
			continue
		}

		if section != nil {
			body.Sections = append(body.Sections, section)
		}
	}

	book.Bodies = []*model.Body{&body}
	return book, nil
}

// hasDRM checks if EPUB has encryption.xml indicating DRM.
func hasDRM(zr *zip.Reader) bool {
	for _, f := range zr.File {
		if strings.ToLower(f.Name) == "meta-inf/encryption.xml" {
			return true
		}
	}
	return false
}

// findOPFPath reads META-INF/container.xml to find the OPF file path.
func findOPFPath(zr *zip.Reader) (string, error) {
	for _, f := range zr.File {
		if strings.ToLower(f.Name) == "meta-inf/container.xml" {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()

			var container struct {
				Rootfiles struct {
					Rootfile []struct {
						FullPath string `xml:"full-path,attr"`
					} `xml:"rootfile"`
				} `xml:"rootfiles"`
			}

			if err := xml.NewDecoder(rc).Decode(&container); err != nil {
				return "", fmt.Errorf("epub: ошибка парсинга container.xml: %w", err)
			}

			if len(container.Rootfiles.Rootfile) == 0 {
				return "", ErrNotEPUB
			}

			return container.Rootfiles.Rootfile[0].FullPath, nil
		}
	}
	return "", ErrNotEPUB
}

// OPF represents the EPUB package document structure.
type OPF struct {
	Metadata struct {
		Title        string `xml:"title"`
		Language     string `xml:"language"`
		Publisher    string `xml:"publisher"`
		Date         string `xml:"date"`
		ISBN         string `xml:"identifier"`
		Keywords     []string
		CoverID      string
		SeriesName   string
		SeriesIndex  string
		Creators     []string `xml:"creator"`
		Contributors []struct {
			Text string `xml:",chardata"`
			Role string `xml:"role,attr"`
		} `xml:"contributor"`
	} `xml:"metadata"`

	Manifest struct {
		Items []struct {
			ID        string `xml:"id,attr"`
			Href      string `xml:"href,attr"`
			MediaType string `xml:"media-type,attr"`
		} `xml:"item"`
	} `xml:"manifest"`

	Spine struct {
		Items []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

// parseOPF reads and parses the OPF file.
func parseOPF(zr *zip.Reader, opfPath string) (*OPF, error) {
	for _, f := range zr.File {
		if f.Name == opfPath {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()

			data, err := io.ReadAll(rc)
			if err != nil {
				return nil, err
			}

			var opf OPF
			dec := xml.NewDecoder(bytes.NewReader(data))
			dec.Strict = false
			if err := dec.Decode(&opf); err != nil {
				return nil, fmt.Errorf("epub: ошибка парсинга OPF: %w", err)
			}

			// Extract cover ID from meta tags
			opf.Metadata.CoverID = extractCoverID(data)

			// Extract series metadata (calibre format)
			extractSeriesMetadata(data, &opf)

			return &opf, nil
		}
	}
	return nil, fmt.Errorf("epub: OPF файл не найден: %s", opfPath)
}

// extractCoverID finds cover image ID from metadata.
func extractCoverID(opfData []byte) string {
	// Look for <meta name="cover" content="cover-image"/>
	s := string(opfData)
	if idx := strings.Index(s, `name="cover"`); idx != -1 {
		if contentIdx := strings.Index(s[idx:], `content="`); contentIdx != -1 {
			start := idx + contentIdx + 9
			if endIdx := strings.Index(s[start:], `"`); endIdx != -1 {
				return s[start : start+endIdx]
			}
		}
	}
	return ""
}

// extractSeriesMetadata extracts calibre:series metadata.
func extractSeriesMetadata(opfData []byte, opf *OPF) {
	s := string(opfData)

	// calibre:series
	if idx := strings.Index(s, `calibre:series"`); idx != -1 {
		if contentIdx := strings.Index(s[idx:], ">") + idx + 1; contentIdx > idx {
			if endIdx := strings.Index(s[contentIdx:], "<"); endIdx != -1 {
				opf.Metadata.SeriesName = strings.TrimSpace(s[contentIdx : contentIdx+endIdx])
			}
		}
	}

	// calibre:series_index
	if idx := strings.Index(s, `calibre:series_index"`); idx != -1 {
		if contentIdx := strings.Index(s[idx:], ">") + idx + 1; contentIdx > idx {
			if endIdx := strings.Index(s[contentIdx:], "<"); endIdx != -1 {
				opf.Metadata.SeriesIndex = strings.TrimSpace(s[contentIdx : contentIdx+endIdx])
			}
		}
	}
}

// findManifestItem finds manifest item by ID.
func findManifestItem(opf *OPF, id string) *struct {
	ID        string `xml:"id,attr"`
	Href      string `xml:"href,attr"`
	MediaType string `xml:"media-type,attr"`
} {
	for i := range opf.Manifest.Items {
		if opf.Manifest.Items[i].ID == id {
			return &opf.Manifest.Items[i]
		}
	}
	return nil
}

// extractBinary reads a binary file from the EPUB by manifest ID.
func extractBinary(zr *zip.Reader, opf *OPF, id string) ([]byte, string) {
	item := findManifestItem(opf, id)
	if item == nil {
		return nil, ""
	}

	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, item.Href) {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				continue
			}
			return data, item.MediaType
		}
	}
	return nil, ""
}

// parseChapter reads an XHTML chapter and converts it to model.Section.
func parseChapter(zr *zip.Reader, chapterPath string) (*model.Section, error) {
	for _, f := range zr.File {
		if f.Name == chapterPath {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()

			data, err := io.ReadAll(rc)
			if err != nil {
				return nil, err
			}

			return xhtmlToSection(data)
		}
	}
	return nil, fmt.Errorf("epub: глава не найдена: %s", chapterPath)
}

// xhtmlToSection converts XHTML content to model.Section.
func xhtmlToSection(data []byte) (*model.Section, error) {
	section := &model.Section{}

	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity

	var currentPara *model.Paragraph
	var inBody bool

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return section, nil // Graceful degradation
		}

		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)

			// Start of body content
			if name == "body" {
				inBody = true
			}

			if !inBody {
				continue
			}

			switch name {
			case "h1", "h2", "h3", "h4", "h5", "h6":
				if currentPara != nil {
					section.Content = append(section.Content, currentPara)
					currentPara = nil
				}
				// Title will be collected in CharData

			case "p":
				if currentPara != nil {
					section.Content = append(section.Content, currentPara)
				}
				currentPara = &model.Paragraph{}

			case "em", "i":
				if currentPara != nil {
					// Mark for emphasis (simplified)
				}

			case "strong", "b":
				if currentPara != nil {
					// Mark for strong (simplified)
				}
			}

		case xml.CharData:
			if !inBody {
				continue
			}

			text := strings.TrimSpace(string(t))
			if text == "" {
				continue
			}

			if currentPara != nil {
				currentPara.Inlines = append(currentPara.Inlines, &model.Text{Value: text})
			} else {
				// Text outside paragraph → create one
				currentPara = &model.Paragraph{
					Inlines: []model.Inline{&model.Text{Value: text}},
				}
			}

		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			if name == "p" && currentPara != nil {
				section.Content = append(section.Content, currentPara)
				currentPara = nil
			}
			if name == "body" {
				inBody = false
			}
		}
	}

	// Flush last paragraph
	if currentPara != nil {
		section.Content = append(section.Content, currentPara)
	}

	return section, nil
}

// parsePerson converts "First Middle Last" to model.Person.
func parsePerson(name string) model.Person {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Person{}
	}

	fields := strings.Fields(name)
	switch len(fields) {
	case 0:
		return model.Person{}
	case 1:
		return model.Person{Last: fields[0]}
	case 2:
		return model.Person{First: fields[0], Last: fields[1]}
	default:
		return model.Person{
			First:  fields[0],
			Middle: strings.Join(fields[1:len(fields)-1], " "),
			Last:   fields[len(fields)-1],
		}
	}
}
