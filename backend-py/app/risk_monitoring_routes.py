"""
Risk Monitoring API Routes for NewAPI Middleware Tool.
Provides real-time leaderboards and per-user analysis for moderation decisions.
"""
import time
from typing import Any, Optional

from fastapi import APIRouter, Body, Depends, Query
from pydantic import BaseModel

from .auth import verify_auth
from .database import get_db_manager
from .main import InvalidParamsError
from .risk_monitoring_service import WINDOW_SECONDS, get_risk_monitoring_service
from .local_storage import get_local_storage
from .user_management_service import get_user_management_service

router = APIRouter(prefix="/api/risk", tags=["Risk Monitoring"])
_RISK_ACTION_BATCHES: dict[str, dict] = {}


class LeaderboardsResponse(BaseModel):
    success: bool
    data: dict


class UserAnalysisResponse(BaseModel):
    success: bool
    data: dict


class BanRecordsResponse(BaseModel):
    success: bool
    data: dict


class RiskActionBatchResponse(BaseModel):
    success: bool
    data: dict


@router.get("/leaderboards", response_model=LeaderboardsResponse)
def get_leaderboards(
    windows: str = Query(default="1h,3h,6h,12h,24h", description="逗号分隔窗口 (1h/3h/6h/12h/24h)"),
    limit: int = Query(default=10, ge=1, le=50, description="每个榜单返回数量"),
    sort_by: str = Query(default="requests", description="排序维度 (risk_score/requests/quota/failure_rate)"),
    no_cache: bool = Query(default=False, description="强制刷新，跳过缓存"),
    _: str = Depends(verify_auth),
):
    service = get_risk_monitoring_service()
    if sort_by not in ["risk_score", "requests", "quota", "failure_rate"]:
        raise InvalidParamsError(message=f"Invalid sort_by: {sort_by}")
    window_list = [w.strip() for w in windows.split(",") if w.strip()]
    data = service.get_leaderboards(windows=window_list, limit=limit, sort_by=sort_by, use_cache=not no_cache)
    return LeaderboardsResponse(success=True, data=data)


@router.get("/queue", response_model=LeaderboardsResponse)
def get_risk_queue(
    window: str = Query(default="24h", description="分析窗口 (1h/3h/6h/12h/24h/3d/7d)"),
    page: int = Query(default=1, ge=1, description="页码"),
    page_size: int = Query(default=50, ge=1, le=200, description="每页数量"),
    sort_by: str = Query(default="risk_score", description="排序维度 (risk_score/requests/quota/failure_rate)"),
    no_cache: bool = Query(default=False, description="强制刷新，跳过缓存"),
    _: str = Depends(verify_auth),
):
    if window not in WINDOW_SECONDS:
        raise InvalidParamsError(message=f"Invalid window: {window}")
    if sort_by not in ["risk_score", "requests", "quota", "failure_rate"]:
        raise InvalidParamsError(message=f"Invalid sort_by: {sort_by}")
    service = get_risk_monitoring_service()
    data = service.get_risk_queue(
        window=window,
        page=page,
        page_size=page_size,
        sort_by=sort_by,
        use_cache=not no_cache,
    )
    return LeaderboardsResponse(success=True, data=data)


@router.post("/actions/batches", response_model=RiskActionBatchResponse)
def execute_risk_action_batch(
    request: dict[str, Any] = Body(...),
    _: str = Depends(verify_auth),
):
    """Execute or preview a batch risk action."""
    action = str(request.get("action") or "ban")
    if action not in ["ban", "unban"]:
        raise InvalidParamsError(message=f"Invalid action: {action}")

    dry_run = bool(request.get("dry_run", True))
    reason = str(request.get("reason") or "")
    source = str(request.get("source") or "risk_center")
    disable_tokens = bool(request.get("disable_tokens", True))
    enable_tokens = bool(request.get("enable_tokens", False))
    exclude_protected = bool(request.get("exclude_protected_roles", True))
    condition = request.get("condition") if isinstance(request.get("condition"), dict) else {}
    user_ids = request.get("user_ids") if isinstance(request.get("user_ids"), list) else []

    users, skipped = _resolve_risk_batch_targets(user_ids, condition, exclude_protected)
    batch_id = f"risk-{action}-{int(time.time())}"
    if dry_run:
        return RiskActionBatchResponse(success=True, data={
            "batch_id": batch_id,
            "dry_run": True,
            "action": action,
            "reason": reason,
            "source": source,
            "affected_count": len(users),
            "skipped_count": len(skipped),
            "users": users,
            "skipped": skipped,
            "message": "预览完成，未修改用户状态",
        })

    service = get_user_management_service()
    succeeded = []
    failed = []
    for user in users:
        user_id = int(user.get("user_id") or 0)
        context = {
            "batch_id": batch_id,
            "source": source,
            "condition": condition,
        }
        if action == "ban":
            result = service.ban_user(user_id, reason=reason, disable_tokens=disable_tokens, operator="risk_batch", context=context)
        else:
            result = service.unban_user(user_id, reason=reason, enable_tokens=enable_tokens, operator="risk_batch", context=context)
        if result.get("success"):
            succeeded.append(user)
        else:
            failed.append({"user_id": user_id, "username": user.get("username"), "reason": result.get("message", "failed")})

    batch = {
        "batch_id": batch_id,
        "dry_run": False,
        "action": action,
        "reason": reason,
        "source": source,
        "condition": condition,
        "affected_count": len(succeeded),
        "failed_count": len(failed),
        "skipped_count": len(skipped),
        "users": succeeded,
        "failed": failed,
        "skipped": skipped,
        "created_at": int(time.time()),
        "reverted_at": None,
    }
    _RISK_ACTION_BATCHES[batch_id] = batch
    return RiskActionBatchResponse(success=True, data=batch)


