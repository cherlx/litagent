package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"litagent/core/cover"
	"litagent/core/epub"
	"litagent/core/fb2"
	"litagent/core/model"
	"litagent/core/rules"
)

// App is the Wails backend: it holds the currently loaded book and exposes the
// load / re-analyse / export operations to the Svelte frontend.
type App struct {
	ctx  context.Context
	mu   sync.Mutex
	path string       // currently open FB2 path
	book *model.Book  // corrected book from the last run (for export)

	// Generated cover state (see cover_app.go).
	coverData   []byte           // last generated JPEG, nil if none
	coverCT     string           // its content type
	coverTpls   []cover.Template // templates from the wallpaper folder
	coverIdx    int              // index of the template last used (-1 if none)
	useGenCover bool             // embed the generated cover on export

	// Edited metadata, applied to the export copy (see meta_app.go). nil = none.
	metaEdit *MetaEdit

	// allowHyphenation leaves the reader's own hyphenation engine on for the
	// exported EPUB. Default false matches the current Kindle-safe CSS.
	allowHyphenation bool

	// previewPages is the flattened, paginated preview of the current book
	// (see preview.go). Rebuilt whenever a.book is (re)assigned in run().
	previewPages []previewPage
}

// NewApp creates a new App application struct.
func NewApp() *App { return &App{coverIdx: -1} }

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// --- types returned to the frontend (JSON-serialised by Wails) -------------

type MetaInfo struct {
	Title    string `json:"title"`
	Author   string `json:"author"`
	Lang     string `json:"lang"`
	Sections int    `json:"sections"`
	HasCover bool   `json:"hasCover"`
}

type RuleStat struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Level    string `json:"level"`
	Count    int    `json:"count"`
	Enabled  bool   `json:"enabled"`
}

