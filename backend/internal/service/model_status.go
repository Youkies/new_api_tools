package service

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/new-api-tools/backend/internal/cache"
	"github.com/new-api-tools/backend/internal/database"
)

// Constants for model status
var (
	AvailableTimeWindows = []string{"1h", "6h", "12h", "24h"}
	DefaultTimeWindow    = "24h"
	AvailableThemes      = []string{
		"daylight", "obsidian", "minimal", "neon", "forest", "ocean", "terminal",
		"cupertino", "material", "openai", "anthropic", "vercel", "linear",
		"stripe", "github", "discord", "tesla",
	}
	DefaultTheme = "daylight"
	// LegacyThemeMap maps old theme names to valid ones
	LegacyThemeMap = map[string]string{
		"light":  "daylight",
		"dark":   "obsidian",
		"system": "daylight",
	}
	AvailableRefreshIntervals = []int{0, 30, 60, 120, 300}
	AvailableSortModes        = []string{"default", "availability", "custom"}
)

// Time window slot configurations: {totalSeconds, numSlots, slotSeconds}
// Must match Python backend and frontend TIME_WINDOWS exactly
type timeWindowConfig struct {
	totalSeconds int64
	numSlots     int
	slotSeconds  int64
}

var timeWindowConfigs = map[string]timeWindowConfig{
	"1h":  {3600, 60, 60},    // 1 hour, 60 slots, 1 minute each
	"6h":  {21600, 24, 900},  // 6 hours, 24 slots, 15 minutes each
	"12h": {43200, 24, 1800}, // 12 hours, 24 slots, 30 minutes each
	"24h": {86400, 24, 3600}, // 24 hours, 24 slots, 1 hour each
}

// getStatusColor determines status color based on success rate (matches Python backend)
func getStatusColor(successRate float64, totalRequests int64) string {
	if totalRequests == 0 {
		return "green" // No requests = no issues
	}
	if successRate >= 95 {
		return "green"
	} else if successRate >= 80 {
		return "yellow"
	}
	return "red"
}

// roundRate rounds a float to 2 decimal places
func roundRate(rate float64) float64 {
	return math.Round(rate*100) / 100
}

// ModelStatusService handles model availability monitoring
type ModelStatusService struct {
	db *database.Manager
}

// NewModelStatusService creates a new ModelStatusService
func NewModelStatusService() *ModelStatusService {
	return &ModelStatusService{db: database.Get()}
}

// GetAvailableModels returns all models with 24h request counts
func (s *ModelStatusService) GetAvailableModels() ([]map[string]interface{}, error) {
	cm := cache.Get()
	var cached []map[string]interface{}
	found, _ := cm.GetJSON("model_status:available_models", &cached)
	if found {
		return cached, nil
	}

	startTime := time.Now().Unix() - 86400

	query := s.db.RebindQuery(`
		SELECT model_name, COUNT(*) as request_count_24h
		FROM logs
		WHERE type IN (2, 5) AND model_name != '' AND created_at >= ?
		GROUP BY model_name
		ORDER BY request_count_24h DESC`)

	rows, err := s.db.QueryWithTimeout(20*time.Second, query, startTime)
	if err != nil {
		return nil, err
	}

	cm.Set("model_status:available_models", rows, 5*time.Minute)
	return rows, nil
}

// GetModelStatus returns status for a specific model
// Uses a single GROUP BY FLOOR query (matches Python backend optimization)
func (s *ModelStatusService) GetModelStatus(modelName, window string, useCache bool) (map[string]interface{}, error) {
	results, err := s.GetMultipleModelsStatus([]string{modelName}, window, useCache)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("model status not found: %s", modelName)
	}
	return results[0], nil
}

