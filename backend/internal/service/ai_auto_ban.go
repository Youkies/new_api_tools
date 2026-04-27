package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/new-api-tools/backend/internal/cache"
	"github.com/new-api-tools/backend/internal/database"
)

// AIAutoBanService handles AI-assisted automatic user banning
type AIAutoBanService struct {
	db *database.Manager
}

// NewAIAutoBanService creates a new AIAutoBanService
func NewAIAutoBanService() *AIAutoBanService {
	return &AIAutoBanService{db: database.Get()}
}

// Default config
var defaultAIBanConfig = map[string]interface{}{
	"base_url":                  "",
	"api_key":                   "",
	"model":                     "",
	"enabled":                   false,
	"dry_run":                   true,
	"scan_interval_minutes":     10,
	"custom_prompt":             "",
	"whitelist_ips":             []string{},
	"blacklist_ips":             []string{},
	"excluded_models":           []string{},
	"excluded_groups":           []string{},
	"pending_review_first":      true,
	"auto_execute_obvious_bans": false,
	"review_scan_limit":         30,
}

const (
	aiPendingReviewsKey = "ai_ban:pending_reviews"
	aiLearningStatsKey  = "ai_ban:learning_stats"
	aiAuditLogsKey      = "ai_ban:audit_logs"
)

// GetConfig returns AI auto ban configuration with computed fields
func (s *AIAutoBanService) GetConfig() map[string]interface{} {
	cm := cache.Get()
	var config map[string]interface{}
	found, _ := cm.GetJSON("ai_ban:config", &config)
	if !found {
		config = make(map[string]interface{})
		for k, v := range defaultAIBanConfig {
			config[k] = v
		}
	}

	// Compute has_api_key and masked_api_key (matching Python backend behavior)
	apiKey, _ := config["api_key"].(string)
	config["has_api_key"] = apiKey != ""

	maskedKey := ""
	if apiKey != "" {
		if len(apiKey) > 8 {
			maskedKey = apiKey[:4] + strings.Repeat("*", len(apiKey)-8) + apiKey[len(apiKey)-4:]
		} else {
			maskedKey = strings.Repeat("*", len(apiKey))
		}
	}
	config["masked_api_key"] = maskedKey
	config["review_policy"] = map[string]interface{}{
		"mode":                       "pending_review_first",
		"review_scan_limit":          toInt64(config["review_scan_limit"]),
		"pending_review_first":       config["pending_review_first"] != false,
		"auto_execute_obvious_bans":  config["auto_execute_obvious_bans"] == true,
		"auto_ban_score_threshold":   10,
		"auto_ban_confidence_floor":  0.96,
		"manual_review_score_floor":  6,
		"assessment_cooldown_hours":  6,
		"review_recommendation_note": "默认进入待处理；仅特征极明显且显式开启自动执行时才自动封禁",
	}

	return config
}

// SaveConfig saves AI auto ban configuration
func (s *AIAutoBanService) SaveConfig(updates map[string]interface{}) error {
	cm := cache.Get()
	// Read raw config from Redis (not via GetConfig which adds computed fields)
	var config map[string]interface{}
	found, _ := cm.GetJSON("ai_ban:config", &config)
	if !found {
		config = make(map[string]interface{})
		for k, v := range defaultAIBanConfig {
			config[k] = v
		}
	}

	// Apply updates
	for k, v := range updates {
		config[k] = v
	}

	// Strip computed fields before saving (they are re-computed in GetConfig)
	delete(config, "has_api_key")
	delete(config, "masked_api_key")

	cm.Set("ai_ban:config", config, 0)
	return nil
}

// ResetAPIHealth resets the API health status
func (s *AIAutoBanService) ResetAPIHealth() map[string]interface{} {
	cm := cache.Get()
	cm.Delete("ai_ban:api_paused")
	return map[string]interface{}{
		"message": "API 健康状态已重置",
		"status":  "healthy",
	}
}

// GetAuditLogs returns AI audit logs
func (s *AIAutoBanService) GetAuditLogs(limit, offset int, status string) map[string]interface{} {
	cm := cache.Get()
	var allLogs []map[string]interface{}
	cm.GetJSON("ai_ban:audit_logs", &allLogs)

	// Filter by status if provided
	filtered := allLogs
	if status != "" {
		filtered = make([]map[string]interface{}, 0)
		for _, log := range allLogs {
			if logStatus, ok := log["status"].(string); ok && logStatus == status {
				filtered = append(filtered, log)
			}
		}
	}

	total := len(filtered)
	// Paginate
	start := offset
	end := offset + limit
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	return map[string]interface{}{
		"items":  filtered[start:end],
		"total":  total,
		"limit":  limit,
		"offset": offset,
	}
}

