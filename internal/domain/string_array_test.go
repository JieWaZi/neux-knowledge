package types

import "testing"

func TestStringArrayScansSQLiteTextAndBlob(t *testing.T) {
	for _, value := range []any{`["concept/a"]`, []byte(`["concept/a"]`)} {
		var got StringArray
		if err := got.Scan(value); err != nil || len(got) != 1 || got[0] != "concept/a" {
			t.Fatalf("lost SQLite JSON %T: %v %v", value, got, err)
		}
	}
	got := StringArray{"old"}
	if err := got.Scan(nil); err != nil || got != nil {
		t.Fatal("NULL retained old data")
	}
	if err := got.Scan(123); err == nil {
		t.Fatal("unsupported JSON type silently accepted")
	}
}
