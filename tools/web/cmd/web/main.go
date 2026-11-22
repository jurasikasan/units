// Command web is a tool avatars call to look at the web: two MCP tools,
// search and fetch, over Streamable HTTP (MCP 2026-07-28, stateless).
//
//	web           the MCP server; fetches itself, or asks WEB_FETCHER_URL
//	web fetcher   only fetches, POST /fetch: run on a network that can only go out
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jurasikasan/units/tools/web/internal/fetch"
	"github.com/jurasikasan/units/tools/web/internal/search"
	"github.com/jurasikasan/units/tools/web/internal/tool"
)

// version is stamped at build time (-ldflags "-X main.version=$VERSION").
var version = "dev"

func main() {
	limits := fetch.Config{
		Timeout:      envDuration("FETCH_TIMEOUT", fetch.Defaults.Timeout),
		MaxBytes:     int64(envNumber("FETCH_MAX_BYTES", int(fetch.Defaults.MaxBytes))),
		MaxChars:     envNumber("FETCH_MAX_CHARS", fetch.Defaults.MaxChars),
		MaxRedirects: envNumber("FETCH_MAX_REDIRECTS", fetch.Defaults.MaxRedirects),
	}
	port := envNumber("PORT", 8080)
	addr := ":" + strconv.Itoa(port)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"ok","version":%q}`, version)
	})

	if len(os.Args) > 1 && os.Args[1] == "fetcher" {
		mux.Handle("POST /fetch", fetch.Handler(fetch.New(limits)))
		log.Printf("web fetcher: listening on %s (POST /fetch, health on GET /health)", addr)
		log.Fatal(http.ListenAndServe(addr, mux))
	}

	var f fetch.Fetcher = fetch.New(limits)
	how := "fetches itself"
	if u := os.Getenv("WEB_FETCHER_URL"); u != "" {
		f = fetch.NewRemote(u, limits.Timeout+10*time.Second)
		how = "fetches through " + u
	}
	var s search.Searcher
	searching := "search not configured (no BRAVE_API_KEY)"
	if key := os.Getenv("BRAVE_API_KEY"); key != "" {
		s = search.NewBrave(key, envDuration("SEARCH_TIMEOUT", 15*time.Second))
		searching = "search by Brave"
	}
	srv := tool.NewMCPServer(s, f, version)
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	log.Printf("web: listening on %s (MCP on /mcp, health on GET /health); %s; %s", addr, searching, how)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envDuration(name string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(envOr(name, def.String()))
	if err != nil || d <= 0 {
		log.Fatalf("web: %s: a positive Go duration, e.g. 20s", name)
	}
	return d
}

func envNumber(name string, def int) int {
	n, err := strconv.Atoi(envOr(name, strconv.Itoa(def)))
	if err != nil || n <= 0 {
		log.Fatalf("web: %s: a positive number", name)
	}
	return n
}
