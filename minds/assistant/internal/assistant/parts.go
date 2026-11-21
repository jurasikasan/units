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
// takes text, image_url, file and input_audio parts; LiteLLM carries them on
// to whatever model answers. Here each part becomes the chat completions part
// it is:
//
//   - an image: image_url, its data as a data URL, or its URL as it came;
//   - a text document (text/*, JSON): a text part naming the file, as
//     assistant-ui's own text attachments are, so any model reads it;
//   - any other document (a PDF): a file part, its data as a data URL;
//   - audio in WAV or MP3: input_audio, its data as it came.
//
// What no chat completions part holds (video, a document or a sound by URL,
// audio in another format) is not carried, and a model may not take what is
// (a text-only model and a picture): either way the part goes as a
// placeholder in words where it stood, so the model knows something is there
// and the run goes on, rather than leaving it out unsaid, letting a provider
// refuse it, or failing the run (check.go).
//
// A message of words only goes as a plain string, as before.

// piece is one part of a person's message as it may go to a model.
type piece struct {
	part  openai.ChatCompletionContentPartUnionParam // the part, when carried
	media *media                                     // nil for words
}

// media is a part a model must be able to take, or one nothing carries.
type media struct {
	kind kind   // what a model has to take; "" when it is not carried
	name string // the file's name, or its media type, for the person
	why  string // when not carried: why
}

// kind is what a model has to take for a part to reach it.
type kind string

const (
	kindImage kind = "image"
	kindPDF   kind = "pdf"
	kindAudio kind = "audio"
)

// userPieces is a person's message as its parts, in the order they came;
// false when it carries nothing.
func userPieces(m types.Message) ([]piece, bool) {
	if s, ok := m.ContentString(); ok {
		return []piece{{part: openai.TextContentPart(s)}}, s != ""
	}
	in, _ := m.ContentInputContents()
	var out []piece
	for _, p := range in {
		part, md, ok := contentPart(p)
		if ok || md != nil {
			out = append(out, piece{part: part, media: md})
		}
	}
	return out, len(out) > 0
}

// userContent is a person's message as the model is asked it: each part as
// it is, but a part nothing carries, or of a kind in refused, as its
// placeholder. A message whose parts are all words goes as a plain string.
func userContent(pieces []piece, refused map[kind]bool) openai.ChatCompletionMessageParamUnion {
	parts := make([]openai.ChatCompletionContentPartUnionParam, len(pieces))
	var words []string
	rich := false
	for i, p := range pieces {
		parts[i] = p.part
		if m := p.media; m != nil && (m.kind == "" || refused[m.kind]) {
			parts[i] = openai.TextContentPart(m.placeholder())
		}
		if t := parts[i].OfText; t != nil {
			words = append(words, t.Text)
		} else {
			rich = true
		}
	}
	if !rich {
		return openai.UserMessage(strings.Join(words, "\n"))
	}
	return openai.UserMessage(parts)
}

// placeholder is the words a part goes as when it cannot go itself.
func (m media) placeholder() string {
	if m.kind == "" {
		return "[an attachment is here that could not be sent to you: " + m.name + "]"
	}
	return "[" + named[m.kind] + " is attached here, not shown to you: " + m.name + "]"
}

var named = map[kind]string{kindImage: "an image", kindPDF: "a PDF", kindAudio: "a sound"}

// contentPart is one AG-UI part as a chat completions part, false for what
// is not carried; and, for a part not every model takes or none is given,
// what it is.
func contentPart(p types.InputContent) (openai.ChatCompletionContentPartUnionParam, *media, bool) {
	none := openai.ChatCompletionContentPartUnionParam{}
	if p.Type == types.InputContentTypeText {
		return openai.TextContentPart(p.Text), nil, p.Text != ""
	}
	mime, data, url := source(p)
	if data == "" && url == "" {
		return none, nil, false
	}
	name := filename(p)
	shown := name
	if shown == "" {
		shown = mime
	}
	notCarried := func(why string) (openai.ChatCompletionContentPartUnionParam, *media, bool) {
		return none, &media{name: shown, why: why}, false
	}
	switch {
	case p.Type == types.InputContentTypeImage || p.Type == types.InputContentTypeBinary && strings.HasPrefix(mime, "image/"):
		if url == "" {
			url = dataURL(mime, data)
		}
		return openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: url}), &media{kind: kindImage, name: shown}, true
	case p.Type == types.InputContentTypeVideo, strings.HasPrefix(mime, "video/"):
		return notCarried("a video has no place in a chat completions message")
	case p.Type == types.InputContentTypeAudio, strings.HasPrefix(mime, "audio/"):
		format := audioFormat(mime)
		switch {
		case data == "":
			return notCarried("a sound goes to a model only as its data, not by a link")
		case format == "":
			return notCarried("a model takes a sound as WAV or MP3, not " + mime)
		}
		return openai.InputAudioContentPart(openai.ChatCompletionContentPartInputAudioInputAudioParam{Data: data, Format: format}), &media{kind: kindAudio, name: shown}, true
	case p.Type != types.InputContentTypeDocument && p.Type != types.InputContentTypeBinary:
		return notCarried("a part of type " + string(p.Type) + " is not known here")
	case data == "":
		return notCarried("a document goes to a model only as its data, not by a link")
	case isText(mime):
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return notCarried("its data is not base64")
		}
		return openai.TextContentPart(fmt.Sprintf("<attachment name=\"%s\">\n%s\n</attachment>", html.EscapeString(name), b)), nil, true
	}
	f := openai.ChatCompletionContentPartFileFileParam{FileData: openai.String(dataURL(mime, data))}
	if name != "" {
		f.Filename = openai.String(name)
	}
	return openai.FileContentPart(f), &media{kind: kindPDF, name: shown}, true
}

// audioFormat is chat completions' name for a sound's format, "" for one it
// does not take.
func audioFormat(mime string) string {
	mime, _, _ = strings.Cut(mime, ";")
	switch mime {
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	}
	return ""
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
