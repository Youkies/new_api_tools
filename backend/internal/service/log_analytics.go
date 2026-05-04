package service

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/new-api-tools/backend/internal/cache"
	"github.com/new-api-tools/backend/internal/database"
	"github.com/new-api-tools/backend/internal/logger"
)

const (
	analyticsStatePrefix = "analytics:"
	defaultBatchSize     = 5000
	defaultMaxIterations = 100
	defaultQuotaPerUSD   = 500000
)

// LogExportOptions controls direct log exports from the logs table.
type LogExportOptions struct {
	StartTime    int64
	EndTime      int64
	Type         int
	ModelName    string
	Username     string
	TokenName    string
	Channel      string
	Group        string
	RequestID    string
	QuotaPerUnit int64
	MaxRows      int
}

// LogAnalyticsService handles log analytics via direct DB queries + cache
type LogAnalyticsService struct {
	db *database.Manager
}

// NewLogAnalyticsService creates a new LogAnalyticsService
func NewLogAnalyticsService() *LogAnalyticsService {
	return &LogAnalyticsService{db: database.Get()}
}

// GetAnalyticsState returns current processing state from DB
// Goes directly to DB to count processed logs (type=2 and type=5)
func (s *LogAnalyticsService) GetAnalyticsState() map[string]interface{} {
	cm := cache.Get()
	var cached map[string]interface{}
	found, _ := cm.GetJSON("analytics:state", &cached)
	if found {
		return cached
	}

	// Get actual counts from database
	row, err := s.db.QueryOne(`
		SELECT COUNT(*) as total_processed, COALESCE(MAX(id), 0) as last_log_id
		FROM logs WHERE type IN (2, 5)`)
	if err != nil || row == nil {
		return map[string]interface{}{
			"last_log_id":       0,
			"last_processed_at": 0,
			"total_processed":   0,
		}
	}

	result := map[string]interface{}{
		"last_log_id":       toInt64(row["last_log_id"]),
		"last_processed_at": time.Now().Unix(),
		"total_processed":   toInt64(row["total_processed"]),
	}

	cm.Set("analytics:state", result, 60*time.Second)
	return result
}

// GetUserRequestRanking returns top users by request count
func (s *LogAnalyticsService) GetUserRequestRanking(limit int) ([]map[string]interface{}, error) {
	cm := cache.Get()
	var cached []map[string]interface{}
	found, _ := cm.GetJSON("analytics:user_request_ranking", &cached)
	if found && len(cached) > 0 {
		if limit > 0 && limit < len(cached) {
			return cached[:limit], nil
		}
		return cached, nil
	}

	var rows []map[string]interface{}
	var err error

	if IsQuotaDataAvailable() {
		// Fast path: aggregate from quota_data
		query := s.db.RebindQuery(`
			SELECT q.user_id,
				COALESCE(u.username, '') as username,
				COALESCE(SUM(q.count), 0) as request_count,
				COALESCE(SUM(q.quota), 0) as quota_used
			FROM quota_data q
			LEFT JOIN users u ON q.user_id = u.id
			WHERE q.user_id > 0
			GROUP BY q.user_id, u.username
			ORDER BY request_count DESC
			LIMIT ?`)
		rows, err = s.db.QueryWithTimeout(30*time.Second, query, limit)
	} else {
		// Fallback: scan logs with 30-day filter
		thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Unix()
		query := s.db.RebindQuery(`
			SELECT l.user_id,
				COALESCE(l.username, '') as username,
				COUNT(*) as request_count,
				COALESCE(SUM(l.quota), 0) as quota_used
			FROM logs l
			WHERE l.type IN (2, 5) AND l.user_id > 0 AND l.created_at >= ?
			GROUP BY l.user_id, l.username
			ORDER BY request_count DESC
			LIMIT ?`)
		rows, err = s.db.QueryWithTimeout(30*time.Second, query, thirtyDaysAgo, limit)
	}
	if err != nil {
		return nil, err
	}

	cm.Set("analytics:user_request_ranking", rows, 5*time.Minute)
	return rows, nil
}

