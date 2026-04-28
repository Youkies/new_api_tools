package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/new-api-tools/backend/internal/cache"
	"github.com/new-api-tools/backend/internal/database"
	"github.com/new-api-tools/backend/internal/logger"
)

// AutoGroupService handles automatic user group assignment
// Mirrors Python auto_group_service.py functionality
type AutoGroupService struct {
	db           *database.Manager
	cachedConfig map[string]interface{} // 优化3: 请求级配置缓存
}

// Cached OAuth column existence checks for auto group
var (
	agOAuthColumnsOnce   sync.Once
	agAvailableOAuthCols []string
	autoGroupScanOnce    sync.Once
)

// allAutoGroupOAuthColumns lists all possible OAuth ID columns
var allAutoGroupOAuthColumns = []string{"github_id", "wechat_id", "telegram_id", "discord_id", "oidc_id", "linux_do_id"}

// NewAutoGroupService creates a new AutoGroupService
func NewAutoGroupService() *AutoGroupService {
	return &AutoGroupService{db: database.Get()}
}

// StartBackgroundAutoGroupScan runs the configured auto-group scan loop.
func StartBackgroundAutoGroupScan(stop <-chan struct{}) {
	autoGroupScanOnce.Do(func() {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.L.Error(fmt.Sprintf("自动分组后台任务 panic: %v", r))
				}
			}()

			select {
			case <-time.After(2 * time.Minute):
			case <-stop:
				return
			}

			logger.L.System("自动分组后台任务已启动")
			for {
				svc := NewAutoGroupService()
				config := svc.GetConfig()
				enabled, _ := config["enabled"].(bool)
				autoScanEnabled, _ := config["auto_scan_enabled"].(bool)
				intervalMinutes := toInt64(config["scan_interval_minutes"])
				if !enabled || !autoScanEnabled || intervalMinutes <= 0 {
					select {
					case <-time.After(1 * time.Minute):
					case <-stop:
						logger.L.System("自动分组后台任务已停止")
						return
					}
					continue
				}

				select {
				case <-time.After(time.Duration(intervalMinutes) * time.Minute):
				case <-stop:
					logger.L.System("自动分组后台任务已停止")
					return
				}

				svc = NewAutoGroupService()
				config = svc.GetConfig()
				enabled, _ = config["enabled"].(bool)
				autoScanEnabled, _ = config["auto_scan_enabled"].(bool)
				if !enabled || !autoScanEnabled || toInt64(config["scan_interval_minutes"]) <= 0 {
					continue
				}

				logger.L.System(fmt.Sprintf("自动分组: 开始定时扫描 (间隔: %d分钟)", intervalMinutes))
				result := svc.RunScan(false)
				if success, _ := result["success"].(bool); !success {
					logger.L.Warn(fmt.Sprintf("自动分组定时扫描失败: %v", result["message"]))
				}
			}
		}()
	})
}

// getGroupCol returns the properly quoted column name for "group"
func (s *AutoGroupService) getGroupCol() string {
	if s.db.IsPG {
		return `"group"`
	}
	return "`group`"
}

// getAvailableOAuthColumns returns OAuth columns that exist in the users table (cached)
func (s *AutoGroupService) getAvailableOAuthColumns() []string {
	agOAuthColumnsOnce.Do(func() {
		agAvailableOAuthCols = make([]string, 0)
		for _, col := range allAutoGroupOAuthColumns {
			if s.db.ColumnExists("users", col) {
				agAvailableOAuthCols = append(agAvailableOAuthCols, col)
			}
		}
	})
	return agAvailableOAuthCols
}

// 优化5: detectSource 只检查数据库中实际存在的列
func (s *AutoGroupService) detectSource(row map[string]interface{}) string {
	cols := s.getAvailableOAuthColumns()
	colSet := make(map[string]bool, len(cols))
	for _, c := range cols {
		colSet[c] = true
	}

	if colSet["github_id"] && toString(row["github_id"]) != "" {
		return "github"
	}
	if colSet["wechat_id"] && toString(row["wechat_id"]) != "" {
		return "wechat"
	}
	if colSet["telegram_id"] && toString(row["telegram_id"]) != "" {
		return "telegram"
	}
	if colSet["discord_id"] && toString(row["discord_id"]) != "" {
		return "discord"
	}
	if colSet["oidc_id"] && toString(row["oidc_id"]) != "" {
		return "oidc"
	}
	if colSet["linux_do_id"] && toString(row["linux_do_id"]) != "" {
		return "linux_do"
	}
	return "password"
}

// buildSourceCaseSQL builds a SQL CASE expression for source detection (优化2)
func (s *AutoGroupService) buildSourceCaseSQL() string {
	cols := s.getAvailableOAuthColumns()
	colSet := make(map[string]bool, len(cols))
	for _, c := range cols {
		colSet[c] = true
	}

	var parts []string
	colSourceMap := []struct{ col, source string }{
		{"github_id", "github"},
		{"wechat_id", "wechat"},
		{"telegram_id", "telegram"},
		{"discord_id", "discord"},
		{"oidc_id", "oidc"},
		{"linux_do_id", "linux_do"},
	}

	for _, cs := range colSourceMap {
		if colSet[cs.col] {
			parts = append(parts, fmt.Sprintf("WHEN %s IS NOT NULL AND %s != '' THEN '%s'", cs.col, cs.col, cs.source))
		}
	}

	if len(parts) == 0 {
		return "'password'"
	}

	return fmt.Sprintf("CASE %s ELSE 'password' END", strings.Join(parts, " "))
}

// Default auto group config — matches Python defaults
var defaultAutoGroupConfig = map[string]interface{}{
	"enabled":               false,
	"mode":                  "simple",
	"target_group":          "",
	"source_rules":          map[string]interface{}{"github": "", "wechat": "", "telegram": "", "discord": "", "oidc": "", "linux_do": "", "password": ""},
	"scan_interval_minutes": 60,
	"auto_scan_enabled":     false,
	"whitelist_ids":         []interface{}{},
	"usage_rules":           []interface{}{},
	"usage_require_topup":   true,
	"last_scan_time":        0,
}

const autoGroupQuotaPerUSD = 500000.0

type usageGroupRule struct {
	Group           string  `json:"group"`
	ThresholdAmount float64 `json:"threshold_amount"`
	ThresholdQuota  int64   `json:"threshold_quota"`
}

// 优化3: getConfigCached 请求级缓存，避免重复 Redis GET + JSON Unmarshal
func (s *AutoGroupService) getConfigCached() map[string]interface{} {
	if s.cachedConfig != nil {
		return s.cachedConfig
	}
	s.cachedConfig = s.GetConfig()
	return s.cachedConfig
}

// invalidateConfigCache clears the cached config (call after SaveConfig)
func (s *AutoGroupService) invalidateConfigCache() {
	s.cachedConfig = nil
}

// GetConfig returns auto group configuration (always fresh from Redis)
func (s *AutoGroupService) GetConfig() map[string]interface{} {
	cm := cache.Get()
	var config map[string]interface{}
	found, _ := cm.GetJSON("auto_group:config", &config)
	if found && config != nil {
		result := make(map[string]interface{})
		for k, v := range defaultAutoGroupConfig {
			result[k] = v
		}
		for k, v := range config {
			result[k] = v
		}
		return result
	}
	result := make(map[string]interface{})
	for k, v := range defaultAutoGroupConfig {
		result[k] = v
	}
	return result
}

