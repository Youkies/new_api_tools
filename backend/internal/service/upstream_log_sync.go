package service

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/new-api-tools/backend/internal/database"
	"github.com/new-api-tools/backend/internal/logger"
)

const (
	upstreamSyncConfigID = 1
	upstreamSyncTimeout  = 60 * time.Second
)

// UpstreamLogSyncConfig stores upstream NewAPI log import settings.
type UpstreamLogSyncConfig struct {
	Enabled               bool   `json:"enabled"`
	BaseURL               string `json:"base_url"`
	Endpoint              string `json:"endpoint"`
	AuthToken             string `json:"auth_token,omitempty"`
	AuthTokenSet          bool   `json:"auth_token_set"`
	ClearAuthToken        bool   `json:"clear_auth_token,omitempty"`
	UserID                string `json:"user_id"`
	PageSize              int    `json:"page_size"`
	RequestDelayMS        int    `json:"request_delay_ms"`
	IntervalMinutes       int    `json:"interval_minutes"`
	LookbackMinutes       int    `json:"lookback_minutes"`
	OverlapMinutes        int    `json:"overlap_minutes"`
	MatchToleranceSeconds int    `json:"match_tolerance_seconds"`
	LogType               int    `json:"log_type"`
	MaxPagesPerRun        int    `json:"max_pages_per_run"`
	LastSyncAt            int64  `json:"last_sync_at"`
	LastSuccessAt         int64  `json:"last_success_at"`
	LastError             string `json:"last_error"`
	TotalImported         int64  `json:"total_imported"`
	UpdatedAt             int64  `json:"updated_at"`
}

// UpstreamLogSyncRunOptions overrides config for a single manual/scheduled run.
type UpstreamLogSyncRunOptions struct {
	StartTime int64 `json:"start_time"`
	EndTime   int64 `json:"end_time"`
	LogType   int   `json:"type"`
}

// UpstreamLogSyncService imports logs from an upstream NewAPI instance.
type UpstreamLogSyncService struct {
	db *database.Manager
}

type upstreamLogMatchCandidate struct {
	ID               int64
	CreatedAt        int64
	RequestID        string
	PromptTokens     int64
	CompletionTokens int64
}

type upstreamLogMatchUpdate struct {
	UpstreamID int64
	LocalID    int64
	Method     string
	Score      int64
}

// NewUpstreamLogSyncService creates an upstream log sync service.
func NewUpstreamLogSyncService() *UpstreamLogSyncService {
	return &UpstreamLogSyncService{db: database.Get()}
}

// StartBackgroundUpstreamLogSync runs scheduled upstream imports when enabled.
func StartBackgroundUpstreamLogSync(stop <-chan struct{}) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.L.Error(fmt.Sprintf("上游日志同步后台任务 panic: %v", r))
			}
		}()

		svc := NewUpstreamLogSyncService()
		if err := svc.EnsureTables(); err != nil {
			logger.L.Warn("上游日志同步初始化失败: "+err.Error(), logger.CatDatabase)
		}

		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				cfg, err := svc.GetConfig(false)
				if err != nil {
					logger.L.Warn("读取上游日志同步配置失败: "+err.Error(), logger.CatDatabase)
					continue
				}
				if !cfg.Enabled || cfg.IntervalMinutes <= 0 || cfg.BaseURL == "" {
					continue
				}
				now := time.Now().Unix()
				if cfg.LastSyncAt > 0 && now-cfg.LastSyncAt < int64(cfg.IntervalMinutes*60) {
					continue
				}
				if _, err := svc.RunSync(UpstreamLogSyncRunOptions{}); err != nil {
					logger.L.Warn("上游日志定时同步失败: "+err.Error(), logger.CatBusiness)
				}
			case <-stop:
				logger.L.System("上游日志同步后台任务已停止")
				return
			}
		}
	}()
}

