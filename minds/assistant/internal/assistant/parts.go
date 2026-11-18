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
// (a text-only model and a picture): either way the run says so to the
// person before a model is asked (check.go), rather than leaving it out
// unsaid or letting a provider refuse it.
//
// A message of words only goes as a plain string, as before.

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

// userMessage is a person's message as the model is asked it, and the parts
// in it a model must be able to take or that are not carried; false when it
// carries nothing a model is sent and nothing to say about.
func userMessage(m types.Message) (openai.ChatCompletionMessageParamUnion, []media, bool) {
	if s, ok := m.ContentString(); ok {
		return openai.UserMessage(s), nil, s != ""
	}
	in, _ := m.ContentInputContents()
	var parts []openai.ChatCompletionContentPartUnionParam
	var carried []media
	rich := false
	for _, p := range in {
		part, md, ok := contentPart(p)
		if md != nil {
			carried = append(carried, *md)
		}
		if ok {
			parts = append(parts, part)
			rich = rich || part.OfText == nil
		}
	}
	if len(parts) == 0 {
		return openai.ChatCompletionMessageParamUnion{}, carried, len(carried) > 0
	}
	if !rich {
		return openai.UserMessage(text(m)), carried, true
	}
	return openai.UserMessage(parts), carried, true
}

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