// SaveConfig saves auto group configuration
func (s *AutoGroupService) SaveConfig(updates map[string]interface{}) bool {
	config := s.GetConfig()
	for k, v := range updates {
		config[k] = v
	}
	cm := cache.Get()
	if err := cm.Set("auto_group:config", config, 0); err != nil {
		logger.L.Error(fmt.Sprintf("保存自动分组配置失败: %v", err))
		return false
	}
	s.invalidateConfigCache()
	logger.L.Business("自动分组配置已更新")
	return true
}

// IsEnabled returns whether auto group is enabled
func (s *AutoGroupService) IsEnabled() bool {
	config := s.getConfigCached()
	if enabled, ok := config["enabled"].(bool); ok {
		return enabled
	}
	return false
}

// getWhitelistIDs extracts whitelist IDs from config
func (s *AutoGroupService) getWhitelistIDs() []int64 {
	config := s.getConfigCached()
	rawList, ok := config["whitelist_ids"]
	if !ok || rawList == nil {
		return nil
	}

	var result []int64
	switch list := rawList.(type) {
	case []interface{}:
		for _, v := range list {
			result = append(result, toInt64(v))
		}
	case []int64:
		result = list
	case []float64:
		for _, v := range list {
			result = append(result, int64(v))
		}
	}
	return result
}

// getTargetGroupBySource returns the target group for a given source
func (s *AutoGroupService) getTargetGroupBySource(source string) string {
	config := s.getConfigCached()
	mode, _ := config["mode"].(string)

	if mode == "simple" {
		tg, _ := config["target_group"].(string)
		return tg
	}

	// by_source mode
	rules, ok := config["source_rules"].(map[string]interface{})
	if !ok {
		return ""
	}
	tg, _ := rules[source].(string)
	return tg
}

func (s *AutoGroupService) getUsageRules() []usageGroupRule {
	config := s.getConfigCached()
	rawRules, ok := config["usage_rules"]
	if !ok || rawRules == nil {
		return nil
	}

	items := make([]interface{}, 0)
	switch rules := rawRules.(type) {
	case []interface{}:
		items = rules
	case []map[string]interface{}:
		for _, rule := range rules {
			items = append(items, rule)
		}
	}

	result := make([]usageGroupRule, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		ruleMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		group := strings.TrimSpace(toString(ruleMap["group"]))
		if group == "" || group == "default" || seen[group] {
			continue
		}

		thresholdQuota := toInt64(ruleMap["threshold_quota"])
		thresholdAmount := toFloat64(ruleMap["threshold_amount"])
		if thresholdAmount <= 0 && thresholdQuota > 0 {
			thresholdAmount = float64(thresholdQuota) / autoGroupQuotaPerUSD
		}
		if thresholdQuota <= 0 && thresholdAmount > 0 {
			thresholdQuota = int64(math.Ceil(thresholdAmount * autoGroupQuotaPerUSD))
		}
		if thresholdAmount <= 0 || thresholdQuota <= 0 {
			continue
		}

		seen[group] = true
		result = append(result, usageGroupRule{
			Group:           group,
			ThresholdAmount: thresholdAmount,
			ThresholdQuota:  thresholdQuota,
		})
	}

	sortUsageRules(result)
	return result
}

func sortUsageRules(rules []usageGroupRule) {
	for i := 0; i < len(rules); i++ {
		for j := i + 1; j < len(rules); j++ {
			if rules[j].ThresholdQuota < rules[i].ThresholdQuota {
				rules[i], rules[j] = rules[j], rules[i]
			}
		}
	}
}

func targetGroupByUsage(usedQuota int64, rules []usageGroupRule) (usageGroupRule, bool) {
	var matched usageGroupRule
	found := false
	for _, rule := range rules {
		if usedQuota >= rule.ThresholdQuota {
			matched = rule
			found = true
		}
	}
	return matched, found
}

func shouldMoveByUsage(currentGroup string, targetRule usageGroupRule, rules []usageGroupRule) bool {
	currentGroup = strings.TrimSpace(currentGroup)
	if currentGroup == "" {
		currentGroup = "default"
	}
	if currentGroup == targetRule.Group {
		return false
	}
	if currentGroup == "default" {
		return true
	}

	currentThreshold := int64(0)
	for _, rule := range rules {
		if rule.Group == currentGroup {
			currentThreshold = rule.ThresholdQuota
			break
		}
	}
	if currentThreshold <= 0 {
		return false
	}
	return targetRule.ThresholdQuota > currentThreshold
}

func (s *AutoGroupService) requireTopUpForUsage() bool {
	config := s.getConfigCached()
	value, ok := config["usage_require_topup"]
	if !ok {
		return true
	}
	if enabled, ok := value.(bool); ok {
		return enabled
	}
	return true
}

func (s *AutoGroupService) buildUsageTopUpCondition() string {
	if !s.requireTopUpForUsage() {
		return ""
	}
	exists, err := s.db.TableExists("top_ups")
	if err != nil || !exists {
		return "AND 1 = 0"
	}
	return `AND EXISTS (
		SELECT 1 FROM top_ups tu
		WHERE tu.user_id = users.id
		AND (LOWER(tu.status) IN ('success', 'completed') OR tu.status = '1')
	)`
}

// buildWhitelistCondition builds the SQL condition and args for whitelist exclusion
func (s *AutoGroupService) buildWhitelistCondition(whitelistIDs []int64, argIdx int) (string, []interface{}, int) {
	if len(whitelistIDs) == 0 {
		return "", nil, argIdx
	}

	var args []interface{}
	if s.db.IsPG {
		placeholders := make([]string, len(whitelistIDs))
		for i, id := range whitelistIDs {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, id)
			argIdx++
		}
		return fmt.Sprintf("AND id NOT IN (%s)", strings.Join(placeholders, ",")), args, argIdx
	}

	placeholders := make([]string, len(whitelistIDs))
	for i, id := range whitelistIDs {
		placeholders[i] = "?"
		args = append(args, id)
		_ = i
	}
	return fmt.Sprintf("AND id NOT IN (%s)", strings.Join(placeholders, ",")), args, argIdx
}

// buildOAuthSelectCols builds the OAuth column select string
func (s *AutoGroupService) buildOAuthSelectCols() string {
	cols := s.getAvailableOAuthColumns()
	if len(cols) == 0 {
		return ""
	}
	result := ""
	for _, col := range cols {
		result += ", " + col
	}
	return result
}

