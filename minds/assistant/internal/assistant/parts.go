package assistant

import (
	"encoding/base64"
	"fmt"
	"html"
	"strings"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/openai/openai-go/v3"
)

// What a person sends may be more than words: AG-UI's InputContent parts
// (image, document, audio, video, and the older binary). Chat completions
// takes text, image_url and file parts; LiteLLM carries them on to whatever
// model answers. Here each part becomes the chat completions part it is:
//
//   - an image: image_url, its data as a data URL, or its URL as it came;
//   - a text document (text/*, JSON): a text part naming the file, as
//     assistant-ui's own text attachments are, so any model reads it;
//   - any other document (a PDF): a file part, its data as a data URL;
//   - audio and video: passed over for now (what a model cannot take is
//     UNIT-33's).
//
// A message of words only goes as a plain string, as before.

// userMessage is a person's message as the model is asked it; false when it
// carries nothing a model is sent.
func userMessage(m types.Message) (openai.ChatCompletionMessageParamUnion, bool) {
	if s, ok := m.ContentString(); ok {
		return openai.UserMessage(s), s != ""
	}
	in, _ := m.ContentInputContents()
	var parts []openai.ChatCompletionContentPartUnionParam
	media := false
	for _, p := range in {
		if part, ok := contentPart(p); ok {
			parts = append(parts, part)
			media = media || part.OfText == nil
		}
	}
	if len(parts) == 0 {
		return openai.ChatCompletionMessageParamUnion{}, false
	}
	if !media {
		return openai.UserMessage(text(m)), true
	}
	return openai.UserMessage(parts), true
}

// contentPart is one AG-UI part as a chat completions part; false for what
// is not carried.
func contentPart(p types.InputContent) (openai.ChatCompletionContentPartUnionParam, bool) {
	if p.Type == types.InputContentTypeText {
		return openai.TextContentPart(p.Text), p.Text != ""
	}
	mime, data, url := source(p)
	if data == "" && url == "" {
		return openai.ChatCompletionContentPartUnionParam{}, false
	}
	name := filename(p)
	switch {
	case p.Type == types.InputContentTypeImage || p.Type == types.InputContentTypeBinary && strings.HasPrefix(mime, "image/"):
		if url == "" {
			url = dataURL(mime, data)
		}
		return openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: url}), true
	case p.Type != types.InputContentTypeDocument && p.Type != types.InputContentTypeBinary,
		strings.HasPrefix(mime, "audio/"), strings.HasPrefix(mime, "video/"):
		return openai.ChatCompletionContentPartUnionParam{}, false // audio, video
	case data == "":
		return openai.ChatCompletionContentPartUnionParam{}, false // a document by URL: nothing fetches it yet
	case isText(mime):
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return openai.ChatCompletionContentPartUnionParam{}, false
		}
		return openai.TextContentPart(fmt.Sprintf("<attachment name=\"%s\">\n%s\n</attachment>", html.EscapeString(name), b)), true
	}
	f := openai.ChatCompletionContentPartFileFileParam{FileData: openai.String(dataURL(mime, data))}
	if name != "" {
		f.Filename = openai.String(name)
	}
	return openai.FileContentPart(f), true
}

// source is a part's media type and either its base64 data or its URL,
// from the typed source or the older binary fields.
func source(p types.InputContent) (mime, data, url string) {
	mime = p.MimeType
	if s := p.Source; s != nil {
		if s.MimeType != "" {
			mime = s.MimeType
		}
		switch s.Type {
		case types.InputContentSourceTypeData:
			data = s.Value
		case types.InputContentSourceTypeURL:
			url = s.Value
		}
	} else {
		data, url = p.Data, p.URL
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	return mime, data, url
}

// filename is the part's file name: its own field, else the metadata the
// assistant-ui adapter puts it in.
func filename(p types.InputContent) string {
	if p.Filename != "" {
		return p.Filename
	}
	if md, ok := p.Metadata.(map[string]any); ok {
		if s, ok := md["filename"].(string); ok {
			return s
		}
	}
	return ""
}

func dataURL(mime, data string) string { return "data:" + mime + ";base64," + data }

func isText(mime string) bool {
	mime, _, _ = strings.Cut(mime, ";")
	return strings.HasPrefix(mime, "text/") || mime == "application/json"
}
