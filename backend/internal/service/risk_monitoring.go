package service

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/new-api-tools/backend/internal/cache"
	"github.com/new-api-tools/backend/internal/database"
	"github.com/new-api-tools/backend/internal/logger"
)

// RiskMonitoringService handles risk detection queries
type RiskMonitoringService struct {
	db *database.Manager
}

// RiskActionBatchRequest describes a server-side moderation batch.
type RiskActionBatchRequest struct {
	Action                string                 `json:"action"`
	DryRun                bool                   `json:"dry_run"`
	Reason                string                 `json:"reason"`
	DisableTokens         bool                   `json:"disable_tokens"`
	EnableTokens          bool                   `json:"enable_tokens"`
	Source                string                 `json:"source"`
	UserIDs               []int64                `json:"user_ids"`
	Condition             map[string]interface{} `json:"condition"`
	ExcludeProtectedRoles bool                   `json:"exclude_protected_roles"`
}

// NewRiskMonitoringService creates a new RiskMonitoringService
func NewRiskMonitoringService() *RiskMonitoringService {
	return &RiskMonitoringService{db: database.Get()}
}

// GetLeaderboards returns usage leaderboards across multiple time windows
func (s *RiskMonitoringService) GetLeaderboards(windows []string, limit int, sortBy string, useCache bool) (map[string]interface{}, error) {
	cm := cache.Get()
	cacheKey := fmt.Sprintf("risk:leaderboards:%s:%d:%s", strings.Join(windows, ","), limit, sortBy)
	if useCache {
		var cached map[string]interface{}
		found, _ := cm.GetJSON(cacheKey, &cached)
		if found {
			return cached, nil
		}
	}

	windowsData := map[string]interface{}{}

	// Validate sortBy to prevent SQL injection via ORDER BY expression
	orderBy := "request_count DESC"
	if sortBy == "quota" {
		orderBy = "quota_used DESC"
	} else if sortBy == "failure_rate" {
		orderBy = "failure_rate DESC, request_count DESC"
	}

	for _, window := range windows {
		seconds, ok := WindowSeconds[window]
		if !ok {
			continue
		}
		now := time.Now().Unix()
		startTime := now - seconds

		query := s.db.RebindQuery(fmt.Sprintf(`
			SELECT l.user_id as user_id,
				COALESCE(
					NULLIF(MAX(u.display_name), ''),
					NULLIF(MAX(u.username), ''),
					NULLIF(MAX(l.username), '')
				) as username,
				COALESCE(MAX(u.status), 0) as user_status,
				COUNT(*) as request_count,
				SUM(CASE WHEN l.type = 5 THEN 1 ELSE 0 END) as failure_requests,
				(SUM(CASE WHEN l.type = 5 THEN 1 ELSE 0 END) * 1.0) / NULLIF(COUNT(*), 0) as failure_rate,
				COALESCE(SUM(l.quota), 0) as quota_used,
				COALESCE(SUM(l.prompt_tokens), 0) as prompt_tokens,
				COALESCE(SUM(l.completion_tokens), 0) as completion_tokens,
				COALESCE(COUNT(DISTINCT NULLIF(l.ip, '')), 0) as unique_ips
			FROM logs l
			LEFT JOIN users u ON u.id = l.user_id AND u.deleted_at IS NULL
			WHERE l.created_at >= ? AND l.created_at <= ?
				AND l.type IN (2, 5)
				AND l.user_id IS NOT NULL
			GROUP BY l.user_id
			ORDER BY %s
			LIMIT ?`, orderBy))

		rows, err := s.db.QueryWithTimeout(20*time.Second, query, startTime, now, limit)
		if err != nil {
			windowsData[window] = []map[string]interface{}{}
			continue
		}

		for _, row := range rows {
			s.enrichRiskRow(row, seconds)
		}
		if sortBy == "risk_score" {
			sort.Slice(rows, func(i, j int) bool {
				return toFloat64(rows[i]["risk_score"]) > toFloat64(rows[j]["risk_score"])
			})
		}

		windowsData[window] = rows
	}

	result := map[string]interface{}{
		"windows":      windowsData,
		"generated_at": time.Now().Unix(),
	}

	cm.Set(cacheKey, result, 3*time.Minute)
	return result, nil
}

// GetRiskQueue returns a unified evidence-first queue sorted by risk score.
func (s *RiskMonitoringService) GetRiskQueue(window string, page, pageSize int, sortBy string, useCache bool) (map[string]interface{}, error) {
	seconds, ok := WindowSeconds[window]
	if !ok {
		seconds = WindowSeconds["24h"]
		window = "24h"
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}

	cm := cache.Get()
	cacheKey := fmt.Sprintf("risk:queue:%s:%d:%d:%s", window, page, pageSize, sortBy)
	if useCache {
		var cached map[string]interface{}
		found, _ := cm.GetJSON(cacheKey, &cached)
		if found {
			cached["cache_hit"] = true
			return cached, nil
		}
	}

	now := time.Now().Unix()
	startTime := now - seconds
	candidateLimit := page * pageSize * 3
	if candidateLimit < 100 {
		candidateLimit = 100
	}
	if candidateLimit > 1000 {
		candidateLimit = 1000
	}

	query := s.db.RebindQuery(`
		SELECT l.user_id as user_id,
			COALESCE(
				NULLIF(MAX(u.display_name), ''),
				NULLIF(MAX(u.username), ''),
				NULLIF(MAX(l.username), '')
			) as username,
			COALESCE(MAX(u.status), 0) as user_status,
			COUNT(*) as request_count,
			SUM(CASE WHEN l.type = 5 THEN 1 ELSE 0 END) as failure_requests,
			(SUM(CASE WHEN l.type = 5 THEN 1 ELSE 0 END) * 1.0) / NULLIF(COUNT(*), 0) as failure_rate,
			COALESCE(SUM(l.quota), 0) as quota_used,
			COALESCE(SUM(l.prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(l.completion_tokens), 0) as completion_tokens,
			COALESCE(COUNT(DISTINCT NULLIF(l.ip, '')), 0) as unique_ips,
			COALESCE(COUNT(DISTINCT l.token_id), 0) as unique_tokens
		FROM logs l
		LEFT JOIN users u ON u.id = l.user_id AND u.deleted_at IS NULL
		WHERE l.created_at >= ? AND l.created_at <= ?
			AND l.type IN (2, 5)
			AND l.user_id IS NOT NULL
		GROUP BY l.user_id
		ORDER BY request_count DESC
		LIMIT ?`)

	rows, err := s.db.QueryWithTimeout(25*time.Second, query, startTime, now, candidateLimit)
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		s.enrichRiskRow(row, seconds)
	}

	sort.Slice(rows, func(i, j int) bool {
		switch sortBy {
		case "requests":
			return toInt64(rows[i]["request_count"]) > toInt64(rows[j]["request_count"])
		case "quota":
			return toInt64(rows[i]["quota_used"]) > toInt64(rows[j]["quota_used"])
		case "failure_rate":
			return toFloat64(rows[i]["failure_rate"]) > toFloat64(rows[j]["failure_rate"])
		default:
			return toFloat64(rows[i]["risk_score"]) > toFloat64(rows[j]["risk_score"])
		}
	})

	start := (page - 1) * pageSize
	if start > len(rows) {
		start = len(rows)
	}
	end := start + pageSize
	if end > len(rows) {
		end = len(rows)
	}
	items := rows[start:end]

	result := map[string]interface{}{
		"items":          items,
		"page":           page,
		"page_size":      pageSize,
		"total":          len(rows),
		"window":         window,
		"sort":           sortBy,
		"generated_at":   now,
		"snapshot_time":  now,
		"candidate_size": candidateLimit,
		"cache_hit":      false,
	}

	cm.Set(cacheKey, result, 2*time.Minute)
	return result, nil
}

