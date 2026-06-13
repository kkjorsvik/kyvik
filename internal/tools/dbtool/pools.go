package dbtool

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"
)

// driverNames maps our config driver names to database/sql driver names.
var driverNames = map[string]string{
	"postgres":  "pgx",
	"mysql":     "mysql",
	"sqlite":    "sqlite",
	"sqlserver": "sqlserver",
	"mssql":     "sqlserver",
}

// PoolManager manages database connection pools keyed by agent+connection name.
type PoolManager struct {
	mu    sync.Mutex
	pools map[string]*sql.DB
}

// NewPoolManager creates a new pool manager.
func NewPoolManager() *PoolManager {
	return &PoolManager{pools: make(map[string]*sql.DB)}
}

// GetOrCreate returns an existing pool or creates a new one.
func (pm *PoolManager) GetOrCreate(key, driver, dsn string) (*sql.DB, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if db, ok := pm.pools[key]; ok {
		return db, nil
	}

	sqlDriver, ok := driverNames[driver]
	if !ok {
		return nil, fmt.Errorf("unsupported database driver: %s (supported: postgres, mysql, sqlite, sqlserver)", driver)
	}

	db, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)

	pm.pools[key] = db
	return db, nil
}

// Close closes all pools.
func (pm *PoolManager) Close() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for k, db := range pm.pools {
		db.Close()
		delete(pm.pools, k)
	}
}
