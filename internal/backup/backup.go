// Package backup implements full-instance backup and restore, per-agent
// export/import, and scheduled backup management for Kyvik.
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
	"time"

	"github.com/dustin/go-humanize"
)

// BackupMetadata is written as backup.json inside every backup archive.
type BackupMetadata struct {
	Version    string    `json:"version"`
	Timestamp  time.Time `json:"timestamp"`
	AgentCount int       `json:"agent_count"`
	DBSize     int64     `json:"db_size"`
}

// BackupResult holds the outcome of a single backup run.
type BackupResult struct {
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	SizeHuman  string    `json:"size_human"`
	Duration   string    `json:"duration"`
	Timestamp  time.Time `json:"timestamp"`
	AgentCount int       `json:"agent_count"`
	DBSize     int64     `json:"db_size"`
	Error      string    `json:"error,omitempty"`
}

// CreateBackup creates a consistent snapshot of the SQLite database and
// any associated data directories (souls/, identities/, skills/) as a
// compressed tar.gz archive in backupDir.
//
// Strategy: BEGIN IMMEDIATE on a dedicated connection to briefly block
// writes, copy the database + WAL file to a temp dir, ROLLBACK to
// release the lock, then checkpoint the copy to merge WAL into the
// main DB file. Finally bundle everything into an archive.
func CreateBackup(ctx context.Context, db *sql.DB, dbPath, backupDir string) (*BackupResult, error) {
	start := time.Now()

	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		return nil, fmt.Errorf("create backup dir: %w", err)
	}

	// Create a temp dir for the snapshot.
	tmpDir, err := os.MkdirTemp("", "kyvik-backup-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// 1. Acquire a write lock with BEGIN IMMEDIATE.
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("get connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("begin immediate: %w", err)
	}

	// 2. Copy database file.
	dbCopyPath := filepath.Join(tmpDir, "kyvik.db")
	if err := copyFile(dbPath, dbCopyPath); err != nil {
		conn.ExecContext(ctx, "ROLLBACK")
		return nil, fmt.Errorf("copy db: %w", err)
	}

	// 3. Copy WAL file if it exists.
	walPath := dbPath + "-wal"
	walCopyPath := filepath.Join(tmpDir, "kyvik.db-wal")
	if _, err := os.Stat(walPath); err == nil {
		if err := copyFile(walPath, walCopyPath); err != nil {
			conn.ExecContext(ctx, "ROLLBACK")
			return nil, fmt.Errorf("copy wal: %w", err)
		}
	}

	// 4. Release the write lock.
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		return nil, fmt.Errorf("rollback: %w", err)
	}

	// 5. Checkpoint the copy to merge WAL into the main DB.
	if err := checkpointCopy(dbCopyPath); err != nil {
		return nil, fmt.Errorf("checkpoint copy: %w", err)
	}
	// Remove WAL/SHM files from copy after checkpoint.
	os.Remove(walCopyPath)
	os.Remove(filepath.Join(tmpDir, "kyvik.db-shm"))

	// Get DB info for metadata.
	dbInfo, _ := os.Stat(dbCopyPath)
	dbSize := dbInfo.Size()

	agentCount := countAgents(ctx, db)

	// Write backup.json metadata.
	meta := BackupMetadata{
		Version:    "1",
		Timestamp:  start,
		AgentCount: agentCount,
		DBSize:     dbSize,
	}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	metaPath := filepath.Join(tmpDir, "backup.json")
	if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
		return nil, fmt.Errorf("write metadata: %w", err)
	}

	// Copy data directories if they exist.
	dataDir := filepath.Dir(dbPath)
	for _, dir := range []string{"souls", "identities", "skills"} {
		src := filepath.Join(dataDir, dir)
		if info, err := os.Stat(src); err == nil && info.IsDir() {
			dst := filepath.Join(tmpDir, dir)
			if err := copyDir(src, dst); err != nil {
				return nil, fmt.Errorf("copy %s dir: %w", dir, err)
			}
		}
	}

	// Create tar.gz archive.
	archiveName := fmt.Sprintf("kyvik-backup-%s.tar.gz", start.Format("20060102-150405"))
	archivePath := filepath.Join(backupDir, archiveName)
	if err := createTarGz(archivePath, tmpDir); err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}

	archiveInfo, err := os.Stat(archivePath)
	if err != nil {
		return nil, fmt.Errorf("stat archive: %w", err)
	}

	return &BackupResult{
		Path:       archivePath,
		Size:       archiveInfo.Size(),
		SizeHuman:  humanize.Bytes(uint64(archiveInfo.Size())),
		Duration:   time.Since(start).Round(time.Millisecond).String(),
		Timestamp:  start,
		AgentCount: agentCount,
		DBSize:     dbSize,
	}, nil
}

// countAgents returns the number of agents in the database.
func countAgents(ctx context.Context, db *sql.DB) int {
	var count int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agents").Scan(&count)
	return count
}

// checkpointCopy opens the copied database and checkpoints WAL into the main file.
func checkpointCopy(dbPath string) error {
	copyDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer copyDB.Close()

	_, err = copyDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// copyFile copies a single file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// copyDir recursively copies a directory from src to dst.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, _ := filepath.Rel(src, path)
		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, 0o750)
		}
		return copyFile(path, dstPath)
	})
}

// createTarGz creates a gzipped tar archive from the contents of srcDir.
func createTarGz(archivePath, srcDir string) error {
	outFile, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	gzw := gzip.NewWriter(outFile)
	defer gzw.Close()

	tw := tar.NewWriter(gzw)
	defer tw.Close()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, _ := filepath.Rel(srcDir, path)
		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(tw, f)
		return err
	})
}
