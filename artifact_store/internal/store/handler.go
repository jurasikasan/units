package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	httpHelper "github.com/jurasikasan/units/shared/http"

	"github.com/gorilla/mux"
)

// Handler answers the file routes of CONTRACT.md section 3.
type Handler struct {
	service Service
}

// NewHandler builds the handler on the given service.
func NewHandler(service Service) *Handler {
	return &Handler{service: service}
}

// statusFor is the one place where a store error becomes an HTTP status:
// 400 a broken alphabet, 404 no such file, 413 body over the limit,
// 503 MinIO or Postgres unavailable (CONTRACT.md section 6).
func statusFor(err error) (code int, message string) {
	switch {
	case errors.Is(err, ErrBadIdent), errors.Is(err, ErrBadBody):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, ErrTooLarge):
		return http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, ErrUnavailable):
		return http.StatusServiceUnavailable, err.Error()
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// writeError writes a failed answer: the status for err and nothing else.
func writeError(w http.ResponseWriter, err error) {
	code, message := statusFor(err)
	http.Error(w, message, code)
}

// Put handles PUT /v1/{space}/{name}: it stores the body as the file,
// keeps Content-Type, and answers with the file's metadata. A second PUT
// under the same name replaces the file.
//
// @Summary      Store a file
// @Description  Stores the request body as the file under name in space, keeping Content-Type; created_by is the X-Principal-Id header as received or null. A second PUT under the same name replaces the file; a broken upload leaves the previous file as it was.
// @Tags         store
// @Produce      json
// @Param        space         path   string true  "space"
// @Param        name          path   string true  "name"
// @Param        X-Principal-Id header string false "who wrote the file"
// @Success      200  {object}  File
// @Failure      400  {string}  string "space or name breaks its alphabet"
// @Failure      413  {string}  string "body over the size limit"
// @Failure      503  {string}  string "MinIO or Postgres unavailable"
// @Router       /v1/{space}/{name} [put]
func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	file, err := h.service.Put(r.Context(),
		vars["space"], vars["name"],
		r.Header.Get("Content-Type"), r.Header.Get("X-Principal-Id"),
		r.Body)
	if err != nil {
		writeError(w, err)
		return
	}

	httpHelper.RespondWithJSON(w, http.StatusOK, file)
}

// List handles GET /v1/{space}?prefix=<p>: the space's files whose name
// starts with p (all of them without p), as their metadata, ordered by
// name; an empty space is [].
//
// @Summary      List a space's files
// @Description  The space's files whose name starts with the prefix parameter (all of them without it), as their metadata, ordered by name. An empty or unknown space is an empty list.
// @Tags         store
// @Produce      json
// @Param        space   path   string true  "space"
// @Param        prefix  query  string false "list only names that start with it"
// @Success      200  {array}   File
// @Failure      400  {string}  string "space breaks its alphabet"
// @Failure      503  {string}  string "MinIO or Postgres unavailable"
// @Router       /v1/{space} [get]
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	space := mux.Vars(r)["space"]

	files, err := h.service.List(r.Context(), space, r.URL.Query().Get("prefix"))
	if err != nil {
		writeError(w, err)
		return
	}

	httpHelper.RespondWithJSON(w, http.StatusOK, files)
}

// Get handles GET /v1/{space}/{name}: the bytes with Content-Type,
// Content-Length and ETag "<sha256>".
//
// @Summary      Read a file back
// @Description  The file's bytes, with Content-Type, Content-Length and ETag "<sha256>" of the bytes.
// @Tags         store
// @Produce      application/octet-stream
// @Param        space  path string true "space"
// @Param        name   path string true "name"
// @Success      200  {file}   file
// @Failure      400  {string} string "space or name breaks its alphabet"
// @Failure      404  {string} string "no such file"
// @Failure      503  {string} string "MinIO or Postgres unavailable"
// @Router       /v1/{space}/{name} [get]
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	data, contentType, err := h.service.Get(r.Context(), vars["space"], vars["name"])
	if err != nil {
		writeError(w, err)
		return
	}

	sum := sha256.Sum256(data)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("ETag", "\""+hex.EncodeToString(sum[:])+"\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// Delete handles DELETE /v1/{space}/{name}: it removes the file and
// answers 204; a file that is not there is 404. Afterwards a GET of the
// name is 404 and the listing no longer carries it.
//
// @Summary      Remove a file
// @Description  Removes the file under name in space. 204 when the file was there, 404 when it was not; afterwards the name no longer answers and is out of the listing.
// @Tags         store
// @Param        space  path string true "space"
// @Param        name   path string true "name"
// @Success      204
// @Failure      400  {string} string "space or name breaks its alphabet"
// @Failure      404  {string} string "no such file"
// @Failure      503  {string} string "MinIO or Postgres unavailable"
// @Router       /v1/{space}/{name} [delete]
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	if err := h.service.Delete(r.Context(), vars["space"], vars["name"]); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
