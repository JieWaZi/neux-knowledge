package remote

import (
	"reflect"
	"strings"
	"testing"
)

// This test constrains the remote connection object's dependency graph; adding a local core, model port, or shadow index field violates the boundary.
func TestCustomConnectionDoesNotOwnLocalExecutionOrStorage(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if seen[typ] {
			return
		}
		seen[typ] = true
		p := typ.PkgPath()
		if strings.HasPrefix(p, "github.com/JieWaZi/neux-knowledge/") && p != "github.com/JieWaZi/neux-knowledge/remote" {
			t.Fatalf("HTTP连接持有本地知识依赖: %s", typ)
		}
		if typ.Kind() == reflect.Struct && p == "github.com/JieWaZi/neux-knowledge/remote" {
			for i := 0; i < typ.NumField(); i++ {
				visit(typ.Field(i).Type)
			}
		}
	}
	visit(reflect.TypeFor[Custom]())
}
