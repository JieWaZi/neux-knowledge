package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestGeneralRulesUseOnlyWeKnoraRegardlessOfIndexMode(t *testing.T) {
	var baseline Bundle
	for _, lite := range []bool{false, true} {
		b, err := LoadOrganize("general", "zh-CN", lite)
		if err != nil {
			t.Fatal(err)
		}
		if len(b.Resources) == 0 {
			t.Fatal("missing WeKnora rules")
		}
		for _, resource := range b.Resources {
			if !strings.HasPrefix(resource.Path, "weknora/") {
				t.Errorf("retired rules delivered: %s", resource.Path)
			}
			digest := sha256.Sum256([]byte(resource.Content))
			if resource.Version != b.Version || resource.SHA256 != hex.EncodeToString(digest[:]) || !strings.Contains(b.Instructions, resource.Content) {
				t.Errorf("incomplete resource: %s", resource.Path)
			}
		}
		for _, marker := range []string{"WeKnora", "wikiLanguage=zh-CN"} {
			if !strings.Contains(b.Instructions, marker) {
				t.Errorf("missing resource binding: %s", marker)
			}
		}
		if !lite {
			baseline = b
		} else if b.SHA256 != baseline.SHA256 {
			t.Fatal("index mode loaded different content rules")
		}
	}
}

func TestLoadRejectsRetiredSoftwareAndInvalidBinding(t *testing.T) {
	for _, kind := range []string{"software", "unknown", ""} {
		if _, err := Load(kind, "zh-CN"); err == nil {
			t.Errorf("accepted kind %q", kind)
		}
	}
	if _, err := Load("general", "fr-FR"); err == nil {
		t.Fatal("unsupported language accepted")
	}
}

func TestLoadDigestIsStableAndBindsLanguage(t *testing.T) {
	a, err := Load("general", "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load("general", "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load("general", "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 == "" || a.SHA256 != b.SHA256 || a.SHA256 == c.SHA256 {
		t.Fatal("invalid language-bound rule digest")
	}
	if !strings.Contains(c.Instructions, "wikiLanguage=en-US") || c.Kind != "general" {
		t.Fatal("binding omitted")
	}
}
