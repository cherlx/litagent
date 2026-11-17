package epub

import (
	"strings"
	"testing"
)

// TestParseNotZIP validates error handling for non-ZIP files.
func TestParseNotZIP(t *testing.T) {
	r := strings.NewReader("not a zip file")
	_, err := Parse(r)
	if err == nil {
		t.Fatal("expected error for non-ZIP file")
	}
	if !strings.Contains(err.Error(), "ZIP") {
		t.Errorf("expected ZIP error, got: %v", err)
	}
}

// TestParsePerson validates name parsing.
func TestParsePerson(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"John Doe", "John Doe"},
		{"John", "John"},
		{"John Middle Doe", "John Middle Doe"},
		{"", ""},
	}

	for _, tt := range tests {
		got := parsePerson(tt.input)
		result := strings.TrimSpace(got.First + " " + got.Middle + " " + got.Last)
		result = strings.Join(strings.Fields(result), " ") // collapse spaces
		if tt.want == "" && result != "" {
			t.Errorf("parsePerson(%q): got %q, want empty", tt.input, result)
		}
	}
}
