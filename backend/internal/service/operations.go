package service

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/new-api-tools/backend/internal/cache"
	"github.com/new-api-tools/backend/internal/database"
)

const (
	operationsQuotaPerUSD       = 500000.0
	highValueQuotaThreshold     = int64(2500000)
	highValueRequestThreshold   = int64(100)
	highValueLifetimeQuota      = int64(5000000)
	highValueTopUpMoney         = 50.0
	quietUserSeconds            = int64(72 * 3600)
	newPaidActivationWindow     = int64(72 * 3600)
	stalePendingPaymentSeconds  = int64(15 * 60)
	experienceWindowSeconds     = int64(24 * 3600)
	experienceFailureRate       = 0.25
	experienceSlowUseTimeMillis = 10000.0
)

// OperationsService builds operator-facing alerts that are not moderation actions.
type OperationsService struct {
	db *database.Manager
}

// NewOperationsService creates an OperationsService.
func NewOperationsService() *OperationsService {
	return &OperationsService{db: database.Get()}
}

// GetOperationsAlerts returns the v1 operations alert stream.
func (s *OperationsService) GetOperationsAlerts(window, alertType, severity string, limit int, useCache bool) (map[string]interface{}, error) {
	seconds, ok := WindowSeconds[window]
	if !ok {
		window = "30d"
		seconds = WindowSeconds[window]
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 300 {
		limit = 300
	}
	if alertType == "" {
		alertType = "all"
	}
	if severity == "" {
		severity = "all"
	}

	cacheKey := fmt.Sprintf("operations:alerts:%s:%s:%s:%d", window, alertType, severity, limit)
	if useCache {
		var cached map[string]interface{}
		if found, _ := cache.Get().GetJSON(cacheKey, &cached); found {
			cached["cache_hit"] = true
			return cached, nil
		}
	}

	now := time.Now().Unix()
	startTime := now - seconds
	alerts := make([]map[string]interface{}, 0, limit)

	if rows, err := s.highValueSilentAlerts(startTime, now, limit); err == nil {
		alerts = append(alerts, rows...)
	}
	if rows, err := s.topUpGapAlerts(now, limit); err == nil {
		alerts = append(alerts, rows...)
	}
	if rows, err := s.newPaidActivationAlerts(now, limit); err == nil {
		alerts = append(alerts, rows...)
	}
	if rows, err := s.experienceAlerts(now, limit); err == nil {
		alerts = append(alerts, rows...)
	}
	if rows, err := s.paymentStateAlerts(startTime, now, limit); err == nil {
		alerts = append(alerts, rows...)
	}

	filtered := make([]map[string]interface{}, 0, len(alerts))
	for _, item := range alerts {
		if alertType != "all" && toString(item["type"]) != alertType && toString(item["category"]) != alertType {
			continue
		}
		if severity != "all" && toString(item["severity"]) != severity {
			continue
		}
		filtered = append(filtered, item)
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		a := severityWeight(toString(filtered[i]["severity"]))
		b := severityWeight(toString(filtered[j]["severity"]))
		if a != b {
			return a > b
		}
		return toInt64(filtered[i]["triggered_at"]) > toInt64(filtered[j]["triggered_at"])
	})

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	summary := buildOperationsSummary(filtered)
	result := map[string]interface{}{
		"items":         filtered,
		"summary":       summary,
		"total":         len(filtered),
		"window":        window,
		"generated_at":  now,
		"snapshot_time": now,
		"cache_hit":     false,
		"notes": []string{
			"v1 暂不包含收入/毛利异常，因为未接入上游真实价格系统",
			"注册邮箱只在用户详情中展示，用于管理员人工联系",
		},
	}

	cache.Get().Set(cacheKey, result, 2*time.Minute)
	return result, nil
}

