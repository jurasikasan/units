package stewardship

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/jurasikasan/units/reins/internal/holds"
	httpHelper "github.com/jurasikasan/units/shared/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// PrincipalHeader is who is asking, as the door sets it. reins trusts it and
// is reached only through the door.
const PrincipalHeader = "X-Principal-Id"

// Reins is what the handler needs of the service.
type Reins interface {
	MySubjects(ctx context.Context, principal string) ([]Subject, error)
	Handover(ctx context.Context, caller string, subject uuid.UUID, to string) error
	Subject(ctx context.Context, caller string, subject uuid.UUID) (Subject, error)
	Change(ctx context.Context, caller string, subject uuid.UUID, c Change) (Subject, error)
}

type Handler struct {
	svc Reins
	// brains, when set, adds each subject's current_brain to what is read.
	brains holds.Viewer
}

// NewHandler: brains may be nil, and subjects are read without
// current_brain.
func NewHandler(svc Reins, brains holds.Viewer) *Handler { return &Handler{svc: svc, brains: brains} }

// withBrains adds current_brain to each subject, as the caller sees it from
// its device. A failure to read it leaves it out and is logged: the
// subjects are still the subjects.
func (h *Handler) withBrains(r *http.Request, p string, subs []Subject) {
	if h.brains == nil || len(subs) == 0 {
		return
	}
	ids := make([]uuid.UUID, len(subs))
	for i, s := range subs {
		ids[i] = s.ID
	}
	views, err := h.brains.Views(r.Context(), p, holds.Device(r), ids)
	if err != nil {
		log.Printf("reins: current brains for %q: %v", p, err)
		return
	}
	for i := range subs {
		if b, ok := views[subs[i].ID]; ok {
			subs[i].CurrentBrain = &b
		}
	}
}

func RegisterRoutes(r *mux.Router, h *Handler) {
	r.HandleFunc("/me/subjects", h.MySubjects).Methods(http.MethodGet)
	r.HandleFunc("/subjects/{id}", h.Subject).Methods(http.MethodGet)
	r.HandleFunc("/subjects/{id}", h.Change).Methods(http.MethodPatch)
	r.HandleFunc("/subjects/{id}/handover", h.Handover).Methods(http.MethodPost)
}

func principal(w http.ResponseWriter, r *http.Request) (string, bool) {
	p := r.Header.Get(PrincipalHeader)
	if p == "" {
		http.Error(w, "no "+PrincipalHeader+": reins is reached through the door", http.StatusUnauthorized)
		return "", false
	}
	return p, true
}

// MySubjects godoc
// @Summary      The subjects the caller steers
// @Description  The subjects whose reins the principal in X-Principal-Id holds,
// @Description  read from `subjects` (deleted ones left out), oldest first. The
// @Description  first call for a principal makes what is missing of its two
// @Description  personas: nick `<login>` and
// @Description  `<login>-assistant`, names empty. A nick taken by
// @Description  another subject is never adopted. Each with its
// @Description  current_brain as the caller sees it from X-Device-Id.
// @Tags         reins
// @Produce      json
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Success      200  {array}   stewardship.Subject
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      409  {string}  string "A persona's nick is taken by another subject"
// @Failure      422  {string}  string "The login cannot become a nick"
// @Failure      502  {string}  string "subjects or the database did not answer"
// @Router       /me/subjects [get]
func (h *Handler) MySubjects(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	subs, err := h.svc.MySubjects(r.Context(), p)
	if err != nil {
		var ref *Refusal
		if errors.As(err, &ref) {
			http.Error(w, ref.Reason, ref.Status)
			return
		}
		log.Printf("reins: subjects of %q: %v", p, err)
		http.Error(w, "subjects could not be read", http.StatusBadGateway)
		return
	}
	h.withBrains(r, p, subs)
	httpHelper.RespondWithJSON(w, http.StatusOK, subs)
}

