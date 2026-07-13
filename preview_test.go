package main

import (
	"fmt"
	"strings"
	"testing"

	"litagent/core/model"
)

func previewPara(s string) *model.Paragraph {
	return &model.Paragraph{Inlines: []model.Inline{&model.Text{Value: s}}}
}

func previewTitle(s string) []model.Block {
	return []model.Block{previewPara(s)}
}

func TestBuildPreviewPages_FrontMatter(t *testing.T) {
	book := &model.Book{Bodies: []*model.Body{{
		Title:    previewTitle("Стивен Фрай"),
		Epigraph: []model.Block{previewPara("Эпиграф текст")},
		Sections: []*model.Section{{
			Title:   previewTitle("Часть первая"),
			Content: []model.Block{previewPara("Текст части")},
		}},
	}}}

	pages := buildPreviewPages(book)
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(pages))
	}
	if !strings.Contains(pages[0].html, "Стивен Фрай") || !strings.Contains(pages[0].html, "Эпиграф текст") {
		t.Errorf("front-matter page missing title/epigraph:\n%s", pages[0].html)
	}
	if strings.Contains(pages[0].html, "Часть первая") {
		t.Errorf("front-matter page should not contain the section's own title:\n%s", pages[0].html)
	}
	if !strings.Contains(pages[1].html, "Часть первая") || !strings.Contains(pages[1].html, "Текст части") {
		t.Errorf("section page missing its own content:\n%s", pages[1].html)
	}
}

// TestBuildPreviewPages_ContainerThenChildren is the regression test for the
// originally-reported bug: a wrapper section with no own content and several
// nested chapters must NOT dump every chapter onto one page.
func TestBuildPreviewPages_ContainerThenChildren(t *testing.T) {
	wrapper := &model.Section{
		Title: previewTitle("Часть первая"),
		Children: []*model.Section{
			{Title: previewTitle("Глава 1"), Content: []model.Block{previewPara("Текст главы один")}},
			{Title: previewTitle("Глава 2"), Content: []model.Block{previewPara("Текст главы два")}},
		},
	}
	book := &model.Book{Bodies: []*model.Body{{Sections: []*model.Section{wrapper}}}}

	pages := buildPreviewPages(book)
	if len(pages) != 3 {
		t.Fatalf("pages = %d, want 3", len(pages))
	}

	p0 := pages[0].html
	if !strings.Contains(p0, "Часть первая") {
		t.Errorf("page 0 missing wrapper title:\n%s", p0)
	}
	if strings.Contains(p0, "Глава 1") || strings.Contains(p0, "Текст главы один") ||
		strings.Contains(p0, "Глава 2") || strings.Contains(p0, "Текст главы два") {
		t.Errorf("page 0 should not contain any child chapter content:\n%s", p0)
	}

	p1 := pages[1].html
	if !strings.Contains(p1, "Глава 1") || !strings.Contains(p1, "Текст главы один") {
		t.Errorf("page 1 missing chapter 1 content:\n%s", p1)
	}
	if strings.Contains(p1, "Глава 2") {
		t.Errorf("page 1 should not contain chapter 2:\n%s", p1)
	}

	p2 := pages[2].html
	if !strings.Contains(p2, "Глава 2") || !strings.Contains(p2, "Текст главы два") {
		t.Errorf("page 2 missing chapter 2 content:\n%s", p2)
	}
	if strings.Contains(p2, "Глава 1") {
		t.Errorf("page 2 should not contain chapter 1:\n%s", p2)
	}
}