// GetStats returns grouping statistics — matches Python's get_stats()
func (s *AutoGroupService) GetStats() map[string]interface{} {
	config := s.getConfigCached()
	enabled, _ := config["enabled"].(bool)
	autoScanEnabled, _ := config["auto_scan_enabled"].(bool)
	scanInterval := toInt64(config["scan_interval_minutes"])
	lastScanTime := toInt64(config["last_scan_time"])

	groupCol := s.getGroupCol()
	whitelistIDs := s.getWhitelistIDs()

	pendingCount := int64(0)
	if mode, _ := config["mode"].(string); mode == "by_usage" {
		pending := s.GetUsageUpgradeUsers(1, 1)
		pendingCount = toInt64(pending["total"])
	} else {
		// Build whitelist condition
		wlCond, wlArgs, _ := s.buildWhitelistCondition(whitelistIDs, 1)

		// Count pending users (default group, active, not whitelisted)
		pendingSQL := fmt.Sprintf(`
			SELECT COUNT(*) as cnt
			FROM users
			WHERE (COALESCE(%s, 'default') = 'default' OR %s = '')
			AND deleted_at IS NULL
			AND status = 1
			%s`, groupCol, groupCol, wlCond)

		if !s.db.IsPG {
			pendingSQL = s.db.RebindQuery(pendingSQL)
		}

		row, err := s.db.QueryOne(pendingSQL, wlArgs...)
		if err == nil && row != nil {
			pendingCount = toInt64(row["cnt"])
		}
	}

	// 优化4: 使用 Redis LLEN 获取总日志计数
	totalAssigned := int64(0)
	cm := cache.Get()
	rdb := cm.RedisClient()
	ctx := context.Background()

	// Count assign logs from Redis list
	logLen, err := rdb.LLen(ctx, "auto_group:logs").Result()
	if err == nil && logLen > 0 {
		// Sample to count "assign" actions (read all, they're capped at 1000)
		logStrings, err := rdb.LRange(ctx, "auto_group:logs", 0, -1).Result()
		if err == nil {
			for _, logStr := range logStrings {
				var entry map[string]interface{}
				if json.Unmarshal([]byte(logStr), &entry) == nil {
					if action, _ := entry["action"].(string); action == "assign" {
						totalAssigned += toInt64(entry["affected"])
					}
				}
			}
		}
	}

	// Calculate next scan time
	nextScanTime := int64(0)
	if autoScanEnabled && scanInterval > 0 {
		nextScanTime = lastScanTime + (scanInterval * 60)
	}

	return map[string]interface{}{
		"pending_count":     pendingCount,
		"total_assigned":    totalAssigned,
		"last_scan_time":    lastScanTime,
		"next_scan_time":    nextScanTime,
		"enabled":           enabled,
		"auto_scan_enabled": autoScanEnabled,
	}
}

// GetAvailableGroups returns all groups known by NewAPI users, channels, abilities and options.
func (s *AutoGroupService) GetAvailableGroups() []map[string]interface{} {
	groupCounts := map[string]int64{}
	groupCol := s.getGroupCol()

	query := fmt.Sprintf(`
		SELECT COALESCE(%s, 'default') as group_name, COUNT(*) as user_count
		FROM users
		WHERE deleted_at IS NULL
		GROUP BY COALESCE(%s, 'default')
		ORDER BY user_count DESC`, groupCol, groupCol)

	rows, err := s.db.Query(query)
	if err != nil {
		logger.L.Error(fmt.Sprintf("获取可用分组列表失败: %v", err))
	}
	for _, row := range rows {
		addGroupNames(groupCounts, toString(row["group_name"]), toInt64(row["user_count"]))
	}

	s.addGroupsFromAbilities(groupCounts)
	s.addGroupsFromChannels(groupCounts)
	s.addGroupsFromOptions(groupCounts)

	delete(groupCounts, "")
	result := make([]map[string]interface{}, 0, len(groupCounts))
	for groupName, userCount := range groupCounts {
		result = append(result, map[string]interface{}{
			"group_name": groupName,
			"user_count": userCount,
		})
	}
	sortGroupRows(result)
	return result
}

func (s *AutoGroupService) addGroupsFromAbilities(groupCounts map[string]int64) {
	exists, err := s.db.TableExists("abilities")
	if err != nil || !exists || !s.db.ColumnExists("abilities", "group") {
		return
	}
	groupCol := s.getGroupCol()
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT DISTINCT COALESCE(NULLIF(%s, ''), 'default') as group_name
		FROM abilities`, groupCol))
	if err != nil {
		return
	}
	for _, row := range rows {
		addGroupNames(groupCounts, toString(row["group_name"]), 0)
	}
}

func (s *AutoGroupService) addGroupsFromChannels(groupCounts map[string]int64) {
	exists, err := s.db.TableExists("channels")
	if err != nil || !exists || !s.db.ColumnExists("channels", "group") {
		return
	}
	groupCol := s.getGroupCol()
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT DISTINCT COALESCE(NULLIF(%s, ''), 'default') as group_name
		FROM channels`, groupCol))
	if err != nil {
		return
	}
	for _, row := range rows {
		addGroupNames(groupCounts, toString(row["group_name"]), 0)
	}
}

func (s *AutoGroupService) addGroupsFromOptions(groupCounts map[string]int64) {
	exists, err := s.db.TableExists("options")
	if err != nil || !exists {
		return
	}
	keyCol := "`key`"
	if s.db.IsPG {
		keyCol = `"key"`
	}
	query := s.db.RebindQuery(fmt.Sprintf(`
		SELECT %s as option_key, value
		FROM options
		WHERE %s IN (?, ?, ?, ?, ?)`, keyCol, keyCol))
	rows, err := s.db.Query(query,
		"GroupRatio",
		"UserUsableGroups",
		"GroupGroupRatio",
		"group_ratio_setting.group_special_usable_group",
		"AutoGroups",
	)
	if err != nil {
		return
	}
	for _, row := range rows {
		addGroupsFromOptionValue(groupCounts, toString(row["option_key"]), toString(row["value"]))
	}
}

func addGroupsFromOptionValue(groupCounts map[string]int64, key, rawValue string) {
	rawValue = strings.TrimSpace(rawValue)
	if rawValue == "" || rawValue == "{}" || rawValue == "[]" {
		return
	}
	var decoded interface{}
	if err := json.Unmarshal([]byte(rawValue), &decoded); err != nil {
		return
	}
	switch key {
	case "GroupRatio", "UserUsableGroups":
		if obj, ok := decoded.(map[string]interface{}); ok {
			for groupName := range obj {
				addGroupNames(groupCounts, groupName, 0)
			}
		}
	case "GroupGroupRatio":
		if obj, ok := decoded.(map[string]interface{}); ok {
			for userGroup, inner := range obj {
				addGroupNames(groupCounts, userGroup, 0)
				if innerObj, ok := inner.(map[string]interface{}); ok {
					for usingGroup := range innerObj {
						addGroupNames(groupCounts, usingGroup, 0)
					}
				}
			}
		}
	case "group_ratio_setting.group_special_usable_group":
		if obj, ok := decoded.(map[string]interface{}); ok {
			for userGroup, inner := range obj {
				addGroupNames(groupCounts, userGroup, 0)
				if innerObj, ok := inner.(map[string]interface{}); ok {
					for rawGroup := range innerObj {
						groupName := strings.TrimPrefix(strings.TrimPrefix(rawGroup, "+:"), "-:")
						addGroupNames(groupCounts, groupName, 0)
					}
				}
			}
		}
	case "AutoGroups":
		switch value := decoded.(type) {
		case []interface{}:
			for _, item := range value {
				addGroupNames(groupCounts, toString(item), 0)
			}
		case map[string]interface{}:
			for groupName := range value {
				addGroupNames(groupCounts, groupName, 0)
			}
		}
	}
}

