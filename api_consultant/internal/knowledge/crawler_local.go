package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const localFileSourceRoot = "local://files"

// CrawlLocalFiles reads markdown/text files from disk and ingests them through
// the same embedding and upserting pipeline as web-crawled pages. Content
// hashing ensures files are only re-embedded when their content changes.
func (c *Crawler) CrawlLocalFiles(ctx context.Context, tenantID string, filePaths []string) error {
	if strings.TrimSpace(tenantID) == "" {
		return errors.New("tenant id is required")
	}
	if len(filePaths) == 0 {
		return nil
	}

	for _, filePath := range filePaths {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			if c.logger != nil {
				c.logger.WithField("path", filePath).WithError(err).Warn("Failed to read local file, skipping")
			}
			continue
		}

		title, content := extractPlainContent(data, filePath)
		if title == "" {
			base := filepath.Base(filePath)
			title = strings.TrimSuffix(base, filepath.Ext(base))
			title = strings.NewReplacer("-", " ", "_", " ").Replace(title)
		}

		pageURL := "local://" + filePath
		result := FetchResult{
			Title:       title,
			Content:     content,
			ContentHash: contentHash(content),
			RawSize:     int64(len(data)),
		}

		var cached *PageCache
		if c.pageCache != nil {
			cached, _ = c.pageCache.Get(ctx, tenantID, pageURL)
		}

		status, err := c.finishPage(ctx, tenantID, localFileSourceRoot, pageURL, "local", result, cached)
		if err != nil {
			return err
		}
		if c.logger != nil && status == PageEmbedded {
			c.logger.WithField("path", filePath).WithField("title", title).Debug("Embedded local file")
		}
	}
	return nil
}