func (s *RiskMonitoringService) enrichRiskRow(row map[string]interface{}, windowSeconds int64) {
	requestCount := toInt64(row["request_count"])
	failureRate := toFloat64(row["failure_rate"])
	quotaUsed := toInt64(row["quota_used"])
	uniqueIPs := toInt64(row["unique_ips"])
	uniqueTokens := toInt64(row["unique_tokens"])
	if uniqueTokens == 0 {
		uniqueTokens = 1
	}

	windowMinutes := math.Max(float64(windowSeconds)/60.0, 1)
	requestsPerMinute := float64(requestCount) / windowMinutes

	reasons := buildRiskReasons(requestCount, requestsPerMinute, failureRate, quotaUsed, uniqueIPs, uniqueTokens)
	score := calculateRiskScore(requestCount, requestsPerMinute, failureRate, quotaUsed, uniqueIPs, uniqueTokens)
	level := "low"
	suggestedAction := "monitor"
	if score >= 80 {
		level = "high"
		suggestedAction = "review"
	} else if score >= 50 {
		level = "medium"
		suggestedAction = "review"
	}

	row["requests_per_minute"] = math.Round(requestsPerMinute*100) / 100
	row["risk_score"] = score
	row["risk_level"] = level
	row["risk_reasons"] = reasons
	row["reasons"] = reasons
	row["suggested_action"] = suggestedAction
	row["metrics"] = map[string]interface{}{
		"request_count":       requestCount,
		"requests_per_minute": math.Round(requestsPerMinute*100) / 100,
		"failure_rate":        failureRate,
		"quota_used":          quotaUsed,
		"unique_ips":          uniqueIPs,
		"unique_tokens":       uniqueTokens,
	}
}

func calculateRiskScore(requestCount int64, rpm float64, failureRate float64, quotaUsed int64, uniqueIPs int64, uniqueTokens int64) int {
	velocityScore := math.Min(rpm/10.0*100, 100)
	requestVolumeScore := math.Min(float64(requestCount)/3000.0*100, 100)
	failureScore := math.Min(failureRate/0.5*100, 100)
	ipScore := math.Min(float64(uniqueIPs)/12.0*100, 100)
	tokenScore := math.Min(float64(uniqueTokens)/8.0*100, 100)
	costScore := math.Min(float64(quotaUsed)/5000000.0*100, 100)

	score := velocityScore*0.25 +
		requestVolumeScore*0.10 +
		failureScore*0.20 +
		ipScore*0.20 +
		tokenScore*0.10 +
		costScore*0.15

	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return int(math.Round(score))
}

func buildRiskReasons(requestCount int64, rpm float64, failureRate float64, quotaUsed int64, uniqueIPs int64, uniqueTokens int64) []map[string]interface{} {
	reasons := []map[string]interface{}{}
	add := func(code, label, severity, evidence, suggestion string) {
		reasons = append(reasons, map[string]interface{}{
			"code":       code,
			"label":      label,
			"severity":   severity,
			"evidence":   evidence,
			"suggestion": suggestion,
		})
	}

	if rpm >= 10 {
		add("HIGH_RPM", "短时间高频请求", "high",
			fmt.Sprintf("当前窗口平均 %.2f RPM", rpm),
			"检查是否为异常脚本或被盗用令牌")
	} else if rpm >= 5 {
		add("ELEVATED_RPM", "请求频率偏高", "medium",
			fmt.Sprintf("当前窗口平均 %.2f RPM", rpm),
			"结合 IP 和失败率继续观察")
	}
	if requestCount >= 3000 {
		add("HIGH_REQUEST_VOLUME", "请求量异常偏高", "high",
			fmt.Sprintf("当前窗口累计 %d 次请求", requestCount),
			"优先查看用户详情和原始日志")
	} else if requestCount >= 1000 {
		add("ELEVATED_REQUEST_VOLUME", "请求量偏高", "medium",
			fmt.Sprintf("当前窗口累计 %d 次请求", requestCount),
			"确认是否为正常业务流量")
	}
	if failureRate >= 0.5 && requestCount >= 20 {
		add("HIGH_FAILURE_RATE", "失败率过高", "high",
			fmt.Sprintf("失败率 %.1f%%", failureRate*100),
			"检查模型、渠道或恶意探测请求")
	} else if failureRate >= 0.25 && requestCount >= 20 {
		add("ELEVATED_FAILURE_RATE", "失败率偏高", "medium",
			fmt.Sprintf("失败率 %.1f%%", failureRate*100),
			"结合模型和渠道分布确认原因")
	}
	if uniqueIPs >= 12 {
		add("MANY_IPS", "多 IP 访问", "high",
			fmt.Sprintf("当前窗口出现 %d 个不同 IP", uniqueIPs),
			"检查是否存在代理池或账号共享")
	} else if uniqueIPs >= 5 {
		add("MULTIPLE_IPS", "IP 数偏多", "medium",
			fmt.Sprintf("当前窗口出现 %d 个不同 IP", uniqueIPs),
			"结合地理位置和令牌使用继续观察")
	}
	if uniqueTokens >= 8 {
		add("TOKEN_ROTATION", "令牌轮换明显", "medium",
			fmt.Sprintf("当前窗口使用 %d 个不同令牌", uniqueTokens),
			"检查是否存在批量分发或泄露")
	}
	if quotaUsed >= 5000000 {
		add("HIGH_QUOTA_USAGE", "额度消耗较高", "medium",
			fmt.Sprintf("当前窗口消耗 %d quota", quotaUsed),
			"结合充值和成本核算确认业务合理性")
	}
	if len(reasons) == 0 {
		add("NORMAL_TRAFFIC", "未命中明显风险", "low",
			"请求量、失败率、IP 和额度消耗均未超过主要阈值",
			"保持观察")
	}
	return reasons
}