// ClearAuditLogs clears all AI audit logs
func (s *AIAutoBanService) ClearAuditLogs() map[string]interface{} {
	cm := cache.Get()
	cm.Set("ai_ban:audit_logs", []map[string]interface{}{}, 0)
	return map[string]interface{}{
		"message": "审查记录已清空",
	}
}

// GetAvailableGroups returns groups used in recent logs
func (s *AIAutoBanService) GetAvailableGroups(days int) ([]map[string]interface{}, error) {
	startTime := time.Now().Unix() - int64(days*86400)
	query := s.db.RebindQuery(`
		SELECT DISTINCT group_id as group_name, COUNT(*) as requests
		FROM logs
		WHERE created_at >= ? AND group_id IS NOT NULL AND group_id != ''
		GROUP BY group_id
		ORDER BY requests DESC`)

	rows, err := s.db.Query(query, startTime)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// GetAvailableModelsForExclude returns models used in recent logs
func (s *AIAutoBanService) GetAvailableModelsForExclude(days int) ([]map[string]interface{}, error) {
	startTime := time.Now().Unix() - int64(days*86400)
	query := s.db.RebindQuery(`
		SELECT DISTINCT model_name as model_name, COUNT(*) as requests
		FROM logs
		WHERE created_at >= ? AND model_name IS NOT NULL AND model_name != ''
		GROUP BY model_name
		ORDER BY requests DESC`)

	rows, err := s.db.Query(query, startTime)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// GetSuspiciousUsers returns users with suspicious behavior patterns
func (s *AIAutoBanService) GetSuspiciousUsers(window string, limit int) ([]map[string]interface{}, error) {
	seconds, ok := WindowSeconds[window]
	if !ok {
		seconds = 3600
	}
	startTime := time.Now().Unix() - seconds

	cacheKey := fmt.Sprintf("ai_ban:suspicious:%s:%d", window, limit)
	cm := cache.Get()
	var cached []map[string]interface{}
	found, _ := cm.GetJSON(cacheKey, &cached)
	if found {
		return cached, nil
	}

	// Find users with IP risks, including IPs shared by multiple users.
	query := s.db.RebindQuery(`
		WITH user_stats AS (
			SELECT l.user_id,
				COALESCE(NULLIF(MAX(u.display_name), ''), NULLIF(MAX(u.username), ''), NULLIF(MAX(l.username), ''), '') as username,
				COALESCE(MAX(u.status), 0) as user_status,
				COUNT(*) as total_requests,
				SUM(CASE WHEN l.type = 5 THEN 1 ELSE 0 END) as failure_count,
				SUM(CASE WHEN l.type = 2 THEN 1 ELSE 0 END) as success_count,
				SUM(CASE WHEN l.type = 2 AND COALESCE(l.completion_tokens, 0) = 0 THEN 1 ELSE 0 END) as empty_count,
				COALESCE(SUM(l.quota), 0) as total_quota,
				COUNT(DISTINCT NULLIF(l.ip, '')) as unique_ips,
				COUNT(DISTINCT l.model_name) as unique_models,
				COUNT(DISTINCT l.token_id) as unique_tokens
			FROM logs l
			LEFT JOIN users u ON l.user_id = u.id AND u.deleted_at IS NULL
			WHERE l.created_at >= ? AND l.type IN (2, 5) AND l.user_id IS NOT NULL
			GROUP BY l.user_id
		),
		shared_ips AS (
			SELECT ip, COUNT(DISTINCT user_id) as user_count
			FROM logs
			WHERE created_at >= ? AND ip IS NOT NULL AND ip <> '' AND user_id IS NOT NULL
			GROUP BY ip
			HAVING COUNT(DISTINCT user_id) >= 3
		),
		user_shared_ips AS (
			SELECT l.user_id,
				COUNT(DISTINCT l.ip) as shared_user_ip_count,
				MAX(si.user_count) as max_shared_ip_users
			FROM logs l
			INNER JOIN shared_ips si ON si.ip = l.ip
			WHERE l.created_at >= ? AND l.user_id IS NOT NULL
			GROUP BY l.user_id
		)
		SELECT us.*,
			COALESCE(usi.shared_user_ip_count, 0) as shared_user_ip_count,
			COALESCE(usi.max_shared_ip_users, 0) as max_shared_ip_users
		FROM user_stats us
		LEFT JOIN user_shared_ips usi ON usi.user_id = us.user_id
		WHERE us.total_requests >= 10
			AND us.user_status <> 2
			AND (
				us.unique_ips >= 10
				OR COALESCE(usi.shared_user_ip_count, 0) > 0
				OR us.failure_count > 0
			)
		ORDER BY COALESCE(usi.max_shared_ip_users, 0) DESC,
			COALESCE(usi.shared_user_ip_count, 0) DESC,
			us.unique_ips DESC,
			us.failure_count DESC,
			us.total_requests DESC
		LIMIT ?`)

	rows, err := s.db.Query(query, startTime, startTime, startTime, limit)
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		total := toInt64(row["total_requests"])
		failures := toInt64(row["failure_count"])
		successes := toInt64(row["success_count"])
		emptyCount := toInt64(row["empty_count"])
		uniqueIPs := toInt64(row["unique_ips"])
		sharedUserIPCount := toInt64(row["shared_user_ip_count"])
		maxSharedIPUsers := toInt64(row["max_shared_ip_users"])

		if total > 0 {
			row["failure_rate"] = float64(failures) / float64(total) * 100
		} else {
			row["failure_rate"] = 0.0
		}
		if successes > 0 {
			row["empty_rate"] = float64(emptyCount) / float64(successes) * 100
		} else {
			row["empty_rate"] = 0.0
		}
		row["rpm"] = math.Round((float64(total)/float64(seconds)*60)*10) / 10

		riskFlags := []string{}
		if uniqueIPs >= 10 {
			riskFlags = append(riskFlags, "MANY_IPS")
		}
		if sharedUserIPCount > 0 {
			riskFlags = append(riskFlags, "MULTI_USER_SHARED_IP")
		}
		if failures > 0 && total > 10 && float64(failures)/float64(total) > 0.5 {
			riskFlags = append(riskFlags, "HIGH_FAILURE_RATE")
		}

		row["risk_flags"] = riskFlags
		row["shared_user_ips"] = sharedUserIPCount
		row["max_shared_ip_users"] = maxSharedIPUsers
	}

	cm.Set(cacheKey, rows, 2*time.Minute)
	return rows, nil
}

func (s *AIAutoBanService) assessUserHeuristically(userID int64, window string, analysis map[string]interface{}) map[string]interface{} {
	summary, _ := analysis["summary"].(map[string]interface{})
	risk, _ := analysis["risk"].(map[string]interface{})
	user, _ := analysis["user"].(map[string]interface{})
	ipSwitch, _ := risk["ip_switch_analysis"].(map[string]interface{})
	sharedIP, _ := risk["shared_ip_analysis"].(map[string]interface{})

	riskFlags := toStringSlice(risk["risk_flags"])
	totalRequests := toInt64(summary["total_requests"])
	uniqueIPs := toInt64(summary["unique_ips"])
	uniqueTokens := toInt64(summary["unique_tokens"])
	failureRate := toFloat64(summary["failure_rate"])
	rapidSwitches := toInt64(ipSwitch["rapid_switch_count"])
	realSwitches := toInt64(ipSwitch["real_switch_count"])
	avgIPDuration := toFloat64(ipSwitch["avg_ip_duration"])
	sharedIPCount := toInt64(sharedIP["shared_ip_count"])
	maxSharedIPUsers := toInt64(sharedIP["max_users_per_ip"])

	score := 1
	reasons := []string{}

	if containsString(riskFlags, "MULTI_USER_SHARED_IP") || sharedIPCount > 0 {
		score += 2
		reasons = append(reasons, fmt.Sprintf("命中 %d 个多用户共用 IP", sharedIPCount))
		if maxSharedIPUsers >= 10 {
			score += 3
			reasons = append(reasons, fmt.Sprintf("单个 IP 关联用户数达到 %d", maxSharedIPUsers))
		} else if maxSharedIPUsers >= 5 {
			score += 2
			reasons = append(reasons, fmt.Sprintf("单个 IP 关联用户数较高 (%d)", maxSharedIPUsers))
		}
	}
	if uniqueIPs >= 30 {
		score += 3
		reasons = append(reasons, fmt.Sprintf("使用 IP 数异常偏高 (%d)", uniqueIPs))
	} else if uniqueIPs >= 10 {
		score += 1
		reasons = append(reasons, fmt.Sprintf("使用 IP 数较多 (%d)", uniqueIPs))
	}
	if rapidSwitches >= 8 && avgIPDuration < 120 {
		score += 3
		reasons = append(reasons, fmt.Sprintf("快速切换 IP %d 次且停留时间短", rapidSwitches))
	} else if rapidSwitches >= 3 && avgIPDuration < 300 {
		score += 2
		reasons = append(reasons, fmt.Sprintf("存在快速 IP 切换 (%d 次)", rapidSwitches))
	}
	if realSwitches >= 10 && avgIPDuration < 60 {
		score += 2
		reasons = append(reasons, "真实 IP 跳动频繁")
	}
	if totalRequests >= 2000 {
		score += 2
		reasons = append(reasons, fmt.Sprintf("请求量很高 (%d)", totalRequests))
	} else if totalRequests >= 500 {
		score += 1
		reasons = append(reasons, fmt.Sprintf("请求量较高 (%d)", totalRequests))
	}
	if uniqueTokens >= 10 && totalRequests > 0 && float64(totalRequests)/float64(uniqueTokens) <= 10 {
		score += 2
		reasons = append(reasons, "多令牌低均值轮换")
	}
	if failureRate >= 0.75 && totalRequests >= 50 {
		score += 1
		reasons = append(reasons, "失败率明显偏高")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "未达到明确封禁阈值，建议持续观察")
	}
	if score > 10 {
		score = 10
	}

	confidence := 0.45 + float64(score)*0.045
	if sharedIPCount > 0 {
		confidence += 0.08
	}
	if maxSharedIPUsers >= 10 || (rapidSwitches >= 8 && avgIPDuration < 120) {
		confidence += 0.08
	}
	if confidence > 0.98 {
		confidence = 0.98
	}
	confidence = math.Round(confidence*100) / 100

	action := "monitor"
	shouldBan := false
	if score >= 10 && confidence >= 0.96 && (maxSharedIPUsers >= 10 || (rapidSwitches >= 8 && uniqueIPs >= 30)) {
		action = "ban"
		shouldBan = true
	} else if score >= 6 {
		action = "review"
	} else if score >= 4 {
		action = "warn"
	}

	username := ""
	if user != nil {
		username = toString(user["username"])
	}
	if username == "" {
		username = fmt.Sprintf("用户%d", userID)
	}

	return map[string]interface{}{
		"should_ban":            shouldBan,
		"risk_score":            score,
		"confidence":            confidence,
		"reason":                strings.Join(reasons, "；"),
		"action":                action,
		"policy":                "pending_review_first",
		"username":              username,
		"risk_flags":            riskFlags,
		"total_requests":        totalRequests,
		"unique_ips":            uniqueIPs,
		"shared_user_ips":       sharedIPCount,
		"max_shared_ip_users":   maxSharedIPUsers,
		"rapid_switch_count":    rapidSwitches,
		"real_switch_count":     realSwitches,
		"avg_ip_duration":       avgIPDuration,
		"requires_human_review": action != "ban",
		"review_note":           "默认进入待处理区，由管理员结合上下文确认",
	}
}