// EnsureTables creates storage for upstream sync config and imported logs.
func (s *UpstreamLogSyncService) EnsureTables() error {
	if s.db.IsPG {
		if err := s.db.ExecuteDDL(`
			CREATE TABLE IF NOT EXISTS api_tools_upstream_log_sync_config (
				id INTEGER PRIMARY KEY,
				enabled BOOLEAN NOT NULL DEFAULT FALSE,
				base_url TEXT NOT NULL DEFAULT '',
				endpoint TEXT NOT NULL DEFAULT 'auto',
				auth_token TEXT NOT NULL DEFAULT '',
				user_id TEXT NOT NULL DEFAULT '',
				page_size INTEGER NOT NULL DEFAULT 100,
				request_delay_ms INTEGER NOT NULL DEFAULT 80,
				interval_minutes INTEGER NOT NULL DEFAULT 0,
				lookback_minutes INTEGER NOT NULL DEFAULT 60,
				overlap_minutes INTEGER NOT NULL DEFAULT 10,
				match_tolerance_seconds INTEGER NOT NULL DEFAULT 60,
				log_type INTEGER NOT NULL DEFAULT 2,
				max_pages_per_run INTEGER NOT NULL DEFAULT 1000,
				last_sync_at BIGINT NOT NULL DEFAULT 0,
				last_success_at BIGINT NOT NULL DEFAULT 0,
				last_error TEXT NOT NULL DEFAULT '',
				total_imported BIGINT NOT NULL DEFAULT 0,
				updated_at BIGINT NOT NULL DEFAULT 0
			)`); err != nil {
			return err
		}
		if err := s.db.ExecuteDDL(`
			CREATE TABLE IF NOT EXISTS api_tools_upstream_logs (
				id BIGSERIAL PRIMARY KEY,
				source_key TEXT NOT NULL UNIQUE,
				source_log_id TEXT NOT NULL DEFAULT '',
				created_at BIGINT NOT NULL DEFAULT 0,
				type INTEGER NOT NULL DEFAULT 0,
				user_id BIGINT NOT NULL DEFAULT 0,
				username TEXT NOT NULL DEFAULT '',
				token_id BIGINT NOT NULL DEFAULT 0,
				token_name TEXT NOT NULL DEFAULT '',
				group_name TEXT NOT NULL DEFAULT '',
				model_name TEXT NOT NULL DEFAULT '',
				channel_id BIGINT NOT NULL DEFAULT 0,
				channel_name TEXT NOT NULL DEFAULT '',
				prompt_tokens BIGINT NOT NULL DEFAULT 0,
				completion_tokens BIGINT NOT NULL DEFAULT 0,
				quota BIGINT NOT NULL DEFAULT 0,
				use_time DOUBLE PRECISION NOT NULL DEFAULT 0,
				is_stream BOOLEAN NOT NULL DEFAULT FALSE,
				request_id TEXT NOT NULL DEFAULT '',
				ip TEXT NOT NULL DEFAULT '',
				content TEXT NOT NULL DEFAULT '',
				other TEXT NOT NULL DEFAULT '',
				raw_json TEXT NOT NULL DEFAULT '',
				imported_at BIGINT NOT NULL DEFAULT 0,
				local_log_id BIGINT NOT NULL DEFAULT 0,
				match_method TEXT NOT NULL DEFAULT '',
				match_score BIGINT NOT NULL DEFAULT 0,
				matched_at BIGINT NOT NULL DEFAULT 0
			)`); err != nil {
			return err
		}
		for _, ddl := range []string{
			`CREATE INDEX IF NOT EXISTS idx_api_tools_upstream_logs_created_type ON api_tools_upstream_logs (created_at, type)`,
			`CREATE INDEX IF NOT EXISTS idx_api_tools_upstream_logs_request ON api_tools_upstream_logs (request_id)`,
			`CREATE INDEX IF NOT EXISTS idx_api_tools_upstream_logs_local_log ON api_tools_upstream_logs (local_log_id)`,
			`CREATE INDEX IF NOT EXISTS idx_api_tools_upstream_logs_tokens_time ON api_tools_upstream_logs (prompt_tokens, completion_tokens, created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_api_tools_upstream_logs_channel_model ON api_tools_upstream_logs (channel_id, model_name)`,
		} {
			if err := s.db.ExecuteDDL(ddl); err != nil {
				return err
			}
		}
		return s.ensureSchemaColumns()
	}

	if err := s.db.ExecuteDDL(`
		CREATE TABLE IF NOT EXISTS api_tools_upstream_log_sync_config (
			id INT PRIMARY KEY,
			enabled TINYINT(1) NOT NULL DEFAULT 0,
			base_url TEXT NOT NULL,
			endpoint VARCHAR(64) NOT NULL DEFAULT 'auto',
			auth_token TEXT NOT NULL,
			user_id VARCHAR(64) NOT NULL DEFAULT '',
			page_size INT NOT NULL DEFAULT 100,
			request_delay_ms INT NOT NULL DEFAULT 80,
			interval_minutes INT NOT NULL DEFAULT 0,
			lookback_minutes INT NOT NULL DEFAULT 60,
			overlap_minutes INT NOT NULL DEFAULT 10,
			match_tolerance_seconds INT NOT NULL DEFAULT 60,
			log_type INT NOT NULL DEFAULT 2,
			max_pages_per_run INT NOT NULL DEFAULT 1000,
			last_sync_at BIGINT NOT NULL DEFAULT 0,
			last_success_at BIGINT NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL,
			total_imported BIGINT NOT NULL DEFAULT 0,
			updated_at BIGINT NOT NULL DEFAULT 0
		)`); err != nil {
		return err
	}
	if err := s.db.ExecuteDDL(`
		CREATE TABLE IF NOT EXISTS api_tools_upstream_logs (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			source_key VARCHAR(191) NOT NULL,
			source_log_id VARCHAR(64) NOT NULL DEFAULT '',
			created_at BIGINT NOT NULL DEFAULT 0,
			type INT NOT NULL DEFAULT 0,
			user_id BIGINT NOT NULL DEFAULT 0,
			username VARCHAR(191) NOT NULL DEFAULT '',
			token_id BIGINT NOT NULL DEFAULT 0,
			token_name VARCHAR(191) NOT NULL DEFAULT '',
			group_name VARCHAR(191) NOT NULL DEFAULT '',
			model_name VARCHAR(191) NOT NULL DEFAULT '',
			channel_id BIGINT NOT NULL DEFAULT 0,
			channel_name VARCHAR(191) NOT NULL DEFAULT '',
			prompt_tokens BIGINT NOT NULL DEFAULT 0,
			completion_tokens BIGINT NOT NULL DEFAULT 0,
			quota BIGINT NOT NULL DEFAULT 0,
			use_time DOUBLE NOT NULL DEFAULT 0,
			is_stream TINYINT(1) NOT NULL DEFAULT 0,
			request_id VARCHAR(191) NOT NULL DEFAULT '',
			ip VARCHAR(64) NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			other TEXT NOT NULL,
			raw_json LONGTEXT NOT NULL,
			imported_at BIGINT NOT NULL DEFAULT 0,
			local_log_id BIGINT NOT NULL DEFAULT 0,
			match_method VARCHAR(32) NOT NULL DEFAULT '',
			match_score BIGINT NOT NULL DEFAULT 0,
			matched_at BIGINT NOT NULL DEFAULT 0,
			UNIQUE KEY uniq_api_tools_upstream_logs_source (source_key),
			KEY idx_api_tools_upstream_logs_created_type (created_at, type),
			KEY idx_api_tools_upstream_logs_request (request_id),
			KEY idx_api_tools_upstream_logs_local_log (local_log_id),
			KEY idx_api_tools_upstream_logs_tokens_time (prompt_tokens, completion_tokens, created_at),
			KEY idx_api_tools_upstream_logs_channel_model (channel_id, model_name)
		)`); err != nil {
		return err
	}
	return s.ensureSchemaColumns()
}

