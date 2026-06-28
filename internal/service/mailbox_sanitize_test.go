package service

import (
	"strings"
	"testing"
)

func TestSanitizeEmailHTMLDropsActiveContent(t *testing.T) {
	raw := `<div onclick="steal()">Hello <script>alert(1)</script><style>body{display:none}</style>` +
		`<a href="javascript:alert(1)" onmouseover="x()">bad</a>` +
		`<a href="https://example.com/path?q=1">good</a>` +
		`<iframe src="https://evil.example">hidden</iframe></div>`

	clean, plain, snippet := SanitizeEmailHTML(raw)

	for _, bad := range []string{"script", "style", "onclick", "onmouseover", "javascript:", "iframe", "hidden"} {
		if strings.Contains(strings.ToLower(clean), bad) {
			t.Fatalf("clean html still contains %q: %s", bad, clean)
		}
	}
	if strings.Contains(plain, "alert") || strings.Contains(plain, "hidden") {
		t.Fatalf("plain text kept dropped content: %q", plain)
	}
	if !strings.Contains(clean, `href="https://example.com/path?q=1"`) {
		t.Fatalf("safe https link was not preserved: %s", clean)
	}
	if !strings.Contains(clean, `rel="noopener noreferrer"`) {
		t.Fatalf("safe link missing rel hardening: %s", clean)
	}
	if snippet != plain {
		t.Fatalf("short snippet should equal plain text, got %q want %q", snippet, plain)
	}
}

func TestSanitizeEmailHTMLImageAllowlist(t *testing.T) {
	raw := `<img src="https://cdn.example/a.png" onerror="x()" alt="ok">` +
		`<img src="data:image/png;base64,AAAA" alt="data">` +
		`<img src="data:image/svg+xml;base64,PHN2Zy8+" alt="svg">` +
		`<img src="file:///etc/passwd" alt="file">`

	clean, _, _ := SanitizeEmailHTML(raw)

	if !strings.Contains(clean, `src="https://cdn.example/a.png"`) {
		t.Fatalf("https image was not preserved: %s", clean)
	}
	if !strings.Contains(clean, `src="data:image/png;base64,AAAA"`) {
		t.Fatalf("safe data image was not preserved: %s", clean)
	}
	if strings.Contains(clean, "svg+xml") || strings.Contains(clean, "file:///") || strings.Contains(clean, "onerror") {
		t.Fatalf("unsafe image attribute was preserved: %s", clean)
	}
}

func TestMailboxSnippetUsesRunes(t *testing.T) {
	input := strings.Repeat("界", 205)
	_, plain, snippet := SanitizeEmailHTML("<p>" + input + "</p>")

	if len([]rune(plain)) != 205 {
		t.Fatalf("plain text length = %d, want 205", len([]rune(plain)))
	}
	if len([]rune(snippet)) != 200 {
		t.Fatalf("snippet length = %d, want 200", len([]rune(snippet)))
	}
}
