package thread

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	httpHelper "github.com/jurasikasan/units/shared/http"
)

// Handler serves threads over HTTP.
type Handler struct {
	service Service
}

// NewHandler wraps a service.
func NewHandler(service Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes mounts the thread routes.
func RegisterRoutes(r *mux.Router, h *Handler) {
	r.HandleFunc("/threads", h.Open).Methods(http.MethodPost)
	r.HandleFunc("/threads", h.List).Methods(http.MethodGet)
	r.HandleFunc("/threads/{id}", h.Get).Methods(http.MethodGet)
	r.HandleFunc("/threads/{id}", h.Delete).Methods(http.MethodDelete)
	r.HandleFunc("/threads/{id}/entries", h.Append).Methods(http.MethodPost)
	r.HandleFunc("/threads/{id}/entries", h.Entries).Methods(http.MethodGet)
}

// decode reads a JSON body, refusing fields the type does not have: a
// misspelt field would otherwise be dropped and the entry stored without it.
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "thread id is not a uuid", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrBadPage):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// Open godoc
// @Summary      Open a thread
// @Description  Mints a thread, optionally under a parent and with the subject that opened it.
// @Tags         threads
// @Accept       json
// @Produce      json
// @Param        thread body thread.NewThread true "parent and opened_by, both optional"
// @Success      201  {object}  thread.Thread
// @Failure      400  {string}  string "Invalid request body"
// @Failure      404  {string}  string "No thread with the given parent id"
// @Router       /threads [post]
func (h *Handler) Open(w http.ResponseWriter, r *http.Request) {
	var n NewThread
	if err := decode(r, &n); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	t, err := h.service.Open(r.Context(), n)
	if err != nil {
		fail(w, err)
		return
	}
	httpHelper.RespondWithJSON(w, http.StatusCreated, t)
}

// List godoc
// @Summary      List threads
// @Description  Newest first, each with its name. member: threads where the subject's last join/leave
// @Description  is a join. parent: a thread's children. Both narrow together; neither lists every thread.
// @Tags         threads
// @Produce      json
// @Param        member query string false "subject id"
// @Param        parent query string false "thread id"
// @Success      200  {array}   thread.Thread
// @Failure      400  {string}  string "member or parent is not a uuid"
// @Router       /threads [get]
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	var f Filter
	for name, dst := range map[string]**uuid.UUID{"member": &f.Member, "parent": &f.Parent} {
		v := r.URL.Query().Get(name)
		if v == "" {
			continue
		}
		id, err := uuid.Parse(v)
		if err != nil {
			http.Error(w, name+" is not a uuid", http.StatusBadRequest)
			return
		}
		*dst = &id
	}
	ts, err := h.service.List(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	httpHelper.RespondWithJSON(w, http.StatusOK, ts)
}

// Get godoc
// @Summary      Read a thread
// @Description  The thread with its name (from its last update entry) and members (subjects whose last
// @Description  join/leave is a join, in the order they joined).
// @Tags         threads
// @Produce      json
// @Param        id   path      string  true  "thread id"
// @Success      200  {object}  thread.Thread
// @Failure      400  {string}  string "id is not a uuid"
// @Failure      404  {string}  string "No such thread"
// @Router       /threads/{id} [get]
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, err := h.service.Get(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	httpHelper.RespondWithJSON(w, http.StatusOK, t)
}

// Delete godoc
// @Summary      Delete a thread
// @Description  The thread, its entries and its children's, for good.
// @Tags         threads
// @Param        id   path      string  true  "thread id"
// @Success      204
// @Failure      400  {string}  string "id is not a uuid"
// @Failure      404  {string}  string "No such thread"
// @Router       /threads/{id} [delete]
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.service.Delete(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Append godoc
// @Summary      Append an entry
// @Description  Stores the entry at the end of the thread; seq, id and at are filled in.
// @Description  create carries at least one block; join and leave nothing; update the new name.
// @Tags         entries
// @Accept       json
// @Produce      json
// @Param        id     path  string          true  "thread id"
// @Param        entry  body  thread.NewEntry true  "sender, kind, content, name"
// @Success      201  {object}  thread.Entry
// @Failure      400  {string}  string "Unknown kind or block type, or content that does not fit the kind"
// @Failure      404  {string}  string "No such thread"
// @Router       /threads/{id}/entries [post]
func (h *Handler) Append(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var n NewEntry
	if err := decode(r, &n); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	e, err := h.service.Append(r.Context(), id, n)
	if err != nil {
		fail(w, err)
		return
	}
	httpHelper.RespondWithJSON(w, http.StatusCreated, e)
}

// Entries godoc
// @Summary      Read entries
// @Description  Oldest first, always. after: the entries after that seq (0 by default). before: the
// @Description  limit entries before that seq. tail: the last limit entries. At most one of the three.
// @Tags         entries
// @Produce      json
// @Param        id      path   string   true   "thread id"
// @Param        after   query  integer  false  "seq"
// @Param        before  query  integer  false  "seq"
// @Param        tail    query  boolean  false  "the end of the thread"
// @Param        limit   query  integer  false  "1..1000, 100 by default"
// @Success      200  {array}   thread.Entry
// @Failure      400  {string}  string "More than one of after, before, tail, or a number out of range"
// @Failure      404  {string}  string "No such thread"
// @Router       /threads/{id}/entries [get]
func (h *Handler) Entries(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var q PageQuery
	vals := r.URL.Query()
	for name, dst := range map[string]**int64{"after": &q.After, "before": &q.Before, "limit": &q.Limit} {
		v := vals.Get(name)
		if v == "" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, name+" is not a number", http.StatusBadRequest)
			return
		}
		*dst = &n
	}
	if v := vals.Get("tail"); v != "" {
		tail, err := strconv.ParseBool(v)
		if err != nil {
			http.Error(w, "tail is true or false", http.StatusBadRequest)
			return
		}
		q.Tail = tail
	}
	es, err := h.service.Entries(r.Context(), id, q)
	if err != nil {
		fail(w, err)
		return
	}
	httpHelper.RespondWithJSON(w, http.StatusOK, es)
}
