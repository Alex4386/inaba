// Package db opens the SQLite files both binaries keep their state in.
package db

import (
	"os"
	"path/filepath"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open opens or creates the database at path and brings its tables up to the models.
// Several processes may open the same file: the server and the CLI editing it.
func Open(path string, models ...any) (*gorm.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// WAL and a busy timeout let a CLI command write while the server is running.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	// Callers report their own errors; a missing row is not worth a log line.
	g, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	// Secrets' hashes live here; keep the file private.
	_ = os.Chmod(path, 0o600)
	return g, g.AutoMigrate(models...)
}

// Size is what the database at path takes on disk: its file and its write-ahead log.
func Size(path string) uint64 {
	var n uint64
	for _, p := range []string{path, path + "-wal"} {
		if st, err := os.Stat(p); err == nil {
			n += uint64(st.Size())
		}
	}
	return n
}

// Compact gives the space deleted rows leave in the file back to the file system.
// It rewrites the whole file, so it takes a while on a large one.
func Compact(g *gorm.DB) error {
	if err := g.Exec("VACUUM").Error; err != nil {
		return err
	}
	// The rewrite went through the log; fold it into the file and empty it.
	return g.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error
}
