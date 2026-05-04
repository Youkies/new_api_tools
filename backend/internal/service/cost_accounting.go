package service

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/new-api-tools/backend/internal/database"
)

const costQuotaPerUSD = 500000.0

// ChannelCostRule stores one cost rule for a channel/model alias.
type ChannelCostRule struct {
	ID                   int64   `json:"id"`
	ChannelID            int64   `json:"channel_id"`
	ModelName            string  `json:"model_name"`
	UpstreamModel        string  `json:"upstream_model"`
	BillingMode          string  `json:"billing_mode"`
	InputCostPerMillion  float64 `json:"input_cost_per_million"`
	OutputCostPerMillion float64 `json:"output_cost_per_million"`
	RequestCost          float64 `json:"request_cost"`
	CostMultiplier       float64 `json:"cost_multiplier"`
	Enabled              bool    `json:"enabled"`
	UpdatedAt            int64   `json:"updated_at"`
}

// CostAccountingService handles channel cost configuration and accounting.
type CostAccountingService struct {
	db *database.Manager
}

// NewCostAccountingService creates a cost accounting service.
func NewCostAccountingService() *CostAccountingService {
	return &CostAccountingService{db: database.Get()}
}

// DefaultCostRange returns local today 00:00 to now.
func DefaultCostRange() (int64, int64) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return start.Unix(), now.Unix()
}

func (s *CostAccountingService) ensureCostTable() error {
	var ddl string
	if s.db.IsPG {
		ddl = `
			CREATE TABLE IF NOT EXISTS api_tools_channel_costs (
				id BIGSERIAL PRIMARY KEY,
				channel_id BIGINT NOT NULL,
				model_name TEXT NOT NULL DEFAULT '*',
				upstream_model TEXT NOT NULL DEFAULT '',
				billing_mode VARCHAR(16) NOT NULL DEFAULT 'token',
				input_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0,
				output_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0,
				request_cost DOUBLE PRECISION NOT NULL DEFAULT 0,
				cost_multiplier DOUBLE PRECISION NOT NULL DEFAULT 1,
				enabled BOOLEAN NOT NULL DEFAULT TRUE,
				updated_at BIGINT NOT NULL,
				UNIQUE (channel_id, model_name)
			)`
	} else {
		ddl = `
			CREATE TABLE IF NOT EXISTS api_tools_channel_costs (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				channel_id BIGINT NOT NULL,
				model_name VARCHAR(191) NOT NULL DEFAULT '*',
				upstream_model VARCHAR(191) NOT NULL DEFAULT '',
				billing_mode VARCHAR(16) NOT NULL DEFAULT 'token',
				input_cost_per_million DOUBLE NOT NULL DEFAULT 0,
				output_cost_per_million DOUBLE NOT NULL DEFAULT 0,
				request_cost DOUBLE NOT NULL DEFAULT 0,
				cost_multiplier DOUBLE NOT NULL DEFAULT 1,
				enabled TINYINT(1) NOT NULL DEFAULT 1,
				updated_at BIGINT NOT NULL,
				UNIQUE KEY uniq_api_tools_channel_model (channel_id, model_name),
				KEY idx_api_tools_channel_costs_channel (channel_id)
			)`
	}
	if err := s.db.ExecuteDDL(ddl); err != nil {
		return err
	}

	if !s.db.ColumnExists("api_tools_channel_costs", "cost_multiplier") {
		alterSQL := "ALTER TABLE api_tools_channel_costs ADD COLUMN cost_multiplier DOUBLE NOT NULL DEFAULT 1"
		if s.db.IsPG {
			alterSQL = "ALTER TABLE api_tools_channel_costs ADD COLUMN cost_multiplier DOUBLE PRECISION NOT NULL DEFAULT 1"
		}
		if err := s.db.ExecuteDDL(alterSQL); err != nil {
			return err
		}
	}

	return nil
}

