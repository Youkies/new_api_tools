"""
Cost accounting API routes.
"""
import time
from typing import Any, Dict, List, Optional

from fastapi import APIRouter, Depends, Query
from pydantic import BaseModel

from .auth import verify_auth
from .cost_accounting_service import default_cost_range, get_cost_accounting_service
from .database import DatabaseEngine
from .main import InvalidParamsError

router = APIRouter(prefix="/api/cost", tags=["Cost Accounting"])


class CostRuleRequest(BaseModel):
    rules: List[Dict[str, Any]]


class UpstreamSyncConfigRequest(BaseModel):
    enabled: bool = False
    source_name: str = ""
    base_url: str = ""
    endpoint: str = "auto"
    auth_token: str = ""
    auth_token_set: bool = False
    clear_auth_token: bool = False
    user_id: str = ""
    page_size: int = 100
    request_delay_ms: int = 80
    interval_minutes: int = 0
    lookback_minutes: int = 60
    overlap_minutes: int = 10
    match_tolerance_seconds: int = 60
    log_type: int = 2
    max_pages_per_run: int = 1000


class UpstreamSyncRunRequest(BaseModel):
    start_time: int = 0
    end_time: int = 0
    type: int = 2


@router.get("/summary")
def get_cost_summary(
    start_time: Optional[int] = Query(default=None),
    end_time: Optional[int] = Query(default=None),
    channel_id: Optional[int] = Query(default=None),
    _: str = Depends(verify_auth),
):
    default_start, default_end = default_cost_range()
    start = start_time or default_start
    end = end_time or default_end
    if end < start:
        raise InvalidParamsError(message="end_time must be greater than or equal to start_time")

    service = get_cost_accounting_service()
    data = service.get_summary(start, end, channel_id)
    return {"success": True, "data": data}


@router.get("/rules")
def get_cost_rules(_: str = Depends(verify_auth)):
    service = get_cost_accounting_service()
    return {"success": True, "data": service.rules_payload()}


@router.post("/rules")
def save_cost_rules(request: CostRuleRequest, _: str = Depends(verify_auth)):
    service = get_cost_accounting_service()
    rules = service.save_rules(request.rules)
    return {
        "success": True,
        "message": "Cost rules saved",
        "data": {
            "rules": rules,
            "channels": service.list_channels(),
        },
    }


@router.get("/upstream-sync/config")
def get_upstream_sync_config(_: str = Depends(verify_auth)):
    service = get_cost_accounting_service()
    return {"success": True, "data": _get_upstream_sync_config(service)}


@router.post("/upstream-sync/config")
def save_upstream_sync_config(request: UpstreamSyncConfigRequest, _: str = Depends(verify_auth)):
    service = get_cost_accounting_service()
    current = _get_upstream_sync_config(service, include_secret=True)
    data = request.model_dump()
    if not str(data.get("auth_token") or "").strip() and not data.get("clear_auth_token"):
        data["auth_token"] = current.get("auth_token") or ""
    if data.get("clear_auth_token"):
        data["auth_token"] = ""
    data.update({
        "last_sync_at": int(current.get("last_sync_at") or 0),
        "last_success_at": int(current.get("last_success_at") or 0),
        "last_error": str(current.get("last_error") or ""),
        "total_imported": int(current.get("total_imported") or 0),
        "updated_at": int(time.time()),
    })
    saved = _save_upstream_sync_config(service, _normalize_upstream_config(data))
    return {"success": True, "message": "Upstream log sync config saved", "data": saved}


@router.post("/upstream-sync/run")
def run_upstream_sync(_: UpstreamSyncRunRequest, __: str = Depends(verify_auth)):
    return {
        "success": False,
        "error": {
            "message": "Python compatibility backend does not implement upstream log sync yet; use the Go backend for manual and scheduled imports.",
        },
    }


