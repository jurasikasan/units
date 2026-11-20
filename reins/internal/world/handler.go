package world

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/jurasikasan/units/reins/internal/holds"
	httpHelper "github.com/jurasikasan/units/shared/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// principalHeader is who is asking, as the door sets it.
const principalHeader = "X-Principal-Id"

// MaxSay is the largest entry a principal may append: pictures and files go
// inline as data: URLs until their bytes are kept elsewhere.
const MaxSay = 16 << 20

// Handler serves the world routes. A nil service (no THREADS_URL) answers
// 503 on each; a nil stream (no REDIS_ADDR) on the stream.
type Handler struct {
	svc    *Service
	stream *Stream
}

func NewHandler(svc *Service, stream *Stream) *Handler { return &Handler{svc: svc, stream: stream} }

func RegisterRoutes(r *mux.Router, h *Handler) {
	r.HandleFunc("/me/threads", h.MyThreads).Methods(http.MethodGet)
	r.HandleFunc("/me/threads", h.Open).Methods(http.MethodPost)
	r.HandleFunc("/me/stream", h.Stream).Methods(http.MethodGet)
	r.HandleFunc("/threads/{id}/entries", h.Say).Methods(http.MethodPost)
	r.HandleFunc("/threads/{id}/entries", h.Entries).Methods(http.MethodGet)
}

// OpenRequest opens a thread: `as` opens it and joins, then each of `with`.
type OpenRequest struct {
	As   uuid.UUID   `json:"as"`
	With []uuid.UUID `json:"with,omitempty"`
}

// SayRequest is what an avatar says or does: threads' content blocks.
type SayRequest struct {
	As      uuid.UUID       `json:"as"`
	Content json.RawMessage `json:"content" swaggertype:"array,object"`
}

func (h *Handler) principal(w http.ResponseWriter, r *http.Request) (string, bool) {
	p := r.Header.Get(principalHeader)
	if p == "" {
		http.Error(w, "no "+principalHeader+": reins is reached through the door", http.StatusUnauthorized)
		return "", false
	}
	if h.svc == nil {
		http.Error(w, "reins reaches no threads here (no THREADS_URL)", http.StatusServiceUnavailable)
		return "", false
	}
	return p, true
}

func threadID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "invalid thread id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func failed(w http.ResponseWriter, p string, err error) {
	var ref *Refused
	switch {
	case errors.As(err, &ref):
		http.Error(w, ref.Reason, http.StatusBadRequest)
	case errors.Is(err, ErrBad):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrNotSteward), errors.Is(err, ErrNotMember), errors.Is(err, ErrNotYours):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, ErrNoThread):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, holds.ErrHeld):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		log.Printf("reins: world for %q: %v", p, err)
		http.Error(w, "threads or the database did not answer", http.StatusBadGateway)
	}
}

// MyThreads godoc
// @Summary      The threads the caller's avatars are in
// @Description  Every thread where an avatar the principal steers is a member,
// @Description  as `threads` reads it: its name and members. Newest first.
// @Tags         world
// @Produce      json
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Success      200  {array}   world.Thread
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      502  {string}  string "threads or the database did not answer"
// @Failure      503  {string}  string "No threads here"
// @Router       /me/threads [get]
func (h *Handler) MyThreads(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	ts, err := h.svc.MyThreads(r.Context(), p)
	if err != nil {
		failed(w, p, err)
		return
	}
	httpHelper.RespondWithJSON(w, http.StatusOK, ts)
}

// Open godoc
// @Summary      Open a thread as an avatar
// @Description  A new thread opened by `as`; `as` joins it, then each of `with`
// @Description  (a join entry sent by that avatar). Every one of them is an
// @Description  avatar the caller steers: inviting another's is a later task.
// @Description  Opening is an act of `as`: the caller holds its reins from
// @Description  X-Device-Id, else 409.
// @Tags         world
// @Accept       json
// @Produce      json
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Param        X-Device-Id    header string true "the device the caller holds `as` from"
// @Param        body body  world.OpenRequest true "who opens it, who joins"
// @Success      201  {object}  world.Thread
// @Failure      400  {string}  string "A bad body, or no `as`"
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      403  {string}  string "The caller does not steer one of them"
// @Failure      409  {string}  string "Held by someone else: who"
// @Failure      502  {string}  string "threads or the database did not answer"
// @Failure      503  {string}  string "No threads here"
// @Router       /me/threads [post]
func (h *Handler) Open(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var req OpenRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	t, err := h.svc.Open(WithTraceparent(r.Context(), r.Header.Get("traceparent")), p, holds.Device(r), req.As, req.With)
	if err != nil {
		failed(w, p, err)
		return
	}
	httpHelper.RespondWithJSON(w, http.StatusCreated, t)
}