// ListRules returns all configured channel cost rules.
func (s *CostAccountingService) ListRules() ([]ChannelCostRule, error) {
	if err := s.ensureCostTable(); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT id, channel_id, model_name, upstream_model, billing_mode,
			input_cost_per_million, output_cost_per_million, request_cost, cost_multiplier,
			enabled, updated_at
		FROM api_tools_channel_costs
		ORDER BY channel_id ASC, model_name ASC`)
	if err != nil {
		return nil, err
	}

	rules := make([]ChannelCostRule, 0, len(rows))
	for _, row := range rows {
		rules = append(rules, ChannelCostRule{
			ID:                   toInt64(row["id"]),
			ChannelID:            toInt64(row["channel_id"]),
			ModelName:            toString(row["model_name"]),
			UpstreamModel:        toString(row["upstream_model"]),
			BillingMode:          normalizeBillingMode(toString(row["billing_mode"])),
			InputCostPerMillion:  toFloat64(row["input_cost_per_million"]),
			OutputCostPerMillion: toFloat64(row["output_cost_per_million"]),
			RequestCost:          toFloat64(row["request_cost"]),
			CostMultiplier:       normalizeCostMultiplier(toFloat64(row["cost_multiplier"])),
			Enabled:              toBool(row["enabled"]),
			UpdatedAt:            toInt64(row["updated_at"]),
		})
	}
	return rules, nil
}

// SaveRules replaces the full cost rule set.
func (s *CostAccountingService) SaveRules(rules []ChannelCostRule) ([]ChannelCostRule, error) {
	if err := s.ensureCostTable(); err != nil {
		return nil, err
	}
	if len(rules) > 2000 {
		return nil, fmt.Errorf("too many cost rules: %d", len(rules))
	}

	normalized := make([]ChannelCostRule, 0, len(rules))
	seen := map[string]bool{}
	now := time.Now().Unix()
	for _, rule := range rules {
		rule = normalizeCostRule(rule, now)
		if rule.ChannelID <= 0 {
			continue
		}
		key := fmt.Sprintf("%d:%s", rule.ChannelID, rule.ModelName)
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, rule)
	}

	tx, err := s.db.DB.Beginx()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM api_tools_channel_costs"); err != nil {
		return nil, err
	}

	insertSQL := s.db.RebindQuery(`
		INSERT INTO api_tools_channel_costs
			(channel_id, model_name, upstream_model, billing_mode,
			 input_cost_per_million, output_cost_per_million, request_cost, cost_multiplier,
			 enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	for _, rule := range normalized {
		if _, err := tx.Exec(insertSQL,
			rule.ChannelID,
			rule.ModelName,
			rule.UpstreamModel,
			rule.BillingMode,
			rule.InputCostPerMillion,
			rule.OutputCostPerMillion,
			rule.RequestCost,
			rule.CostMultiplier,
			rule.Enabled,
			rule.UpdatedAt,
		); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListRules()
}

// ListChannels returns channel options for the cost UI.
func (s *CostAccountingService) ListChannels() ([]map[string]interface{}, error) {
	query := `
		SELECT id, name, type, status,
			COALESCE(used_quota, 0) as used_quota,
			COALESCE(balance, 0) as balance,
			priority
		FROM channels
	`
	if s.db.ColumnExists("channels", "deleted_at") {
		query += " WHERE deleted_at IS NULL"
	}
	query += " ORDER BY priority DESC, id ASC"

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if toString(row["name"]) == "" {
			row["name"] = fmt.Sprintf("Channel#%d", toInt64(row["id"]))
		}
	}
	return rows, nil
}

// GetRulesPayload returns cost rules plus channels.
func (s *CostAccountingService) GetRulesPayload() (map[string]interface{}, error) {
	rules, err := s.ListRules()
	if err != nil {
		return nil, err
	}
	channels, err := s.ListChannels()
	if err != nil {
		channels = []map[string]interface{}{}
	}
	return map[string]interface{}{
		"rules":    rules,
		"channels": channels,
	}, nil
}

// GetSummary returns cost accounting data for a time range.
func (s *CostAccountingService) GetSummary(startTime, endTime int64, channelID *int64) (map[string]interface{}, error) {
	if startTime <= 0 || endTime <= 0 {
		startTime, endTime = DefaultCostRange()
	}
	if endTime < startTime {
		return nil, fmt.Errorf("end_time must be greater than or equal to start_time")
	}

	rules, err := s.ListRules()
	if err != nil {
		return nil, err
	}

	ruleMap := buildCostRuleMap(rules)
	channelModelMappings, err := s.loadChannelModelMappings(channelID)
	if err != nil {
		return nil, err
	}
	query := `
		SELECT COALESCE(l.channel_id, 0) as channel_id,
			COALESCE(MAX(c.name), '') as channel_name,
			COALESCE(NULLIF(l.model_name, ''), 'unknown') as model_name,
			COUNT(*) as request_count,
			COALESCE(SUM(l.quota), 0) as quota_used,
			COALESCE(SUM(l.prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(l.completion_tokens), 0) as completion_tokens
		FROM logs l
		LEFT JOIN channels c ON c.id = l.channel_id
		WHERE l.created_at >= ? AND l.created_at <= ? AND l.type = 2`
	args := []interface{}{startTime, endTime}
	if channelID != nil && *channelID > 0 {
		query += " AND l.channel_id = ?"
		args = append(args, *channelID)
	}
	query += `
		GROUP BY COALESCE(l.channel_id, 0), COALESCE(NULLIF(l.model_name, ''), 'unknown')
		ORDER BY request_count DESC`

	rows, err := s.db.QueryWithTimeout(45*time.Second, s.db.RebindQuery(query), args...)
	if err != nil {
		return nil, err
	}

	channelsByID := map[int64]map[string]interface{}{}
	totalRequests := int64(0)
	totalQuota := int64(0)
	totalPrompt := int64(0)
	totalCompletion := int64(0)
	totalBilled := 0.0
	totalCost := 0.0
	configuredModels := 0
	unconfiguredModels := 0

	for _, row := range rows {
		cid := toInt64(row["channel_id"])
		modelName := toString(row["model_name"])
		channelName := toString(row["channel_name"])
		if channelName == "" {
			channelName = fmt.Sprintf("Channel#%d", cid)
		}
		if cid == 0 {
			channelName = "Unknown Channel"
		}

		requests := toInt64(row["request_count"])
		quotaUsed := toInt64(row["quota_used"])
		promptTokens := toInt64(row["prompt_tokens"])
		completionTokens := toInt64(row["completion_tokens"])
		billedAmount := float64(quotaUsed) / costQuotaPerUSD

		upstreamModel := resolveUpstreamModel(channelModelMappings, cid, modelName)
		rule, configured := findCostRule(ruleMap, cid, modelName, upstreamModel)
		estimatedCost := 0.0
		billingMode := "token"
		ruleID := int64(0)
		if configured {
			ruleUpstreamModel := strings.TrimSpace(rule.UpstreamModel)
			if ruleUpstreamModel != "" && ruleUpstreamModel != "*" && ruleUpstreamModel != rule.ModelName {
				upstreamModel = ruleUpstreamModel
			}
			billingMode = rule.BillingMode
			ruleID = rule.ID
			estimatedCost = calculateCost(rule, requests, promptTokens, completionTokens)
			configuredModels++
		} else {
			unconfiguredModels++
		}
		margin := billedAmount - estimatedCost

		modelRow := map[string]interface{}{
			"channel_id":        cid,
			"channel_name":      channelName,
			"model_name":        modelName,
			"upstream_model":    upstreamModel,
			"billing_mode":      billingMode,
			"request_count":     requests,
			"quota_used":        quotaUsed,
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"billed_amount":     roundMoney(billedAmount),
			"estimated_cost":    roundMoney(estimatedCost),
			"gross_margin":      roundMoney(margin),
			"margin_rate":       marginRate(margin, billedAmount),
			"cost_multiplier":   ruleCostMultiplier(rule, configured),
			"configured":        configured,
			"rule_id":           ruleID,
		}

		channel, exists := channelsByID[cid]
		if !exists {
			channel = map[string]interface{}{
				"channel_id":          cid,
				"channel_name":        channelName,
				"request_count":       int64(0),
				"quota_used":          int64(0),
				"prompt_tokens":       int64(0),
				"completion_tokens":   int64(0),
				"billed_amount":       float64(0),
				"estimated_cost":      float64(0),
				"gross_margin":        float64(0),
				"configured_models":   0,
				"unconfigured_models": 0,
				"models":              []map[string]interface{}{},
			}
			channelsByID[cid] = channel
		}

		channel["request_count"] = toInt64(channel["request_count"]) + requests
		channel["quota_used"] = toInt64(channel["quota_used"]) + quotaUsed
		channel["prompt_tokens"] = toInt64(channel["prompt_tokens"]) + promptTokens
		channel["completion_tokens"] = toInt64(channel["completion_tokens"]) + completionTokens
		channel["billed_amount"] = toFloat64(channel["billed_amount"]) + billedAmount
		channel["estimated_cost"] = toFloat64(channel["estimated_cost"]) + estimatedCost
		channel["gross_margin"] = toFloat64(channel["gross_margin"]) + margin
		if configured {
			channel["configured_models"] = toInt64(channel["configured_models"]) + 1
		} else {
			channel["unconfigured_models"] = toInt64(channel["unconfigured_models"]) + 1
		}
		models := channel["models"].([]map[string]interface{})
		channel["models"] = append(models, modelRow)

		totalRequests += requests
		totalQuota += quotaUsed
		totalPrompt += promptTokens
		totalCompletion += completionTokens
		totalBilled += billedAmount
		totalCost += estimatedCost
	}

	channels := make([]map[string]interface{}, 0, len(channelsByID))
	for _, channel := range channelsByID {
		channel["billed_amount"] = roundMoney(toFloat64(channel["billed_amount"]))
		channel["estimated_cost"] = roundMoney(toFloat64(channel["estimated_cost"]))
		channel["gross_margin"] = roundMoney(toFloat64(channel["gross_margin"]))
		channel["margin_rate"] = marginRate(toFloat64(channel["gross_margin"]), toFloat64(channel["billed_amount"]))
		models := channel["models"].([]map[string]interface{})
		sort.Slice(models, func(i, j int) bool {
			ci := toFloat64(models[i]["estimated_cost"])
			cj := toFloat64(models[j]["estimated_cost"])
			if ci == cj {
				return toInt64(models[i]["request_count"]) > toInt64(models[j]["request_count"])
			}
			return ci > cj
		})
		channel["models"] = models
		channels = append(channels, channel)
	}
	sort.Slice(channels, func(i, j int) bool {
		ci := toFloat64(channels[i]["estimated_cost"])
		cj := toFloat64(channels[j]["estimated_cost"])
		if ci == cj {
			return toInt64(channels[i]["request_count"]) > toInt64(channels[j]["request_count"])
		}
		return ci > cj
	})

	return map[string]interface{}{
		"range": map[string]interface{}{
			"start_time": startTime,
			"end_time":   endTime,
		},
		"summary": map[string]interface{}{
			"request_count":       totalRequests,
			"quota_used":          totalQuota,
			"prompt_tokens":       totalPrompt,
			"completion_tokens":   totalCompletion,
			"billed_amount":       roundMoney(totalBilled),
			"estimated_cost":      roundMoney(totalCost),
			"gross_margin":        roundMoney(totalBilled - totalCost),
			"margin_rate":         marginRate(totalBilled-totalCost, totalBilled),
			"configured_models":   configuredModels,
			"unconfigured_models": unconfiguredModels,
		},
		"channels": channels,
		"rules":    rules,
	}, nil
}

func (s *CostAccountingService) loadChannelModelMappings(channelID *int64) (map[int64]map[string]string, error) {
	mappings := map[int64]map[string]string{}
	if !s.db.ColumnExists("channels", "model_mapping") {
		return mappings, nil
	}

	query := `SELECT id, model_mapping FROM channels`
	args := []interface{}{}
	conditions := []string{}
	if s.db.ColumnExists("channels", "deleted_at") {
		conditions = append(conditions, "deleted_at IS NULL")
	}
	if channelID != nil && *channelID > 0 {
		conditions = append(conditions, "id = ?")
		args = append(args, *channelID)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	rows, err := s.db.QueryWithTimeout(15*time.Second, s.db.RebindQuery(query), args...)
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		channelID := toInt64(row["id"])
		parsed := parseModelMapping(toString(row["model_mapping"]))
		if channelID > 0 && len(parsed) > 0 {
			mappings[channelID] = parsed
		}
	}
	return mappings, nil
}

func parseModelMapping(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || raw == "{}" || raw == "[]" {
		return map[string]string{}
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return map[string]string{}
	}

	mapping := make(map[string]string, len(decoded))
	for key, value := range decoded {
		source := strings.TrimSpace(key)
		target := mappingValueToString(value)
		if source != "" && target != "" {
			mapping[source] = target
		}
	}
	return mapping
}

func mappingValueToString(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []interface{}:
		for _, item := range v {
			if resolved := mappingValueToString(item); resolved != "" {
				return resolved
			}
		}
	case map[string]interface{}:
		for _, key := range []string{"model", "target", "upstream_model", "upstream", "value"} {
			if resolved := mappingValueToString(v[key]); resolved != "" {
				return resolved
			}
		}
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

func resolveUpstreamModel(mappings map[int64]map[string]string, channelID int64, modelName string) string {
	if channelMapping, ok := mappings[channelID]; ok {
		currentModel := modelName
		visited := map[string]bool{currentModel: true}
		for {
			nextModel := strings.TrimSpace(channelMapping[currentModel])
			if nextModel == "" {
				return currentModel
			}
			if visited[nextModel] {
				return currentModel
			}
			visited[nextModel] = true
			currentModel = nextModel
		}
	}
	return modelName
}

type costRuleMap struct {
	exact    map[string]ChannelCostRule
	wildcard map[int64]ChannelCostRule
}

func buildCostRuleMap(rules []ChannelCostRule) costRuleMap {
	result := costRuleMap{
		exact:    map[string]ChannelCostRule{},
		wildcard: map[int64]ChannelCostRule{},
	}
	for _, rule := range rules {
		if !rule.Enabled || rule.ChannelID <= 0 {
			continue
		}
		rule = normalizeCostRule(rule, rule.UpdatedAt)
		if rule.ModelName == "*" {
			result.wildcard[rule.ChannelID] = rule
			continue
		}
		result.exact[ruleKey(rule.ChannelID, rule.ModelName)] = rule
	}
	return result
}

func findCostRule(rules costRuleMap, channelID int64, modelName, upstreamModel string) (ChannelCostRule, bool) {
	if rule, ok := rules.exact[ruleKey(channelID, modelName)]; ok {
		return rule, true
	}
	if upstreamModel != "" && upstreamModel != modelName {
		if rule, ok := rules.exact[ruleKey(channelID, upstreamModel)]; ok {
			return rule, true
		}
	}
	if rule, ok := rules.wildcard[channelID]; ok {
		return rule, true
	}
	return ChannelCostRule{}, false
}

func ruleKey(channelID int64, modelName string) string {
	return fmt.Sprintf("%d\x00%s", channelID, strings.TrimSpace(modelName))
}

func normalizeCostRule(rule ChannelCostRule, updatedAt int64) ChannelCostRule {
	rule.ModelName = strings.TrimSpace(rule.ModelName)
	if rule.ModelName == "" {
		rule.ModelName = "*"
	}
	rule.UpstreamModel = strings.TrimSpace(rule.UpstreamModel)
	if rule.UpstreamModel == "" {
		rule.UpstreamModel = rule.ModelName
	}
	rule.BillingMode = normalizeBillingMode(rule.BillingMode)
	rule.InputCostPerMillion = nonNegative(rule.InputCostPerMillion)
	rule.OutputCostPerMillion = nonNegative(rule.OutputCostPerMillion)
	rule.RequestCost = nonNegative(rule.RequestCost)
	rule.CostMultiplier = normalizeCostMultiplier(rule.CostMultiplier)
	rule.UpdatedAt = updatedAt
	return rule
}

func normalizeBillingMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "request":
		return "request"
	default:
		return "token"
	}
}