func (s *AIAutoBanService) queuePendingReview(userID int64, username, window, source string, assessment map[string]interface{}) map[string]interface{} {
	cm := cache.Get()
	reviews := s.getPendingReviewsRaw()
	now := time.Now().Unix()
	reviewID := fmt.Sprintf("%d:%s", userID, window)

	item := map[string]interface{}{
		"id":                  reviewID,
		"user_id":             userID,
		"username":            username,
		"window":              window,
		"source":              source,
		"status":              "pending",
		"risk_score":          assessment["risk_score"],
		"confidence":          assessment["confidence"],
		"reason":              assessment["reason"],
		"action":              assessment["action"],
		"risk_flags":          assessment["risk_flags"],
		"total_requests":      assessment["total_requests"],
		"unique_ips":          assessment["unique_ips"],
		"shared_user_ips":     assessment["shared_user_ips"],
		"max_shared_ip_users": assessment["max_shared_ip_users"],
		"created_at":          now,
		"updated_at":          now,
	}

	replaced := false
	for i, existing := range reviews {
		if toString(existing["id"]) == reviewID || (toInt64(existing["user_id"]) == userID && toString(existing["status"]) == "pending") {
			if createdAt := toInt64(existing["created_at"]); createdAt > 0 {
				item["created_at"] = createdAt
			}
			reviews[i] = item
			replaced = true
			break
		}
	}
	if !replaced {
		reviews = append([]map[string]interface{}{item}, reviews...)
	}
	if len(reviews) > 500 {
		reviews = reviews[:500]
	}
	cm.Set(aiPendingReviewsKey, reviews, 0)
	return item
}

