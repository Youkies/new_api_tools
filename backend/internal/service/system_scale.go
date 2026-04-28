package service

import (
	"fmt"
	"sync"
	"time"

	"github.com/new-api-tools/backend/internal/cache"
	"github.com/new-api-tools/backend/internal/database"
	"github.com/new-api-tools/backend/internal/logger"
)

const systemScaleCacheKey = "system:scale"

var (
	warmupMu    sync.Mutex
	warmupState = map[string]interface{}{
		"status":       "pending",
		"progress":     0,
		"message":      "等待启动...",
		"steps":        []map[string]interface{}{},
		"started_at":   int64(0),
		"completed_at": int64(0),
	}
	systemTasksOnce sync.Once
)

// GetSystemScale returns workload scale and recommended refresh settings.
func GetSystemScale(force bool) map[string]interface{} {
	cm := cache.Get()
	if !force {
		var cached map[string]interface{}
		if found, _ := cm.GetJSON(systemScaleCacheKey, &cached); found {
			return cached
		}
	}

	db := database.Get()
	now := time.Now().Unix()
	start24h := now - 86400

	totalUsers := queryInt64WithTimeout(db, 5*time.Second, "SELECT COUNT(*) as cnt FROM users WHERE deleted_at IS NULL")
	totalLogs := estimateLogsCount(db)
	logs24h := queryInt64WithTimeout(db, 10*time.Second,
		db.RebindQuery("SELECT COUNT(*) as cnt FROM logs WHERE created_at >= ?"), start24h)
	activeUsers24h := queryInt64WithTimeout(db, 10*time.Second,
		db.RebindQuery("SELECT COUNT(DISTINCT user_id) as cnt FROM logs WHERE created_at >= ? AND user_id IS NOT NULL"), start24h)

	rpm := float64(logs24h) / 1440.0
	scale := "medium"
	description := "中型系统"
	cacheTTL := int64(300)
	refreshInterval := int64(300)
	frontendRefreshInterval := int64(60)

	switch {
	case totalLogs >= 50000000 || logs24h >= 3000000 || rpm >= 2000:
		scale = "xlarge"
		description = "超大型系统"
		cacheTTL = 900
		refreshInterval = 900
		frontendRefreshInterval = 300
	case totalLogs >= 5000000 || logs24h >= 500000 || rpm >= 500:
		scale = "large"
		description = "大型系统"
		cacheTTL = 600
		refreshInterval = 600
		frontendRefreshInterval = 180
	case totalLogs < 500000 && logs24h < 100000 && rpm < 100:
		scale = "small"
		description = "小型系统"
		cacheTTL = 180
		refreshInterval = 180
		frontendRefreshInterval = 60
	}

	result := map[string]interface{}{
		"scale": scale,
		"metrics": map[string]interface{}{
			"total_users":      totalUsers,
			"total_logs":       totalLogs,
			"logs_24h":         logs24h,
			"active_users_24h": activeUsers24h,
			"rpm_avg":          rpm,
		},
		"settings": map[string]interface{}{
			"cache_ttl":                 cacheTTL,
			"leaderboard_cache_ttl":     cacheTTL,
			"refresh_interval":          refreshInterval,
			"frontend_refresh_interval": frontendRefreshInterval,
			"description":               description,
		},
		"generated_at": now,
	}
	cm.Set(systemScaleCacheKey, result, 5*time.Minute)
	return result
}

func queryInt64WithTimeout(db *database.Manager, timeout time.Duration, query string, args ...interface{}) int64 {
	row, err := db.QueryOneWithTimeout(timeout, query, args...)
	if err != nil || row == nil {
		return 0
	}
	return toInt64(row["cnt"])
}

func estimateLogsCount(db *database.Manager) int64 {
	if db.IsPG {
		row, err := db.QueryOneWithTimeout(5*time.Second, `
			SELECT COALESCE(reltuples, 0)::bigint as cnt
			FROM pg_class
			WHERE relname = 'logs'
			LIMIT 1`)
		if err == nil && row != nil {
			if count := toInt64(row["cnt"]); count > 0 {
				return count
			}
		}
	} else {
		row, err := db.QueryOneWithTimeout(5*time.Second, `
			SELECT COALESCE(TABLE_ROWS, 0) as cnt
			FROM information_schema.tables
			WHERE table_schema = DATABASE() AND table_name = 'logs'
			LIMIT 1`)
		if err == nil && row != nil {
			if count := toInt64(row["cnt"]); count > 0 {
				return count
			}
		}
	}

	return queryInt64WithTimeout(db, 15*time.Second, "SELECT COUNT(*) as cnt FROM logs")
}

