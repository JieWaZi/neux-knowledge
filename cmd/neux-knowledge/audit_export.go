package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JieWaZi/neux-knowledge/core"
)

func exportPathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func exportTarget(target, dbPath, rawPath string) (string, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	db, err := filepath.Abs(dbPath)
	if err == nil {
		db, err = filepath.EvalSymlinks(db)
	}
	if err != nil {
		return "", err
	}
	raw, err := filepath.Abs(rawPath)
	if err == nil {
		raw, err = filepath.EvalSymlinks(raw)
	}
	if err != nil {
		return "", err
	}
	if exportPathContains(abs, db) || exportPathContains(abs, raw) || exportPathContains(raw, abs) || exportPathContains(db+".processors", abs) {
		return "", fmt.Errorf("export directory overlaps managed database or raw storage")
	}
	return abs, nil
}

// writeMarkdownExport creates only a new directory and safe leaf files; the manifest is written last to mark the complete output set.
// On output errors, clean only files created by this invocation; never overwrite existing directories or write back to SQLite.
func writeMarkdownExport(ctx context.Context, target, dbPath, rawPath string, bundle *core.MarkdownExport) (*core.MarkdownExport, error) {
	if bundle == nil {
		return nil, fmt.Errorf("export bundle is missing")
	}
	target, err := exportTarget(target, dbPath, rawPath)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, file := range bundle.Files {
		if file.Path == "" || filepath.Base(file.Path) != file.Path || strings.ContainsAny(file.Path, "/\\") || filepath.Ext(file.Path) != ".md" || seen[file.Path] {
			return nil, fmt.Errorf("export filenames must be safe unique Markdown leaf names")
		}
		if fmt.Sprintf("%x", sha256.Sum256([]byte(file.Markdown))) != file.SHA256 {
			return nil, fmt.Errorf("export file digest mismatch")
		}
		seen[file.Path] = true
	}
	manifest := *bundle
	manifest.Files = append([]core.MarkdownFile{}, bundle.Files...)
	for i := range manifest.Files {
		manifest.Files[i].Markdown = ""
	}
	manifestBytes, err := json.MarshalIndent(&manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = os.Mkdir(target, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		_ = os.Remove(target)
		return nil, err
	}
	created := []string{}
	complete := false
	defer func() {
		if !complete {
			for _, name := range created {
				_ = root.Remove(name)
			}
		}
		_ = root.Close()
		if !complete {
			_ = os.Remove(target)
		}
	}()
	write := func(name string, data []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		created = append(created, name)
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	for _, file := range bundle.Files {
		if err = write(file.Path, []byte(file.Markdown)); err != nil {
			return nil, err
		}
	}
	if err = write("manifest.json", append(manifestBytes, '\n')); err != nil {
		return nil, err
	}
	complete = true
	return &manifest, nil
}
