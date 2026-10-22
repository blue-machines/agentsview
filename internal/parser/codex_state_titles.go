package parser

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const codexStateDBFilename = "state_5.sqlite"

type codexStateTitleEntry struct {
	titles  map[string]string
	loadErr error
	mtime   int64
	loaded  time.Time
}

var (
	codexStateTitleMu    sync.Mutex
	codexStateTitleCache = make(map[string]*codexStateTitleEntry)
)

// evictCodexStateTitleCache removes a cached state_5.sqlite entry so a
// changed database is re-read on the next access.
func evictCodexStateTitleCache(dbPath string) {
	codexStateTitleMu.Lock()
	delete(codexStateTitleCache, dbPath)
	codexStateTitleMu.Unlock()
}

func codexStateTitlePaths(metadataDirs []string) []string {
	paths := make([]string, 0, len(metadataDirs))
	for _, dir := range metadataDirs {
		paths = append(paths, filepath.Join(dir, codexStateDBFilename))
	}
	return paths
}

// codexReadThreadTitleFromState reads a single thread's title from
// state_5.sqlite. It opens the database read-only and tolerates the file
// being missing, locked, or using an unrecognised schema.
func codexReadThreadTitleFromState(dbPaths []string, threadID string) (string, bool) {
	if threadID == "" {
		return "", false
	}
	for _, dbPath := range dbPaths {
		title, ok := codexLookupStateTitleCached(dbPath, threadID)
		if ok {
			return title, true
		}
	}
	return "", false
}

func codexLookupStateTitleCached(dbPath, threadID string) (string, bool) {
	codexStateTitleMu.Lock()
	defer codexStateTitleMu.Unlock()

	info, err := os.Stat(dbPath)
	if err != nil {
		return "", false
	}
	mtime := info.ModTime().UnixNano()

	entry := codexStateTitleCache[dbPath]
	if entry != nil && entry.mtime == mtime &&
		time.Since(entry.loaded) < 30*time.Second {
		if entry.loadErr != nil {
			return "", false
		}
		title, ok := entry.titles[threadID]
		return strings.TrimSpace(title), ok && strings.TrimSpace(title) != ""
	}

	titles, loadErr := codexLoadStateTitles(dbPath)
	codexStateTitleCache[dbPath] = &codexStateTitleEntry{
		titles:  titles,
		loadErr: loadErr,
		mtime:   mtime,
		loaded:  time.Now(),
	}
	if loadErr != nil {
		return "", false
	}
	title, ok := titles[threadID]
	return strings.TrimSpace(title), ok && strings.TrimSpace(title) != ""
}

func codexLoadStateTitles(dbPath string) (map[string]string, error) {
	conn, err := sql.Open("sqlite3", dbPath+"?mode=ro&_busy_timeout=1000&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if !codexStateHasThreadsTable(conn) {
		return nil, errors.New("no threads table")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	rows, err := conn.QueryContext(ctx,
		`SELECT id, title FROM threads WHERE title IS NOT NULL AND title != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	titles := make(map[string]string)
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			continue
		}
		titles[id] = title
	}
	return titles, rows.Err()
}

func codexStateHasThreadsTable(conn *sql.DB) bool {
	var n int
	err := conn.QueryRow(
		`SELECT 1 FROM sqlite_master WHERE type='table' AND name='threads'`,
	).Scan(&n)
	return err == nil
}
