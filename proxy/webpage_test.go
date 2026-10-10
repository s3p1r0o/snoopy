package main

import (
	"net/url"
	"strings"
	"testing"
)

func testPage(t *testing.T, contentType, body string) *page {
	t.Helper()
	u, _ := url.Parse("https://news.example/story")
	p := &page{URL: u, Status: 200, rawType: contentType, Body: []byte(body)}
	p.ContentType = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	return p
}

func TestPageMarkdownKeepsMainContent(t *testing.T) {
	body := `<html><head><title>Big story</title></head><body>
<nav>Home | Sport | Weather</nav>
<article><h1>Big story</h1><p>` + strings.Repeat("Facts about the event. ", 30) +
		`<a href="/more">Read more</a></p></article><footer>© News Inc</footer></body></html>`
	title, md, err := pageMarkdown(testPage(t, "text/html; charset=utf-8", body), maxPageMarkdownChars)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Big story" {
		t.Errorf("title = %q", title)
	}
	if !strings.Contains(md, "Facts about the event.") || !strings.Contains(md, "(https://news.example/more)") {
		t.Errorf("main content or absolute link missing:\n%s", md)
	}
	if strings.Contains(md, "Weather") || strings.Contains(md, "News Inc") {
		t.Errorf("navigation or footer kept:\n%s", md)
	}
}

func TestPageMarkdownDecodesLatin1(t *testing.T) {
	// "Genève, l'été" in ISO-8859-1.
	body := "<html><body><p>Gen\xe8ve, l'\xe9t\xe9</p></body></html>"
	_, md, err := pageMarkdown(testPage(t, "text/html; charset=iso-8859-1", body), maxPageMarkdownChars)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "Genève, l'été") {
		t.Fatalf("Latin-1 not decoded: %q", md)
	}
}

func TestPageMarkdownShortPagesConvertWhole(t *testing.T) {
	_, md, err := pageMarkdown(testPage(t, "text/html", `<html><body><p>Tiny <b>page</b></p></body></html>`), maxPageMarkdownChars)
	if err != nil {
		t.Fatal(err)
	}
	if md != "Tiny **page**" {
		t.Fatalf("md = %q", md)
	}
}

func TestPageMarkdownPassesTextAndRejectsBinary(t *testing.T) {
	_, md, err := pageMarkdown(testPage(t, "application/json", `{"a":1}`), maxPageMarkdownChars)
	if err != nil || md != `{"a":1}` {
		t.Fatalf("json: md=%q err=%v", md, err)
	}
	if _, _, err := pageMarkdown(testPage(t, "image/png", "\x89PNG"), maxPageMarkdownChars); err == nil || !strings.Contains(err.Error(), "unsupported content type") {
		t.Fatalf("png: err = %v", err)
	}
}

func TestCapCharsCutsOnRunes(t *testing.T) {
	got := capChars("ééééé", 3)
	if !strings.HasPrefix(got, "ééé\n\n[truncated") {
		t.Fatalf("got %q", got)
	}
	if capChars("abc", 3) != "abc" {
		t.Fatal("short strings must pass through")
	}
}

func TestPageMarkdownKeepsUndeclaredUTF8(t *testing.T) {
	// No charset in the header and the <meta> past the 1024 bytes the sniffer reads:
	// valid UTF-8 must stay UTF-8 instead of being read as windows-1252.
	head := "<html><head><title>T</title>" + strings.Repeat("<!-- padding -->", 100) + `<meta charset="utf-8"></head>`
	_, md, err := pageMarkdown(testPage(t, "text/html", head+"<body><p>Genève, l'été est là</p></body></html>"), maxPageMarkdownChars)
	if err != nil || !strings.Contains(md, "Genève, l'été est là") {
		t.Fatalf("html: md=%q err=%v", md, err)
	}
	_, md, err = pageMarkdown(testPage(t, "application/json", `{"pad":"`+strings.Repeat("x", 1100)+`","city":"Genève"}`), maxPageMarkdownChars)
	if err != nil || !strings.Contains(md, `"city":"Genève"`) {
		t.Fatalf("json: md=%q err=%v", md[len(md)-40:], err)
	}
}