func (s *AIAutoBanService) getPendingReviewsRaw() []map[string]interface{} {
	cm := cache.Get()
	var reviews []map[string]interface{}
	cm.GetJSON(aiPendingReviewsKey, &reviews)
	if reviews == nil {
		return []map[string]interface{}{}
	}
	return reviews
}

// GetPendingReviews returns users waiting for administrator review.
func (s *AIAutoBanService) GetPendingReviews(limit, offset int, status string) map[string]interface{} {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	if status == "" {
		status = "pending"
	}

	reviews := s.getPendingReviewsRaw()
	filtered := make([]map[string]interface{}, 0, len(reviews))
	for _, item := range reviews {
		if status == "all" || toString(item["status"]) == status {
			filtered = append(filtered, item)
		}
	}

	total := len(filtered)
	end := offset + limit
	if offset > total {
		offset = total
	}
	if end > total {
		end = total
	}

	return map[string]interface{}{
		"items":          filtered[offset:end],
		"total":          total,
		"limit":          limit,
		"offset":         offset,
		"learning_stats": s.GetLearningStats(),
	}
}

// ResolvePendingReview records admin feedback for a pending AI review.
func (s *AIAutoBanService) ResolvePendingReview(reviewID, action, note string) map[string]interface{} {
	if reviewID == "" {
		return map[string]interface{}{"success": false, "message": "缺少 review_id"}
	}
	if action == "" {
		action = "monitor"
	}

	reviews := s.getPendingReviewsRaw()
	now := time.Now().Unix()
	updated := false
	var resolved map[string]interface{}
	for _, item := range reviews {
		if toString(item["id"]) == reviewID {
			item["status"] = action
			item["admin_note"] = note
			item["resolved_at"] = now
			item["updated_at"] = now
			updated = true
			resolved = item
			break
		}
	}
	if !updated {
		return map[string]interface{}{"success": false, "message": "待处理项不存在"}
	}

	cache.Get().Set(aiPendingReviewsKey, reviews, 0)
	s.recordLearningFeedback(resolved, action)
	return map[string]interface{}{
		"success": true,
		"message": "已记录管理员处理结果",
		"item":    resolved,
	}
}

