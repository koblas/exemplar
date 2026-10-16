package schema

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"

	schemas "github.com/koblas/exemplar/schema"
)

// TestSchemaDrift fails when a Go type and its JSON Schema disagree on the
// set of fields, at any depth. The schema is the source of truth: fix the Go
// type to match, or change the schema deliberately.
func TestSchemaDrift(t *testing.T) {
	tests := []struct {
		doc Document
		typ any
	}{
		{DocRule, Rule{}},
		{DocGlobal, GlobalConfig{}},
		{DocRepo, RepoConfig{}},
		{DocEvalCases, EvalCases{}},
		{DocEligibility, Eligibility{}},
		{DocRetention, Retention{}},
		{DocFixture, Fixture{}},
		{DocAdversarial, AdversarialCase{}},
		{DocVerifierLabel, VerifierLabel{}},
		{DocCategorizerLabel, CategorizerLabel{}},
	}
	for _, tt := range tests {
		t.Run(string(tt.doc), func(t *testing.T) {
			root := loadSchemaDoc(t, string(tt.doc)+".schema.json")
			compareNode(t, string(tt.doc), root, root, reflect.TypeOf(tt.typ))
		})
	}
}

var rawMessageType = reflect.TypeFor[json.RawMessage]()

func compareNode(t *testing.T, path string, doc, node map[string]any, typ reflect.Type) {
	t.Helper()
	doc, node = resolve(t, doc, node)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == rawMessageType {
		return
	}
	switch typ.Kind() { //nolint:exhaustive // only composite kinds have schema structure to compare
	case reflect.Struct:
		props, _ := node["properties"].(map[string]any)
		if props == nil {
			t.Errorf("%s: Go struct %s but schema node has no properties", path, typ)
			return
		}
		goFields := map[string]reflect.Type{}
		for f := range typ.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			goFields[name] = f.Type
		}
		for name, ft := range goFields {
			sub, ok := props[name].(map[string]any)
			if !ok {
				t.Errorf("%s: Go field %q is not in the schema", path, name)
				continue
			}
			compareNode(t, path+"."+name, doc, sub, ft)
		}
		for name := range props {
			if _, ok := goFields[name]; !ok {
				t.Errorf("%s: schema property %q has no Go field", path, name)
			}
		}
	case reflect.Slice:
		if items, ok := node["items"].(map[string]any); ok {
			compareNode(t, path+"[]", doc, items, typ.Elem())
		}
	case reflect.Map:
		if ap, ok := node["additionalProperties"].(map[string]any); ok {
			compareNode(t, path+"{}", doc, ap, typ.Elem())
		}
	}
}

// resolve follows $ref to a local $defs entry or another schema file.
func resolve(t *testing.T, doc, node map[string]any) (map[string]any, map[string]any) {
	t.Helper()
	for {
		ref, ok := node["$ref"].(string)
		if !ok {
			return doc, node
		}
		file, frag, _ := strings.Cut(ref, "#")
		if file != "" {
			doc = loadSchemaDoc(t, file)
		}
		node = doc
		for part := range strings.SplitSeq(strings.TrimPrefix(frag, "/"), "/") {
			if part == "" {
				continue
			}
			next, ok := node[part].(map[string]any)
			if !ok {
				t.Fatalf("unresolvable $ref %q", ref)
			}
			node = next
		}
	}
}

func loadSchemaDoc(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := fs.ReadFile(schemas.FS, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return doc
}

func TestEverySchemaHasADocument(t *testing.T) {
	known := []Document{DocRule, DocGlobal, DocRepo, DocEvalCases, DocEligibility, DocRetention, DocAdversarial, DocFixture, DocVerifierLabel, DocCategorizerLabel}
	entries, err := fs.ReadDir(schemas.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := Document(strings.TrimSuffix(e.Name(), ".schema.json"))
		if !slices.Contains(known, name) {
			t.Errorf("schema %s has no Document constant or drift test", e.Name())
		}
	}
}