// GetOperationsUserDetail returns contact and context for an operations alert user.
func (s *OperationsService) GetOperationsUserDetail(userID int64, window string) (map[string]interface{}, error) {
	seconds, ok := WindowSeconds[window]
	if !ok {
		window = "30d"
		seconds = WindowSeconds[window]
	}
	now := time.Now().Unix()
	startTime := now - seconds

	groupCol := "`group`"
	if s.db.IsPG {
		groupCol = `"group"`
	}

	createdExpr := "0 as created_at"
	if s.db.ColumnExists("users", "created_at") {
		createdExpr = "COALESCE(created_at, 0) as created_at"
	}

	userQuery := s.db.RebindQuery(fmt.Sprintf(`
		SELECT id, COALESCE(username, '') as username, COALESCE(display_name, '') as display_name,
			COALESCE(email, '') as email, COALESCE(status, 0) as status, COALESCE(role, 0) as role,
			COALESCE(%s, '') as user_group, COALESCE(quota, 0) as quota,
			COALESCE(used_quota, 0) as used_quota, COALESCE(request_count, 0) as request_count,
			%s
		FROM users
		WHERE id = ? AND deleted_at IS NULL`, groupCol, createdExpr))
	userRow, err := s.db.QueryOneWithTimeout(8*time.Second, userQuery, userID)
	if err != nil {
		return nil, err
	}
	if userRow == nil {
		return nil, fmt.Errorf("user not found")
	}

	usageQuery := s.db.RebindQuery(`
		SELECT COUNT(*) as total_requests,
			SUM(CASE WHEN type = 2 THEN 1 ELSE 0 END) as success_requests,
			SUM(CASE WHEN type = 5 THEN 1 ELSE 0 END) as failure_requests,
			COALESCE(SUM(quota), 0) as quota_used,
			COALESCE(AVG(CASE WHEN type = 2 THEN use_time ELSE NULL END), 0) as avg_use_time,
			COALESCE(MAX(created_at), 0) as last_request_time,
			COALESCE(MIN(created_at), 0) as first_request_time,
			COUNT(DISTINCT NULLIF(model_name, '')) as unique_models,
			COUNT(DISTINCT channel_id) as unique_channels,
			COUNT(DISTINCT NULLIF(ip, '')) as unique_ips,
			COUNT(DISTINCT token_id) as unique_tokens
		FROM logs
		WHERE user_id = ? AND created_at >= ? AND created_at <= ? AND type IN (2, 5)`)
	usageRow, _ := s.db.QueryOneWithTimeout(12*time.Second, usageQuery, userID, startTime, now)
	if usageRow == nil {
		usageRow = map[string]interface{}{}
	}
	totalRequests := toInt64(usageRow["total_requests"])
	failureRequests := toInt64(usageRow["failure_requests"])
	failureRate := 0.0
	if totalRequests > 0 {
		failureRate = float64(failureRequests) / float64(totalRequests)
	}
	usageRow["failure_rate"] = failureRate

	recentLogsQuery := s.db.RebindQuery(`
		SELECT created_at, type, COALESCE(model_name, '') as model_name, COALESCE(channel_id, 0) as channel_id,
			COALESCE(quota, 0) as quota, COALESCE(use_time, 0) as use_time,
			COALESCE(ip, '') as ip, COALESCE(request_id, '') as request_id
		FROM logs
		WHERE user_id = ? AND type IN (2, 5)
		ORDER BY created_at DESC
		LIMIT 20`)
	recentLogs, _ := s.db.QueryWithTimeout(10*time.Second, recentLogsQuery, userID)
	if recentLogs == nil {
		recentLogs = []map[string]interface{}{}
	}

	topupSummary := map[string]interface{}{
		"available": false,
	}
	recentTopUps := []map[string]interface{}{}
	if s.hasTopUps() {
		topupStatsQuery := s.db.RebindQuery(fmt.Sprintf(`
			SELECT COUNT(*) as total_count,
				SUM(CASE WHEN %s THEN 1 ELSE 0 END) as success_count,
				COALESCE(SUM(CASE WHEN %s THEN amount ELSE 0 END), 0) as success_amount,
				COALESCE(SUM(CASE WHEN %s THEN money ELSE 0 END), 0) as success_money,
				COALESCE(MAX(CASE WHEN %s THEN %s ELSE 0 END), 0) as last_success_time
			FROM top_ups t
			WHERE user_id = ?`, s.successTopUpCondition("t"), s.successTopUpCondition("t"), s.successTopUpCondition("t"), s.successTopUpCondition("t"), s.successTopUpTimeExpr("t")))
		row, _ := s.db.QueryOneWithTimeout(8*time.Second, topupStatsQuery, userID)
		if row != nil {
			row["available"] = true
			topupSummary = row
		}

		recentTopUpsQuery := s.db.RebindQuery(`
			SELECT id, COALESCE(amount, 0) as amount, COALESCE(money, 0) as money,
				COALESCE(trade_no, '') as trade_no, COALESCE(payment_method, '') as payment_method,
				COALESCE(create_time, 0) as create_time, COALESCE(complete_time, 0) as complete_time,
				COALESCE(status, '') as status
			FROM top_ups
			WHERE user_id = ?
			ORDER BY create_time DESC
			LIMIT 10`)
		recentTopUps, _ = s.db.QueryWithTimeout(8*time.Second, recentTopUpsQuery, userID)
		if recentTopUps == nil {
			recentTopUps = []map[string]interface{}{}
		}
	}

	return map[string]interface{}{
		"user": map[string]interface{}{
			"id":            userRow["id"],
			"username":      userRow["username"],
			"display_name":  userRow["display_name"],
			"email":         userRow["email"],
			"status":        userRow["status"],
			"role":          userRow["role"],
			"group":         userRow["user_group"],
			"quota":         userRow["quota"],
			"used_quota":    userRow["used_quota"],
			"request_count": userRow["request_count"],
			"created_at":    userRow["created_at"],
		},
		"window":        window,
		"snapshot_time": now,
		"usage":         usageRow,
		"topups":        topupSummary,
		"recent_topups": recentTopUps,
		"recent_logs":   recentLogs,
		"privacy_note":  "注册邮箱仅用于管理员人工联系；不要发送给外部 AI 或默认导出。",
	}, nil
}