func (s *AIAutoBanService) recordLearningFeedback(item map[string]interface{}, action string) {
	cm := cache.Get()
	stats := s.GetLearningStats()
	total := toInt64(stats["total_feedback"]) + 1
	stats["total_feedback"] = total
	stats["updated_at"] = time.Now().Unix()

	actions, _ := stats["actions"].(map[string]interface{})
	if actions == nil {
		actions = map[string]interface{}{}
	}
	actions[action] = toInt64(actions[action]) + 1
	stats["actions"] = actions

	flags, _ := stats["risk_flags"].(map[string]interface{})
	if flags == nil {
		flags = map[string]interface{}{}
	}
	for _, flag := range toStringSlice(item["risk_flags"]) {
		flagStats, _ := flags[flag].(map[string]interface{})
		if flagStats == nil {
			flagStats = map[string]interface{}{}
		}
		flagStats[action] = toInt64(flagStats[action]) + 1
		flagStats["total"] = toInt64(flagStats["total"]) + 1
		flags[flag] = flagStats
	}
	stats["risk_flags"] = flags
	cm.Set(aiLearningStatsKey, stats, 0)
}

// GetLearningStats returns accumulated admin feedback counters.
func (s *AIAutoBanService) GetLearningStats() map[string]interface{} {
	var stats map[string]interface{}
	cache.Get().GetJSON(aiLearningStatsKey, &stats)
	if stats == nil {
		stats = map[string]interface{}{
			"total_feedback": int64(0),
			"actions":        map[string]interface{}{},
			"risk_flags":     map[string]interface{}{},
			"updated_at":     int64(0),
		}
	}
	return stats
}

func (s *AIAutoBanService) appendAuditLog(entry map[string]interface{}) {
	cm := cache.Get()
	var logs []map[string]interface{}
	cm.GetJSON(aiAuditLogsKey, &logs)
	entry["id"] = len(logs) + 1
	entry["created_at"] = time.Now().Unix()
	logs = append([]map[string]interface{}{entry}, logs...)
	if len(logs) > 300 {
		logs = logs[:300]
	}
	cm.Set(aiAuditLogsKey, logs, 0)
}

func (s *AIAutoBanService) shouldAutoExecuteObviousBan(assessment map[string]interface{}) bool {
	return toString(assessment["action"]) == "ban" &&
		toInt64(assessment["risk_score"]) >= 10 &&
		toFloat64(assessment["confidence"]) >= 0.96
}

func toStringSlice(v interface{}) []string {
	switch val := v.(type) {
	case []string:
		return val
	case []interface{}:
		items := make([]string, 0, len(val))
		for _, item := range val {
			if s := toString(item); s != "" {
				items = append(items, s)
			}
		}
		return items
	default:
		return []string{}
	}
}

func containsString(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}