// GetUserAnalysis returns detailed risk analysis for a user
func (s *RiskMonitoringService) GetUserAnalysis(userID int64, windowSeconds int64, endTime *int64) (map[string]interface{}, error) {
	now := time.Now().Unix()
	if endTime != nil {
		now = *endTime
	}
	startTime := now - windowSeconds

	// User info
	groupCol := "`group`"
	if s.db.IsPG {
		groupCol = `"group"`
	}
	userRow, _ := s.db.QueryOne(s.db.RebindQuery(
		fmt.Sprintf("SELECT id, username, display_name, email, status, %s, remark, linux_do_id, request_count FROM users WHERE id = ? AND deleted_at IS NULL", groupCol)), userID)

	// Build user object
	userInfo := map[string]interface{}{
		"id":           userID,
		"username":     "",
		"display_name": nil,
		"email":        nil,
		"status":       1,
		"group":        nil,
		"remark":       nil,
		"linux_do_id":  nil,
	}
	if userRow != nil {
		userInfo["id"] = userRow["id"]
		userInfo["username"] = userRow["username"]
		userInfo["display_name"] = userRow["display_name"]
		userInfo["email"] = userRow["email"]
		userInfo["status"] = userRow["status"]
		userInfo["group"] = userRow["group"]
		userInfo["remark"] = userRow["remark"]
		userInfo["linux_do_id"] = userRow["linux_do_id"]
	}

	// Usage stats in window
	statsQuery := s.db.RebindQuery(`
		SELECT COUNT(*) as total_requests,
			SUM(CASE WHEN type = 2 THEN 1 ELSE 0 END) as success_requests,
			SUM(CASE WHEN type = 5 THEN 1 ELSE 0 END) as failure_requests,
			COALESCE(SUM(quota), 0) as quota_used,
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COUNT(DISTINCT NULLIF(ip, '')) as unique_ips,
			COUNT(DISTINCT token_id) as unique_tokens,
			COUNT(DISTINCT model_name) as unique_models,
			COUNT(DISTINCT channel_id) as unique_channels,
			SUM(CASE WHEN type = 2 AND completion_tokens = 0 THEN 1 ELSE 0 END) as empty_count
		FROM logs
		WHERE user_id = ? AND created_at >= ? AND created_at <= ? AND type IN (2, 5)`)

	statsRow, _ := s.db.QueryOne(statsQuery, userID, startTime, now)

	totalRequests := int64(0)
	successRequests := int64(0)
	failureRequests := int64(0)
	quotaUsed := int64(0)
	promptTokens := int64(0)
	completionTokens := int64(0)
	uniqueIPs := int64(0)
	uniqueTokens := int64(0)
	uniqueModels := int64(0)
	uniqueChannels := int64(0)
	emptyCount := int64(0)

	if statsRow != nil {
		totalRequests = toInt64(statsRow["total_requests"])
		successRequests = toInt64(statsRow["success_requests"])
		failureRequests = toInt64(statsRow["failure_requests"])
		quotaUsed = toInt64(statsRow["quota_used"])
		promptTokens = toInt64(statsRow["prompt_tokens"])
		completionTokens = toInt64(statsRow["completion_tokens"])
		uniqueIPs = toInt64(statsRow["unique_ips"])
		uniqueTokens = toInt64(statsRow["unique_tokens"])
		uniqueModels = toInt64(statsRow["unique_models"])
		uniqueChannels = toInt64(statsRow["unique_channels"])
		emptyCount = toInt64(statsRow["empty_count"])
	}

	// Calculate rates
	failureRate := 0.0
	emptyRate := 0.0
	if totalRequests > 0 {
		failureRate = float64(failureRequests) / float64(totalRequests)
	}
	if successRequests > 0 {
		emptyRate = float64(emptyCount) / float64(successRequests)
	}

	// Average use time
	avgUseTimeQuery := s.db.RebindQuery(`
		SELECT COALESCE(AVG(use_time), 0) as avg_use_time
		FROM logs
		WHERE user_id = ? AND created_at >= ? AND created_at <= ? AND type = 2`)
	avgRow, _ := s.db.QueryOne(avgUseTimeQuery, userID, startTime, now)
	avgUseTime := 0.0
	if avgRow != nil {
		if v, ok := avgRow["avg_use_time"].(float64); ok {
			avgUseTime = v
		} else {
			avgUseTime = float64(toInt64(avgRow["avg_use_time"]))
		}
	}

	// Summary
	summary := map[string]interface{}{
		"total_requests":    totalRequests,
		"success_requests":  successRequests,
		"failure_requests":  failureRequests,
		"quota_used":        quotaUsed,
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"avg_use_time":      avgUseTime,
		"unique_ips":        uniqueIPs,
		"unique_tokens":     uniqueTokens,
		"unique_models":     uniqueModels,
		"unique_channels":   uniqueChannels,
		"empty_count":       emptyCount,
		"failure_rate":      failureRate,
		"empty_rate":        emptyRate,
	}

	// Risk analysis
	windowMinutes := float64(windowSeconds) / 60.0
	requestsPerMinute := 0.0
	if windowMinutes > 0 {
		requestsPerMinute = float64(totalRequests) / windowMinutes
	}

	avgQuotaPerRequest := 0.0
	if totalRequests > 0 {
		avgQuotaPerRequest = float64(quotaUsed) / float64(totalRequests)
	}

	// IP switch analysis — fetch IP sequence ordered by time
	ipSeqQuery := s.db.RebindQuery(`
		SELECT created_at, ip
		FROM logs
		WHERE user_id = ? AND created_at >= ? AND created_at <= ?
			AND type IN (2, 5) AND ip IS NOT NULL AND ip != ''
		ORDER BY created_at ASC`)
	ipSequence, _ := s.db.Query(ipSeqQuery, userID, startTime, now)
	if ipSequence == nil {
		ipSequence = []map[string]interface{}{}
	}
	ipSwitchAnalysis := analyzeIPSwitches(ipSequence)
	sharedIPAnalysis := s.getSharedIPAnalysis(userID, startTime, now, 3, 20)

	// Risk flags
	riskFlags := []string{}
	if requestsPerMinute > 5.0 {
		riskFlags = append(riskFlags, "HIGH_RPM")
	}
	if uniqueIPs > 10 {
		riskFlags = append(riskFlags, "MANY_IPS")
	}
	if failureRate > 0.5 && totalRequests > 10 {
		riskFlags = append(riskFlags, "HIGH_FAILURE_RATE")
	}

	// IP switch risk flags (matching Python logic)
	avgIPDuration := toFloat64(ipSwitchAnalysis["avg_ip_duration"])
	rapidSwitchCount := toInt64(ipSwitchAnalysis["rapid_switch_count"])
	realSwitchCount := toInt64(ipSwitchAnalysis["real_switch_count"])
	if rapidSwitchCount >= 3 && avgIPDuration < 300 {
		riskFlags = append(riskFlags, "IP_RAPID_SWITCH")
	}
	if avgIPDuration < 30 && realSwitchCount >= 3 {
		riskFlags = append(riskFlags, "IP_HOPPING")
	}
	if toInt64(sharedIPAnalysis["shared_ip_count"]) > 0 {
		riskFlags = append(riskFlags, "MULTI_USER_SHARED_IP")
	}

	// Checkin anomaly detection
	checkin := analyzeCheckins(s.db, userID, startTime, now)
	var checkinAnalysisMap map[string]interface{}
	if checkin != nil && checkin.CheckinCount > 0 {
		requestsPerCheckin := float64(0)
		if checkin.CheckinCount > 0 {
			requestsPerCheckin = float64(totalRequests) / float64(checkin.CheckinCount)
		}
		checkin.RequestsPerCheckin = math.Round(requestsPerCheckin*10) / 10

		checkinAnalysisMap = map[string]interface{}{
			"checkin_count":        checkin.CheckinCount,
			"total_quota_awarded":  checkin.TotalQuotaAwarded,
			"requests_per_checkin": checkin.RequestsPerCheckin,
		}

		// Flag: many checkins but very few requests per checkin
		if checkin.CheckinCount > 3 && requestsPerCheckin < 5 {
			riskFlags = append(riskFlags, "CHECKIN_ANOMALY")
		}
	}

	riskProbe := map[string]interface{}{
		"request_count": totalRequests,
		"failure_rate":  failureRate,
		"quota_used":    quotaUsed,
		"unique_ips":    uniqueIPs,
		"unique_tokens": uniqueTokens,
	}
	s.enrichRiskRow(riskProbe, windowSeconds)

	risk := map[string]interface{}{
		"requests_per_minute":   requestsPerMinute,
		"avg_quota_per_request": avgQuotaPerRequest,
		"risk_flags":            riskFlags,
		"risk_score":            riskProbe["risk_score"],
		"risk_level":            riskProbe["risk_level"],
		"risk_reasons":          riskProbe["risk_reasons"],
		"ip_switch_analysis":    ipSwitchAnalysis,
		"shared_ip_analysis":    sharedIPAnalysis,
	}
	if checkinAnalysisMap != nil {
		risk["checkin_analysis"] = checkinAnalysisMap
	}

	// Top models
	modelsQuery := s.db.RebindQuery(`
		SELECT COALESCE(model_name, 'unknown') as model_name, COUNT(*) as requests,
			COALESCE(SUM(quota), 0) as quota_used,
			SUM(CASE WHEN type = 2 THEN 1 ELSE 0 END) as success_requests,
			SUM(CASE WHEN type = 5 THEN 1 ELSE 0 END) as failure_requests,
			SUM(CASE WHEN type = 2 AND completion_tokens = 0 THEN 1 ELSE 0 END) as empty_count
		FROM logs
		WHERE user_id = ? AND created_at >= ? AND created_at <= ? AND type IN (2, 5)
		GROUP BY COALESCE(model_name, 'unknown')
		ORDER BY requests DESC
		LIMIT 10`)

	topModels, _ := s.db.Query(modelsQuery, userID, startTime, now)
	if topModels == nil {
		topModels = []map[string]interface{}{}
	}

	// Top channels
	channelsQuery := s.db.RebindQuery(`
		SELECT channel_id, COALESCE(MAX(channel_name), '') as channel_name,
			COUNT(*) as requests,
			COALESCE(SUM(quota), 0) as quota_used
		FROM logs
		WHERE user_id = ? AND created_at >= ? AND created_at <= ? AND type IN (2, 5)
		GROUP BY channel_id
		ORDER BY requests DESC
		LIMIT 10`)

	topChannels, _ := s.db.Query(channelsQuery, userID, startTime, now)
	if topChannels == nil {
		topChannels = []map[string]interface{}{}
	}

	// Top IPs
	ipsQuery := s.db.RebindQuery(`
		SELECT ip, COUNT(*) as requests
		FROM logs
		WHERE user_id = ? AND created_at >= ? AND created_at <= ? AND ip IS NOT NULL AND ip != ''
		GROUP BY ip
		ORDER BY requests DESC
		LIMIT 20`)

	topIPs, _ := s.db.Query(ipsQuery, userID, startTime, now)
	if topIPs == nil {
		topIPs = []map[string]interface{}{}
	}

	// Recent logs (token_name and channel_name are directly in logs table)
	recentLogsQuery := s.db.RebindQuery(`
		SELECT id, created_at, type, COALESCE(model_name,'') as model_name,
			COALESCE(quota, 0) as quota,
			COALESCE(prompt_tokens, 0) as prompt_tokens,
			COALESCE(completion_tokens, 0) as completion_tokens,
			COALESCE(use_time, 0) as use_time,
			COALESCE(ip, '') as ip,
			COALESCE(channel_id, 0) as channel_id,
			COALESCE(channel_name, '') as channel_name,
			COALESCE(token_id, 0) as token_id,
			COALESCE(token_name, '') as token_name
		FROM logs
		WHERE user_id = ? AND created_at >= ? AND created_at <= ? AND type IN (2, 5)
		ORDER BY id DESC
		LIMIT 50`)

	recentLogs, _ := s.db.Query(recentLogsQuery, userID, startTime, now)
	if recentLogs == nil {
		recentLogs = []map[string]interface{}{}
	}

	result := map[string]interface{}{
		"range": map[string]interface{}{
			"start_time":     startTime,
			"end_time":       now,
			"window_seconds": windowSeconds,
		},
		"user":         userInfo,
		"summary":      summary,
		"risk":         risk,
		"top_models":   topModels,
		"top_channels": topChannels,
		"top_ips":      topIPs,
		"recent_logs":  recentLogs,
	}

	return result, nil
}

