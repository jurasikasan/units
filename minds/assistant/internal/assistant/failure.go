package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

// A model call fails in one of a few ways, and the run ends with RUN_ERROR
// whose code says which, its message in the endpoint's own words. Retrying
// and falling back are the endpoint's (LiteLLM's): what reaches the mind has
// already been tried again there, so the mind passes it on and does not ask
// again.

// Codes of the RUN_ERROR a failed model call ends a run with.
const (
	CodeModelRateLimited = "model_rate_limited" // 429: asked too often, or out of quota
	CodeModelRefused     = "model_refused"      // another 4xx: the endpoint will not take the request as it is
	CodeModelFailed      = "model_failed"       // 5xx, or the answer broke off midway
	CodeModelTimedOut    = "model_timed_out"    // 408 or 504, or silent past the idle limit
	CodeModelUnreachable = "model_unreachable"  // no answer at all: refused connection, no such host
)

// DefaultIdle is how long a model call may go without a byte before the run
// gives up on it, unless SetIdle says otherwise. Longer than LiteLLM's own
// timeouts in the lab: the endpoint should be the one to say so first.
const DefaultIdle = 5 * time.Minute

// errSilent is why a call is cut when the model has said nothing for too long.
var errSilent = errors.New("the model was silent too long")

// failure is the code and the words a failed model call ends a run with.
// call is the call's own context: its cause tells a silent model from any
// other; answered is whether the endpoint had begun to answer (a 200 and
// headers), which tells an answer that broke off from no answer at all.
func failure(call context.Context, err error, answered bool, idle time.Duration) (code, message string) {
	if errors.Is(context.Cause(call), errSilent) {
		return CodeModelTimedOut, fmt.Sprintf("The model said nothing for %s; the call was dropped.", idle)
	}
	var api *openai.Error
	if errors.As(err, &api) {
		words := strings.TrimSpace(api.Message)
		if words == "" {
			words = http.StatusText(api.StatusCode)
		}
		return byStatus(api.StatusCode), fmt.Sprintf("The model's endpoint answered %d: %s", api.StatusCode, words)
	}
	var mid *ssestream.StreamError
	if errors.As(err, &mid) {
		// An error in the stream, after a 200: LiteLLM sends one when the
		// provider fails midway. Its code, when a status, says which kind.
		var e struct {
			Error struct {
				Message string `json:"message"`
				Code    any    `json:"code"` // LiteLLM: the status, as a string
			} `json:"error"`
		}
		words := string(mid.Event.Data)
		code := CodeModelFailed
		if json.Unmarshal(mid.Event.Data, &e) == nil {
			if e.Error.Message != "" {
				words = e.Error.Message
			}
			if status, err := strconv.Atoi(fmt.Sprint(e.Error.Code)); err == nil {
				code = byStatus(status)
			}
		}
		return code, "The model's answer broke off: " + strings.TrimSpace(words)
	}
	if answered {
		return CodeModelFailed, "The model's answer broke off: " + err.Error()
	}
	return CodeModelUnreachable, "The model's endpoint could not be reached: " + err.Error()
}

func byStatus(status int) string {
	switch {
	case status == http.StatusTooManyRequests:
		return CodeModelRateLimited
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return CodeModelTimedOut
	case status >= 400 && status < 500:
		return CodeModelRefused
	default:
		return CodeModelFailed
	}
}
