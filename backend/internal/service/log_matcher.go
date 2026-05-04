package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const logMatchDefaultQuotaPerUSD = 500000.0

var (
	logMatchExportNameRE = regexp.MustCompile(`(?i)^newapi_logs_(.+?)_\d{8}_\d{6}\.csv$`)
	logMatchModePrefixRE = regexp.MustCompile(`「[^」]*」`)
)

// LogMatchUploadedFile is one uploaded upstream CSV file.
type LogMatchUploadedFile struct {
	Name string
	Data []byte
}

// LogMatchUpstreamRule maps a local channel alias to an upstream export file.
type LogMatchUpstreamRule struct {
	Aliases        []string `json:"aliases"`
	CostMultiplier float64  `json:"cost_multiplier"`
}

// LogMatchConfig controls local/upstream matching.
type LogMatchConfig struct {
	LocalHost           string                          `json:"local_host"`
	QuotaPerUnit        float64                         `json:"quota_per_unit"`
	TimeWindowSeconds   int                             `json:"time_window_seconds"`
	Upstreams           map[string]LogMatchUpstreamRule `json:"upstreams"`
	ModelSuffixPatterns []string                        `json:"model_suffix_patterns"`
}

// LogMatchAnalyzeOptions controls one analysis run.
type LogMatchAnalyzeOptions struct {
	StartTime  int64
	EndTime    int64
	MaxRows    int
	ConfigJSON string
}