// GetUserQuotaRanking returns top users by quota consumption
func (s *LogAnalyticsService) GetUserQuotaRanking(limit int) ([]map[string]interface{}, error) {
	cm := cache.Get()
	var cached []map[string]interface{}
	found, _ := cm.GetJSON("analytics:user_quota_ranking", &cached)
	if found && len(cached) > 0 {
		if limit > 0 && limit < len(cached) {
			return cached[:limit], nil
		}
		return cached, nil
	}

	var rows []map[string]interface{}
	var err error

	if IsQuotaDataAvailable() {
		query := s.db.RebindQuery(`
			SELECT q.user_id,
				COALESCE(u.username, '') as username,
				COALESCE(SUM(q.count), 0) as request_count,
				COALESCE(SUM(q.quota), 0) as quota_used
			FROM quota_data q
			LEFT JOIN users u ON q.user_id = u.id
			WHERE q.user_id > 0
			GROUP BY q.user_id, u.username
			ORDER BY quota_used DESC
			LIMIT ?`)
		rows, err = s.db.QueryWithTimeout(30*time.Second, query, limit)
	} else {
		thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Unix()
		query := s.db.RebindQuery(`
			SELECT l.user_id,
				COALESCE(l.username, '') as username,
				COUNT(*) as request_count,
				COALESCE(SUM(l.quota), 0) as quota_used
			FROM logs l
			WHERE l.type IN (2, 5) AND l.user_id > 0 AND l.created_at >= ?
			GROUP BY l.user_id, l.username
			ORDER BY quota_used DESC
			LIMIT ?`)
		rows, err = s.db.QueryWithTimeout(30*time.Second, query, thirtyDaysAgo, limit)
	}
	if err != nil {
		return nil, err
	}

	cm.Set("analytics:user_quota_ranking", rows, 5*time.Minute)
	return rows, nil
}

// GetModelStatistics returns model usage statistics with success_rate and empty_rate
func (s *LogAnalyticsService) GetModelStatistics(limit int) ([]map[string]interface{}, error) {
	cm := cache.Get()
	var cached []map[string]interface{}
	found, _ := cm.GetJSON("analytics:model_statistics", &cached)
	if found && len(cached) > 0 {
		if limit > 0 && limit < len(cached) {
			return cached[:limit], nil
		}
		return cached, nil
	}

	thirtyDaysAgo := time.Now().AddDate(0, 0, -30).Unix()
	query := s.db.RebindQuery(`
		SELECT model_name,
			COUNT(*) as total_requests,
			SUM(CASE WHEN type = 2 THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN type = 5 THEN 1 ELSE 0 END) as failure_count,
			SUM(CASE WHEN type = 2 AND completion_tokens = 0 THEN 1 ELSE 0 END) as empty_count
		FROM logs
		WHERE type IN (2, 5) AND model_name != '' AND created_at >= ?
		GROUP BY model_name
		ORDER BY total_requests DESC
		LIMIT ?`)

	rows, err := s.db.QueryWithTimeout(30*time.Second, query, thirtyDaysAgo, limit)
	if err != nil {
		return nil, err
	}

	// Calculate success_rate and empty_rate
	for _, row := range rows {
		total := toInt64(row["total_requests"])
		success := toInt64(row["success_count"])
		empty := toInt64(row["empty_count"])

		successRate := float64(0)
		if total > 0 {
			successRate = float64(success) / float64(total) * 100
		}
		emptyRate := float64(0)
		if success > 0 {
			emptyRate = float64(empty) / float64(success) * 100
		}

		row["success_rate"] = math.Round(successRate*100) / 100
		row["empty_rate"] = math.Round(emptyRate*100) / 100
	}

	cm.Set("analytics:model_statistics", rows, 5*time.Minute)
	return rows, nil
}

// GetSummary returns analytics summary matching Python backend format
// Frontend expects: state, user_request_ranking, user_quota_ranking, model_statistics
func (s *LogAnalyticsService) GetSummary() (map[string]interface{}, error) {
	state := s.GetAnalyticsState()

	requestRanking, err := s.GetUserRequestRanking(10)
	if err != nil {
		requestRanking = []map[string]interface{}{}
	}

	quotaRanking, err := s.GetUserQuotaRanking(10)
	if err != nil {
		quotaRanking = []map[string]interface{}{}
	}

	modelStats, err := s.GetModelStatistics(20)
	if err != nil {
		modelStats = []map[string]interface{}{}
	}

	return map[string]interface{}{
		"state":                state,
		"user_request_ranking": requestRanking,
		"user_quota_ranking":   quotaRanking,
		"model_statistics":     modelStats,
	}, nil
}

