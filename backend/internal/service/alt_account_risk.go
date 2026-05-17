package service

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/new-api-tools/backend/internal/cache"
	"github.com/new-api-tools/backend/internal/database"
)

// AltAccountRiskService builds evidence-first alt-account risk cases.
type AltAccountRiskService struct {
	db *database.Manager
}

// NewAltAccountRiskService creates a new AltAccountRiskService.
func NewAltAccountRiskService() *AltAccountRiskService {
	return &AltAccountRiskService{db: database.Get()}
}

// GetAltAccountCases returns live generated alt-account cases.
func (s *AltAccountRiskService) GetAltAccountCases(caseType, window string, limit, offset int, useCache bool) (map[string]interface{}, error) {
	caseType = strings.TrimSpace(caseType)
	if caseType == "" {
		caseType = "all"
	}
	if _, ok := WindowSeconds[window]; !ok {
		window = "30d"
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}

	cacheKey := fmt.Sprintf("risk:alt_cases:%s:%s:%d:%d", caseType, window, limit, offset)
	cm := cache.Get()
	var cached map[string]interface{}
	if useCache {
		if found, _ := cm.GetJSON(cacheKey, &cached); found {
			return cached, nil
		}
	}

	generationLimit := limit + offset
	if generationLimit < 50 {
		generationLimit = 50
	}
	if generationLimit > 200 {
		generationLimit = 200
	}
	allCases, err := s.generateAltAccountCases(caseType, window, generationLimit)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(allCases, func(i, j int) bool {
		leftScore := toFloat64(allCases[i]["risk_score"])
		rightScore := toFloat64(allCases[j]["risk_score"])
		if leftScore == rightScore {
			return toInt64(allCases[i]["user_count"]) > toInt64(allCases[j]["user_count"])
		}
		return leftScore > rightScore
	})

	total := len(allCases)
	end := offset + limit
	if offset > total {
		offset = total
	}
	if end > total {
		end = total
	}

	result := map[string]interface{}{
		"items":        allCases[offset:end],
		"total":        total,
		"limit":        limit,
		"offset":       offset,
		"case_type":    caseType,
		"window":       window,
		"generated_at": time.Now().Unix(),
		"source":       "live_rules",
	}
	if useCache {
		cm.Set(cacheKey, result, 5*time.Minute)
	}
	return result, nil
}

// GetAltAccountCase returns a single case with detail.
func (s *AltAccountRiskService) GetAltAccountCase(caseID, window string) (map[string]interface{}, error) {
	if _, ok := WindowSeconds[window]; !ok {
		window = "30d"
	}
	windows := []string{window}
	if window != "30d" {
		windows = append(windows, "30d")
	}
	if window != "24h" {
		windows = append(windows, "24h")
	}
	caseType := caseTypeFromCaseID(caseID)
	if caseType == "" {
		caseType = "all"
	}
	for _, candidateWindow := range windows {
		cases, err := s.generateAltAccountCases(caseType, candidateWindow, 250)
		if err != nil {
			return nil, err
		}
		for _, item := range cases {
			if toString(item["case_id"]) == caseID {
				item["detail_loaded_at"] = time.Now().Unix()
				return item, nil
			}
		}
	}
	return nil, fmt.Errorf("未找到小号风险案件")
}