func (s *UpstreamLogSyncService) ensureSchemaColumns() error {
	columns := []struct {
		table string
		name  string
		pgDDL string
		myDDL string
	}{
		{
			table: "api_tools_upstream_log_sync_config",
			name:  "match_tolerance_seconds",
			pgDDL: "ALTER TABLE api_tools_upstream_log_sync_config ADD COLUMN match_tolerance_seconds INTEGER NOT NULL DEFAULT 60",
			myDDL: "ALTER TABLE api_tools_upstream_log_sync_config ADD COLUMN match_tolerance_seconds INT NOT NULL DEFAULT 60",
		},
		{
			table: "api_tools_upstream_logs",
			name:  "local_log_id",
			pgDDL: "ALTER TABLE api_tools_upstream_logs ADD COLUMN local_log_id BIGINT NOT NULL DEFAULT 0",
			myDDL: "ALTER TABLE api_tools_upstream_logs ADD COLUMN local_log_id BIGINT NOT NULL DEFAULT 0",
		},
		{
			table: "api_tools_upstream_logs",
			name:  "match_method",
			pgDDL: "ALTER TABLE api_tools_upstream_logs ADD COLUMN match_method TEXT NOT NULL DEFAULT ''",
			myDDL: "ALTER TABLE api_tools_upstream_logs ADD COLUMN match_method VARCHAR(32) NOT NULL DEFAULT ''",
		},
		{
			table: "api_tools_upstream_logs",
			name:  "match_score",
			pgDDL: "ALTER TABLE api_tools_upstream_logs ADD COLUMN match_score BIGINT NOT NULL DEFAULT 0",
			myDDL: "ALTER TABLE api_tools_upstream_logs ADD COLUMN match_score BIGINT NOT NULL DEFAULT 0",
		},
		{
			table: "api_tools_upstream_logs",
			name:  "matched_at",
			pgDDL: "ALTER TABLE api_tools_upstream_logs ADD COLUMN matched_at BIGINT NOT NULL DEFAULT 0",
			myDDL: "ALTER TABLE api_tools_upstream_logs ADD COLUMN matched_at BIGINT NOT NULL DEFAULT 0",
		},
	}
	for _, column := range columns {
		if s.db.ColumnExists(column.table, column.name) {
			continue
		}
		ddl := column.myDDL
		if s.db.IsPG {
			ddl = column.pgDDL
		}
		if err := s.db.ExecuteDDL(ddl); err != nil {
			return err
		}
	}

	if s.db.IsPG {
		for _, ddl := range []string{
			`CREATE INDEX IF NOT EXISTS idx_api_tools_upstream_logs_local_log ON api_tools_upstream_logs (local_log_id)`,
			`CREATE INDEX IF NOT EXISTS idx_api_tools_upstream_logs_tokens_time ON api_tools_upstream_logs (prompt_tokens, completion_tokens, created_at)`,
		} {
			if err := s.db.ExecuteDDL(ddl); err != nil {
				return err
			}
		}
		return nil
	}

	for _, idx := range []struct {
		name string
		ddl  string
	}{
		{"idx_api_tools_upstream_logs_local_log", "CREATE INDEX idx_api_tools_upstream_logs_local_log ON api_tools_upstream_logs (local_log_id)"},
		{"idx_api_tools_upstream_logs_tokens_time", "CREATE INDEX idx_api_tools_upstream_logs_tokens_time ON api_tools_upstream_logs (prompt_tokens, completion_tokens, created_at)"},
	} {
		if s.indexExists("api_tools_upstream_logs", idx.name) {
			continue
		}
		if err := s.db.ExecuteDDL(idx.ddl); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return err
		}
	}
	return nil
}

func (s *UpstreamLogSyncService) indexExists(tableName, indexName string) bool {
	var query string
	args := []interface{}{tableName, indexName}
	if s.db.IsPG {
		query = `SELECT 1 FROM pg_indexes WHERE tablename = ? AND indexname = ? LIMIT 1`
	} else {
		query = `SELECT 1 FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ? LIMIT 1`
	}
	row, err := s.db.QueryOne(s.db.RebindQuery(query), args...)
	return err == nil && row != nil
}

func defaultUpstreamLogSyncConfig() UpstreamLogSyncConfig {
	return UpstreamLogSyncConfig{
		Endpoint:              "auto",
		PageSize:              100,
		RequestDelayMS:        80,
		IntervalMinutes:       0,
		LookbackMinutes:       60,
		OverlapMinutes:        10,
		MatchToleranceSeconds: 60,
		LogType:               2,
		MaxPagesPerRun:        1000,
	}
}

// GetConfig returns upstream log sync config. Secrets are masked unless includeSecret is true.
func (s *UpstreamLogSyncService) GetConfig(includeSecret bool) (UpstreamLogSyncConfig, error) {
	if err := s.EnsureTables(); err != nil {
		return UpstreamLogSyncConfig{}, err
	}

	row, err := s.db.QueryOne(s.db.RebindQuery(`
		SELECT enabled, base_url, endpoint, auth_token, user_id, page_size, request_delay_ms,
			interval_minutes, lookback_minutes, overlap_minutes, match_tolerance_seconds, log_type, max_pages_per_run,
			last_sync_at, last_success_at, last_error, total_imported, updated_at
		FROM api_tools_upstream_log_sync_config
		WHERE id = ?`), upstreamSyncConfigID)
	if err != nil {
		return UpstreamLogSyncConfig{}, err
	}
	cfg := defaultUpstreamLogSyncConfig()
	if row != nil {
		cfg.Enabled = toBool(row["enabled"])
		cfg.BaseURL = toString(row["base_url"])
		cfg.Endpoint = toString(row["endpoint"])
		cfg.AuthToken = toString(row["auth_token"])
		cfg.UserID = toString(row["user_id"])
		cfg.PageSize = int(toInt64(row["page_size"]))
		cfg.RequestDelayMS = int(toInt64(row["request_delay_ms"]))
		cfg.IntervalMinutes = int(toInt64(row["interval_minutes"]))
		cfg.LookbackMinutes = int(toInt64(row["lookback_minutes"]))
		cfg.OverlapMinutes = int(toInt64(row["overlap_minutes"]))
		cfg.MatchToleranceSeconds = int(toInt64(row["match_tolerance_seconds"]))
		cfg.LogType = int(toInt64(row["log_type"]))
		cfg.MaxPagesPerRun = int(toInt64(row["max_pages_per_run"]))
		cfg.LastSyncAt = toInt64(row["last_sync_at"])
		cfg.LastSuccessAt = toInt64(row["last_success_at"])
		cfg.LastError = toString(row["last_error"])
		cfg.TotalImported = toInt64(row["total_imported"])
		cfg.UpdatedAt = toInt64(row["updated_at"])
	}
	cfg = normalizeUpstreamSyncConfig(cfg)
	cfg.AuthTokenSet = strings.TrimSpace(cfg.AuthToken) != ""
	if !includeSecret {
		cfg.AuthToken = ""
	}
	return cfg, nil
}

