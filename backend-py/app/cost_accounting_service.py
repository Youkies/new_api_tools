"""
Channel cost accounting service.
"""
import math
import time
from dataclasses import dataclass
from datetime import datetime
from typing import Any, Dict, List, Optional

from sqlalchemy import text

from .database import DatabaseEngine, DatabaseManager, get_db_manager

QUOTA_PER_USD = 500000.0


@dataclass
class ChannelCostRule:
    id: int = 0
    channel_id: int = 0
    model_name: str = "*"
    upstream_model: str = ""
    billing_mode: str = "token"
    input_cost_per_million: float = 0.0
    output_cost_per_million: float = 0.0
    request_cost: float = 0.0
    cost_multiplier: float = 1.0
    enabled: bool = True
    updated_at: int = 0


def default_cost_range() -> tuple[int, int]:
    now = datetime.now()
    start = datetime(now.year, now.month, now.day)
    return int(start.timestamp()), int(time.time())


class CostAccountingService:
    """Cost accounting queries and settings."""

    def __init__(self, db: Optional[DatabaseManager] = None):
        self._db = db

    @property
    def db(self) -> DatabaseManager:
        if self._db is None:
            self._db = get_db_manager()
        return self._db

    def _ensure_table(self) -> None:
        if self.db.config.engine == DatabaseEngine.POSTGRESQL:
            ddl = """
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
                )
            """
        else:
            ddl = """
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
                )
            """
        self.db.execute(ddl)
        if not self._column_exists("api_tools_channel_costs", "cost_multiplier"):
            if self.db.config.engine == DatabaseEngine.POSTGRESQL:
                self.db.execute("ALTER TABLE api_tools_channel_costs ADD COLUMN cost_multiplier DOUBLE PRECISION NOT NULL DEFAULT 1")
            else:
                self.db.execute("ALTER TABLE api_tools_channel_costs ADD COLUMN cost_multiplier DOUBLE NOT NULL DEFAULT 1")

    def _column_exists(self, table_name: str, column_name: str) -> bool:
        try:
            if self.db.config.engine == DatabaseEngine.POSTGRESQL:
                sql = """
                    SELECT 1
                    FROM information_schema.columns
                    WHERE table_name = :table_name AND column_name = :column_name
                    LIMIT 1
                """
                rows = self.db.execute(sql, {"table_name": table_name, "column_name": column_name})
            else:
                sql = """
                    SELECT 1
                    FROM information_schema.columns
                    WHERE table_schema = :db_name AND table_name = :table_name AND column_name = :column_name
                    LIMIT 1
                """
                rows = self.db.execute(sql, {
                    "db_name": self.db.config.database,
                    "table_name": table_name,
                    "column_name": column_name,
                })
            return bool(rows)
        except Exception:
            return False

    def list_rules(self) -> List[Dict[str, Any]]:
        self._ensure_table()
        rows = self.db.execute("""
            SELECT id, channel_id, model_name, upstream_model, billing_mode,
                input_cost_per_million, output_cost_per_million, request_cost, cost_multiplier,
                enabled, updated_at
            FROM api_tools_channel_costs
            ORDER BY channel_id ASC, model_name ASC
        """)
        return [self._rule_to_dict(row) for row in rows]

    def save_rules(self, rules: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
        self._ensure_table()
        if len(rules) > 2000:
            raise ValueError(f"too many cost rules: {len(rules)}")

        now = int(time.time())
        normalized: List[Dict[str, Any]] = []
        seen = set()
        for raw in rules:
            rule = self._normalize_rule(raw, now)
            if rule["channel_id"] <= 0:
                continue
            key = (rule["channel_id"], rule["model_name"])
            if key in seen:
                continue
            seen.add(key)
            normalized.append(rule)

        insert_sql = text("""
            INSERT INTO api_tools_channel_costs
                (channel_id, model_name, upstream_model, billing_mode,
                 input_cost_per_million, output_cost_per_million, request_cost, cost_multiplier,
                 enabled, updated_at)
            VALUES
                (:channel_id, :model_name, :upstream_model, :billing_mode,
                 :input_cost_per_million, :output_cost_per_million, :request_cost, :cost_multiplier,
                 :enabled, :updated_at)
        """)
        with self.db.engine.begin() as conn:
            conn.execute(text("DELETE FROM api_tools_channel_costs"))
            if normalized:
                conn.execute(insert_sql, normalized)

        return self.list_rules()

    def list_channels(self) -> List[Dict[str, Any]]:
        rows = self.db.execute("""
            SELECT id, name, type, status,
                COALESCE(used_quota, 0) as used_quota,
                COALESCE(balance, 0) as balance,
                priority
            FROM channels
            WHERE deleted_at IS NULL
            ORDER BY priority DESC, id ASC
        """)
        for row in rows:
            if not row.get("name"):
                row["name"] = f"Channel#{row.get('id')}"
        return rows

    def rules_payload(self) -> Dict[str, Any]:
        return {
            "rules": self.list_rules(),
            "channels": self.list_channels(),
        }

    def get_summary(self, start_time: int, end_time: int, channel_id: Optional[int] = None) -> Dict[str, Any]:
        if start_time <= 0 or end_time <= 0:
            start_time, end_time = default_cost_range()
        if end_time < start_time:
            raise ValueError("end_time must be greater than or equal to start_time")

        rules = self.list_rules()
        rule_map = self._build_rule_map(rules)

        params: Dict[str, Any] = {"start_time": start_time, "end_time": end_time}
        channel_clause = ""
        if channel_id and channel_id > 0:
            channel_clause = " AND l.channel_id = :channel_id"
            params["channel_id"] = channel_id

        rows = self.db.execute(f"""
            SELECT COALESCE(l.channel_id, 0) as channel_id,
                COALESCE(MAX(c.name), '') as channel_name,
                COALESCE(NULLIF(l.model_name, ''), 'unknown') as model_name,
                COUNT(*) as request_count,
                COALESCE(SUM(l.quota), 0) as quota_used,
                COALESCE(SUM(l.prompt_tokens), 0) as prompt_tokens,
                COALESCE(SUM(l.completion_tokens), 0) as completion_tokens
            FROM logs l
            LEFT JOIN channels c ON c.id = l.channel_id
            WHERE l.created_at >= :start_time AND l.created_at <= :end_time AND l.type = 2
                {channel_clause}
            GROUP BY COALESCE(l.channel_id, 0), COALESCE(NULLIF(l.model_name, ''), 'unknown')
            ORDER BY request_count DESC
        """, params)

        channels_by_id: Dict[int, Dict[str, Any]] = {}
        totals = {
            "request_count": 0,
            "quota_used": 0,
            "prompt_tokens": 0,
            "completion_tokens": 0,
            "billed_amount": 0.0,
            "estimated_cost": 0.0,
            "configured_models": 0,
            "unconfigured_models": 0,
        }

        for row in rows:
            cid = int(row.get("channel_id") or 0)
            model_name = str(row.get("model_name") or "unknown")
            channel_name = str(row.get("channel_name") or f"Channel#{cid}")
            if cid == 0:
                channel_name = "Unknown Channel"

            requests = int(row.get("request_count") or 0)
            quota_used = int(row.get("quota_used") or 0)
            prompt_tokens = int(row.get("prompt_tokens") or 0)
            completion_tokens = int(row.get("completion_tokens") or 0)
            billed_amount = quota_used / QUOTA_PER_USD

            rule = self._find_rule(rule_map, cid, model_name)
            configured = rule is not None
            estimated_cost = 0.0
            upstream_model = model_name
            billing_mode = "token"
            rule_id = 0
            if rule:
                upstream_model = rule.get("upstream_model") or model_name
                if upstream_model == "*":
                    upstream_model = model_name
                billing_mode = rule.get("billing_mode") or "token"
                rule_id = int(rule.get("id") or 0)
                estimated_cost = self._calculate_cost(rule, requests, prompt_tokens, completion_tokens)

            margin = billed_amount - estimated_cost
            model_row = {
                "channel_id": cid,
                "channel_name": channel_name,
                "model_name": model_name,
                "upstream_model": upstream_model,
                "billing_mode": billing_mode,
                "request_count": requests,
                "quota_used": quota_used,
                "prompt_tokens": prompt_tokens,
                "completion_tokens": completion_tokens,
                "billed_amount": self._round_money(billed_amount),
                "estimated_cost": self._round_money(estimated_cost),
                "gross_margin": self._round_money(margin),
                "margin_rate": self._margin_rate(margin, billed_amount),
                "cost_multiplier": self._cost_multiplier(rule if rule else None),
                "configured": configured,
                "rule_id": rule_id,
            }

            channel = channels_by_id.setdefault(cid, {
                "channel_id": cid,
                "channel_name": channel_name,
                "request_count": 0,
                "quota_used": 0,
                "prompt_tokens": 0,
                "completion_tokens": 0,
                "billed_amount": 0.0,
                "estimated_cost": 0.0,
                "gross_margin": 0.0,
                "configured_models": 0,
                "unconfigured_models": 0,
                "models": [],
            })
            channel["request_count"] += requests
            channel["quota_used"] += quota_used
            channel["prompt_tokens"] += prompt_tokens
            channel["completion_tokens"] += completion_tokens
            channel["billed_amount"] += billed_amount
            channel["estimated_cost"] += estimated_cost
            channel["gross_margin"] += margin
            channel["configured_models" if configured else "unconfigured_models"] += 1
            channel["models"].append(model_row)

            totals["request_count"] += requests
            totals["quota_used"] += quota_used
            totals["prompt_tokens"] += prompt_tokens
            totals["completion_tokens"] += completion_tokens
            totals["billed_amount"] += billed_amount
            totals["estimated_cost"] += estimated_cost
            totals["configured_models" if configured else "unconfigured_models"] += 1

        channels = list(channels_by_id.values())
        for channel in channels:
            channel["billed_amount"] = self._round_money(channel["billed_amount"])
            channel["estimated_cost"] = self._round_money(channel["estimated_cost"])
            channel["gross_margin"] = self._round_money(channel["gross_margin"])
            channel["margin_rate"] = self._margin_rate(channel["gross_margin"], channel["billed_amount"])
            channel["models"].sort(key=lambda item: (item["estimated_cost"], item["request_count"]), reverse=True)
        channels.sort(key=lambda item: (item["estimated_cost"], item["request_count"]), reverse=True)

        gross_margin = totals["billed_amount"] - totals["estimated_cost"]
        summary = {
            **totals,
            "billed_amount": self._round_money(totals["billed_amount"]),
            "estimated_cost": self._round_money(totals["estimated_cost"]),
            "gross_margin": self._round_money(gross_margin),
            "margin_rate": self._margin_rate(gross_margin, totals["billed_amount"]),
        }

        return {
            "range": {"start_time": start_time, "end_time": end_time},
            "summary": summary,
            "channels": channels,
            "rules": rules,
        }

    def _rule_to_dict(self, row: Dict[str, Any]) -> Dict[str, Any]:
        return {
            "id": int(row.get("id") or 0),
            "channel_id": int(row.get("channel_id") or 0),
            "model_name": str(row.get("model_name") or "*"),
            "upstream_model": str(row.get("upstream_model") or ""),
            "billing_mode": self._normalize_billing_mode(str(row.get("billing_mode") or "token")),
            "input_cost_per_million": float(row.get("input_cost_per_million") or 0),
            "output_cost_per_million": float(row.get("output_cost_per_million") or 0),
            "request_cost": float(row.get("request_cost") or 0),
            "cost_multiplier": self._cost_multiplier(row),
            "enabled": bool(row.get("enabled")),
            "updated_at": int(row.get("updated_at") or 0),
        }

    def _normalize_rule(self, raw: Dict[str, Any], updated_at: int) -> Dict[str, Any]:
        model_name = str(raw.get("model_name") or "*").strip() or "*"
        upstream_model = str(raw.get("upstream_model") or model_name).strip() or model_name
        return {
            "channel_id": int(raw.get("channel_id") or 0),
            "model_name": model_name,
            "upstream_model": upstream_model,
            "billing_mode": self._normalize_billing_mode(str(raw.get("billing_mode") or "token")),
            "input_cost_per_million": self._non_negative(raw.get("input_cost_per_million")),
            "output_cost_per_million": self._non_negative(raw.get("output_cost_per_million")),
            "request_cost": self._non_negative(raw.get("request_cost")),
            "cost_multiplier": self._cost_multiplier(raw),
            "enabled": bool(raw.get("enabled", True)),
            "updated_at": updated_at,
        }

    def _build_rule_map(self, rules: List[Dict[str, Any]]) -> Dict[str, Any]:
        exact = {}
        wildcard = {}
        for rule in rules:
            if not rule.get("enabled") or int(rule.get("channel_id") or 0) <= 0:
                continue
            cid = int(rule["channel_id"])
            if rule.get("model_name") == "*":
                wildcard[cid] = rule
            else:
                exact[(cid, rule.get("model_name"))] = rule
        return {"exact": exact, "wildcard": wildcard}

    def _find_rule(self, rule_map: Dict[str, Any], channel_id: int, model_name: str) -> Optional[Dict[str, Any]]:
        return rule_map["exact"].get((channel_id, model_name)) or rule_map["wildcard"].get(channel_id)

    def _calculate_cost(self, rule: Dict[str, Any], requests: int, prompt_tokens: int, completion_tokens: int) -> float:
        multiplier = self._cost_multiplier(rule)
        if rule.get("billing_mode") == "request":
            return requests * float(rule.get("request_cost") or 0) * multiplier
        return (
            prompt_tokens / 1_000_000.0 * float(rule.get("input_cost_per_million") or 0)
            + completion_tokens / 1_000_000.0 * float(rule.get("output_cost_per_million") or 0)
        ) * multiplier

    def _normalize_billing_mode(self, mode: str) -> str:
        return "request" if mode.strip().lower() == "request" else "token"

    def _non_negative(self, value: Any) -> float:
        try:
            parsed = float(value or 0)
        except (TypeError, ValueError):
            return 0.0
        if parsed < 0 or math.isnan(parsed) or math.isinf(parsed):
            return 0.0
        return parsed

    def _cost_multiplier(self, row: Optional[Dict[str, Any]]) -> float:
        if not row:
            return 1.0
        try:
            parsed = float(row.get("cost_multiplier") or 0)
        except (TypeError, ValueError):
            return 1.0
        if parsed <= 0 or math.isnan(parsed) or math.isinf(parsed):
            return 1.0
        return parsed

    def _round_money(self, value: float) -> float:
        return round(float(value or 0), 6)

    def _margin_rate(self, margin: float, billed: float) -> float:
        if billed <= 0:
            return 0.0
        return round(margin / billed * 100, 2)


_service: Optional[CostAccountingService] = None


def get_cost_accounting_service() -> CostAccountingService:
    global _service
    if _service is None:
        _service = CostAccountingService()
    return _service