// ProcessLogs clears caches and returns actual total count
// In Go implementation, data is queried live from DB — "processing" means refreshing cache
func (s *LogAnalyticsService) ProcessLogs() (map[string]interface{}, error) {
	s.clearAllCaches()

	// Get actual counts to return meaningful response
	row, _ := s.db.QueryOne(`
		SELECT COUNT(*) as total, COALESCE(MAX(id), 0) as max_id
		FROM logs WHERE type IN (2, 5)`)

	total := int64(0)
	maxID := int64(0)
	if row != nil {
		total = toInt64(row["total"])
		maxID = toInt64(row["max_id"])
	}

	logger.L.Business(fmt.Sprintf("日志分析处理完成，共 %d 条日志", total))

	return map[string]interface{}{
		"success":        true,
		"processed":      total,
		"message":        "Analytics cache refreshed, data will reload on next query",
		"last_log_id":    maxID,
		"users_updated":  0,
		"models_updated": 0,
	}, nil
}

// BatchProcess clears caches and returns completion status
// Since Go queries DB directly (no incremental state), batch process just refreshes everything
func (s *LogAnalyticsService) BatchProcess(maxIterations int) (map[string]interface{}, error) {
	if maxIterations <= 0 {
		maxIterations = defaultMaxIterations
	}

	start := time.Now()
	s.clearAllCaches()

	// Get total log count for progress reporting
	row, _ := s.db.QueryOne(`
		SELECT COUNT(*) as total, COALESCE(MAX(id), 0) as max_id
		FROM logs WHERE type IN (2, 5)`)

	total := int64(0)
	maxID := int64(0)
	if row != nil {
		total = toInt64(row["total"])
		maxID = toInt64(row["max_id"])
	}

	elapsed := time.Since(start).Seconds()
	logsPerSec := float64(0)
	if elapsed > 0 {
		logsPerSec = float64(total) / elapsed
	}

	return map[string]interface{}{
		"success":          true,
		"total_processed":  total,
		"iterations":       1,
		"batch_size":       defaultBatchSize,
		"elapsed_seconds":  math.Round(elapsed*100) / 100,
		"logs_per_second":  math.Round(logsPerSec*10) / 10,
		"progress_percent": 100.0,
		"remaining_logs":   0,
		"last_log_id":      maxID,
		"completed":        true,
		"timed_out":        false,
	}, nil
}

// ResetAnalytics clears all analytics caches
func (s *LogAnalyticsService) ResetAnalytics() error {
	s.clearAllCaches()
	logger.L.Business("分析数据已重置")
	return nil
}

// GetSyncStatus returns sync status matching frontend SyncStatus interface
func (s *LogAnalyticsService) GetSyncStatus() (map[string]interface{}, error) {
	// Since Go queries DB directly, we are always "synced"
	row, err := s.db.QueryOne(`
		SELECT COUNT(*) as total, COALESCE(MAX(id), 0) as max_id
		FROM logs WHERE type IN (2, 5)`)
	if err != nil {
		return nil, err
	}

	total := int64(0)
	maxID := int64(0)
	if row != nil {
		total = toInt64(row["total"])
		maxID = toInt64(row["max_id"])
	}

	return map[string]interface{}{
		"last_log_id":        maxID,
		"max_log_id":         maxID,
		"init_cutoff_id":     nil,
		"total_logs_in_db":   total,
		"total_processed":    total,
		"progress_percent":   100.0,
		"remaining_logs":     0,
		"is_synced":          true,
		"is_initializing":    false,
		"needs_initial_sync": false,
		"data_inconsistent":  false,
		"needs_reset":        false,
	}, nil
}

// CheckDataConsistency checks data consistency
func (s *LogAnalyticsService) CheckDataConsistency(autoReset bool) (map[string]interface{}, error) {
	syncStatus, err := s.GetSyncStatus()
	if err != nil {
		return nil, err
	}

	// Since Go queries DB directly, data is always consistent
	return map[string]interface{}{
		"consistent":        true,
		"reset":             false,
		"message":           "Data is consistent (Go backend queries DB directly)",
		"data_inconsistent": false,
		"needs_reset":       false,
		"details":           syncStatus,
	}, nil
}

// clearAllCaches removes all analytics-related caches
func (s *LogAnalyticsService) clearAllCaches() {
	cm := cache.Get()
	cm.Delete("analytics:state")
	cm.Delete("analytics:user_request_ranking")
	cm.Delete("analytics:user_quota_ranking")
	cm.Delete("analytics:model_statistics")
	cm.Delete(analyticsStatePrefix)
}

// OpenLogExport opens a streaming row cursor for matching log rows.
func (s *LogAnalyticsService) OpenLogExport(ctx context.Context, opts LogExportOptions) (*sqlx.Rows, error) {
	query, args, err := s.buildLogExportQuery(opts)
	if err != nil {
		return nil, err
	}
	return s.db.DB.QueryxContext(ctx, query, args...)
}