func buildModelStatusResult(modelName, window string, rows []map[string]interface{}, twConfig timeWindowConfig, startTime int64) map[string]interface{} {
	numSlots := twConfig.numSlots
	slotSeconds := twConfig.slotSeconds

	type slotInfo struct {
		total   int64
		success int64
		failure int64
		empty   int64
	}
	slotMap := make(map[int64]*slotInfo, numSlots)

	for _, row := range rows {
		idx := toInt64(row["slot_idx"])
		if idx >= 0 && idx < int64(numSlots) {
			slotMap[idx] = &slotInfo{
				total:   toInt64(row["total"]),
				success: toInt64(row["success"]),
				failure: toInt64(row["failure"]),
				empty:   toInt64(row["empty_count"]),
			}
		}
	}

	// Build slot_data list with status colors
	slotData := make([]map[string]interface{}, 0, numSlots)
	totalReqs := int64(0)
	totalSuccess := int64(0)
	totalFailure := int64(0)
	totalEmpty := int64(0)

	for i := 0; i < numSlots; i++ {
		slotStart := startTime + int64(i)*slotSeconds
		slotEnd := slotStart + slotSeconds

		si := slotMap[int64(i)]
		slotTotal := int64(0)
		slotSuccess := int64(0)
		slotFailure := int64(0)
		slotEmpty := int64(0)
		if si != nil {
			slotTotal = si.total
			slotSuccess = si.success
			slotFailure = si.failure
			slotEmpty = si.empty
		}

		slotRate := float64(100)
		if slotTotal > 0 {
			slotRate = float64(slotSuccess) / float64(slotTotal) * 100
		}

		slotData = append(slotData, map[string]interface{}{
			"slot":           i,
			"start_time":     slotStart,
			"end_time":       slotEnd,
			"total_requests": slotTotal,
			"success_count":  slotSuccess,
			"failure_count":  slotFailure,
			"empty_count":    slotEmpty,
			"success_rate":   roundRate(slotRate),
			"status":         getStatusColor(slotRate, slotTotal),
		})

		totalReqs += slotTotal
		totalSuccess += slotSuccess
		totalFailure += slotFailure
		totalEmpty += slotEmpty
	}

	overallRate := float64(100)
	if totalReqs > 0 {
		overallRate = float64(totalSuccess) / float64(totalReqs) * 100
	}

	return map[string]interface{}{
		"model_name":     modelName,
		"display_name":   modelName,
		"time_window":    window,
		"total_requests": totalReqs,
		"success_count":  totalSuccess,
		"failure_count":  totalFailure,
		"empty_count":    totalEmpty,
		"success_rate":   roundRate(overallRate),
		"current_status": getStatusColor(overallRate, totalReqs),
		"slot_data":      slotData,
	}
}

// GetMultipleModelsStatus returns status for multiple models
func (s *ModelStatusService) GetMultipleModelsStatus(modelNames []string, window string, useCache bool) ([]map[string]interface{}, error) {
	twConfig, ok := timeWindowConfigs[window]
	if !ok {
		window = DefaultTimeWindow
		twConfig = timeWindowConfigs[DefaultTimeWindow]
	}

	orderedNames := make([]string, 0, len(modelNames))
	seen := map[string]bool{}
	for _, name := range modelNames {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		orderedNames = append(orderedNames, name)
	}

	resultsByModel := map[string]map[string]interface{}{}
	uncached := make([]string, 0, len(orderedNames))
	cm := cache.Get()
	if useCache {
		for _, name := range orderedNames {
			cacheKey := fmt.Sprintf("model_status:%s:%s", name, window)
			var cached map[string]interface{}
			if found, _ := cm.GetJSON(cacheKey, &cached); found {
				resultsByModel[name] = cached
			} else {
				uncached = append(uncached, name)
			}
		}
	} else {
		uncached = append(uncached, orderedNames...)
	}

	if len(uncached) > 0 {
		now := time.Now().Unix()
		startTime := now - twConfig.totalSeconds
		rowsByModel := map[string][]map[string]interface{}{}
		const batchSize = 100

		for start := 0; start < len(uncached); start += batchSize {
			end := start + batchSize
			if end > len(uncached) {
				end = len(uncached)
			}
			batch := uncached[start:end]
			modelPlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")

			// Batch aggregate by model + time slot. This replaces N per-model SQL queries with
			// one query per chunk and keeps the response shape unchanged.
			query := s.db.RebindQuery(fmt.Sprintf(`
				SELECT model_name,
					FLOOR((created_at - %d) / %d) as slot_idx,
					COUNT(*) as total,
					SUM(CASE WHEN type = 2 AND completion_tokens > 0 THEN 1 ELSE 0 END) as success,
					SUM(CASE WHEN type = 5 THEN 1 ELSE 0 END) as failure,
					SUM(CASE WHEN type = 2 AND completion_tokens = 0 THEN 1 ELSE 0 END) as empty_count
				FROM logs
				WHERE model_name IN (%s)
					AND created_at >= ? AND created_at < ?
					AND type IN (2, 5)
				GROUP BY model_name, FLOOR((created_at - %d) / %d)`,
				startTime, twConfig.slotSeconds, modelPlaceholders, startTime, twConfig.slotSeconds))

			args := make([]interface{}, 0, len(batch)+2)
			for _, name := range batch {
				args = append(args, name)
			}
			args = append(args, startTime, now)

			rows, err := s.db.QueryWithTimeout(30*time.Second, query, args...)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				name := toString(row["model_name"])
				if name == "" {
					continue
				}
				rowsByModel[name] = append(rowsByModel[name], row)
			}
		}

		for _, name := range uncached {
			result := buildModelStatusResult(name, window, rowsByModel[name], twConfig, startTime)
			resultsByModel[name] = result
			cm.Set(fmt.Sprintf("model_status:%s:%s", name, window), result, 30*time.Second)
		}
	}

	results := make([]map[string]interface{}, 0, len(orderedNames))
	for _, name := range orderedNames {
		if result, ok := resultsByModel[name]; ok {
			results = append(results, result)
		}
	}

	return results, nil
}

