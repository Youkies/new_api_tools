"""
Channel cost accounting service.
"""
import json
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

    def _table_exists(self, table_name: str) -> bool:
        try:
            if self.db.config.engine == DatabaseEngine.POSTGRESQL:
                sql = """
                    SELECT 1
                    FROM information_schema.tables
                    WHERE table_name = :table_name
                    LIMIT 1
                """
                rows = self.db.execute(sql, {"table_name": table_name})
            else:
                sql = """
                    SELECT 1
                    FROM information_schema.tables
                    WHERE table_schema = :db_name AND table_name = :table_name
                    LIMIT 1
                """
                rows = self.db.execute(sql, {
                    "db_name": self.db.config.database,
                    "table_name": table_name,
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
        sql = """
            SELECT id, name, type, status,
                COALESCE(used_quota, 0) as used_quota,
                COALESCE(balance, 0) as balance,
                priority
            FROM channels
        """
        if self._column_exists("channels", "deleted_at"):
            sql += " WHERE deleted_at IS NULL"
        sql += " ORDER BY priority DESC, id ASC"

        rows = self.db.execute(sql)
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
        channel_model_mappings = self._load_channel_model_mappings(channel_id)
        upstream_join_available = (
            self._column_exists("logs", "id")
            and self._table_exists("api_tools_upstream_logs")
            and self._column_exists("api_tools_upstream_logs", "local_log_id")
        )

        params: Dict[str, Any] = {"start_time": start_time, "end_time": end_time}
        channel_clause = ""
        if channel_id and channel_id > 0:
            channel_clause = " AND l.channel_id = :channel_id"
            params["channel_id"] = channel_id

        upstream_select = """
                COALESCE(SUM(ul.upstream_quota), 0) as upstream_quota,
                COALESCE(SUM(CASE WHEN ul.local_log_id IS NOT NULL THEN 1 ELSE 0 END), 0) as upstream_matched_count,
                COALESCE(SUM(CASE WHEN ul.local_log_id IS NOT NULL THEN l.prompt_tokens ELSE 0 END), 0) as upstream_matched_prompt_tokens,
                COALESCE(SUM(CASE WHEN ul.local_log_id IS NOT NULL THEN l.completion_tokens ELSE 0 END), 0) as upstream_matched_completion_tokens,
                COALESCE(SUM(ul.request_id_matches), 0) as upstream_request_id_matches,
                COALESCE(SUM(ul.tokens_time_matches), 0) as upstream_tokens_time_matches
        """ if upstream_join_available else """
                0 as upstream_quota,
                0 as upstream_matched_count,
                0 as upstream_matched_prompt_tokens,
                0 as upstream_matched_completion_tokens,
                0 as upstream_request_id_matches,
                0 as upstream_tokens_time_matches
        """
        upstream_join = """
            LEFT JOIN (
                SELECT local_log_id,
                    COALESCE(SUM(quota), 0) as upstream_quota,
                    COALESCE(SUM(CASE WHEN match_method = 'request_id' THEN 1 ELSE 0 END), 0) as request_id_matches,
                    COALESCE(SUM(CASE WHEN match_method = 'tokens_time' THEN 1 ELSE 0 END), 0) as tokens_time_matches
                FROM api_tools_upstream_logs
                WHERE created_at >= :start_time AND created_at <= :end_time AND type = 2 AND local_log_id > 0
                GROUP BY local_log_id
            ) ul ON ul.local_log_id = l.id
        """ if upstream_join_available else ""

        rows = self.db.execute(f"""
            SELECT COALESCE(l.channel_id, 0) as channel_id,
                COALESCE(MAX(c.name), '') as channel_name,
                COALESCE(NULLIF(l.model_name, ''), 'unknown') as model_name,
                COUNT(*) as request_count,
                COALESCE(SUM(l.quota), 0) as quota_used,
                COALESCE(SUM(l.prompt_tokens), 0) as prompt_tokens,
                COALESCE(SUM(l.completion_tokens), 0) as completion_tokens,
                {upstream_select}
            FROM logs l
            LEFT JOIN channels c ON c.id = l.channel_id
            {upstream_join}
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
            "rule_estimated_cost": 0.0,
            "upstream_imported_cost": 0.0,
            "upstream_matched_requests": 0,
            "upstream_request_id_matches": 0,
            "upstream_tokens_time_matches": 0,
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
            upstream_quota = int(row.get("upstream_quota") or 0)
            upstream_imported_cost = upstream_quota / QUOTA_PER_USD
            upstream_matched_requests = self._clamp_matched_count(int(row.get("upstream_matched_count") or 0), requests)
            upstream_matched_prompt = self._clamp_matched_count(int(row.get("upstream_matched_prompt_tokens") or 0), prompt_tokens)
            upstream_matched_completion = self._clamp_matched_count(
                int(row.get("upstream_matched_completion_tokens") or 0),
                completion_tokens,
            )
            upstream_request_id_matches = self._clamp_matched_count(
                int(row.get("upstream_request_id_matches") or 0),
                upstream_matched_requests,
            )
            upstream_tokens_time_matches = self._clamp_matched_count(
                int(row.get("upstream_tokens_time_matches") or 0),
                upstream_matched_requests,
            )
            unmatched_requests = requests - upstream_matched_requests
            unmatched_prompt_tokens = prompt_tokens - upstream_matched_prompt
            unmatched_completion_tokens = completion_tokens - upstream_matched_completion

            upstream_model = self._resolve_upstream_model(channel_model_mappings, cid, model_name)
            rule = self._find_rule(rule_map, cid, model_name, upstream_model)
            configured = rule is not None
            estimated_cost = 0.0
            rule_estimated_cost = 0.0
            billing_mode = "token"
            rule_id = 0
            if rule:
                rule_upstream_model = str(rule.get("upstream_model") or "").strip()
                if rule_upstream_model and rule_upstream_model != "*" and rule_upstream_model != rule.get("model_name"):
                    upstream_model = rule_upstream_model
                billing_mode = rule.get("billing_mode") or "token"
                rule_id = int(rule.get("id") or 0)
                rule_estimated_cost = self._calculate_cost(
                    rule,
                    unmatched_requests,
                    unmatched_prompt_tokens,
                    unmatched_completion_tokens,
                )

            estimated_cost = upstream_imported_cost + rule_estimated_cost
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
                "rule_estimated_cost": self._round_money(rule_estimated_cost),
                "upstream_imported_cost": self._round_money(upstream_imported_cost),
                "upstream_matched_requests": upstream_matched_requests,
                "upstream_request_id_matches": upstream_request_id_matches,
                "upstream_tokens_time_matches": upstream_tokens_time_matches,
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
                "rule_estimated_cost": 0.0,
                "upstream_imported_cost": 0.0,
                "upstream_matched_requests": 0,
                "upstream_request_id_matches": 0,
                "upstream_tokens_time_matches": 0,
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
            channel["rule_estimated_cost"] += rule_estimated_cost
            channel["upstream_imported_cost"] += upstream_imported_cost
            channel["upstream_matched_requests"] += upstream_matched_requests
            channel["upstream_request_id_matches"] += upstream_request_id_matches
            channel["upstream_tokens_time_matches"] += upstream_tokens_time_matches
            channel["gross_margin"] += margin
            channel["configured_models" if configured else "unconfigured_models"] += 1
            channel["models"].append(model_row)

            totals["request_count"] += requests
            totals["quota_used"] += quota_used
            totals["prompt_tokens"] += prompt_tokens
            totals["completion_tokens"] += completion_tokens
            totals["billed_amount"] += billed_amount
            totals["estimated_cost"] += estimated_cost
            totals["rule_estimated_cost"] += rule_estimated_cost
            totals["upstream_imported_cost"] += upstream_imported_cost
            totals["upstream_matched_requests"] += upstream_matched_requests
            totals["upstream_request_id_matches"] += upstream_request_id_matches
            totals["upstream_tokens_time_matches"] += upstream_tokens_time_matches
            totals["configured_models" if configured else "unconfigured_models"] += 1

        channels = list(channels_by_id.values())
        for channel in channels:
            channel["billed_amount"] = self._round_money(channel["billed_amount"])
            channel["estimated_cost"] = self._round_money(channel["estimated_cost"])
            channel["rule_estimated_cost"] = self._round_money(channel["rule_estimated_cost"])
            channel["upstream_imported_cost"] = self._round_money(channel["upstream_imported_cost"])
            channel["gross_margin"] = self._round_money(channel["gross_margin"])
            channel["margin_rate"] = self._margin_rate(channel["gross_margin"], channel["billed_amount"])
            channel["models"].sort(key=lambda item: (item["estimated_cost"], item["request_count"]), reverse=True)
        channels.sort(key=lambda item: (item["estimated_cost"], item["request_count"]), reverse=True)

        gross_margin = totals["billed_amount"] - totals["estimated_cost"]
        summary = {
            **totals,
            "billed_amount": self._round_money(totals["billed_amount"]),
            "estimated_cost": self._round_money(totals["estimated_cost"]),
            "rule_estimated_cost": self._round_money(totals["rule_estimated_cost"]),
            "upstream_imported_cost": self._round_money(totals["upstream_imported_cost"]),
            "gross_margin": self._round_money(gross_margin),
            "margin_rate": self._margin_rate(gross_margin, totals["billed_amount"]),
        }
        upstream_import = self._upstream_import_summary(start_time, end_time, channel_id)
        summary["upstream_unmatched_cost"] = self._round_money(max(
            0.0,
            float(upstream_import.get("cost") or 0) - float(totals["upstream_imported_cost"] or 0),
        ))

        return {
            "range": {"start_time": start_time, "end_time": end_time},
            "summary": summary,
            "channels": channels,
            "rules": rules,
            "upstream_import": upstream_import,
        }

    def _upstream_import_summary(
        self,
        start_time: int,
        end_time: int,
        channel_id: Optional[int] = None,
    ) -> Dict[str, Any]:
        result = {
            "available": False,
            "request_count": 0,
            "matched_request_count": 0,
            "unmatched_request_count": 0,
            "request_id_matches": 0,
            "tokens_time_matches": 0,
            "quota_used": 0,
            "cost": 0.0,
        }
        if not self._table_exists("api_tools_upstream_logs"):
            return result

        params: Dict[str, Any] = {"start_time": start_time, "end_time": end_time}
        channel_clause = ""
        if channel_id and channel_id > 0:
            channel_clause = " AND channel_id = :channel_id"
            params["channel_id"] = channel_id
        rows = self.db.execute(f"""
            SELECT COUNT(*) as request_count,
                COALESCE(SUM(CASE WHEN local_log_id > 0 THEN 1 ELSE 0 END), 0) as matched_request_count,
                COALESCE(SUM(CASE WHEN match_method = 'request_id' THEN 1 ELSE 0 END), 0) as request_id_matches,
                COALESCE(SUM(CASE WHEN match_method = 'tokens_time' THEN 1 ELSE 0 END), 0) as tokens_time_matches,
                COALESCE(SUM(quota), 0) as quota_used
            FROM api_tools_upstream_logs
            WHERE created_at >= :start_time AND created_at <= :end_time AND type = 2
                {channel_clause}
        """, params)
        if not rows:
            return result
        quota = int(rows[0].get("quota_used") or 0)
        request_count = int(rows[0].get("request_count") or 0)
        matched_count = int(rows[0].get("matched_request_count") or 0)
        result.update({
            "available": True,
            "request_count": request_count,
            "matched_request_count": matched_count,
            "unmatched_request_count": max(0, request_count - matched_count),
            "request_id_matches": int(rows[0].get("request_id_matches") or 0),
            "tokens_time_matches": int(rows[0].get("tokens_time_matches") or 0),
            "quota_used": quota,
            "cost": self._round_money(quota / QUOTA_PER_USD),
        })
        return result

    def _load_channel_model_mappings(self, channel_id: Optional[int] = None) -> Dict[int, Dict[str, str]]:
        mappings: Dict[int, Dict[str, str]] = {}
        if not self._column_exists("channels", "model_mapping"):
            return mappings

        sql = "SELECT id, model_mapping FROM channels"
        params: Dict[str, Any] = {}
        conditions = []
        if self._column_exists("channels", "deleted_at"):
            conditions.append("deleted_at IS NULL")
        if channel_id and channel_id > 0:
            conditions.append("id = :channel_id")
            params["channel_id"] = channel_id
        if conditions:
            sql += " WHERE " + " AND ".join(conditions)

        rows = self.db.execute(sql, params)
        for row in rows:
            cid = int(row.get("id") or 0)
            parsed = self._parse_model_mapping(row.get("model_mapping"))
            if cid > 0 and parsed:
                mappings[cid] = parsed
        return mappings

    def _parse_model_mapping(self, raw: Any) -> Dict[str, str]:
        if raw is None:
            return {}
        if isinstance(raw, (bytes, bytearray)):
            raw = raw.decode("utf-8", errors="ignore")
        raw_text = str(raw).strip()
        if raw_text in ("", "null", "{}", "[]"):
            return {}

        try:
            decoded = json.loads(raw_text)
        except (TypeError, ValueError):
            return {}
        if not isinstance(decoded, dict):
            return {}

        result: Dict[str, str] = {}
        for source, target in decoded.items():
            source_model = str(source or "").strip()
            target_model = self._mapping_value_to_str(target)
            if source_model and target_model:
                result[source_model] = target_model
        return result

    def _mapping_value_to_str(self, value: Any) -> str:
        if value is None:
            return ""
        if isinstance(value, str):
            return value.strip()
        if isinstance(value, list):
            for item in value:
                resolved = self._mapping_value_to_str(item)
                if resolved:
                    return resolved
            return ""
        if isinstance(value, dict):
            for key in ("model", "target", "upstream_model", "upstream", "value"):
                resolved = self._mapping_value_to_str(value.get(key))
                if resolved:
                    return resolved
            return ""
        return str(value).strip()

    def _resolve_upstream_model(self, mappings: Dict[int, Dict[str, str]], channel_id: int, model_name: str) -> str:
        channel_mapping = mappings.get(channel_id, {})
        current_model = model_name
        visited = {current_model}
        while True:
            next_model = channel_mapping.get(current_model, "").strip()
            if not next_model:
                return current_model
            if next_model in visited:
                return current_model
            visited.add(next_model)
            current_model = next_model

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

    def _find_rule(
        self,
        rule_map: Dict[str, Any],
        channel_id: int,
        model_name: str,
        upstream_model: str,
    ) -> Optional[Dict[str, Any]]:
        exact = rule_map["exact"].get((channel_id, model_name))
        if exact:
            return exact
        if upstream_model and upstream_model != model_name:
            exact = rule_map["exact"].get((channel_id, upstream_model))
            if exact:
                return exact
        return rule_map["wildcard"].get(channel_id)

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

    def _clamp_matched_count(self, value: int, total: int) -> int:
        if value < 0:
            return 0
        if value > total:
            return total
        return value


_service: Optional[CostAccountingService] = None


def get_cost_accounting_service() -> CostAccountingService:
    global _service
    if _service is None:
        _service = CostAccountingService()
    return _service
