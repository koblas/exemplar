package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"
	"github.com/santhosh-tekuri/jsonschema/v6"

	schemas "github.com/koblas/exemplar/schema"
)

// Document names a contract and the schema file that defines it.
type Document string

// Contracts with a schema in schema/.
const (
	DocRule             Document = "rule"
	DocGlobal           Document = "global"
	DocRepo             Document = "repo"
	DocEvalCases        Document = "eval-cases"
	DocEligibility      Document = "eligibility"
	DocRetention        Document = "retention"
	DocAdversarial      Document = "adversarial-case"
	DocFixture          Document = "fixture"
	DocVerifierLabel    Document = "verifier-label"
	DocCategorizerLabel Document = "categorizer-label"
)

const schemaBase = "https://exemplar.internal/schema/"

var (
	compileOnce sync.Once
	compiled    map[Document]*jsonschema.Schema
	compileErr  error //nolint:errname // compileAll's cached result, not a sentinel
)

func compileAll() {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	entries, err := fs.ReadDir(schemas.FS, ".")
	if err != nil {
		compileErr = err
		return
	}
	var docs []Document
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".schema.json")
		if !ok {
			continue
		}
		data, err := fs.ReadFile(schemas.FS, e.Name())
		if err != nil {
			compileErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			compileErr = fmt.Errorf("schema %s: %w", e.Name(), err)
			return
		}
		if err := c.AddResource(schemaBase+e.Name(), doc); err != nil {
			compileErr = fmt.Errorf("schema %s: %w", e.Name(), err)
			return
		}
		docs = append(docs, Document(name))
	}
	compiled = make(map[Document]*jsonschema.Schema, len(docs))
	for _, d := range docs {
		s, err := c.Compile(schemaBase + string(d) + ".schema.json")
		if err != nil {
			compileErr = fmt.Errorf("compile schema %s: %w", d, err)
			return
		}
		compiled[d] = s
	}
}

func schemaFor(d Document) (*jsonschema.Schema, error) {
	compileOnce.Do(compileAll)
	if compileErr != nil {
		return nil, compileErr
	}
	s, ok := compiled[d]
	if !ok {
		return nil, fmt.Errorf("no schema for %q", d)
	}
	return s, nil
}

// ValidationError reports a document that does not match its schema.
type ValidationError struct {
	Document Document
	Err      error
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s does not match schema: %v", e.Document, e.Err)
}

func (e *ValidationError) Unwrap() error { return e.Err }

// Decode validates YAML (or JSON) data against the document's schema and then
// decodes it into out, rejecting unknown fields. Schema validation runs first
// so errors point at the contract, not at Go types.
func Decode(d Document, data []byte, out any) error {
	s, err := schemaFor(d)
	if err != nil {
		return err
	}
	jsonData, err := yaml.YAMLToJSON(data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", d, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("parse %s: %w", d, err)
	}
	if err := s.Validate(inst); err != nil {
		if ve, ok := errors.AsType[*jsonschema.ValidationError](err); ok {
			return &ValidationError{Document: d, Err: ve}
		}
		return fmt.Errorf("validate %s: %w", d, err)
	}
	dec := json.NewDecoder(bytes.NewReader(jsonData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", d, err)
	}
	return nil
}