// AssessAltAccountCase performs a generic case-level AI assessment.
func (s *AltAccountRiskService) AssessAltAccountCase(caseID, window, baseURL, apiKey, model string) map[string]interface{} {
	caseData, err := s.GetAltAccountCase(caseID, window)
	if err != nil {
		return map[string]interface{}{"success": false, "message": err.Error(), "case_id": caseID}
	}

	config := NewAIAutoBanService().GetConfig()
	if strings.TrimSpace(baseURL) == "" {
		baseURL = toString(config["base_url"])
	}
	if strings.TrimSpace(apiKey) == "" {
		apiKey = toString(config["api_key"])
	}
	if strings.TrimSpace(model) == "" {
		model = toString(config["model"])
	}
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(apiKey) == "" || strings.TrimSpace(model) == "" {
		return map[string]interface{}{
			"success": false,
			"message": "AI Base URL、API Key 或模型未配置",
			"case":    caseData,
		}
	}

	prompt := buildAltAccountCasePrompt(caseData)
	assessment, usage, rawContent, err := callAltAccountAI(baseURL, apiKey, model, prompt)
	if err != nil {
		return map[string]interface{}{
			"success":     false,
			"message":     "小号案件 AI 研判失败: " + err.Error(),
			"case_id":     caseID,
			"case":        caseData,
			"raw_content": rawContent,
		}
	}

	assessment = normalizeAltAccountAssessment(assessment)
	assessment["prompt_version"] = toString(caseData["prompt_version"])
	assessment["policy"] = "pending_review_first"
	assessment["case_type"] = toString(caseData["case_type"])
	assessment["model"] = usage["model"]
	assessment["prompt_tokens"] = usage["prompt_tokens"]
	assessment["completion_tokens"] = usage["completion_tokens"]
	assessment["total_tokens"] = usage["total_tokens"]
	assessment["api_duration_ms"] = usage["api_duration_ms"]

	return map[string]interface{}{
		"success":     true,
		"case_id":     caseID,
		"window":      caseData["window"],
		"case":        caseData,
		"assessment":  assessment,
		"model":       usage["model"],
		"usage":       usage,
		"assessed_at": time.Now().Unix(),
	}
}

func (s *AltAccountRiskService) generateAltAccountCases(caseType, window string, limit int) ([]map[string]interface{}, error) {
	cases := make([]map[string]interface{}, 0)
	if caseType == "all" || caseType == "shared_ip" {
		items, err := s.buildSharedIPCases(window, limit)
		if err != nil {
			return nil, err
		}
		cases = append(cases, items...)
	}
	if caseType == "all" || caseType == "rotating_pool" {
		effectiveWindow := window
		if effectiveWindow != "7d" && effectiveWindow != "30d" {
			effectiveWindow = "30d"
		}
		items, err := s.buildRotatingPoolCases(effectiveWindow, 8, limit)
		if err != nil {
			return nil, err
		}
		cases = append(cases, items...)
	}
	if caseType == "all" || caseType == "invite_chain" {
		items, err := s.buildInviteChainCases(5, limit)
		if err != nil {
			return nil, err
		}
		cases = append(cases, items...)
	}
	if caseType == "all" || caseType == "token_rotation" {
		items, err := s.buildTokenRotationCases(window, 5, 10, limit)
		if err != nil {
			return nil, err
		}
		cases = append(cases, items...)
	}
	return cases, nil
}

func (s *AltAccountRiskService) buildSharedIPCases(window string, limit int) ([]map[string]interface{}, error) {
	ipSvc := NewIPMonitoringService()
	data, err := ipSvc.GetSharedUserIPs(window, 2, limit, false)
	if err != nil {
		return nil, err
	}
	result := []map[string]interface{}{}
	for _, item := range toMapSlice(data["items"]) {
		stats := summarizeSharedIPCase(item)
		score, reasons := scoreSharedIPAltCase(item, stats)
		caseID := altCaseID("shared_ip", window, toString(item["ip"]))
		result = append(result, map[string]interface{}{
			"case_id":           caseID,
			"case_type":         "shared_ip",
			"case_type_label":   "共享 IP 小号",
			"case_key":          toString(item["ip"]),
			"primary_ip":        toString(item["ip"]),
			"primary_ip_masked": maskIP(toString(item["ip"])),
			"window":            window,
			"risk_score":        score,
			"risk_level":        altRiskLevel(score),
			"risk_labels":       []string{"MULTI_USER_SHARED_IP", "FREE_QUOTA_FARMING"},
			"risk_reasons":      reasons,
			"prompt_version":    "shared-ip-alt-account-v1",
			"user_count":        toInt64(stats["user_count"]),
			"request_count":     toInt64(stats["request_count"]),
			"token_count":       toInt64(stats["token_count"]),
			"no_topup_count":    toInt64(stats["no_topup_user_count"]),
			"first_seen":        minUserTime(toMapSlice(item["users"]), "first_seen"),
			"last_seen":         maxUserTime(toMapSlice(item["users"]), "last_seen"),
			"case_stats":        stats,
			"users":             trimMapSlice(toMapSlice(item["users"]), 80),
			"timeline":          buildFirstSeenTimeline(toMapSlice(item["users"])),
			"source":            "shared_ip_rules",
		})
	}
	return result, nil
}