func (s *OperationsService) highValueSilentAlerts(startTime, now int64, limit int) ([]map[string]interface{}, error) {
	quietCutoff := now - quietUserSeconds
	if startTime >= quietCutoff {
		startTime = quietCutoff - WindowSeconds["30d"]
	}
	topupSelect, topupJoin := s.topUpAggregateSelectAndJoin()
	query := s.db.RebindQuery(fmt.Sprintf(`
		SELECT l.user_id as user_id,
			%s as username,
			COALESCE(MAX(u.status), 0) as user_status,
			COALESCE(MAX(u.quota), 0) as current_quota,
			COALESCE(MAX(u.used_quota), 0) as lifetime_quota,
			COUNT(*) as historical_requests,
			SUM(CASE WHEN l.type = 2 THEN 1 ELSE 0 END) as historical_success_requests,
			COALESCE(SUM(l.quota), 0) as historical_quota,
			COALESCE(MAX(l.created_at), 0) as last_request_time,
			%s
		FROM logs l
		INNER JOIN users u ON u.id = l.user_id AND u.deleted_at IS NULL
		%s
		WHERE l.created_at >= ? AND l.created_at < ?
			AND l.type IN (2, 5) AND l.user_id IS NOT NULL
			AND NOT EXISTS (
				SELECT 1 FROM logs lr
				WHERE lr.user_id = l.user_id AND lr.created_at >= ? AND lr.type IN (2, 5)
			)
		GROUP BY l.user_id
		HAVING COALESCE(SUM(l.quota), 0) >= ? OR COUNT(*) >= ?
		ORDER BY historical_quota DESC, historical_requests DESC
		LIMIT ?`, s.displayNameExpr("l"), topupSelect, topupJoin))
	rows, err := s.db.QueryWithTimeout(25*time.Second, query, startTime, quietCutoff, quietCutoff, highValueQuotaThreshold, highValueRequestThreshold, limit)
	if err != nil {
		return nil, err
	}
	alerts := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		userID := toInt64(row["user_id"])
		quota := toInt64(row["historical_quota"])
		requests := toInt64(row["historical_requests"])
		lastRequest := toInt64(row["last_request_time"])
		silentHours := int64(0)
		if lastRequest > 0 {
			silentHours = (now - lastRequest) / 3600
		}
		severity := "medium"
		if quota >= 10000000 || toFloat64(row["topup_money"]) >= 200 {
			severity = "critical"
		} else if quota >= highValueQuotaThreshold || requests >= 500 || toFloat64(row["topup_money"]) >= highValueTopUpMoney {
			severity = "high"
		}
		alerts = append(alerts, s.userAlert(row, "high_value_silent", "retention", severity,
			"高价值用户突然停用",
			[]string{
				fmt.Sprintf("历史窗口消耗 %s，累计 %d 次请求", formatOperationQuota(quota), requests),
				fmt.Sprintf("最近一次调用距今约 %d 小时", silentHours),
				fmt.Sprintf("当前余额约 %s", formatOperationQuota(toInt64(row["current_quota"]))),
			},
			"检查最近失败日志、余额和模型可用性，必要时通过注册邮箱联系用户。",
			lastRequest,
			map[string]interface{}{
				"historical_quota":    quota,
				"historical_requests": requests,
				"silent_hours":        silentHours,
				"current_quota":       row["current_quota"],
				"last_request_time":   lastRequest,
			}, fmt.Sprintf("high-value-silent-%d-%d", userID, lastRequest)))
	}
	return alerts, nil
}

