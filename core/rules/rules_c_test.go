package rules

import (
	"strings"
	"testing"

	"litagent/core/model"
)

func findByRule(fs []Finding, id string) *Finding {
	for i := range fs {
		if fs[i].RuleID == id {
			return &fs[i]
		}
	}
	return nil
}

// yo-restore is DISABLED in the pipeline (see catalog.go). The rule code and the
// dictionary are retained, so these tests cover lookupYo directly instead of via
// the pipeline.
func TestYoLookupRestoresAndPreservesCase(t *testing.T) {
	if got, ok := lookupYo("еще"); !ok || got != "ещё" {
		t.Errorf("lookupYo(\"еще\") = %q,%v; want ещё,true", got, ok)
	}
	if got, ok := lookupYo("Еще"); !ok || got != "Ещё" {
		t.Errorf("case not preserved: lookupYo(\"Еще\") = %q,%v; want Ещё,true", got, ok)
	}
}

func TestYoLookupSkipsAmbiguous(t *testing.T) {
	// "все" is ambiguous (все/всё) and must NOT be in the dictionary.
	if got, ok := lookupYo("все"); ok {
		t.Errorf("ambiguous word in dict: lookupYo(\"все\") = %q,%v; want _,false", got, ok)
	}
}

func TestMixedAlphabet(t *testing.T) {
	latinO := "o" // ASCII look-alike of Cyrillic о
	got, fs := runPara("хор" + latinO + "шо")
	if got != "хор"+latinO+"шо" {
		t.Errorf("text mutated by review-only rule: %q", got)
	}
	f := findByRule(fs, "mixed-alpha")
	if f == nil {
		t.Fatal("no mixed-alpha finding")
	}
	if f.After != "хорошо" {
		t.Errorf("proposed = %q, want хорошо", f.After)
	}
}

func TestOCRDigit(t *testing.T) {
	got, fs := runPara("это д0м большой")
	if got != "это д0м большой" {
		t.Errorf("text mutated by review-only rule: %q", got)
	}
	f := findByRule(fs, "ocr-digit")
	if f == nil {
		t.Fatal("no ocr-digit finding")
	}
	if f.Before != "д0м" || f.After != "дом" {
		t.Errorf("finding = %q -> %q, want д0м -> дом", f.Before, f.After)
	}
}

func TestDehyphDictGated(t *testing.T) {
	// Known word → auto-joined (level B), across a space or a line break.
	for _, in := range []string{"сло- во", "сло-\nво"} {
		got, fs := runPara(in)
		if got != "слово" {
			t.Errorf("in %q: known word not auto-joined: %q", in, got)
		}
		if f := findByRule(fs, "dehyph"); f == nil || f.Level != ConfB {
			t.Errorf("in %q: expected B-level dehyph, got %+v", in, f)
		}
	}
	// Unknown joined form → left as-is, suggested only (level C).
	got, fs := runPara("ккк- ппп")
	if got != "ккк- ппп" {
		t.Errorf("unknown join applied: %q", got)
	}
	if f := findByRule(fs, "dehyph"); f == nil || f.Level != ConfC {
		t.Errorf("expected C-level dehyph suggestion, got %+v", f)
	}
	// A real compound (hyphen, no space) is never matched.
	if _, fs3 := runPara("что-то там"); findByRule(fs3, "dehyph") != nil {
		t.Error("compound word wrongly flagged")
	}
}

func TestDehyphSuspendedHyphen(t *testing.T) {
	// A suspended hyphen ("трёх- и четырёхэтажный") must keep its hyphen and not
	// be suggested for joining — the hyphen marks an omitted shared part. (The
	// conjunction may still get a non-breaking space after it from nbsp-prep; that
	// is a separate, correct change, so we assert the hyphen+conjunction prefix.)
	cases := []struct{ in, keep string }{
		{"застроен трех- и четырехэтажными домами", "трех- и"},
		{"одно- или двухкомнатная квартира", "одно- или"},
		{"радио- и телепередачи", "радио- и"},
	}
	for _, c := range cases {
		got, fs := runPara(c.in)
		if !strings.Contains(got, c.keep) {
			t.Errorf("in %q: suspended hyphen altered: %q", c.in, got)
		}
		if f := findByRule(fs, "dehyph"); f != nil {
			t.Errorf("in %q: suspended hyphen wrongly flagged: %+v", c.in, f)
		}
	}
}

func TestPageNumberDetect(t *testing.T) {
	if _, fs := runPara("42"); findByRule(fs, "page-number") == nil {
		t.Error("standalone number not flagged as page number")
	}
	if _, fs := runPara("Глава 42 началась"); findByRule(fs, "page-number") != nil {
		t.Error("normal paragraph wrongly flagged as page number")
	}
}

