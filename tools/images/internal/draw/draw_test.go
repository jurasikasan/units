package draw

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// aPNG is a 2x2 red picture.
func aPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for x := 0; x < 2; x++ {
		for y := 0; y < 2; y++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// fakeImages is an OpenAI Images endpoint: it remembers what it was asked
// and answers with pic, or with status and an OpenAI error.
type fakeImages struct {
	pic    []byte
	status int
	asked  map[string]any
	auth   string
	asURL  string // answer with this url instead of b64_json
}

func (f *fakeImages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/pic.png" {
		_, _ = w.Write(f.pic)
		return
	}
	if r.URL.Path != "/v1/images/generations" {
		http.NotFound(w, r)
		return
	}
	f.auth = r.Header.Get("Authorization")
	_ = json.NewDecoder(r.Body).Decode(&f.asked)
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"error":{"message":"the allowance is spent","type":"rate_limit"}}`))
		return
	}
	d := map[string]any{"b64_json": base64.StdEncoding.EncodeToString(f.pic)}
	if f.asURL != "" {
		d = map[string]any{"url": f.asURL}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"created": 0, "data": []any{d}, "size": "2x2"})
}

func serve(t *testing.T, f *fakeImages) (*Images, string) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(Config{URL: srv.URL + "/v1", Key: "k", Model: "images", FastModel: "images-fast", Timeout: 5 * time.Second}), srv.URL
}

func TestDrawAsksTheModelAndReadsThePicture(t *testing.T) {
	pic := aPNG(t)
	f := &fakeImages{pic: pic}
	im, _ := serve(t, f)
	p, err := im.Draw(context.Background(), "a red square", "512x512", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.Data, pic) || p.MIMEType != "image/png" || p.Model != "images" || p.Size != "2x2" {
		t.Fatalf("got %s %s %s, %d bytes", p.MIMEType, p.Model, p.Size, len(p.Data))
	}
	if f.asked["model"] != "images" || f.asked["prompt"] != "a red square" || f.asked["size"] != "512x512" {
		t.Fatalf("asked %v", f.asked)
	}
	if f.auth != "Bearer k" {
		t.Fatalf("auth %q", f.auth)
	}
}

func TestDrawFastAsksTheFastModelAndNoSize(t *testing.T) {
	f := &fakeImages{pic: aPNG(t)}
	im, _ := serve(t, f)
	if _, err := im.Draw(context.Background(), "x", "", true); err != nil {
		t.Fatal(err)
	}
	if f.asked["model"] != "images-fast" {
		t.Fatalf("asked %v", f.asked)
	}
	if _, has := f.asked["size"]; has {
		t.Fatalf("a size was asked: %v", f.asked)
	}
}

func TestDrawReadsAPictureByItsURL(t *testing.T) {
	f := &fakeImages{pic: aPNG(t)}
	im, base := serve(t, f)
	f.asURL = base + "/pic.png"
	p, err := im.Draw(context.Background(), "x", "", false)
	if err != nil || p.MIMEType != "image/png" {
		t.Fatalf("%v %v", p, err)
	}
}

func TestDrawSaysWhyItCouldNot(t *testing.T) {
	im, _ := serve(t, &fakeImages{status: 429})
	_, err := im.Draw(context.Background(), "x", "", false)
	if err == nil || !strings.Contains(err.Error(), "429: the allowance is spent") {
		t.Fatalf("err %v", err)
	}
	if _, err := im.Draw(context.Background(), " ", "", false); err == nil {
		t.Fatal("an empty prompt was drawn")
	}
	im2, _ := serve(t, &fakeImages{pic: []byte("not a picture")})
	if _, err := im2.Draw(context.Background(), "x", "", false); err == nil || !strings.Contains(err.Error(), "not a picture") {
		t.Fatalf("err %v", err)
	}
}

// The tool over MCP, stateless as the mind calls it: the picture as an
// image, a line of words beside it; a refusal as the tool's error.
func TestDrawOverMCP(t *testing.T) {
	pic := aPNG(t)
	f := &fakeImages{pic: pic}
	im, _ := serve(t, f)
	srv := NewMCPServer(im, "test")
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	tool := httptest.NewServer(h)
	t.Cleanup(tool.Close)

	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "t"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: tool.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	var names []string
	for tl, err := range cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tl.Name)
		schema, _ := json.Marshal(tl.InputSchema)
		if !strings.Contains(string(schema), `"required":["prompt"]`) {
			t.Fatalf("schema %s", schema)
		}
	}
	if strings.Join(names, ",") != "draw" {
		t.Fatalf("tools %v", names)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "draw", Arguments: map[string]any{"prompt": "a red square"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) != 2 {
		t.Fatalf("result %+v", res)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "a red square") || !strings.Contains(text.Text, "by images") {
		t.Fatalf("words %+v", res.Content[0])
	}
	img, ok := res.Content[1].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/png" || !bytes.Equal(img.Data, pic) {
		t.Fatalf("picture %+v", res.Content[1])
	}

	f.status = 429
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "draw", Arguments: map[string]any{"prompt": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "the allowance is spent") {
		t.Fatalf("refusal %+v", res)
	}
}
