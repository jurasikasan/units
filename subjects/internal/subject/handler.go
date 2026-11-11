package subject

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	httpHelper "github.com/jurasikasan/units/shared/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// SubjectHandler handles HTTP requests for subjects.
type SubjectHandler struct {
	service SubjectService
}

// NewSubjectHandler creates a new SubjectHandler.
func NewSubjectHandler(service SubjectService) *SubjectHandler {
	return &SubjectHandler{service: service}
}

// RegisterRoutes mounts the subject routes.
func RegisterRoutes(r *mux.Router, h *SubjectHandler) {
	r.HandleFunc("/subjects", h.CreateSubject).Methods(http.MethodPost)
	r.HandleFunc("/subjects", h.GetAllSubjects).Methods(http.MethodGet)
	r.HandleFunc("/subjects/{id}", h.GetSubjectByID).Methods(http.MethodGet)
	r.HandleFunc("/subjects/{id}", h.UpdateSubject).Methods(http.MethodPut)
	r.HandleFunc("/subjects/{id}", h.DeleteSubject).Methods(http.MethodDelete)
}

// CreateSubject godoc
// @Summary      Create a new subject
// @Description  Creates a new subject with the provided details
// @Tags         subjects
// @Accept       json
// @Produce      json
// @Param        subject body subject.Subject true "Subject details"
// @Success      201  {object}  subject.Subject
// @Failure      400  {string}  string "Invalid request body, an empty subject_type, preferences that are not an object, or the nick breaks its rules"
// @Failure      409  {string}  string "The nick is taken"
// @Failure      500  {string}  string "Failed to create subject"
// @Router       /subjects [post]
func (h *SubjectHandler) CreateSubject(w http.ResponseWriter, r *http.Request) {
	var subject Subject
	if err := json.NewDecoder(r.Body).Decode(&subject); err != nil {
		http.Error(w, "Invalid request body "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.service.CreateSubject(r.Context(), &subject); err != nil {
		switch {
		case errors.Is(err, ErrInvalidNick):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, ErrEmptyType), errors.Is(err, ErrPreferencesNotObject):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, ErrNickTaken):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			http.Error(w, "Failed to create subject", http.StatusInternalServerError)
		}
		return
	}

	httpHelper.RespondWithJSON(w, http.StatusCreated, subject)
}

// GetAllSubjects godoc
// @Summary      Get all subjects
// @Description  Retrieves a list of all subjects
// @Tags         subjects
// @Accept       json
// @Produce      json
// @Success      200  {array}  subject.Subject
// @Failure      500  {string}  string "Failed to get subjects"
// @Router       /subjects [get]
func (h *SubjectHandler) GetAllSubjects(w http.ResponseWriter, r *http.Request) {
	subjects, err := h.service.GetAllSubjects(r.Context())
	if err != nil {
		http.Error(w, "Failed to get subjects", http.StatusInternalServerError)
		return
	}

	httpHelper.RespondWithJSON(w, http.StatusOK, subjects)
}

// GetSubjectByID godoc
// @Summary      Get a single subject by its ID
// @Description  Retrieves a subject based on the ID provided in the path.
// @Tags         subjects
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Subject ID"
// @Success      200  {object}  subject.Subject
// @Failure      400  {string}  string "Invalid subject ID format or Subject ID is required"
// @Failure      404  {string}  string "Subject not found"
// @Failure      500  {string}  string "Failed to get subject"
// @Router       /subjects/{id} [get]
func (h *SubjectHandler) GetSubjectByID(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, exists := vars["id"]
	if !exists || id == "" {
		http.Error(w, "Subject ID is required", http.StatusBadRequest)
		return
	}

	if _, err := uuid.Parse(id); err != nil {
		http.Error(w, "Invalid subject ID format", http.StatusBadRequest)
		return
	}

	subject, err := h.service.GetSubjectByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "Subject not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to get subject", http.StatusInternalServerError)
		return
	}

	if subject == nil {
		http.Error(w, "Subject not found", http.StatusNotFound)
		return
	}

	httpHelper.RespondWithJSON(w, http.StatusOK, subject)
}

// UpdateSubject godoc
// @Summary      Update a subject
// @Description  Updates an existing subject. The body replaces the subject:
// fields left out are written empty, so a preferences object left out is cleared.
// @Tags         subjects
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Subject ID"
// @Param        subject body subject.Subject true "Subject details"
// @Success      200  {object}  subject.Subject
// @Failure      400  {string}  string "Invalid subject ID format, Invalid request body, an empty subject_type, preferences that are not an object, or an attempt to change the nick"
// @Failure      404  {string}  string "Subject not found"
// @Failure      500  {string}  string "Failed to update subject"
// @Router       /subjects/{id} [put]
func (h *SubjectHandler) UpdateSubject(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, exists := vars["id"]
	if !exists || id == "" {
		http.Error(w, "Subject ID is required", http.StatusBadRequest)
		return
	}

	var subject Subject
	if err := json.NewDecoder(r.Body).Decode(&subject); err != nil {
		http.Error(w, "Invalid request body "+err.Error(), http.StatusBadRequest)
		return
	}

	// Set the ID from the URL path
	subjectID, err := uuid.Parse(id)
	if err != nil {
		http.Error(w, "Invalid subject ID format", http.StatusBadRequest)
		return
	}
	subject.ID = subjectID

	if err := h.service.UpdateSubject(r.Context(), &subject); err != nil {
		switch {
		case errors.Is(err, ErrNickImmutable):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, ErrEmptyType), errors.Is(err, ErrPreferencesNotObject):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, pgx.ErrNoRows):
			http.Error(w, "Subject not found", http.StatusNotFound)
		default:
			http.Error(w, "Failed to update subject", http.StatusInternalServerError)
		}
		return
	}

	httpHelper.RespondWithJSON(w, http.StatusOK, subject)
}

// DeleteSubject godoc
// @Summary      Delete a subject
// @Description  Deletes a subject by its ID
// @Tags         subjects
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Subject ID"
// @Success      200  {object}  object "Status of deletion"
// @Failure      400  {string}  string "Invalid subject ID format or Subject ID is required"
// @Failure      404  {string}  string "Subject not found"
// @Failure      500  {string}  string "Failed to delete subject"
// @Router       /subjects/{id} [delete]
func (h *SubjectHandler) DeleteSubject(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, exists := vars["id"]
	if !exists || id == "" {
		http.Error(w, "Subject ID is required", http.StatusBadRequest)
		return
	}

	if _, err := uuid.Parse(id); err != nil {
		http.Error(w, "Invalid subject ID format", http.StatusBadRequest)
		return
	}

	if err := h.service.DeleteSubject(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "Subject not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to delete subject", http.StatusInternalServerError)
		return
	}

	httpHelper.RespondWithJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