// WriteLogExportCSV writes matching rows as a CSV file and returns the row count.
func (s *LogAnalyticsService) WriteLogExportCSV(rows *sqlx.Rows, w io.Writer, opts LogExportOptions) (int64, error) {
	defer rows.Close()

	quotaPerUnit := opts.QuotaPerUnit
	if quotaPerUnit <= 0 {
		quotaPerUnit = defaultQuotaPerUSD
	}

	writer := csv.NewWriter(w)
	headers := []string{
		"时间",
		"用户名",
		"用户ID",
		"令牌名称",
		"分组",
		"模型名称",
		"渠道ID",
		"渠道名称",
		"类型",
		"输入Tokens",
		"输出Tokens",
		"总Tokens",
		"Quota",
		"费用(USD)",
		"耗时(ms)",
		"是否流式",
		"模型倍率",
		"分组倍率",
		"补全倍率",
		"Request ID",
		"IP",
		"详情",
	}
	if err := writer.Write(headers); err != nil {
		return 0, err
	}

	var count int64
	var totalQuota int64
	var totalInput int64
	var totalOutput int64

	for rows.Next() {
		row, err := scanLogExportRow(rows)
		if err != nil {
			return count, err
		}
		other := parseLogOther(toString(row["other"]))
		inputTokens := toInt64(row["prompt_tokens"])
		outputTokens := toInt64(row["completion_tokens"])
		quota := toInt64(row["quota"])
		totalTokens := inputTokens + outputTokens

		record := []string{
			formatLogExportTimestamp(toInt64(row["created_at"])),
			toString(row["username"]),
			strconv.FormatInt(toInt64(row["user_id"]), 10),
			toString(row["token_name"]),
			toString(row["group_name"]),
			toString(row["model_name"]),
			strconv.FormatInt(toInt64(row["channel_id"]), 10),
			toString(row["channel_name"]),
			logTypeLabel(toInt64(row["type"])),
			strconv.FormatInt(inputTokens, 10),
			strconv.FormatInt(outputTokens, 10),
			strconv.FormatInt(totalTokens, 10),
			strconv.FormatInt(quota, 10),
			fmt.Sprintf("%.6f", float64(quota)/float64(quotaPerUnit)),
			formatLogExportNumber(row["use_time"]),
			formatBoolCN(toBool(row["is_stream"])),
			otherValue(other, "model_ratio"),
			otherValue(other, "group_ratio"),
			otherValue(other, "completion_ratio"),
			toString(row["request_id"]),
			toString(row["ip"]),
			toString(row["content"]),
		}
		if err := writer.Write(record); err != nil {
			return count, err
		}

		count++
		totalQuota += quota
		totalInput += inputTokens
		totalOutput += outputTokens
	}
	if err := rows.Err(); err != nil {
		return count, err
	}

	if err := writer.Write([]string{}); err != nil {
		return count, err
	}
	summary := []string{
		"汇总",
		"",
		"",
		"",
		"",
		"",
		"",
		"合计",
		"",
		strconv.FormatInt(totalInput, 10),
		strconv.FormatInt(totalOutput, 10),
		strconv.FormatInt(totalInput+totalOutput, 10),
		strconv.FormatInt(totalQuota, 10),
		fmt.Sprintf("%.6f", float64(totalQuota)/float64(quotaPerUnit)),
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
	}
	if err := writer.Write(summary); err != nil {
		return count, err
	}
	writer.Flush()
	return count, writer.Error()
}