// Say godoc
// @Summary      Say something in a thread as an avatar
// @Description  A create entry sent by `as`, its content as threads takes it
// @Description  (text, image, file, audio… blocks; threads' CONTRACT.md). `as`
// @Description  must be an avatar the caller steers, whose reins it holds from
// @Description  X-Device-Id (else 409, "held by <holder>"), and a member of the
// @Description  thread. The entry as threads stored it, seq and all.
// @Tags         world
// @Accept       json
// @Produce      json
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Param        X-Device-Id    header string true "the device the caller holds `as` from"
// @Param        id   path  string true "thread id"
// @Param        body body  world.SayRequest true "who says it, what"
// @Success      201  {object}  object "the entry"
// @Failure      400  {string}  string "A bad body, or content threads refuses (its words)"
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      403  {string}  string "The caller does not steer `as`, or `as` is not a member"
// @Failure      404  {string}  string "No such thread"
// @Failure      409  {string}  string "Held by someone else: who"
// @Failure      413  {string}  string "Over 16 MB"
// @Failure      502  {string}  string "threads or the database did not answer"
// @Failure      503  {string}  string "No threads here"
// @Router       /threads/{id}/entries [post]
func (h *Handler) Say(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	id, ok := threadID(w, r)
	if !ok {
		return
	}
	var req SayRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxSay))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "an entry is at most 16 MB", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	e, err := h.svc.Say(WithTraceparent(r.Context(), r.Header.Get("traceparent")), p, holds.Device(r), id, req.As, req.Content)
	if err != nil {
		failed(w, p, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(e)
}

// Entries godoc
// @Summary      Read a thread the caller's avatars are in
// @Description  As threads answers it, oldest first: after={seq}, before={seq}
// @Description  or tail=true, and limit. Every entry, whoever sent it: a watcher
// @Description  sees the channel itself.
// @Tags         world
// @Produce      json
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Param        id     path   string  true  "thread id"
// @Param        after  query  integer false "the entries after this seq"
// @Param        before query  integer false "the limit entries before this seq"
// @Param        tail   query  boolean false "the last limit entries"
// @Param        limit  query  integer false "1..1000, 100 by default"
// @Success      200  {array}   object "entries"
// @Failure      400  {string}  string "A bad id or query"
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      403  {string}  string "None of the caller's avatars is in the thread"
// @Failure      404  {string}  string "No such thread"
// @Failure      502  {string}  string "threads or the database did not answer"
// @Failure      503  {string}  string "No threads here"
// @Router       /threads/{id}/entries [get]
func (h *Handler) Entries(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	id, ok := threadID(w, r)
	if !ok {
		return
	}
	es, err := h.svc.Entries(r.Context(), p, id, r.URL.Query())
	if err != nil {
		failed(w, p, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(es)
}

// Stream godoc
// @Summary      Follow the threads the caller's avatars are in
// @Description  Server-sent events. `ready` first, its id the position the stream
// @Description  starts after. `entry`: one appended entry (threads' shape) of a
// @Description  thread one of the caller's avatars is in, read back from threads
// @Description  on its event; its id is the event's id on threads_events, so a
// @Description  reconnect with Last-Event-ID (or since=) resumes after it.
// @Description  `writing`: a piece of a reply still being written there (never
// @Description  journaled, no id). `reset`: the position is no longer on the
// @Description  stream; reload what is shown.
// @Tags         world
// @Produce      text/event-stream
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Param        Last-Event-ID  header string false "resume after this id"
// @Param        since          query  string false "resume after this id, when the header cannot be set"
// @Success      200  {string}  string "the events"
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      502  {string}  string "The bus did not answer"
// @Failure      503  {string}  string "No threads or no bus here"
// @Router       /me/stream [get]
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	if h.stream == nil {
		http.Error(w, "reins follows no bus here (no REDIS_ADDR)", http.StatusServiceUnavailable)
		return
	}
	h.stream.Serve(w, r, p)
}