func TestHeadingDetect(t *testing.T) {
	// ALL-CAPS short line and a chapter-keyword line are detected.
	if _, fs := runPara("ГЛАВА ПЯТАЯ"); findByRule(fs, "heading-detect") == nil {
		t.Error("all-caps heading not detected")
	}
	if _, fs := runPara("Глава пятая"); findByRule(fs, "heading-detect") == nil {
		t.Error("chapter-keyword heading not detected")
	}
	// A normal sentence is not flagged.
	if _, fs := runPara("Был поздний вечер."); findByRule(fs, "heading-detect") != nil {
		t.Error("normal sentence flagged as heading")
	}
}

func TestHeadingDetectApplyGated(t *testing.T) {
	mk := func() (*model.Paragraph, *model.Book) {
		p := &model.Paragraph{Inlines: []model.Inline{&model.Text{Value: "ГЛАВА"}}}
		b := &model.Book{Bodies: []*model.Body{{Sections: []*model.Section{{Content: []model.Block{p}}}}}}
		return p, b
	}
	// Default: detected but not applied.
	p1, b1 := mk()
	DefaultEngine().Run(b1)
	if p1.Heading {
		t.Error("heading applied without acceptance")
	}
	// Accepted: the paragraph is marked as a heading.
	p2, b2 := mk()
	NewEngineFor(func(id string) bool { return id == "heading-detect" }).Run(b2)
	if !p2.Heading {
		t.Error("heading not applied when accepted")
	}
}

func TestGluedWords(t *testing.T) {
	// Two known words run together are suggested (review-only: text unchanged).
	got, fs := runPara("онсказал ей")
	if got != "онсказал ей" {
		t.Errorf("text mutated by review-only rule: %q", got)
	}
	f := findByRule(fs, "glued-words")
	if f == nil {
		t.Fatal("no glued-words finding")
	}
	if f.Before != "онсказал" || f.After != "он сказал" {
		t.Errorf("finding = %q -> %q, want онсказал -> он сказал", f.Before, f.After)
	}
}

func TestGluedWordsCasePreservedAndKnownSkipped(t *testing.T) {
	// A sentence-initial capital is preserved on the split.
	if _, fs := runPara("Онсказал"); findByRule(fs, "glued-words") == nil {
		t.Error("capitalised glued word not detected")
	} else if f := findByRule(fs, "glued-words"); f.After != "Он сказал" {
		t.Errorf("case not preserved: After = %q, want «Он сказал»", f.After)
	}
	// A known dictionary word is never split.
	if _, fs := runPara("состоит"); findByRule(fs, "glued-words") != nil {
		t.Error("known word wrongly flagged as glued")
	}
	// A word with an internal capital (proper-noun/OCR case-boundary) is left to
	// other handling, not treated as a dictionary glue.
	if _, fs := runPara("МакДональд"); findByRule(fs, "glued-words") != nil {
		t.Error("internal-capital word wrongly flagged as glued")
	}
}

func TestSceneBreakDetect(t *testing.T) {
	for _, sep := range []string{"* * *", "***", "###", "———"} {
		if _, fs := runPara(sep); findByRule(fs, "scene-break") == nil {
			t.Errorf("separator %q not detected as scene break", sep)
		}
	}
	// A lone dash and normal prose are NOT scene breaks.
	if _, fs := runPara("—"); findByRule(fs, "scene-break") != nil {
		t.Error("lone dash wrongly flagged as scene break")
	}
	if _, fs := runPara("Обычный текст."); findByRule(fs, "scene-break") != nil {
		t.Error("normal paragraph wrongly flagged as scene break")
	}
}

func TestSceneBreakApplyGated(t *testing.T) {
	mk := func() (*model.Paragraph, *model.Book) {
		p := &model.Paragraph{Inlines: []model.Inline{&model.Text{Value: "***"}}}
		b := &model.Book{Bodies: []*model.Body{{Sections: []*model.Section{{Content: []model.Block{p}}}}}}
		return p, b
	}
	// Default: detected but not applied.
	p1, b1 := mk()
	DefaultEngine().Run(b1)
	if v := p1.Inlines[0].(*model.Text).Value; v != "***" {
		t.Errorf("scene break normalised without acceptance: %q", v)
	}
	// Accepted: normalised to the canonical "* * *".
	p2, b2 := mk()
	NewEngineFor(func(id string) bool { return id == "scene-break" }).Run(b2)
	if v := p2.Inlines[0].(*model.Text).Value; v != "* * *" {
		t.Errorf("scene break not normalised when accepted: %q", v)
	}
}

func TestLevelBApplies(t *testing.T) {
	// Dialogue dash (B) applies; «еще» is left unchanged (yo-restore is disabled).
	got, fs := runPara("- еще не время")
	want := "— еще не" + nbsp + "время" // dialog-dash + nbsp-prep (B)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if findByRule(fs, "dialog-dash") == nil {
		t.Error("expected dialog-dash to apply")
	}
}
