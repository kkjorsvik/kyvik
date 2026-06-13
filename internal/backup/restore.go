package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ValidateArchive opens the archive, reads backup.json, and runs
// an integrity check on the embedded database.
func ValidateArchive(archivePath string) (*BackupMetadata, error) {
	tmpDir, err := os.MkdirTemp("", "kyvik-validate-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extractTarGz(archivePath, tmpDir); err != nil {
		return nil, fmt.Errorf("extract archive: %w", err)
	}

	// Read metadata.
	meta, err := readMetadata(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	// Integrity check on the database.
	dbPath := filepath.Join(tmpDir, "kyvik.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("archive missing kyvik.db")
	}

	if err := integrityCheck(dbPath); err != nil {
		return nil, fmt.Errorf("integrity check failed: %w", err)
	}

	return meta, nil
}

// RestoreBackup extracts the archive to targetDir, replacing the existing
// database and data directories. The caller is responsible for stopping
// Kyvik before calling this function.
func RestoreBackup(_ context.Context, archivePath, targetDir string) (*BackupMetadata, error) {
	// Validate first.
	meta, err := ValidateArchive(archivePath)
	if err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Extract to temp dir first.
	tmpDir, err := os.MkdirTemp("", "kyvik-restore-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extractTarGz(archivePath, tmpDir); err != nil {
		return nil, fmt.Errorf("extract archive: %w", err)
	}

	// Ensure target directory exists.
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return nil, fmt.Errorf("create target dir: %w", err)
	}

	// Replace database.
	srcDB := filepath.Join(tmpDir, "kyvik.db")
	dstDB := filepath.Join(targetDir, "kyvik.db")

	// Remove old WAL/SHM files.
	os.Remove(dstDB + "-wal")
	os.Remove(dstDB + "-shm")

	if err := copyFile(srcDB, dstDB); err != nil {
		return nil, fmt.Errorf("restore database: %w", err)
	}

	// Restore data directories.
	for _, dir := range []string{"souls", "identities", "skills"} {
		srcPath := filepath.Join(tmpDir, dir)
		dstPath := filepath.Join(targetDir, dir)

		if info, err := os.Stat(srcPath); err == nil && info.IsDir() {
			// Remove existing and copy from backup.
			os.RemoveAll(dstPath)
			if err := copyDir(srcPath, dstPath); err != nil {
				return nil, fmt.Errorf("restore %s dir: %w", dir, err)
			}
		}
	}

	return meta, nil
}

// readMetadata parses backup.json from the extracted directory.
func readMetadata(dir string) (*BackupMetadata, error) {
	data, err := os.ReadFile(filepath.Join(dir, "backup.json"))
	if err != nil {
		return nil, err
	}
	var meta BackupMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// integrityCheck opens the database and runs PRAGMA integrity_check.
func integrityCheck(dbPath string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity check: %s", result)
	}
	return nil
}

// extractTarGz extracts a .tar.gz archive to dstDir.
func extractTarGz(archivePath, dstDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Sanitize path to prevent directory traversal.
		cleanName := filepath.Clean(header.Name)
		if strings.Contains(cleanName, "..") {
			return fmt.Errorf("invalid path in archive: %s", header.Name)
		}
		target := filepath.Join(dstDir, cleanName)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			outFile, err := os.Create(target)
			if err != nil {
				return err
			}
			// Limit extraction size to 10GB to prevent decompression bombs.
			if _, err := io.Copy(outFile, io.LimitReader(tr, 10<<30)); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		}
	}
	return nil
}
