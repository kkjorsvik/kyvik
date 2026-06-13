package handlers

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/kkjorsvik/kyvik/internal/backup"
)

// BackupPage renders the full backup management page.
func (h *Handlers) BackupPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	mgr := h.kyvik.BackupManager()
	if mgr == nil {
		h.renderPageWithRequest(w, r, "backup", map[string]any{
			"Title":   "Backup",
			"Nav":     "backup",
			"Enabled": false,
		})
		return
	}

	// Load last result from DB if not in memory.
	lastResult := mgr.LastResult()
	if lastResult == nil {
		loaded, _ := mgr.LoadLastResult(ctx)
		lastResult = loaded
	}

	backups, _ := mgr.ListBackups()
	cfg := mgr.Config()

	h.renderPageWithRequest(w, r, "backup", map[string]any{
		"Title":      "Backup",
		"Nav":        "backup",
		"Enabled":    true,
		"Config":     cfg,
		"LastResult": lastResult,
		"Backups":    backups,
	})
}

// BackupNow triggers an immediate backup and returns an HTMX fragment.
func (h *Handlers) BackupNow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mgr := h.kyvik.BackupManager()
	if mgr == nil {
		http.Error(w, "backup not configured", http.StatusBadRequest)
		return
	}

	result := mgr.RunNow(ctx)

	backups, _ := mgr.ListBackups()

	h.renderFragment(w, r, "backup-result", map[string]any{
		"LastResult": &result,
		"Backups":    backups,
	})
}

// BackupDownload serves a backup file for download.
func (h *Handlers) BackupDownload(w http.ResponseWriter, r *http.Request) {
	mgr := h.kyvik.BackupManager()
	if mgr == nil {
		http.Error(w, "backup not configured", http.StatusBadRequest)
		return
	}

	filename := r.PathValue("filename")
	clean := filepath.Base(filename)
	if clean != filename {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	cfg := mgr.Config()
	filePath := filepath.Join(cfg.Path, clean)

	// Verify file is within backup dir.
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	absDir, err := filepath.Abs(cfg.Path)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if len(absPath) <= len(absDir) || absPath[:len(absDir)] != absDir {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", clean))
	w.Header().Set("Content-Type", "application/gzip")
	http.ServeFile(w, r, filePath)
}

// BackupDelete removes a backup file and returns the updated list fragment.
func (h *Handlers) BackupDelete(w http.ResponseWriter, r *http.Request) {
	mgr := h.kyvik.BackupManager()
	if mgr == nil {
		http.Error(w, "backup not configured", http.StatusBadRequest)
		return
	}

	filename := r.PathValue("filename")
	if err := mgr.DeleteBackup(filename); err != nil {
		slog.Warn("backup delete failed", "filename", filename, "error", err)
		http.Error(w, "delete failed", http.StatusBadRequest)
		return
	}

	backups, _ := mgr.ListBackups()
	h.renderFragment(w, r, "backup-list", map[string]any{
		"Backups": backups,
	})
}

// BackupRestore handles a multipart upload of a backup archive for restore.
func (h *Handlers) BackupRestore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Limit upload to 5GB.
	r.Body = http.MaxBytesReader(w, r.Body, 5<<30)

	file, header, err := r.FormFile("backup_file")
	if err != nil {
		http.Error(w, "upload failed", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Save to temp file.
	tmpFile, err := os.CreateTemp("", "kyvik-restore-upload-*.tar.gz")
	if err != nil {
		http.Error(w, "temp file failed", http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, file); err != nil {
		h.serverError(w, r, "saving upload", err)
		return
	}
	tmpFile.Close()

	// Validate the archive.
	meta, err := backup.ValidateArchive(tmpFile.Name())
	if err != nil {
		h.renderFragment(w, r, "backup-restore-result", map[string]any{
			"Error": fmt.Sprintf("Invalid backup: %v", err),
		})
		return
	}

	_ = ctx
	_ = header

	h.renderFragment(w, r, "backup-restore-result", map[string]any{
		"Meta":     meta,
		"Filename": header.Filename,
		"Warning":  "Restore requires stopping Kyvik. Download and use CLI restore, or confirm to replace the database (requires restart).",
	})
}

// AgentExport handles exporting a single agent as a passphrase-encrypted archive.
func (h *Handlers) AgentExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	agentID := r.PathValue("id")
	passphrase := r.FormValue("passphrase")

	if passphrase == "" {
		http.Error(w, "passphrase is required", http.StatusBadRequest)
		return
	}

	tmpDir := os.TempDir()
	archivePath, err := backup.ExportAgent(ctx, h.backupDeps, agentID, passphrase, tmpDir)
	if err != nil {
		h.serverError(w, r, "exporting agent", err)
		return
	}
	defer os.Remove(archivePath)

	filename := filepath.Base(archivePath)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Type", "application/gzip")
	http.ServeFile(w, r, archivePath)
}

// AgentImport handles importing an agent from a passphrase-encrypted archive.
func (h *Handlers) AgentImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	passphrase := r.FormValue("passphrase")

	if passphrase == "" {
		http.Error(w, "passphrase is required", http.StatusBadRequest)
		return
	}

	// Limit upload to 500MB.
	r.Body = http.MaxBytesReader(w, r.Body, 500<<20)

	file, _, err := r.FormFile("agent_file")
	if err != nil {
		http.Error(w, "upload failed", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Save to temp file.
	tmpFile, err := os.CreateTemp("", "kyvik-import-upload-*.tar.gz")
	if err != nil {
		http.Error(w, "temp file failed", http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, file); err != nil {
		h.serverError(w, r, "saving upload", err)
		return
	}
	tmpFile.Close()

	agent, err := backup.ImportAgent(ctx, h.backupDeps, tmpFile.Name(), passphrase)
	if err != nil {
		slog.Error("agent import failed", "error", err)
		if isHTMX(r) {
			h.renderFragment(w, r, "backup-import-result", map[string]any{
				"Error": "Import failed. Check the passphrase and try again.",
			})
			return
		}
		http.Error(w, "import failed", http.StatusBadRequest)
		return
	}

	if isHTMX(r) {
		h.renderFragment(w, r, "backup-import-result", map[string]any{
			"Agent": agent,
		})
		return
	}

	http.Redirect(w, r, "/agents/"+agent.ID, http.StatusSeeOther)
}