// GetAllModelsStatus returns status for all models that have requests
func (s *ModelStatusService) GetAllModelsStatus(window string, useCache bool) ([]map[string]interface{}, error) {
	models, err := s.GetAvailableModels()
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(models))
	for _, m := range models {
		if name, ok := m["model_name"].(string); ok {
			names = append(names, name)
		}
	}

	return s.GetMultipleModelsStatus(names, window, useCache)
}

// GetTokenGroups 返回令牌分组列表及其关联的模型（基于 abilities 表）
func (s *ModelStatusService) GetTokenGroups() ([]map[string]interface{}, error) {
	cm := cache.Get()
	var cached []map[string]interface{}
	found, _ := cm.GetJSON("model_status:token_groups", &cached)
	if found {
		return cached, nil
	}

	// 从 abilities 表获取分组及其模型列表（abilities 表定义了 group-model-channel 的映射）
	groupCol := s.getGroupCol()
	query := s.db.RebindQuery(fmt.Sprintf(`
		SELECT COALESCE(NULLIF(a.%s, ''), 'default') as group_name,
			COUNT(DISTINCT a.model) as model_count
		FROM abilities a
		INNER JOIN channels c ON c.id = a.channel_id
		WHERE c.status = 1
		GROUP BY COALESCE(NULLIF(a.%s, ''), 'default')
		ORDER BY model_count DESC`, groupCol, groupCol))

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}

	// 为每个分组获取其模型列表
	results := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		groupName := fmt.Sprintf("%v", row["group_name"])

		modelsQuery := s.db.RebindQuery(fmt.Sprintf(`
			SELECT DISTINCT a.model as model_name
			FROM abilities a
			INNER JOIN channels c ON c.id = a.channel_id
			WHERE c.status = 1 AND COALESCE(NULLIF(a.%s, ''), 'default') = ?
			ORDER BY a.model`, groupCol))

		modelRows, err := s.db.Query(modelsQuery, groupName)
		if err != nil {
			continue
		}

		modelNames := make([]string, 0, len(modelRows))
		for _, mr := range modelRows {
			if name, ok := mr["model_name"].(string); ok && name != "" {
				modelNames = append(modelNames, name)
			}
		}

		results = append(results, map[string]interface{}{
			"group_name":  groupName,
			"model_count": row["model_count"],
			"models":      modelNames,
		})
	}

	cm.Set("model_status:token_groups", results, 5*time.Minute)
	return results, nil
}

// getGroupCol 返回正确引用的 group 列名（group 是保留字）
func (s *ModelStatusService) getGroupCol() string {
	if s.db.IsPG {
		return `"group"`
	}
	return "`group`"
}

// Config management via cache

// GetSelectedModels returns selected model names from cache
func (s *ModelStatusService) GetSelectedModels() []string {
	cm := cache.Get()
	var models []string
	found, _ := cm.GetJSON("model_status:selected_models", &models)
	if found {
		return models
	}
	return []string{}
}

