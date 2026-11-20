package driver

import (
	"context"
	"strings"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/google/uuid"
)

// Prompt is the context a subject answers from, built from the thread's
// tail. The first filter is identity's: roles are computed here, threads
// knows none. The subject's own entries are the assistant's; everyone
// else's are the user's, with who said it in the words ("alice: …"), so a
// thread of several is not read as one person. Joins, leaves and renames
// are not said.
//
// It answers false when there is nothing to answer: no entry said by
// another, or the last thing said is the subject's own.
func Prompt(ctx context.Context, subject uuid.UUID, tail []Entry, names Registry) ([]types.Message, bool) {
	var out []types.Message
	for _, e := range tail {
		if e.Kind != "create" {
			continue
		}
		if e.Sender == subject {
			if text := ownWords(e.Content); text != "" {
				out = append(out, types.Message{ID: e.ID.String(), Role: types.RoleAssistant, Content: text})
			}
			continue
		}
		parts := userParts(names.Name(ctx, e.Sender), e.Content)
		if len(parts) == 0 {
			continue
		}
		m := types.Message{ID: e.ID.String(), Role: types.RoleUser}
		if len(parts) == 1 && parts[0].Type == types.InputContentTypeText {
			m.Content = parts[0].Text
		} else {
			m.Content = parts
		}
		out = append(out, m)
	}
	if len(out) == 0 || out[len(out)-1].Role != types.RoleUser {
		return nil, false
	}
	return out, true
}

// ownWords is what the subject said before, as words: its text blocks, a
// sound by its transcript.
func ownWords(blocks []Block) string {
	var words []string
	for _, b := range blocks {
		switch {
		case b.Type == "text" && b.Text != "":
			words = append(words, b.Text)
		case b.Type == "audio" && b.Transcript != "":
			words = append(words, b.Transcript)
		}
	}
	return strings.Join(words, "\n")
}

// userParts is another's entry as AG-UI's input parts, the first text
// naming who said it: a picture as an image, a document (a PDF) as a
// document, each from its data: URL inline or its URL; a sound by its
// transcript. Whether the model takes them is the mind's to say.
func userParts(who string, blocks []Block) []types.InputContent {
	var parts []types.InputContent
	named := false
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text == "" {
				continue
			}
			t := b.Text
			if !named && !strings.HasPrefix(t, "<attachment") {
				t, named = who+": "+t, true
			}
			parts = append(parts, types.InputContent{Type: types.InputContentTypeText, Text: t})
		case "audio":
			if b.Transcript != "" {
				t := b.Transcript
				if !named {
					t, named = who+": "+t, true
				}
				parts = append(parts, types.InputContent{Type: types.InputContentTypeText, Text: t})
			}
		case "image", "file":
			kind := types.InputContentTypeImage
			if b.Type == "file" {
				kind = types.InputContentTypeDocument
			}
			p := types.InputContent{Type: kind, Source: source(b), Filename: b.Name}
			parts = append(parts, p)
		}
	}
	if len(parts) > 0 && !named {
		parts = append([]types.InputContent{{Type: types.InputContentTypeText, Text: who + ":"}}, parts...)
	}
	return parts
}

// source is where a block's bytes are: inline when its url is a base64
// data: URL, else the url itself.
func source(b Block) *types.InputContentSource {
	if rest, ok := strings.CutPrefix(b.URL, "data:"); ok {
		if head, data, ok := strings.Cut(rest, ","); ok && strings.HasSuffix(head, ";base64") {
			mime := strings.TrimSuffix(head, ";base64")
			if mime == "" {
				mime = b.MediaType
			}
			return &types.InputContentSource{Type: types.InputContentSourceTypeData, Value: data, MimeType: mime}
		}
	}
	return &types.InputContentSource{Type: types.InputContentSourceTypeURL, Value: b.URL, MimeType: b.MediaType}
}