// ManualAssess performs a cautious assessment on a single user.
func (s *AIAutoBanService) ManualAssess(userID int64, window string) map[string]interface{} {
	seconds, ok := WindowSeconds[window]
	if !ok {
		seconds = 3600
	}
	riskSvc := NewRiskMonitoringService()
	analysis, err := riskSvc.GetUserAnalysis(userID, seconds, nil)
	if err != nil {
		return map[string]interface{}{
			"user_id": userID,
			"window":  window,
			"success": false,
			"message": "用户风险分析失败: " + err.Error(),
		}
	}

	assessment := s.assessUserHeuristically(userID, window, analysis)
	username := toString(assessment["username"])
	var review map[string]interface{}
	if toInt64(assessment["risk_score"]) >= 6 {
		review = s.queuePendingReview(userID, username, window, "manual_assess", assessment)
	}

	return map[string]interface{}{
		"user_id":       userID,
		"username":      username,
		"window":        window,
		"assessment":    assessment,
		"review":        review,
		"would_execute": s.shouldAutoExecuteObviousBan(assessment),
		"assessed":      true,
		"assessed_at":   time.Now().Unix(),
	}
}

// RunScan reviews more candidates but defaults risky decisions into a pending queue.
func (s *AIAutoBanService) RunScan(window string, limit int) map[string]interface{} {
	config := s.GetConfig()
	if configuredLimit := toInt64(config["review_scan_limit"]); configuredLimit > int64(limit) {
		limit = int(configuredLimit)
	}
	if limit < 20 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	start := time.Now()
	candidates, err := s.GetSuspiciousUsers(window, limit)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"message": "获取可疑用户失败: " + err.Error(),
			"window":  window,
		}
	}

	autoExecute := config["auto_execute_obvious_bans"] == true && config["dry_run"] == false
	riskSvc := NewRiskMonitoringService()
	userSvc := NewUserManagementService()
	results := []map[string]interface{}{}
	queued := 0
	banned := 0
	reviewCandidates := 0
	autoBanCandidates := 0
	errors := 0
	seconds, ok := WindowSeconds[window]
	if !ok {
		seconds = 3600
	}

	for _, candidate := range candidates {
		userID := toInt64(candidate["user_id"])
		if userID == 0 {
			continue
		}
		analysis, err := riskSvc.GetUserAnalysis(userID, seconds, nil)
		if err != nil {
			errors++
			results = append(results, map[string]interface{}{
				"user_id": userID,
				"action":  "error",
				"message": err.Error(),
			})
			continue
		}
		assessment := s.assessUserHeuristically(userID, window, analysis)
		username := toString(assessment["username"])
		action := toString(assessment["action"])
		if action == "ban" {
			autoBanCandidates++
		}
		if toInt64(assessment["risk_score"]) >= 6 {
			review := s.queuePendingReview(userID, username, window, "scan", assessment)
			queued++
			reviewCandidates++
			results = append(results, map[string]interface{}{
				"user_id":    userID,
				"username":   username,
				"action":     "review",
				"assessment": assessment,
				"review":     review,
				"executed":   false,
				"message":    "已进入待处理队列，等待管理员复核",
			})
		} else {
			results = append(results, map[string]interface{}{
				"user_id":    userID,
				"username":   username,
				"action":     action,
				"assessment": assessment,
				"executed":   false,
				"message":    "风险较低，继续观察",
			})
		}

		if autoExecute && s.shouldAutoExecuteObviousBan(assessment) {
			if err := userSvc.BanUser(userID, true); err == nil {
				banned++
			} else {
				errors++
			}
		}
	}

	stats := map[string]interface{}{
		"total_scanned":       len(candidates),
		"total_processed":     len(results),
		"queued":              queued,
		"review_candidates":   reviewCandidates,
		"auto_ban_candidates": autoBanCandidates,
		"banned":              banned,
		"errors":              errors,
		"warned":              0,
		"skipped":             0,
	}
	status := "success"
	if len(candidates) == 0 {
		status = "empty"
	} else if errors > 0 {
		status = "partial"
	}

	s.appendAuditLog(map[string]interface{}{
		"scan_id":          fmt.Sprintf("go-%d", time.Now().Unix()),
		"status":           status,
		"window":           window,
		"total_scanned":    len(candidates),
		"total_processed":  len(results),
		"banned_count":     banned,
		"warned_count":     0,
		"skipped_count":    0,
		"error_count":      errors,
		"queued_count":     queued,
		"dry_run":          config["dry_run"] != false,
		"elapsed_seconds":  math.Round(time.Since(start).Seconds()*100) / 100,
		"details":          results,
		"review_policy":    "pending_review_first",
		"auto_execute":     autoExecute,
		"learning_enabled": true,
	})

	return map[string]interface{}{
		"success":         true,
		"dry_run":         config["dry_run"] != false,
		"window":          window,
		"elapsed_seconds": math.Round(time.Since(start).Seconds()*100) / 100,
		"stats":           stats,
		"results":         results,
		"message":         fmt.Sprintf("扫描完成：%d 个候选，%d 个进入待处理，%d 个极高危候选", len(candidates), queued, autoBanCandidates),
	}
}

