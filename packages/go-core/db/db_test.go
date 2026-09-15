package db

import "testing"

func TestSchemaSearchPathQuotesIdentifiers(t *testing.T) {
	got := SchemaSearchPath("identity", `odd"name`, "public")
	want := `"identity","odd""name","public"`
	if got != want {
		t.Fatalf("SchemaSearchPath() = %q, want %q", got, want)
	}
}
