package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Activity struct {
	Timestamp time.Time `json:"timestamp"`
	Harness   string    `json:"harness"`
	Namespace string    `json:"namespace"`
	SessionID string    `json:"session_id,omitempty"`
	Kind      EventKind `json:"kind"`
	Files     []string  `json:"files,omitempty"`
	Artifact  string    `json:"artifact,omitempty"`
}

type ActivitySink interface {
	Record(context.Context, Activity) error
}

type JSONLActivitySink struct {
	Root string

	mu sync.Mutex
}

func (s *JSONLActivitySink) Record(ctx context.Context, activity Activity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := s.root()
	if err != nil {
		return err
	}
	record, err := json.Marshal(activity)
	if err != nil {
		return fmt.Errorf("encode activity: %w", err)
	}
	record = append(record, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create activity directory: %w", err)
	}
	path := filepath.Join(root, activityFilename(activity.Namespace, activity.SessionID))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open activity log: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return fmt.Errorf("secure activity log: %w", err)
	}
	if _, err := file.Write(record); err != nil {
		file.Close()
		return fmt.Errorf("append activity log: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close activity log: %w", err)
	}
	return nil
}

func (s *JSONLActivitySink) root() (string, error) {
	if strings.TrimSpace(s.Root) != "" {
		return filepath.Clean(s.Root), nil
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	return filepath.Join(cacheRoot, "dualmem", "activity"), nil
}

func activityFilename(namespace, sessionID string) string {
	return safeFilenamePart(namespace, "namespace") + "--" + safeFilenamePart(sessionID, "no-session") + ".jsonl"
}

func safeFilenamePart(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	var safe strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '-', char == '_':
			safe.WriteRune(char)
		default:
			safe.WriteByte('_')
		}
		if safe.Len() >= 64 {
			break
		}
	}
	prefix := strings.Trim(safe.String(), "_-")
	if prefix == "" {
		prefix = fallback
	}
	if prefix == value && len(value) <= 64 {
		return prefix
	}
	hash := sha256.Sum256([]byte(value))
	return prefix + "-" + hex.EncodeToString(hash[:6])
}