func (s *RiskMonitoringService) getSharedIPAnalysis(userID int64, startTime, endTime int64, minUsers, limit int) map[string]interface{} {
	empty := map[string]interface{}{
		"shared_ip_count":  int64(0),
		"max_users_per_ip": int64(0),
		"ips":              []map[string]interface{}{},
	}

	if minUsers < 2 {
		minUsers = 2
	}

	query := s.db.RebindQuery(`
		WITH user_ips AS (
			SELECT DISTINCT ip
			FROM logs
			WHERE user_id = ? AND created_at >= ? AND created_at <= ?
				AND ip IS NOT NULL AND ip <> ''
		)
		SELECT l.ip,
			COUNT(DISTINCT l.user_id) as user_count,
			COUNT(DISTINCT l.token_id) as token_count,
			COUNT(*) as request_count
		FROM logs l
		INNER JOIN user_ips ui ON ui.ip = l.ip
		WHERE l.created_at >= ? AND l.created_at <= ?
			AND l.ip IS NOT NULL AND l.ip <> ''
			AND l.user_id IS NOT NULL
		GROUP BY l.ip
		HAVING COUNT(DISTINCT l.user_id) >= ?
		ORDER BY user_count DESC, request_count DESC
		LIMIT ?`)

	rows, err := s.db.Query(query, userID, startTime, endTime, startTime, endTime, minUsers, limit)
	if err != nil || len(rows) == 0 {
		return empty
	}

	maxUsers := int64(0)
	for _, row := range rows {
		userCount := toInt64(row["user_count"])
		if userCount > maxUsers {
			maxUsers = userCount
		}
	}

	return map[string]interface{}{
		"shared_ip_count":  int64(len(rows)),
		"max_users_per_ip": maxUsers,
		"ips":              rows,
	}
}

