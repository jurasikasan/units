// Package app wires the module's HTTP routes.
//
// Every route promised by CONTRACT.md is registered here; the generated
// spec in docs/ is generated from the handlers' annotations, and
// routes_documented_test.go keeps the two in step in both directions.
package app

import (
	"net/http"

	"github.com/jurasikasan/units/artifact_store/internal/health"
	"github.com/jurasikasan/units/artifact_store/internal/store"

	"github.com/gorilla/mux"
)

// Route is a registered method+path pair.
type Route struct {
	Path    string
	Methods []string
}

// Router wraps a mux router and records every route registered on it, so
// tests can compare what the code answers with what the spec promises.
type Router struct {
	*mux.Router
	Routes []Route
}

// Route registers path on the underlying mux router and records it.
func (r *Router) Route(path string, methods []string, handler http.HandlerFunc) {
	r.Router.HandleFunc(path, handler).Methods(methods...)
	r.Routes = append(r.Routes, Route{Path: path, Methods: methods})
}

// NewRouter returns the module's router with every registered route.
// svc answers the file routes; it may be nil where only the registered
// routes matter, as in routes_documented_test.go.
func NewRouter(svc store.Service) *Router {
	router := &Router{Router: mux.NewRouter()}

	files := store.NewHandler(svc)

	router.Route("/health", []string{http.MethodGet}, health.NewHandler().Health)
	// Before the space routes: mux takes the first match, and "_" cannot
	// start a space, so no file is shadowed by it.
	router.Route("/v1/_sha256/{hex}", []string{http.MethodGet}, files.GetBySha)
	router.Route("/v1/{space}/{name}", []string{http.MethodPut}, files.Put)
	router.Route("/v1/{space}/{name}", []string{http.MethodGet}, files.Get)
	router.Route("/v1/{space}", []string{http.MethodGet}, files.List)
	router.Route("/v1/{space}/{name}", []string{http.MethodDelete}, files.Delete)

	return router
}
