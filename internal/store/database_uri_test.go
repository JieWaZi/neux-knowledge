package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/vfs"
)

type databasePathVFS struct {
	vfs.VFS          // 其余操作保留默认VFS，本测试在文件系统访问前终止。
	paths   []string // 记录SQLite完成URI解码后的真实路径。
}

func (v *databasePathVFS) FullPathname(name string) (string, error) {
	v.paths = append(v.paths, name)
	return "", sqlite3.CANTOPEN
}

func TestDatabaseURIKeepsFilesystemPath(t *testing.T) {
	for _, name := range []string{`C:\knowledge\library.db`, `D:/knowledge/library.db`, `D:\知识 库\100%#原件.db`, `\\server\share\library.db`, `//server/share/library.db`, `//localhost/share/library.db`, `/tmp/知识 库 #100%.db`, `relative.db`} {
		t.Run(name, func(t *testing.T) {
			probe := &databasePathVFS{VFS: vfs.Find("os")}
			vfs.Register("neux_database_path_test", probe)
			defer vfs.Unregister("neux_database_path_test")
			conn, err := driver.Open(databaseURI(name) + "?vfs=neux_database_path_test")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// Open是惰性的；必须真正连接，才能验证SQLite实际交给VFS的路径。
			if err = conn.PingContext(context.Background()); !errors.Is(err, sqlite3.CANTOPEN) {
				t.Fatalf("controlled probe did not intercept connection: %v", err)
			}
			if !reflect.DeepEqual(probe.paths, []string{name}) {
				t.Fatalf("SQLite received %q, want original path %q", probe.paths, name)
			}
		})
	}
}

func TestDatabasePathCreatesAndReopensExpectedFile(t *testing.T) {
	for _, relative := range []bool{false, true} {
		t.Run(map[bool]string{false: "absolute", true: "relative"}[relative], func(t *testing.T) {
			root := t.TempDir()
			expected := filepath.Join(root, "知识 库 #100%", "索引 #%.db")
			name := expected
			if relative {
				t.Chdir(root)
				name = filepath.Join("知识 库 #100%", "索引 #%.db")
			}
			raw := filepath.Join(root, "raw")
			for attempt := 0; attempt < 2; attempt++ {
				store, err := Open(context.Background(), name, raw)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err = os.Stat(expected); err != nil {
					t.Fatalf("database was not created at the requested filesystem path: %v", err)
				}
			}
		})
	}
}