@router.post("/upstream-sync/register")
def register_upstream_sync_config(_: UpstreamSyncConfigRequest, __: str = Depends(verify_auth)):
    return {
        "success": False,
        "error": {
            "message": "Python compatibility backend does not implement upstream log registration yet; use the Go backend for scheduled imports.",
        },
    }


@router.post("/upstream-sync/upload")
def upload_upstream_logs(_: Dict[str, Any], __: str = Depends(verify_auth)):
    return {
        "success": False,
        "error": {
            "message": "Python compatibility backend does not implement upstream log upload yet; use the Go backend for userscript uploads.",
        },
    }


def _default_upstream_config() -> Dict[str, Any]:
    return {
        "enabled": False,
        "source_name": "",
        "base_url": "",
        "endpoint": "auto",
        "auth_token": "",
        "auth_token_set": False,
        "user_id": "",
        "page_size": 100,
        "request_delay_ms": 80,
        "interval_minutes": 0,
        "lookback_minutes": 60,
        "overlap_minutes": 10,
        "match_tolerance_seconds": 60,
        "log_type": 2,
        "max_pages_per_run": 1000,
        "last_sync_at": 0,
        "last_success_at": 0,
        "last_error": "",
        "total_imported": 0,
        "updated_at": 0,
    }


def _ensure_upstream_config_table(service) -> None:
    db = service.db
    if db.config.engine == DatabaseEngine.POSTGRESQL:
        ddl = """
            CREATE TABLE IF NOT EXISTS api_tools_upstream_log_sync_config (
                id INTEGER PRIMARY KEY,
                enabled BOOLEAN NOT NULL DEFAULT FALSE,
                source_name TEXT NOT NULL DEFAULT '',
                base_url TEXT NOT NULL DEFAULT '',
                endpoint TEXT NOT NULL DEFAULT 'auto',
                auth_token TEXT NOT NULL DEFAULT '',
                user_id TEXT NOT NULL DEFAULT '',
                page_size INTEGER NOT NULL DEFAULT 100,
                request_delay_ms INTEGER NOT NULL DEFAULT 80,
                interval_minutes INTEGER NOT NULL DEFAULT 0,
                lookback_minutes INTEGER NOT NULL DEFAULT 60,
                overlap_minutes INTEGER NOT NULL DEFAULT 10,
                match_tolerance_seconds INTEGER NOT NULL DEFAULT 60,
                log_type INTEGER NOT NULL DEFAULT 2,
                max_pages_per_run INTEGER NOT NULL DEFAULT 1000,
                last_sync_at BIGINT NOT NULL DEFAULT 0,
                last_success_at BIGINT NOT NULL DEFAULT 0,
                last_error TEXT NOT NULL DEFAULT '',
                total_imported BIGINT NOT NULL DEFAULT 0,
                updated_at BIGINT NOT NULL DEFAULT 0
            )
        """
    else:
        ddl = """
            CREATE TABLE IF NOT EXISTS api_tools_upstream_log_sync_config (
                id INT PRIMARY KEY,
                enabled TINYINT(1) NOT NULL DEFAULT 0,
                source_name VARCHAR(191) NOT NULL DEFAULT '',
                base_url TEXT NOT NULL,
                endpoint VARCHAR(64) NOT NULL DEFAULT 'auto',
                auth_token TEXT NOT NULL,
                user_id VARCHAR(64) NOT NULL DEFAULT '',
                page_size INT NOT NULL DEFAULT 100,
                request_delay_ms INT NOT NULL DEFAULT 80,
                interval_minutes INT NOT NULL DEFAULT 0,
                lookback_minutes INT NOT NULL DEFAULT 60,
                overlap_minutes INT NOT NULL DEFAULT 10,
                match_tolerance_seconds INT NOT NULL DEFAULT 60,
                log_type INT NOT NULL DEFAULT 2,
                max_pages_per_run INT NOT NULL DEFAULT 1000,
                last_sync_at BIGINT NOT NULL DEFAULT 0,
                last_success_at BIGINT NOT NULL DEFAULT 0,
                last_error TEXT NOT NULL,
                total_imported BIGINT NOT NULL DEFAULT 0,
                updated_at BIGINT NOT NULL DEFAULT 0
            )
        """
    db.execute(ddl)
    if not service._column_exists("api_tools_upstream_log_sync_config", "source_name"):
        if db.config.engine == DatabaseEngine.POSTGRESQL:
            db.execute("ALTER TABLE api_tools_upstream_log_sync_config ADD COLUMN source_name TEXT NOT NULL DEFAULT ''")
        else:
            db.execute("ALTER TABLE api_tools_upstream_log_sync_config ADD COLUMN source_name VARCHAR(191) NOT NULL DEFAULT ''")
    if not service._column_exists("api_tools_upstream_log_sync_config", "match_tolerance_seconds"):
        if db.config.engine == DatabaseEngine.POSTGRESQL:
            db.execute("ALTER TABLE api_tools_upstream_log_sync_config ADD COLUMN match_tolerance_seconds INTEGER NOT NULL DEFAULT 60")
        else:
            db.execute("ALTER TABLE api_tools_upstream_log_sync_config ADD COLUMN match_tolerance_seconds INT NOT NULL DEFAULT 60")