func (s *OperationsService) topUpGapAlerts(now int64, limit int) ([]map[string]interface{}, error) {
	if !s.hasTopUps() {
		return []map[string]interface{}{}, nil
	}
	query := s.db.RebindQuery(fmt.Sprintf(`
		SELECT tu.user_id as user_id,
			%s as username,
			COALESCE(MAX(u.status), 0) as user_status,
			COALESCE(MAX(u.quota), 0) as current_quota,
			COALESCE(MAX(u.used_quota), 0) as lifetime_quota,
			tu.topup_count as topup_count,
			tu.first_topup_time as first_topup_time,
			tu.last_topup_time as last_topup_time,
			tu.topup_amount as topup_amount,
			tu.topup_money as topup_money,
			COALESCE(MAX(l.last_request_time), 0) as last_request_time
		FROM (
			SELECT t.user_id,
				COUNT(*) as topup_count,
				MIN(%s) as first_topup_time,
				MAX(%s) as last_topup_time,
				COALESCE(SUM(t.amount), 0) as topup_amount,
				COALESCE(SUM(t.money), 0) as topup_money
			FROM top_ups t
			WHERE %s
			GROUP BY t.user_id
		) tu
		INNER JOIN users u ON u.id = tu.user_id AND u.deleted_at IS NULL
		LEFT JOIN (
			SELECT user_id, MAX(created_at) as last_request_time
			FROM logs
			WHERE type IN (2, 5)
			GROUP BY user_id
		) l ON l.user_id = tu.user_id
		WHERE tu.topup_count >= 2 AND tu.last_topup_time <= ?
		GROUP BY tu.user_id, tu.topup_count, tu.first_topup_time, tu.last_topup_time, tu.topup_amount, tu.topup_money
		ORDER BY tu.topup_money DESC, tu.topup_amount DESC
		LIMIT ?`, s.userDisplayNameExpr(), s.successTopUpTimeExpr("t"), s.successTopUpTimeExpr("t"), s.successTopUpCondition("t")))
	rows, err := s.db.QueryWithTimeout(20*time.Second, query, now-7*86400, limit*2)
	if err != nil {
		return nil, err
	}
	alerts := []map[string]interface{}{}
	for _, row := range rows {
		count := toInt64(row["topup_count"])
		first := toInt64(row["first_topup_time"])
		last := toInt64(row["last_topup_time"])
		if count < 2 || first <= 0 || last <= 0 || last <= first {
			continue
		}
		avgInterval := (last - first) / (count - 1)
		threshold := int64(math.Max(float64(avgInterval*2), float64(7*86400)))
		overdue := now - last
		if overdue < threshold {
			continue
		}
		severity := "medium"
		if overdue >= threshold*2 || toFloat64(row["topup_money"]) >= 200 {
			severity = "high"
		}
		userID := toInt64(row["user_id"])
		alerts = append(alerts, s.userAlert(row, "topup_gap", "retention", severity,
			"高充值用户充值断档",
			[]string{
				fmt.Sprintf("历史成功充值 %d 次，累计金额 %.2f", count, toFloat64(row["topup_money"])),
				fmt.Sprintf("历史平均充值间隔约 %d 天，当前已间隔 %d 天", maxInt64(avgInterval/86400, 1), maxInt64(overdue/86400, 1)),
				fmt.Sprintf("当前余额约 %s", formatOperationQuota(toInt64(row["current_quota"]))),
			},
			"确认用户是否余额不足、体验异常或已迁移到其他渠道，必要时人工联系。",
			last,
			map[string]interface{}{
				"topup_count":       count,
				"topup_money":       row["topup_money"],
				"last_topup_time":   last,
				"overdue_days":      maxInt64(overdue/86400, 1),
				"avg_interval_days": maxInt64(avgInterval/86400, 1),
			}, fmt.Sprintf("topup-gap-%d-%d", userID, last)))
		if len(alerts) >= limit {
			break
		}
	}
	return alerts, nil
}