func addGroupNames(groupCounts map[string]int64, raw string, userCount int64) {
	for _, groupName := range strings.Split(raw, ",") {
		groupName = strings.TrimSpace(groupName)
		if groupName == "" {
			continue
		}
		if _, exists := groupCounts[groupName]; !exists {
			groupCounts[groupName] = 0
		}
		if userCount > 0 {
			groupCounts[groupName] += userCount
		}
	}
}

func sortGroupRows(rows []map[string]interface{}) {
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			ci := toInt64(rows[i]["user_count"])
			cj := toInt64(rows[j]["user_count"])
			gi := toString(rows[i]["group_name"])
			gj := toString(rows[j]["group_name"])
			if cj > ci || (cj == ci && gj < gi) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
}

// GetPendingUsers returns users not yet assigned to a group
func (s *AutoGroupService) GetPendingUsers(page, pageSize int) map[string]interface{} {
	config := s.getConfigCached()
	if mode, _ := config["mode"].(string); mode == "by_usage" {
		return s.GetUsageUpgradeUsers(page, pageSize)
	}

	groupCol := s.getGroupCol()
	whitelistIDs := s.getWhitelistIDs()
	oauthCols := s.buildOAuthSelectCols()

	args := make([]interface{}, 0)
	argIdx := 1

	wlCond, wlArgs, nextIdx := s.buildWhitelistCondition(whitelistIDs, argIdx)
	args = append(args, wlArgs...)
	argIdx = nextIdx

	// Count total
	countSQL := fmt.Sprintf(`
		SELECT COUNT(*) as cnt
		FROM users
		WHERE (COALESCE(%s, 'default') = 'default' OR %s = '')
		AND deleted_at IS NULL
		AND status = 1
		%s`, groupCol, groupCol, wlCond)

	if !s.db.IsPG {
		countSQL = s.db.RebindQuery(countSQL)
	}

	total := int64(0)
	countRow, err := s.db.QueryOne(countSQL, args...)
	if err == nil && countRow != nil {
		total = toInt64(countRow["cnt"])
	}

	// Get user list
	offset := (page - 1) * pageSize
	var listArgs []interface{}
	listArgs = append(listArgs, args...)

	var listSQL string
	if s.db.IsPG {
		listSQL = fmt.Sprintf(`
			SELECT id, username, display_name, email, %s as user_group, status%s
			FROM users
			WHERE (COALESCE(%s, 'default') = 'default' OR %s = '')
			AND deleted_at IS NULL
			AND status = 1
			%s
			ORDER BY id DESC
			LIMIT $%d OFFSET $%d`,
			groupCol, oauthCols, groupCol, groupCol, wlCond, argIdx, argIdx+1)
		listArgs = append(listArgs, pageSize, offset)
	} else {
		listSQL = fmt.Sprintf(`
			SELECT id, username, display_name, email, %s as user_group, status%s
			FROM users
			WHERE (COALESCE(%s, 'default') = 'default' OR %s = '')
			AND deleted_at IS NULL
			AND status = 1
			%s
			ORDER BY id DESC
			LIMIT ? OFFSET ?`,
			groupCol, oauthCols, groupCol, groupCol, wlCond)
		listArgs = append(listArgs, pageSize, offset)
		listSQL = s.db.RebindQuery(listSQL)
	}

	rows, err := s.db.Query(listSQL, listArgs...)
	if err != nil {
		logger.L.Error(fmt.Sprintf("获取待分配用户列表失败: %v", err))
		rows = nil
	}

	items := make([]map[string]interface{}, 0)
	for _, row := range rows {
		source := s.detectSource(row)
		items = append(items, map[string]interface{}{
			"id":           toInt64(row["id"]),
			"username":     toString(row["username"]),
			"display_name": toString(row["display_name"]),
			"email":        toString(row["email"]),
			"group":        toString(row["user_group"]),
			"source":       source,
			"status":       toInt64(row["status"]),
		})
	}

	totalPages := int64(0)
	if total > 0 {
		totalPages = (total + int64(pageSize) - 1) / int64(pageSize)
	}

	return map[string]interface{}{
		"items":       items,
		"total":       total,
		"page":        page,
		"page_size":   pageSize,
		"total_pages": totalPages,
	}
}

// GetUsageUpgradeUsers returns active users eligible for usage-based group upgrades.
func (s *AutoGroupService) GetUsageUpgradeUsers(page, pageSize int) map[string]interface{} {
	rules := s.getUsageRules()
	if len(rules) == 0 {
		return map[string]interface{}{
			"items": []map[string]interface{}{}, "total": int64(0),
			"page": page, "page_size": pageSize, "total_pages": int64(0),
		}
	}

	groupCol := s.getGroupCol()
	oauthCols := s.buildOAuthSelectCols()
	whitelistIDs := s.getWhitelistIDs()
	minThreshold := rules[0].ThresholdQuota
	topUpCond := s.buildUsageTopUpCondition()
	hasTopUp := s.requireTopUpForUsage()

	args := []interface{}{minThreshold}
	wlCond, wlArgs, _ := s.buildWhitelistCondition(whitelistIDs, 2)
	args = append(args, wlArgs...)

	var query string
	if s.db.IsPG {
		query = fmt.Sprintf(`
			SELECT id, username, display_name, email, %s as user_group, status,
				COALESCE(used_quota, 0) as used_quota%s
			FROM users
			WHERE deleted_at IS NULL
			AND status = 1
			AND COALESCE(used_quota, 0) >= $1
			%s
			%s
			ORDER BY COALESCE(used_quota, 0) DESC, id DESC`, groupCol, oauthCols, wlCond, topUpCond)
	} else {
		query = fmt.Sprintf(`
			SELECT id, username, display_name, email, %s as user_group, status,
				COALESCE(used_quota, 0) as used_quota%s
			FROM users
			WHERE deleted_at IS NULL
			AND status = 1
			AND COALESCE(used_quota, 0) >= ?
			%s
			%s
			ORDER BY COALESCE(used_quota, 0) DESC, id DESC`, groupCol, oauthCols, wlCond, topUpCond)
		query = s.db.RebindQuery(query)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		logger.L.Error(fmt.Sprintf("获取按消费升级用户失败: %v", err))
		rows = nil
	}

	eligible := make([]map[string]interface{}, 0)
	for _, row := range rows {
		usedQuota := toInt64(row["used_quota"])
		targetRule, ok := targetGroupByUsage(usedQuota, rules)
		if !ok {
			continue
		}
		currentGroup := toString(row["user_group"])
		if currentGroup == "" {
			currentGroup = "default"
		}
		if !shouldMoveByUsage(currentGroup, targetRule, rules) {
			continue
		}

		source := s.detectSource(row)
		eligible = append(eligible, map[string]interface{}{
			"id":               toInt64(row["id"]),
			"username":         toString(row["username"]),
			"display_name":     toString(row["display_name"]),
			"email":            toString(row["email"]),
			"group":            currentGroup,
			"source":           source,
			"status":           toInt64(row["status"]),
			"used_quota":       usedQuota,
			"used_amount":      math.Round((float64(usedQuota)/autoGroupQuotaPerUSD)*100) / 100,
			"has_topup":        hasTopUp,
			"target_group":     targetRule.Group,
			"threshold_amount": targetRule.ThresholdAmount,
			"threshold_quota":  targetRule.ThresholdQuota,
		})
	}

	total := int64(len(eligible))
	start := (page - 1) * pageSize
	if start < 0 {
		start = 0
	}
	end := start + pageSize
	if start > len(eligible) {
		start = len(eligible)
	}
	if end > len(eligible) {
		end = len(eligible)
	}

	totalPages := int64(0)
	if total > 0 {
		totalPages = (total + int64(pageSize) - 1) / int64(pageSize)
	}

	return map[string]interface{}{
		"items":       eligible[start:end],
		"total":       total,
		"page":        page,
		"page_size":   pageSize,
		"total_pages": totalPages,
	}
}