func (s *AltAccountRiskService) buildRotatingPoolCases(window string, minUsers, limit int) ([]map[string]interface{}, error) {
	seconds, ok := WindowSeconds[window]
	if !ok {
		seconds = WindowSeconds["30d"]
		window = "30d"
	}
	startTime := time.Now().Unix() - seconds

	topupJoin, topupSelect := s.successfulTopupJoin("summary.user_id")
	query := s.db.RebindQuery(fmt.Sprintf(`
		WITH summary AS (
			SELECT l.ip,
				l.user_id,
				COUNT(*) as request_count,
				COUNT(DISTINCT FLOOR(l.created_at / 86400)) as active_days,
				COUNT(DISTINCT l.token_id) as token_count,
				COUNT(DISTINCT l.model_name) as model_count,
				COUNT(DISTINCT l.channel_id) as channel_count,
				COALESCE(SUM(CASE WHEN l.type = 2 THEN l.quota ELSE 0 END), 0) as window_quota,
				MIN(l.created_at) as first_seen,
				MAX(l.created_at) as last_seen
			FROM logs l
			WHERE l.created_at >= ? AND l.type IN (2, 5)
				AND l.ip IS NOT NULL AND l.ip <> ''
				AND l.user_id IS NOT NULL
			GROUP BY l.ip, l.user_id
		)
		SELECT summary.ip,
			summary.user_id,
			COALESCE(NULLIF(u.display_name, ''), NULLIF(u.username, ''), '') as username,
			COALESCE(u.status, 0) as status,
			COALESCE(u.role, 0) as role,
			COALESCE(u.used_quota, 0) as used_quota,
			COALESCE(u.request_count, 0) as total_request_count,
			%s,
			summary.request_count,
			summary.active_days,
			summary.token_count,
			summary.model_count,
			summary.channel_count,
			summary.window_quota,
			summary.first_seen,
			summary.last_seen
		FROM summary
		LEFT JOIN users u ON u.id = summary.user_id AND u.deleted_at IS NULL
		%s
		WHERE summary.user_id IS NOT NULL
		ORDER BY summary.ip, summary.first_seen`, topupSelect, topupJoin))

	rows, err := s.db.Query(query, startTime)
	if err != nil {
		return nil, err
	}
	byIP := map[string][]map[string]interface{}{}
	for _, row := range rows {
		ip := toString(row["ip"])
		delete(row, "ip")
		byIP[ip] = append(byIP[ip], row)
	}

	cases := []map[string]interface{}{}
	for ip, users := range byIP {
		if len(users) < minUsers {
			continue
		}
		stats := summarizeRotatingPool(users)
		if toFloat64(stats["active_days_median"]) > 2 && toFloat64(stats["sequential_activation_score"]) < 0.45 {
			continue
		}
		score, reasons := scoreRotatingPoolCase(users, stats)
		if score < 45 {
			continue
		}
		caseID := altCaseID("rotating_pool", window, ip)
		cases = append(cases, map[string]interface{}{
			"case_id":           caseID,
			"case_type":         "rotating_pool",
			"case_type_label":   "轮换小号池",
			"case_key":          ip,
			"primary_ip":        ip,
			"primary_ip_masked": maskIP(ip),
			"window":            window,
			"risk_score":        score,
			"risk_level":        altRiskLevel(score),
			"risk_labels":       []string{"ROTATING_ALT_ACCOUNT_POOL", "FREE_QUOTA_FARMING"},
			"risk_reasons":      reasons,
			"prompt_version":    "rotating-alt-account-pool-v1",
			"user_count":        len(users),
			"request_count":     toInt64(stats["request_count"]),
			"token_count":       toInt64(stats["token_count"]),
			"no_topup_count":    toInt64(stats["no_topup_count"]),
			"first_seen":        minUserTime(users, "first_seen"),
			"last_seen":         maxUserTime(users, "last_seen"),
			"case_stats":        stats,
			"users":             trimMapSlice(users, 80),
			"timeline":          buildFirstSeenTimeline(users),
			"source":            "rotating_pool_rules",
		})
	}
	sort.SliceStable(cases, func(i, j int) bool {
		return toFloat64(cases[i]["risk_score"]) > toFloat64(cases[j]["risk_score"])
	})
	if len(cases) > limit {
		cases = cases[:limit]
	}
	return cases, nil
}