type FindingView struct {
	RuleID  string `json:"ruleId"`
	Section string `json:"section"`
	Context string `json:"context"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

type LoadResult struct {
	Path      string        `json:"path"`
	Name      string        `json:"name"`
	Meta      MetaInfo      `json:"meta"`
	Rules     []RuleStat    `json:"rules"`
	Findings  []FindingView `json:"findings"`
	Total     int           `json:"total"`
	Truncated bool          `json:"truncated"`
}

// perRuleCap limits how many example findings of each rule are sent to the UI.
// Counts (the sidebar badges) are always exact; this only bounds the drill-down
// payload. Capping per rule (not by a global document-order prefix) guarantees
// every rule with findings is represented, so clicking any rule shows examples.
const perRuleCap = 200

// --- bound methods ---------------------------------------------------------

// Open shows a file picker, loads the chosen FB2 and runs all rules.
func (a *App) Open() (*LoadResult, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Открыть FB2",
		Filters: []runtime.FileFilter{{DisplayName: "FB2 книги (*.fb2)", Pattern: "*.fb2"}},
	})
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil // user cancelled
	}
	a.mu.Lock()
	a.path = path
	// Reset per-book export state for the newly opened file.
	a.coverData = nil
	a.coverCT = ""
	a.coverIdx = -1
	a.useGenCover = false
	a.metaEdit = nil
	a.mu.Unlock()
	// Default: apply A/B rules; C rules are surfaced as suggestions only.
	return a.run(rules.DefaultApplyIDs())
}

// Reload re-parses the open file and applies only the enabled rules. This is how
// the UI reverts an entire rule: untick it and reload.
func (a *App) Reload(enabled []string) (*LoadResult, error) {
	a.mu.Lock()
	path := a.path
	a.mu.Unlock()
	if path == "" {
		return nil, fmt.Errorf("файл не открыт")
	}
	return a.run(enabled)
}

// Export writes the corrected book to an EPUB chosen via a save dialog.
func (a *App) Export() (string, error) {
	a.mu.Lock()
	book := a.book
	path := a.path
	a.mu.Unlock()
	if book == nil {
		return "", fmt.Errorf("нет открытой книги")
	}
	// Default file name = the book title from metadata (edited + tags stripped),
	// falling back to the FB2 file name when the title is empty.
	title, _ := a.effectiveTitleAuthor(book)
	name := sanitizeFilename(cleanMetaTitle(title))
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	def := name + ".epub"
	out, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Сохранить EPUB",
		DefaultFilename: def,
		Filters:         []runtime.FileFilter{{DisplayName: "EPUB (*.epub)", Pattern: "*.epub"}},
	})
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", nil // cancelled
	}
	exportBook := a.prepareExportBook(book)

	a.mu.Lock()
	allowHyphenation := a.allowHyphenation
	a.mu.Unlock()

	w, err := os.Create(out)
	if err != nil {
		return "", err
	}
	defer w.Close()
	if err := epub.Build(exportBook, epub.Options{AllowHyphenation: allowHyphenation}, w); err != nil {
		return "", fmt.Errorf("сборка EPUB: %w", err)
	}
	return out, nil
}

// GetHyphenation reports whether the reader's own automatic hyphenation is
// left enabled on export (default: disabled, since it breaks words
// mid-syllable on some Kindle firmwares).
func (a *App) GetHyphenation() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.allowHyphenation
}

// SetHyphenation toggles whether the reader's own automatic hyphenation is
// left enabled on export.
func (a *App) SetHyphenation(allow bool) {
	a.mu.Lock()
	a.allowHyphenation = allow
	a.mu.Unlock()
}

// PreviewPageView is one paginated preview page sent to the frontend.
type PreviewPageView struct {
	Index int    `json:"index"` // 0-based
	Title string `json:"title"`
	HTML  string `json:"html"`
}

// PreviewInfoView is returned by PreviewInfo: total page count plus page 0, so
// the frontend can render the tab from a single round trip.
type PreviewInfoView struct {
	PageCount int             `json:"pageCount"`
	Page      PreviewPageView `json:"page"`
}

// PreviewSearchHit is one search match, mirroring FindingView's ⟦⟧-context
// convention so the frontend can reuse its existing highlight rendering.
type PreviewSearchHit struct {
	Page    int    `json:"page"` // 0-based page index to jump to
	Title   string `json:"title"`
	Context string `json:"context"`
}

// PreviewSearchResult is returned by SearchPreview.
type PreviewSearchResult struct {
	Hits      []PreviewSearchHit `json:"hits"`
	Total     int                `json:"total"`
	Truncated bool               `json:"truncated"`
}

// previewSearchPerPageCap/previewSearchTotalCap mirror perRuleCap's
// cap-per-group-but-count-everything convention, with "page" as the grouping
// axis, so one common word on one page can't crowd out matches elsewhere.
const previewSearchPerPageCap = 20
const previewSearchTotalCap = 300

func (a *App) previewPage(page int) (*PreviewPageView, error) {
	a.mu.Lock()
	pages := a.previewPages
	a.mu.Unlock()
	if len(pages) == 0 {
		return &PreviewPageView{Title: "Нет содержимого", HTML: "<p>В книге нет содержимого для предпросмотра.</p>"}, nil
	}
	if page < 0 || page >= len(pages) {
		return nil, fmt.Errorf("страница %d вне диапазона (всего %d)", page+1, len(pages))
	}
	p := pages[page]
	return &PreviewPageView{Index: page, Title: p.title, HTML: p.html}, nil
}

// PreviewInfo returns the total page count and the first page of the current
// book's preview.
func (a *App) PreviewInfo() (*PreviewInfoView, error) {
	a.mu.Lock()
	book := a.book
	count := len(a.previewPages)
	a.mu.Unlock()
	if book == nil {
		return nil, fmt.Errorf("нет открытой книги")
	}
	page, err := a.previewPage(0)
	if err != nil {
		return nil, err
	}
	return &PreviewInfoView{PageCount: count, Page: *page}, nil
}

// PreviewPage returns one page (0-based) of the current book's preview.
func (a *App) PreviewPage(page int) (*PreviewPageView, error) {
	a.mu.Lock()
	book := a.book
	a.mu.Unlock()
	if book == nil {
		return nil, fmt.Errorf("нет открытой книги")
	}
	return a.previewPage(page)
}

// SearchPreview finds query across every preview page (case-insensitive,
// rune-safe) and returns capped, ⟦⟧-marked matches with their page index.
func (a *App) SearchPreview(query string) (*PreviewSearchResult, error) {
	a.mu.Lock()
	book := a.book
	pages := a.previewPages
	a.mu.Unlock()
	if book == nil {
		return nil, fmt.Errorf("нет открытой книги")
	}

	q := []rune(strings.TrimSpace(query))
	for i, r := range q {
		q[i] = unicode.ToLower(r)
	}
	if len(q) == 0 {
		return &PreviewSearchResult{}, nil
	}

	var hits []PreviewSearchHit
	total := 0
	for pi, p := range pages {
		matches := foldIndexAllRunes(p.textRunes, q)
		total += len(matches)
		take := matches
		if len(take) > previewSearchPerPageCap {
			take = take[:previewSearchPerPageCap]
		}
		for _, rng := range take {
			if len(hits) >= previewSearchTotalCap {
				continue // keep scanning remaining pages so Total stays exact
			}
			hits = append(hits, PreviewSearchHit{
				Page:    pi,
				Title:   p.title,
				Context: previewRuneContext(p.textRunes, rng[0], rng[1]),
			})
		}
	}
	return &PreviewSearchResult{Hits: hits, Total: total, Truncated: total > len(hits)}, nil
}

// --- internals -------------------------------------------------------------

func (a *App) run(enabled []string) (*LoadResult, error) {
	a.mu.Lock()
	path := a.path
	a.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	book, err := fb2.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("разбор FB2: %w", err)
	}

	enabledSet := make(map[string]bool, len(enabled))
	for _, id := range enabled {
		enabledSet[id] = true
	}
	eng := rules.NewEngineFor(func(id string) bool { return enabledSet[id] })
	findings := eng.Run(book)

	a.mu.Lock()
	a.book = book
	a.previewPages = buildPreviewPages(book)
	a.mu.Unlock()

	return buildResult(path, book, findings, enabledSet), nil
}

func buildResult(path string, book *model.Book, findings []rules.Finding, enabled map[string]bool) *LoadResult {
	counts := make(map[string]int)
	for _, f := range findings {
		counts[f.RuleID]++
	}

	stats := make([]RuleStat, 0)
	for _, m := range rules.Catalog() {
		stats = append(stats, RuleStat{
			ID:       m.ID,
			Name:     m.Name,
			Category: m.Category,
			Level:    m.Level.String(),
			Count:    counts[m.ID],
			Enabled:  enabled[m.ID],
		})
	}

	// Send up to perRuleCap examples per rule. Scanning all findings (no early
	// break) ensures every rule's first occurrences are included regardless of
	// their position in the document.
	sent := make(map[string]int)
	views := make([]FindingView, 0, 4096)
	for _, f := range findings {
		if sent[f.RuleID] >= perRuleCap {
			continue
		}
		sent[f.RuleID]++
		views = append(views, FindingView{
			RuleID:  f.RuleID,
			Section: f.Section,
			Context: f.Context,
			Before:  f.Before,
			After:   f.After,
		})
	}

	return &LoadResult{
		Path:      path,
		Name:      filepath.Base(path),
		Meta:      metaInfo(book),
		Rules:     stats,
		Findings:  views,
		Total:     len(findings),
		Truncated: len(findings) > len(views),
	}
}

func metaInfo(book *model.Book) MetaInfo {
	m := book.Meta
	author := ""
	if len(m.Authors) > 0 {
		a := m.Authors[0]
		author = strings.TrimSpace(strings.Join(nonEmpty(a.First, a.Middle, a.Last), " "))
		if author == "" {
			author = a.Nick
		}
	}
	sections := 0
	for _, b := range book.Bodies {
		sections += len(b.Sections)
	}
	return MetaInfo{
		Title:    m.Title,
		Author:   author,
		Lang:     m.Lang,
		Sections: sections,
		HasCover: m.CoverHref != "",
	}
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
