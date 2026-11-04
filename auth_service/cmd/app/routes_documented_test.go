package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

type registeredRoute struct {
	path    string
	methods map[string]bool
	source  string
}

var (
	methodedRegistration = regexp.MustCompile(`\.\s*HandleFunc\(\s*"(/[^"]+)"\s*,[^)]*\)\s*\.Methods\(\s*([^)]*)\)`)
	bareRegistration     = regexp.MustCompile(`\.\s*HandleFunc\(\s*"(/[^"]+)"\s*,[^)]*\)`)
	methodWord           = regexp.MustCompile(`Method[A-Za-z]+|"[A-Za-z]+"`)
)

func normalizePath(p string) string {

	re := regexp.MustCompile(`:(\w+)`)
	return re.ReplaceAllString(p, "{$1}")
}

func parseRegisteredRoutes(t *testing.T) map[string]registeredRoute {
	t.Helper()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("cannot read main.go: %v", err)
	}
	text := string(src)
	routes := map[string]registeredRoute{}
	add := func(path, source string, methods ...string) {
		norm := normalizePath(path)
		m := map[string]bool{}
		for _, x := range methods {
			m[strings.ToLower(x)] = true
		}
		routes[norm] = registeredRoute{path: norm, methods: m, source: source}
	}
	methodedIdx := methodedRegistration.FindAllStringSubmatchIndex(text, -1)
	for _, m := range methodedIdx {
		var ms []string
		for _, tok := range methodWord.FindAllString(text[m[4]:m[5]], -1) {
			ms = append(ms, strings.Trim(tok, `"`))
		}

		cleaned := make([]string, 0, len(ms))
		for _, x := range ms {
			if i := strings.LastIndex(x, "."); i >= 0 {
				x = x[i+1:]
			}
			cleaned = append(cleaned, strings.ToLower(strings.TrimPrefix(x, "Method")))
		}
		add(text[m[2]:m[3]], text[m[0]:m[1]], cleaned...)
	}
	for _, m := range bareRegistration.FindAllStringSubmatchIndex(text, -1) {

		inMethoded := false
		for _, mm := range methodedIdx {
			if m[0] >= mm[0] && m[1] <= mm[1] {
				inMethoded = true
				break
			}
		}
		if inMethoded {
			continue
		}
		add(text[m[2]:m[3]], text[m[0]:m[1]])
	}
	if len(routes) == 0 {
		t.Fatal("the regexp no longer matches how routes are registered: parsed zero routes from main.go")
	}
	return routes
}

type swaggerSpec struct {
	Paths map[string]map[string]json.RawMessage `json:"paths"`
}

func TestEveryRouteIsDocumented(t *testing.T) {
	routes := parseRegisteredRoutes(t)

	specBytes, err := os.ReadFile("../../docs/swagger.json")
	if err != nil {
		t.Fatalf("cannot read docs/swagger.json (run doc.sh): %v", err)
	}
	var spec swaggerSpec
	if err := json.Unmarshal(specBytes, &spec); err != nil {
		t.Fatalf("cannot parse docs/swagger.json: %v", err)
	}
	methods := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true, "head": true, "options": true}

	var notDocumented, undocumentedMethods, notRegistered []string
	for path, route := range routes {
		ops, ok := spec.Paths[path]
		if !ok {
			notDocumented = append(notDocumented, route.source)
			continue
		}
		if len(route.methods) == 0 {

			hasAny := false
			for m := range ops {
				if methods[strings.ToLower(m)] {
					hasAny = true
					break
				}
			}
			if !hasAny {
				undocumentedMethods = append(undocumentedMethods, "registered any-method "+path+" but spec documents no methods")
			}
			continue
		}
		for m := range route.methods {
			if _, ok := ops[m]; !ok {
				undocumentedMethods = append(undocumentedMethods, "registered "+strings.ToUpper(m)+" "+path+" is not in the spec")
			}
		}
	}
	for path := range spec.Paths {
		if _, ok := routes[path]; !ok {
			notRegistered = append(notRegistered, path)
		}
	}

	if len(notDocumented) > 0 || len(undocumentedMethods) > 0 || len(notRegistered) > 0 {
		t.Errorf("routes and spec disagree:\n  registered but not documented: %v\n  documented methods missing: %v\n  documented but not registered: %v",
			notDocumented, undocumentedMethods, notRegistered)
	}
}
