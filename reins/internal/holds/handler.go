package holds

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	httpHelper "github.com/jurasikasan/units/shared/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

const (
	principalHeader = "X-Principal-Id"
	// DeviceHeader names the device a principal acts from: the face makes
	// one per browser and keeps it. ?device= says the same where a header
	// cannot be set (an EventSource).
	DeviceHeader = "X-Device-Id"
)

// Device is the device a request comes from: X-Device-Id, else ?device=.
func Device(r *http.Request) string {
	if d := r.Header.Get(DeviceHeader); d != "" {
		return d
	}
	return r.URL.Query().Get("device")
}

// Handler serves the routes a principal reaches through the door. A nil
// service answers 503 (no database to keep holds in is not a thing reins
// runs without; the nil is for the route walk in tests).
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func RegisterRoutes(r *mux.Router, h *Handler) {
	r.HandleFunc("/subjects/{id}/take", h.Take).Methods(http.MethodPost)
	r.HandleFunc("/subjects/{id}/give-back", h.GiveBack).Methods(http.MethodPost)
	r.HandleFunc("/subjects/{id}/hand", h.Hand).Methods(http.MethodPost)
	r.HandleFunc("/brains", h.Profiles).Methods(http.MethodGet)
	r.HandleFunc("/threads/{id}/stop", h.Stop).Methods(http.MethodPost)
}

// RegisterInternal is what a driver reads, on reins' inner port: never
// forwarded by the door.
func RegisterInternal(r *mux.Router, h *Handler) {
	r.HandleFunc("/holds", h.BrainHolds).Methods(http.MethodGet)
}

func (h *Handler) principal(w http.ResponseWriter, r *http.Request) (string, bool) {
	p := r.Header.Get(principalHeader)
	if p == "" {
		http.Error(w, "no "+principalHeader+": reins is reached through the door", http.StatusUnauthorized)
		return "", false
	}
	if h.svc == nil {
		http.Error(w, "no holds here", http.StatusServiceUnavailable)
		return "", false
	}
	return p, true
}

func pathID(w http.ResponseWriter, r *http.Request, what string) (uuid.UUID, bool) {
	id, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "invalid "+what+" id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func failed(w http.ResponseWriter, p string, err error) {
	switch {
	case errors.Is(err, ErrNoDevice):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrNoProfile):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, ErrNotSteward):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, ErrNoBrains), errors.Is(err, ErrNoBus):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		log.Printf("reins: holds for %q: %v", p, err)
		http.Error(w, "the database or the driver did not answer", http.StatusBadGateway)
	}
}

// Take godoc
// @Summary      Take the reins of a subject, from this device
// @Description  The caller holds the subject from the device it names
// @Description  (X-Device-Id): what it writes as that avatar is its. Any
// @Description  steward may, from whoever holds it now (a brain, another
// @Description  steward, another device); the former holder learns it on its
// @Description  stream. The hold lapses when the device's stream has been gone
// @Description  longer than HOLD_GRACE. The subject's current_brain as the caller sees it.
// @Tags         holds
// @Produce      json
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Param        X-Device-Id    header string true "the device it holds from"
// @Param        id   path  string true "subject id"
// @Success      200  {object}  holds.Brain
// @Failure      400  {string}  string "A bad id, or no device"
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      403  {string}  string "The caller does not steer this subject"
// @Failure      502  {string}  string "The database did not answer"
// @Router       /subjects/{id}/take [post]
func (h *Handler) Take(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "subject")
	if !ok {
		return
	}
	b, err := h.svc.Take(r.Context(), p, Device(r), id)
	if err != nil {
		failed(w, p, err)
		return
	}
	httpHelper.RespondWithJSON(w, http.StatusOK, b)
}