// SetSelectedModels saves selected models to cache
func (s *ModelStatusService) SetSelectedModels(models []string) {
	cm := cache.Get()
	cm.Set("model_status:selected_models", models, 0) // no expiry
}

// GetConfig returns all model status config
func (s *ModelStatusService) GetConfig() map[string]interface{} {
	cm := cache.Get()

	var timeWindow string
	found, _ := cm.GetJSON("model_status:time_window", &timeWindow)
	if !found {
		timeWindow = DefaultTimeWindow
	}

	var theme string
	found, _ = cm.GetJSON("model_status:theme", &theme)
	if !found {
		theme = DefaultTheme
	}
	// Map legacy theme names to valid ones
	if mapped, ok := LegacyThemeMap[theme]; ok {
		theme = mapped
	}

	var refreshInterval int
	found, _ = cm.GetJSON("model_status:refresh_interval", &refreshInterval)
	if !found {
		refreshInterval = 60
	}

	var sortMode string
	found, _ = cm.GetJSON("model_status:sort_mode", &sortMode)
	if !found {
		sortMode = "default"
	}

	var customOrder []string
	cm.GetJSON("model_status:custom_order", &customOrder)

	var customGroups []map[string]interface{}
	found, _ = cm.GetJSON("model_status:custom_groups", &customGroups)
	if !found {
		customGroups = []map[string]interface{}{}
	}

	return map[string]interface{}{
		"time_window":      timeWindow,
		"theme":            theme,
		"refresh_interval": refreshInterval,
		"sort_mode":        sortMode,
		"custom_order":     customOrder,
		"selected_models":  s.GetSelectedModels(),
		"custom_groups":    customGroups,
		"site_title":       s.GetSiteTitle(),
	}
}

// SetTimeWindow saves time window to cache
func (s *ModelStatusService) SetTimeWindow(window string) {
	cm := cache.Get()
	cm.Set("model_status:time_window", window, 0)
}

// SetTheme saves theme to cache
func (s *ModelStatusService) SetTheme(theme string) {
	cm := cache.Get()
	cm.Set("model_status:theme", theme, 0)
}

// SetRefreshInterval saves refresh interval to cache
func (s *ModelStatusService) SetRefreshInterval(interval int) {
	cm := cache.Get()
	cm.Set("model_status:refresh_interval", interval, 0)
}

// SetSortMode saves sort mode to cache
func (s *ModelStatusService) SetSortMode(mode string) {
	cm := cache.Get()
	cm.Set("model_status:sort_mode", mode, 0)
}

// SetCustomOrder saves custom order to cache
func (s *ModelStatusService) SetCustomOrder(order []string) {
	cm := cache.Get()
	cm.Set("model_status:custom_order", order, 0)
}

// GetCustomGroups returns custom model groups from cache
func (s *ModelStatusService) GetCustomGroups() []map[string]interface{} {
	cm := cache.Get()
	var groups []map[string]interface{}
	found, _ := cm.GetJSON("model_status:custom_groups", &groups)
	if found {
		return groups
	}
	return []map[string]interface{}{}
}

// SetCustomGroups saves custom model groups to cache
func (s *ModelStatusService) SetCustomGroups(groups []map[string]interface{}) {
	cm := cache.Get()
	cm.Set("model_status:custom_groups", groups, 0) // no expiry
}

// GetSiteTitle returns the custom site title
func (s *ModelStatusService) GetSiteTitle() string {
	cm := cache.Get()
	var title string
	found, _ := cm.GetJSON("model_status:site_title", &title)
	if found {
		return title
	}
	return ""
}

// SetSiteTitle saves the custom site title
func (s *ModelStatusService) SetSiteTitle(title string) {
	cm := cache.Get()
	cm.Set("model_status:site_title", title, 0)
}

// GetEmbedConfig returns embed page configuration
func (s *ModelStatusService) GetEmbedConfig() map[string]interface{} {
	config := s.GetConfig()
	config["available_time_windows"] = AvailableTimeWindows
	config["available_themes"] = AvailableThemes
	config["available_refresh_intervals"] = AvailableRefreshIntervals
	config["available_sort_modes"] = AvailableSortModes
	return config
}