// GetTokenRotationUsers detects token rotation behavior
func (s *RiskMonitoringService) GetTokenRotationUsers(window string, minTokens, maxReqPerToken, limit int) (map[string]interface{}, error) {
	seconds, ok := WindowSeconds[window]
	if !ok {
		seconds = 86400
	}
	startTime := time.Now().Unix() - seconds

	cacheKey := fmt.Sprintf("risk:token_rotation:%s:%d:%d:%d", window, minTokens, maxReqPerToken, limit)
	cm := cache.Get()
	var cached map[string]interface{}
	found, _ := cm.GetJSON(cacheKey, &cached)
	if found {
		return cached, nil
	}

	query := s.db.RebindQuery(`
		SELECT l.user_id, COALESCE(u.username, '') as username,
			COUNT(DISTINCT l.token_id) as token_count,
			COUNT(*) as total_requests
		FROM logs l
		LEFT JOIN users u ON l.user_id = u.id
		WHERE l.created_at >= ? AND l.type IN (2, 5)
		GROUP BY l.user_id, u.username
		HAVING COUNT(DISTINCT l.token_id) >= ?
			AND (COUNT(*) * 1.0 / COUNT(DISTINCT l.token_id)) <= ?
		ORDER BY token_count DESC
		LIMIT ?`)

	rows, err := s.db.Query(query, startTime, minTokens, maxReqPerToken, limit)
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		total := toInt64(row["total_requests"])
		tokens := toInt64(row["token_count"])
		if tokens > 0 {
			row["avg_requests_per_token"] = float64(total) / float64(tokens)
		}
	}

	result := map[string]interface{}{
		"items":  rows,
		"total":  len(rows),
		"window": window,
	}

	cm.Set(cacheKey, result, 5*time.Minute)
	return result, nil
}

// GetAffiliatedAccounts detects accounts from same inviter
func (s *RiskMonitoringService) GetAffiliatedAccounts(minInvited, limit int) (map[string]interface{}, error) {
	cacheKey := fmt.Sprintf("risk:affiliated:%d:%d", minInvited, limit)
	cm := cache.Get()
	var cached map[string]interface{}
	found, _ := cm.GetJSON(cacheKey, &cached)
	if found {
		return cached, nil
	}

	query := s.db.RebindQuery(`
		SELECT inviter_id, COUNT(*) as invited_count
		FROM users
		WHERE inviter_id IS NOT NULL AND inviter_id > 0 AND deleted_at IS NULL
		GROUP BY inviter_id
		HAVING COUNT(*) >= ?
		ORDER BY invited_count DESC
		LIMIT ?`)

	rows, err := s.db.Query(query, minInvited, limit)
	if err != nil {
		return nil, err
	}

	result := map[string]interface{}{
		"items":       rows,
		"total":       len(rows),
		"min_invited": minInvited,
	}

	cm.Set(cacheKey, result, 10*time.Minute)
	return result, nil
}

