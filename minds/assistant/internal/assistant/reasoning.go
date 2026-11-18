package assistant

import (
	"encoding/base64"
	"encoding/json"

	"github.com/openai/openai-go/v3/packages/respjson"
)

// What a model thinks comes in two halves. The words it shows go to the
// person as AG-UI reasoning and stay text, like anything else said. What only
// a model can read again (a signature, a provider's state) belongs to the
// model that made it: it rides to the client inside REASONING_ENCRYPTED_VALUE,
// comes back with the history, and is returned only to that same model.
//
// Chat completions carries both through LiteLLM's extensions, not OpenAI's
// schema: reasoning_content (GLM also repeats it as reasoning), thinking_blocks,
// and provider_specific_fields on the message and on each tool call (Gemini's
// thought signatures). openai-go reaches them through a delta's ExtraFields.

// opaque is the part of a turn only its model reads: kept as it came, keyed
// by who made it.
type opaque struct {
	// Model is who answered: LiteLLM's x-litellm-model-name (provider and
	// model, e.g. gemini/gemini-3.1-flash-lite), else the chunks' model.
	Model string `json:"model"`
	// Message is the id of the answer it belongs to (its text message, and
	// the parent of its tool calls): a client may give the reasoning back
	// anywhere in the history, not next to its answer.
	Message string                    `json:"message,omitempty"`
	Blocks  []map[string]any          `json:"thinking_blocks,omitempty"`
	Fields  map[string]any            `json:"provider_specific_fields,omitempty"`
	Calls   map[string]map[string]any `json:"tool_calls,omitempty"` // a call's id → its provider_specific_fields
}

func (o *opaque) empty() bool {
	return o == nil || len(o.Blocks) == 0 && len(o.Fields) == 0 && len(o.Calls) == 0
}

// seal is the opaque part as the client carries it: base64 of its JSON. It
// is not secret, only not the client's to read.
func (o *opaque) seal() string {
	b, _ := json.Marshal(o)
	return base64.StdEncoding.EncodeToString(b)
}

// unseal reads what seal made; anything else is no opaque part.
func unseal(s string) *opaque {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	var o opaque
	if json.Unmarshal(b, &o) != nil || o.Model == "" || o.empty() {
		return nil
	}
	return &o
}

// extra reads an extension a delta carries, by name, as JSON. openai-go
// marks every field outside its schema invalid, so only the raw text counts.
func extra(fields map[string]respjson.Field, name string, into any) bool {
	raw := fields[name].Raw()
	if raw == "" || raw == "null" {
		return false
	}
	return json.Unmarshal([]byte(raw), into) == nil
}

// reasoningText is the reasoning a delta shows: reasoning_content, or
// reasoning when only that came (both carry the same words for GLM).
func reasoningText(fields map[string]respjson.Field) string {
	var s string
	if extra(fields, "reasoning_content", &s) && s != "" {
		return s
	}
	if extra(fields, "reasoning", &s) {
		return s
	}
	return ""
}

// take adds what one delta carries to the opaque part of the turn.
func (o *opaque) take(fields map[string]respjson.Field) {
	var blocks []map[string]any
	if extra(fields, "thinking_blocks", &blocks) {
		for _, b := range blocks {
			o.Blocks = joinBlock(o.Blocks, b)
		}
	}
	var psf map[string]any
	if extra(fields, "provider_specific_fields", &psf) {
		o.Fields = merge(o.Fields, psf)
	}
}

// takeCall adds a tool call's own provider_specific_fields.
func (o *opaque) takeCall(id string, fields map[string]respjson.Field) {
	var psf map[string]any
	if id == "" || !extra(fields, "provider_specific_fields", &psf) || len(psf) == 0 {
		return
	}
	if o.Calls == nil {
		o.Calls = map[string]map[string]any{}
	}
	o.Calls[id] = merge(o.Calls[id], psf)
}

// merge folds a later piece of provider_specific_fields into what came
// before: lists grow (Gemini sends thought_signatures as one), anything else
// is replaced.
func merge(into, piece map[string]any) map[string]any {
	if into == nil {
		into = map[string]any{}
	}
	for k, v := range piece {
		old, had := into[k].([]any)
		add, isList := v.([]any)
		if had && isList {
			into[k] = append(old, add...)
		} else {
			into[k] = v
		}
	}
	return into
}

// joinBlock adds a streamed thinking block: a piece of the same block while
// that block has no signature yet, else a block of its own.
// (Anthropic's shape, through LiteLLM; not seen live here.)
func joinBlock(blocks []map[string]any, b map[string]any) []map[string]any {
	if n := len(blocks); n > 0 {
		last := blocks[n-1]
		if last["type"] == "thinking" && b["type"] == "thinking" && last["signature"] == nil {
			t1, _ := last["thinking"].(string)
			t2, _ := b["thinking"].(string)
			last["thinking"] = t1 + t2
			if sig, ok := b["signature"]; ok {
				last["signature"] = sig
			}
			return blocks
		}
	}
	return append(blocks, b)
}

// callFields is a call's own provider_specific_fields, if any.
func (o *opaque) callFields(id string) map[string]any {
	if o == nil {
		return nil
	}
	return o.Calls[id]
}