@router.post("/actions/batches/{batch_id}/revert", response_model=RiskActionBatchResponse)
def revert_risk_action_batch(
    batch_id: str,
    _: str = Depends(verify_auth),
):
    """Revert a previously executed in-process batch action."""
    batch = _RISK_ACTION_BATCHES.get(batch_id)
    if not batch:
        raise InvalidParamsError(message="Batch not found or expired")
    if batch.get("reverted_at"):
        return RiskActionBatchResponse(success=True, data=batch)
    if batch.get("action") != "ban":
        raise InvalidParamsError(message="Only ban batches can be reverted")

    service = get_user_management_service()
    reverted = []
    failed = []
    for user in batch.get("users", []):
        user_id = int(user.get("user_id") or 0)
        result = service.unban_user(
            user_id,
            reason=f"撤销批量封禁 {batch_id}",
            enable_tokens=False,
            operator="risk_batch_revert",
            context={"reverted_batch_id": batch_id},
        )
        if result.get("success"):
            reverted.append(user)
        else:
            failed.append({"user_id": user_id, "username": user.get("username"), "reason": result.get("message", "failed")})

    batch["reverted_at"] = int(time.time())
    batch["reverted_users"] = reverted
    batch["revert_failed"] = failed
    return RiskActionBatchResponse(success=True, data=batch)


def _resolve_risk_batch_targets(user_ids: list[Any], condition: dict[str, Any], exclude_protected: bool) -> tuple[list[dict], list[dict]]:
    """Resolve target users for Python risk batch actions."""
    db = get_db_manager()
    db.connect()
    users: list[dict] = []
    skipped: list[dict] = []

    if user_ids:
        ids = [int(uid) for uid in user_ids if str(uid).isdigit()]
        if not ids:
            return users, skipped
        placeholders = ", ".join([f":id{i}" for i in range(len(ids))])
        params = {f"id{i}": uid for i, uid in enumerate(ids)}
        rows = db.execute(f"""
            SELECT id, username, display_name, role, status
            FROM users
            WHERE deleted_at IS NULL AND id IN ({placeholders})
        """, params)
    elif condition.get("type") == "shared_ip" and condition.get("ip"):
        window = str(condition.get("window") or "24h")
        seconds = WINDOW_SECONDS.get(window, WINDOW_SECONDS["24h"])
        rows = db.execute("""
            SELECT DISTINCT u.id, u.username, u.display_name, u.role, u.status
            FROM users u
            INNER JOIN logs l ON l.user_id = u.id
            WHERE u.deleted_at IS NULL
              AND l.ip = :ip
              AND l.created_at >= :start_time
        """, {"ip": condition["ip"], "start_time": int(time.time()) - seconds})
    else:
        rows = []

    for row in rows:
        user = {
            "user_id": int(row.get("id") or 0),
            "username": row.get("display_name") or row.get("username") or f"User#{row.get('id')}",
            "display_name": row.get("display_name") or row.get("username") or f"User#{row.get('id')}",
            "role": int(row.get("role") or 0),
            "status": int(row.get("status") or 0),
        }
        if exclude_protected and user["role"] >= 10:
            skipped.append({**user, "reason": "protected_role"})
            continue
        users.append(user)

    return users, skipped


@router.get("/users/{user_id}/analysis", response_model=UserAnalysisResponse)
def get_user_analysis(
    user_id: int,
    window: str = Query(default="24h", description="分析窗口 (1h/3h/6h/12h/24h)"),
    end_time: Optional[int] = Query(default=None, description="结束时间点(Unix时间戳)，用于查看历史数据如封禁时刻"),
    _: str = Depends(verify_auth),
):
    seconds = WINDOW_SECONDS.get(window)
    if not seconds:
        raise InvalidParamsError(message=f"Invalid window: {window}")

    service = get_risk_monitoring_service()
    # 如果指定了 end_time，则以该时间为基准查询历史数据
    data = service.get_user_analysis(user_id=user_id, window_seconds=seconds, now=end_time)
    return UserAnalysisResponse(success=True, data=data)


