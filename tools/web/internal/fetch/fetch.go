// Package fetch reads a page off the public web and hands back its words:
// HTML as markdown, other text as it is. It reaches the public internet
// only: every address it would connect to, on the first url, on every
// redirect and at the dial itself, is checked (guard.go), so a url cannot
// lead it into the machine or the stack it runs in.
package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"
)

// Config is fetch's limits.
type Config struct {
	Timeout      time.Duration // the whole fetch, redirects included
	MaxBytes     int64         // read of a body at most; the rest is left unread
	MaxChars     int           // returned at most, and the most a call may ask for
	MaxRedirects int
	UserAgent    string
}

// Defaults are the limits when none are set.
var Defaults = Config{
	Timeout:      20 * time.Second,
	MaxBytes:     5 << 20,
	MaxChars:     20000,
	MaxRedirects: 5,
	UserAgent:    "units-tools-web/1 (+https://github.com/jurasikasan/units)",
}

// Page is what was read: where it ended up and its words.
type Page struct {
	URL         string `json:"url" jsonschema:"where the page was read, after redirects"`
	Status      int    `json:"status" jsonschema:"the HTTP status it answered"`
	ContentType string `json:"content_type" jsonschema:"its media type"`
	Title       string `json:"title,omitempty" jsonschema:"its title, for HTML"`
	Text        string `json:"text" jsonschema:"its words: HTML as markdown, other text as it is"`
	Truncated   bool   `json:"truncated,omitempty" jsonschema:"true when the page was longer than what is returned"`
}

// Fetcher reads a page: here (Client) or in an egress-only process
// (Remote).
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string, maxChars int) (*Page, error)
}

// Client fetches pages from this process.
type Client struct {
	cfg    Config
	http   *http.Client
	allow  func(netip.Addr) bool
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

// New is a client that reaches public addresses only.
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = Defaults.Timeout
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = Defaults.MaxBytes
	}
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = Defaults.MaxChars
	}
	if cfg.MaxRedirects <= 0 {
		cfg.MaxRedirects = Defaults.MaxRedirects
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = Defaults.UserAgent
	}
	c := &Client{cfg: cfg, allow: Public, lookup: lookupIP}
	c.http = &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{
			Proxy:                 nil, // never a proxy: the dial below must see the real address
			DialContext:           c.dial,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: cfg.Timeout,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: c.checkRedirect,
	}
	return c
}

func lookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > c.cfg.MaxRedirects {
		return &Refused{fmt.Sprintf("more than %d redirects", c.cfg.MaxRedirects)}
	}
	if err := checkURL(req.URL); err != nil {
		return err
	}
	return c.checkHost(req.Context(), req.URL.Hostname())
}

// Fetch GETs rawURL and returns its words, at most maxChars of them (the
// configured most when 0 or more).
func (c *Client) Fetch(ctx context.Context, rawURL string, maxChars int) (*Page, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, &Refused{fmt.Sprintf("not a url: %v", err)}
	}
	if err := checkURL(u); err != nil {
		return nil, err
	}
	if err := c.checkHost(ctx, u.Hostname()); err != nil {
		return nil, err
	}
	if maxChars <= 0 || maxChars > c.cfg.MaxChars {
		maxChars = c.cfg.MaxChars
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5")
	resp, err := c.http.Do(req)
	if err != nil {
		var r *Refused
		if errors.As(err, &r) {
			return nil, r
		}
		return nil, fmt.Errorf("the page did not answer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	final := resp.Request.URL.String()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s answered %s", final, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("the page broke off: %w", err)
	}
	cut := int64(len(body)) > c.cfg.MaxBytes
	if cut {
		body = body[:c.cfg.MaxBytes]
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(body)
	}
	mt, _, _ := mime.ParseMediaType(ct)
	p := &Page{URL: final, Status: resp.StatusCode, ContentType: mt}
	switch {
	case mt == "text/html" || mt == "application/xhtml+xml":
		p.Title, p.Text, err = markdown(body, ct, resp.Request.URL)
		if err != nil {
			return nil, fmt.Errorf("the page could not be read: %w", err)
		}
	case textual(mt):
		r, err := charset.NewReader(bytes.NewReader(body), ct)
		if err != nil {
			return nil, fmt.Errorf("the page's charset: %w", err)
		}
		b, _ := io.ReadAll(r)
		p.Text = strings.ToValidUTF8(string(b), "�")
	default:
		return nil, fmt.Errorf("%s is %s, not text: it is not read", final, mt)
	}
	p.Text, p.Truncated = clip(strings.TrimSpace(p.Text), maxChars)
	p.Truncated = p.Truncated || cut
	return p, nil
}

// textual is a media type read as text as it comes.
func textual(mt string) bool {
	return strings.HasPrefix(mt, "text/") ||
		mt == "application/json" || mt == "application/xml" || mt == "application/javascript" ||
		strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "+xml")
}

// clip keeps at most n characters (runes), cut at a line or word end when
// one is near.
func clip(s string, n int) (string, bool) {
	if utf8.RuneCountInString(s) <= n {
		return s, false
	}
	i, k := 0, 0
	for i = range s {
		if k == n {
			break
		}
		k++
	}
	s = s[:i]
	if j := strings.LastIndexAny(s, "\n "); j > len(s)*9/10 {
		s = s[:j]
	}
	return s, true
}

// noise is what is no part of a page's text: its chrome and its machinery.
var noise = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true,
	atom.Nav: true, atom.Header: true, atom.Footer: true, atom.Aside: true,
	atom.Form: true, atom.Iframe: true, atom.Svg: true, atom.Canvas: true,
	atom.Button: true, atom.Select: true, atom.Input: true, atom.Dialog: true,
}

// markdown is an HTML page's title and its body as markdown, links made
// absolute against where it was read.
func markdown(body []byte, contentType string, base *url.URL) (string, string, error) {
	r, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return "", "", err
	}
	doc, err := html.Parse(r)
	if err != nil {
		return "", "", err
	}
	var title string
	var main *html.Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for ch := n.FirstChild; ch != nil; {
			next := ch.NextSibling
			if ch.Type == html.ElementNode {
				switch {
				case ch.DataAtom == atom.Title && title == "":
					title = strings.Join(strings.Fields(text(ch)), " ")
					n.RemoveChild(ch)
				case noise[ch.DataAtom] || hidden(ch):
					n.RemoveChild(ch)
				default:
					if ch.DataAtom == atom.Main && main == nil {
						main = ch
					}
					walk(ch)
				}
			}
			ch = next
		}
	}
	walk(doc)
	root := doc
	if main != nil {
		root = main // a page that says where its content is: that, and not the rest
	}
	md, err := htmltomarkdown.ConvertNode(root, converter.WithDomain(base.Scheme+"://"+base.Host))
	if err != nil {
		return "", "", err
	}
	return title, string(md), nil
}

func hidden(n *html.Node) bool {
	for _, a := range n.Attr {
		if a.Key == "hidden" || (a.Key == "aria-hidden" && a.Val == "true") {
			return true
		}
	}
	return false
}

func text(n *html.Node) string {
	var b strings.Builder
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			f(ch)
		}
	}
	f(n)
	return b.String()
}
