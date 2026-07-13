package main

import (
	"fmt"
	"html"
	"strings"
	"unicode"

	"litagent/core/model"
)

// previewPageBlockTarget is the soft per-page cap: a section whose own
// content exceeds this many top-level blocks is split into several
// consecutively-numbered sub-pages, so no single preview page is unbounded.
const previewPageBlockTarget = 400

// previewPage is one paginated preview unit: rendered HTML plus a plain-text
// shadow (no markup) of the same content, which search matches against.
type previewPage struct {
	title     string
	html      string
	textRunes []rune
}

// buildPreviewPages flattens a (corrected) book into an ordered list of
// preview pages: a front-matter page per body that has its own title or
// epigraph, followed by one page per top-level section, each recursively
// followed by its children's pages (see flattenSection). Footnotes
// (book.Notes) are not paginated here — EPUB export already keeps them in a
// separate notes.xhtml, and preview stays scoped to the narrative body.
func buildPreviewPages(book *model.Book) []previewPage {
	var pages []previewPage
	for _, body := range book.Bodies {
		if len(body.Title) > 0 || len(body.Epigraph) > 0 {
			var epi [][]model.Block
			if len(body.Epigraph) > 0 {
				epi = [][]model.Block{body.Epigraph}
			}
			pages = append(pages, chunkSectionPage(body.Title, epi, nil, nil)...)
		}
		for _, sec := range body.Sections {
			pages = append(pages, flattenSection(sec)...)
		}
	}
	return pages
}

// flattenSection lays out sec's own page(s) first, then each child section's
// page(s) in document order — a child's content is never inlined into an
// ancestor's page. This is what fixes a "Часть первая" wrapper section (own
// content empty, ~20 nested chapters as Children) from dumping every nested
// chapter onto one preview page: the wrapper now gets its own short header
// page, and each chapter becomes its own separate page right after it.
func flattenSection(sec *model.Section) []previewPage {
	pages := chunkSectionPage(sec.Title, sec.Epigraphs, sec.Annotation, sec.Content)
	for _, c := range sec.Children {
		pages = append(pages, flattenSection(c)...)
	}
	return pages
}

// chunkSectionPage renders one section/body's own header (title + epigraphs +
// annotation, always on the first sub-page only) and its content, split into
// groups of at most previewPageBlockTarget top-level blocks.
func chunkSectionPage(title []model.Block, epigraphs [][]model.Block, annotation, content []model.Block) []previewPage {
	label := previewBlocksText(title)
	if label == "" {
		label = "Без названия"
	}

	var chunks [][]model.Block
	if len(content) == 0 {
		chunks = [][]model.Block{nil}
	} else {
		body := content
		for len(body) > 0 {
			n := previewPageBlockTarget
			if n > len(body) {
				n = len(body)
			}
			chunks = append(chunks, body[:n])
			body = body[n:]
		}
	}

	pages := make([]previewPage, len(chunks))
	for i, blocks := range chunks {
		r := &previewRenderer{}
		if i == 0 {
			r.writeHeader(title, epigraphs, annotation)
		}
		r.blocks(blocks)
		pages[i] = previewPage{html: r.w.String(), textRunes: []rune(r.text.String())}
		if len(chunks) > 1 {
			pages[i].title = fmt.Sprintf("%s (%d/%d)", label, i+1, len(chunks))
		} else {
			pages[i].title = label
		}
	}
	return pages
}

type previewRenderer struct {
	w    strings.Builder // rendered HTML
	text strings.Builder // plain-text shadow of the same content, for search
}

// writeHeader renders a section/body's title (as a single fixed heading —
// pages no longer nest visually in one continuous document, so a per-depth
// h1..h6 no longer carries meaning), its epigraphs and its annotation.
func (r *previewRenderer) writeHeader(title []model.Block, epigraphs [][]model.Block, annotation []model.Block) {
	if len(title) > 0 {
		r.w.WriteString("<h2>")
		for i, b := range title {
			if p, ok := b.(*model.Paragraph); ok {
				if i > 0 {
					r.w.WriteString("<br/>")
				}
				r.inlines(p.Inlines)
			}
		}
		r.w.WriteString("</h2>")
	}
	for _, ep := range epigraphs {
		r.w.WriteString(`<div class="epi">`)
		r.blocks(ep)
		r.w.WriteString("</div>")
	}
	if len(annotation) > 0 {
		r.w.WriteString(`<div class="annotation">`)
		r.blocks(annotation)
		r.w.WriteString("</div>")
	}
}

func (r *previewRenderer) blocks(blocks []model.Block) {
	for _, b := range blocks {
		r.block(b)
	}
}

