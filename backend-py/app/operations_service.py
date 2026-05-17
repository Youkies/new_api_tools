"""
Operations alert service.
Builds admin-facing retention, activation, experience, and payment-state alerts.
Revenue/margin alerts are intentionally excluded until upstream pricing is reliable.
"""
from __future__ import annotations

import math
import time
from decimal import Decimal
from typing import Any, Dict, List, Optional

from .database import DatabaseEngine, DatabaseManager, get_db_manager
from .risk_monitoring_service import WINDOW_SECONDS


QUOTA_PER_USD = 500000.0
HIGH_VALUE_QUOTA = 2_500_000
HIGH_VALUE_REQUESTS = 100
HIGH_VALUE_LIFETIME_QUOTA = 5_000_000
HIGH_VALUE_TOPUP_MONEY = 50.0
QUIET_SECONDS = 72 * 3600
NEW_PAID_WINDOW = 72 * 3600
STALE_PENDING_SECONDS = 15 * 60
EXPERIENCE_WINDOW = 24 * 3600
EXPERIENCE_FAILURE_RATE = 0.25
EXPERIENCE_SLOW_MS = 10000.0


_cache: dict[str, tuple[float, dict[str, Any]]] = {}


def _to_int(value: Any) -> int:
    try:
        return int(value or 0)
    except Exception:
        return 0


def _to_float(value: Any) -> float:
    try:
        return float(value or 0)
    except Exception:
        return 0.0


def _quota_label(value: Any) -> str:
    return f"${_to_float(value) / QUOTA_PER_USD:.2f}"


def _normalize(value: Any) -> Any:
    if isinstance(value, Decimal):
        return float(value)
    if isinstance(value, list):
        return [_normalize(item) for item in value]
    if isinstance(value, dict):
        return {key: _normalize(item) for key, item in value.items()}
    return value