func (s *AltAccountRiskService) buildInviteChainCases(minInvited, limit int) ([]map[string]interface{}, error) {
	topupJoin, topupSelect := s.successfulTopupJoin("u.id")
	query := s.db.RebindQuery(fmt.Sprintf(`
		SELECT u.inviter_id,
			COUNT(*) as user_count,
			SUM(CASE WHEN COALESCE(u.request_count, 0) > 0 THEN 1 ELSE 0 END) as active_user_count,
			SUM(CASE WHEN %s <= 0 THEN 1 ELSE 0 END) as no_topup_count,
			COALESCE(SUM(u.used_quota), 0) as used_quota,
			COALESCE(SUM(u.request_count), 0) as request_count,
			MIN(u.id) as first_user_id,
			MAX(u.id) as last_user_id
		FROM users u
		%s
		WHERE u.deleted_at IS NULL AND u.inviter_id IS NOT NULL AND u.inviter_id > 0
		GROUP BY u.inviter_id
		HAVING COUNT(*) >= ?
		ORDER BY user_count DESC
		LIMIT ?`, strings.TrimSuffix(topupSelect, " as topup_count"), topupJoin))
	rows, err := s.db.Query(query, minInvited, limit)
	if err != nil {
		return nil, err
	}

	cases := []map[string]interface{}{}
	for _, row := range rows {
		score, reasons := scoreInviteChainCase(row)
		if score < 35 {
			continue
		}
		inviterID := toInt64(row["inviter_id"])
		users := s.fetchInvitedUserSummaries(inviterID, 80)
		caseID := altCaseID("invite_chain", "30d", fmt.Sprintf("%d", inviterID))
		cases = append(cases, map[string]interface{}{
			"case_id":            caseID,
			"case_type":          "invite_chain",
			"case_type_label":    "邀请链小号",
			"case_key":           fmt.Sprintf("%d", inviterID),
			"primary_inviter_id": inviterID,
			"window":             "30d",
			"risk_score":         score,
			"risk_level":         altRiskLevel(score),
			"risk_labels":        []string{"INVITE_CHAIN_ALT_ACCOUNTS", "FREE_QUOTA_FARMING"},
			"risk_reasons":       reasons,
			"prompt_version":     "invite-chain-alt-account-v1",
			"user_count":         toInt64(row["user_count"]),
			"request_count":      toInt64(row["request_count"]),
			"token_count":        0,
			"no_topup_count":     toInt64(row["no_topup_count"]),
			"case_stats":         row,
			"users":              users,
			"source":             "invite_chain_rules",
		})
	}
	return cases, nil
}

func (s *AltAccountRiskService) buildTokenRotationCases(window string, minTokens, maxReqPerToken, limit int) ([]map[string]interface{}, error) {
	riskSvc := NewRiskMonitoringService()
	data, err := riskSvc.GetTokenRotationUsers(window, minTokens, maxReqPerToken, limit)
	if err != nil {
		return nil, err
	}
	cases := []map[string]interface{}{}
	for _, row := range toMapSlice(data["items"]) {
		score, reasons := scoreTokenRotationCase(row)
		caseID := altCaseID("token_rotation", window, fmt.Sprintf("%d", toInt64(row["user_id"])))
		cases = append(cases, map[string]interface{}{
			"case_id":         caseID,
			"case_type":       "token_rotation",
			"case_type_label": "Token 轮换",
			"case_key":        fmt.Sprintf("%d", toInt64(row["user_id"])),
			"primary_user_id": toInt64(row["user_id"]),
			"window":          window,
			"risk_score":      score,
			"risk_level":      altRiskLevel(score),
			"risk_labels":     []string{"TOKEN_ROTATION_ABUSE"},
			"risk_reasons":    reasons,
			"prompt_version":  "token-rotation-alt-account-v1",
			"user_count":      1,
			"request_count":   toInt64(row["total_requests"]),
			"token_count":     toInt64(row["token_count"]),
			"case_stats":      row,
			"users":           []map[string]interface{}{row},
			"source":          "token_rotation_rules",
		})
	}
	return cases, nil
}