// Handover godoc
// @Summary      Hand the reins of a subject to another principal
// @Description  Only a current steward may. The caller stops steering the
// @Description  subject and `to` starts. Nothing is written to `subjects` or
// @Description  `threads`: who steers a subject is not a world event.
// @Tags         reins
// @Accept       json
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Param        id   path  string true "subject id"
// @Param        body body  stewardship.HandoverRequest true "who takes the reins"
// @Success      204
// @Failure      400  {string}  string "A bad id or body, or `to` empty or the caller"
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      403  {string}  string "The caller does not steer this subject"
// @Failure      500  {string}  string "The database failed"
// @Router       /subjects/{id}/handover [post]
func (h *Handler) Handover(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "invalid subject id", http.StatusBadRequest)
		return
	}
	var req HandoverRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	switch err := h.svc.Handover(r.Context(), p, id, req.To); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrBadHandover):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrNotSteward):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		log.Printf("reins: handover of %s by %q: %v", id, p, err)
		http.Error(w, "handover failed", http.StatusInternalServerError)
	}
}

// steeredFailed answers an error from reading or changing one subject.
func steeredFailed(w http.ResponseWriter, p string, id uuid.UUID, err error) {
	switch {
	case errors.Is(err, ErrNotSteward):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, ErrNoSubject):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrBadChange):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		log.Printf("reins: subject %s for %q: %v", id, p, err)
		http.Error(w, "subjects could not be read or written", http.StatusBadGateway)
	}
}

// Subject godoc
// @Summary      One subject the caller steers
// @Description  As `subjects` returns it, with its current_brain as the
// @Description  caller sees it from X-Device-Id. Only a steward may look.
// @Tags         reins
// @Produce      json
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Param        id   path  string true "subject id"
// @Success      200  {object}  stewardship.Subject
// @Failure      400  {string}  string "A bad id"
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      403  {string}  string "The caller does not steer this subject"
// @Failure      404  {string}  string "The caller steers it, but subjects has it no more"
// @Failure      502  {string}  string "subjects or the database did not answer"
// @Router       /subjects/{id} [get]
func (h *Handler) Subject(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "invalid subject id", http.StatusBadRequest)
		return
	}
	sub, err := h.svc.Subject(r.Context(), p, id)
	if err != nil {
		steeredFailed(w, p, id, err)
		return
	}
	one := []Subject{sub}
	h.withBrains(r, p, one)
	httpHelper.RespondWithJSON(w, http.StatusOK, one[0])
}

// Change godoc
// @Summary      Change the name, bio or preferences of a subject the caller steers
// @Description  A field left out is kept. Preferences are merged key by key
// @Description  into the stored ones: a key given replaces it, a key given as
// @Description  null takes it out, the rest are kept. Written to `subjects`
// @Description  whole (its PUT), the nick and every other field as stored. The
// @Description  nick is not in the body: it never changes, and a body naming
// @Description  it, or any field but these three, is refused.
// @Tags         reins
// @Accept       json
// @Produce      json
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Param        id   path  string true "subject id"
// @Param        body body  stewardship.Change true "what changes"
// @Success      200  {object}  stewardship.Subject
// @Failure      400  {string}  string "A bad id or body, or preferences that are not an object"
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      403  {string}  string "The caller does not steer this subject"
// @Failure      404  {string}  string "The caller steers it, but subjects has it no more"
// @Failure      502  {string}  string "subjects or the database did not answer"
// @Router       /subjects/{id} [patch]
func (h *Handler) Change(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "invalid subject id", http.StatusBadRequest)
		return
	}
	var c Change
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		http.Error(w, "invalid request body: only name, bio and preferences change: "+err.Error(), http.StatusBadRequest)
		return
	}
	sub, err := h.svc.Change(r.Context(), p, id, c)
	if err != nil {
		steeredFailed(w, p, id, err)
		return
	}
	one := []Subject{sub}
	h.withBrains(r, p, one)
	httpHelper.RespondWithJSON(w, http.StatusOK, one[0])
}