func (r *previewRenderer) block(b model.Block) {
	switch n := b.(type) {
	case *model.Paragraph:
		r.w.WriteString("<p>")
		r.inlines(n.Inlines)
		r.w.WriteString("</p>")
		r.text.WriteString("\n")
	case *model.Subtitle:
		r.w.WriteString(`<p class="sub">`)
		r.inlines(n.Inlines)
		r.w.WriteString("</p>")
		r.text.WriteString("\n")
	case *model.EmptyLine:
		r.w.WriteString(`<div class="el"></div>`)
	case *model.Poem:
		r.w.WriteString(`<div class="poem">`)
		for _, st := range n.Stanzas {
			for _, v := range st.Verses {
				r.w.WriteString(`<p class="v">`)
				r.inlines(v)
				r.w.WriteString("</p>")
				r.text.WriteString("\n")
			}
		}
		r.w.WriteString("</div>")
	case *model.Cite:
		r.w.WriteString("<blockquote>")
		r.blocks(n.Content)
		if len(n.TextAuthor) > 0 {
			r.w.WriteString(`<p class="author">`)
			r.inlines(n.TextAuthor)
			r.w.WriteString("</p>")
			r.text.WriteString("\n")
		}
		r.w.WriteString("</blockquote>")
	case *model.Epigraph:
		r.w.WriteString(`<div class="epi">`)
		r.blocks(n.Content)
		r.w.WriteString("</div>")
	case *model.TextAuthor:
		r.w.WriteString(`<p class="author">`)
		r.inlines(n.Inlines)
		r.w.WriteString("</p>")
		r.text.WriteString("\n")
	case *model.Image:
		r.w.WriteString(`<p class="img">[изображение]</p>`)
	}
}

func (r *previewRenderer) inlines(in []model.Inline) {
	for _, x := range in {
		switch n := x.(type) {
		case *model.Text:
			r.w.WriteString(html.EscapeString(n.Value))
			r.text.WriteString(n.Value)
		case *model.Styled:
			tag := previewTag(n.Kind)
			if tag != "" {
				r.w.WriteString("<" + tag + ">")
			}
			r.inlines(n.Children)
			if tag != "" {
				r.w.WriteString("</" + tag + ">")
			}
		case *model.Link:
			r.inlines(n.Children)
		}
	}
}

func previewTag(k model.StyleKind) string {
	switch k {
	case model.StyleStrong:
		return "b"
	case model.StyleEmphasis:
		return "i"
	case model.StyleStrikethrough:
		return "s"
	case model.StyleSub:
		return "sub"
	case model.StyleSup:
		return "sup"
	case model.StyleCode:
		return "code"
	default:
		return ""
	}
}

// previewBlocksText flattens title/heading blocks to a plain label string.
// Duplicated (rather than imported) from core/epub/plan.go's blocksText to
// keep the preview feature independent of the epub package.
func previewBlocksText(blocks []model.Block) string {
	var parts []string
	for _, b := range blocks {
		switch n := b.(type) {
		case *model.Paragraph:
			if s := previewInlinesText(n.Inlines); s != "" {
				parts = append(parts, s)
			}
		case *model.Subtitle:
			if s := previewInlinesText(n.Inlines); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func previewInlinesText(in []model.Inline) string {
	var sb strings.Builder
	var walk func([]model.Inline)
	walk = func(xs []model.Inline) {
		for _, x := range xs {
			switch n := x.(type) {
			case *model.Text:
				sb.WriteString(n.Value)
			case *model.Styled:
				walk(n.Children)
			case *model.Link:
				walk(n.Children)
			}
		}
	}
	walk(in)
	return strings.TrimSpace(sb.String())
}

// foldIndexAllRunes returns rune-index ranges [start,end) in text where
// queryLower (already lowercased) occurs, case-insensitively.
func foldIndexAllRunes(text, queryLower []rune) [][2]int {
	var out [][2]int
	if len(queryLower) == 0 {
		return out
	}
	for i := 0; i+len(queryLower) <= len(text); i++ {
		match := true
		for j, q := range queryLower {
			if unicode.ToLower(text[i+j]) != q {
				match = false
				break
			}
		}
		if match {
			out = append(out, [2]int{i, i + len(queryLower)})
		}
	}
	return out
}

// previewRuneContext returns up to 12 runes on each side of [start,end) with
// the match marked, mirroring core/rules/apply.go's runeContext convention
// (same 12-rune radius, same ⟦⟧ markers) so the frontend's existing
// ⟦⟧-highlight rendering can be reused unchanged for search results.
func previewRuneContext(text []rune, start, end int) string {
	lo, hi := start-12, end+12
	if lo < 0 {
		lo = 0
	}
	if hi > len(text) {
		hi = len(text)
	}
	return string(text[lo:start]) + "⟦" + string(text[start:end]) + "⟧" + string(text[end:hi])
}