func (s *AltAccountRiskService) successfulTopupJoin(userIDExpr string) (string, string) {
	if ok, _ := s.db.TableExists("top_ups"); !ok {
		return "", "0 as topup_count"
	}
	statusExpr := "LOWER(CAST(status AS CHAR))"
	if s.db.IsPG {
		statusExpr = "LOWER(CAST(status AS TEXT))"
	}
	join := fmt.Sprintf(`
		LEFT JOIN (
			SELECT user_id, COUNT(*) as success_count
			FROM top_ups
			WHERE %s IN ('success', 'completed', '1')
			GROUP BY user_id
		) tu ON tu.user_id = %s`, statusExpr, userIDExpr)
	return join, "COALESCE(tu.success_count, 0) as topup_count"
}

func (s *AltAccountRiskService) fetchInvitedUserSummaries(inviterID int64, limit int) []map[string]interface{} {
	topupJoin, topupSelect := s.successfulTopupJoin("u.id")
	query := s.db.RebindQuery(fmt.Sprintf(`
		SELECT u.id as user_id,
			COALESCE(NULLIF(u.display_name, ''), NULLIF(u.username, ''), '') as username,
			COALESCE(u.status, 0) as status,
			COALESCE(u.role, 0) as role,
			COALESCE(u.used_quota, 0) as used_quota,
			COALESCE(u.request_count, 0) as total_request_count,
			%s
		FROM users u
		%s
		WHERE u.deleted_at IS NULL AND u.inviter_id = ?
		ORDER BY u.request_count DESC
		LIMIT ?`, topupSelect, topupJoin))
	rows, err := s.db.Query(query, inviterID, limit)
	if err != nil {
		return []map[string]interface{}{}
	}
	return rows
}

func scoreSharedIPAltCase(item, stats map[string]interface{}) (int, []string) {
	score := 0
	reasons := []string{}
	userCount := toInt64(stats["user_count"])
	unbanned := toInt64(stats["unbanned_count"])
	noTopup := toInt64(stats["no_topup_user_count"])
	lowReq := toInt64(stats["low_request_user_count"])
	spread := toInt64(stats["first_seen_spread_seconds"])
	tokenCount := toInt64(stats["token_count"])
	requestCount := toInt64(stats["request_count"])
	if userCount >= 15 {
		score += 30
		reasons = append(reasons, fmt.Sprintf("同一 IP 聚集 %d 个用户", userCount))
	} else if userCount >= 8 {
		score += 24
		reasons = append(reasons, fmt.Sprintf("同一 IP 聚集 %d 个用户", userCount))
	} else if userCount >= 3 {
		score += 14
		reasons = append(reasons, fmt.Sprintf("同一 IP 聚集 %d 个用户", userCount))
	}
	if unbanned >= 5 {
		score += 16
		reasons = append(reasons, fmt.Sprintf("%d 个未封禁账号仍可调用", unbanned))
	}
	if userCount > 0 && noTopup*100/userCount >= 70 {
		score += 18
		reasons = append(reasons, fmt.Sprintf("%d 个账号无成功充值记录", noTopup))
	}
	if lowReq >= 3 {
		score += 12
		reasons = append(reasons, fmt.Sprintf("%d 个账号低调用低令牌", lowReq))
	}
	if tokenCount >= userCount && userCount >= 3 {
		score += 10
		reasons = append(reasons, "令牌数接近或超过用户数")
	}
	if spread > 0 && spread <= 3600 {
		score += 16
		reasons = append(reasons, "多个账号首次出现集中在 1 小时内")
	} else if spread > 0 && spread <= 86400 {
		score += 8
		reasons = append(reasons, "多个账号首次出现集中在 24 小时内")
	}
	if requestCount >= 1000 {
		score += 8
		reasons = append(reasons, "共享 IP 有持续调用量")
	}
	if toInt64(item["banned_count"]) > 0 {
		score += 6
		reasons = append(reasons, "该 IP 已有关联账号被封禁")
	}
	return clampScore(score), reasons
}

func summarizeRotatingPool(users []map[string]interface{}) map[string]interface{} {
	activeDays := make([]int, 0, len(users))
	noTopup := int64(0)
	requests := int64(0)
	tokens := int64(0)
	quota := int64(0)
	for _, user := range users {
		activeDays = append(activeDays, int(toInt64(user["active_days"])))
		if toInt64(user["topup_count"]) <= 0 {
			noTopup++
		}
		requests += toInt64(user["request_count"])
		tokens += toInt64(user["token_count"])
		quota += toInt64(user["window_quota"])
	}
	sort.Ints(activeDays)
	median := 0.0
	if len(activeDays) > 0 {
		mid := len(activeDays) / 2
		if len(activeDays)%2 == 0 {
			median = float64(activeDays[mid-1]+activeDays[mid]) / 2
		} else {
			median = float64(activeDays[mid])
		}
	}
	sequential := sequentialActivationScore(users)
	return map[string]interface{}{
		"user_count":                  len(users),
		"request_count":               requests,
		"token_count":                 tokens,
		"window_quota":                quota,
		"no_topup_count":              noTopup,
		"active_days_median":          median,
		"sequential_activation_score": math.Round(sequential*100) / 100,
	}
}

