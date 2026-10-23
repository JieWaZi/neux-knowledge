package core_test

import (
	"context"
	"database/sql"
	"testing"
)

// TestCoreAndHostSQLiteConnectionsStayIndependent 验证同一通用驱动不会共享核心与宿主数据。
func TestCoreAndHostSQLiteConnectionsStayIndependent(t *testing.T) {
	e, _ := open(t)
	l := library(t, e, "independent")
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='knowledge_libraries'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("宿主连接混入知识库: %d %v", count, err)
	}
	if _, err = e.GetLibrary(context.Background(), l.ID); err != nil {
		t.Fatal(err)
	}
}