class OperationsService:
    def __init__(self, db: Optional[DatabaseManager] = None):
        self._db = db

    @property
    def db(self) -> DatabaseManager:
        if self._db is None:
            self._db = get_db_manager()
        return self._db

    def get_alerts(
        self,
        window: str = "30d",
        alert_type: str = "all",
        severity: str = "all",
        limit: int = 100,
        use_cache: bool = True,
    ) -> dict[str, Any]:
        seconds = WINDOW_SECONDS.get(window, WINDOW_SECONDS["30d"])
        if window not in WINDOW_SECONDS:
            window = "30d"
        limit = max(1, min(limit, 300))
        alert_type = alert_type or "all"
        severity = severity or "all"
        cache_key = f"operations:alerts:{window}:{alert_type}:{severity}:{limit}"
        now = int(time.time())

        if use_cache:
            cached = _cache.get(cache_key)
            if cached and cached[0] > time.time():
                data = dict(cached[1])
                data["cache_hit"] = True
                return data

        start_time = now - seconds
        alerts: list[dict[str, Any]] = []
        builders = (
            (self._high_value_silent_alerts, True),
            (self._topup_gap_alerts, False),
            (self._new_paid_activation_alerts, False),
            (self._experience_alerts, False),
            (self._payment_state_alerts, True),
        )
        for builder, needs_start in builders:
            try:
                if needs_start:
                    alerts.extend(builder(start_time, now, limit))
                else:
                    alerts.extend(builder(now, limit))
            except Exception:
                continue

        filtered = [
            item for item in alerts
            if (alert_type == "all" or item.get("type") == alert_type or item.get("category") == alert_type)
            and (severity == "all" or item.get("severity") == severity)
        ]
        filtered.sort(key=lambda item: (_severity_weight(str(item.get("severity"))), _to_int(item.get("triggered_at"))), reverse=True)
        filtered = filtered[:limit]

        result = {
            "items": _normalize(filtered),
            "summary": self._summary(filtered),
            "total": len(filtered),
            "window": window,
            "generated_at": now,
            "snapshot_time": now,
            "cache_hit": False,
            "notes": [
                "v1 暂不包含收入/毛利异常，因为未接入上游真实价格系统",
                "注册邮箱只在用户详情中展示，用于管理员人工联系",
            ],
        }
        _cache[cache_key] = (time.time() + 120, result)
        return result

    def get_user_detail(self, user_id: int, window: str = "30d") -> dict[str, Any]:
        seconds = WINDOW_SECONDS.get(window, WINDOW_SECONDS["30d"])
        if window not in WINDOW_SECONDS:
            window = "30d"
        now = int(time.time())
        start_time = now - seconds
        group_col = '"group"' if self.db.config.engine == DatabaseEngine.POSTGRESQL else "`group`"
        created_expr = "COALESCE(created_at, 0) as created_at" if self._column_exists("users", "created_at") else "0 as created_at"
        rows = self.db.execute(f"""
            SELECT id, COALESCE(username, '') as username, COALESCE(display_name, '') as display_name,
                COALESCE(email, '') as email, COALESCE(status, 0) as status, COALESCE(role, 0) as role,
                COALESCE({group_col}, '') as user_group, COALESCE(quota, 0) as quota,
                COALESCE(used_quota, 0) as used_quota, COALESCE(request_count, 0) as request_count,
                {created_expr}
            FROM users
            WHERE id = :user_id AND deleted_at IS NULL
        """, {"user_id": user_id})
        if not rows:
            raise ValueError("user not found")
        user = rows[0]

        usage_rows = self.db.execute("""
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
            WHERE user_id = :user_id AND created_at >= :start_time AND created_at <= :end_time AND type IN (2, 5)
        """, {"user_id": user_id, "start_time": start_time, "end_time": now})
        usage = usage_rows[0] if usage_rows else {}
        total = _to_int(usage.get("total_requests"))
        failures = _to_int(usage.get("failure_requests"))
        usage["failure_rate"] = failures / total if total > 0 else 0

        recent_logs = self.db.execute("""
            SELECT created_at, type, COALESCE(model_name, '') as model_name, COALESCE(channel_id, 0) as channel_id,
                COALESCE(quota, 0) as quota, COALESCE(use_time, 0) as use_time,
                COALESCE(ip, '') as ip, COALESCE(request_id, '') as request_id
            FROM logs
            WHERE user_id = :user_id AND type IN (2, 5)
            ORDER BY created_at DESC
            LIMIT 20
        """, {"user_id": user_id})

        topups: dict[str, Any] = {"available": False}
        recent_topups: list[dict[str, Any]] = []
        if self._has_topups():
            topup_rows = self.db.execute(f"""
                SELECT COUNT(*) as total_count,
                    SUM(CASE WHEN {self._success_condition('t')} THEN 1 ELSE 0 END) as success_count,
                    COALESCE(SUM(CASE WHEN {self._success_condition('t')} THEN amount ELSE 0 END), 0) as success_amount,
                    COALESCE(SUM(CASE WHEN {self._success_condition('t')} THEN money ELSE 0 END), 0) as success_money,
                    COALESCE(MAX(CASE WHEN {self._success_condition('t')} THEN {self._success_time_expr('t')} ELSE 0 END), 0) as last_success_time
                FROM top_ups t
                WHERE user_id = :user_id
            """, {"user_id": user_id})
            if topup_rows:
                topups = dict(topup_rows[0])
                topups["available"] = True
            recent_topups = self.db.execute("""
                SELECT id, COALESCE(amount, 0) as amount, COALESCE(money, 0) as money,
                    COALESCE(trade_no, '') as trade_no, COALESCE(payment_method, '') as payment_method,
                    COALESCE(create_time, 0) as create_time, COALESCE(complete_time, 0) as complete_time,
                    COALESCE(status, '') as status
                FROM top_ups
                WHERE user_id = :user_id
                ORDER BY create_time DESC
                LIMIT 10
            """, {"user_id": user_id})

        return _normalize({
            "user": {
                "id": user.get("id"),
                "username": user.get("username"),
                "display_name": user.get("display_name"),
                "email": user.get("email"),
                "status": user.get("status"),
                "role": user.get("role"),
                "group": user.get("user_group"),
                "quota": user.get("quota"),
                "used_quota": user.get("used_quota"),
                "request_count": user.get("request_count"),
                "created_at": user.get("created_at"),
            },
            "window": window,
            "snapshot_time": now,
            "usage": usage,
            "topups": topups,
            "recent_topups": recent_topups,
            "recent_logs": recent_logs,
            "privacy_note": "注册邮箱仅用于管理员人工联系；不要发送给外部 AI 或默认导出。",
        })

    def _high_value_silent_alerts(self, start_time: int, now: int, limit: int) -> list[dict[str, Any]]:
        quiet_cutoff = now - QUIET_SECONDS
        if start_time >= quiet_cutoff:
            start_time = quiet_cutoff - WINDOW_SECONDS["30d"]
        topup_select, topup_join = self._topup_select_join()
        rows = self.db.execute(f"""
            SELECT l.user_id as user_id, {self._display_name_expr('l')} as username,
                COALESCE(MAX(u.status), 0) as user_status,
                COALESCE(MAX(u.quota), 0) as current_quota,
                COALESCE(MAX(u.used_quota), 0) as lifetime_quota,
                COUNT(*) as historical_requests,
                SUM(CASE WHEN l.type = 2 THEN 1 ELSE 0 END) as historical_success_requests,
                COALESCE(SUM(l.quota), 0) as historical_quota,
                COALESCE(MAX(l.created_at), 0) as last_request_time,
                {topup_select}
            FROM logs l
            INNER JOIN users u ON u.id = l.user_id AND u.deleted_at IS NULL
            {topup_join}
            WHERE l.created_at >= :start_time AND l.created_at < :quiet_cutoff
                AND l.type IN (2, 5) AND l.user_id IS NOT NULL
                AND NOT EXISTS (
                    SELECT 1 FROM logs lr
                    WHERE lr.user_id = l.user_id AND lr.created_at >= :quiet_cutoff AND lr.type IN (2, 5)
                )
            GROUP BY l.user_id
            HAVING COALESCE(SUM(l.quota), 0) >= :quota_threshold OR COUNT(*) >= :request_threshold
            ORDER BY historical_quota DESC, historical_requests DESC
            LIMIT :limit
        """, {
            "start_time": start_time,
            "quiet_cutoff": quiet_cutoff,
            "quota_threshold": HIGH_VALUE_QUOTA,
            "request_threshold": HIGH_VALUE_REQUESTS,
            "limit": limit,
        })
        alerts = []
        for row in rows:
            quota = _to_int(row.get("historical_quota"))
            requests = _to_int(row.get("historical_requests"))
            last_request = _to_int(row.get("last_request_time"))
            silent_hours = (now - last_request) // 3600 if last_request else 0
            severity = "critical" if quota >= 10_000_000 or _to_float(row.get("topup_money")) >= 200 else "high"
            if quota < HIGH_VALUE_QUOTA and requests < 500 and _to_float(row.get("topup_money")) < HIGH_VALUE_TOPUP_MONEY:
                severity = "medium"
            alerts.append(self._user_alert(
                row, "high_value_silent", "retention", severity, "高价值用户突然停用",
                [
                    f"历史窗口消耗 {_quota_label(quota)}，累计 {requests} 次请求",
                    f"最近一次调用距今约 {silent_hours} 小时",
                    f"当前余额约 {_quota_label(row.get('current_quota'))}",
                ],
                "检查最近失败日志、余额和模型可用性，必要时通过注册邮箱联系用户。",
                last_request,
                {
                    "historical_quota": quota,
                    "historical_requests": requests,
                    "silent_hours": silent_hours,
                    "current_quota": row.get("current_quota"),
                    "last_request_time": last_request,
                },
                f"high-value-silent-{_to_int(row.get('user_id'))}-{last_request}",
            ))
        return alerts

    def _topup_gap_alerts(self, now: int, limit: int) -> list[dict[str, Any]]:
        if not self._has_topups():
            return []
        rows = self.db.execute(f"""
            SELECT tu.user_id as user_id, {self._aggregate_user_name_expr()} as username,
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
                SELECT t.user_id, COUNT(*) as topup_count,
                    MIN({self._success_time_expr('t')}) as first_topup_time,
                    MAX({self._success_time_expr('t')}) as last_topup_time,
                    COALESCE(SUM(t.amount), 0) as topup_amount,
                    COALESCE(SUM(t.money), 0) as topup_money
                FROM top_ups t
                WHERE {self._success_condition('t')}
                GROUP BY t.user_id
            ) tu
            INNER JOIN users u ON u.id = tu.user_id AND u.deleted_at IS NULL
            LEFT JOIN (
                SELECT user_id, MAX(created_at) as last_request_time
                FROM logs
                WHERE type IN (2, 5)
                GROUP BY user_id
            ) l ON l.user_id = tu.user_id
            WHERE tu.topup_count >= 2 AND tu.last_topup_time <= :oldest
            GROUP BY tu.user_id, tu.topup_count, tu.first_topup_time, tu.last_topup_time, tu.topup_amount, tu.topup_money
            ORDER BY tu.topup_money DESC, tu.topup_amount DESC
            LIMIT :limit
        """, {"oldest": now - 7 * 86400, "limit": limit * 2})
        alerts = []
        for row in rows:
            count = _to_int(row.get("topup_count"))
            first = _to_int(row.get("first_topup_time"))
            last = _to_int(row.get("last_topup_time"))
            if count < 2 or first <= 0 or last <= first:
                continue
            avg_interval = (last - first) // max(count - 1, 1)
            threshold = int(max(avg_interval * 2, 7 * 86400))
            overdue = now - last
            if overdue < threshold:
                continue
            severity = "high" if overdue >= threshold * 2 or _to_float(row.get("topup_money")) >= 200 else "medium"
            alerts.append(self._user_alert(
                row, "topup_gap", "retention", severity, "高充值用户充值断档",
                [
                    f"历史成功充值 {count} 次，累计金额 {_to_float(row.get('topup_money')):.2f}",
                    f"历史平均充值间隔约 {max(avg_interval // 86400, 1)} 天，当前已间隔 {max(overdue // 86400, 1)} 天",
                    f"当前余额约 {_quota_label(row.get('current_quota'))}",
                ],
                "确认用户是否余额不足、体验异常或已迁移到其他渠道，必要时人工联系。",
                last,
                {
                    "topup_count": count,
                    "topup_money": row.get("topup_money"),
                    "last_topup_time": last,
                    "overdue_days": max(overdue // 86400, 1),
                    "avg_interval_days": max(avg_interval // 86400, 1),
                },
                f"topup-gap-{_to_int(row.get('user_id'))}-{last}",
            ))
            if len(alerts) >= limit:
                break
        return alerts

    def _new_paid_activation_alerts(self, now: int, limit: int) -> list[dict[str, Any]]:
        if not self._has_topups():
            return []
        rows = self.db.execute(f"""
            SELECT tu.user_id as user_id, {self._aggregate_user_name_expr()} as username,
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
                SELECT t.user_id, MAX({self._success_time_expr('t')}) as last_topup_time,
                    COALESCE(SUM(t.amount), 0) as topup_amount,
                    COALESCE(SUM(t.money), 0) as topup_money
                FROM top_ups t
                WHERE {self._success_condition('t')}
                GROUP BY t.user_id
            ) tu
            INNER JOIN users u ON u.id = tu.user_id AND u.deleted_at IS NULL
            LEFT JOIN logs l ON l.user_id = tu.user_id AND l.created_at >= tu.last_topup_time AND l.created_at <= :now AND l.type IN (2, 5)
            WHERE tu.last_topup_time >= :start_time
            GROUP BY tu.user_id, tu.last_topup_time, tu.topup_amount, tu.topup_money
            HAVING COUNT(l.id) = 0 OR (COUNT(l.id) >= 3 AND (SUM(CASE WHEN l.type = 5 THEN 1 ELSE 0 END) * 1.0 / NULLIF(COUNT(l.id), 0)) >= 0.5)
            ORDER BY tu.last_topup_time DESC
            LIMIT :limit
        """, {"now": now, "start_time": now - NEW_PAID_WINDOW, "limit": limit})
        alerts = []
        for row in rows:
            total = _to_int(row.get("total_requests"))
            failures = _to_int(row.get("failure_requests"))
            failure_rate = failures / total if total > 0 else 0
            severity = "high" if total >= 3 and failure_rate >= 0.5 else "medium"
            title = "新充值用户调用失败偏高" if severity == "high" else "新充值用户未激活"
            last_topup = _to_int(row.get("last_topup_time"))
            evidence = [
                f"最近充值时间距今约 {max((now - last_topup) // 3600, 0)} 小时",
                f"充值后请求 {total} 次，失败 {failures} 次",
            ]
            evidence.append(f"充值后失败率 {failure_rate * 100:.1f}%" if total > 0 else "充值后尚未出现成功或失败调用记录")
            alerts.append(self._user_alert(
                row, "new_paid_activation_failed", "activation", severity, title,
                evidence,
                "优先检查用户是否不会配置、模型不可用或支付后体验受阻。",
                last_topup,
                {
                    "last_topup_time": last_topup,
                    "topup_money": row.get("topup_money"),
                    "total_requests": total,
                    "failure_rate": failure_rate,
                },
                f"new-paid-activation-{_to_int(row.get('user_id'))}-{last_topup}",
            ))
        return alerts

    def _experience_alerts(self, now: int, limit: int) -> list[dict[str, Any]]:
        start_time = now - EXPERIENCE_WINDOW
        topup_select, topup_join = self._topup_select_join()
        topup_money_expr = "COALESCE(MAX(tu.topup_money), 0)" if self._has_topups() else "0"
        rows = self.db.execute(f"""
            SELECT l.user_id as user_id, {self._display_name_expr('l')} as username,
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
                {topup_select}
            FROM logs l
            INNER JOIN users u ON u.id = l.user_id AND u.deleted_at IS NULL
            {topup_join}
            WHERE l.created_at >= :start_time AND l.created_at <= :now AND l.type IN (2, 5) AND l.user_id IS NOT NULL
            GROUP BY l.user_id
            HAVING COUNT(*) >= 20
                AND ((SUM(CASE WHEN l.type = 5 THEN 1 ELSE 0 END) * 1.0) / NULLIF(COUNT(*), 0) >= :failure_rate OR COALESCE(AVG(CASE WHEN l.type = 2 THEN l.use_time ELSE NULL END), 0) >= :slow_ms)
                AND (COALESCE(MAX(u.used_quota), 0) >= :lifetime_quota OR {topup_money_expr} >= :topup_money OR COALESCE(SUM(l.quota), 0) >= :quota_threshold)
            ORDER BY failure_rate DESC, avg_use_time DESC
            LIMIT :limit
        """, {
            "start_time": start_time,
            "now": now,
            "failure_rate": EXPERIENCE_FAILURE_RATE,
            "slow_ms": EXPERIENCE_SLOW_MS,
            "lifetime_quota": HIGH_VALUE_LIFETIME_QUOTA,
            "topup_money": HIGH_VALUE_TOPUP_MONEY,
            "quota_threshold": HIGH_VALUE_QUOTA,
            "limit": limit,
        })
        alerts = []
        for row in rows:
            total = _to_int(row.get("total_requests"))
            failures = _to_int(row.get("failure_requests"))
            failure_rate = _to_float(row.get("failure_rate"))
            avg_use_time = _to_float(row.get("avg_use_time"))
            severity = "medium"
            title = "高价值用户体验异常"
            if failure_rate >= 0.5:
                severity = "critical"
                title = "高价值用户失败率过高"
            elif avg_use_time >= 30000:
                severity = "high"
                title = "高价值用户响应耗时过高"
            triggered_at = _to_int(row.get("last_request_time"))
            alerts.append(self._user_alert(
                row, "paid_user_experience", "experience", severity, title,
                [
                    f"24h 内请求 {total} 次，失败 {failures} 次",
                    f"失败率 {failure_rate * 100:.1f}%，平均响应 {avg_use_time:.2f}ms",
                    f"24h 消耗 {_quota_label(row.get('quota_used'))}",
                ],
                "优先查看原始日志，确认是否为模型/渠道故障或用户配置问题。",
                triggered_at,
                {
                    "total_requests": total,
                    "failure_rate": failure_rate,
                    "avg_use_time": avg_use_time,
                    "quota_used": row.get("quota_used"),
                },
                f"paid-user-experience-{_to_int(row.get('user_id'))}-{triggered_at}",
            ))
        return alerts

    def _payment_state_alerts(self, start_time: int, now: int, limit: int) -> list[dict[str, Any]]:
        if not self._has_topups():
            return []
        alerts = []
        pending_rows = self.db.execute(f"""
            SELECT t.id, t.user_id, {self._simple_user_name_expr()} as username,
                COALESCE(t.amount, 0) as amount, COALESCE(t.money, 0) as money,
                COALESCE(t.trade_no, '') as trade_no, COALESCE(t.payment_method, '') as payment_method,
                COALESCE(t.create_time, 0) as create_time, COALESCE(t.status, '') as status,
                COALESCE(u.status, 0) as user_status, COALESCE(u.quota, 0) as current_quota,
                COALESCE(u.used_quota, 0) as lifetime_quota
            FROM top_ups t
            LEFT JOIN users u ON u.id = t.user_id AND u.deleted_at IS NULL
            WHERE t.create_time >= :start_time AND t.create_time <= :stale_before AND {self._pending_condition('t')}
            ORDER BY t.create_time ASC
            LIMIT :limit
        """, {"start_time": start_time, "stale_before": now - STALE_PENDING_SECONDS, "limit": limit})
        for row in pending_rows:
            create_time = _to_int(row.get("create_time"))
            age_minutes = (now - create_time) // 60 if create_time else 0
            severity = "high" if age_minutes >= 120 else "medium"
            alerts.append(self._user_alert(
                row, "payment_pending_stale", "payment", severity, "支付订单长时间待支付",
                [
                    f"订单创建后已待处理约 {age_minutes} 分钟",
                    f"支付方式 {row.get('payment_method') or ''}，订单号 {row.get('trade_no') or ''}",
                ],
                "检查支付回跳、查单兜底和订单最终状态，避免用户看到错误状态。",
                create_time,
                {
                    "topup_id": row.get("id"),
                    "trade_no": row.get("trade_no"),
                    "payment_method": row.get("payment_method"),
                    "age_minutes": age_minutes,
                    "amount": row.get("amount"),
                    "money": row.get("money"),
                },
                f"payment-pending-{_to_int(row.get('user_id'))}-{_to_int(row.get('id'))}",
            ))

        duplicate_rows = self.db.execute(f"""
            SELECT COALESCE(t.trade_no, '') as trade_no,
                MIN(t.user_id) as user_id,
                COUNT(*) as duplicate_count,
                COALESCE(SUM(t.amount), 0) as amount,
                COALESCE(SUM(t.money), 0) as money,
                MIN(t.create_time) as first_seen,
                MAX(t.create_time) as last_seen
            FROM top_ups t
            WHERE t.create_time >= :start_time AND t.trade_no IS NOT NULL AND t.trade_no != '' AND {self._success_condition('t')}
            GROUP BY t.trade_no
            HAVING COUNT(*) > 1
            ORDER BY duplicate_count DESC, last_seen DESC
            LIMIT :limit
        """, {"start_time": start_time, "limit": limit})
        for row in duplicate_rows:
            row["username"] = ""
            row["current_quota"] = 0
            row["lifetime_quota"] = 0
            user_id = _to_int(row.get("user_id"))
            alerts.append(self._user_alert(
                row, "payment_duplicate_success", "payment", "critical", "疑似重复成功到账订单",
                [
                    f"同一订单号出现 {_to_int(row.get('duplicate_count'))} 条成功充值记录",
                    f"订单号 {row.get('trade_no') or ''}，累计金额 {_to_float(row.get('money')):.2f}",
                ],
                "优先人工核对支付平台和用户余额，避免重复到账扩大。",
                _to_int(row.get("last_seen")),
                {
                    "trade_no": row.get("trade_no"),
                    "duplicate_count": row.get("duplicate_count"),
                    "amount": row.get("amount"),
                    "money": row.get("money"),
                },
                f"payment-duplicate-{row.get('trade_no')}-{user_id}",
            ))
        return alerts[:limit]

    def _user_alert(
        self,
        row: dict[str, Any],
        alert_type: str,
        category: str,
        severity: str,
        title: str,
        evidence: list[str],
        suggestion: str,
        triggered_at: int,
        metrics: dict[str, Any],
        alert_id: str,
    ) -> dict[str, Any]:
        user_id = _to_int(row.get("user_id"))
        return {
            "id": alert_id,
            "type": alert_type,
            "category": category,
            "severity": severity,
            "title": title,
            "user_id": user_id,
            "username": row.get("username") or "",
            "user_status": row.get("user_status"),
            "triggered_at": triggered_at,
            "evidence": evidence,
            "suggested_action": suggestion,
            "metrics": metrics,
            "drilldown": {
                "user_detail": f"/api/operations/users/{user_id}/detail",
                "analytics_url": f"/analytics?user_id={user_id}&source=operations&alert_type={alert_type}",
            },
        }

    def _summary(self, items: list[dict[str, Any]]) -> dict[str, Any]:
        by_severity = {"critical": 0, "high": 0, "medium": 0, "low": 0}
        by_category: dict[str, int] = {}
        by_type: dict[str, int] = {}
        users = set()
        for item in items:
            sev = str(item.get("severity") or "")
            by_severity[sev] = by_severity.get(sev, 0) + 1
            cat = str(item.get("category") or "")
            by_category[cat] = by_category.get(cat, 0) + 1
            typ = str(item.get("type") or "")
            by_type[typ] = by_type.get(typ, 0) + 1
            uid = _to_int(item.get("user_id"))
            if uid:
                users.add(uid)
        return {
            "total_alerts": len(items),
            "affected_users": len(users),
            "by_severity": by_severity,
            "by_category": by_category,
            "by_type": by_type,
            "needs_attention": by_severity.get("critical", 0) + by_severity.get("high", 0),
            "revenue_alerts_off": True,
        }

    def _has_topups(self) -> bool:
        return self._table_exists("top_ups")

    def _table_exists(self, table_name: str) -> bool:
        try:
            if self.db.config.engine == DatabaseEngine.POSTGRESQL:
                rows = self.db.execute("""
                    SELECT 1 FROM information_schema.tables WHERE table_name = :table_name LIMIT 1
                """, {"table_name": table_name})
            else:
                rows = self.db.execute("""
                    SELECT 1 FROM information_schema.tables
                    WHERE table_schema = :db_name AND table_name = :table_name LIMIT 1
                """, {"db_name": self.db.config.database, "table_name": table_name})
            return bool(rows)
        except Exception:
            return False

    def _column_exists(self, table_name: str, column_name: str) -> bool:
        try:
            if self.db.config.engine == DatabaseEngine.POSTGRESQL:
                rows = self.db.execute("""
                    SELECT 1 FROM information_schema.columns
                    WHERE table_name = :table_name AND column_name = :column_name LIMIT 1
                """, {"table_name": table_name, "column_name": column_name})
            else:
                rows = self.db.execute("""
                    SELECT 1 FROM information_schema.columns
                    WHERE table_schema = :db_name AND table_name = :table_name AND column_name = :column_name LIMIT 1
                """, {"db_name": self.db.config.database, "table_name": table_name, "column_name": column_name})
            return bool(rows)
        except Exception:
            return False

    def _success_condition(self, alias: str) -> str:
        return f"(LOWER({alias}.status) IN ('success', 'completed') OR {alias}.status = '1')"

    def _pending_condition(self, alias: str) -> str:
        return (
            f"({alias}.status IS NULL OR {alias}.status = '' OR "
            f"(LOWER({alias}.status) NOT IN ('success', 'failed', 'completed', 'error') "
            f"AND {alias}.status NOT IN ('1', '-1')))"
        )

    def _success_time_expr(self, alias: str) -> str:
        return f"COALESCE(NULLIF({alias}.complete_time, 0), {alias}.create_time)"

    def _display_name_expr(self, log_alias: str) -> str:
        user_id_cast = "CAST(MAX(u.id) AS TEXT)" if self.db.config.engine == DatabaseEngine.POSTGRESQL else "CAST(MAX(u.id) AS CHAR)"
        return f"COALESCE(NULLIF(MAX(u.display_name), ''), NULLIF(MAX(u.username), ''), NULLIF(MAX({log_alias}.username), ''), {user_id_cast})"

    def _aggregate_user_name_expr(self) -> str:
        user_id_cast = "CAST(MAX(u.id) AS TEXT)" if self.db.config.engine == DatabaseEngine.POSTGRESQL else "CAST(MAX(u.id) AS CHAR)"
        return f"COALESCE(NULLIF(MAX(u.display_name), ''), NULLIF(MAX(u.username), ''), {user_id_cast})"

    def _simple_user_name_expr(self) -> str:
        user_id_cast = "CAST(u.id AS TEXT)" if self.db.config.engine == DatabaseEngine.POSTGRESQL else "CAST(u.id AS CHAR)"
        return f"COALESCE(NULLIF(u.display_name, ''), NULLIF(u.username, ''), {user_id_cast}, '')"

    def _topup_select_join(self) -> tuple[str, str]:
        if not self._has_topups():
            return "0 as topup_count, 0 as last_topup_time, 0 as topup_money, 0 as topup_amount", ""
        select_sql = """
                COALESCE(MAX(tu.topup_count), 0) as topup_count,
                COALESCE(MAX(tu.last_topup_time), 0) as last_topup_time,
                COALESCE(MAX(tu.topup_money), 0) as topup_money,
                COALESCE(MAX(tu.topup_amount), 0) as topup_amount
        """
        join_sql = f"""
            LEFT JOIN (
                SELECT t.user_id, COUNT(*) as topup_count,
                    MAX({self._success_time_expr('t')}) as last_topup_time,
                    COALESCE(SUM(t.money), 0) as topup_money,
                    COALESCE(SUM(t.amount), 0) as topup_amount
                FROM top_ups t
                WHERE {self._success_condition('t')}
                GROUP BY t.user_id
            ) tu ON tu.user_id = l.user_id
        """
        return select_sql, join_sql


def _severity_weight(severity: str) -> int:
    return {"critical": 4, "high": 3, "medium": 2, "low": 1}.get(severity, 0)


_operations_service: Optional[OperationsService] = None


def get_operations_service() -> OperationsService:
    global _operations_service
    if _operations_service is None:
        _operations_service = OperationsService()
    return _operations_service
