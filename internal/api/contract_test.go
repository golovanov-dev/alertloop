package api

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	apispec "github.com/golovanov-dev/alertloop/api"
	"github.com/golovanov-dev/alertloop/internal/config"
)

// Every field a /v1 response carries is declared in api/openapi.yaml, at every
// level of nesting, and every field the schema requires is there. A field
// added to a Go struct and not to the contract fails here, and so does one the
// contract promises and the response dropped.
func TestResponsesMatchOpenAPISchemas(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(apispec.OpenAPIYAML, &doc); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	schemas := doc.Components.Schemas

	ts, _ := newTestServer(t, map[string]string{"ingest-key": config.ScopeIngest})
	ev := `{"status":"firing","type":"incident","severity":"error","source":"s","message":"m","dedupe_key":"k","payload":{"a":1}}`
	_, created := doJSON(t, "POST", ts.URL+"/v1/events", adminTok, ev)
	id, _ := created["id"].(string)
	_, summary := doJSON(t, "POST", ts.URL+"/v1/events", "ingest-key", ev)

	checks := []struct {
		what, method, path, body, schema string
		got                              map[string]any
	}{
		{what: "ingest, full scope", schema: "IngestResult", got: created},
		{what: "ingest, ingest scope", schema: "IngestSummary", got: summary},
		{what: "event list", method: "GET", path: "/v1/events", schema: "EventList"},
		{what: "event", method: "GET", path: "/v1/events/" + id, schema: "Event"},
		{what: "action", method: "POST", path: "/v1/events/" + id + "/ack", schema: "Event"},
		{what: "delivery list", method: "GET", path: "/v1/delivery-attempts", schema: "DeliveryAttemptList"},
		{what: "stats", method: "GET", path: "/v1/stats", schema: "Stats"},
		{what: "info", method: "GET", path: "/v1/info", schema: "Info"},
		{what: "routing table", method: "GET", path: "/v1/routing", schema: "RoutingTable"},
		{what: "routing preview", method: "POST", path: "/v1/routing/preview", body: `{"type":"incident"}`, schema: "RoutingPreview"},
		{what: "error", method: "GET", path: "/v1/events/missing", schema: "Error"},
	}
	for _, c := range checks {
		got := c.got
		if got == nil {
			_, got = doJSON(t, c.method, ts.URL+c.path, adminTok, c.body)
		}
		if len(got) == 0 {
			t.Errorf("%s: empty response", c.what)
			continue
		}
		checkDeclared(t, c.what, got, map[string]any{"$ref": "#/components/schemas/" + c.schema}, schemas)
	}
}

// checkDeclared fails for every key of obj that schema does not declare and
// every key it requires that obj lacks, and descends into declared objects and
// arrays of objects.
func checkDeclared(t *testing.T, where string, obj map[string]any, schema map[string]any, schemas map[string]any) {
	t.Helper()
	for _, key := range required(schema, schemas) {
		if _, ok := obj[key]; !ok {
			t.Errorf("%s: required field %q is missing from the response", where, key)
		}
	}
	props := properties(schema, schemas)
	if props == nil {
		return // free-form: additionalProperties, payload
	}
	for key, value := range obj {
		prop, ok := props[key].(map[string]any)
		if !ok {
			t.Errorf("%s: field %q is not in openapi.yaml", where, key)
			continue
		}
		switch v := value.(type) {
		case map[string]any:
			checkDeclared(t, where+"."+key, v, prop, schemas)
		case []any:
			items, _ := resolve(prop, schemas)["items"].(map[string]any)
			for _, item := range v {
				if m, ok := item.(map[string]any); ok && items != nil {
					checkDeclared(t, where+"."+key+"[]", m, items, schemas)
				}
			}
		}
	}
}

// properties returns the declared properties of schema, following $ref and
// merging allOf; nil when it declares none.
func properties(schema map[string]any, schemas map[string]any) map[string]any {
	schema = resolve(schema, schemas)
	var out map[string]any
	if p, ok := schema["properties"].(map[string]any); ok {
		out = map[string]any{}
		for k, v := range p {
			out[k] = v
		}
	}
	all, _ := schema["allOf"].([]any)
	for _, part := range all {
		m, _ := part.(map[string]any)
		for k, v := range properties(m, schemas) {
			if out == nil {
				out = map[string]any{}
			}
			out[k] = v
		}
	}
	return out
}

// required returns the names schema requires, following $ref and allOf.
func required(schema map[string]any, schemas map[string]any) []string {
	schema = resolve(schema, schemas)
	var out []string
	names, _ := schema["required"].([]any)
	for _, n := range names {
		if s, ok := n.(string); ok {
			out = append(out, s)
		}
	}
	all, _ := schema["allOf"].([]any)
	for _, part := range all {
		m, _ := part.(map[string]any)
		out = append(out, required(m, schemas)...)
	}
	return out
}

func resolve(schema map[string]any, schemas map[string]any) map[string]any {
	for {
		ref, ok := schema["$ref"].(string)
		if !ok {
			return schema
		}
		next, _ := schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
		if next == nil {
			panic("openapi.yaml: unknown $ref " + ref)
		}
		schema = next
	}
}
