package core_test

import (
	"database/sql"
	"testing"
)

func TestCoreDoesNotRegisterHostSQLite3Driver(t *testing.T) {
	for _, name := range sql.Drivers() {
		if name == "sqlite3" {
			t.Fatal("core registered sqlite3, which conflicts with host ncruces driver")
		}
	}
}