// LogMatchRow is the normalized shape used for local and upstream rows.
type LogMatchRow struct {
	SourceHost       string  `json:"source_host"`
	SourceFile       string  `json:"source_file,omitempty"`
	RowNumber        int     `json:"row_number"`
	CreatedAt        int64   `json:"created_at"`
	TimeText         string  `json:"time_text"`
	Username         string  `json:"username"`
	TokenName        string  `json:"token_name"`
	Group            string  `json:"group"`
	ModelName        string  `json:"model_name"`
	NormalizedModel  string  `json:"normalized_model"`
	ChannelID        int64   `json:"channel_id"`
	ChannelName      string  `json:"channel_name"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	Quota            int64   `json:"quota"`
	CostUSD          float64 `json:"cost_usd"`
	RequestID        string  `json:"request_id"`
	TargetHost       string  `json:"target_host,omitempty"`
	TargetReason     string  `json:"target_reason,omitempty"`
}

// LogMatchRecord is one local request plus its match status.
type LogMatchRecord struct {
	Status            string   `json:"status"`
	Reason            string   `json:"reason"`
	CandidateCount    int      `json:"candidate_count"`
	TimeDiffSeconds   *int64   `json:"time_diff_seconds,omitempty"`
	TargetHost        string   `json:"target_host"`
	UpstreamHost      string   `json:"upstream_host,omitempty"`
	LocalTime         string   `json:"local_time"`
	UpstreamTime      string   `json:"upstream_time,omitempty"`
	LocalRequestID    string   `json:"local_request_id"`
	UpstreamRequestID string   `json:"upstream_request_id,omitempty"`
	LocalModel        string   `json:"local_model"`
	NormalizedModel   string   `json:"normalized_model"`
	UpstreamModel     string   `json:"upstream_model,omitempty"`
	LocalChannel      string   `json:"local_channel"`
	LocalGroup        string   `json:"local_group"`
	LocalUsername     string   `json:"local_username"`
	LocalTokenName    string   `json:"local_token_name"`
	PromptTokens      int64    `json:"prompt_tokens"`
	CompletionTokens  int64    `json:"completion_tokens"`
	TotalTokens       int64    `json:"total_tokens"`
	LocalRevenue      float64  `json:"local_revenue"`
	UpstreamCost      float64  `json:"upstream_cost"`
	Gross             *float64 `json:"gross,omitempty"`
	IsPerCall         bool     `json:"is_per_call"`
}

// LogMatchByHostSummary summarizes one upstream.
type LogMatchByHostSummary struct {
	LocalRows             int     `json:"local_rows"`
	UpstreamRows          int     `json:"upstream_rows"`
	MatchedRows           int     `json:"matched_rows"`
	AmbiguousRows         int     `json:"ambiguous_rows"`
	UnmatchedRows         int     `json:"unmatched_rows"`
	LocalRevenue          float64 `json:"local_revenue"`
	UpstreamCost          float64 `json:"upstream_cost"`
	Gross                 float64 `json:"gross"`
	MatchedLocalRevenue   float64 `json:"matched_local_revenue"`
	MatchedUpstreamCost   float64 `json:"matched_upstream_cost"`
	MatchedGross          float64 `json:"matched_gross"`
	PerCallRows           int     `json:"per_call_rows"`
	PerCallCurrentAvg     float64 `json:"per_call_current_avg"`
	PerCallBreakEvenPrice float64 `json:"per_call_break_even_price"`
}

// LogMatchSummary is the aggregate result.
type LogMatchSummary struct {
	LocalHost           string                           `json:"local_host"`
	LocalRows           int                              `json:"local_rows"`
	UpstreamRows        int                              `json:"upstream_rows"`
	MatchedRows         int                              `json:"matched_rows"`
	AmbiguousRows       int                              `json:"ambiguous_rows"`
	UnmatchedRows       int                              `json:"unmatched_rows"`
	UnusedUpstreamRows  int                              `json:"unused_upstream_rows"`
	LocalRevenue        float64                          `json:"local_revenue"`
	UpstreamCost        float64                          `json:"upstream_cost"`
	Gross               float64                          `json:"gross"`
	MatchedLocalRevenue float64                          `json:"matched_local_revenue"`
	MatchedUpstreamCost float64                          `json:"matched_upstream_cost"`
	MatchedGross        float64                          `json:"matched_gross"`
	ByHost              map[string]LogMatchByHostSummary `json:"by_host"`
}

// LogMatchAnalyzeResult is returned to the UI.
type LogMatchAnalyzeResult struct {
	Summary           LogMatchSummary          `json:"summary"`
	Records           []LogMatchRecord         `json:"records"`
	UploadedFiles     []map[string]interface{} `json:"uploaded_files"`
	Config            LogMatchConfig           `json:"config"`
	GeneratedAt       int64                    `json:"generated_at"`
	TimeWindowSeconds int                      `json:"time_window_seconds"`
}

type logMatchCandidateKey struct {
	Prompt     int64
	Completion int64
	Total      int64
}

type logMatchResult struct {
	Status          string
	Local           *LogMatchRow
	Upstream        *LogMatchRow
	Reason          string
	CandidateCount  int
	TimeDiffSeconds *int64
}

// LogMatcherService handles uploaded upstream CSV matching against local DB logs.
type LogMatcherService struct {
	analytics *LogAnalyticsService
}

// NewLogMatcherService creates a matcher service.
func NewLogMatcherService() *LogMatcherService {
	return &LogMatcherService{analytics: NewLogAnalyticsService()}
}

// DefaultLogMatchConfig returns the built-in matching rules.
func DefaultLogMatchConfig() LogMatchConfig {
	return LogMatchConfig{
		LocalHost:         "newapi.youkies.space",
		QuotaPerUnit:      logMatchDefaultQuotaPerUSD,
		TimeWindowSeconds: 120,
		Upstreams: map[string]LogMatchUpstreamRule{
			"us.llmgate.io": {
				Aliases:        []string{"llmgate"},
				CostMultiplier: 1,
			},
			"www.omnai.xyz": {
				Aliases:        []string{"ominiai", "omnai", "omni"},
				CostMultiplier: 1,
			},
			"api.opusclaw.me": {
				Aliases:        []string{"opus", "opusclaw"},
				CostMultiplier: 0.1,
			},
			"api.guicore.com": {
				Aliases:        []string{"guicore", "cpa", "gui"},
				CostMultiplier: 1,
			},
		},
		ModelSuffixPatterns: []string{
			`-渠道\d+$`,
			`-nothinking$`,
			`-backup$`,
			`-standby$`,
			`-备用$`,
			`-windsurf$`,
			`-kiro$`,
			`-cli$`,
		},
	}
}

// Analyze matches uploaded upstream CSV files against local DB logs.
func (s *LogMatcherService) Analyze(ctx context.Context, files []LogMatchUploadedFile, opts LogMatchAnalyzeOptions) (*LogMatchAnalyzeResult, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("at least one upstream CSV file is required")
	}
	if opts.StartTime <= 0 || opts.EndTime <= 0 {
		opts.StartTime, opts.EndTime = DefaultCostRange()
	}
	if opts.EndTime < opts.StartTime {
		return nil, fmt.Errorf("end_time must be greater than or equal to start_time")
	}

	config, err := mergeLogMatchConfig(opts.ConfigJSON)
	if err != nil {
		return nil, err
	}
	if config.TimeWindowSeconds <= 0 {
		config.TimeWindowSeconds = 120
	}
	if config.QuotaPerUnit <= 0 {
		config.QuotaPerUnit = logMatchDefaultQuotaPerUSD
	}

	localRows, err := s.loadLocalRows(ctx, opts, config)
	if err != nil {
		return nil, err
	}
	assignLogMatchTargets(localRows, config)

	upstreamRowsByHost := map[string][]*LogMatchRow{}
	uploadedFiles := make([]map[string]interface{}, 0, len(files))
	for _, file := range files {
		host := detectLogMatchHost(file.Name, config)
		rows, err := parseLogMatchCSV(file.Name, host, file.Data, config)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file.Name, err)
		}
		upstreamRowsByHost[host] = append(upstreamRowsByHost[host], rows...)
		uploadedFiles = append(uploadedFiles, map[string]interface{}{
			"name": file.Name,
			"host": host,
			"rows": len(rows),
		})
	}

	results, used := matchLogRows(localRows, upstreamRowsByHost, config)
	summary := summarizeLogMatches(config, localRows, upstreamRowsByHost, results, used)
	records := make([]LogMatchRecord, 0, len(results))
	for _, result := range results {
		records = append(records, logMatchRecordFromResult(result, config))
	}

	return &LogMatchAnalyzeResult{
		Summary:           summary,
		Records:           records,
		UploadedFiles:     uploadedFiles,
		Config:            config,
		GeneratedAt:       time.Now().Unix(),
		TimeWindowSeconds: config.TimeWindowSeconds,
	}, nil
}

func mergeLogMatchConfig(raw string) (LogMatchConfig, error) {
	config := DefaultLogMatchConfig()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return config, nil
	}

	var patch LogMatchConfig
	if err := json.Unmarshal([]byte(raw), &patch); err != nil {
		return config, fmt.Errorf("invalid config_json: %w", err)
	}
	if strings.TrimSpace(patch.LocalHost) != "" {
		config.LocalHost = strings.TrimSpace(patch.LocalHost)
	}
	if patch.QuotaPerUnit > 0 {
		config.QuotaPerUnit = patch.QuotaPerUnit
	}
	if patch.TimeWindowSeconds > 0 {
		config.TimeWindowSeconds = patch.TimeWindowSeconds
	}
	if len(patch.Upstreams) > 0 {
		for host, rule := range patch.Upstreams {
			host = strings.TrimSpace(host)
			if host == "" {
				continue
			}
			if rule.CostMultiplier <= 0 {
				rule.CostMultiplier = 1
			}
			config.Upstreams[host] = rule
		}
	}
	if len(patch.ModelSuffixPatterns) > 0 {
		config.ModelSuffixPatterns = patch.ModelSuffixPatterns
	}
	return config, nil
}

func (s *LogMatcherService) loadLocalRows(ctx context.Context, opts LogMatchAnalyzeOptions, config LogMatchConfig) ([]*LogMatchRow, error) {
	maxRows := opts.MaxRows
	if maxRows <= 0 {
		maxRows = 50000
	}
	exportOpts := LogExportOptions{
		StartTime:    opts.StartTime,
		EndTime:      opts.EndTime,
		Type:         2,
		QuotaPerUnit: int64(config.QuotaPerUnit),
		MaxRows:      maxRows,
	}
	rows, err := s.analytics.OpenLogExport(ctx, exportOpts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []*LogMatchRow{}
	rowNumber := 0
	for rows.Next() {
		rowNumber++
		row, err := scanLogExportRow(rows)
		if err != nil {
			return nil, err
		}
		prompt := toInt64(row["prompt_tokens"])
		completion := toInt64(row["completion_tokens"])
		total := prompt + completion
		quota := toInt64(row["quota"])
		modelName := toString(row["model_name"])
		createdAt := toInt64(row["created_at"])
		result = append(result, &LogMatchRow{
			SourceHost:       config.LocalHost,
			RowNumber:        rowNumber,
			CreatedAt:        createdAt,
			TimeText:         formatLogMatchTime(createdAt),
			Username:         toString(row["username"]),
			TokenName:        toString(row["token_name"]),
			Group:            toString(row["group_name"]),
			ModelName:        modelName,
			NormalizedModel:  normalizeLogMatchModel(modelName, config),
			ChannelID:        toInt64(row["channel_id"]),
			ChannelName:      toString(row["channel_name"]),
			PromptTokens:     prompt,
			CompletionTokens: completion,
			TotalTokens:      total,
			Quota:            quota,
			CostUSD:          float64(quota) / config.QuotaPerUnit,
			RequestID:        toString(row["request_id"]),
		})
	}
	return result, rows.Err()
}

func parseLogMatchCSV(fileName, host string, data []byte, config LogMatchConfig) ([]*LogMatchRow, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		if err == io.EOF {
			return []*LogMatchRow{}, nil
		}
		return nil, err
	}
	index := buildLogMatchHeaderIndex(header)
	rows := []*LogMatchRow{}
	rowNumber := 1
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rowNumber++
		createdAt := parseLogMatchCSVTime(csvValue(record, index, "time"))
		if createdAt <= 0 {
			continue
		}
		quota := parseLogMatchInt(csvValue(record, index, "quota"))
		cost := parseLogMatchFloat(csvValue(record, index, "cost_usd"))
		if cost == 0 && quota != 0 {
			cost = float64(quota) / config.QuotaPerUnit
		}
		if cost == 0 {
			continue
		}
		prompt := parseLogMatchInt(csvValue(record, index, "prompt_tokens"))
		completion := parseLogMatchInt(csvValue(record, index, "completion_tokens"))
		total := parseLogMatchInt(csvValue(record, index, "total_tokens"))
		if total == 0 {
			total = prompt + completion
		}
		modelName := csvValue(record, index, "model_name")
		rows = append(rows, &LogMatchRow{
			SourceHost:       host,
			SourceFile:       fileName,
			RowNumber:        rowNumber,
			CreatedAt:        createdAt,
			TimeText:         formatLogMatchTime(createdAt),
			Username:         csvValue(record, index, "username"),
			TokenName:        csvValue(record, index, "token_name"),
			Group:            csvValue(record, index, "group"),
			ModelName:        modelName,
			NormalizedModel:  normalizeLogMatchModel(modelName, config),
			ChannelID:        parseLogMatchInt(csvValue(record, index, "channel_id")),
			ChannelName:      csvValue(record, index, "channel_name"),
			PromptTokens:     prompt,
			CompletionTokens: completion,
			TotalTokens:      total,
			Quota:            quota,
			CostUSD:          cost,
			RequestID:        csvValue(record, index, "request_id"),
		})
	}
	return rows, nil
}

func buildLogMatchHeaderIndex(header []string) map[string]int {
	aliases := map[string][]string{
		"time":              {"时间", "time", "created_at"},
		"username":          {"用户名", "username", "user_name"},
		"token_name":        {"令牌名称", "token_name", "token"},
		"group":             {"分组", "group", "group_name"},
		"model_name":        {"模型名称", "model_name", "model"},
		"channel_id":        {"渠道ID", "渠道 ID", "channel_id"},
		"channel_name":      {"渠道名称", "channel_name"},
		"prompt_tokens":     {"输入Tokens", "输入 Tokens", "prompt_tokens"},
		"completion_tokens": {"输出Tokens", "输出 Tokens", "completion_tokens"},
		"total_tokens":      {"总Tokens", "总 Tokens", "total_tokens"},
		"quota":             {"Quota", "quota"},
		"cost_usd":          {"费用(USD)", "cost_usd", "_cost_usd"},
		"request_id":        {"Request ID", "request_id"},
	}
	normalized := map[string]int{}
	for idx, name := range header {
		normalized[strings.ToLower(strings.TrimSpace(name))] = idx
	}
	result := map[string]int{}
	for key, names := range aliases {
		for _, name := range names {
			if idx, ok := normalized[strings.ToLower(strings.TrimSpace(name))]; ok {
				result[key] = idx
				break
			}
		}
	}
	// Userscript fallback positions.
	fallbacks := map[string]int{
		"time":              0,
		"username":          1,
		"token_name":        2,
		"group":             3,
		"model_name":        4,
		"channel_id":        5,
		"channel_name":      6,
		"prompt_tokens":     8,
		"completion_tokens": 9,
		"total_tokens":      10,
		"quota":             11,
		"cost_usd":          12,
		"request_id":        18,
	}
	for key, idx := range fallbacks {
		if _, ok := result[key]; !ok {
			result[key] = idx
		}
	}
	return result
}

func csvValue(row []string, index map[string]int, key string) string {
	idx, ok := index[key]
	if !ok || idx < 0 || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

func parseLogMatchCSVTime(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil && unix > 0 {
		return unix
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed.Unix()
		}
	}
	return 0
}

func parseLogMatchInt(value string) int64 {
	value = strings.TrimSpace(strings.ReplaceAll(value, ",", ""))
	if value == "" {
		return 0
	}
	if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
		return parsed
	}
	floatValue, _ := strconv.ParseFloat(value, 64)
	return int64(floatValue)
}

func parseLogMatchFloat(value string) float64 {
	value = strings.TrimSpace(strings.ReplaceAll(value, ",", ""))
	if value == "" {
		return 0
	}
	parsed, _ := strconv.ParseFloat(value, 64)
	return parsed
}

func detectLogMatchHost(fileName string, config LogMatchConfig) string {
	base := filepath.Base(fileName)
	detected := ""
	if match := logMatchExportNameRE.FindStringSubmatch(base); len(match) == 2 {
		detected = match[1]
	} else {
		ext := filepath.Ext(base)
		detected = strings.TrimSuffix(base, ext)
	}
	lowerDetected := strings.ToLower(strings.TrimSpace(detected))
	for host, rule := range config.Upstreams {
		lowerHost := strings.ToLower(host)
		if lowerDetected == lowerHost || strings.Contains(lowerDetected, lowerHost) {
			return host
		}
		if len(lowerDetected) >= 5 && strings.Contains(lowerHost, lowerDetected) {
			return host
		}
		for _, alias := range rule.Aliases {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if alias != "" && strings.Contains(lowerDetected, alias) {
				return host
			}
		}
	}
	return detected
}

func assignLogMatchTargets(rows []*LogMatchRow, config LogMatchConfig) {
	type aliasItem struct {
		alias string
		host  string
	}
	items := []aliasItem{}
	for host, rule := range config.Upstreams {
		for _, alias := range rule.Aliases {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if alias != "" {
				items = append(items, aliasItem{alias: alias, host: host})
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return len(items[i].alias) > len(items[j].alias) })
	for _, row := range rows {
		haystack := strings.ToLower(row.ChannelName)
		for _, item := range items {
			if strings.Contains(haystack, item.alias) {
				row.TargetHost = item.host
				row.TargetReason = "alias:" + item.alias
				break
			}
		}
	}
}

func normalizeLogMatchModel(value string, config LogMatchConfig) string {
	text := strings.ToLower(strings.TrimSpace(value))
	text = logMatchModePrefixRE.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "_", "-")
	text = strings.ReplaceAll(text, " ", "")
	text = strings.Trim(text, "-_/ ")
	for {
		changed := false
		for _, pattern := range config.ModelSuffixPatterns {
			re, err := regexp.Compile("(?i)" + pattern)
			if err != nil {
				continue
			}
			next := strings.Trim(re.ReplaceAllString(text, ""), "-_/ ")
			if next != text {
				text = next
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return text
}

func matchLogRows(localRows []*LogMatchRow, upstreamRowsByHost map[string][]*LogMatchRow, config LogMatchConfig) ([]logMatchResult, map[string]bool) {
	index := map[string]map[logMatchCandidateKey][]*LogMatchRow{}
	for host, rows := range upstreamRowsByHost {
		hostIndex := map[logMatchCandidateKey][]*LogMatchRow{}
		for _, row := range rows {
			key := logMatchCandidateKey{Prompt: row.PromptTokens, Completion: row.CompletionTokens, Total: row.TotalTokens}
			hostIndex[key] = append(hostIndex[key], row)
		}
		index[host] = hostIndex
	}

	sort.Slice(localRows, func(i, j int) bool {
		if localRows[i].CreatedAt == localRows[j].CreatedAt {
			return localRows[i].RowNumber < localRows[j].RowNumber
		}
		return localRows[i].CreatedAt < localRows[j].CreatedAt
	})

	used := map[string]bool{}
	results := make([]logMatchResult, 0, len(localRows))
	window := int64(config.TimeWindowSeconds)
	for _, local := range localRows {
		if local.TargetHost == "" {
			results = append(results, logMatchResult{Status: "unmatched", Local: local, Reason: "no_upstream_alias"})
			continue
		}
		key := logMatchCandidateKey{Prompt: local.PromptTokens, Completion: local.CompletionTokens, Total: local.TotalTokens}
		candidates := []*LogMatchRow{}
		for _, row := range index[local.TargetHost][key] {
			if used[logMatchUsedKey(row)] {
				continue
			}
			if absInt64(row.CreatedAt-local.CreatedAt) <= window {
				candidates = append(candidates, row)
			}
		}
		modelCandidates := []*LogMatchRow{}
		for _, row := range candidates {
			if logMatchModelsCompatible(local.NormalizedModel, row.NormalizedModel) {
				modelCandidates = append(modelCandidates, row)
			}
		}
		if len(modelCandidates) > 0 {
			candidates = modelCandidates
		}
		if len(candidates) == 0 {
			results = append(results, logMatchResult{Status: "unmatched", Local: local, Reason: "no_token_time_model_candidate"})
			continue
		}
		sort.Slice(candidates, func(i, j int) bool {
			leftDiff := absInt64(candidates[i].CreatedAt - local.CreatedAt)
			rightDiff := absInt64(candidates[j].CreatedAt - local.CreatedAt)
			if leftDiff != rightDiff {
				return leftDiff < rightDiff
			}
			leftExact := candidates[i].NormalizedModel == local.NormalizedModel
			rightExact := candidates[j].NormalizedModel == local.NormalizedModel
			if leftExact != rightExact {
				return leftExact
			}
			return candidates[i].RowNumber < candidates[j].RowNumber
		})
		if len(candidates) > 1 && !logMatchUniqueBest(local, candidates) {
			results = append(results, logMatchResult{Status: "ambiguous", Local: local, Reason: "multiple_candidates", CandidateCount: len(candidates)})
			continue
		}
		upstream := candidates[0]
		used[logMatchUsedKey(upstream)] = true
		diff := absInt64(upstream.CreatedAt - local.CreatedAt)
		results = append(results, logMatchResult{
			Status:          "matched",
			Local:           local,
			Upstream:        upstream,
			Reason:          "token_time_model",
			CandidateCount:  len(candidates),
			TimeDiffSeconds: &diff,
		})
	}
	return results, used
}

func logMatchUniqueBest(local *LogMatchRow, candidates []*LogMatchRow) bool {
	if len(candidates) < 2 {
		return true
	}
	bestDiff := absInt64(candidates[0].CreatedAt - local.CreatedAt)
	secondDiff := absInt64(candidates[1].CreatedAt - local.CreatedAt)
	if bestDiff != secondDiff {
		return bestDiff < secondDiff
	}
	bestExact := candidates[0].NormalizedModel == local.NormalizedModel
	secondExact := candidates[1].NormalizedModel == local.NormalizedModel
	return bestExact && !secondExact
}

func logMatchModelsCompatible(localModel, upstreamModel string) bool {
	if localModel == "" || upstreamModel == "" {
		return true
	}
	return localModel == upstreamModel ||
		strings.HasPrefix(localModel, upstreamModel+"-") ||
		strings.HasPrefix(upstreamModel, localModel+"-")
}

func summarizeLogMatches(config LogMatchConfig, localRows []*LogMatchRow, upstreamRowsByHost map[string][]*LogMatchRow, results []logMatchResult, used map[string]bool) LogMatchSummary {
	byHost := map[string]LogMatchByHostSummary{}
	for host := range config.Upstreams {
		localMapped := []*LogMatchRow{}
		for _, row := range localRows {
			if row.TargetHost == host {
				localMapped = append(localMapped, row)
			}
		}
		upstreamRows := upstreamRowsByHost[host]
		item := LogMatchByHostSummary{
			LocalRows:    len(localMapped),
			UpstreamRows: len(upstreamRows),
		}
		for _, row := range localMapped {
			item.LocalRevenue += row.CostUSD
			if logMatchIsPerCall(row) {
				item.PerCallRows++
				item.PerCallCurrentAvg += row.CostUSD
			}
		}
		if item.PerCallRows > 0 {
			item.PerCallCurrentAvg = item.PerCallCurrentAvg / float64(item.PerCallRows)
		}
		for _, row := range upstreamRows {
			item.UpstreamCost += logMatchRealCost(row, config)
		}
		for _, result := range results {
			if result.Local.TargetHost != host {
				continue
			}
			switch result.Status {
			case "matched":
				item.MatchedRows++
				item.MatchedLocalRevenue += result.Local.CostUSD
				if result.Upstream != nil {
					item.MatchedUpstreamCost += logMatchRealCost(result.Upstream, config)
				}
			case "ambiguous":
				item.AmbiguousRows++
			case "unmatched":
				item.UnmatchedRows++
			}
		}
		item.Gross = item.LocalRevenue - item.UpstreamCost
		item.MatchedGross = item.MatchedLocalRevenue - item.MatchedUpstreamCost
		if item.PerCallRows > 0 {
			nonPerCallRevenue := 0.0
			for _, row := range localMapped {
				if !logMatchIsPerCall(row) {
					nonPerCallRevenue += row.CostUSD
				}
			}
			item.PerCallBreakEvenPrice = (item.UpstreamCost - nonPerCallRevenue) / float64(item.PerCallRows)
		}
		byHost[host] = roundLogMatchByHost(item)
	}

	summary := LogMatchSummary{
		LocalHost: config.LocalHost,
		LocalRows: len(localRows),
		ByHost:    byHost,
	}
	for _, row := range localRows {
		summary.LocalRevenue += row.CostUSD
	}
	for host, rows := range upstreamRowsByHost {
		for _, row := range rows {
			summary.UpstreamRows++
			summary.UpstreamCost += logMatchRealCost(row, config)
			if !used[logMatchUsedKey(row)] {
				_ = host
				summary.UnusedUpstreamRows++
			}
		}
	}
	for _, result := range results {
		switch result.Status {
		case "matched":
			summary.MatchedRows++
			summary.MatchedLocalRevenue += result.Local.CostUSD
			if result.Upstream != nil {
				summary.MatchedUpstreamCost += logMatchRealCost(result.Upstream, config)
			}
		case "ambiguous":
			summary.AmbiguousRows++
		case "unmatched":
			summary.UnmatchedRows++
		}
	}
	summary.Gross = summary.LocalRevenue - summary.UpstreamCost
	summary.MatchedGross = summary.MatchedLocalRevenue - summary.MatchedUpstreamCost
	return roundLogMatchSummary(summary)
}

func logMatchRecordFromResult(result logMatchResult, config LogMatchConfig) LogMatchRecord {
	record := LogMatchRecord{
		Status:           result.Status,
		Reason:           result.Reason,
		CandidateCount:   result.CandidateCount,
		TimeDiffSeconds:  result.TimeDiffSeconds,
		TargetHost:       result.Local.TargetHost,
		LocalTime:        result.Local.TimeText,
		LocalRequestID:   result.Local.RequestID,
		LocalModel:       result.Local.ModelName,
		NormalizedModel:  result.Local.NormalizedModel,
		LocalChannel:     result.Local.ChannelName,
		LocalGroup:       result.Local.Group,
		LocalUsername:    result.Local.Username,
		LocalTokenName:   result.Local.TokenName,
		PromptTokens:     result.Local.PromptTokens,
		CompletionTokens: result.Local.CompletionTokens,
		TotalTokens:      result.Local.TotalTokens,
		LocalRevenue:     roundLogMatchMoney(result.Local.CostUSD),
		IsPerCall:        logMatchIsPerCall(result.Local),
	}
	if result.Upstream != nil {
		record.UpstreamHost = result.Upstream.SourceHost
		record.UpstreamTime = result.Upstream.TimeText
		record.UpstreamRequestID = result.Upstream.RequestID
		record.UpstreamModel = result.Upstream.ModelName
		record.UpstreamCost = roundLogMatchMoney(logMatchRealCost(result.Upstream, config))
		gross := roundLogMatchMoney(result.Local.CostUSD - logMatchRealCost(result.Upstream, config))
		record.Gross = &gross
	}
	return record
}

func logMatchRealCost(row *LogMatchRow, config LogMatchConfig) float64 {
	multiplier := 1.0
	if rule, ok := config.Upstreams[row.SourceHost]; ok && rule.CostMultiplier > 0 {
		multiplier = rule.CostMultiplier
	}
	return row.CostUSD * multiplier
}

func logMatchIsPerCall(row *LogMatchRow) bool {
	for _, value := range []string{row.ModelName, row.Group, row.ChannelName, row.TokenName} {
		if strings.Contains(value, "按次") {
			return true
		}
	}
	return false
}

func logMatchUsedKey(row *LogMatchRow) string {
	return fmt.Sprintf("%s:%s:%d", row.SourceHost, row.SourceFile, row.RowNumber)
}

func formatLogMatchTime(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func roundLogMatchMoney(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}

func roundLogMatchByHost(item LogMatchByHostSummary) LogMatchByHostSummary {
	item.LocalRevenue = roundLogMatchMoney(item.LocalRevenue)
	item.UpstreamCost = roundLogMatchMoney(item.UpstreamCost)
	item.Gross = roundLogMatchMoney(item.Gross)
	item.MatchedLocalRevenue = roundLogMatchMoney(item.MatchedLocalRevenue)
	item.MatchedUpstreamCost = roundLogMatchMoney(item.MatchedUpstreamCost)
	item.MatchedGross = roundLogMatchMoney(item.MatchedGross)
	item.PerCallCurrentAvg = roundLogMatchMoney(item.PerCallCurrentAvg)
	item.PerCallBreakEvenPrice = roundLogMatchMoney(item.PerCallBreakEvenPrice)
	return item
}

func roundLogMatchSummary(summary LogMatchSummary) LogMatchSummary {
	summary.LocalRevenue = roundLogMatchMoney(summary.LocalRevenue)
	summary.UpstreamCost = roundLogMatchMoney(summary.UpstreamCost)
	summary.Gross = roundLogMatchMoney(summary.Gross)
	summary.MatchedLocalRevenue = roundLogMatchMoney(summary.MatchedLocalRevenue)
	summary.MatchedUpstreamCost = roundLogMatchMoney(summary.MatchedUpstreamCost)
	summary.MatchedGross = roundLogMatchMoney(summary.MatchedGross)
	return summary
}