// TestConnection tests the configured API connection (placeholder)
func (s *AIAutoBanService) TestConnection() map[string]interface{} {
	config := s.GetConfig()
	baseURL, _ := config["base_url"].(string)
	if baseURL == "" {
		return map[string]interface{}{
			"success": false,
			"message": "未配置 API Base URL",
		}
	}
	return map[string]interface{}{
		"success": true,
		"message": "连接测试需要在运行时执行",
	}
}

// getEndpointURL builds the API URL, auto-appending /v1 if needed
func getEndpointURL(baseURL, endpoint string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + endpoint
	}
	return base + "/v1" + endpoint
}

// FetchModels fetches available models from OpenAI-compatible /v1/models API with caching
func (s *AIAutoBanService) FetchModels(baseURL, apiKey string, forceRefresh bool) map[string]interface{} {
	config := s.GetConfig()

	if baseURL == "" {
		baseURL, _ = config["base_url"].(string)
	}
	base := strings.TrimRight(baseURL, "/")

	if apiKey == "" {
		apiKey, _ = config["api_key"].(string)
	}
	if apiKey == "" {
		return map[string]interface{}{
			"success": false,
			"message": "API Key 未配置",
			"models":  []interface{}{},
		}
	}

	cm := cache.Get()
	cacheKey := "ai_ban:models_cache"
	cacheURLKey := "ai_ban:models_cache_url"

	// Check if API URL changed
	var cachedURL string
	if found, _ := cm.GetJSON(cacheURLKey, &cachedURL); found && cachedURL != base {
		forceRefresh = true
	}

	// Check cache (permanent, 30 days TTL)
	if !forceRefresh {
		var cached []map[string]interface{}
		if found, _ := cm.GetJSON(cacheKey, &cached); found && len(cached) > 0 {
			return map[string]interface{}{
				"success": true,
				"message": fmt.Sprintf("获取到 %d 个模型", len(cached)),
				"models":  cached,
			}
		}
	}

	// Call external API
	url := getEndpointURL(base, "/models")
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("创建请求失败: %s", err.Error()),
			"models":  []interface{}{},
		}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		msg := "连接失败，请检查 API 地址"
		if strings.Contains(err.Error(), "timeout") {
			msg = "请求超时，请检查网络或 API 地址"
		}
		return map[string]interface{}{
			"success": false,
			"message": msg,
			"models":  []interface{}{},
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("请求失败: %d", resp.StatusCode),
			"models":  []interface{}{},
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("读取响应失败: %s", err.Error()),
			"models":  []interface{}{},
		}
	}

	var data struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("解析响应失败: %s", err.Error()),
			"models":  []interface{}{},
		}
	}

	// Build model list
	models := make([]map[string]interface{}, 0, len(data.Data))
	for _, m := range data.Data {
		if m.ID != "" {
			models = append(models, map[string]interface{}{
				"id":       m.ID,
				"owned_by": m.OwnedBy,
				"created":  m.Created,
			})
		}
	}

	// Sort by model ID
	sort.Slice(models, func(i, j int) bool {
		return models[i]["id"].(string) < models[j]["id"].(string)
	})

	// Cache permanently (30 days TTL)
	cacheTTL := 30 * 24 * time.Hour
	cm.Set(cacheKey, models, cacheTTL)
	cm.Set(cacheURLKey, base, cacheTTL)

	return map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("获取到 %d 个模型", len(models)),
		"models":  models,
	}
}