// SaveConfig stores upstream log sync config.
func (s *UpstreamLogSyncService) SaveConfig(next UpstreamLogSyncConfig) (UpstreamLogSyncConfig, error) {
	if err := s.EnsureTables(); err != nil {
		return UpstreamLogSyncConfig{}, err
	}

	current, _ := s.GetConfig(true)
	if strings.TrimSpace(next.AuthToken) == "" && !next.ClearAuthToken {
		next.AuthToken = current.AuthToken
	}
	if next.ClearAuthToken {
		next.AuthToken = ""
	}
	next.LastSyncAt = current.LastSyncAt
	next.LastSuccessAt = current.LastSuccessAt
	next.LastError = current.LastError
	next.TotalImported = current.TotalImported
	next.UpdatedAt = time.Now().Unix()
	next = normalizeUpstreamSyncConfig(next)

	query := `
		INSERT INTO api_tools_upstream_log_sync_config
			(id, enabled, base_url, endpoint, auth_token, user_id, page_size, request_delay_ms,
			 interval_minutes, lookback_minutes, overlap_minutes, match_tolerance_seconds, log_type, max_pages_per_run,
			 last_sync_at, last_success_at, last_error, total_imported, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	args := []interface{}{
		upstreamSyncConfigID, next.Enabled, next.BaseURL, next.Endpoint, next.AuthToken, next.UserID,
		next.PageSize, next.RequestDelayMS, next.IntervalMinutes, next.LookbackMinutes,
		next.OverlapMinutes, next.MatchToleranceSeconds, next.LogType, next.MaxPagesPerRun, next.LastSyncAt, next.LastSuccessAt,
		next.LastError, next.TotalImported, next.UpdatedAt,
	}
	if s.db.IsPG {
		query += `
			ON CONFLICT (id) DO UPDATE SET
				enabled = EXCLUDED.enabled,
				base_url = EXCLUDED.base_url,
				endpoint = EXCLUDED.endpoint,
				auth_token = EXCLUDED.auth_token,
				user_id = EXCLUDED.user_id,
				page_size = EXCLUDED.page_size,
				request_delay_ms = EXCLUDED.request_delay_ms,
				interval_minutes = EXCLUDED.interval_minutes,
				lookback_minutes = EXCLUDED.lookback_minutes,
				overlap_minutes = EXCLUDED.overlap_minutes,
				match_tolerance_seconds = EXCLUDED.match_tolerance_seconds,
				log_type = EXCLUDED.log_type,
				max_pages_per_run = EXCLUDED.max_pages_per_run,
				last_sync_at = EXCLUDED.last_sync_at,
				last_success_at = EXCLUDED.last_success_at,
				last_error = EXCLUDED.last_error,
				total_imported = EXCLUDED.total_imported,
				updated_at = EXCLUDED.updated_at`
	} else {
		query += `
			ON DUPLICATE KEY UPDATE
				enabled = VALUES(enabled),
				base_url = VALUES(base_url),
				endpoint = VALUES(endpoint),
				auth_token = VALUES(auth_token),
				user_id = VALUES(user_id),
				page_size = VALUES(page_size),
				request_delay_ms = VALUES(request_delay_ms),
				interval_minutes = VALUES(interval_minutes),
				lookback_minutes = VALUES(lookback_minutes),
				overlap_minutes = VALUES(overlap_minutes),
				match_tolerance_seconds = VALUES(match_tolerance_seconds),
				log_type = VALUES(log_type),
				max_pages_per_run = VALUES(max_pages_per_run),
				last_sync_at = VALUES(last_sync_at),
				last_success_at = VALUES(last_success_at),
				last_error = VALUES(last_error),
				total_imported = VALUES(total_imported),
				updated_at = VALUES(updated_at)`
	}
	if _, err := s.db.Execute(s.db.RebindQuery(query), args...); err != nil {
		return UpstreamLogSyncConfig{}, err
	}
	return s.GetConfig(false)
}

func normalizeUpstreamSyncConfig(cfg UpstreamLogSyncConfig) UpstreamLogSyncConfig {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	if cfg.Endpoint == "" {
		cfg.Endpoint = "auto"
	}
	if cfg.Endpoint != "auto" && !strings.HasPrefix(cfg.Endpoint, "/") {
		cfg.Endpoint = "/" + cfg.Endpoint
	}
	cfg.PageSize = clampIntValue(cfg.PageSize, 1, 1000, 100)
	cfg.RequestDelayMS = clampIntValue(cfg.RequestDelayMS, 0, 5000, 80)
	cfg.IntervalMinutes = clampIntValue(cfg.IntervalMinutes, 0, 1440, 0)
	cfg.LookbackMinutes = clampIntValue(cfg.LookbackMinutes, 1, 525600, 60)
	cfg.OverlapMinutes = clampIntValue(cfg.OverlapMinutes, 0, 1440, 10)
	cfg.MatchToleranceSeconds = clampIntValue(cfg.MatchToleranceSeconds, 1, 3600, 60)
	cfg.LogType = clampIntValue(cfg.LogType, 0, 9, 2)
	cfg.MaxPagesPerRun = clampIntValue(cfg.MaxPagesPerRun, 1, 100000, 1000)
	return cfg
}

func clampIntValue(value, minVal, maxVal, fallback int) int {
	if value <= 0 && minVal > 0 {
		return fallback
	}
	if value < minVal {
		return minVal
	}
	if value > maxVal {
		return maxVal
	}
	return value
}

// RunSync fetches upstream logs and upserts them into api_tools_upstream_logs.
func (s *UpstreamLogSyncService) RunSync(opts UpstreamLogSyncRunOptions) (map[string]interface{}, error) {
	cfg, err := s.GetConfig(true)
	if err != nil {
		return nil, err
	}
	if cfg.BaseURL == "" {
		err := fmt.Errorf("upstream base_url is required")
		s.markRunFinished(cfg, 0, err)
		return nil, err
	}
	if strings.TrimSpace(cfg.AuthToken) == "" {
		err := fmt.Errorf("upstream auth token is required")
		s.markRunFinished(cfg, 0, err)
		return nil, err
	}

	startTime, endTime := s.syncWindow(cfg, opts)
	if endTime <= startTime {
		err := fmt.Errorf("invalid sync range")
		s.markRunFinished(cfg, 0, err)
		return nil, err
	}
	logType := opts.LogType
	if logType < 0 {
		logType = cfg.LogType
	}
	if logType == 0 {
		logType = cfg.LogType
	}

	client := &http.Client{Timeout: upstreamSyncTimeout}
	endpoint, firstResp, err := s.detectEndpoint(client, cfg, startTime, endTime, logType)
	if err != nil {
		s.markRunFinished(cfg, 0, err)
		return nil, err
	}

	totalFetched := 0
	totalUpserted := 0
	totalRecords := extractUpstreamTotal(firstResp)
	items := extractUpstreamItems(firstResp)
	upserted, err := s.upsertImportedLogs(items)
	if err != nil {
		s.markRunFinished(cfg, 0, err)
		return nil, err
	}
	totalFetched += len(items)
	totalUpserted += upserted

	totalPages := 1
	if totalRecords > 0 {
		totalPages = int(math.Ceil(float64(totalRecords) / float64(cfg.PageSize)))
	}
	if totalPages < 1 && len(items) > 0 {
		totalPages = cfg.MaxPagesPerRun
	}
	if totalPages > cfg.MaxPagesPerRun {
		totalPages = cfg.MaxPagesPerRun
	}

	for page := 2; page <= totalPages; page++ {
		if cfg.RequestDelayMS > 0 {
			time.Sleep(time.Duration(cfg.RequestDelayMS) * time.Millisecond)
		}
		resp, err := s.fetchPage(client, cfg, endpoint, startTime, endTime, logType, page)
		if err != nil {
			s.markRunFinished(cfg, totalUpserted, err)
			return nil, err
		}
		pageItems := extractUpstreamItems(resp)
		if len(pageItems) == 0 {
			break
		}
		upserted, err := s.upsertImportedLogs(pageItems)
		if err != nil {
			s.markRunFinished(cfg, totalUpserted, err)
			return nil, err
		}
		totalFetched += len(pageItems)
		totalUpserted += upserted
	}

	matchResult, err := s.ReconcileMatches(startTime, endTime, cfg.MatchToleranceSeconds)
	if err != nil {
		s.markRunFinished(cfg, totalUpserted, err)
		return nil, err
	}

	if err := s.markRunFinished(cfg, totalUpserted, nil); err != nil {
		return nil, err
	}
	logger.L.Business(fmt.Sprintf(
		"上游日志同步完成: endpoint=%s fetched=%d upserted=%d matched=%d",
		endpoint,
		totalFetched,
		totalUpserted,
		toInt64(matchResult["matched_count"]),
	))

	return map[string]interface{}{
		"success":       true,
		"endpoint":      endpoint,
		"start_time":    startTime,
		"end_time":      endTime,
		"type":          logType,
		"fetched":       totalFetched,
		"upserted":      totalUpserted,
		"total_records": totalRecords,
		"match":         matchResult,
	}, nil
}

func (s *UpstreamLogSyncService) syncWindow(cfg UpstreamLogSyncConfig, opts UpstreamLogSyncRunOptions) (int64, int64) {
	now := time.Now().Unix()
	startTime := opts.StartTime
	endTime := opts.EndTime
	if endTime <= 0 {
		endTime = now
	}
	if startTime <= 0 {
		if cfg.LastSuccessAt > 0 {
			startTime = cfg.LastSuccessAt - int64(cfg.OverlapMinutes*60)
		} else {
			startTime = endTime - int64(cfg.LookbackMinutes*60)
		}
	}
	if startTime < 0 {
		startTime = 0
	}
	return startTime, endTime
}

// ReconcileMatches links imported upstream logs to local logs one-to-one.
func (s *UpstreamLogSyncService) ReconcileMatches(startTime, endTime int64, toleranceSeconds int) (map[string]interface{}, error) {
	result := map[string]interface{}{
		"available":               false,
		"upstream_count":          int64(0),
		"matched_count":           int64(0),
		"request_id_matches":      int64(0),
		"tokens_time_matches":     int64(0),
		"unmatched_count":         int64(0),
		"match_tolerance_seconds": toleranceSeconds,
	}
	if err := s.EnsureTables(); err != nil {
		return result, err
	}
	if !s.db.ColumnExists("logs", "id") ||
		!s.db.ColumnExists("logs", "created_at") ||
		!s.db.ColumnExists("logs", "prompt_tokens") ||
		!s.db.ColumnExists("logs", "completion_tokens") {
		return result, nil
	}

	toleranceSeconds = clampIntValue(toleranceSeconds, 1, 3600, 60)
	result["match_tolerance_seconds"] = toleranceSeconds
	windowStart := startTime - int64(toleranceSeconds)
	if windowStart < 0 {
		windowStart = 0
	}
	windowEnd := endTime + int64(toleranceSeconds)

	requestExpr := "''"
	if s.db.ColumnExists("logs", "request_id") {
		requestExpr = "COALESCE(request_id, '')"
	}
	localRows, err := s.db.QueryWithTimeout(45*time.Second, s.db.RebindQuery(fmt.Sprintf(`
		SELECT id, created_at, %s as request_id,
			COALESCE(prompt_tokens, 0) as prompt_tokens,
			COALESCE(completion_tokens, 0) as completion_tokens
		FROM logs
		WHERE created_at >= ? AND created_at <= ? AND type = 2
		ORDER BY created_at ASC, id ASC`, requestExpr)), windowStart, windowEnd)
	if err != nil {
		return result, err
	}
	upstreamRows, err := s.db.QueryWithTimeout(45*time.Second, s.db.RebindQuery(`
		SELECT id, created_at, COALESCE(request_id, '') as request_id,
			COALESCE(prompt_tokens, 0) as prompt_tokens,
			COALESCE(completion_tokens, 0) as completion_tokens
		FROM api_tools_upstream_logs
		WHERE created_at >= ? AND created_at <= ? AND type = 2
		ORDER BY created_at ASC, id ASC`), windowStart, windowEnd)
	if err != nil {
		return result, err
	}

	result["available"] = true
	result["upstream_count"] = int64(len(upstreamRows))
	if len(localRows) == 0 || len(upstreamRows) == 0 {
		result["unmatched_count"] = int64(len(upstreamRows))
		return result, s.clearMatches(windowStart, windowEnd)
	}

	localCandidates := make([]upstreamLogMatchCandidate, 0, len(localRows))
	localsByRequest := map[string][]int{}
	localsByTokens := map[string][]int{}
	for _, row := range localRows {
		candidate := upstreamLogMatchCandidate{
			ID:               toInt64(row["id"]),
			CreatedAt:        toInt64(row["created_at"]),
			RequestID:        strings.TrimSpace(toString(row["request_id"])),
			PromptTokens:     toInt64(row["prompt_tokens"]),
			CompletionTokens: toInt64(row["completion_tokens"]),
		}
		if candidate.ID <= 0 {
			continue
		}
		idx := len(localCandidates)
		localCandidates = append(localCandidates, candidate)
		if candidate.RequestID != "" {
			localsByRequest[candidate.RequestID] = append(localsByRequest[candidate.RequestID], idx)
		}
		if candidate.PromptTokens+candidate.CompletionTokens > 0 {
			key := tokenMatchKey(candidate.PromptTokens, candidate.CompletionTokens)
			localsByTokens[key] = append(localsByTokens[key], idx)
		}
	}

	usedLocal := map[int]bool{}
	usedUpstream := map[int]bool{}
	updates := []upstreamLogMatchUpdate{}
	requestIDMatches := int64(0)
	tokensTimeMatches := int64(0)

	upstreamCandidates := make([]upstreamLogMatchCandidate, 0, len(upstreamRows))
	for _, row := range upstreamRows {
		upstreamCandidates = append(upstreamCandidates, upstreamLogMatchCandidate{
			ID:               toInt64(row["id"]),
			CreatedAt:        toInt64(row["created_at"]),
			RequestID:        strings.TrimSpace(toString(row["request_id"])),
			PromptTokens:     toInt64(row["prompt_tokens"]),
			CompletionTokens: toInt64(row["completion_tokens"]),
		})
	}

	for upstreamIdx, upstream := range upstreamCandidates {
		if upstream.ID <= 0 || upstream.RequestID == "" {
			continue
		}
		bestIdx, bestScore := nearestLocalCandidate(localCandidates, localsByRequest[upstream.RequestID], usedLocal, upstream.CreatedAt, 0)
		if bestIdx < 0 {
			continue
		}
		usedLocal[bestIdx] = true
		usedUpstream[upstreamIdx] = true
		requestIDMatches++
		updates = append(updates, upstreamLogMatchUpdate{
			UpstreamID: upstream.ID,
			LocalID:    localCandidates[bestIdx].ID,
			Method:     "request_id",
			Score:      bestScore,
		})
	}

	for upstreamIdx, upstream := range upstreamCandidates {
		if upstream.ID <= 0 || usedUpstream[upstreamIdx] || upstream.PromptTokens+upstream.CompletionTokens <= 0 {
			continue
		}
		key := tokenMatchKey(upstream.PromptTokens, upstream.CompletionTokens)
		bestIdx, bestScore := nearestLocalCandidate(localCandidates, localsByTokens[key], usedLocal, upstream.CreatedAt, int64(toleranceSeconds))
		if bestIdx < 0 {
			continue
		}
		usedLocal[bestIdx] = true
		usedUpstream[upstreamIdx] = true
		tokensTimeMatches++
		updates = append(updates, upstreamLogMatchUpdate{
			UpstreamID: upstream.ID,
			LocalID:    localCandidates[bestIdx].ID,
			Method:     "tokens_time",
			Score:      bestScore,
		})
	}

	if err := s.persistMatches(windowStart, windowEnd, updates); err != nil {
		return result, err
	}

	matchedCount := requestIDMatches + tokensTimeMatches
	result["matched_count"] = matchedCount
	result["request_id_matches"] = requestIDMatches
	result["tokens_time_matches"] = tokensTimeMatches
	result["unmatched_count"] = int64(len(upstreamCandidates)) - matchedCount
	return result, nil
}

func (s *UpstreamLogSyncService) clearMatches(startTime, endTime int64) error {
	query := s.db.RebindQuery(`
		UPDATE api_tools_upstream_logs
		SET local_log_id = 0, match_method = '', match_score = 0, matched_at = 0
		WHERE created_at >= ? AND created_at <= ? AND type = 2`)
	_, err := s.db.Execute(query, startTime, endTime)
	return err
}

func (s *UpstreamLogSyncService) persistMatches(startTime, endTime int64, updates []upstreamLogMatchUpdate) error {
	tx, err := s.db.DB.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	clearSQL := s.db.RebindQuery(`
		UPDATE api_tools_upstream_logs
		SET local_log_id = 0, match_method = '', match_score = 0, matched_at = 0
		WHERE created_at >= ? AND created_at <= ? AND type = 2`)
	if _, err := tx.Exec(clearSQL, startTime, endTime); err != nil {
		return err
	}

	if len(updates) > 0 {
		updateSQL := s.db.RebindQuery(`
			UPDATE api_tools_upstream_logs
			SET local_log_id = ?, match_method = ?, match_score = ?, matched_at = ?
			WHERE id = ?`)
		now := time.Now().Unix()
		for _, update := range updates {
			if _, err := tx.Exec(updateSQL, update.LocalID, update.Method, update.Score, now, update.UpstreamID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func nearestLocalCandidate(candidates []upstreamLogMatchCandidate, indexes []int, used map[int]bool, targetTime int64, tolerance int64) (int, int64) {
	bestIdx := -1
	bestScore := int64(0)
	for _, idx := range indexes {
		if idx < 0 || idx >= len(candidates) || used[idx] {
			continue
		}
		score := absInt64(candidates[idx].CreatedAt - targetTime)
		if tolerance > 0 && score > tolerance {
			continue
		}
		if bestIdx < 0 || score < bestScore {
			bestIdx = idx
			bestScore = score
		}
	}
	return bestIdx, bestScore
}

func tokenMatchKey(promptTokens, completionTokens int64) string {
	return fmt.Sprintf("%d:%d", promptTokens, completionTokens)
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func (s *UpstreamLogSyncService) detectEndpoint(client *http.Client, cfg UpstreamLogSyncConfig, startTime, endTime int64, logType int) (string, map[string]interface{}, error) {
	endpoints := []string{cfg.Endpoint}
	if cfg.Endpoint == "auto" {
		endpoints = []string{"/api/log/", "/api/log/self/"}
	}
	var lastErr error
	for _, endpoint := range endpoints {
		resp, err := s.fetchPage(client, cfg, endpoint, startTime, endTime, logType, 1)
		if err == nil {
			return endpoint, resp, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no upstream endpoint available")
	}
	return "", nil, lastErr
}

func (s *UpstreamLogSyncService) fetchPage(client *http.Client, cfg UpstreamLogSyncConfig, endpoint string, startTime, endTime int64, logType, page int) (map[string]interface{}, error) {
	requestURL, err := url.Parse(cfg.BaseURL + endpoint)
	if err != nil {
		return nil, err
	}
	params := requestURL.Query()
	params.Set("p", strconv.Itoa(page))
	params.Set("page_size", strconv.Itoa(cfg.PageSize))
	if startTime > 0 {
		params.Set("start_timestamp", strconv.FormatInt(startTime, 10))
	}
	if endTime > 0 {
		params.Set("end_timestamp", strconv.FormatInt(endTime, 10))
	}
	if logType > 0 {
		params.Set("type", strconv.Itoa(logType))
	}
	requestURL.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	}
	if cfg.UserID != "" {
		req.Header.Set("New-API-User", cfg.UserID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyText := string(bytes.TrimSpace(body))
		if len(bodyText) > 200 {
			bodyText = bodyText[:200]
		}
		return nil, fmt.Errorf("upstream HTTP %d: %s", resp.StatusCode, bodyText)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	if success, ok := decoded["success"].(bool); ok && !success {
		return nil, fmt.Errorf("upstream success=false: %s", toString(decoded["message"]))
	}
	return decoded, nil
}

func extractUpstreamItems(resp map[string]interface{}) []map[string]interface{} {
	if resp == nil {
		return []map[string]interface{}{}
	}
	data := resp["data"]
	if items := mapItems(data); len(items) > 0 {
		return items
	}
	return []map[string]interface{}{}
}

func mapItems(value interface{}) []map[string]interface{} {
	switch v := value.(type) {
	case []interface{}:
		items := make([]map[string]interface{}, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				items = append(items, m)
			}
		}
		return items
	case map[string]interface{}:
		for _, key := range []string{"items", "data", "logs", "list"} {
			if items := mapItems(v[key]); len(items) > 0 {
				return items
			}
		}
	}
	return []map[string]interface{}{}
}

func extractUpstreamTotal(resp map[string]interface{}) int {
	if resp == nil {
		return 0
	}
	if data, ok := resp["data"].(map[string]interface{}); ok {
		for _, key := range []string{"total", "count"} {
			if total := int(toInt64(data[key])); total > 0 {
				return total
			}
		}
	}
	return 0
}

func (s *UpstreamLogSyncService) upsertImportedLogs(items []map[string]interface{}) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	if err := s.EnsureTables(); err != nil {
		return 0, err
	}

	tx, err := s.db.DB.Beginx()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	query := `
		INSERT INTO api_tools_upstream_logs
			(source_key, source_log_id, created_at, type, user_id, username, token_id, token_name,
			 group_name, model_name, channel_id, channel_name, prompt_tokens, completion_tokens,
			 quota, use_time, is_stream, request_id, ip, content, other, raw_json, imported_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if s.db.IsPG {
		query += `
			ON CONFLICT (source_key) DO UPDATE SET
				source_log_id = EXCLUDED.source_log_id,
				created_at = EXCLUDED.created_at,
				type = EXCLUDED.type,
				user_id = EXCLUDED.user_id,
				username = EXCLUDED.username,
				token_id = EXCLUDED.token_id,
				token_name = EXCLUDED.token_name,
				group_name = EXCLUDED.group_name,
				model_name = EXCLUDED.model_name,
				channel_id = EXCLUDED.channel_id,
				channel_name = EXCLUDED.channel_name,
				prompt_tokens = EXCLUDED.prompt_tokens,
				completion_tokens = EXCLUDED.completion_tokens,
				quota = EXCLUDED.quota,
				use_time = EXCLUDED.use_time,
				is_stream = EXCLUDED.is_stream,
				request_id = EXCLUDED.request_id,
				ip = EXCLUDED.ip,
				content = EXCLUDED.content,
				other = EXCLUDED.other,
				raw_json = EXCLUDED.raw_json,
				imported_at = EXCLUDED.imported_at`
	} else {
		query += `
			ON DUPLICATE KEY UPDATE
				source_log_id = VALUES(source_log_id),
				created_at = VALUES(created_at),
				type = VALUES(type),
				user_id = VALUES(user_id),
				username = VALUES(username),
				token_id = VALUES(token_id),
				token_name = VALUES(token_name),
				group_name = VALUES(group_name),
				model_name = VALUES(model_name),
				channel_id = VALUES(channel_id),
				channel_name = VALUES(channel_name),
				prompt_tokens = VALUES(prompt_tokens),
				completion_tokens = VALUES(completion_tokens),
				quota = VALUES(quota),
				use_time = VALUES(use_time),
				is_stream = VALUES(is_stream),
				request_id = VALUES(request_id),
				ip = VALUES(ip),
				content = VALUES(content),
				other = VALUES(other),
				raw_json = VALUES(raw_json),
				imported_at = VALUES(imported_at)`
	}
	query = s.db.RebindQuery(query)

	now := time.Now().Unix()
	for _, item := range items {
		row := normalizeUpstreamLogRow(item, now)
		if row.SourceKey == "" {
			continue
		}
		if _, err := tx.Exec(query,
			row.SourceKey, row.SourceLogID, row.CreatedAt, row.Type, row.UserID, row.Username,
			row.TokenID, row.TokenName, row.GroupName, row.ModelName, row.ChannelID, row.ChannelName,
			row.PromptTokens, row.CompletionTokens, row.Quota, row.UseTime, row.IsStream,
			row.RequestID, row.IP, row.Content, row.Other, row.RawJSON, row.ImportedAt,
		); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(items), nil
}

type upstreamLogRow struct {
	SourceKey        string
	SourceLogID      string
	CreatedAt        int64
	Type             int
	UserID           int64
	Username         string
	TokenID          int64
	TokenName        string
	GroupName        string
	ModelName        string
	ChannelID        int64
	ChannelName      string
	PromptTokens     int64
	CompletionTokens int64
	Quota            int64
	UseTime          float64
	IsStream         bool
	RequestID        string
	IP               string
	Content          string
	Other            string
	RawJSON          string
	ImportedAt       int64
}

func normalizeUpstreamLogRow(item map[string]interface{}, importedAt int64) upstreamLogRow {
	rawJSONBytes, _ := json.Marshal(item)
	sourceLogID := toString(item["id"])
	requestID := firstStringValue(item, "request_id", "requestId")
	sourceKey := ""
	if sourceLogID != "" && sourceLogID != "0" {
		sourceKey = "id:" + sourceLogID
	} else if requestID != "" {
		sourceKey = "request:" + requestID
	} else {
		hash := sha1.Sum(rawJSONBytes)
		sourceKey = "sha1:" + hex.EncodeToString(hash[:])
	}
	other := item["other"]
	otherText := ""
	switch v := other.(type) {
	case string:
		otherText = v
	case nil:
		otherText = ""
	default:
		if data, err := json.Marshal(v); err == nil {
			otherText = string(data)
		}
	}

	return upstreamLogRow{
		SourceKey:        sourceKey,
		SourceLogID:      sourceLogID,
		CreatedAt:        firstInt64Value(item, "created_at", "createdAt", "timestamp"),
		Type:             int(firstInt64Value(item, "type")),
		UserID:           firstInt64Value(item, "user_id", "userId"),
		Username:         firstStringValue(item, "username", "user_name"),
		TokenID:          firstInt64Value(item, "token_id", "tokenId"),
		TokenName:        firstStringValue(item, "token_name", "tokenName"),
		GroupName:        firstStringValue(item, "group", "group_name"),
		ModelName:        firstStringValue(item, "model_name", "modelName", "model"),
		ChannelID:        firstInt64Value(item, "channel_id", "channel"),
		ChannelName:      firstStringValue(item, "channel_name", "channelName"),
		PromptTokens:     firstInt64Value(item, "prompt_tokens", "promptTokens"),
		CompletionTokens: firstInt64Value(item, "completion_tokens", "completionTokens"),
		Quota:            firstInt64Value(item, "quota"),
		UseTime:          firstFloat64Value(item, "use_time", "duration"),
		IsStream:         toBool(firstValue(item, "is_stream", "isStream")),
		RequestID:        requestID,
		IP:               firstStringValue(item, "ip"),
		Content:          firstStringValue(item, "content"),
		Other:            otherText,
		RawJSON:          string(rawJSONBytes),
		ImportedAt:       importedAt,
	}
}

func firstValue(item map[string]interface{}, keys ...string) interface{} {
	for _, key := range keys {
		if value, ok := item[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func firstStringValue(item map[string]interface{}, keys ...string) string {
	return toString(firstValue(item, keys...))
}

func firstInt64Value(item map[string]interface{}, keys ...string) int64 {
	return toInt64(firstValue(item, keys...))
}

func firstFloat64Value(item map[string]interface{}, keys ...string) float64 {
	return toFloat64(firstValue(item, keys...))
}

func (s *UpstreamLogSyncService) markRunFinished(cfg UpstreamLogSyncConfig, imported int, runErr error) error {
	now := time.Now().Unix()
	lastError := ""
	lastSuccessAt := cfg.LastSuccessAt
	totalImported := cfg.TotalImported + int64(imported)
	if runErr != nil {
		lastError = runErr.Error()
	} else {
		lastSuccessAt = now
	}
	query := s.db.RebindQuery(`
		UPDATE api_tools_upstream_log_sync_config
		SET last_sync_at = ?, last_success_at = ?, last_error = ?, total_imported = ?, updated_at = ?
		WHERE id = ?`)
	_, err := s.db.Execute(query, now, lastSuccessAt, lastError, totalImported, now, upstreamSyncConfigID)
	return err
}

// UpstreamImportSummary returns imported upstream usage totals for a range.
func (s *UpstreamLogSyncService) UpstreamImportSummary(startTime, endTime int64, channelID *int64) map[string]interface{} {
	result := map[string]interface{}{
		"available":               false,
		"request_count":           int64(0),
		"matched_request_count":   int64(0),
		"unmatched_request_count": int64(0),
		"request_id_matches":      int64(0),
		"tokens_time_matches":     int64(0),
		"quota_used":              int64(0),
		"cost":                    float64(0),
	}
	if exists, err := s.db.TableExists("api_tools_upstream_logs"); err != nil || !exists {
		return result
	}
	query := `
		SELECT COUNT(*) as request_count,
			COALESCE(SUM(CASE WHEN local_log_id > 0 THEN 1 ELSE 0 END), 0) as matched_request_count,
			COALESCE(SUM(CASE WHEN match_method = 'request_id' THEN 1 ELSE 0 END), 0) as request_id_matches,
			COALESCE(SUM(CASE WHEN match_method = 'tokens_time' THEN 1 ELSE 0 END), 0) as tokens_time_matches,
			COALESCE(SUM(quota), 0) as quota_used
		FROM api_tools_upstream_logs
		WHERE created_at >= ? AND created_at <= ? AND type = 2`
	args := []interface{}{startTime, endTime}
	if channelID != nil && *channelID > 0 {
		query += ` AND channel_id = ?`
		args = append(args, *channelID)
	}
	row, err := s.db.QueryOneWithTimeout(20*time.Second, s.db.RebindQuery(query), args...)
	if err != nil || row == nil {
		return result
	}
	quota := toInt64(row["quota_used"])
	result["available"] = true
	requestCount := toInt64(row["request_count"])
	matchedCount := toInt64(row["matched_request_count"])
	result["request_count"] = requestCount
	result["matched_request_count"] = matchedCount
	result["unmatched_request_count"] = requestCount - matchedCount
	result["request_id_matches"] = toInt64(row["request_id_matches"])
	result["tokens_time_matches"] = toInt64(row["tokens_time_matches"])
	result["quota_used"] = quota
	result["cost"] = roundMoney(float64(quota) / costQuotaPerUSD)
	return result
}
