// Package rules embeds and loads versioned knowledge Wiki rules.
package rules

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Version is the stable version identifier of the rule resource bundle.
const Version = "2026-10-21.lite-direct"

// Resource is a logical resource that can be individually addressed, verified, and read within the rule bundle.
type Resource struct {
	Path    string // Path is the stable logical path within the bundle.
	Version string // Version is the version of the rule bundle containing this resource.
	SHA256  string // SHA256 is the lowercase hexadecimal digest of the resource's original bytes.
	Content string // Content is the complete resource body available for direct host projection and reading.
}

// Bundle is the complete rule instruction bundle selected for one knowledge library.
type Bundle struct {
	Version      string     // Version is the rule resource bundle version.
	Kind         string     // Kind is the currently supported general knowledge library type, general.
	WikiLanguage string     // WikiLanguage is the library's default writing language.
	Resources    []Resource // Resources is the complete resource manifest sorted by stable logical path.
	Instructions string     // Instructions contains the complete common and type-specific rule instructions.
	SHA256       string     // SHA256 is a stable digest of the version, type, language binding, and all rule files.
}

//go:embed weknora
var resources embed.FS

// Load returns WeKnora rules for a general knowledge library, with a digest bound to resource content, type, and language.
func Load(kind string, wikiLanguage string) (Bundle, error) {
	return LoadOrganize(kind, wikiLanguage, false)
}

// LoadOrganize's index mode does not change content rules; all stage prompts use the WeKnora contract.
func LoadOrganize(kind string, wikiLanguage string, _ bool) (Bundle, error) {
	if kind != "general" {
		return Bundle{}, fmt.Errorf("unsupported knowledge kind %q", kind)
	}
	if wikiLanguage != "zh-CN" && wikiLanguage != "en-US" {
		return Bundle{}, fmt.Errorf("unsupported wiki language %q", wikiLanguage)
	}
	roots := []string{"weknora"}
	files := make([]string, 0, 16)
	for _, root := range roots {
		if err := fs.WalkDir(resources, root, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasSuffix(name, ".md") {
				files = append(files, name)
			}
			return nil
		}); err != nil {
			return Bundle{}, fmt.Errorf("walk rules %s: %w", root, err)
		}
	}
	sort.Strings(files)

	manifest := make([]Resource, 0, len(files))
	var instructions strings.Builder
	hash := sha256.New()
	writeHashPart(hash, Version)
	writeHashPart(hash, kind)
	writeHashPart(hash, wikiLanguage)
	for _, name := range files {
		content, err := resources.ReadFile(name)
		if err != nil {
			return Bundle{}, fmt.Errorf("read rules %s: %w", name, err)
		}
		resourceHash := sha256.Sum256(content)
		item := Resource{
			Path: name, Version: Version,
			SHA256: hex.EncodeToString(resourceHash[:]), Content: string(content),
		}
		manifest = append(manifest, item)
		writeHashPart(hash, item.Path)
		writeHashPart(hash, item.Version)
		writeHashPart(hash, item.SHA256)
		fmt.Fprintf(&instructions, "\n\n<!-- resource: %s -->\n", name)
		instructions.Write(content)
	}
	languageBinding := fmt.Sprintf("\n\n## 本次任务的语言绑定\nwikiLanguage=%s\n%s", wikiLanguage, languageInstruction(wikiLanguage))
	instructions.WriteString(languageBinding)
	writeHashPart(hash, languageBinding)

	return Bundle{
		Version: Version, Kind: kind, WikiLanguage: wikiLanguage, Resources: manifest,
		Instructions: strings.TrimSpace(instructions.String()),
		SHA256:       hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

type hashWriter interface{ Write([]byte) (int, error) }

func writeHashPart(w hashWriter, value string) {
	fmt.Fprintf(w, "%d:", len(value))
	w.Write([]byte(value))
}

func languageInstruction(language string) string {
	if language == "en-US" {
		return "新建页面和新主题的标题、摘要、正文使用 en-US；更新既有页面沿用其页面语言。界面语言与原件语言不改变共享知识语言。来源摘录保留原文，专有名词、标准缩写和代码符号保留原样。"
	}
	return "新建页面和新主题的标题、摘要、正文使用 zh-CN；更新既有页面沿用其页面语言。界面语言与原件语言不改变共享知识语言。来源摘录保留原文，专有名词、标准缩写和代码符号保留原样。"
}