// GetUsers returns users with filtering — matches Python's get_users()
// 优化2: source 过滤使用 SQL CASE WHEN 代替全表拉取
func (s *AutoGroupService) GetUsers(page, pageSize int, group, source, keyword string) map[string]interface{} {
	groupCol := s.getGroupCol()
	oauthCols := s.buildOAuthSelectCols()
	sourceCaseSQL := s.buildSourceCaseSQL()

	offset := (page - 1) * pageSize
	where := []string{"deleted_at IS NULL"}
	args := []interface{}{}
	argIdx := 1

	if group != "" {
		if group == "default" {
			where = append(where, fmt.Sprintf("(COALESCE(%s, 'default') = 'default' OR %s = '')", groupCol, groupCol))
		} else {
			if s.db.IsPG {
				where = append(where, fmt.Sprintf("%s = $%d", groupCol, argIdx))
				argIdx++
			} else {
				where = append(where, fmt.Sprintf("%s = ?", groupCol))
			}
			args = append(args, group)
		}
	}

	if keyword != "" {
		if s.db.IsPG {
			where = append(where, fmt.Sprintf("(username ILIKE $%d OR CAST(id AS TEXT) LIKE $%d)", argIdx, argIdx+1))
			args = append(args, "%"+keyword+"%", "%"+keyword+"%")
			argIdx += 2
		} else {
			where = append(where, "(username LIKE ? OR CAST(id AS CHAR) LIKE ?)")
			args = append(args, "%"+keyword+"%", "%"+keyword+"%")
		}
	}

	// 优化2: source 过滤下推到 SQL 层
	if source != "" {
		// Validate source against known values to prevent injection
		validSources := map[string]bool{
			"github": true, "wechat": true, "telegram": true,
			"discord": true, "oidc": true, "linux_do": true, "password": true,
		}
		if validSources[source] {
			if s.db.IsPG {
				where = append(where, fmt.Sprintf("(%s) = $%d", sourceCaseSQL, argIdx))
				argIdx++
			} else {
				where = append(where, fmt.Sprintf("(%s) = ?", sourceCaseSQL))
			}
			args = append(args, source)
		}
	}

	whereClause := strings.Join(where, " AND ")

	// Count total (now includes source filter if specified)
	countSQL := fmt.Sprintf("SELECT COUNT(*) as cnt FROM users WHERE %s", whereClause)
	if !s.db.IsPG {
		countSQL = s.db.RebindQuery(countSQL)
	}
	total := int64(0)
	countRow, err := s.db.QueryOne(countSQL, args...)
	if err == nil && countRow != nil {
		total = toInt64(countRow["cnt"])
	}

	// Get users
	var listArgs []interface{}
	listArgs = append(listArgs, args...)

	var listSQL string
	if s.db.IsPG {
		listSQL = fmt.Sprintf(`
			SELECT id, username, display_name, email, %s as user_group, status%s
			FROM users
			WHERE %s
			ORDER BY id DESC
			LIMIT $%d OFFSET $%d`,
			groupCol, oauthCols, whereClause, argIdx, argIdx+1)
		listArgs = append(listArgs, pageSize, offset)
	} else {
		listSQL = fmt.Sprintf(`
			SELECT id, username, display_name, email, %s as user_group, status%s
			FROM users
			WHERE %s
			ORDER BY id DESC
			LIMIT ? OFFSET ?`,
			groupCol, oauthCols, whereClause)
		listArgs = append(listArgs, pageSize, offset)
		listSQL = s.db.RebindQuery(listSQL)
	}

	rows, err := s.db.Query(listSQL, listArgs...)
	if err != nil {
		logger.L.Error(fmt.Sprintf("获取用户列表失败: %v", err))
		rows = nil
	}

	// Build items with source detection
	items := make([]map[string]interface{}, 0)
	for _, row := range rows {
		userSource := s.detectSource(row)
		items = append(items, map[string]interface{}{
			"id":           toInt64(row["id"]),
			"username":     toString(row["username"]),
			"display_name": toString(row["display_name"]),
			"email":        toString(row["email"]),
			"group":        toString(row["user_group"]),
			"source":       userSource,
			"status":       toInt64(row["status"]),
		})
	}

	totalPages := int64(0)
	if total > 0 {
		totalPages = (total + int64(pageSize) - 1) / int64(pageSize)
	}

	return map[string]interface{}{
		"items":       items,
		"total":       total,
		"page":        page,
		"page_size":   pageSize,
		"total_pages": totalPages,
	}
}

func (s *AutoGroupService) getAssignableUser(userID int64) (map[string]interface{}, map[string]interface{}) {
	groupCol := s.getGroupCol()
	oauthCols := s.buildOAuthSelectCols()

	var userSQL string
	if s.db.IsPG {
		userSQL = fmt.Sprintf(
			"SELECT id, username, %s as user_group%s FROM users WHERE id = $1 AND deleted_at IS NULL",
			groupCol, oauthCols)
	} else {
		userSQL = fmt.Sprintf(
			"SELECT id, username, %s as user_group%s FROM users WHERE id = ? AND deleted_at IS NULL",
			groupCol, oauthCols)
	}

	userRow, err := s.db.QueryOne(userSQL, userID)
	if err != nil || userRow == nil {
		return nil, map[string]interface{}{
			"success": false,
			"message": "用户不存在",
		}
	}

	oldGroup := toString(userRow["user_group"])
	if oldGroup == "" {
		oldGroup = "default"
	}
	username := toString(userRow["username"])
	source := s.detectSource(userRow)

	return map[string]interface{}{
		"id":        userID,
		"username":  username,
		"old_group": oldGroup,
		"source":    source,
	}, nil
}

// assignUser assigns a single user to a target group — matches Python's assign_user()
func (s *AutoGroupService) assignUser(userID int64, targetGroup, operator string) map[string]interface{} {
	return s.assignUserWithBatch(userID, targetGroup, operator, "")
}

func (s *AutoGroupService) assignUserWithBatch(userID int64, targetGroup, operator, batchID string) map[string]interface{} {
	userInfo, errorResult := s.getAssignableUser(userID)
	if errorResult != nil {
		return errorResult
	}

	groupCol := s.getGroupCol()
	oldGroup := toString(userInfo["old_group"])
	username := toString(userInfo["username"])
	source := toString(userInfo["source"])

	var updateSQL string
	if s.db.IsPG {
		updateSQL = fmt.Sprintf("UPDATE users SET %s = $1 WHERE id = $2", groupCol)
	} else {
		updateSQL = fmt.Sprintf("UPDATE users SET %s = ? WHERE id = ?", groupCol)
	}

	_, err := s.db.Execute(updateSQL, targetGroup, userID)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("更新用户分组失败: %v", err),
		}
	}

	s.addUserLogWithMeta("assign", userID, username, oldGroup, targetGroup, source, operator, batchID, 0)

	logger.L.Business(fmt.Sprintf("自动分组: 用户分配 user_id=%d username=%s %s -> %s source=%s operator=%s",
		userID, username, oldGroup, targetGroup, source, operator))

	return map[string]interface{}{
		"success":   true,
		"message":   fmt.Sprintf("用户 %s 已分配到 %s", username, targetGroup),
		"user_id":   userID,
		"username":  username,
		"old_group": oldGroup,
		"new_group": targetGroup,
		"source":    source,
		"batch_id":  batchID,
	}
}