func scoreRotatingPoolCase(users []map[string]interface{}, stats map[string]interface{}) (int, []string) {
	score := 0
	reasons := []string{}
	userCount := int(toInt64(stats["user_count"]))
	noTopup := toInt64(stats["no_topup_count"])
	median := toFloat64(stats["active_days_median"])
	sequential := toFloat64(stats["sequential_activation_score"])
	if userCount >= 20 {
		score += 30
		reasons = append(reasons, fmt.Sprintf("30d 内同一 IP 关联 %d 个候选账号", userCount))
	} else if userCount >= 10 {
		score += 24
		reasons = append(reasons, fmt.Sprintf("窗口内同一 IP 关联 %d 个候选账号", userCount))
	} else if userCount >= 8 {
		score += 18
		reasons = append(reasons, fmt.Sprintf("窗口内同一 IP 关联 %d 个候选账号", userCount))
	}
	if median <= 1 {
		score += 18
		reasons = append(reasons, "账号活跃日中位数不超过 1 天，呈低频轮换")
	} else if median <= 2 {
		score += 12
		reasons = append(reasons, "账号活跃日中位数不超过 2 天")
	}
	if userCount > 0 && noTopup*100/int64(userCount) >= 80 {
		score += 18
		reasons = append(reasons, fmt.Sprintf("%d 个账号无成功充值记录", noTopup))
	} else if userCount > 0 && noTopup*100/int64(userCount) >= 60 {
		score += 12
		reasons = append(reasons, "未充值账号占比超过 60%")
	}
	if sequential >= 0.7 {
		score += 18
		reasons = append(reasons, "账号首次出现呈明显接力顺序")
	} else if sequential >= 0.45 {
		score += 10
		reasons = append(reasons, "账号出现时间存在接力特征")
	}
	if toInt64(stats["request_count"]) > 0 {
		score += 6
		reasons = append(reasons, "账号池存在真实调用行为")
	}
	return clampScore(score), reasons
}

func scoreInviteChainCase(row map[string]interface{}) (int, []string) {
	score := 0
	reasons := []string{}
	userCount := toInt64(row["user_count"])
	active := toInt64(row["active_user_count"])
	noTopup := toInt64(row["no_topup_count"])
	if userCount >= 20 {
		score += 30
	} else if userCount >= 10 {
		score += 24
	} else if userCount >= 5 {
		score += 16
	}
	reasons = append(reasons, fmt.Sprintf("同一邀请人关联 %d 个账号", userCount))
	if userCount > 0 && noTopup*100/userCount >= 80 {
		score += 22
		reasons = append(reasons, fmt.Sprintf("%d 个被邀请账号未成功充值", noTopup))
	}
	if active >= 3 {
		score += 10
		reasons = append(reasons, fmt.Sprintf("%d 个被邀请账号已有调用", active))
	}
	if toInt64(row["used_quota"]) > 0 {
		score += 8
		reasons = append(reasons, "被邀请账号已产生额度消耗")
	}
	return clampScore(score), reasons
}

func scoreTokenRotationCase(row map[string]interface{}) (int, []string) {
	score := 35
	reasons := []string{}
	tokens := toInt64(row["token_count"])
	requests := toInt64(row["total_requests"])
	if tokens >= 10 {
		score += 24
	} else if tokens >= 5 {
		score += 16
	}
	reasons = append(reasons, fmt.Sprintf("单用户窗口内使用 %d 个 token", tokens))
	if tokens > 0 && requests/tokens <= 5 {
		score += 14
		reasons = append(reasons, "平均每个 token 请求很少，像批量轮换")
	}
	return clampScore(score), reasons
}