def _get_upstream_sync_config(service, include_secret: bool = False) -> Dict[str, Any]:
    _ensure_upstream_config_table(service)
    rows = service.db.execute("""
        SELECT enabled, source_name, base_url, endpoint, auth_token, user_id, page_size, request_delay_ms,
            interval_minutes, lookback_minutes, overlap_minutes, match_tolerance_seconds, log_type, max_pages_per_run,
            last_sync_at, last_success_at, last_error, total_imported, updated_at
        FROM api_tools_upstream_log_sync_config
        WHERE id = 1
    """)
    config = _default_upstream_config()
    if rows:
        row = rows[0]
        config.update({
            "enabled": bool(row.get("enabled")),
            "source_name": str(row.get("source_name") or ""),
            "base_url": str(row.get("base_url") or ""),
            "endpoint": str(row.get("endpoint") or "auto"),
            "auth_token": str(row.get("auth_token") or ""),
            "user_id": str(row.get("user_id") or ""),
            "page_size": int(row.get("page_size") or 100),
            "request_delay_ms": int(row.get("request_delay_ms") or 80),
            "interval_minutes": int(row.get("interval_minutes") or 0),
            "lookback_minutes": int(row.get("lookback_minutes") or 60),
            "overlap_minutes": int(row.get("overlap_minutes") or 10),
            "match_tolerance_seconds": int(row.get("match_tolerance_seconds") or 60),
            "log_type": int(row.get("log_type") or 2),
            "max_pages_per_run": int(row.get("max_pages_per_run") or 1000),
            "last_sync_at": int(row.get("last_sync_at") or 0),
            "last_success_at": int(row.get("last_success_at") or 0),
            "last_error": str(row.get("last_error") or ""),
            "total_imported": int(row.get("total_imported") or 0),
            "updated_at": int(row.get("updated_at") or 0),
        })
    config = _normalize_upstream_config(config)
    config["auth_token_set"] = bool(str(config.get("auth_token") or "").strip())
    if not include_secret:
        config["auth_token"] = ""
    return config


