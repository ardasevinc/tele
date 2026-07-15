package output

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaBase = "https://raw.githubusercontent.com/ardasevinc/tele/main/schemas/v1/"

func TestCanonicalGoldensValidateAgainstPublishedSchemas(t *testing.T) {
	schemas := compilePublicSchemas(t)
	tests := []struct {
		golden string
		schema string
		jsonl  bool
	}{
		{golden: "envelope.json", schema: "envelope.schema.json"},
		{golden: "error.json", schema: "error.schema.json"},
		{golden: "records.jsonl", schema: "record.schema.json", jsonl: true},
	}
	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", "golden", tt.golden))
			if err != nil {
				t.Fatal(err)
			}
			if tt.jsonl {
				scanner := bufio.NewScanner(bytes.NewReader(b))
				line := 0
				for scanner.Scan() {
					line++
					validateJSON(t, schemas[tt.schema], scanner.Bytes(), line)
				}
				if err := scanner.Err(); err != nil {
					t.Fatal(err)
				}
				if line == 0 {
					t.Fatal("JSONL golden is empty")
				}
				return
			}
			validateJSON(t, schemas[tt.schema], b, 1)
		})
	}
}

func TestJSONLDataSchemaRejectsUnpublishedFields(t *testing.T) {
	schema := compilePublicSchemas(t)["record.schema.json"]
	record := []byte(`{"schema_version":"tele/v1","type":"data","data":{"phone":"+15555550123"}}`)
	var value any
	if err := json.Unmarshal(record, &value); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err == nil {
		t.Fatal("record schema accepted an unpublished private field")
	}
}

func compilePublicSchemas(t *testing.T) map[string]*jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	for _, name := range []string{"envelope.schema.json", "error.schema.json", "record.schema.json", "command-envelope.schema.json"} {
		path := filepath.Join("..", "..", "schemas", "v1", name)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(b, &document); err != nil {
			t.Fatalf("decode schema %s: %v", name, err)
		}
		if err := compiler.AddResource(schemaBase+name, document); err != nil {
			t.Fatalf("add schema %s: %v", name, err)
		}
	}
	compiled := make(map[string]*jsonschema.Schema, 4)
	for _, name := range []string{"envelope.schema.json", "error.schema.json", "record.schema.json", "command-envelope.schema.json"} {
		schema, err := compiler.Compile(schemaBase + name)
		if err != nil {
			t.Fatalf("compile schema %s: %v", name, err)
		}
		compiled[name] = schema
	}
	return compiled
}

func validateJSON(t *testing.T, schema *jsonschema.Schema, b []byte, line int) {
	t.Helper()
	var value any
	if err := json.Unmarshal(b, &value); err != nil {
		t.Fatalf("decode line %d: %v", line, err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("validate line %d: %v", line, err)
	}
}