func TestBuildPreviewPages_OversizedSectionSubChunked(t *testing.T) {
	const n = previewPageBlockTarget + 50
	content := make([]model.Block, n)
	for i := range n {
		content[i] = previewPara(fmt.Sprintf("para-%03d", i))
	}
	book := &model.Book{Bodies: []*model.Body{{Sections: []*model.Section{{
		Title:   previewTitle("Большая глава"),
		Content: content,
	}}}}}

	pages := buildPreviewPages(book)
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(pages))
	}
	if !strings.HasSuffix(pages[0].title, " (1/2)") {
		t.Errorf("page 0 title = %q, want suffix \" (1/2)\"", pages[0].title)
	}
	if !strings.HasSuffix(pages[1].title, " (2/2)") {
		t.Errorf("page 1 title = %q, want suffix \" (2/2)\"", pages[1].title)
	}

	last := fmt.Sprintf("para-%03d", previewPageBlockTarget-1)
	first := fmt.Sprintf("para-%03d", previewPageBlockTarget)
	if !strings.Contains(pages[0].html, last) || strings.Contains(pages[0].html, first) {
		t.Errorf("page 0 should end at %s and not include %s:\n%s", last, first, pages[0].html)
	}
	if !strings.Contains(pages[1].html, first) || strings.Contains(pages[1].html, last) {
		t.Errorf("page 1 should start at %s and not include %s:\n%s", first, last, pages[1].html)
	}
}

func TestSearchPreview_FindsMatchWithContextAndPage(t *testing.T) {
	book := &model.Book{Bodies: []*model.Body{{Sections: []*model.Section{{
		Content: []model.Block{previewPara("текст с уникальное слово тут")},
	}}}}}

	a := NewApp()
	a.book = book
	a.previewPages = buildPreviewPages(book)

	res, err := a.SearchPreview("УНИКАЛЬНОЕ")
	if err != nil {
		t.Fatalf("SearchPreview: %v", err)
	}
	if res.Total != 1 || len(res.Hits) != 1 {
		t.Fatalf("hits = %d, total = %d, want 1/1", len(res.Hits), res.Total)
	}
	if res.Hits[0].Page != 0 {
		t.Errorf("page = %d, want 0", res.Hits[0].Page)
	}
	if !strings.Contains(res.Hits[0].Context, "⟦уникальное⟧") {
		t.Errorf("context = %q, want to contain ⟦уникальное⟧", res.Hits[0].Context)
	}
}

func TestSearchPreview_MultiPageAndCap(t *testing.T) {
	manyParas := make([]model.Block, previewSearchPerPageCap+5)
	for i := range manyParas {
		manyParas[i] = previewPara("слово одно")
	}
	fewParas := []model.Block{previewPara("слово"), previewPara("слово"), previewPara("слово")}

	book := &model.Book{Bodies: []*model.Body{{Sections: []*model.Section{
		{Content: manyParas},
		{Content: fewParas},
	}}}}

	a := NewApp()
	a.book = book
	a.previewPages = buildPreviewPages(book)

	res, err := a.SearchPreview("слово")
	if err != nil {
		t.Fatalf("SearchPreview: %v", err)
	}
	wantTotal := len(manyParas) + len(fewParas)
	if res.Total != wantTotal {
		t.Errorf("total = %d, want %d", res.Total, wantTotal)
	}
	wantHits := previewSearchPerPageCap + len(fewParas)
	if len(res.Hits) != wantHits {
		t.Errorf("hits = %d, want %d", len(res.Hits), wantHits)
	}
	if !res.Truncated {
		t.Error("expected Truncated = true")
	}

	pages := map[int]bool{}
	for _, h := range res.Hits {
		pages[h.Page] = true
	}
	if len(pages) < 2 {
		t.Errorf("expected hits from multiple pages, got pages %v", pages)
	}
}

func TestPreviewInfoAndPage_NoBookOpen(t *testing.T) {
	a := NewApp()
	if _, err := a.PreviewInfo(); err == nil {
		t.Error("expected error from PreviewInfo with no book open")
	}
	if _, err := a.PreviewPage(0); err == nil {
		t.Error("expected error from PreviewPage with no book open")
	}
	if _, err := a.SearchPreview("x"); err == nil {
		t.Error("expected error from SearchPreview with no book open")
	}
}

func TestPreviewPage_OutOfRange(t *testing.T) {
	book := &model.Book{Bodies: []*model.Body{{Sections: []*model.Section{
		{Content: []model.Block{previewPara("раз")}},
	}}}}
	a := NewApp()
	a.book = book
	a.previewPages = buildPreviewPages(book)

	if _, err := a.PreviewPage(5); err == nil {
		t.Error("expected error for out-of-range page index")
	}
	if _, err := a.PreviewPage(-1); err == nil {
		t.Error("expected error for negative page index")
	}
}
