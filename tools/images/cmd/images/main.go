// Command images is a tool agents call to draw: one MCP tool, draw, over
// Streamable HTTP (MCP 2026-07-28, stateless), asking an OpenAI Images
// endpoint for the picture.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jurasikasan/units/tools/images/internal/draw"
)

// version is stamped at build time (-ldflags "-X main.version=$VERSION").
var version = "dev"

func main() {
	url := os.Getenv("IMAGES_URL")
	model := os.Getenv("IMAGES_MODEL")
	if url == "" || model == "" {
		log.Fatal("images: IMAGES_URL and IMAGES_MODEL are required")
	}
	timeout, err := time.ParseDuration(envOr("IMAGES_TIMEOUT", "120s"))
	if err != nil {
		log.Fatalf("images: IMAGES_TIMEOUT: %v", err)
	}
	im := draw.New(draw.Config{
		URL:       url,
		Key:       os.Getenv("IMAGES_API_KEY"),
		Model:     model,
		FastModel: os.Getenv("IMAGES_FAST_MODEL"),
		Timeout:   timeout,
	})
	srv := draw.NewMCPServer(im, version)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"ok","version":%q}`, version)
	})
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))

	port, err := strconv.Atoi(envOr("PORT", "8080"))
	if err != nil {
		log.Fatalf("images: PORT: %v", err)
	}
	addr := ":" + strconv.Itoa(port)
	log.Printf("images: listening on %s (MCP on /mcp, health on GET /health), model %s", addr, model)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("images: %v", err)
	}
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
