package app

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var specVerbs = map[string]bool{
	"get": true, "post": true, "put": true,
	"delete": true, "patch": true, "head": true, "options": true,
}

// registeredRoutes returns the "METHOD path" pairs the router answers.
func registeredRoutes(t *testing.T) []string {
	t.Helper()

	var routes []string
	for _, route := range NewRouter(nil).Routes {
		for _, method := range route.Methods {
			routes = append(routes, method+" "+route.Path)
		}
	}
	return routes
}

// specRoutes returns the "METHOD path" pairs listed in docs/swagger.yaml.
func specRoutes(t *testing.T) []string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "swagger.yaml"))
	if err != nil {
		t.Fatalf("read docs/swagger.yaml: %v", err)
	}

	var spec struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse docs/swagger.yaml: %v", err)
	}

	var routes []string
	for path, operations := range spec.Paths {
		for verb := range operations {
			if !specVerbs[verb] {
				continue
			}
			routes = append(routes, strings.ToUpper(verb)+" "+path)
		}
	}
	return routes
}

// TestEveryRouteIsDocumented compares the registered routes with the paths
// of docs/swagger.yaml in both directions: a route the spec does not carry is
// not promised, and a spec path with no route is a promise the code breaks.
func TestEveryRouteIsDocumented(t *testing.T) {
	registered := registeredRoutes(t)
	documented := specRoutes(t)

	// A zero is never an honest "nothing to check": it means the
	// registration no longer looks like the router.Route(...) calls this
	// test reads, and the comparison below would pass on nothing.
	if len(registered) == 0 {
		t.Fatal("parsed zero routes — the registration no longer looks like router.Route(path, methods, handler) calls")
	}

	registeredSet := map[string]bool{}
	for _, r := range registered {
		registeredSet[r] = true
	}
	documentedSet := map[string]bool{}
	for _, d := range documented {
		documentedSet[d] = true
	}

	var undocumented []string
	for _, r := range registered {
		if !documentedSet[r] {
			undocumented = append(undocumented, r)
		}
	}
	sort.Strings(undocumented)
	if len(undocumented) > 0 {
		t.Errorf("registered but missing from docs/swagger.yaml: %v", strings.Join(undocumented, ", "))
	}

	var unrouted []string
	for _, d := range documented {
		if !registeredSet[d] {
			unrouted = append(unrouted, d)
		}
	}
	sort.Strings(unrouted)
	if len(unrouted) > 0 {
		t.Errorf("in docs/swagger.yaml but not registered: %v", strings.Join(unrouted, ", "))
	}
}