// StartBackgroundSystemTasks warms hot caches and refreshes them periodically.
func StartBackgroundSystemTasks(stop <-chan struct{}) {
	systemTasksOnce.Do(func() {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.L.Error(fmt.Sprintf("[系统预热] 后台任务 panic: %v", r))
					setWarmupState("error", 100, "预热任务异常", nil)
				}
			}()

			runSystemWarmup(stop)
			runSystemRefreshLoop(stop)
		}()
	})
}

func runSystemWarmup(stop <-chan struct{}) {
	setWarmupState("initializing", 5, "正在检测系统规模...", []map[string]interface{}{
		{"name": "检测系统规模", "status": "pending"},
		{"name": "预热排行榜", "status": "pending"},
		{"name": "预热 Dashboard", "status": "pending"},
		{"name": "预热模型状态", "status": "pending"},
	})

	select {
	case <-time.After(5 * time.Second):
	case <-stop:
		return
	}

	steps := []map[string]interface{}{
		{"name": "检测系统规模", "status": "pending"},
		{"name": "预热排行榜", "status": "pending"},
		{"name": "预热 Dashboard", "status": "pending"},
		{"name": "预热模型状态", "status": "pending"},
	}

	start := time.Now()
	GetSystemScale(true)
	steps[0]["status"] = "done"
	setWarmupState("initializing", 20, "正在预热排行榜...", steps)

	refreshRiskCaches()
	steps[1]["status"] = "done"
	setWarmupState("initializing", 55, "正在预热 Dashboard...", steps)

	refreshDashboardCaches()
	steps[2]["status"] = "done"
	setWarmupState("initializing", 80, "正在预热模型状态...", steps)

	refreshModelStatusCaches()
	steps[3]["status"] = "done"
	setWarmupState("ready", 100, fmt.Sprintf("预热完成，耗时 %.1fs", time.Since(start).Seconds()), steps)
	logger.L.System(fmt.Sprintf("[系统预热] 完成，耗时 %.1fs", time.Since(start).Seconds()))
}

func runSystemRefreshLoop(stop <-chan struct{}) {
	for {
		scale := GetSystemScale(false)
		interval := int64(300)
		if settings, ok := scale["settings"].(map[string]interface{}); ok {
			if v := toInt64(settings["refresh_interval"]); v > 0 {
				interval = v
			}
		}

		select {
		case <-time.After(time.Duration(interval) * time.Second):
		case <-stop:
			return
		}

		refreshRiskCaches()
		refreshDashboardCaches()
		refreshModelStatusCaches()
	}
}

func refreshRiskCaches() {
	svc := NewRiskMonitoringService()
	windows := []string{"1h", "3h", "6h", "12h", "24h", "3d", "7d"}
	_, _ = svc.GetLeaderboards(windows, 10, "requests", false)
}

func refreshDashboardCaches() {
	svc := NewDashboardService()
	_, _ = svc.GetSystemOverview("24h", true)
	_, _ = svc.GetUsageStatistics("24h", true)
	_, _ = svc.GetModelUsage("24h", 8, true)
	_, _ = svc.GetHourlyTrends(24, true)
	_, _ = svc.GetTopUsers("24h", 10, true)
}

func refreshModelStatusCaches() {
	svc := NewModelStatusService()
	models, err := svc.GetAvailableModels()
	if err != nil || len(models) == 0 {
		return
	}

	names := make([]string, 0, 20)
	for _, item := range models {
		name := toString(item["model_name"])
		if name == "" {
			continue
		}
		names = append(names, name)
		if len(names) >= 20 {
			break
		}
	}
	if len(names) > 0 {
		_, _ = svc.GetMultipleModelsStatus(names, DefaultTimeWindow, false)
	}
}

func setWarmupState(status string, progress int, message string, steps []map[string]interface{}) {
	warmupMu.Lock()
	defer warmupMu.Unlock()

	now := time.Now().Unix()
	warmupState["status"] = status
	warmupState["progress"] = progress
	warmupState["message"] = message
	if steps != nil {
		warmupState["steps"] = steps
	}
	if status == "initializing" && toInt64(warmupState["started_at"]) == 0 {
		warmupState["started_at"] = now
	}
	if status == "ready" || status == "error" {
		warmupState["completed_at"] = now
	}
}

// GetWarmupStatus returns a copy of the current warmup state.
func GetWarmupStatus() map[string]interface{} {
	warmupMu.Lock()
	defer warmupMu.Unlock()

	copyState := make(map[string]interface{}, len(warmupState))
	for k, v := range warmupState {
		copyState[k] = v
	}
	return copyState
}
