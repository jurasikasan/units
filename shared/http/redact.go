package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

var sensitiveKeys = []string{
	"password",
	"passwd",
	"secret",
	"token",
	"api_key",
	"apikey",
	"authorization",
	"credential",
	"private_key",
}

const redacted = "<redacted>"

func isSensitiveKey(k string) bool {
	lk := strings.ToLower(k)
	for _, s := range sensitiveKeys {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}

func redactBodyForLog(body []byte) string {
	var v any
	if err := json.Unmarshal(body, &v); err == nil {
		redactValue(v)

		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err == nil {
			return buildPrefixPostfixTrimmedStr(bytes.TrimRight(buf.Bytes(), "\n"))
		}
	}

	if isSensitiveKey(string(body)) {
		return fmt.Sprintf("%s (%d bytes, unparsed body with a sensitive field)", redacted, len(body))
	}
	return buildPrefixPostfixTrimmedStr(body)
}

func redactValue(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if isSensitiveKey(k) {
				t[k] = redacted
				continue
			}
			redactValue(val)
		}
	case []any:
		for _, val := range t {
			redactValue(val)
		}
	}
}
