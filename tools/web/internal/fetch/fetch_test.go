package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// local is a client that may also reach loopback, where httptest serves;
// every other refusal stands.
func local(cfg Config) *Client {
	c := New(cfg)
	c.allow = func(ip netip.Addr) bool { return ip.Unmap().IsLoopback() || Public(ip) }
	return c
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

const page = `<!doctype html><html><head><title> A  page </title><style>p{color:red}</style>
<script>alert("no")</script></head><body>
<nav><a href="/home">Home</a> | <a href="/about">About</a></nav>
<header>The site's banner</header>
<main><h1>Hello</h1><p>Some <b>words</b> and <a href="/more">a link</a>.</p>
<div hidden>not shown</div><ul><li>one</li><li>two</li></ul></main>
<footer>© someone</footer></body></html>`

func TestHTMLIsReadAsMarkdownWithoutTheChrome(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
	p, err := local(Config{}).Fetch(context.Background(), s.URL+"/x", 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "A page" || p.ContentType != "text/html" || p.Status != 200 || p.URL != s.URL+"/x" || p.Truncated {
		t.Fatalf("page %+v", p)
	}
	for _, want := range []string{"# Hello", "Some **words** and [a link](" + s.URL + "/more).", "- one\n- two"} {
		if !strings.Contains(p.Text, want) {
			t.Fatalf("no %q in:\n%s", want, p.Text)
		}
	}
	for _, not := range []string{"alert", "color:red", "Home", "banner", "not shown", "someone", "A page"} {
		if strings.Contains(p.Text, not) {
			t.Fatalf("%q in:\n%s", not, p.Text)
		}
	}
}

func TestACharsetIsReadAsUTF8(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=iso-8859-1")
		_, _ = w.Write([]byte("caf\xe9"))
	})
	p, err := local(Config{}).Fetch(context.Background(), s.URL, 0)
	if err != nil || p.Text != "café" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestTextAsItIsAndNotTextRefused(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/j":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"a":1}`))
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.4"))
		case "/gone":
			http.Error(w, "gone", http.StatusNotFound)
		}
	})
	c := local(Config{})
	if p, err := c.Fetch(context.Background(), s.URL+"/j", 0); err != nil || p.Text != `{"a":1}` {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := c.Fetch(context.Background(), s.URL+"/pdf", 0); err == nil || !strings.Contains(err.Error(), "application/pdf, not text") {
		t.Fatalf("err %v", err)
	}
	if _, err := c.Fetch(context.Background(), s.URL+"/gone", 0); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err %v", err)
	}
}

func TestLimits(t *testing.T) {
	long := strings.Repeat("word ", 1000) // 5000 chars
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/long":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(long))
		case "/slow":
			time.Sleep(2 * time.Second)
		default: // a redirect to itself, for ever
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	})
	ctx := context.Background()
	c := local(Config{MaxChars: 100, MaxBytes: 1000, MaxRedirects: 3, Timeout: 500 * time.Millisecond})

	p, err := c.Fetch(ctx, s.URL+"/long", 0)
	if err != nil || len(p.Text) > 100 || !p.Truncated {
		t.Fatalf("max chars: %d %v %v", len(p.Text), p.Truncated, err)
	}
	if p, _ = c.Fetch(ctx, s.URL+"/long", 20); len(p.Text) > 20 {
		t.Fatalf("a call's max_chars: %q", p.Text)
	}
	if p, _ = c.Fetch(ctx, s.URL+"/long", 100000); len(p.Text) > 100 {
		t.Fatalf("a call asked past the most: %d", len(p.Text))
	}
	if p, _ = local(Config{MaxBytes: 1000}).Fetch(ctx, s.URL+"/long", 0); len(p.Text) > 1000 || !p.Truncated {
		t.Fatalf("max bytes: %d %v", len(p.Text), p.Truncated)
	}
	if _, err := c.Fetch(ctx, s.URL+"/loop", 0); err == nil || !strings.Contains(err.Error(), "more than 3 redirects") {
		t.Fatalf("redirects: %v", err)
	}
	if _, err := c.Fetch(ctx, s.URL+"/slow", 0); err == nil {
		t.Fatal("the timeout did not hold")
	}
}

func TestPublic(t *testing.T) {
	for _, s := range []string{"93.184.215.14", "1.1.1.1", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if !Public(netip.MustParseAddr(s)) {
			t.Errorf("%s refused", s)
		}
	}
	for _, s := range []string{
		"127.0.0.1", "127.1.2.3", "10.0.0.1", "172.16.5.4", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "0.0.0.0", "0.1.2.3", "224.0.0.1", "255.255.255.255", "240.0.0.1", "192.0.0.170",
		"::1", "::", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1", "fec0::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "::127.0.0.1",
		"64:ff9b::a00:1", "2002:a00:1::", "2001:0:a00:1::",
	} {
		if Public(netip.MustParseAddr(s)) {
			t.Errorf("%s allowed", s)
		}
	}
}

