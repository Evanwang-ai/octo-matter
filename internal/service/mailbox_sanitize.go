package service

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

var mailboxAllowedTags = map[string]bool{
	"p": true, "br": true, "div": true, "span": true, "a": true, "img": true,
	"table": true, "thead": true, "tbody": true, "tr": true, "td": true, "th": true,
	"ul": true, "ol": true, "li": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"blockquote": true, "pre": true, "code": true, "b": true, "i": true, "strong": true, "em": true,
}

var mailboxVoidTags = map[string]bool{
	"br": true, "img": true,
}

var mailboxDropContentTags = map[string]bool{
	"script": true, "style": true, "iframe": true, "object": true, "embed": true,
}

var mailboxWhitespace = regexp.MustCompile(`\s+`)

// SanitizeEmailHTML turns untrusted email/system-letter HTML into a small
// allowlisted HTML subset plus plain text and a short list snippet.
func SanitizeEmailHTML(rawHTML string) (cleanHTML, plainText, snippet string) {
	z := html.NewTokenizer(strings.NewReader(rawHTML))
	var out bytes.Buffer
	var text strings.Builder
	var dropStack []string

	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			plainText = normalizeMailboxText(text.String())
			return out.String(), plainText, mailboxSnippet(plainText, 200)
		case html.TextToken:
			if len(dropStack) > 0 {
				continue
			}
			s := string(z.Text())
			if s == "" {
				continue
			}
			text.WriteString(s)
			text.WriteByte(' ')
			out.WriteString(html.EscapeString(s))
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			name := strings.ToLower(tok.Data)
			if mailboxDropContentTags[name] {
				dropStack = append(dropStack, name)
				continue
			}
			if len(dropStack) > 0 || !mailboxAllowedTags[name] {
				continue
			}
			out.WriteByte('<')
			out.WriteString(name)
			for _, attr := range sanitizedMailboxAttrs(name, tok.Attr) {
				out.WriteByte(' ')
				out.WriteString(attr.Key)
				out.WriteString(`="`)
				out.WriteString(html.EscapeString(attr.Val))
				out.WriteByte('"')
			}
			if tt == html.SelfClosingTagToken || mailboxVoidTags[name] {
				out.WriteString("/>")
			} else {
				out.WriteByte('>')
			}
			if name == "br" || name == "p" || name == "div" || name == "li" || strings.HasPrefix(name, "h") {
				text.WriteByte(' ')
			}
		case html.EndTagToken:
			tok := z.Token()
			name := strings.ToLower(tok.Data)
			if len(dropStack) > 0 {
				if dropStack[len(dropStack)-1] == name {
					dropStack = dropStack[:len(dropStack)-1]
				}
				continue
			}
			if !mailboxAllowedTags[name] || mailboxVoidTags[name] {
				continue
			}
			out.WriteString("</")
			out.WriteString(name)
			out.WriteByte('>')
			if name == "p" || name == "div" || name == "li" || strings.HasPrefix(name, "h") {
				text.WriteByte(' ')
			}
		}
	}
}

func sanitizedMailboxAttrs(tag string, attrs []html.Attribute) []html.Attribute {
	out := make([]html.Attribute, 0, len(attrs)+2)
	for _, attr := range attrs {
		key := strings.ToLower(strings.TrimSpace(attr.Key))
		if key == "" || strings.HasPrefix(key, "on") {
			continue
		}
		val := strings.TrimSpace(attr.Val)
		switch tag {
		case "a":
			if key == "href" && isSafeMailboxHref(val) {
				out = append(out, html.Attribute{Key: "href", Val: val})
			} else if key == "title" {
				out = append(out, html.Attribute{Key: "title", Val: val})
			}
		case "img":
			switch key {
			case "src":
				if isSafeMailboxImageSrc(val) {
					out = append(out, html.Attribute{Key: "src", Val: val})
				}
			case "alt", "title", "width", "height":
				out = append(out, html.Attribute{Key: key, Val: val})
			}
		case "td", "th":
			if key == "colspan" || key == "rowspan" {
				out = append(out, html.Attribute{Key: key, Val: val})
			}
		}
	}
	if tag == "a" {
		out = append(out,
			html.Attribute{Key: "target", Val: "_blank"},
			html.Attribute{Key: "rel", Val: "noopener noreferrer"},
		)
	}
	return out
}

func isSafeMailboxHref(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return false
	}
	s := strings.ToLower(u.Scheme)
	return s == "http" || s == "https"
}

func isSafeMailboxImageSrc(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return false
	}
	s := strings.ToLower(u.Scheme)
	if s == "http" || s == "https" {
		return true
	}
	if s != "data" {
		return false
	}
	lower := strings.ToLower(raw)
	return strings.HasPrefix(lower, "data:image/png;") ||
		strings.HasPrefix(lower, "data:image/jpeg;") ||
		strings.HasPrefix(lower, "data:image/gif;") ||
		strings.HasPrefix(lower, "data:image/webp;")
}

func normalizeMailboxText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(mailboxWhitespace.ReplaceAllString(s, " "))
}

func mailboxSnippet(s string, max int) string {
	if max <= 0 {
		return ""
	}
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= max {
		return string(rs)
	}
	return string(rs[:max])
}