func (s *OperationsService) newPaidActivationAlerts(now int64, limit int) ([]map[string]interface{}, error) {
	if !s.hasTopUps() {
		return []map[string]interface{}{}, nil
	}
	query := s.db.RebindQuery(fmt.Sprintf(`
		SELECT tu.user_id as user_id,
			%s as username,
			COALESCE(MAX(u.status), 0) as user_status,
			COALESCE(MAX(u.quota), 0) as current_quota,
			COALESCE(MAX(u.used_quota), 0) as lifetime_quota,
			tu.last_topup_time as last_topup_time,
			tu.topup_amount as topup_amount,
			tu.topup_money as topup_money,
			COUNT(l.id) as total_requests,
			SUM(CASE WHEN l.type = 2 THEN 1 ELSE 0 END) as success_requests,
			SUM(CASE WHEN l.type = 5 THEN 1 ELSE 0 END) as failure_requests,
			COALESCE(AVG(CASE WHEN l.type = 2 THEN l.use_time ELSE NULL END), 0) as avg_use_time
		FROM (
			SELECT t.user_id,
				MAX(%s) as last_topup_time,
				COALESCE(SUM(t.amount), 0) as topup_amount,
				COALESCE(SUM(t.money), 0) as topup_money
			FROM top_ups t
			WHERE %s
			GROUP BY t.user_id
		) tu
		INNER JOIN users u ON u.id = tu.user_id AND u.deleted_at IS NULL
		LEFT JOIN logs l ON l.user_id = tu.user_id AND l.created_at >= tu.last_topup_time AND l.created_at <= ? AND l.type IN (2, 5)
		WHERE tu.last_topup_time >= ?
		GROUP BY tu.user_id, tu.last_topup_time, tu.topup_amount, tu.topup_money
		HAVING COUNT(l.id) = 0 OR (COUNT(l.id) >= 3 AND (SUM(CASE WHEN l.type = 5 THEN 1 ELSE 0 END) * 1.0 / NULLIF(COUNT(l.id), 0)) >= 0.5)
		ORDER BY tu.last_topup_time DESC
		LIMIT ?`, s.userDisplayNameExpr(), s.successTopUpTimeExpr("t"), s.successTopUpCondition("t")))
	rows, err := s.db.QueryWithTimeout(20*time.Second, query, now, now-newPaidActivationWindow, limit)
	if err != nil {
		return nil, err
	}
	alerts := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		total := toInt64(row["total_requests"])
		failures := toInt64(row["failure_requests"])
		failureRate := 0.0
		if total > 0 {
			failureRate = float64(failures) / float64(total)
		}
		severity := "medium"
		title := "新充值用户未激活"
		if total >= 3 && failureRate >= 0.5 {
			severity = "high"
			title = "新充值用户调用失败偏高"
		}
		lastTopup := toInt64(row["last_topup_time"])
		userID := toInt64(row["user_id"])
		evidence := []string{
			fmt.Sprintf("最近充值时间距今约 %d 小时", maxInt64((now-lastTopup)/3600, 0)),
			fmt.Sprintf("充值后请求 %d 次，失败 %d 次", total, failures),
		}
		if total > 0 {
			evidence = append(evidence, fmt.Sprintf("充值后失败率 %.1f%%", failureRate*100))
		} else {
			evidence = append(evidence, "充值后尚未出现成功或失败调用记录")
		}
		alerts = append(alerts, s.userAlert(row, "new_paid_activation_failed", "activation", severity,
			title, evidence,
			"优先检查用户是否不会配置、模型不可用或支付后体验受阻。",
			lastTopup,
			map[string]interface{}{
				"last_topup_time": lastTopup,
				"topup_money":     row["topup_money"],
				"total_requests":  total,
				"failure_rate":    failureRate,
			}, fmt.Sprintf("new-paid-activation-%d-%d", userID, lastTopup)))
	}
	return alerts, nil
}