// 优化1: RunScan 使用批量 UPDATE 消除 N+1
func (s *AutoGroupService) RunScan(dryRun bool) map[string]interface{} {
	config := s.getConfigCached()
	mode, _ := config["mode"].(string)

	// Validate configuration
	if mode == "simple" {
		targetGroup, _ := config["target_group"].(string)
		if targetGroup == "" {
			return map[string]interface{}{
				"success": false,
				"message": "未配置目标分组",
			}
		}
	} else if mode == "by_source" {
		rules, _ := config["source_rules"].(map[string]interface{})
		hasAnyRule := false
		if rules != nil {
			for _, v := range rules {
				if sv, ok := v.(string); ok && sv != "" {
					hasAnyRule = true
					break
				}
			}
		}
		if !hasAnyRule {
			return map[string]interface{}{
				"success": false,
				"message": "未配置任何来源分组规则",
			}
		}
	} else if mode == "by_usage" {
		if len(s.getUsageRules()) == 0 {
			return map[string]interface{}{
				"success": false,
				"message": "未配置任何消费升级规则",
			}
		}
	}

	startTime := time.Now()

	// Get pending users for preview/logging
	pending := s.GetPendingUsers(1, 1000)
	users, _ := pending["items"].([]map[string]interface{})

	logger.L.Info(fmt.Sprintf("自动分组扫描: 发现 %d 个待分配用户", len(users)))

	if len(users) == 0 {
		return map[string]interface{}{
			"success": true,
			"dry_run": dryRun,
			"stats": map[string]interface{}{
				"total": 0, "assigned": 0, "skipped": 0, "errors": 0,
			},
			"elapsed_seconds": "0.00",
			"results":         []map[string]interface{}{},
		}
	}

	results := make([]map[string]interface{}, 0, len(users))
	assignedCount := 0
	skippedCount := 0
	errorCount := 0

	if mode == "simple" && !dryRun {
		// 优化1 路径: simple模式批量UPDATE
		targetGroup, _ := config["target_group"].(string)
		groupCol := s.getGroupCol()
		whitelistIDs := s.getWhitelistIDs()
		wlCond, wlArgs, nextIdx := s.buildWhitelistCondition(whitelistIDs, 2)

		// Collect user info before update for logging
		userInfos := make([]map[string]interface{}, 0, len(users))
		for _, user := range users {
			userInfos = append(userInfos, map[string]interface{}{
				"id":       toInt64(user["id"]),
				"username": toString(user["username"]),
				"source":   toString(user["source"]),
			})
		}

		// Batch UPDATE in one SQL
		var updateSQL string
		updateArgs := make([]interface{}, 0)
		if s.db.IsPG {
			updateSQL = fmt.Sprintf(`
				UPDATE users SET %s = $1
				WHERE (COALESCE(%s, 'default') = 'default' OR %s = '')
				AND deleted_at IS NULL AND status = 1
				%s`, groupCol, groupCol, groupCol, wlCond)
			updateArgs = append(updateArgs, targetGroup)
			updateArgs = append(updateArgs, wlArgs...)
		} else {
			updateSQL = fmt.Sprintf(`
				UPDATE users SET %s = ?
				WHERE (COALESCE(%s, 'default') = 'default' OR %s = '')
				AND deleted_at IS NULL AND status = 1
				%s`, groupCol, groupCol, groupCol, wlCond)
			updateArgs = append(updateArgs, targetGroup)
			updateArgs = append(updateArgs, wlArgs...)
		}
		_ = nextIdx

		affected, err := s.db.Execute(updateSQL, updateArgs...)
		if err != nil {
			logger.L.Error(fmt.Sprintf("自动分组批量UPDATE失败: %v", err))
			errorCount = len(users)
			for _, user := range userInfos {
				results = append(results, map[string]interface{}{
					"user_id": user["id"], "username": user["username"],
					"source": user["source"], "action": "error",
					"message": fmt.Sprintf("批量更新失败: %v", err),
				})
			}
		} else {
			assignedCount = int(affected)
			// Batch log all assigned users via Redis LPUSH
			s.addBatchLogs("assign", userInfos, "default", targetGroup, "system")
			for _, user := range userInfos {
				results = append(results, map[string]interface{}{
					"user_id":      user["id"],
					"username":     user["username"],
					"source":       user["source"],
					"target_group": targetGroup,
					"action":       "assigned",
					"message":      fmt.Sprintf("已分配到 %s", targetGroup),
				})
			}
			logger.L.Business(fmt.Sprintf("自动分组: 批量分配 %d 个用户到 %s", assignedCount, targetGroup))
		}
	} else {
		// by_source/by_usage 模式 or dry_run: 逐用户处理
		for _, user := range users {
			userID := toInt64(user["id"])
			username := toString(user["username"])
			userSource := toString(user["source"])

			targetGroup := s.getTargetGroupBySource(userSource)
			if mode == "by_usage" {
				targetGroup = toString(user["target_group"])
			}

			if targetGroup == "" {
				skippedCount++
				message := fmt.Sprintf("来源 %s 未配置目标分组", userSource)
				if mode == "by_usage" {
					message = "未命中消费升级规则"
				}
				results = append(results, map[string]interface{}{
					"user_id": userID, "username": username, "source": userSource,
					"action": "skipped", "message": message,
				})
				continue
			}

			if dryRun {
				assignedCount++
				message := fmt.Sprintf("[试运行] 将分配到 %s", targetGroup)
				if mode == "by_usage" {
					message = fmt.Sprintf("[试运行] 已消费 $%.2f，将升级到 %s", toFloat64(user["used_amount"]), targetGroup)
				}
				results = append(results, map[string]interface{}{
					"user_id": userID, "username": username, "source": userSource,
					"target_group": targetGroup, "action": "would_assign",
					"message": message,
				})
			} else {
				result := s.assignUser(userID, targetGroup, "system")
				if success, _ := result["success"].(bool); success {
					assignedCount++
					message := toString(result["message"])
					if mode == "by_usage" {
						message = fmt.Sprintf("已消费 $%.2f，已升级到 %s", toFloat64(user["used_amount"]), targetGroup)
					}
					results = append(results, map[string]interface{}{
						"user_id": userID, "username": username, "source": userSource,
						"target_group": targetGroup, "action": "assigned",
						"message": message,
					})
				} else {
					errorCount++
					results = append(results, map[string]interface{}{
						"user_id": userID, "username": username, "source": userSource,
						"action": "error", "message": toString(result["message"]),
					})
				}
			}
		}
	}

	elapsed := time.Since(startTime).Seconds()

	// Update last scan time
	s.SaveConfig(map[string]interface{}{
		"last_scan_time": time.Now().Unix(),
	})

	logger.L.Business(fmt.Sprintf("自动分组扫描完成 dry_run=%v total=%d assigned=%d skipped=%d errors=%d elapsed=%.2fs",
		dryRun, len(users), assignedCount, skippedCount, errorCount, elapsed))

	return map[string]interface{}{
		"success": true,
		"dry_run": dryRun,
		"stats": map[string]interface{}{
			"total":    len(users),
			"assigned": assignedCount,
			"skipped":  skippedCount,
			"errors":   errorCount,
		},
		"elapsed_seconds": fmt.Sprintf("%.2f", elapsed),
		"results":         results,
	}
}

