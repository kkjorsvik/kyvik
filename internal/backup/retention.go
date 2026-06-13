package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PruneBackups removes old backup archives from backupDir, keeping only
// the most recent keepCount files. Returns the number of files removed.
func PruneBackups(backupDir string, keepCount int) (int, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read backup dir: %w", err)
	}

	// Collect only .tar.gz backup files.
	type backupFile struct {
		name    string
		modTime int64
	}
	var files []backupFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, backupFile{name: e.Name(), modTime: info.ModTime().UnixNano()})
	}

	if len(files) <= keepCount {
		return 0, nil
	}

	// Sort newest first.
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime > files[j].modTime
	})

	removed := 0
	for _, f := range files[keepCount:] {
		path := filepath.Join(backupDir, f.name)
		if err := os.Remove(path); err != nil {
			return removed, fmt.Errorf("remove %s: %w", f.name, err)
		}
		removed++
	}

	return removed, nil
}