func (s *OperationsService) experienceAlerts(now int64, limit int) ([]map[string]interface{}, error) {
	startTime := now - experienceWindowSeconds
	topupSelect, topupJoin := s.topUpAggregateSelectAndJoin()
	topupMoneyExpr := "0"
	if s.hasTopUps() {
		topupMoneyExpr = "COALESCE(MAX(tu.topup_money), 0)"
	}
	query := s.db.RebindQuery(fmt.Sprintf(`
		SELECT l.user_id as user_id,
			%s as username,
			COALESCE(MAX(u.status), 0) as user_status,
			COALESCE(MAX(u.quota), 0) as current_quota,
			COALESCE(MAX(u.used_quota), 0) as lifetime_quota,
			COUNT(*) as total_requests,
			SUM(CASE WHEN l.type = 2 THEN 1 ELSE 0 END) as success_requests,
			SUM(CASE WHEN l.type = 5 THEN 1 ELSE 0 END) as failure_requests,
			(SUM(CASE WHEN l.type = 5 THEN 1 ELSE 0 END) * 1.0) / NULLIF(COUNT(*), 0) as failure_rate,
			COALESCE(AVG(CASE WHEN l.type = 2 THEN l.use_time ELSE NULL END), 0) as avg_use_time,
			COALESCE(SUM(l.quota), 0) as quota_used,
			COALESCE(MAX(l.created_at), 0) as last_request_time,
			%s
		FROM logs l
		INNER JOIN users u ON u.id = l.user_id AND u.deleted_at IS NULL
		%s
		WHERE l.created_at >= ? AND l.created_at <= ? AND l.type IN (2, 5) AND l.user_id IS NOT NULL
		GROUP BY l.user_id
		HAVING COUNT(*) >= 20
			AND ((SUM(CASE WHEN l.type = 5 THEN 1 ELSE 0 END) * 1.0) / NULLIF(COUNT(*), 0) >= ? OR COALESCE(AVG(CASE WHEN l.type = 2 THEN l.use_time ELSE NULL END), 0) >= ?)
			AND (COALESCE(MAX(u.used_quota), 0) >= ? OR %s >= ? OR COALESCE(SUM(l.quota), 0) >= ?)
		ORDER BY failure_rate DESC, avg_use_time DESC
		LIMIT ?`, s.displayNameExpr("l"), topupSelect, topupJoin, topupMoneyExpr))
	rows, err := s.db.QueryWithTimeout(25*time.Second, query, startTime, now, experienceFailureRate, experienceSlowUseTimeMillis, highValueLifetimeQuota, highValueTopUpMoney, highValueQuotaThreshold, limit)
	if err != nil {
		return nil, err
	}
	alerts := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		total := toInt64(row["total_requests"])
		failures := toInt64(row["failure_requests"])
		failureRate := toFloat64(row["failure_rate"])
		avgUseTime := toFloat64(row["avg_use_time"])
		severity := "medium"
		title := "高价值用户体验异常"
		if failureRate >= 0.5 {
			severity = "critical"
			title = "高价值用户失败率过高"
		} else if avgUseTime >= 30000 {
			severity = "high"
			title = "高价值用户响应耗时过高"
		}
		triggeredAt := toInt64(row["last_request_time"])
		userID := toInt64(row["user_id"])
		alerts = append(alerts, s.userAlert(row, "paid_user_experience", "experience", severity,
			title,
			[]string{
				fmt.Sprintf("24h 内请求 %d 次，失败 %d 次", total, failures),
				fmt.Sprintf("失败率 %.1f%%，平均响应 %.2fms", failureRate*100, avgUseTime),
				fmt.Sprintf("24h 消耗 %s", formatOperationQuota(toInt64(row["quota_used"]))),
			},
			"优先查看原始日志，确认是否为模型/渠道故障或用户配置问题。",
			triggeredAt,
			map[string]interface{}{
				"total_requests": total,
				"failure_rate":   failureRate,
				"avg_use_time":   avgUseTime,
				"quota_used":     row["quota_used"],
			}, fmt.Sprintf("paid-user-experience-%d-%d", userID, triggeredAt)))
	}
	return alerts, nil
}