func sequentialActivationScore(users []map[string]interface{}) float64 {
	if len(users) < 2 {
		return 0
	}
	sortedUsers := append([]map[string]interface{}{}, users...)
	sort.Slice(sortedUsers, func(i, j int) bool {
		return toInt64(sortedUsers[i]["first_seen"]) < toInt64(sortedUsers[j]["first_seen"])
	})
	sequential := 0
	for i := 1; i < len(sortedUsers); i++ {
		gap := toInt64(sortedUsers[i]["first_seen"]) - toInt64(sortedUsers[i-1]["first_seen"])
		if gap >= 6*3600 && gap <= 72*3600 {
			sequential++
		}
	}
	return float64(sequential) / float64(len(sortedUsers)-1)
}

func buildFirstSeenTimeline(users []map[string]interface{}) []map[string]interface{} {
	type bucket struct {
		UserIDs      []int64 `json:"user_ids"`
		RequestCount int64   `json:"request_count"`
		Quota        int64   `json:"quota"`
	}
	buckets := map[string]*bucket{}
	for _, user := range users {
		ts := toInt64(user["first_seen"])
		if ts == 0 {
			continue
		}
		day := time.Unix(ts, 0).Format("2006-01-02")
		if _, ok := buckets[day]; !ok {
			buckets[day] = &bucket{UserIDs: []int64{}}
		}
		buckets[day].UserIDs = append(buckets[day].UserIDs, toInt64(user["user_id"]))
		buckets[day].RequestCount += toInt64(user["request_count"])
		buckets[day].Quota += toInt64(user["window_quota"]) + toInt64(user["used_quota"])
	}
	days := make([]string, 0, len(buckets))
	for day := range buckets {
		days = append(days, day)
	}
	sort.Strings(days)
	result := make([]map[string]interface{}, 0, len(days))
	for _, day := range days {
		result = append(result, map[string]interface{}{
			"date":            day,
			"active_user_ids": buckets[day].UserIDs,
			"request_count":   buckets[day].RequestCount,
			"quota":           buckets[day].Quota,
		})
	}
	return result
}

func buildAltAccountCasePrompt(caseData map[string]interface{}) string {
	safeCase := cloneCaseForAI(caseData)
	payloadBytes, _ := json.Marshal(safeCase)
	return fmt.Sprintf(`你是 NewAPI Tools 的首席风控分析师。请基于聚合后的案件证据，判断这是否是批量小号/轮换账号池/邀请链小号/Token 轮换滥用。

重要要求：
1. 只基于证据判断，不要因为单一共享 IP 直接建议封禁。
2. 必须考虑误报：公司 NAT、校园网、家庭宽带、运营商 CGNAT、Cloudflare/代理出口、真实团队共用网络。
3. 对“每隔 24 小时使用一个账号”的轮换账号池，要重点看 7d/30d 时间线、单账号活跃日、未充值占比、接力顺序。
4. 默认策略是 pending_review_first：除非证据极强，否则 action 给 review 或 monitor。
5. 你可以严厉评估风险，但建议动作必须可审计、可复核。

请只返回 JSON，不要 Markdown。字段必须包含：
{
  "risk_score": 0-100,
  "confidence": 0-1,
  "action": "monitor" | "review" | "ban",
  "risk_labels": ["ROTATING_ALT_ACCOUNT_POOL"],
  "reason": "一句中文结论",
  "evidence_summary": ["证据1", "证据2"],
  "false_positive_risk": "low" | "medium" | "high",
  "false_positive_reasons": ["可能误报原因"],
  "questions_for_admin": ["需要管理员确认的问题"],
  "likely_user_ids": [123],
  "recommended_actions": ["进入待复核", "移动观察分组"],
  "recommended_admin_action": "建议管理员下一步怎么做"
}

案件数据如下：
%s`, string(payloadBytes))
}

