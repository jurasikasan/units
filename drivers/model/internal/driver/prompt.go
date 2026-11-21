package driver

import (
	"context"
	"fmt"
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
			out = append(out, own(e)...)
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

// Greeting is the context a subject speaks first from, in a thread it
// opened where nothing is said yet: no words of anyone's, only a cue
// naming who is there. Not journaled: what is said is the greeting.
func Greeting(ctx context.Context, subject uuid.UUID, members []uuid.UUID, names Registry) []types.Message {
	var who []string
	for _, m := range members {
		if m != subject {
			who = append(who, names.Name(ctx, m))
		}
	}
	cue := "Nobody has said anything yet in this conversation, which you opened"
	if len(who) > 0 {
		cue += "; " + strings.Join(who, ", ") + " " + map[bool]string{true: "is", false: "are"}[len(who) == 1] + " here"
	}
	cue += ". Speak first: greet them."
	return []types.Message{{ID: uuid.NewString(), Role: types.RoleUser, Content: "(" + cue + ")"}}
}

// own is what the subject said and did before, in the order it did it: its
// words (text blocks, a sound by its transcript) as the assistant's, a tool
// call it made as the assistant's call, and what the tool returned as the
// tool's result in words — a picture or a sound named, never sent back as
// itself. A call with no result in the entry is left out (a model refuses
// a call nobody answered), and so is a result with no call.
func own(e Entry) []types.Message {
	var out []types.Message
	var cur *types.Message
	id := func() string {
		if len(out) == 0 {
			return e.ID.String()
		}
		return fmt.Sprintf("%s-%d", e.ID, len(out))
	}
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	answered, called := map[string]bool{}, map[string]bool{}
	for _, b := range e.Content {
		if b.Type == "tool_result" {
			answered[b.CallID] = true
		}
	}
	for _, b := range e.Content {
		switch b.Type {
		case "text", "audio":
			w := b.Text
			if b.Type == "audio" {
				w = b.Transcript
			}
			if w == "" {
				continue
			}
			if cur != nil && len(cur.ToolCalls) > 0 {
				flush()
			}
			if cur == nil {
				cur = &types.Message{Role: types.RoleAssistant}
			}
			if s, _ := cur.Content.(string); s != "" {
				cur.Content = s + "\n" + w
			} else {
				cur.Content = w
			}
		case "tool_call":
			if !answered[b.ID] {
				continue
			}
			if cur == nil {
				cur = &types.Message{Role: types.RoleAssistant}
			}
			args := string(b.Args)
			if args == "" {
				args = "{}"
			}
			cur.ToolCalls = append(cur.ToolCalls, types.ToolCall{ID: b.ID, Type: types.ToolCallTypeFunction, Function: types.FunctionCall{Name: b.Name, Arguments: args}})
			called[b.ID] = true
		case "tool_result":
			if !called[b.CallID] {
				continue
			}
			if cur != nil {
				cur.ID = id()
			}
			flush()
			out = append(out, types.Message{ID: id(), Role: types.RoleTool, ToolCallID: b.CallID, Content: resultWords(b)})
		}
	}
	if cur != nil {
		cur.ID = id()
	}
	flush()
	return out
}

// resultWords is a tool's result as the model reads it back: its words,
// and its media named.
func resultWords(b Block) string {
	words := []string{}
	if b.Text != "" {
		words = append(words, b.Text)
	}
	for _, m := range b.Media {
		kind := "an image"
		if m.Type == "audio" {
			kind = "a sound"
		}
		words = append(words, fmt.Sprintf("[%s, %s]", kind, m.MediaType))
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