// The url as it is asked: none of these leave the process.
func TestNothingIntoTheMachineOrTheStack(t *testing.T) {
	c := New(Config{Timeout: 2 * time.Second})
	for _, u := range []string{
		"http://127.0.0.1/", "http://127.0.0.1:8080/mcp", "http://10.1.2.3/", "http://192.168.0.1/",
		"http://169.254.169.254/latest/meta-data/", "http://[::1]/", "http://[::ffff:127.0.0.1]/",
		"http://[fe80::1]/", "http://0.0.0.0/", "http://localhost/", "http://localhost./",
		"file:///etc/passwd", "ftp://example.com/", "gopher://x/", "http:///nohost",
		// credentials in the url, built here so no url with a password is written down
		(&url.URL{Scheme: "http", User: url.UserPassword("user", "pw"), Host: "example.com", Path: "/"}).String(),
	} {
		_, err := c.Fetch(context.Background(), u, 0)
		var r *Refused
		if !errors.As(err, &r) {
			t.Errorf("%s: %v, not refused", u, err)
		}
	}
}

// A public page redirecting into the stack: the redirect is not followed.
func TestARedirectToAPrivateAddressIsRefused(t *testing.T) {
	for _, to := range []string{"http://10.0.0.1/", "http://169.254.169.254/latest/meta-data/", "http://[fd00::1]/", "http://[::ffff:10.0.0.1]/"} {
		s := serve(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) })
		c := New(Config{Timeout: 2 * time.Second})
		// The server itself may be reached (it stands for a public one); its
		// redirect may not.
		c.allow = func(ip netip.Addr) bool { return ip == netip.MustParseAddr("127.0.0.1") || Public(ip) }
		_, err := c.Fetch(context.Background(), "http://"+s.Listener.Addr().String()+"/", 0)
		var r *Refused
		if !errors.As(err, &r) || !strings.Contains(err.Error(), "not a public address") {
			t.Errorf("redirect to %s: %v", to, err)
		}
	}
}

// A name that resolves to a public address when checked and to a private
// one when dialled (DNS rebinding): the dial refuses it, nothing connects.
func TestDNSRebindingIsRefusedAtTheDial(t *testing.T) {
	var hits atomic.Int32
	s := serve(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	u, _ := url.Parse(s.URL)
	var asked atomic.Int32
	c := New(Config{Timeout: 2 * time.Second})
	c.lookup = func(context.Context, string) ([]netip.Addr, error) {
		if asked.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("93.184.215.14")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	_, err := c.Fetch(context.Background(), "http://rebind.example:"+u.Port()+"/", 0)
	var r *Refused
	if !errors.As(err, &r) || !strings.Contains(err.Error(), "rebind.example resolves to 127.0.0.1") {
		t.Fatalf("err %v", err)
	}
	if asked.Load() < 2 || hits.Load() != 0 {
		t.Fatalf("asked %d, hits %d", asked.Load(), hits.Load())
	}
}

// A name with a public and a private address is refused whole.
func TestANameWithAnyPrivateAddressIsRefused(t *testing.T) {
	c := New(Config{})
	c.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.215.14"), netip.MustParseAddr("10.0.0.5")}, nil
	}
	_, err := c.Fetch(context.Background(), "https://mixed.example/", 0)
	var r *Refused
	if !errors.As(err, &r) {
		t.Fatalf("err %v", err)
	}
}

// The fetcher over HTTP, as the MCP server asks it.
func TestRemote(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	})
	fetcher := httptest.NewServer(Handler(local(Config{})))
	t.Cleanup(fetcher.Close)
	r := NewRemote(fetcher.URL, 5*time.Second)
	p, err := r.Fetch(context.Background(), s.URL, 50)
	if err != nil || p.Title != "A page" || !strings.Contains(p.Text, "# Hello") || !p.Truncated {
		t.Fatalf("%+v %v", p, err)
	}
	_, err = r.Fetch(context.Background(), "http://169.254.169.254/", 0)
	var ref *Refused
	if !errors.As(err, &ref) || !strings.Contains(err.Error(), "169.254.169.254 is not a public address") {
		t.Fatalf("err %v", err)
	}
	if _, err := NewRemote("http://127.0.0.1:1", time.Second).Fetch(context.Background(), s.URL, 0); err == nil {
		t.Fatal("a fetcher gone answered")
	}
}
