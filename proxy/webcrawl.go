package main

// webcrawl.go backs the web_crawl tool: a polite breadth-first crawl from one
// URL over the same host (optionally under one path prefix), honouring the
// site's robots.txt, with page, depth, size and time limits. Every request
// goes through the fetcher, so its address checks and caps apply.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/temoto/robotstxt"
)

const (
	defaultCrawlPages   = 10
	maxCrawlPages       = 20
	defaultCrawlDepth   = 2
	maxCrawlDepth       = 3
	maxCrawlPageChars   = 4_000
	maxCrawlOutputChars = 60_000
	crawlTimeout        = 90 * time.Second
	robotsAgent         = "snoopy-proxy"
)

// crawlDelay spaces requests to the crawled site; tests set it to zero.
var crawlDelay = 250 * time.Millisecond

type crawlOptions struct {
	maxPages, maxDepth int
	pathPrefix         string // only follow links under this path; "" for the whole host
}

// crawl visits up to opt.maxPages pages from start and renders each one.
func (f *fetcher) crawl(ctx context.Context, start string, opt crawlOptions) (string, error) {
	root, err := url.Parse(strings.TrimSpace(start))
	if err != nil || (root.Scheme != "http" && root.Scheme != "https") || root.Host == "" {
		return "", fmt.Errorf("invalid URL %q: only absolute http(s) URLs can be crawled", start)
	}
	root.Fragment = ""
	ctx, cancel := context.WithTimeout(ctx, crawlTimeout)
	defer cancel()
	robots := f.robots(ctx, root)

	type item struct {
		u     *url.URL
		depth int
	}
	queue := []item{{root, 0}}
	seen := map[string]bool{root.String(): true}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Crawl of %s (same host, depth <= %d, at most %d pages)\n", root, opt.maxDepth, opt.maxPages)
	pages, blocked := 0, 0
	for len(queue) > 0 && pages < opt.maxPages {
		it := queue[0]
		queue = queue[1:]
		if !robots.TestAgent(it.u.RequestURI(), robotsAgent) {
			blocked++
			continue
		}
		if pages > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(crawlDelay):
			}
		}
		if ctx.Err() != nil {
			sb.WriteString("\n[stopped: the crawl time limit was reached]\n")
			break
		}
		pages++
		fmt.Fprintf(&sb, "\n## %s\n", it.u)
		p, err := f.get(ctx, it.u.String())
		switch {
		case err != nil:
			fmt.Fprintf(&sb, "(fetch failed: %v)\n", err)
			continue
		case p.Status != 200:
			fmt.Fprintf(&sb, "(status %d)\n", p.Status)
			continue
		}
		if pages == 1 && p.URL.Host != root.Host {
			// The start URL redirected to another host (apex -> www): crawl
			// that host, under its own robots.txt.
			root = &url.URL{Scheme: p.URL.Scheme, Host: p.URL.Host}
			robots = f.robots(ctx, root)
			seen[p.URL.String()] = true
		}
		title, md, err := pageMarkdown(p, maxCrawlPageChars)
		if err != nil {
			fmt.Fprintf(&sb, "(skipped: %v)\n", err)
			continue
		}
		if title != "" {
			fmt.Fprintf(&sb, "Title: %s\n", title)
		}
		sb.WriteString("\n" + md + "\n")
		if sb.Len() > maxCrawlOutputChars {
			sb.WriteString("\n[stopped: the crawl output limit was reached]\n")
			break
		}
		if it.depth < opt.maxDepth {
			for _, link := range pageLinks(p) {
				if link.Host == root.Host && strings.HasPrefix(link.Path, opt.pathPrefix) && !seen[link.String()] {
					seen[link.String()] = true
					queue = append(queue, item{link, it.depth + 1})
				}
			}
		}
	}
	fmt.Fprintf(&sb, "\n(%d pages fetched", pages)
	if blocked > 0 {
		fmt.Fprintf(&sb, ", %d skipped by robots.txt", blocked)
	}
	sb.WriteString(")")
	return sb.String(), nil
}

// robots returns the site's robots.txt rules. Following RFC 9309, a missing
// file allows everything and an unreachable one (server or network error)
// disallows everything; that verdict lives on RobotsData, so callers must use
// RobotsData.TestAgent (a Group alone does not carry it).
func (f *fetcher) robots(ctx context.Context, root *url.URL) *robotstxt.RobotsData {
	robotsURL := root.Scheme + "://" + root.Host + "/robots.txt"
	status, body := 500, []byte(nil)
	if p, err := f.get(ctx, robotsURL); err == nil {
		status, body = p.Status, p.Body
	}
	data, err := robotstxt.FromStatusAndBytes(status, body)
	if err != nil {
		data, _ = robotstxt.FromStatusAndBytes(500, nil)
	}
	return data
}

// pageLinks returns the absolute http(s) links of an HTML page, without fragments.
func pageLinks(p *page) []*url.URL {
	if p.ContentType != "text/html" && p.ContentType != "application/xhtml+xml" {
		return nil
	}
	decoded, err := decodeBody(p)
	if err != nil {
		return nil
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(decoded))
	if err != nil {
		return nil
	}
	var links []*url.URL
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		u, err := p.URL.Parse(strings.TrimSpace(href))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return
		}
		u.Fragment = ""
		links = append(links, u)
	})
	return links
}
