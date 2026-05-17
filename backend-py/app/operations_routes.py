"""
Operations alert API routes.
"""
from fastapi import APIRouter, Depends, Query
from pydantic import BaseModel

from .auth import verify_auth
from .main import InvalidParamsError
from .operations_service import get_operations_service
from .risk_monitoring_service import WINDOW_SECONDS

router = APIRouter(prefix="/api/operations", tags=["Operations"])


class OperationsResponse(BaseModel):
    success: bool
    data: dict


@router.get("/alerts", response_model=OperationsResponse)
def get_operations_alerts(
    window: str = Query(default="30d", description="分析窗口"),
    type: str = Query(default="all", description="预警类型或 all"),
    severity: str = Query(default="all", description="严重等级或 all"),
    limit: int = Query(default=100, ge=1, le=300, description="返回数量"),
    no_cache: bool = Query(default=False, description="强制刷新，跳过缓存"),
    _: str = Depends(verify_auth),
):
    if window not in WINDOW_SECONDS:
        raise InvalidParamsError(message=f"Invalid window: {window}")
    service = get_operations_service()
    data = service.get_alerts(
        window=window,
        alert_type=type,
        severity=severity,
        limit=limit,
        use_cache=not no_cache,
    )
    return OperationsResponse(success=True, data=data)

@router.get("/users/{user_id}/detail", response_model=OperationsResponse)
def get_operations_user_detail(
    user_id: int,
    window: str = Query(default="30d", description="分析窗口"),
    _: str = Depends(verify_auth),
):
    if window not in WINDOW_SECONDS:
        raise InvalidParamsError(message=f"Invalid window: {window}")
    service = get_operations_service()
    try:
        data = service.get_user_detail(user_id=user_id, window=window)
    except ValueError as exc:
        raise InvalidParamsError(message=str(exc))
    return OperationsResponse(success=True, data=data)