// GetSameIPRegistrations detects accounts registered from same IP
func (s *RiskMonitoringService) GetSameIPRegistrations(window string, minUsers, limit int) (map[string]interface{}, error) {
	seconds, ok := WindowSeconds[window]
	if !ok {
		seconds = 604800
	}
	startTime := time.Now().Unix() - seconds

	cacheKey := fmt.Sprintf("risk:same_ip:%s:%d:%d", window, minUsers, limit)
	cm := cache.Get()
	var cached map[string]interface{}
	found, _ := cm.GetJSON(cacheKey, &cached)
	if found {
		return cached, nil
	}

	// Find IPs with first requests from multiple users
	query := s.db.RebindQuery(`
		SELECT first_ip, COUNT(*) as user_count
		FROM (
			SELECT user_id, ip as first_ip
			FROM logs
			WHERE type IN (2, 5) AND ip IS NOT NULL AND ip != ''
			AND created_at >= ?
			GROUP BY user_id, ip
		) sub
		GROUP BY first_ip
		HAVING COUNT(*) >= ?
		ORDER BY user_count DESC
		LIMIT ?`)

	rows, err := s.db.Query(query, startTime, minUsers, limit)
	if err != nil {
		return nil, err
	}

	result := map[string]interface{}{
		"items":     rows,
		"total":     len(rows),
		"window":    window,
		"min_users": minUsers,
	}

	cm.Set(cacheKey, result, 10*time.Minute)
	return result, nil
}

// ExecuteRiskActionBatch previews or executes a server-side moderation batch.
func (s *RiskMonitoringService) ExecuteRiskActionBatch(req RiskActionBatchRequest) (map[string]interface{}, error) {
	if req.Action == "" {
		req.Action = "ban"
	}
	if req.Reason == "" {
		req.Reason = "risk_center_batch"
	}
	if req.Source == "" {
		req.Source = "risk_center"
	}
	if req.Action == "ban" && !req.DisableTokens {
		req.DisableTokens = true
	}
	if req.Action == "unban" && !req.EnableTokens {
		req.EnableTokens = true
	}
	if !req.ExcludeProtectedRoles {
		req.ExcludeProtectedRoles = true
	}

	targets, err := s.resolveRiskBatchTargets(req)
	if err != nil {
		return nil, err
	}

	batchID := fmt.Sprintf("risk-%s-%d", req.Action, time.Now().UnixNano())
	result := map[string]interface{}{
		"batch_id":       batchID,
		"action":         req.Action,
		"dry_run":        req.DryRun,
		"affected_count": len(targets),
		"skipped_count":  0,
		"users":          targets,
		"message":        "预览完成，未修改用户状态",
	}
	if req.DryRun {
		return result, nil
	}

	userSvc := NewUserManagementService()
	succeeded := []map[string]interface{}{}
	failed := []map[string]interface{}{}
	for _, target := range targets {
		userID := toInt64(target["user_id"])
		context := map[string]interface{}{
			"source":     req.Source,
			"batch_id":   batchID,
			"condition":  req.Condition,
			"risk_batch": true,
		}
		if req.Action == "unban" {
			err = userSvc.UnbanUserWithAudit(userID, req.EnableTokens, req.Reason, "Admin", context)
		} else {
			err = userSvc.BanUserWithAudit(userID, req.DisableTokens, req.Reason, "Admin", context)
		}
		if err != nil {
			failed = append(failed, map[string]interface{}{"user_id": userID, "error": err.Error()})
			continue
		}
		succeeded = append(succeeded, target)
	}

	if err := s.ensureRiskActionBatchTable(); err == nil {
		_ = s.insertRiskActionBatch(batchID, req, succeeded, failed)
	}

	result["dry_run"] = false
	result["affected_count"] = len(succeeded)
	result["failed_count"] = len(failed)
	result["users"] = succeeded
	result["failed"] = failed
	result["message"] = fmt.Sprintf("已执行批量%s，成功 %d 个，失败 %d 个", riskActionLabel(req.Action), len(succeeded), len(failed))
	invalidateRiskCaches()
	return result, nil
}

// RevertRiskActionBatch reverts a previously executed ban batch when possible.
func (s *RiskMonitoringService) RevertRiskActionBatch(batchID string) (map[string]interface{}, error) {
	if batchID == "" {
		return nil, fmt.Errorf("batch_id is required")
	}
	if err := s.ensureRiskActionBatchTable(); err != nil {
		return nil, err
	}
	row, err := s.db.QueryOne(s.db.RebindQuery(`
		SELECT batch_id, action, affected_users_snapshot, reverted_at
		FROM api_tools_risk_action_batches WHERE batch_id = ?`), batchID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("batch not found")
	}
	if toInt64(row["reverted_at"]) > 0 {
		return nil, fmt.Errorf("batch already reverted")
	}
	if toString(row["action"]) != "ban" {
		return nil, fmt.Errorf("only ban batches can be reverted automatically")
	}

	var users []map[string]interface{}
	if err := json.Unmarshal([]byte(toString(row["affected_users_snapshot"])), &users); err != nil {
		return nil, err
	}

	userSvc := NewUserManagementService()
	succeeded := 0
	failed := []map[string]interface{}{}
	for _, user := range users {
		userID := toInt64(user["user_id"])
		err := userSvc.UnbanUserWithAudit(userID, true, "撤销批量封禁", "Admin", map[string]interface{}{
			"source":        "risk_batch_revert",
			"undo_batch_id": batchID,
		})
		if err != nil {
			failed = append(failed, map[string]interface{}{"user_id": userID, "error": err.Error()})
			continue
		}
		succeeded++
	}

	now := time.Now().Unix()
	_, _ = s.db.Execute(s.db.RebindQuery(`
		UPDATE api_tools_risk_action_batches SET reverted_at = ? WHERE batch_id = ?`), now, batchID)
	invalidateRiskCaches()
	return map[string]interface{}{
		"batch_id":       batchID,
		"reverted_at":    now,
		"success_count":  succeeded,
		"failed_count":   len(failed),
		"failed":         failed,
		"message":        fmt.Sprintf("已撤销批量封禁，成功解封 %d 个，失败 %d 个", succeeded, len(failed)),
		"affected_count": succeeded,
	}, nil
}

