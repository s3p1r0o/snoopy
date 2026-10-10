package main

// webpage.go turns a fetched page into the Markdown the model reads: the
// page's charset is decoded to UTF-8, Readability keeps the main content of
// HTML pages (whole-page conversion when it finds too little), and the result
// is capped.

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html/charset"
)

const (
	maxPageMarkdownChars = 40_000
	// minReadableChars is the main-content size below which Readability's
	// extraction is distrusted and the whole page is converted instead.
	minReadableChars = 200
)

// textTypes are the non-HTML media types passed through as text.
var textTypes = map[string]bool{
	"text/plain": true, "text/markdown": true, "text/csv": true,
	"application/json": true, "application/xml": true, "text/xml": true,
}

// pageMarkdown renders p for the model: its title (empty when unknown) and
// its content as Markdown, at most max characters.
func pageMarkdown(p *page, max int) (title, md string, err error) {
	html := p.ContentType == "text/html" || p.ContentType == "application/xhtml+xml"
	if !html && !textTypes[p.ContentType] {
		return "", "", fmt.Errorf("unsupported content type %q", p.ContentType)
	}
	decoded, err := decodeBody(p)
	if err != nil {
		return "", "", err
	}
	if !html {
		return "", capChars(strings.TrimSpace(decoded), max), nil
	}
	source := decoded
	if art, err := readability.FromReader(strings.NewReader(decoded), p.URL); err == nil &&
		len(strings.TrimSpace(art.TextContent)) >= minReadableChars {
		source, title = art.Content, strings.TrimSpace(art.Title)
	}
	md, err = htmltomarkdown.ConvertString(source, converter.WithDomain(p.URL.Scheme+"://"+p.URL.Host))
	if err != nil {
		return "", "", fmt.Errorf("convert HTML: %w", err)
	}
	return title, capChars(strings.TrimSpace(md), max), nil
}

// decodeBody returns p's body as UTF-8, using its declared or sniffed charset.
// A charset that was only guessed (no header parameter, no BOM; the sniffer
// reads the first 1024 bytes and falls back to windows-1252) loses to a body
// that is valid UTF-8, the common case of servers that omit the charset.
func decodeBody(p *page) (string, error) {
	enc, _, certain := charset.DetermineEncoding(p.Body, p.rawType)
	if !certain && utf8.Valid(p.Body) {
		return string(p.Body), nil
	}
	b, err := io.ReadAll(enc.NewDecoder().Reader(bytes.NewReader(p.Body)))
	if err != nil {
		return "", fmt.Errorf("decode charset: %w", err)
	}
	return string(b), nil
}

// capChars cuts s to at most max characters, on a rune boundary, with a note.
func capChars(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "\n\n[truncated: the content continues]"
}