func callAltAccountAI(baseURL, apiKey, model, prompt string) (map[string]interface{}, map[string]interface{}, string, error) {
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": "You are a senior API risk analyst. Return JSON only."},
			{"role": "user", "content": prompt},
		},
		"temperature":     0.1,
		"max_tokens":      3500,
		"response_format": map[string]string{"type": "json_object"},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, "", err
	}
	req, err := http.NewRequest("POST", getEndpointURL(baseURL, "/chat/completions"), bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 150 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := string(body)
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return nil, nil, detail, fmt.Errorf("HTTP %d: %s", resp.StatusCode, detail)
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
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, nil, string(body), err
	}
	if len(chatResp.Choices) == 0 {
		return nil, nil, "", fmt.Errorf("AI 响应缺少 choices")
	}
	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	var assessment map[string]interface{}
	if err := json.Unmarshal([]byte(extractJSONObject(content)), &assessment); err != nil {
		return nil, nil, content, err
	}
	actualModel := chatResp.Model
	if actualModel == "" {
		actualModel = model
	}
	usage := map[string]interface{}{
		"model":             actualModel,
		"prompt_tokens":     chatResp.Usage.PromptTokens,
		"completion_tokens": chatResp.Usage.CompletionTokens,
		"total_tokens":      chatResp.Usage.TotalTokens,
		"api_duration_ms":   time.Since(start).Milliseconds(),
	}
	return assessment, usage, content, nil
}

func cloneCaseForAI(caseData map[string]interface{}) map[string]interface{} {
	clone := map[string]interface{}{}
	for k, v := range caseData {
		clone[k] = v
	}
	return clone
}

func normalizeAltAccountAssessment(assessment map[string]interface{}) map[string]interface{} {
	score := toFloat64(assessment["risk_score"])
	if score <= 10 && score > 0 {
		score *= 10
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	confidence := toFloat64(assessment["confidence"])
	if confidence > 1 {
		confidence = confidence / 100
	}
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	action := toString(assessment["action"])
	if action != "ban" && action != "review" && action != "monitor" {
		if score >= 70 {
			action = "review"
		} else {
			action = "monitor"
		}
	}
	assessment["risk_score"] = int(math.Round(score))
	assessment["confidence"] = math.Round(confidence*100) / 100
	assessment["action"] = action
	if toString(assessment["reason"]) == "" {
		assessment["reason"] = "AI 未返回明确结论，建议人工复核案件证据"
	}
	for _, key := range []string{"risk_labels", "evidence_summary", "false_positive_reasons", "questions_for_admin", "likely_user_ids", "recommended_actions"} {
		if _, ok := assessment[key].([]interface{}); !ok {
			if _, ok := assessment[key].([]string); !ok {
				assessment[key] = []interface{}{}
			}
		}
	}
	falsePositiveRisk := strings.ToLower(strings.TrimSpace(toString(assessment["false_positive_risk"])))
	if riskMap, ok := assessment["false_positive_risk"].(map[string]interface{}); ok {
		falsePositiveRisk = strings.ToLower(strings.TrimSpace(toString(riskMap["level"])))
	}
	if falsePositiveRisk != "low" && falsePositiveRisk != "medium" && falsePositiveRisk != "high" {
		falsePositiveRisk = "medium"
	}
	assessment["false_positive_risk"] = falsePositiveRisk
	return assessment
}

func caseTypeFromCaseID(caseID string) string {
	for _, caseType := range []string{"shared_ip", "rotating_pool", "invite_chain", "token_rotation"} {
		if strings.HasPrefix(caseID, caseType+"_") {
			return caseType
		}
	}
	return ""
}

func altCaseID(caseType, window, key string) string {
	sum := sha1.Sum([]byte(caseType + "|" + window + "|" + key))
	return fmt.Sprintf("%s_%s_%s", caseType, window, hex.EncodeToString(sum[:])[:12])
}

func altRiskLevel(score int) string {
	if score >= 85 {
		return "critical"
	}
	if score >= 65 {
		return "high"
	}
	if score >= 40 {
		return "medium"
	}
	return "low"
}

func clampScore(score int) int {
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

func trimMapSlice(items []map[string]interface{}, limit int) []map[string]interface{} {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[:limit]
}

func minUserTime(users []map[string]interface{}, field string) int64 {
	var result int64
	for _, user := range users {
		value := toInt64(user[field])
		if value == 0 {
			continue
		}
		if result == 0 || value < result {
			result = value
		}
	}
	return result
}

func maxUserTime(users []map[string]interface{}, field string) int64 {
	var result int64
	for _, user := range users {
		value := toInt64(user[field])
		if value > result {
			result = value
		}
	}
	return result
}

func maskIP(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		return parts[0] + "." + parts[1] + ".*.*"
	}
	if ip == "" {
		return ""
	}
	sum := sha1.Sum([]byte(ip))
	return "iphash:" + hex.EncodeToString(sum[:])[:10]
}
