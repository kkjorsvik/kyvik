package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/robfig/cron/v3"

	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/notifications"
)

// StateStore is the narrow interface used to persist backup results.
type StateStore interface {
	GetSystemState(ctx context.Context, key string) (string, error)
	SetSystemState(ctx context.Context, key, value string) error
}

// BackupFileInfo describes a single backup file on disk.
type BackupFileInfo struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	SizeHuman string    `json:"size_human"`
	ModTime   time.Time `json:"mod_time"`
}

// Manager manages scheduled backups, following the same pattern as
// the retention.Pruner.
type Manager struct {
	db         *sql.DB
	dbPath     string
	dataDir    string
	stateStore StateStore
	notifier   notifications.Notifier
	config     config.BackupConfig
	cron       *cron.Cron

	mu         sync.RWMutex
	lastResult *BackupResult
}

// NewManager creates a new backup Manager.
func NewManager(db *sql.DB, dbPath, dataDir string, ss StateStore, cfg config.BackupConfig) *Manager {
	return &Manager{
		db:         db,
		dbPath:     dbPath,
		dataDir:    dataDir,
		stateStore: ss,
		config:     cfg,
		cron:       cron.New(),
	}
}

// SetNotifier configures operator notifications for backup results.
func (m *Manager) SetNotifier(n notifications.Notifier) {
	m.notifier = n
}

// Start registers the cron job and begins the scheduler.
func (m *Manager) Start() {
	if m.config.Enabled != nil && !*m.config.Enabled {
		slog.Info("backup manager disabled")
		return
	}

	_, err := m.cron.AddFunc(m.config.Schedule, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		result := m.RunNow(ctx)
		slog.Info("backup completed",
			"path", result.Path,
			"size", result.SizeHuman,
			"duration", result.Duration,
			"error", result.Error,
		)
	})
	if err != nil {
		slog.Error("backup manager: invalid cron schedule", "schedule", m.config.Schedule, "error", err)
		return
	}

	m.cron.Start()
	slog.Info("backup manager started", "schedule", m.config.Schedule, "retention", m.config.Retention)
}

// Stop halts the cron scheduler.
func (m *Manager) Stop() {
	if m.cron != nil {
		m.cron.Stop()
	}
}

// RunNow executes a backup immediately and returns the result.
func (m *Manager) RunNow(ctx context.Context) BackupResult {
	result, err := CreateBackup(ctx, m.db, m.dbPath, m.config.Path)
	if err != nil {
		errResult := BackupResult{
			Timestamp: time.Now(),
			Error:     err.Error(),
			Duration:  "0ms",
		}
		m.persistResult(ctx, errResult)

		m.mu.Lock()
		m.lastResult = &errResult
		m.mu.Unlock()

		if m.notifier != nil {
			_ = m.notifier.Send(ctx, notifications.Event{
				Type:      "backup",
				Severity:  "critical",
				Title:     "Backup failed",
				Detail:    err.Error(),
				Timestamp: time.Now(),
			})
		}
		return errResult
	}

	// Prune old backups.
	removed, pruneErr := PruneBackups(m.config.Path, m.config.Retention)
	if pruneErr != nil {
		slog.Warn("backup retention prune failed", "error", pruneErr)
	} else if removed > 0 {
		slog.Info("old backups pruned", "removed", removed)
	}

	m.persistResult(ctx, *result)

	m.mu.Lock()
	m.lastResult = result
	m.mu.Unlock()

	if m.notifier != nil {
		_ = m.notifier.Send(ctx, notifications.Event{
			Type:     "backup",
			Severity: "info",
			Title:    "Backup completed",
			Detail: fmt.Sprintf("Size: %s, Agents: %d, Duration: %s",
				result.SizeHuman, result.AgentCount, result.Duration),
			Timestamp: time.Now(),
		})
	}

	return *result
}

// LastResult returns the most recent backup result from memory.
func (m *Manager) LastResult() *BackupResult {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastResult
}

// LoadLastResult loads the last backup result from the database.
func (m *Manager) LoadLastResult(ctx context.Context) (*BackupResult, error) {
	raw, err := m.stateStore.GetSystemState(ctx, "last_backup_result")
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var result BackupResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.lastResult = &result
	m.mu.Unlock()

	return &result, nil
}

// Config returns the current backup configuration.
func (m *Manager) Config() config.BackupConfig {
	return m.config
}

// ListBackups returns info about all backup files in the backup directory.
func (m *Manager) ListBackups() ([]BackupFileInfo, error) {
	entries, err := os.ReadDir(m.config.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []BackupFileInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, BackupFileInfo{
			Name:      e.Name(),
			Size:      info.Size(),
			SizeHuman: humanize.Bytes(uint64(info.Size())),
			ModTime:   info.ModTime(),
		})
	}

	// Sort newest first.
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})

	return files, nil
}

// DeleteBackup removes a single backup file by name. The filename is
// validated to prevent path traversal.
func (m *Manager) DeleteBackup(filename string) error {
	// Validate: must be a base filename, no directory components.
	clean := filepath.Base(filename)
	if clean != filename || strings.Contains(filename, "..") {
		return fmt.Errorf("invalid backup filename")
	}
	if !strings.HasSuffix(clean, ".tar.gz") {
		return fmt.Errorf("invalid backup filename")
	}

	path := filepath.Join(m.config.Path, clean)

	// Verify the file is within the backup directory.
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	absDir, err := filepath.Abs(m.config.Path)
	if err != nil {
		return fmt.Errorf("resolve backup dir: %w", err)
	}
	if !strings.HasPrefix(absPath, absDir+string(os.PathSeparator)) {
		return fmt.Errorf("invalid backup filename")
	}

	return os.Remove(path)
}

// persistResult saves the backup result to the system_state table.
func (m *Manager) persistResult(ctx context.Context, result BackupResult) {
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = m.stateStore.SetSystemState(ctx, "last_backup_result", string(data))
	_ = m.stateStore.SetSystemState(ctx, "last_backup_time", result.Timestamp.UTC().Format(time.RFC3339))
}