func (s *OperationsService) paymentStateAlerts(startTime, now int64, limit int) ([]map[string]interface{}, error) {
	if !s.hasTopUps() {
		return []map[string]interface{}{}, nil
	}
	alerts := []map[string]interface{}{}
	pendingQuery := s.db.RebindQuery(fmt.Sprintf(`
		SELECT t.id, t.user_id, %s as username,
			COALESCE(t.amount, 0) as amount, COALESCE(t.money, 0) as money,
			COALESCE(t.trade_no, '') as trade_no, COALESCE(t.payment_method, '') as payment_method,
			COALESCE(t.create_time, 0) as create_time, COALESCE(t.status, '') as status,
			COALESCE(u.status, 0) as user_status, COALESCE(u.quota, 0) as current_quota,
			COALESCE(u.used_quota, 0) as lifetime_quota
		FROM top_ups t
		LEFT JOIN users u ON u.id = t.user_id AND u.deleted_at IS NULL
		WHERE t.create_time >= ? AND t.create_time <= ? AND %s
		ORDER BY t.create_time ASC
		LIMIT ?`, s.simpleUserDisplayNameExpr(), s.pendingTopUpCondition("t")))
	pendingRows, err := s.db.QueryWithTimeout(15*time.Second, pendingQuery, startTime, now-stalePendingPaymentSeconds, limit)
	if err != nil {
		return nil, err
	}
	for _, row := range pendingRows {
		createTime := toInt64(row["create_time"])
		ageMinutes := int64(0)
		if createTime > 0 {
			ageMinutes = (now - createTime) / 60
		}
		severity := "medium"
		if ageMinutes >= 120 {
			severity = "high"
		}
		userID := toInt64(row["user_id"])
		alerts = append(alerts, s.userAlert(row, "payment_pending_stale", "payment", severity,
			"支付订单长时间待支付",
			[]string{
				fmt.Sprintf("订单创建后已待处理约 %d 分钟", ageMinutes),
				fmt.Sprintf("支付方式 %s，订单号 %s", toString(row["payment_method"]), toString(row["trade_no"])),
			},
			"检查支付回跳、查单兜底和订单最终状态，避免用户看到错误状态。",
			createTime,
			map[string]interface{}{
				"topup_id":       row["id"],
				"trade_no":       row["trade_no"],
				"payment_method": row["payment_method"],
				"age_minutes":    ageMinutes,
				"amount":         row["amount"],
				"money":          row["money"],
			}, fmt.Sprintf("payment-pending-%d-%d", userID, toInt64(row["id"]))))
	}

	duplicateQuery := s.db.RebindQuery(fmt.Sprintf(`
		SELECT COALESCE(t.trade_no, '') as trade_no,
			MIN(t.user_id) as user_id,
			COUNT(*) as duplicate_count,
			COALESCE(SUM(t.amount), 0) as amount,
			COALESCE(SUM(t.money), 0) as money,
			MIN(t.create_time) as first_seen,
			MAX(t.create_time) as last_seen
		FROM top_ups t
		WHERE t.create_time >= ? AND t.trade_no IS NOT NULL AND t.trade_no != '' AND %s
		GROUP BY t.trade_no
		HAVING COUNT(*) > 1
		ORDER BY duplicate_count DESC, last_seen DESC
		LIMIT ?`, s.successTopUpCondition("t")))
	duplicateRows, err := s.db.QueryWithTimeout(15*time.Second, duplicateQuery, startTime, limit)
	if err == nil {
		for _, row := range duplicateRows {
			userID := toInt64(row["user_id"])
			row["username"] = ""
			row["current_quota"] = 0
			row["lifetime_quota"] = 0
			alerts = append(alerts, s.userAlert(row, "payment_duplicate_success", "payment", "critical",
				"疑似重复成功到账订单",
				[]string{
					fmt.Sprintf("同一订单号出现 %d 条成功充值记录", toInt64(row["duplicate_count"])),
					fmt.Sprintf("订单号 %s，累计金额 %.2f", toString(row["trade_no"]), toFloat64(row["money"])),
				},
				"优先人工核对支付平台和用户余额，避免重复到账扩大。",
				toInt64(row["last_seen"]),
				map[string]interface{}{
					"trade_no":        row["trade_no"],
					"duplicate_count": row["duplicate_count"],
					"amount":          row["amount"],
					"money":           row["money"],
				}, fmt.Sprintf("payment-duplicate-%s-%d", toString(row["trade_no"]), userID)))
		}
	}

	if len(alerts) > limit {
		alerts = alerts[:limit]
	}
	return alerts, nil
}

func (s *OperationsService) userAlert(row map[string]interface{}, alertType, category, severity, title string, evidence []string, suggestion string, triggeredAt int64, metrics map[string]interface{}, id string) map[string]interface{} {
	userID := toInt64(row["user_id"])
	username := toString(row["username"])
	return map[string]interface{}{
		"id":               id,
		"type":             alertType,
		"category":         category,
		"severity":         severity,
		"title":            title,
		"user_id":          userID,
		"username":         username,
		"user_status":      row["user_status"],
		"triggered_at":     triggeredAt,
		"evidence":         evidence,
		"suggested_action": suggestion,
		"metrics":          metrics,
		"drilldown": map[string]interface{}{
			"user_detail":   fmt.Sprintf("/api/operations/users/%d/detail", userID),
			"analytics_url": fmt.Sprintf("/analytics?user_id=%d&source=operations&alert_type=%s", userID, alertType),
		},
	}
}

