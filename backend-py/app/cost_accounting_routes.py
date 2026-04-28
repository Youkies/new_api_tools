"""
Cost accounting API routes.
"""
from typing import Any, Dict, List, Optional

from fastapi import APIRouter, Depends, Query
from pydantic import BaseModel

from .auth import verify_auth
from .cost_accounting_service import default_cost_range, get_cost_accounting_service
from .main import InvalidParamsError

router = APIRouter(prefix="/api/cost", tags=["Cost Accounting"])


class CostRuleRequest(BaseModel):
    rules: List[Dict[str, Any]]


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