// WriteLogExportJSON writes matching rows as a JSON array and returns the row count.
func (s *LogAnalyticsService) WriteLogExportJSON(rows *sqlx.Rows, w io.Writer, opts LogExportOptions) (int64, error) {
	defer rows.Close()

	quotaPerUnit := opts.QuotaPerUnit
	if quotaPerUnit <= 0 {
		quotaPerUnit = defaultQuotaPerUSD
	}

	if _, err := io.WriteString(w, "[\n"); err != nil {
		return 0, err
	}

	var count int64
	for rows.Next() {
		row, err := scanLogExportRow(rows)
		if err != nil {
			return count, err
		}
		quota := toInt64(row["quota"])
		row["_formatted_time"] = formatLogExportTimestamp(toInt64(row["created_at"]))
		row["_cost_usd"] = fmt.Sprintf("%.6f", float64(quota)/float64(quotaPerUnit))
		row["_total_tokens"] = toInt64(row["prompt_tokens"]) + toInt64(row["completion_tokens"])
		row["_other_parsed"] = parseLogOther(toString(row["other"]))

		if count > 0 {
			if _, err := io.WriteString(w, ",\n"); err != nil {
				return count, err
			}
		}
		payload, err := json.Marshal(row)
		if err != nil {
			return count, err
		}
		if _, err := w.Write(payload); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	if _, err := io.WriteString(w, "\n]\n"); err != nil {
		return count, err
	}
	return count, nil
}

func (s *LogAnalyticsService) buildLogExportQuery(opts LogExportOptions) (string, []interface{}, error) {
	columns := s.logColumnMap()
	if !columns["created_at"] {
		return "", nil, fmt.Errorf("logs.created_at column is required")
	}

	channelTableExists, _ := s.db.TableExists("channels")
	canJoinChannels := channelTableExists && columns["channel_id"] && s.db.ColumnExists("channels", "id")

	selects := []string{
		s.logExportIntExpr(columns, "id", "id"),
		s.logExportIntExpr(columns, "user_id", "user_id"),
		s.logExportIntExpr(columns, "created_at", "created_at"),
		s.logExportIntExpr(columns, "type", "type"),
		s.logExportTextExpr(columns, "username", "username"),
		s.logExportTextExpr(columns, "token_name", "token_name"),
		s.logExportTextExpr(columns, "group", "group_name"),
		s.logExportTextExpr(columns, "model_name", "model_name"),
		s.logExportIntExpr(columns, "channel_id", "channel_id"),
		s.logExportIntExpr(columns, "prompt_tokens", "prompt_tokens"),
		s.logExportIntExpr(columns, "completion_tokens", "completion_tokens"),
		s.logExportIntExpr(columns, "quota", "quota"),
		s.logExportTextExpr(columns, "request_id", "request_id"),
		s.logExportTextExpr(columns, "ip", "ip"),
		s.logExportTextExpr(columns, "content", "content"),
		s.logExportTextExpr(columns, "other", "other"),
		s.logExportBoolExpr(columns, "is_stream", "is_stream"),
	}
	if columns["use_time"] {
		selects = append(selects, s.logExportNumberExpr(columns, "use_time", "use_time"))
	} else {
		selects = append(selects, s.logExportNumberExpr(columns, "duration", "use_time"))
	}
	if canJoinChannels && s.db.ColumnExists("channels", "name") {
		selects = append(selects, "COALESCE(c.name, '') as channel_name")
	} else if columns["channel_name"] {
		selects = append(selects, s.logExportTextExpr(columns, "channel_name", "channel_name"))
	} else {
		selects = append(selects, "'' as channel_name")
	}

	query := "SELECT " + strings.Join(selects, ", ") + " FROM logs l"
	if canJoinChannels {
		query += " LEFT JOIN channels c ON c.id = l.channel_id"
	}

	where := []string{"1=1"}
	args := []interface{}{}
	if opts.StartTime > 0 {
		where = append(where, "l.created_at >= ?")
		args = append(args, opts.StartTime)
	}
	if opts.EndTime > 0 {
		where = append(where, "l.created_at <= ?")
		args = append(args, opts.EndTime)
	}
	if opts.Type > 0 {
		if !columns["type"] {
			return "", nil, fmt.Errorf("logs.type column is required for type filter")
		}
		where = append(where, "l.type = ?")
		args = append(args, opts.Type)
	}
	if opts.ModelName != "" {
		if !columns["model_name"] {
			return "", nil, fmt.Errorf("logs.model_name column is required for model filter")
		}
		where = append(where, s.logExportColumn("l", "model_name")+" = ?")
		args = append(args, opts.ModelName)
	}
	if opts.Username != "" {
		if !columns["username"] {
			return "", nil, fmt.Errorf("logs.username column is required for username filter")
		}
		where = append(where, s.logExportColumn("l", "username")+" = ?")
		args = append(args, opts.Username)
	}
	if opts.TokenName != "" {
		if !columns["token_name"] {
			return "", nil, fmt.Errorf("logs.token_name column is required for token filter")
		}
		where = append(where, s.logExportColumn("l", "token_name")+" = ?")
		args = append(args, opts.TokenName)
	}
	if opts.Group != "" {
		if !columns["group"] {
			return "", nil, fmt.Errorf("logs.group column is required for group filter")
		}
		where = append(where, s.logExportColumn("l", "group")+" = ?")
		args = append(args, opts.Group)
	}
	if opts.RequestID != "" {
		if !columns["request_id"] {
			return "", nil, fmt.Errorf("logs.request_id column is required for request_id filter")
		}
		where = append(where, s.logExportColumn("l", "request_id")+" = ?")
		args = append(args, opts.RequestID)
	}
	if opts.Channel != "" {
		if channelID, err := strconv.ParseInt(strings.TrimSpace(opts.Channel), 10, 64); err == nil {
			if !columns["channel_id"] {
				return "", nil, fmt.Errorf("logs.channel_id column is required for channel filter")
			}
			where = append(where, "l.channel_id = ?")
			args = append(args, channelID)
		} else if canJoinChannels && s.db.ColumnExists("channels", "name") {
			where = append(where, "c.name = ?")
			args = append(args, opts.Channel)
		} else {
			return "", nil, fmt.Errorf("channel filter requires logs.channel_id or channels.name")
		}
	}

	query += " WHERE " + strings.Join(where, " AND ")
	query += " ORDER BY l.created_at ASC, l.id ASC"
	if opts.MaxRows > 0 {
		query += " LIMIT ?"
		args = append(args, opts.MaxRows)
	}

	return s.db.RebindQuery(query), args, nil
}

func (s *LogAnalyticsService) logColumnMap() map[string]bool {
	names := []string{
		"id", "user_id", "created_at", "type", "username", "token_name", "group",
		"model_name", "channel_id", "channel_name", "prompt_tokens", "completion_tokens",
		"quota", "request_id", "ip", "content", "other", "is_stream", "use_time", "duration",
	}
	result := make(map[string]bool, len(names))
	for _, name := range names {
		result[name] = s.db.ColumnExists("logs", name)
	}
	return result
}

func (s *LogAnalyticsService) logExportColumn(alias, column string) string {
	quoted := column
	if column == "group" {
		if s.db.IsPG {
			quoted = `"group"`
		} else {
			quoted = "`group`"
		}
	}
	return alias + "." + quoted
}

func (s *LogAnalyticsService) logExportTextExpr(columns map[string]bool, column, alias string) string {
	if !columns[column] {
		return "'' as " + alias
	}
	return fmt.Sprintf("COALESCE(%s, '') as %s", s.logExportColumn("l", column), alias)
}

func (s *LogAnalyticsService) logExportIntExpr(columns map[string]bool, column, alias string) string {
	if !columns[column] {
		return "0 as " + alias
	}
	return fmt.Sprintf("COALESCE(%s, 0) as %s", s.logExportColumn("l", column), alias)
}

func (s *LogAnalyticsService) logExportNumberExpr(columns map[string]bool, column, alias string) string {
	if !columns[column] {
		return "0 as " + alias
	}
	return fmt.Sprintf("COALESCE(%s, 0) as %s", s.logExportColumn("l", column), alias)
}

func (s *LogAnalyticsService) logExportBoolExpr(columns map[string]bool, column, alias string) string {
	if !columns[column] {
		return "0 as " + alias
	}
	if s.db.IsPG {
		return fmt.Sprintf("COALESCE(%s, FALSE) as %s", s.logExportColumn("l", column), alias)
	}
	return fmt.Sprintf("COALESCE(%s, 0) as %s", s.logExportColumn("l", column), alias)
}

func scanLogExportRow(rows *sqlx.Rows) (map[string]interface{}, error) {
	row := make(map[string]interface{})
	if err := rows.MapScan(row); err != nil {
		return nil, err
	}
	for key, value := range row {
		if bytesValue, ok := value.([]byte); ok {
			row[key] = string(bytesValue)
		}
	}
	return row, nil
}

func formatLogExportTimestamp(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}

func formatLogExportNumber(value interface{}) string {
	switch v := value.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 64)
	default:
		return strconv.FormatInt(toInt64(value), 10)
	}
}

func formatBoolCN(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func logTypeLabel(value int64) string {
	labels := map[int64]string{
		1: "充值",
		2: "消费",
		3: "管理",
		4: "系统",
		5: "错误",
		6: "退款",
	}
	if label, ok := labels[value]; ok {
		return label
	}
	if value == 0 {
		return "未知"
	}
	return strconv.FormatInt(value, 10)
}

func parseLogOther(raw string) map[string]interface{} {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return map[string]interface{}{}
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return map[string]interface{}{}
	}
	return decoded
}

func otherValue(other map[string]interface{}, key string) string {
	if other == nil {
		return ""
	}
	value, ok := other[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}