// TestModel tests if a specific model is available by sending a chat completion request
func (s *AIAutoBanService) TestModel(baseURL, apiKey, model string) map[string]interface{} {
	config := s.GetConfig()

	if baseURL == "" {
		baseURL, _ = config["base_url"].(string)
	}
	base := strings.TrimRight(baseURL, "/")

	if apiKey == "" {
		apiKey, _ = config["api_key"].(string)
	}
	if apiKey == "" {
		return map[string]interface{}{
			"success": false,
			"message": "API Key 未配置",
		}
	}

	testMessage := "你好，这是一条 API 连接测试消息，请简短回复确认连接正常。"

	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": testMessage},
		},
		"max_tokens": 100,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("序列化请求失败: %s", err.Error()),
		}
	}

	url := getEndpointURL(base, "/chat/completions")
	req, err := http.NewRequest("POST", url, bytes.NewReader(payloadBytes))
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("创建请求失败: %s", err.Error()),
		}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	startTime := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(startTime)

	if err != nil {
		msg := "连接失败，请检查 API 地址"
		if strings.Contains(err.Error(), "timeout") {
			msg = "请求超时"
		}
		return map[string]interface{}{
			"success": false,
			"message": msg,
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("读取响应失败: %s", err.Error()),
		}
	}

	if resp.StatusCode != 200 {
		// Try to extract error detail
		var errResp struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		errorDetail := string(body)
		if len(errorDetail) > 200 {
			errorDetail = errorDetail[:200]
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
			errorDetail = errResp.Error.Message
		}
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("请求失败 (%d): %s", resp.StatusCode, errorDetail),
		}
	}

	var chatResp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(body, &chatResp); err != nil {
		return map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("解析响应失败: %s", err.Error()),
		}
	}

	content := ""
	if len(chatResp.Choices) > 0 {
		content = chatResp.Choices[0].Message.Content
	}
	actualModel := chatResp.Model
	if actualModel == "" {
		actualModel = model
	}

	return map[string]interface{}{
		"success":      true,
		"message":      "连接成功",
		"model":        actualModel,
		"test_message": testMessage,
		"response":     content,
		"latency_ms":   elapsed.Milliseconds(),
		"usage": map[string]int{
			"prompt_tokens":     chatResp.Usage.PromptTokens,
			"completion_tokens": chatResp.Usage.CompletionTokens,
		},
	}
}

// Whitelist management

// GetWhitelist returns the whitelist user IDs
func (s *AIAutoBanService) GetWhitelist() map[string]interface{} {
	cm := cache.Get()
	var whitelist []int64
	cm.GetJSON("ai_ban:whitelist", &whitelist)

	items := make([]map[string]interface{}, 0)
	if len(whitelist) > 0 {
		// Batch query all whitelist users in one query
		placeholders := buildPlaceholders(s.db.IsPG, len(whitelist), 1)
		args := make([]interface{}, len(whitelist))
		for i, uid := range whitelist {
			args[i] = uid
		}
		query := s.db.RebindQuery(fmt.Sprintf(
			"SELECT id, username, status FROM users WHERE id IN (%s)", placeholders))
		rows, err := s.db.Query(query, args...)
		if err == nil && rows != nil {
			items = rows
		}
	}

	return map[string]interface{}{
		"items": items,
		"total": len(items),
	}
}

// AddToWhitelist adds a user to the whitelist
func (s *AIAutoBanService) AddToWhitelist(userID int64) map[string]interface{} {
	cm := cache.Get()
	var whitelist []int64
	cm.GetJSON("ai_ban:whitelist", &whitelist)

	for _, uid := range whitelist {
		if uid == userID {
			return map[string]interface{}{"message": "用户已在白名单中"}
		}
	}
	whitelist = append(whitelist, userID)
	cm.Set("ai_ban:whitelist", whitelist, 0)
	return map[string]interface{}{"message": fmt.Sprintf("用户 %d 已加入白名单", userID)}
}

// RemoveFromWhitelist removes a user from the whitelist
func (s *AIAutoBanService) RemoveFromWhitelist(userID int64) map[string]interface{} {
	cm := cache.Get()
	var whitelist []int64
	cm.GetJSON("ai_ban:whitelist", &whitelist)

	newList := make([]int64, 0)
	for _, uid := range whitelist {
		if uid != userID {
			newList = append(newList, uid)
		}
	}
	cm.Set("ai_ban:whitelist", newList, 0)
	return map[string]interface{}{"message": fmt.Sprintf("用户 %d 已从白名单移除", userID)}
}

// SearchUserForWhitelist searches users for whitelist addition
func (s *AIAutoBanService) SearchUserForWhitelist(keyword string) ([]map[string]interface{}, error) {
	// Try numeric first (user ID)
	var query string
	var args []interface{}
	if id, err := strconv.ParseInt(keyword, 10, 64); err == nil {
		query = s.db.RebindQuery(
			"SELECT id, username, status FROM users WHERE id = ? OR username LIKE ? LIMIT 20")
		args = []interface{}{id, "%" + keyword + "%"}
	} else {
		query = s.db.RebindQuery(
			"SELECT id, username, status FROM users WHERE username LIKE ? LIMIT 20")
		args = []interface{}{"%" + keyword + "%"}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