@router.get("/ban-records", response_model=BanRecordsResponse)
def list_ban_records(
    page: int = Query(default=1, ge=1, description="页码"),
    page_size: int = Query(default=50, ge=1, le=200, description="每页数量"),
    action: Optional[str] = Query(default=None, description="过滤动作 (ban/unban)"),
    user_id: Optional[int] = Query(default=None, description="过滤用户ID"),
    _: str = Depends(verify_auth),
):
    storage = get_local_storage()
    if action is not None and action not in ["ban", "unban"]:
        raise InvalidParamsError(message=f"Invalid action: {action}")
    data = storage.list_security_audits(page=page, page_size=page_size, action=action, user_id=user_id)
    return BanRecordsResponse(success=True, data=data)


class TokenRotationResponse(BaseModel):
    success: bool
    data: dict


class AffiliatedAccountsResponse(BaseModel):
    success: bool
    data: dict


class SameIPRegistrationsResponse(BaseModel):
    success: bool
    data: dict


@router.get("/token-rotation", response_model=TokenRotationResponse)
def get_token_rotation_users(
    window: str = Query(default="24h", description="时间窗口 (1h/3h/6h/12h/24h/3d/7d)"),
    min_tokens: int = Query(default=5, ge=2, le=50, description="最小 Token 数量阈值"),
    max_requests_per_token: int = Query(default=10, ge=1, le=100, description="每个 Token 最大平均请求数"),
    limit: int = Query(default=50, ge=1, le=200, description="返回数量"),
    no_cache: bool = Query(default=False, description="强制刷新，跳过缓存"),
    _: str = Depends(verify_auth),
):
    """
    检测 Token 轮换行为。
    
    返回同一用户短时间内使用多个 Token，且每个 Token 请求较少的用户列表。
    这种行为可能表示用户在规避限制或多人共享账号。
    """
    seconds = WINDOW_SECONDS.get(window)
    if not seconds:
        raise InvalidParamsError(message=f"Invalid window: {window}")

    service = get_risk_monitoring_service()
    data = service.get_token_rotation_users(
        window_seconds=seconds,
        min_tokens=min_tokens,
        max_requests_per_token=max_requests_per_token,
        limit=limit,
        use_cache=not no_cache,
    )
    return TokenRotationResponse(success=True, data=data)


@router.get("/affiliated-accounts", response_model=AffiliatedAccountsResponse)
def get_affiliated_accounts(
    min_invited: int = Query(default=3, ge=2, le=50, description="最小被邀请账号数量"),
    include_activity: bool = Query(default=True, description="是否包含账号活跃度信息"),
    limit: int = Query(default=50, ge=1, le=200, description="返回数量"),
    no_cache: bool = Query(default=False, description="强制刷新，跳过缓存"),
    _: str = Depends(verify_auth),
):
    """
    检测关联账号 - 同一邀请人下的多个账号。
    
    返回有多个被邀请账号的邀请人列表，包含被邀请账号的详细信息和活跃度。
    这种情况可能表示同一人注册多个账号或有组织的批量注册。
    """
    service = get_risk_monitoring_service()
    data = service.get_affiliated_accounts(
        min_invited=min_invited,
        include_activity=include_activity,
        limit=limit,
        use_cache=not no_cache,
    )
    return AffiliatedAccountsResponse(success=True, data=data)


@router.get("/same-ip-registrations", response_model=SameIPRegistrationsResponse)
def get_same_ip_registrations(
    window: str = Query(default="7d", description="时间窗口 (1h/3h/6h/12h/24h/3d/7d)"),
    min_users: int = Query(default=3, ge=2, le=50, description="最小用户数量"),
    limit: int = Query(default=50, ge=1, le=200, description="返回数量"),
    no_cache: bool = Query(default=False, description="强制刷新，跳过缓存"),
    _: str = Depends(verify_auth),
):
    """
    检测同 IP 注册的多个账号。
    
    通过分析用户首次请求的 IP 地址，找出从同一 IP 注册的多个账号。
    这种情况可能表示批量注册或同一人使用多个账号。
    """
    seconds = WINDOW_SECONDS.get(window)
    if not seconds:
        raise InvalidParamsError(message=f"Invalid window: {window}")

    service = get_risk_monitoring_service()
    data = service.get_same_ip_registrations(
        window_seconds=seconds,
        min_users=min_users,
        limit=limit,
        use_cache=not no_cache,
    )
    return SameIPRegistrationsResponse(success=True, data=data)
