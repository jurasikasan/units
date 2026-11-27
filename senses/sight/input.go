package sight

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/jurasikasan/units/senses/sight/internal/change"
	"github.com/jurasikasan/units/senses/sight/internal/senml"
)

// MaxPack is the largest pack sight_in takes: a few frames, base64url.
const MaxPack = 16 << 20

// ClockSenML is what placed a single frame in time: its record's SenML time.
const ClockSenML = "senml"

// InputHandler serves sight_in (CONTRACT §4): POST /in/{session_id} with a
// SenML pack. The whole pack is checked before any of it is taken; then it is
// queued and answered 202, and its events follow from the session's worker.
func (s *Service) InputHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Relative SenML times are relative to the pack's arrival (RFC 8428
		// §4.5.3): read the clock once, now, not when the pack is processed.
		arrived := time.Now()
		se := s.session(r.PathValue("session_id"))
		if se == nil {
			answer(w, http.StatusNotFound, "unknown_session: no such session")
			return
		}
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mt != senml.ContentType && mt != "application/json" {
			answer(w, http.StatusUnsupportedMediaType, "bad_input: send "+senml.ContentType)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPack))
		if err != nil {
			answer(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("bad_input: a pack is at most %d bytes", MaxPack))
			return
		}
		items, err := itemsOf(body, arrived)
		if err != nil {
			answer(w, http.StatusBadRequest, "bad_input: "+err.Error())
			return
		}
		switch err := se.take(items); {
		case errors.Is(err, errClosed):
			answer(w, http.StatusGone, "closed: the session is closed")
		case errors.Is(err, errBusy):
			w.Header().Set("Retry-After", "1")
			answer(w, http.StatusServiceUnavailable, "busy: the session is still describing what it saw")
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprintf(w, `{"accepted":%d}`, len(items))
		}
	})
}

// itemsOf reads a pack into what a session does with it. A record is known by
// the last segment of its name: frame (vd, a JPEG or PNG), description or
// read (vs, words).
func itemsOf(body []byte, arrived time.Time) ([]item, error) {
	recs, err := senml.Parse(body, arrived)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("an empty pack")
	}
	items := make([]item, 0, len(recs))
	for i, rec := range recs {
		it := item{kind: rec.Kind(), at: rec.Time}
		switch it.kind {
		case "frame":
			mt := change.MediaType(rec.Data)
			if mt == "" {
				return nil, fmt.Errorf("record %d (%s): vd is not a JPEG or PNG", i, rec.Name)
			}
			img, err := change.Decode(rec.Data)
			if err != nil {
				return nil, fmt.Errorf("record %d (%s): %w", i, rec.Name, err)
			}
			grid, err := change.OfImage(img)
			if err != nil {
				return nil, fmt.Errorf("record %d (%s): %w", i, rec.Name, err)
			}
			it.pic = &picture{img: img, grid: grid, at: rec.Time, clock: ClockSenML}
			if mt == "image/jpeg" {
				it.pic.raw = rec.Data // kept as it came; a PNG is kept as a JPEG
			}
		case "description", "read":
			if !rec.HasString || rec.String == "" {
				return nil, fmt.Errorf("record %d (%s): no words in vs", i, rec.Name)
			}
			it.words = rec.String
		default:
			return nil, fmt.Errorf("record %d: %q is not frame, description or read", i, rec.Name)
		}
		items = append(items, it)
	}
	return items, nil
}

func answer(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