def _save_upstream_sync_config(service, config: Dict[str, Any]) -> Dict[str, Any]:
    _ensure_upstream_config_table(service)
    db = service.db
    sql = """
        INSERT INTO api_tools_upstream_log_sync_config
            (id, enabled, source_name, base_url, endpoint, auth_token, user_id, page_size, request_delay_ms,
             interval_minutes, lookback_minutes, overlap_minutes, match_tolerance_seconds, log_type, max_pages_per_run,
             last_sync_at, last_success_at, last_error, total_imported, updated_at)
        VALUES
            (:id, :enabled, :source_name, :base_url, :endpoint, :auth_token, :user_id, :page_size, :request_delay_ms,
             :interval_minutes, :lookback_minutes, :overlap_minutes, :match_tolerance_seconds, :log_type, :max_pages_per_run,
             :last_sync_at, :last_success_at, :last_error, :total_imported, :updated_at)
    """
    if db.config.engine == DatabaseEngine.POSTGRESQL:
        sql += """
            ON CONFLICT (id) DO UPDATE SET
                enabled = EXCLUDED.enabled,
                source_name = EXCLUDED.source_name,
                base_url = EXCLUDED.base_url,
                endpoint = EXCLUDED.endpoint,
                auth_token = EXCLUDED.auth_token,
                user_id = EXCLUDED.user_id,
                page_size = EXCLUDED.page_size,
                request_delay_ms = EXCLUDED.request_delay_ms,
                interval_minutes = EXCLUDED.interval_minutes,
                lookback_minutes = EXCLUDED.lookback_minutes,
                overlap_minutes = EXCLUDED.overlap_minutes,
                match_tolerance_seconds = EXCLUDED.match_tolerance_seconds,
                log_type = EXCLUDED.log_type,
                max_pages_per_run = EXCLUDED.max_pages_per_run,
                last_sync_at = EXCLUDED.last_sync_at,
                last_success_at = EXCLUDED.last_success_at,
                last_error = EXCLUDED.last_error,
                total_imported = EXCLUDED.total_imported,
                updated_at = EXCLUDED.updated_at
        """
    else:
        sql += """
            ON DUPLICATE KEY UPDATE
                enabled = VALUES(enabled),
                source_name = VALUES(source_name),
                base_url = VALUES(base_url),
                endpoint = VALUES(endpoint),
                auth_token = VALUES(auth_token),
                user_id = VALUES(user_id),
                page_size = VALUES(page_size),
                request_delay_ms = VALUES(request_delay_ms),
                interval_minutes = VALUES(interval_minutes),
                lookback_minutes = VALUES(lookback_minutes),
                overlap_minutes = VALUES(overlap_minutes),
                match_tolerance_seconds = VALUES(match_tolerance_seconds),
                log_type = VALUES(log_type),
                max_pages_per_run = VALUES(max_pages_per_run),
                last_sync_at = VALUES(last_sync_at),
                last_success_at = VALUES(last_success_at),
                last_error = VALUES(last_error),
                total_imported = VALUES(total_imported),
                updated_at = VALUES(updated_at)
        """
    db.execute(sql, {"id": 1, **config})
    return _get_upstream_sync_config(service)


def _normalize_upstream_config(config: Dict[str, Any]) -> Dict[str, Any]:
    def clamp(value: Any, min_value: int, max_value: int, fallback: int) -> int:
        try:
            parsed = int(value)
        except (TypeError, ValueError):
            parsed = fallback
        if parsed <= 0 and min_value > 0:
            parsed = fallback
        return max(min_value, min(max_value, parsed))

    endpoint = str(config.get("endpoint") or "auto").strip() or "auto"
    if endpoint != "auto" and not endpoint.startswith("/"):
        endpoint = "/" + endpoint
    return {
        **config,
        "source_name": str(config.get("source_name") or "").strip(),
        "base_url": str(config.get("base_url") or "").strip().rstrip("/"),
        "endpoint": endpoint,
        "user_id": str(config.get("user_id") or "").strip(),
        "page_size": clamp(config.get("page_size"), 1, 1000, 100),
        "request_delay_ms": clamp(config.get("request_delay_ms"), 0, 5000, 80),
        "interval_minutes": clamp(config.get("interval_minutes"), 0, 1440, 0),
        "lookback_minutes": clamp(config.get("lookback_minutes"), 1, 525600, 60),
        "overlap_minutes": clamp(config.get("overlap_minutes"), 0, 1440, 10),
        "match_tolerance_seconds": clamp(config.get("match_tolerance_seconds"), 1, 3600, 60),
        "log_type": clamp(config.get("log_type"), 0, 9, 2),
        "max_pages_per_run": clamp(config.get("max_pages_per_run"), 1, 100000, 1000),
    }
