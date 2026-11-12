package main

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/jurasikasan/units/threads/internal/thread"

	"github.com/gorilla/mux"
)

// TestEveryRouteIsDocumented keeps docs/swagger.json honest about what the
// service actually serves.
//
// Why this exists: the swagger spec is generated from `// @Router` annotations,
// and swag has no idea which routes are registered — a handler with no
// annotation is simply absent from the output, and `swag init` still exits 0.
// accounting_api shipped exactly that once: a spec covering 12 of 17 routes
// with a green gate, and nothing anywhere went red.
//
// So the check is a set comparison in both directions, against the router the
// server actually serves — newRouter, the same call main() makes — rather than
// against a second description of it. A route added without an annotation
// fails here, and an annotation left behind after its route is deleted fails
// too — the second is the one that turns a spec into a lie for an integrator
// building against it.
//
// If this fails, the fix is an annotation plus `bash doc.sh`, not an edit here.
func TestEveryRouteIsDocumented(t *testing.T) {
	registered := routesFromRouter(t)
	documented := routesFromSpec(t)

	if missing := difference(registered, documented); len(missing) > 0 {
		t.Errorf("registered in newRouter but missing from docs/swagger.json (add @Router, then run doc.sh):\n  %v", missing)
	}
	if extra := difference(documented, registered); len(extra) > 0 {
		t.Errorf("documented in docs/swagger.json but not registered in newRouter (stale annotation):\n  %v", extra)
	}
}

// Walking the router rather than reading main.go is what routes_test.go already
// does for the ordering bug, for the same reason: what a request resolves to is
// the contract, and the lines that registered it are not.
func routesFromRouter(t *testing.T) map[string]bool {
	t.Helper()
	r := newRouter(thread.NewHandler(nil), nil)
	out := map[string]bool{}
	err := r.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		tmpl, err := route.GetPathTemplate()
		if err != nil {
			return err
		}
		ms, err := route.GetMethods()
		if err != nil {
			return err
		}
		for _, m := range ms {
			out[m+" "+tmpl] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk router: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("parsed zero routes — the router is empty, so this test is not checking anything")
	}
	return out
}

func routesFromSpec(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../../docs/swagger.json")
	if err != nil {
		t.Fatalf("read docs/swagger.json (run `bash doc.sh`): %v", err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse docs/swagger.json: %v", err)
	}
	methods := map[string]string{
		"get": "GET", "post": "POST", "put": "PUT",
		"patch": "PATCH", "delete": "DELETE",
	}
	out := map[string]bool{}
	for path, ops := range spec.Paths {
		for op := range ops {
			if m, ok := methods[op]; ok {
				out[m+" "+path] = true
			}
		}
	}
	return out
}

func difference(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