// BatchMoveUsers moves users to a target group, or previews the move when dryRun is true.
func (s *AutoGroupService) BatchMoveUsers(userIDs []int64, targetGroup string, dryRun bool) map[string]interface{} {
	if len(userIDs) == 0 {
		return map[string]interface{}{
			"success": false,
			"message": "未选择用户",
		}
	}
	if targetGroup == "" {
		return map[string]interface{}{
			"success": false,
			"message": "未指定目标分组",
		}
	}

	batchID := ""
	if !dryRun {
		batchID = generateAutoGroupBatchID("manual")
	}
	successCount := 0
	failedCount := 0
	skippedCount := 0
	results := make([]map[string]interface{}, 0)

	for _, userID := range userIDs {
		userInfo, errorResult := s.getAssignableUser(userID)
		if errorResult != nil {
			failedCount++
			results = append(results, errorResult)
			continue
		}

		oldGroup := toString(userInfo["old_group"])
		username := toString(userInfo["username"])
		source := toString(userInfo["source"])
		if oldGroup == targetGroup {
			skippedCount++
			results = append(results, map[string]interface{}{
				"success":      true,
				"user_id":      userID,
				"username":     username,
				"old_group":    oldGroup,
				"new_group":    targetGroup,
				"source":       source,
				"target_group": targetGroup,
				"action":       "skipped",
				"message":      fmt.Sprintf("用户 %s 已经在 %s", username, targetGroup),
			})
			continue
		}

		if dryRun {
			successCount++
			results = append(results, map[string]interface{}{
				"success":      true,
				"user_id":      userID,
				"username":     username,
				"old_group":    oldGroup,
				"new_group":    targetGroup,
				"source":       source,
				"target_group": targetGroup,
				"action":       "would_move",
				"message":      fmt.Sprintf("[试运行] 用户 %s 将从 %s 移动到 %s", username, oldGroup, targetGroup),
			})
		} else {
			result := s.assignUserWithBatch(userID, targetGroup, "admin", batchID)
			if success, _ := result["success"].(bool); success {
				successCount++
				result["action"] = "moved"
				result["target_group"] = targetGroup
				result["batch_id"] = batchID
			} else {
				failedCount++
				result["action"] = "error"
			}
			results = append(results, result)
		}
	}

	message := fmt.Sprintf("成功移动 %d 个用户，跳过 %d 个，失败 %d 个", successCount, skippedCount, failedCount)
	if dryRun {
		message = fmt.Sprintf("试运行完成：%d 个用户可移动，%d 个跳过，%d 个失败", successCount, skippedCount, failedCount)
	}

	return map[string]interface{}{
		"success":       failedCount == 0,
		"message":       message,
		"dry_run":       dryRun,
		"batch_id":      batchID,
		"success_count": successCount,
		"skipped_count": skippedCount,
		"failed_count":  failedCount,
		"results":       results,
	}
}

// GetLogs returns group assignment logs — 优化4: 使用 Redis List
func (s *AutoGroupService) GetLogs(page, pageSize int, action string, userID *int64) map[string]interface{} {
	cm := cache.Get()
	rdb := cm.RedisClient()
	ctx := context.Background()

	// Read all logs from Redis list
	logStrings, err := rdb.LRange(ctx, "auto_group:logs", 0, -1).Result()
	if err != nil {
		logger.L.Error(fmt.Sprintf("读取自动分组日志失败: %v", err))
		logStrings = []string{}
	}

	// Parse and filter
	filtered := make([]map[string]interface{}, 0)
	for _, logStr := range logStrings {
		var entry map[string]interface{}
		if json.Unmarshal([]byte(logStr), &entry) != nil {
			continue
		}

		if action != "" {
			if logAction, ok := entry["action"].(string); !ok || logAction != action {
				continue
			}
		}
		if userID != nil {
			logUserID := toInt64(entry["user_id"])
			if logUserID != *userID {
				continue
			}
		}
		filtered = append(filtered, entry)
	}

	total := len(filtered)
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}

	return map[string]interface{}{
		"items":       filtered[start:end],
		"total":       total,
		"page":        page,
		"page_size":   pageSize,
		"total_pages": totalPages,
	}
}

// RevertUser reverts a user's group assignment
func (s *AutoGroupService) RevertUser(logID int) map[string]interface{} {
	var targetLog map[string]interface{}
	entries, err := s.getAutoGroupLogEntries()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("读取日志失败: %v", err),
		}
	}
	for _, entry := range entries {
		if toInt64(entry["id"]) == int64(logID) {
			targetLog = entry
			break
		}
	}
	if targetLog == nil {
		return map[string]interface{}{
			"success": false,
			"message": "日志记录不存在",
		}
	}
	return s.revertLogEntry(targetLog, "admin")
}

// RevertBatch reverts all non-reverted assignment logs in the same batch.
func (s *AutoGroupService) RevertBatch(batchID string) map[string]interface{} {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return map[string]interface{}{
			"success": false,
			"message": "缺少批次 ID",
		}
	}

	entries, err := s.getAutoGroupLogEntries()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("读取日志失败: %v", err),
		}
	}

	results := make([]map[string]interface{}, 0)
	successCount := 0
	failedCount := 0
	skippedCount := 0
	for _, entry := range entries {
		if toString(entry["batch_id"]) != batchID || toString(entry["action"]) != "assign" {
			continue
		}
		if toInt64(entry["reverted_at"]) > 0 {
			skippedCount++
			continue
		}
		result := s.revertLogEntry(entry, "admin")
		if success, _ := result["success"].(bool); success {
			successCount++
		} else {
			failedCount++
		}
		results = append(results, result)
	}

	if successCount == 0 && failedCount == 0 && skippedCount == 0 {
		return map[string]interface{}{
			"success":  false,
			"message":  "没有找到可撤销的批次记录",
			"batch_id": batchID,
		}
	}

	return map[string]interface{}{
		"success":       failedCount == 0,
		"message":       fmt.Sprintf("批次撤销完成：成功 %d 个，跳过 %d 个，失败 %d 个", successCount, skippedCount, failedCount),
		"batch_id":      batchID,
		"success_count": successCount,
		"skipped_count": skippedCount,
		"failed_count":  failedCount,
		"results":       results,
	}
}