// GiveBack godoc
// @Summary      Give the reins of a subject back, to nobody
// @Description  Nobody holds it: the avatar is offline until a steward takes it
// @Description  or hands it to a brain. Any steward may.
// @Tags         holds
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Param        id   path  string true "subject id"
// @Success      204
// @Failure      400  {string}  string "A bad id"
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      403  {string}  string "The caller does not steer this subject"
// @Failure      502  {string}  string "The database did not answer"
// @Router       /subjects/{id}/give-back [post]
func (h *Handler) GiveBack(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "subject")
	if !ok {
		return
	}
	if err := h.svc.GiveBack(r.Context(), p, id); err != nil {
		failed(w, p, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Hand godoc
// @Summary      Hand the reins of a subject to a brain
// @Description  A brain is a driver's profile (GET /brains): who moves into the
// @Description  avatar and thinks for it. The subject's context stays one: a
// @Description  brain builds it from the journal. Any steward may.
// @Tags         holds
// @Accept       json
// @Produce      json
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Param        id   path  string true "subject id"
// @Param        body body  holds.HandRequest true "the profile"
// @Success      200  {object}  holds.Brain
// @Failure      400  {string}  string "A bad id or body"
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      403  {string}  string "The caller does not steer this subject"
// @Failure      422  {string}  string "No brain by that profile"
// @Failure      502  {string}  string "The database or the driver did not answer"
// @Failure      503  {string}  string "No brains here (no BRAINS_URL)"
// @Router       /subjects/{id}/hand [post]
func (h *Handler) Hand(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "subject")
	if !ok {
		return
	}
	var req HandRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	b, err := h.svc.Hand(r.Context(), p, id, req.Profile)
	if err != nil {
		failed(w, p, err)
		return
	}
	httpHelper.RespondWithJSON(w, http.StatusOK, b)
}

// Profiles godoc
// @Summary      The brains a subject can be handed to
// @Description  The driver's profiles, as it says them: names only.
// @Tags         holds
// @Produce      json
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Success      200  {array}   string
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      502  {string}  string "The driver did not answer"
// @Failure      503  {string}  string "No brains here (no BRAINS_URL)"
// @Router       /brains [get]
func (h *Handler) Profiles(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	ps, err := h.svc.Profiles(r.Context())
	if err != nil {
		failed(w, p, err)
		return
	}
	httpHelper.RespondWithJSON(w, http.StatusOK, ps)
}

// Stop godoc
// @Summary      Stop the reply a subject's brain is writing in a thread
// @Description  An action on the brain: published as reins.reply.stop, and the
// @Description  driver writing as the subject there cancels its run. What it
// @Description  wrote so far is journaled as said when there is any text;
// @Description  nothing when there is none. A steward of the subject may.
// @Tags         holds
// @Accept       json
// @Param        X-Principal-Id header string true "who is asking, as the door sets it"
// @Param        id   path  string true "thread id"
// @Param        body body  holds.StopRequest true "whose reply"
// @Success      202
// @Failure      400  {string}  string "A bad id or body"
// @Failure      401  {string}  string "No X-Principal-Id"
// @Failure      403  {string}  string "The caller does not steer the subject"
// @Failure      502  {string}  string "The database or the bus did not answer"
// @Failure      503  {string}  string "No bus here (no REDIS_ADDR)"
// @Router       /threads/{id}/stop [post]
func (h *Handler) Stop(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	thread, ok := pathID(w, r, "thread")
	if !ok {
		return
	}
	var req StopRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Subject == uuid.Nil {
		http.Error(w, `invalid request body: "subject" names whose reply is stopped`, http.StatusBadRequest)
		return
	}
	if err := h.svc.Stop(r.Context(), p, thread, req.Subject); err != nil {
		failed(w, p, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// BrainHolds is every hold of a brain, on the inner port: a driver reads
// it at boot and on each reins.hold.changed, and keeps those of its own
// profiles. No principal: the door never forwards here.
func (h *Handler) BrainHolds(w http.ResponseWriter, r *http.Request) {
	hs, err := h.svc.Brains(r.Context())
	if err != nil {
		log.Printf("reins: brains' holds: %v", err)
		http.Error(w, "the database did not answer", http.StatusBadGateway)
		return
	}
	type out struct {
		Hold
		Profile string `json:"profile"`
	}
	list := make([]out, 0, len(hs))
	for _, x := range hs {
		p, _ := x.Profile()
		list = append(list, out{x, p})
	}
	httpHelper.RespondWithJSON(w, http.StatusOK, list)
}

// Viewer is what others need to show current_brain.
type Viewer interface {
	Views(ctx context.Context, principal, device string, subjects []uuid.UUID) (map[uuid.UUID]Brain, error)
}