func (s *OperationsService) displayNameExpr(logAlias string) string {
	userIDCast := "CAST(MAX(u.id) AS CHAR)"
	if s.db.IsPG {
		userIDCast = "CAST(MAX(u.id) AS TEXT)"
	}
	return fmt.Sprintf("COALESCE(NULLIF(MAX(u.display_name), ''), NULLIF(MAX(u.username), ''), NULLIF(MAX(%s.username), ''), %s)", logAlias, userIDCast)
}

func (s *OperationsService) userDisplayNameExpr() string {
	userIDCast := "CAST(MAX(u.id) AS CHAR)"
	if s.db.IsPG {
		userIDCast = "CAST(MAX(u.id) AS TEXT)"
	}
	return fmt.Sprintf("COALESCE(NULLIF(MAX(u.display_name), ''), NULLIF(MAX(u.username), ''), %s)", userIDCast)
}

func (s *OperationsService) simpleUserDisplayNameExpr() string {
	userIDCast := "CAST(u.id AS CHAR)"
	if s.db.IsPG {
		userIDCast = "CAST(u.id AS TEXT)"
	}
	return fmt.Sprintf("COALESCE(NULLIF(u.display_name, ''), NULLIF(u.username, ''), %s, '')", userIDCast)
}

func (s *OperationsService) hasTopUps() bool {
	exists, err := s.db.TableExists("top_ups")
	return err == nil && exists
}

func (s *OperationsService) successTopUpCondition(alias string) string {
	return fmt.Sprintf("(LOWER(%s.status) IN ('success', 'completed') OR %s.status = '1')", alias, alias)
}

func (s *OperationsService) pendingTopUpCondition(alias string) string {
	return fmt.Sprintf("(%s.status IS NULL OR %s.status = '' OR (LOWER(%s.status) NOT IN ('success', 'failed', 'completed', 'error') AND %s.status NOT IN ('1', '-1')))", alias, alias, alias, alias)
}

func (s *OperationsService) successTopUpTimeExpr(alias string) string {
	return fmt.Sprintf("COALESCE(NULLIF(%s.complete_time, 0), %s.create_time)", alias, alias)
}

func (s *OperationsService) topUpAggregateSelectAndJoin() (string, string) {
	if !s.hasTopUps() {
		return "0 as topup_count, 0 as last_topup_time, 0 as topup_money, 0 as topup_amount", ""
	}
	selectSQL := `
			COALESCE(MAX(tu.topup_count), 0) as topup_count,
			COALESCE(MAX(tu.last_topup_time), 0) as last_topup_time,
			COALESCE(MAX(tu.topup_money), 0) as topup_money,
			COALESCE(MAX(tu.topup_amount), 0) as topup_amount`
	joinSQL := fmt.Sprintf(`
		LEFT JOIN (
			SELECT t.user_id,
				COUNT(*) as topup_count,
				MAX(%s) as last_topup_time,
				COALESCE(SUM(t.money), 0) as topup_money,
				COALESCE(SUM(t.amount), 0) as topup_amount
			FROM top_ups t
			WHERE %s
			GROUP BY t.user_id
		) tu ON tu.user_id = l.user_id`, s.successTopUpTimeExpr("t"), s.successTopUpCondition("t"))
	return selectSQL, joinSQL
}

func buildOperationsSummary(items []map[string]interface{}) map[string]interface{} {
	bySeverity := map[string]int{"critical": 0, "high": 0, "medium": 0, "low": 0}
	byCategory := map[string]int{}
	byType := map[string]int{}
	userSet := map[int64]bool{}
	for _, item := range items {
		sev := toString(item["severity"])
		if _, ok := bySeverity[sev]; !ok {
			bySeverity[sev] = 0
		}
		bySeverity[sev]++
		cat := toString(item["category"])
		byCategory[cat]++
		typ := toString(item["type"])
		byType[typ]++
		if uid := toInt64(item["user_id"]); uid > 0 {
			userSet[uid] = true
		}
	}
	return map[string]interface{}{
		"total_alerts":       len(items),
		"affected_users":     len(userSet),
		"by_severity":        bySeverity,
		"by_category":        byCategory,
		"by_type":            byType,
		"needs_attention":    bySeverity["critical"] + bySeverity["high"],
		"revenue_alerts_off": true,
	}
}

func severityWeight(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func formatOperationQuota(quota int64) string {
	return fmt.Sprintf("$%.2f", float64(quota)/operationsQuotaPerUSD)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