func (s *RiskMonitoringService) resolveRiskBatchTargets(req RiskActionBatchRequest) ([]map[string]interface{}, error) {
	protectedCondition := "AND COALESCE(u.role, 0) < 10"
	if !req.ExcludeProtectedRoles {
		protectedCondition = ""
	}
	userIDCast := "CAST(u.id AS CHAR)"
	logUserIDCast := "CAST(l.user_id AS CHAR)"
	if s.db.IsPG {
		userIDCast = "CAST(u.id AS TEXT)"
		logUserIDCast = "CAST(l.user_id AS TEXT)"
	}
	if len(req.UserIDs) > 0 {
		placeholders := make([]string, 0, len(req.UserIDs))
		args := make([]interface{}, 0, len(req.UserIDs))
		for _, id := range req.UserIDs {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		query := s.db.RebindQuery(fmt.Sprintf(`
			SELECT u.id as user_id, COALESCE(NULLIF(u.display_name, ''), u.username, %s) as username,
				u.status as user_status, u.role
			FROM users u
			WHERE u.deleted_at IS NULL %s AND u.id IN (%s) AND u.status <> 2
			ORDER BY u.id ASC`, userIDCast, protectedCondition, strings.Join(placeholders, ",")))
		return s.db.Query(query, args...)
	}

	conditionType := toString(req.Condition["type"])
	if conditionType == "" {
		conditionType = toString(req.Condition["case_type"])
	}
	if conditionType == "shared_ip" || req.Condition["ip"] != nil {
		ip := toString(req.Condition["ip"])
		if ip == "" {
			return nil, fmt.Errorf("condition.ip is required for shared_ip batch")
		}
		window := toString(req.Condition["window"])
		if window == "" {
			window = "24h"
		}
		seconds, ok := WindowSeconds[window]
		if !ok {
			seconds = WindowSeconds["24h"]
		}
		startTime := time.Now().Unix() - seconds
		query := s.db.RebindQuery(fmt.Sprintf(`
			SELECT l.user_id as user_id,
				COALESCE(NULLIF(MAX(u.display_name), ''), NULLIF(MAX(u.username), ''), NULLIF(MAX(l.username), ''), %s) as username,
				COALESCE(MAX(u.status), 0) as user_status,
				COALESCE(MAX(u.role), 0) as role,
				COUNT(*) as request_count,
				COUNT(DISTINCT l.token_id) as token_count
			FROM logs l
			INNER JOIN users u ON u.id = l.user_id AND u.deleted_at IS NULL
			WHERE l.created_at >= ? AND l.ip = ? AND l.type IN (2, 5)
				AND l.user_id IS NOT NULL AND u.status <> 2 %s
			GROUP BY l.user_id
			ORDER BY request_count DESC
			LIMIT 500`, logUserIDCast, protectedCondition))
		return s.db.Query(query, startTime, ip)
	}

	return nil, fmt.Errorf("unsupported batch condition")
}

func (s *RiskMonitoringService) ensureRiskActionBatchTable() error {
	var query string
	if s.db.IsPG {
		query = `
			CREATE TABLE IF NOT EXISTS api_tools_risk_action_batches (
				batch_id TEXT PRIMARY KEY,
				action TEXT NOT NULL,
				operator TEXT NOT NULL,
				reason TEXT,
				source TEXT,
				condition_snapshot TEXT,
				affected_users_snapshot TEXT,
				failed_users_snapshot TEXT,
				success_count BIGINT NOT NULL DEFAULT 0,
				failed_count BIGINT NOT NULL DEFAULT 0,
				created_at BIGINT NOT NULL,
				reverted_at BIGINT
			)`
		return s.db.ExecuteDDL(query)
	}
	query = `
		CREATE TABLE IF NOT EXISTS api_tools_risk_action_batches (
			batch_id VARCHAR(128) PRIMARY KEY,
			action VARCHAR(32) NOT NULL,
			operator VARCHAR(128) NOT NULL,
			reason VARCHAR(255),
			source VARCHAR(128),
			condition_snapshot JSON,
			affected_users_snapshot JSON,
			failed_users_snapshot JSON,
			success_count BIGINT NOT NULL DEFAULT 0,
			failed_count BIGINT NOT NULL DEFAULT 0,
			created_at BIGINT NOT NULL,
			reverted_at BIGINT NULL,
			KEY idx_api_tools_risk_batches_created (created_at),
			KEY idx_api_tools_risk_batches_action (action)
		)`
	_, err := s.db.Execute(query)
	return err
}

func (s *RiskMonitoringService) insertRiskActionBatch(batchID string, req RiskActionBatchRequest, succeeded, failed []map[string]interface{}) error {
	conditionJSON, _ := json.Marshal(req.Condition)
	succeededJSON, _ := json.Marshal(succeeded)
	failedJSON, _ := json.Marshal(failed)
	query := s.db.RebindQuery(`
		INSERT INTO api_tools_risk_action_batches
			(batch_id, action, operator, reason, source, condition_snapshot, affected_users_snapshot,
			 failed_users_snapshot, success_count, failed_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := s.db.Execute(query, batchID, req.Action, "Admin", req.Reason, req.Source,
		string(conditionJSON), string(succeededJSON), string(failedJSON), len(succeeded), len(failed), time.Now().Unix())
	return err
}

func riskActionLabel(action string) string {
	if action == "unban" {
		return "解封"
	}
	return "封禁"
}

// ListBanRecords returns ban/unban audit records stored by moderation actions.
func (s *RiskMonitoringService) ListBanRecords(page, pageSize int, action string, userID *int64) map[string]interface{} {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	var records []map[string]interface{}
	cache.Get().GetJSON(securityAuditCacheKey, &records)

	filtered := make([]map[string]interface{}, 0, len(records))
	for _, record := range records {
		if action != "" && toString(record["action"]) != action {
			continue
		}
		if userID != nil && toInt64(record["user_id"]) != *userID {
			continue
		}
		filtered = append(filtered, record)
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		return toInt64(filtered[i]["created_at"]) > toInt64(filtered[j]["created_at"])
	})

	total := len(filtered)
	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	offset := (page - 1) * pageSize
	if offset > total {
		offset = total
	}
	end := offset + pageSize
	if end > total {
		end = total
	}

	return map[string]interface{}{
		"items":       filtered[offset:end],
		"total":       total,
		"page":        page,
		"page_size":   pageSize,
		"total_pages": totalPages,
	}
}

// ========== Checkin Analysis ==========

var (
	checkinTableOnce   sync.Once
	checkinTableExists bool
)

// checkinAnalysis holds checkin anomaly detection results
type checkinAnalysis struct {
	CheckinCount       int64   `json:"checkin_count"`
	TotalQuotaAwarded  int64   `json:"total_quota_awarded"`
	RequestsPerCheckin float64 `json:"requests_per_checkin"`
}

// analyzeCheckins checks for checkin abuse patterns
func analyzeCheckins(db *database.Manager, userID int64, startTime, endTime int64) *checkinAnalysis {
	checkinTableOnce.Do(func() {
		exists, err := db.TableExists("checkins")
		if err != nil {
			logger.L.Warn("检查 checkins 表失败: " + err.Error())
			return
		}
		checkinTableExists = exists
		if exists {
			logger.L.System("checkins 表已检测到，启用签到分析")
		}
	})

	if !checkinTableExists {
		return nil
	}

	row, err := db.QueryOne(db.RebindQuery(`
		SELECT COUNT(*) as checkin_count,
			COALESCE(SUM(quota), 0) as total_quota_awarded
		FROM checkins
		WHERE user_id = ? AND created_at >= ? AND created_at <= ?`),
		userID, startTime, endTime)
	if err != nil || row == nil {
		return nil
	}

	count := toInt64(row["checkin_count"])
	quotaAwarded := toInt64(row["total_quota_awarded"])

	return &checkinAnalysis{
		CheckinCount:      count,
		TotalQuotaAwarded: quotaAwarded,
	}
}

// ========== IP Switch Analysis ==========

// getIPVersion returns "v4" or "v6" based on the IP string
func getIPVersion(ip string) string {
	if strings.Contains(ip, ":") {
		return "v6"
	}
	return "v4"
}

// analyzeIPSwitches detects IP switching patterns from a time-ordered IP sequence.
// Matches Python's _analyze_ip_switches logic.
func analyzeIPSwitches(ipSequence []map[string]interface{}) map[string]interface{} {
	empty := map[string]interface{}{
		"switch_count":        int64(0),
		"real_switch_count":   int64(0),
		"rapid_switch_count":  int64(0),
		"dual_stack_switches": int64(0),
		"avg_ip_duration":     float64(0),
		"min_switch_interval": int64(0),
		"switch_details":      []map[string]interface{}{},
	}

	if len(ipSequence) < 2 {
		return empty
	}

	type switchDetail struct {
		Time        int64  `json:"time"`
		FromIP      string `json:"from_ip"`
		ToIP        string `json:"to_ip"`
		Interval    int64  `json:"interval"`
		IsDualStack bool   `json:"is_dual_stack"`
		FromVersion string `json:"from_version"`
		ToVersion   string `json:"to_version"`
	}

	var switches []switchDetail
	ipDurations := map[string][]int64{} // track usage duration per IP
	var rapidSwitches int64
	var dualStackSwitches int64

	var prevIP string
	var prevTime int64
	var ipStartTime int64

	for _, row := range ipSequence {
		currentIP := fmt.Sprintf("%v", row["ip"])
		currentTime := toInt64(row["created_at"])
		if currentIP == "" || currentTime == 0 {
			continue
		}

		if prevIP == "" {
			prevIP = currentIP
			prevTime = currentTime
			ipStartTime = currentTime
			continue
		}

		if currentIP != prevIP {
			switchInterval := currentTime - prevTime

			prevVersion := getIPVersion(prevIP)
			currVersion := getIPVersion(currentIP)

			// Detect dual-stack switch (v4 <-> v6)
			isDualStack := false
			isV4V6Switch := (prevVersion == "v4" && currVersion == "v6") ||
				(prevVersion == "v6" && currVersion == "v4")
			if isV4V6Switch {
				// Simple heuristic: v4/v6 switch within 60s is likely dual-stack
				if switchInterval <= 60 {
					isDualStack = true
				}
			}

			switches = append(switches, switchDetail{
				Time:        currentTime,
				FromIP:      prevIP,
				ToIP:        currentIP,
				Interval:    switchInterval,
				IsDualStack: isDualStack,
				FromVersion: prevVersion,
				ToVersion:   currVersion,
			})

			if isDualStack {
				dualStackSwitches++
			} else if switchInterval <= 60 {
				rapidSwitches++
			}

			// Record IP usage duration
			ipDuration := currentTime - ipStartTime
			ipDurations[prevIP] = append(ipDurations[prevIP], ipDuration)

			prevIP = currentIP
			ipStartTime = currentTime
		}

		prevTime = currentTime
	}

	switchCount := int64(len(switches))
	realSwitchCount := switchCount - dualStackSwitches

	// Min switch interval (excluding dual-stack)
	var minSwitchInterval int64
	first := true
	for _, s := range switches {
		if !s.IsDualStack {
			if first || s.Interval < minSwitchInterval {
				minSwitchInterval = s.Interval
				first = false
			}
		}
	}

	// Average IP duration
	var allDurations []int64
	for _, durations := range ipDurations {
		allDurations = append(allDurations, durations...)
	}
	avgIPDuration := float64(0)
	if len(allDurations) > 0 {
		var sum int64
		for _, d := range allDurations {
			sum += d
		}
		avgIPDuration = math.Round(float64(sum)/float64(len(allDurations))*10) / 10
	}

	// Return last 10 switch details
	detailLimit := 10
	startIdx := 0
	if len(switches) > detailLimit {
		startIdx = len(switches) - detailLimit
	}
	recentSwitches := make([]map[string]interface{}, 0, detailLimit)
	for _, s := range switches[startIdx:] {
		recentSwitches = append(recentSwitches, map[string]interface{}{
			"time":          s.Time,
			"from_ip":       s.FromIP,
			"to_ip":         s.ToIP,
			"interval":      s.Interval,
			"is_dual_stack": s.IsDualStack,
			"from_version":  s.FromVersion,
			"to_version":    s.ToVersion,
		})
	}

	return map[string]interface{}{
		"switch_count":        switchCount,
		"real_switch_count":   realSwitchCount,
		"rapid_switch_count":  rapidSwitches,
		"dual_stack_switches": dualStackSwitches,
		"avg_ip_duration":     avgIPDuration,
		"min_switch_interval": minSwitchInterval,
		"switch_details":      recentSwitches,
	}
}
