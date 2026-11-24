package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"bugpk-api/internal/pkg/masking"

	"go.uber.org/zap"
)

const (
	uploadCleanupGrace       = 24 * time.Hour
	uploadCleanupInitialWait = 10 * time.Minute
	uploadCleanupInterval    = 24 * time.Hour
	uploadCleanupBatch       = 1000
)

var generatedUploadName = regexp.MustCompile(`(?i)[a-f0-9]{32}\.(?:csv|gif|jpe?g|json|log|pdf|png|txt|webp|zip)`)

type uploadCleaner struct {
	cancel context.CancelFunc
	db     *sql.DB
	done   chan struct{}
	log    *zap.Logger
	once   sync.Once
	root   string
}

func startUploadCleaner(db *sql.DB, root string, log *zap.Logger) *uploadCleaner {
	if log == nil {
		log = zap.NewNop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cleaner := &uploadCleaner{cancel: cancel, db: db, done: make(chan struct{}), log: log, root: root}
	go cleaner.run(ctx)
	return cleaner
}

func (c *uploadCleaner) run(ctx context.Context) {
	defer close(c.done)
	timer := time.NewTimer(uploadCleanupInitialWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	for {
		cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		removed, err := c.cleanup(cleanupCtx)
		cancel()
		if err != nil {
			c.log.Warn("upload orphan cleanup failed", zap.String("error", masking.Error(err)))
		} else if removed > 0 {
			c.log.Info("upload orphan cleanup completed", zap.Int("removed_files", removed))
		}
		timer.Reset(uploadCleanupInterval)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (c *uploadCleaner) cleanup(ctx context.Context) (int, error) {
	references, err := loadUploadReferences(ctx, c.db)
	if err != nil {
		return 0, err
	}
	return cleanupOrphanUploads(c.root, references, time.Now().Add(-uploadCleanupGrace), uploadCleanupBatch)
}

func (c *uploadCleaner) Close(ctx context.Context) error {
	c.once.Do(c.cancel)
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func loadUploadReferences(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	references := make(map[string]struct{})
	queries := []string{
		"select coalesce(avatar_url,'') from bp_interface_api where avatar_url like '%/upload/interface-avatar/%'",
		"select coalesce(logo_url,'') from bp_site_config where logo_url like '%/upload/site-logo/%'",
		"select coalesce(loading_animation_url,'') from bp_site_config where loading_animation_url like '%/upload/loading-animation/%'",
		"select coalesce(content_html,'') from bp_notice where content_html like '%/upload/notice-image/%'",
		"select coalesce(cast(methods as char),'') from bp_donation_config where cast(methods as char) like '%/upload/donation-qr/%'",
		"select coalesce(stored_name,'') from bp_ticket_attachment",
	}
	for _, query := range queries {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				_ = rows.Close()
				return nil, err
			}
			collectUploadReferences(references, value)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return references, nil
}

func collectUploadReferences(references map[string]struct{}, value string) {
	for _, name := range generatedUploadName.FindAllString(value, -1) {
		references[strings.ToLower(name)] = struct{}{}
	}
}

func cleanupOrphanUploads(root string, references map[string]struct{}, cutoff time.Time, limit int) (int, error) {
	removed := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || removed >= limit {
			return nil
		}
		name := strings.ToLower(entry.Name())
		if generatedUploadName.FindString(name) != name {
			return nil
		}
		if _, keep := references[name]; keep {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		removed++
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return removed, err
}