func nonNegative(value float64) float64 {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func calculateCost(rule ChannelCostRule, requests, promptTokens, completionTokens int64) float64 {
	multiplier := normalizeCostMultiplier(rule.CostMultiplier)
	if rule.BillingMode == "request" {
		return float64(requests) * rule.RequestCost * multiplier
	}
	inputCost := float64(promptTokens) / 1_000_000.0 * rule.InputCostPerMillion
	outputCost := float64(completionTokens) / 1_000_000.0 * rule.OutputCostPerMillion
	return (inputCost + outputCost) * multiplier
}

func normalizeCostMultiplier(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 1
	}
	return value
}

func ruleCostMultiplier(rule ChannelCostRule, configured bool) float64 {
	if !configured {
		return 1
	}
	return normalizeCostMultiplier(rule.CostMultiplier)
}

func roundMoney(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}

func marginRate(margin, billed float64) float64 {
	if billed <= 0 {
		return 0
	}
	return math.Round((margin/billed)*10000) / 100
}

func toBool(v interface{}) bool {
	if v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case int64:
		return val != 0
	case int:
		return val != 0
	case int32:
		return val != 0
	case float64:
		return val != 0
	case []byte:
		return string(val) == "1" || strings.EqualFold(string(val), "true")
	case string:
		return val == "1" || strings.EqualFold(val, "true")
	default:
		return false
	}
}
