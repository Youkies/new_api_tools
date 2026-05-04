package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/new-api-tools/backend/internal/config"
)

var (
	logMatchUploadMu       sync.Mutex
	logMatchUploadUnsafeRE = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
)

// LogMatchUploadMetadata describes one CSV uploaded by the userscript or UI.
type LogMatchUploadMetadata struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	SourceURL  string `json:"source_url,omitempty"`
	SourceName string `json:"source_name,omitempty"`
	StartTime  int64  `json:"start_time,omitempty"`
	EndTime    int64  `json:"end_time,omitempty"`
	Rows       int    `json:"rows"`
	Size       int64  `json:"size"`
	UploadedAt int64  `json:"uploaded_at"`
	FileName   string `json:"file_name"`
}

// LogMatchUploadStore stores uploaded upstream CSV files under DATA_DIR.
type LogMatchUploadStore struct {
	dir      string
	manifest string
}

// NewLogMatchUploadStore creates a store using the configured DATA_DIR.
func NewLogMatchUploadStore() *LogMatchUploadStore {
	dataDir := strings.TrimSpace(config.Get().DataDir)
	if dataDir == "" {
		dataDir = "./data"
	}
	dir := filepath.Join(dataDir, "log_match_uploads")
	return &LogMatchUploadStore{
		dir:      dir,
		manifest: filepath.Join(dir, "manifest.json"),
	}
}

// Save stores uploaded CSV files and returns their metadata.
func (s *LogMatchUploadStore) Save(files []LogMatchUploadedFile, sourceURL, sourceName string, startTime, endTime int64) ([]LogMatchUploadMetadata, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("at least one CSV file is required")
	}

	logMatchUploadMu.Lock()
	defer logMatchUploadMu.Unlock()

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, err
	}
	items, err := s.loadLocked()
	if err != nil {
		return nil, err
	}

	config := DefaultLogMatchConfig()
	saved := make([]LogMatchUploadMetadata, 0, len(files))
	for _, file := range files {
		if len(file.Data) == 0 {
			return nil, fmt.Errorf("%s is empty", file.Name)
		}
		host := detectLogMatchHost(file.Name, config)
		rows, err := parseLogMatchCSV(file.Name, host, file.Data, config)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file.Name, err)
		}

		id := newLogMatchUploadID()
		name := strings.TrimSpace(file.Name)
		if name == "" {
			name = fmt.Sprintf("newapi_logs_%s_%s.csv", safeLogMatchUploadName(host), time.Now().Format("20060102_150405"))
		}
		storedFileName := id + "_" + safeLogMatchUploadName(name)
		if !strings.HasSuffix(strings.ToLower(storedFileName), ".csv") {
			storedFileName += ".csv"
		}
		if err := os.WriteFile(filepath.Join(s.dir, storedFileName), file.Data, 0o600); err != nil {
			return nil, err
		}

		meta := LogMatchUploadMetadata{
			ID:         id,
			Name:       name,
			Host:       host,
			SourceURL:  strings.TrimSpace(sourceURL),
			SourceName: strings.TrimSpace(sourceName),
			StartTime:  startTime,
			EndTime:    endTime,
			Rows:       len(rows),
			Size:       int64(len(file.Data)),
			UploadedAt: time.Now().Unix(),
			FileName:   storedFileName,
		}
		items = append(items, meta)
		saved = append(saved, meta)
	}

	if err := s.saveLocked(items); err != nil {
		return nil, err
	}
	return saved, nil
}

// List returns uploaded CSV metadata, newest first.
func (s *LogMatchUploadStore) List() ([]LogMatchUploadMetadata, error) {
	logMatchUploadMu.Lock()
	defer logMatchUploadMu.Unlock()

	items, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UploadedAt > items[j].UploadedAt })
	return items, nil
}

// Load returns uploaded CSV contents by ID.
func (s *LogMatchUploadStore) Load(ids []string) ([]LogMatchUploadedFile, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	logMatchUploadMu.Lock()
	defer logMatchUploadMu.Unlock()

	items, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	byID := map[string]LogMatchUploadMetadata{}
	for _, item := range items {
		byID[item.ID] = item
	}

	files := make([]LogMatchUploadedFile, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		item, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("uploaded file %s not found", id)
		}
		path := filepath.Join(s.dir, item.FileName)
		if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(s.dir)) {
			return nil, fmt.Errorf("invalid uploaded file path")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files = append(files, LogMatchUploadedFile{Name: item.Name, Data: data})
	}
	return files, nil
}

// Delete removes one uploaded CSV by ID.
func (s *LogMatchUploadStore) Delete(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("id is required")
	}

	logMatchUploadMu.Lock()
	defer logMatchUploadMu.Unlock()

	items, err := s.loadLocked()
	if err != nil {
		return err
	}
	next := make([]LogMatchUploadMetadata, 0, len(items))
	deleted := false
	for _, item := range items {
		if item.ID == id {
			deleted = true
			_ = os.Remove(filepath.Join(s.dir, item.FileName))
			continue
		}
		next = append(next, item)
	}
	if !deleted {
		return fmt.Errorf("uploaded file %s not found", id)
	}
	return s.saveLocked(next)
}

func (s *LogMatchUploadStore) loadLocked() ([]LogMatchUploadMetadata, error) {
	data, err := os.ReadFile(s.manifest)
	if err != nil {
		if os.IsNotExist(err) {
			return []LogMatchUploadMetadata{}, nil
		}
		return nil, err
	}
	var items []LogMatchUploadMetadata
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *LogMatchUploadStore) saveLocked(items []LogMatchUploadMetadata) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.manifest, data, 0o600)
}

func newLogMatchUploadID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%d_%s", time.Now().Unix(), hex.EncodeToString(buf))
}

func safeLogMatchUploadName(value string) string {
	text := strings.TrimSpace(value)
	text = strings.ReplaceAll(text, "\\", "_")
	text = strings.ReplaceAll(text, "/", "_")
	text = logMatchUploadUnsafeRE.ReplaceAllString(text, "_")
	text = strings.Trim(text, "._-")
	if text == "" {
		return "upload.csv"
	}
	if len(text) > 160 {
		ext := filepath.Ext(text)
		base := strings.TrimSuffix(text, ext)
		if len(base) > 140 {
			base = base[:140]
		}
		text = base + ext
	}
	return text
}
