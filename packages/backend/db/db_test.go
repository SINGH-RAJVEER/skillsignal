package db

import (
	"context"
	"os"
	"testing"
)

func TestSchemaSearchPathQuotesIdentifiers(t *testing.T) {
	got := SchemaSearchPath("identity", `odd"name`, "public")
	want := `"identity","odd""name","public"`
	if got != want {
		t.Fatalf("SchemaSearchPath() = %q, want %q", got, want)
	}
}

func TestConnectForSetsSearchPathOnEveryConnection(t *testing.T) {
	address := os.Getenv("TEST_DATABASE_URL")
	if address == "" {
		t.Skip("TEST_DATABASE_URL must point to a disposable database")
	}
	ctx := context.Background()
	pool, err := ConnectFor(ctx, address, "identity", "public")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var got string
	if err := pool.QueryRow(ctx, `SELECT current_setting('search_path')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := "identity, public"; got != want {
		t.Fatalf("search_path = %q, want %q", got, want)
	}
}