func (s *AutoGroupService) revertLogEntry(targetLog map[string]interface{}, operator string) map[string]interface{} {
	logID := toInt64(targetLog["id"])
	if toString(targetLog["action"]) != "assign" {
		return map[string]interface{}{
			"success": false,
			"message": "只有分配记录可以撤销",
		}
	}
	if toInt64(targetLog["reverted_at"]) > 0 {
		return map[string]interface{}{
			"success": false,
			"message": "该记录已撤销",
		}
	}
	userIDVal := toInt64(targetLog["user_id"])
	oldGroup := toString(targetLog["old_group"])
	newGroup := toString(targetLog["new_group"])
	username := toString(targetLog["username"])
	source := toString(targetLog["source"])
	batchID := toString(targetLog["batch_id"])

	if userIDVal == 0 {
		return map[string]interface{}{
			"success": false,
			"message": "日志记录缺少用户信息，无法恢复",
		}
	}

	groupCol := s.getGroupCol()

	// Check current user group
	var userSQL string
	if s.db.IsPG {
		userSQL = fmt.Sprintf("SELECT id, %s as user_group FROM users WHERE id = $1 AND deleted_at IS NULL", groupCol)
	} else {
		userSQL = fmt.Sprintf("SELECT id, %s as user_group FROM users WHERE id = ? AND deleted_at IS NULL", groupCol)
	}

	userRow, err := s.db.QueryOne(userSQL, userIDVal)
	if err != nil || userRow == nil {
		return map[string]interface{}{
			"success": false,
			"message": "用户不存在",
		}
	}

	currentGroup := toString(userRow["user_group"])
	if currentGroup == "" {
		currentGroup = "default"
	}

	if currentGroup != newGroup {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("用户当前分组 (%s) 与日志记录不符 (%s)，无法恢复", currentGroup, newGroup),
		}
	}

	// Revert the group
	var updateSQL string
	if s.db.IsPG {
		updateSQL = fmt.Sprintf("UPDATE users SET %s = $1 WHERE id = $2", groupCol)
	} else {
		updateSQL = fmt.Sprintf("UPDATE users SET %s = ? WHERE id = ?", groupCol)
	}

	_, err = s.db.Execute(updateSQL, oldGroup, userIDVal)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("恢复用户分组失败: %v", err),
		}
	}

	revertLogID := s.addUserLogWithMeta("revert", userIDVal, username, newGroup, oldGroup, source, operator, batchID, logID)
	s.markAutoGroupLogReverted(logID, revertLogID)

	logger.L.Business(fmt.Sprintf("自动分组: 用户恢复 user_id=%d username=%s %s -> %s", userIDVal, username, newGroup, oldGroup))

	return map[string]interface{}{
		"success":   true,
		"message":   fmt.Sprintf("用户 %s 已恢复到 %s", username, oldGroup),
		"user_id":   userIDVal,
		"username":  username,
		"old_group": newGroup,
		"new_group": oldGroup,
		"batch_id":  batchID,
	}
}

func generateAutoGroupBatchID(prefix string) string {
	return fmt.Sprintf("%s:%d", prefix, time.Now().UnixNano())
}

func (s *AutoGroupService) getAutoGroupLogEntries() ([]map[string]interface{}, error) {
	cm := cache.Get()
	rdb := cm.RedisClient()
	ctx := context.Background()

	logStrings, err := rdb.LRange(ctx, "auto_group:logs", 0, -1).Result()
	if err != nil {
		return nil, err
	}

	entries := make([]map[string]interface{}, 0, len(logStrings))
	for _, logStr := range logStrings {
		var entry map[string]interface{}
		if json.Unmarshal([]byte(logStr), &entry) == nil {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func (s *AutoGroupService) markAutoGroupLogReverted(logID, revertLogID int64) {
	if logID == 0 {
		return
	}
	cm := cache.Get()
	rdb := cm.RedisClient()
	ctx := context.Background()

	logStrings, err := rdb.LRange(ctx, "auto_group:logs", 0, -1).Result()
	if err != nil {
		logger.L.Error(fmt.Sprintf("读取自动分组日志失败: %v", err))
		return
	}

	now := time.Now().Unix()
	updated := make([]interface{}, 0, len(logStrings))
	for _, logStr := range logStrings {
		var entry map[string]interface{}
		if json.Unmarshal([]byte(logStr), &entry) == nil && toInt64(entry["id"]) == logID {
			entry["reverted_at"] = now
			entry["revert_log_id"] = revertLogID
			if data, err := json.Marshal(entry); err == nil {
				updated = append(updated, string(data))
				continue
			}
		}
		updated = append(updated, logStr)
	}

	pipe := rdb.Pipeline()
	pipe.Del(ctx, "auto_group:logs")
	if len(updated) > 0 {
		pipe.RPush(ctx, "auto_group:logs", updated...)
		pipe.LTrim(ctx, "auto_group:logs", 0, 999)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		logger.L.Error(fmt.Sprintf("标记自动分组日志撤销状态失败: %v", err))
	}
}

// 优化4: addUserLog 使用 Redis LPUSH + LTRIM 原子操作
func (s *AutoGroupService) addUserLog(action string, userID int64, username, oldGroup, newGroup, source, operator string) int64 {
	return s.addUserLogWithMeta(action, userID, username, oldGroup, newGroup, source, operator, "", 0)
}

func (s *AutoGroupService) addUserLogWithMeta(action string, userID int64, username, oldGroup, newGroup, source, operator, batchID string, revertOf int64) int64 {
	cm := cache.Get()
	rdb := cm.RedisClient()
	ctx := context.Background()

	logID := time.Now().UnixNano()
	entry := map[string]interface{}{
		"id":         logID,
		"action":     action,
		"user_id":    userID,
		"username":   username,
		"old_group":  oldGroup,
		"new_group":  newGroup,
		"source":     source,
		"operator":   operator,
		"affected":   1,
		"created_at": time.Now().Unix(),
	}
	if batchID != "" {
		entry["batch_id"] = batchID
	}
	if action == "assign" {
		entry["reverted_at"] = 0
	}
	if revertOf > 0 {
		entry["revert_of"] = revertOf
	}

	data, err := json.Marshal(entry)
	if err != nil {
		logger.L.Error(fmt.Sprintf("序列化自动分组日志失败: %v", err))
		return 0
	}

	// Atomic LPUSH + LTRIM
	rdb.LPush(ctx, "auto_group:logs", string(data))
	rdb.LTrim(ctx, "auto_group:logs", 0, 999) // Keep latest 1000
	return logID
}

// 优化1: addBatchLogs 批量写入日志
func (s *AutoGroupService) addBatchLogs(action string, users []map[string]interface{}, oldGroup, newGroup, operator string) {
	cm := cache.Get()
	rdb := cm.RedisClient()
	ctx := context.Background()

	pipe := rdb.Pipeline()
	baseID := time.Now().UnixNano()
	now := time.Now().Unix()
	for i, user := range users {
		entry := map[string]interface{}{
			"id":         baseID + int64(i),
			"action":     action,
			"user_id":    user["id"],
			"username":   user["username"],
			"old_group":  oldGroup,
			"new_group":  newGroup,
			"source":     user["source"],
			"operator":   operator,
			"affected":   1,
			"created_at": now,
		}
		if action == "assign" {
			entry["reverted_at"] = 0
		}
		data, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		pipe.LPush(ctx, "auto_group:logs", string(data))
	}
	pipe.LTrim(ctx, "auto_group:logs", 0, 999)
	_, err := pipe.Exec(ctx)
	if err != nil {
		logger.L.Error(fmt.Sprintf("批量写入自动分组日志失败: %v", err))
	}
}
