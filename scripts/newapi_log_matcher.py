#!/usr/bin/env python3
"""Local NewAPI CSV log matcher and cost analyzer.

This script intentionally uses only the Python standard library so it can run
on a plain Windows host after CSV files are exported by the userscript.
"""

from __future__ import annotations

import argparse
import csv
import json
import re
from dataclasses import dataclass
from datetime import datetime
from decimal import Decimal, InvalidOperation, ROUND_HALF_UP
from pathlib import Path
from typing import Any


DEFAULT_CONFIG: dict[str, Any] = {
    "local_host": "newapi.youkies.space",
    "quota_per_unit": 500000,
    "time_window_seconds": 120,
    "upstreams": {
        "us.llmgate.io": {"aliases": ["llmgate"], "cost_multiplier": 1},
        "www.omnai.xyz": {
            "aliases": ["ominiai", "omnai", "omni"],
            "cost_multiplier": 1,
        },
        "api.opusclaw.me": {"aliases": ["opus", "opusclaw"], "cost_multiplier": 0.1},
        "api.guicore.com": {
            "aliases": ["guicore", "cpa", "gui"],
            "cost_multiplier": 1,
        },
    },
    "model_suffix_patterns": [
        r"-\u6e20\u9053\d+$",
        r"-nothinking$",
        r"-backup$",
        r"-standby$",
        r"-\u5907\u7528$",
        r"-windsurf$",
        r"-kiro$",
        r"-cli$",
    ],
}


HEADER = [
    "time",
    "username",
    "token_name",
    "group",
    "model_name",
    "channel_id",
    "channel_name",
    "type",
    "prompt_tokens",
    "completion_tokens",
    "total_tokens",
    "quota",
    "cost_usd",
    "use_time",
    "is_stream",
    "model_ratio",
    "group_ratio",
    "completion_ratio",
    "request_id",
    "content",
]

CSV_DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}\s")
EXPORT_NAME_RE = re.compile(r"^newapi_logs_(?P<host>.+?)_\d{8}_\d{6}\.csv$", re.I)
MODE_PREFIX_RE = re.compile(r"\u300c[^\u300d]*\u300d")
PER_CALL_MARKER = "\u6309\u6b21"


@dataclass(slots=True)
class LogRow:
    source_host: str
    source_file: str
    row_number: int
    raw: list[str]
    created_at: datetime
    username: str
    token_name: str
    group: str
    model_name: str
    channel_id: str
    channel_name: str
    prompt_tokens: int
    completion_tokens: int
    total_tokens: int
    quota: Decimal
    cost_usd: Decimal
    request_id: str
    normalized_model: str
    target_host: str = ""
    target_reason: str = ""

    @property
    def token_key(self) -> tuple[int, int, int]:
        return (self.prompt_tokens, self.completion_tokens, self.total_tokens)


@dataclass(slots=True)
class MatchResult:
    status: str
    local: LogRow
    upstream: LogRow | None
    reason: str
    candidate_count: int
    time_diff_seconds: int | None = None


def parse_decimal(value: Any) -> Decimal:
    try:
        text = str(value or "").strip().replace(",", "")
        if not text:
            return Decimal("0")
        return Decimal(text)
    except (InvalidOperation, ValueError):
        return Decimal("0")


def parse_int(value: Any) -> int:
    try:
        text = str(value or "").strip().replace(",", "")
        if not text:
            return 0
        return int(Decimal(text))
    except (InvalidOperation, ValueError):
        return 0


def money(value: Decimal | int | float, places: int = 6) -> str:
    quant = Decimal("1").scaleb(-places)
    return str(Decimal(value).quantize(quant, rounding=ROUND_HALF_UP))


def load_config(path: Path | None) -> dict[str, Any]:
    config = json.loads(json.dumps(DEFAULT_CONFIG))
    if not path:
        return config
    with path.open("r", encoding="utf-8") as handle:
        user_config = json.load(handle)
    deep_update(config, user_config)
    return config


def deep_update(target: dict[str, Any], source: dict[str, Any]) -> None:
    for key, value in source.items():
        if isinstance(value, dict) and isinstance(target.get(key), dict):
            deep_update(target[key], value)
        else:
            target[key] = value


def detect_host(path: Path) -> str:
    match = EXPORT_NAME_RE.match(path.name)
    if match:
        return match.group("host")
    return path.stem


def normalize_model(value: str, suffix_patterns: list[str]) -> str:
    text = MODE_PREFIX_RE.sub("", value or "").strip().lower()
    text = text.replace("_", "-").replace(" ", "")
    text = text.strip("-_/ ")
    changed = True
    while changed:
        changed = False
        for pattern in suffix_patterns:
            new_text = re.sub(pattern, "", text, flags=re.I).strip("-_/ ")
            if new_text != text:
                text = new_text
                changed = True
    return text


def models_compatible(local_model: str, upstream_model: str) -> bool:
    if not local_model or not upstream_model:
        return True
    if local_model == upstream_model:
        return True
    return local_model.startswith(upstream_model + "-") or upstream_model.startswith(
        local_model + "-"
    )


def parse_datetime(value: str) -> datetime | None:
    try:
        return datetime.strptime(value.strip(), "%Y-%m-%d %H:%M:%S")
    except ValueError:
        return None


def read_logs(log_dir: Path, config: dict[str, Any]) -> dict[str, list[LogRow]]:
    suffix_patterns = list(config.get("model_suffix_patterns") or [])
    rows_by_host: dict[str, list[LogRow]] = {}

    for path in sorted(log_dir.glob("*.csv")):
        host = detect_host(path)
        rows: list[LogRow] = []
        with path.open("r", encoding="utf-8-sig", newline="") as handle:
            reader = csv.reader(handle)
            next(reader, None)
            for row_number, raw in enumerate(reader, start=2):
                if not is_log_row(raw):
                    continue
                created_at = parse_datetime(raw[0])
                if not created_at:
                    continue
                row = pad_row(raw)
                rows.append(
                    LogRow(
                        source_host=host,
                        source_file=path.name,
                        row_number=row_number,
                        raw=row,
                        created_at=created_at,
                        username=row[1],
                        token_name=row[2],
                        group=row[3],
                        model_name=row[4],
                        channel_id=row[5],
                        channel_name=row[6],
                        prompt_tokens=parse_int(row[8]),
                        completion_tokens=parse_int(row[9]),
                        total_tokens=parse_int(row[10]),
                        quota=parse_decimal(row[11]),
                        cost_usd=parse_decimal(row[12]),
                        request_id=row[18],
                        normalized_model=normalize_model(row[4], suffix_patterns),
                    )
                )
        rows_by_host[host] = rows

    return rows_by_host


def is_log_row(row: list[str]) -> bool:
    return len(row) > 12 and bool(CSV_DATE_RE.match(row[0] or "")) and parse_decimal(row[12]) != 0


def pad_row(row: list[str]) -> list[str]:
    if len(row) >= len(HEADER):
        return row
    return row + [""] * (len(HEADER) - len(row))


def upstream_multiplier(config: dict[str, Any], host: str) -> Decimal:
    value = (config.get("upstreams", {}).get(host, {}) or {}).get("cost_multiplier", 1)
    return parse_decimal(value) or Decimal("1")


def real_cost(row: LogRow, config: dict[str, Any]) -> Decimal:
    return row.cost_usd * upstream_multiplier(config, row.source_host)


def assign_target_hosts(rows: list[LogRow], config: dict[str, Any]) -> None:
    upstreams = config.get("upstreams", {}) or {}
    alias_items: list[tuple[str, str]] = []
    for host, info in upstreams.items():
        for alias in info.get("aliases", []) or []:
            alias_items.append((str(alias).lower(), host))
    alias_items.sort(key=lambda item: len(item[0]), reverse=True)

    for row in rows:
        haystack = row.channel_name.lower()
        for alias, host in alias_items:
            if alias and alias in haystack:
                row.target_host = host
                row.target_reason = f"alias:{alias}"
                break


def build_token_index(rows_by_host: dict[str, list[LogRow]]) -> dict[str, dict[tuple[int, int, int], list[LogRow]]]:
    index: dict[str, dict[tuple[int, int, int], list[LogRow]]] = {}
    for host, rows in rows_by_host.items():
        host_index: dict[tuple[int, int, int], list[LogRow]] = {}
        for row in rows:
            host_index.setdefault(row.token_key, []).append(row)
        index[host] = host_index
    return index


def match_rows(
    local_rows: list[LogRow],
    upstream_rows_by_host: dict[str, list[LogRow]],
    config: dict[str, Any],
) -> tuple[list[MatchResult], set[tuple[str, int]]]:
    window_seconds = int(config.get("time_window_seconds") or 120)
    token_index = build_token_index(upstream_rows_by_host)
    used_upstream: set[tuple[str, int]] = set()
    results: list[MatchResult] = []

    for local in sorted(local_rows, key=lambda row: row.created_at):
        if not local.target_host:
            results.append(
                MatchResult(
                    status="unmatched",
                    local=local,
                    upstream=None,
                    reason="no_upstream_alias",
                    candidate_count=0,
                )
            )
            continue

        candidates = [
            row
            for row in token_index.get(local.target_host, {}).get(local.token_key, [])
            if (row.source_host, row.row_number) not in used_upstream
            and abs(int((row.created_at - local.created_at).total_seconds())) <= window_seconds
        ]

        model_candidates = [
            row
            for row in candidates
            if models_compatible(local.normalized_model, row.normalized_model)
        ]
        if model_candidates:
            candidates = model_candidates

        if not candidates:
            results.append(
                MatchResult(
                    status="unmatched",
                    local=local,
                    upstream=None,
                    reason="no_token_time_model_candidate",
                    candidate_count=0,
                )
            )
            continue

        ranked = sorted(
            candidates,
            key=lambda row: (
                abs(int((row.created_at - local.created_at).total_seconds())),
                0 if local.normalized_model == row.normalized_model else 1,
                row.row_number,
            ),
        )

        if len(ranked) == 1 or unique_best(local, ranked):
            upstream = ranked[0]
            used_upstream.add((upstream.source_host, upstream.row_number))
            results.append(
                MatchResult(
                    status="matched",
                    local=local,
                    upstream=upstream,
                    reason="token_time_model",
                    candidate_count=len(candidates),
                    time_diff_seconds=abs(
                        int((upstream.created_at - local.created_at).total_seconds())
                    ),
                )
            )
        else:
            results.append(
                MatchResult(
                    status="ambiguous",
                    local=local,
                    upstream=None,
                    reason="multiple_candidates",
                    candidate_count=len(candidates),
                )
            )

    return results, used_upstream


def unique_best(local: LogRow, ranked: list[LogRow]) -> bool:
    if len(ranked) < 2:
        return True
    best = ranked[0]
    second = ranked[1]
    best_score = (
        abs(int((best.created_at - local.created_at).total_seconds())),
        0 if local.normalized_model == best.normalized_model else 1,
    )
    second_score = (
        abs(int((second.created_at - local.created_at).total_seconds())),
        0 if local.normalized_model == second.normalized_model else 1,
    )
    return best_score < second_score


def summarize(
    rows_by_host: dict[str, list[LogRow]],
    results: list[MatchResult],
    used_upstream: set[tuple[str, int]],
    config: dict[str, Any],
) -> dict[str, Any]:
    local_host = config["local_host"]
    local_rows = rows_by_host.get(local_host, [])
    upstream_hosts = [host for host in rows_by_host if host != local_host]
    matched = [result for result in results if result.status == "matched" and result.upstream]
    ambiguous = [result for result in results if result.status == "ambiguous"]
    unmatched = [result for result in results if result.status == "unmatched"]

    local_total = sum((row.cost_usd for row in local_rows), Decimal("0"))
    upstream_total = sum(
        (real_cost(row, config) for host in upstream_hosts for row in rows_by_host[host]),
        Decimal("0"),
    )
    matched_local_total = sum((item.local.cost_usd for item in matched), Decimal("0"))
    matched_upstream_total = sum((real_cost(item.upstream, config) for item in matched if item.upstream), Decimal("0"))

    by_host: dict[str, dict[str, Any]] = {}
    for host in config.get("upstreams", {}) or {}:
        local_mapped = [row for row in local_rows if row.target_host == host]
        host_matches = [item for item in matched if item.local.target_host == host]
        host_ambiguous = [item for item in ambiguous if item.local.target_host == host]
        host_unmatched = [item for item in unmatched if item.local.target_host == host]
        upstream_rows = rows_by_host.get(host, [])
        host_local_total = sum((row.cost_usd for row in local_mapped), Decimal("0"))
        host_upstream_total = sum((real_cost(row, config) for row in upstream_rows), Decimal("0"))
        host_matched_upstream = sum(
            (real_cost(item.upstream, config) for item in host_matches if item.upstream),
            Decimal("0"),
        )
        host_matched_local = sum((item.local.cost_usd for item in host_matches), Decimal("0"))
        per_call_rows = [row for row in local_mapped if is_per_call(row)]
        non_per_call_revenue = sum(
            (row.cost_usd for row in local_mapped if not is_per_call(row)),
            Decimal("0"),
        )
        current_per_call_total = sum((row.cost_usd for row in per_call_rows), Decimal("0"))
        break_even_price = Decimal("0")
        if per_call_rows:
            break_even_price = (host_upstream_total - non_per_call_revenue) / len(per_call_rows)

        by_host[host] = {
            "local_rows": len(local_mapped),
            "upstream_rows": len(upstream_rows),
            "matched_rows": len(host_matches),
            "ambiguous_rows": len(host_ambiguous),
            "unmatched_rows": len(host_unmatched),
            "local_revenue": host_local_total,
            "upstream_cost": host_upstream_total,
            "gross": host_local_total - host_upstream_total,
            "matched_local_revenue": host_matched_local,
            "matched_upstream_cost": host_matched_upstream,
            "matched_gross": host_matched_local - host_matched_upstream,
            "per_call_rows": len(per_call_rows),
            "per_call_current_avg": (
                current_per_call_total / len(per_call_rows) if per_call_rows else Decimal("0")
            ),
            "per_call_break_even_price": break_even_price,
        }

    return {
        "local_host": local_host,
        "local_rows": len(local_rows),
        "upstream_rows": sum(len(rows_by_host[host]) for host in upstream_hosts),
        "matched_rows": len(matched),
        "ambiguous_rows": len(ambiguous),
        "unmatched_rows": len(unmatched),
        "local_revenue": local_total,
        "upstream_cost": upstream_total,
        "gross": local_total - upstream_total,
        "matched_local_revenue": matched_local_total,
        "matched_upstream_cost": matched_upstream_total,
        "matched_gross": matched_local_total - matched_upstream_total,
        "by_host": by_host,
        "unused_upstream_rows": sum(
            1
            for host in upstream_hosts
            for row in rows_by_host[host]
            if (row.source_host, row.row_number) not in used_upstream
        ),
    }


def is_per_call(row: LogRow) -> bool:
    return any(
        PER_CALL_MARKER in value
        for value in (row.model_name, row.group, row.channel_name, row.token_name)
    )


def write_reports(
    report_dir: Path,
    rows_by_host: dict[str, list[LogRow]],
    results: list[MatchResult],
    used_upstream: set[tuple[str, int]],
    summary: dict[str, Any],
    config: dict[str, Any],
) -> None:
    report_dir.mkdir(parents=True, exist_ok=True)
    write_summary(report_dir / "summary.md", summary, config)
    write_match_csv(report_dir / "matches.csv", results, config)
    write_local_status_csv(report_dir / "local_status.csv", results, config)
    write_unmatched_upstream_csv(report_dir / "unmatched_upstream.csv", rows_by_host, used_upstream, config)
    write_by_host_csv(report_dir / "by_upstream.csv", summary)
    write_report_html(report_dir / "report.html", results, summary, config)


def write_summary(path: Path, summary: dict[str, Any], config: dict[str, Any]) -> None:
    lines = [
        "# NewAPI local log match report",
        "",
        f"- Local host: `{summary['local_host']}`",
        f"- Local rows: {summary['local_rows']}",
        f"- Upstream rows: {summary['upstream_rows']}",
        f"- Matched rows: {summary['matched_rows']}",
        f"- Ambiguous rows: {summary['ambiguous_rows']}",
        f"- Unmatched local rows: {summary['unmatched_rows']}",
        f"- Unused upstream rows: {summary['unused_upstream_rows']}",
        f"- Local revenue: ${money(summary['local_revenue'])}",
        f"- Upstream cost: ${money(summary['upstream_cost'])}",
        f"- Gross: ${money(summary['gross'])}",
        "",
        "## By Upstream",
        "",
        "| upstream | local rows | upstream rows | matched | unmatched | local revenue | upstream cost | gross | per-call avg | per-call break-even |",
        "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for host, item in summary["by_host"].items():
        lines.append(
            "| "
            + " | ".join(
                [
                    host,
                    str(item["local_rows"]),
                    str(item["upstream_rows"]),
                    str(item["matched_rows"]),
                    str(item["unmatched_rows"]),
                    "$" + money(item["local_revenue"]),
                    "$" + money(item["upstream_cost"]),
                    "$" + money(item["gross"]),
                    "$" + money(item["per_call_current_avg"]),
                    "$" + money(item["per_call_break_even_price"]),
                ]
            )
            + " |"
        )
    lines.extend(
        [
            "",
            "## Notes",
            "",
            f"- Time window: {config.get('time_window_seconds', 120)} seconds.",
            "- Matching key: mapped upstream alias + exact prompt/completion/total tokens + time window + normalized model.",
            "- `ambiguous` rows are intentionally not forced into a match.",
        ]
    )
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def write_match_csv(path: Path, results: list[MatchResult], config: dict[str, Any]) -> None:
    fields = [
        "status",
        "target_host",
        "time_diff_seconds",
        "local_time",
        "upstream_time",
        "local_request_id",
        "upstream_request_id",
        "local_model",
        "upstream_model",
        "prompt_tokens",
        "completion_tokens",
        "total_tokens",
        "local_revenue",
        "upstream_cost",
        "gross",
        "local_channel",
        "local_group",
        "local_username",
        "reason",
        "candidate_count",
    ]
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for result in results:
            if result.status != "matched" or not result.upstream:
                continue
            upstream_cost = real_cost(result.upstream, config)
            writer.writerow(
                {
                    "status": result.status,
                    "target_host": result.local.target_host,
                    "time_diff_seconds": result.time_diff_seconds,
                    "local_time": fmt_time(result.local.created_at),
                    "upstream_time": fmt_time(result.upstream.created_at),
                    "local_request_id": result.local.request_id,
                    "upstream_request_id": result.upstream.request_id,
                    "local_model": result.local.model_name,
                    "upstream_model": result.upstream.model_name,
                    "prompt_tokens": result.local.prompt_tokens,
                    "completion_tokens": result.local.completion_tokens,
                    "total_tokens": result.local.total_tokens,
                    "local_revenue": money(result.local.cost_usd),
                    "upstream_cost": money(upstream_cost),
                    "gross": money(result.local.cost_usd - upstream_cost),
                    "local_channel": result.local.channel_name,
                    "local_group": result.local.group,
                    "local_username": result.local.username,
                    "reason": result.reason,
                    "candidate_count": result.candidate_count,
                }
            )


def write_local_status_csv(path: Path, results: list[MatchResult], config: dict[str, Any]) -> None:
    fields = [
        "status",
        "reason",
        "candidate_count",
        "target_host",
        "local_time",
        "local_request_id",
        "local_model",
        "prompt_tokens",
        "completion_tokens",
        "total_tokens",
        "local_revenue",
        "local_channel",
        "local_group",
        "local_username",
    ]
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for result in results:
            writer.writerow(
                {
                    "status": result.status,
                    "reason": result.reason,
                    "candidate_count": result.candidate_count,
                    "target_host": result.local.target_host,
                    "local_time": fmt_time(result.local.created_at),
                    "local_request_id": result.local.request_id,
                    "local_model": result.local.model_name,
                    "prompt_tokens": result.local.prompt_tokens,
                    "completion_tokens": result.local.completion_tokens,
                    "total_tokens": result.local.total_tokens,
                    "local_revenue": money(result.local.cost_usd),
                    "local_channel": result.local.channel_name,
                    "local_group": result.local.group,
                    "local_username": result.local.username,
                }
            )


def write_unmatched_upstream_csv(
    path: Path,
    rows_by_host: dict[str, list[LogRow]],
    used_upstream: set[tuple[str, int]],
    config: dict[str, Any],
) -> None:
    local_host = config["local_host"]
    fields = [
        "source_host",
        "time",
        "request_id",
        "model",
        "prompt_tokens",
        "completion_tokens",
        "total_tokens",
        "upstream_cost",
        "username",
        "token_name",
        "group",
    ]
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for host, rows in rows_by_host.items():
            if host == local_host:
                continue
            for row in rows:
                if (row.source_host, row.row_number) in used_upstream:
                    continue
                writer.writerow(
                    {
                        "source_host": row.source_host,
                        "time": fmt_time(row.created_at),
                        "request_id": row.request_id,
                        "model": row.model_name,
                        "prompt_tokens": row.prompt_tokens,
                        "completion_tokens": row.completion_tokens,
                        "total_tokens": row.total_tokens,
                        "upstream_cost": money(real_cost(row, config)),
                        "username": row.username,
                        "token_name": row.token_name,
                        "group": row.group,
                    }
                )


def write_by_host_csv(path: Path, summary: dict[str, Any]) -> None:
    fields = [
        "upstream",
        "local_rows",
        "upstream_rows",
        "matched_rows",
        "ambiguous_rows",
        "unmatched_rows",
        "local_revenue",
        "upstream_cost",
        "gross",
        "matched_local_revenue",
        "matched_upstream_cost",
        "matched_gross",
        "per_call_rows",
        "per_call_current_avg",
        "per_call_break_even_price",
    ]
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for host, item in summary["by_host"].items():
            writer.writerow(
                {
                    key: host
                    if key == "upstream"
                    else (
                        money(item[key])
                        if isinstance(item[key], Decimal)
                        else item[key]
                    )
                    for key in fields
                }
            )


def write_report_html(
    path: Path,
    results: list[MatchResult],
    summary: dict[str, Any],
    config: dict[str, Any],
) -> None:
    records = [match_result_to_record(result, config) for result in results]
    payload = {
        "summary": summary_to_json(summary),
        "records": records,
        "generatedAt": datetime.now().strftime("%Y-%m-%d %H:%M:%S"),
        "timeWindowSeconds": int(config.get("time_window_seconds") or 120),
    }
    json_payload = json.dumps(payload, ensure_ascii=False).replace("</", "<\\/")
    path.write_text(report_html_template(json_payload), encoding="utf-8")


def summary_to_json(summary: dict[str, Any]) -> dict[str, Any]:
    converted: dict[str, Any] = {}
    for key, value in summary.items():
        if key == "by_host":
            converted[key] = {
                host: {
                    child_key: decimal_to_json(child_value)
                    for child_key, child_value in item.items()
                }
                for host, item in value.items()
            }
        else:
            converted[key] = decimal_to_json(value)
    return converted


def decimal_to_json(value: Any) -> Any:
    if isinstance(value, Decimal):
        return float(value)
    return value


def match_result_to_record(result: MatchResult, config: dict[str, Any]) -> dict[str, Any]:
    upstream_cost = Decimal("0")
    upstream_time = ""
    upstream_request_id = ""
    upstream_model = ""
    upstream_host = ""
    gross: Decimal | None = None
    if result.upstream:
        upstream_cost = real_cost(result.upstream, config)
        upstream_time = fmt_time(result.upstream.created_at)
        upstream_request_id = result.upstream.request_id
        upstream_model = result.upstream.model_name
        upstream_host = result.upstream.source_host
        gross = result.local.cost_usd - upstream_cost

    return {
        "status": result.status,
        "reason": result.reason,
        "candidateCount": result.candidate_count,
        "timeDiffSeconds": result.time_diff_seconds,
        "targetHost": result.local.target_host,
        "upstreamHost": upstream_host,
        "localTime": fmt_time(result.local.created_at),
        "upstreamTime": upstream_time,
        "localRequestId": result.local.request_id,
        "upstreamRequestId": upstream_request_id,
        "localModel": result.local.model_name,
        "normalizedModel": result.local.normalized_model,
        "upstreamModel": upstream_model,
        "localChannel": result.local.channel_name,
        "localGroup": result.local.group,
        "localUsername": result.local.username,
        "localTokenName": result.local.token_name,
        "promptTokens": result.local.prompt_tokens,
        "completionTokens": result.local.completion_tokens,
        "totalTokens": result.local.total_tokens,
        "localRevenue": float(result.local.cost_usd),
        "upstreamCost": float(upstream_cost),
        "gross": float(gross) if gross is not None else None,
        "isPerCall": is_per_call(result.local),
    }


def report_html_template(json_payload: str) -> str:
    template = """<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>NewAPI 日志匹配核算</title>
  <style>
    :root {{
      color-scheme: light;
      --bg: #f7f8fb;
      --panel: #ffffff;
      --line: #dfe3eb;
      --text: #172033;
      --muted: #667085;
      --accent: #2563eb;
      --good: #047857;
      --warn: #b45309;
      --bad: #b91c1c;
      --shadow: 0 12px 30px rgba(18, 25, 38, 0.08);
    }}
    * {{ box-sizing: border-box; }}
    body {{
      margin: 0;
      background: var(--bg);
      color: var(--text);
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans SC", sans-serif;
    }}
    header {{
      position: sticky;
      top: 0;
      z-index: 10;
      border-bottom: 1px solid var(--line);
      background: rgba(247, 248, 251, 0.94);
      backdrop-filter: blur(10px);
    }}
    .topbar {{
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
      padding: 14px 20px;
    }}
    h1 {{
      margin: 0;
      font-size: 18px;
      line-height: 1.3;
    }}
    .meta {{
      color: var(--muted);
      font-size: 12px;
      white-space: nowrap;
    }}
    .layout {{
      display: grid;
      grid-template-columns: 320px minmax(0, 1fr);
      min-height: calc(100vh - 58px);
    }}
    aside {{
      border-right: 1px solid var(--line);
      background: var(--panel);
      padding: 16px;
      overflow: auto;
      max-height: calc(100vh - 58px);
      position: sticky;
      top: 58px;
    }}
    main {{
      padding: 16px 18px 28px;
      overflow: hidden;
    }}
    .cards {{
      display: grid;
      grid-template-columns: repeat(5, minmax(130px, 1fr));
      gap: 10px;
      margin-bottom: 14px;
    }}
    .card {{
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 12px;
      box-shadow: var(--shadow);
    }}
    .card .label {{
      color: var(--muted);
      font-size: 12px;
      margin-bottom: 4px;
    }}
    .card .value {{
      font-size: 18px;
      font-weight: 750;
      line-height: 1.2;
    }}
    .section {{
      margin-bottom: 16px;
      padding-bottom: 14px;
      border-bottom: 1px solid var(--line);
    }}
    .section:last-child {{ border-bottom: none; }}
    .section h2 {{
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
      margin: 0 0 8px;
      font-size: 13px;
    }}
    .section input[type="search"] {{
      width: 100%;
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 8px 9px;
      font-size: 13px;
      outline: none;
    }}
    .actions {{
      display: flex;
      gap: 6px;
      margin: 8px 0;
    }}
    button {{
      border: 1px solid var(--line);
      border-radius: 7px;
      background: #fff;
      color: var(--text);
      padding: 6px 8px;
      font-size: 12px;
      cursor: pointer;
    }}
    button:hover {{ border-color: var(--accent); color: var(--accent); }}
    .check-list {{
      display: grid;
      gap: 6px;
      max-height: 260px;
      overflow: auto;
      padding-right: 4px;
    }}
    label.check {{
      display: grid;
      grid-template-columns: 16px minmax(0, 1fr) auto;
      align-items: start;
      gap: 8px;
      font-size: 12px;
      color: #263247;
    }}
    label.check span.name {{
      overflow-wrap: anywhere;
      line-height: 1.35;
    }}
    label.check span.count {{
      color: var(--muted);
      font-variant-numeric: tabular-nums;
    }}
    .status-pills {{
      display: grid;
      gap: 6px;
    }}
    .toolbar {{
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 10px;
      margin-bottom: 12px;
    }}
    .toolbar input {{
      min-width: 260px;
      flex: 1;
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 9px 10px;
      font-size: 13px;
      outline: none;
    }}
    .table-wrap {{
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      box-shadow: var(--shadow);
      overflow: auto;
      max-height: calc(100vh - 245px);
    }}
    table {{
      width: 100%;
      min-width: 1500px;
      border-collapse: collapse;
      font-size: 12px;
    }}
    th, td {{
      padding: 8px 9px;
      border-bottom: 1px solid #edf0f5;
      text-align: left;
      vertical-align: top;
      white-space: nowrap;
    }}
    th {{
      position: sticky;
      top: 0;
      z-index: 2;
      background: #f3f5f9;
      color: #344054;
      font-weight: 700;
    }}
    td.wrap {{
      white-space: normal;
      min-width: 190px;
      max-width: 300px;
      overflow-wrap: anywhere;
    }}
    .badge {{
      display: inline-flex;
      align-items: center;
      border-radius: 999px;
      padding: 3px 8px;
      font-size: 11px;
      font-weight: 700;
    }}
    .matched {{ background: #dcfce7; color: var(--good); }}
    .unmatched {{ background: #fee2e2; color: var(--bad); }}
    .ambiguous {{ background: #fef3c7; color: var(--warn); }}
    .num {{ text-align: right; font-variant-numeric: tabular-nums; }}
    .positive {{ color: var(--good); }}
    .negative {{ color: var(--bad); }}
    .muted {{ color: var(--muted); }}
    @media (max-width: 980px) {{
      .layout {{ grid-template-columns: 1fr; }}
      aside {{
        position: static;
        max-height: none;
        border-right: none;
        border-bottom: 1px solid var(--line);
      }}
      .cards {{ grid-template-columns: repeat(2, minmax(0, 1fr)); }}
    }}
  </style>
</head>
<body>
  <header>
    <div class="topbar">
      <h1>NewAPI 日志匹配核算</h1>
      <div class="meta" id="meta"></div>
    </div>
  </header>
  <div class="layout">
    <aside>
      <div class="section">
        <h2>匹配状态</h2>
        <div class="status-pills" id="statusFilters"></div>
      </div>
      <div class="section">
        <h2>上游站点</h2>
        <div class="actions">
          <button type="button" data-action="all" data-target="host">全选</button>
          <button type="button" data-action="none" data-target="host">清空</button>
        </div>
        <div class="check-list" id="hostFilters"></div>
      </div>
      <div class="section">
        <h2>本站渠道</h2>
        <input type="search" id="channelSearch" placeholder="搜索渠道">
        <div class="actions">
          <button type="button" data-action="all" data-target="channel">全选</button>
          <button type="button" data-action="none" data-target="channel">清空</button>
        </div>
        <div class="check-list" id="channelFilters"></div>
      </div>
      <div class="section">
        <h2>本站模型</h2>
        <input type="search" id="modelSearch" placeholder="搜索模型">
        <div class="actions">
          <button type="button" data-action="all" data-target="model">全选</button>
          <button type="button" data-action="none" data-target="model">清空</button>
        </div>
        <div class="check-list" id="modelFilters"></div>
      </div>
    </aside>
    <main>
      <div class="cards" id="cards"></div>
      <div class="toolbar">
        <input type="search" id="tableSearch" placeholder="搜索用户名、模型、渠道、Request ID">
        <button type="button" id="perCallOnly">只看按次</button>
        <button type="button" id="resetFilters">重置筛选</button>
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>状态</th>
              <th>本站时间</th>
              <th>上游</th>
              <th>本站渠道</th>
              <th>本站模型</th>
              <th>分组</th>
              <th class="num">输入</th>
              <th class="num">输出</th>
              <th class="num">总 Tokens</th>
              <th class="num">本站收入</th>
              <th class="num">上游成本</th>
              <th class="num">毛利</th>
              <th>上游时间</th>
              <th>时间差</th>
              <th>原因</th>
              <th>本站 Request ID</th>
              <th>上游 Request ID</th>
            </tr>
          </thead>
          <tbody id="rows"></tbody>
        </table>
      </div>
    </main>
  </div>
  <script type="application/json" id="report-data">__REPORT_JSON__</script>
  <script>
    const data = JSON.parse(document.getElementById("report-data").textContent);
    const records = data.records;
    const filters = {{
      status: new Set(["matched", "ambiguous", "unmatched"]),
      host: new Set(),
      channel: new Set(),
      model: new Set(),
      search: "",
      channelSearch: "",
      modelSearch: "",
      perCallOnly: false,
    }};

    const by = (items, getter) => {{
      const map = new Map();
      for (const item of items) {{
        const key = getter(item) || "(空)";
        map.set(key, (map.get(key) || 0) + 1);
      }}
      return [...map.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
    }};

    const hosts = by(records, item => item.targetHost || "未映射");
    const channels = by(records, item => item.localChannel || "(空)");
    const models = by(records, item => item.localModel || "(空)");
    hosts.forEach(([name]) => filters.host.add(name));
    channels.forEach(([name]) => filters.channel.add(name));
    models.forEach(([name]) => filters.model.add(name));

    const money = value => {{
      if (value === null || value === undefined || Number.isNaN(Number(value))) return "";
      return "$" + Number(value).toFixed(6);
    }};
    const intFmt = value => Number(value || 0).toLocaleString();
    const html = value => String(value ?? "").replace(/[&<>"']/g, ch => ({{
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      "\\"": "&quot;",
      "'": "&#39;",
    }}[ch]));

    function makeChecks(containerId, type, items, searchValue = "") {{
      const container = document.getElementById(containerId);
      const needle = searchValue.trim().toLowerCase();
      container.innerHTML = items
        .filter(([name]) => !needle || name.toLowerCase().includes(needle))
        .map(([name, count]) => `
          <label class="check">
            <input type="checkbox" data-filter="${{type}}" value="${{html(name)}}" ${{filters[type].has(name) ? "checked" : ""}}>
            <span class="name">${{html(name)}}</span>
            <span class="count">${{count}}</span>
          </label>
        `).join("");
    }}

    function renderFilters() {{
      const statusItems = [["matched", "已匹配"], ["ambiguous", "多候选"], ["unmatched", "未匹配"]];
      document.getElementById("statusFilters").innerHTML = statusItems.map(([value, label]) => `
        <label class="check">
          <input type="checkbox" data-filter="status" value="${{value}}" ${{filters.status.has(value) ? "checked" : ""}}>
          <span class="name">${{label}}</span>
          <span class="count">${{records.filter(r => r.status === value).length}}</span>
        </label>
      `).join("");
      makeChecks("hostFilters", "host", hosts);
      makeChecks("channelFilters", "channel", channels, filters.channelSearch);
      makeChecks("modelFilters", "model", models, filters.modelSearch);
    }}

    function include(record) {{
      const host = record.targetHost || "未映射";
      const channel = record.localChannel || "(空)";
      const model = record.localModel || "(空)";
      if (!filters.status.has(record.status)) return false;
      if (!filters.host.has(host)) return false;
      if (!filters.channel.has(channel)) return false;
      if (!filters.model.has(model)) return false;
      if (filters.perCallOnly && !record.isPerCall) return false;
      if (filters.search) {{
        const haystack = [
          record.localUsername,
          record.localTokenName,
          record.localGroup,
          record.localChannel,
          record.localModel,
          record.localRequestId,
          record.upstreamRequestId,
          record.targetHost,
        ].join(" ").toLowerCase();
        if (!haystack.includes(filters.search)) return false;
      }}
      return true;
    }}

    function renderCards(rows) {{
      const matched = rows.filter(r => r.status === "matched");
      const localRevenue = rows.reduce((sum, r) => sum + Number(r.localRevenue || 0), 0);
      const upstreamCost = matched.reduce((sum, r) => sum + Number(r.upstreamCost || 0), 0);
      const gross = matched.reduce((sum, r) => sum + Number(r.gross || 0), 0);
      const matchRate = rows.length ? matched.length / rows.length * 100 : 0;
      document.getElementById("cards").innerHTML = [
        ["当前记录", intFmt(rows.length)],
        ["匹配率", matchRate.toFixed(2) + "%"],
        ["本站收入", money(localRevenue)],
        ["已匹配上游成本", money(upstreamCost)],
        ["已匹配毛利", money(gross)],
      ].map(([label, value]) => `
        <div class="card"><div class="label">${{label}}</div><div class="value">${{value}}</div></div>
      `).join("");
    }}

    function renderRows() {{
      const filtered = records.filter(include);
      renderCards(filtered);
      const shown = filtered.slice(0, 1000);
      document.getElementById("rows").innerHTML = shown.map(record => {{
        const grossClass = record.gross === null ? "" : Number(record.gross) >= 0 ? "positive" : "negative";
        return `
          <tr>
            <td><span class="badge ${{record.status}}">${{html(record.status)}}</span></td>
            <td>${{html(record.localTime)}}</td>
            <td>${{html(record.targetHost || "未映射")}}</td>
            <td class="wrap">${{html(record.localChannel)}}</td>
            <td class="wrap">${{html(record.localModel)}}</td>
            <td class="wrap">${{html(record.localGroup)}}</td>
            <td class="num">${{intFmt(record.promptTokens)}}</td>
            <td class="num">${{intFmt(record.completionTokens)}}</td>
            <td class="num">${{intFmt(record.totalTokens)}}</td>
            <td class="num">${{money(record.localRevenue)}}</td>
            <td class="num">${{record.status === "matched" ? money(record.upstreamCost) : ""}}</td>
            <td class="num ${{grossClass}}">${{record.gross === null ? "" : money(record.gross)}}</td>
            <td>${{html(record.upstreamTime)}}</td>
            <td>${{record.timeDiffSeconds ?? ""}}</td>
            <td class="wrap muted">${{html(record.reason)}}</td>
            <td class="wrap">${{html(record.localRequestId)}}</td>
            <td class="wrap">${{html(record.upstreamRequestId)}}</td>
          </tr>
        `;
      }).join("") + (filtered.length > shown.length ? `
        <tr><td colspan="17" class="muted">当前筛选共有 ${{filtered.length}} 条，仅显示前 ${{shown.length}} 条，请继续缩小筛选。</td></tr>
      ` : "");
    }}

    function render() {{
      document.getElementById("meta").textContent =
        `生成时间 ${{data.generatedAt}}，时间窗口 ${{data.timeWindowSeconds}} 秒`;
      renderFilters();
      renderRows();
    }}

    document.addEventListener("change", event => {{
      const target = event.target;
      if (!target.matches("input[type=checkbox][data-filter]")) return;
      const type = target.dataset.filter;
      if (target.checked) filters[type].add(target.value);
      else filters[type].delete(target.value);
      renderRows();
    }});

    document.addEventListener("click", event => {{
      const target = event.target;
      if (!target.matches("button[data-action]")) return;
      const type = target.dataset.target;
      const source = type === "host" ? hosts : type === "channel" ? channels : models;
      filters[type] = new Set(target.dataset.action === "all" ? source.map(([name]) => name) : []);
      render();
    }});

    document.getElementById("tableSearch").addEventListener("input", event => {{
      filters.search = event.target.value.trim().toLowerCase();
      renderRows();
    }});
    document.getElementById("channelSearch").addEventListener("input", event => {{
      filters.channelSearch = event.target.value;
      makeChecks("channelFilters", "channel", channels, filters.channelSearch);
    }});
    document.getElementById("modelSearch").addEventListener("input", event => {{
      filters.modelSearch = event.target.value;
      makeChecks("modelFilters", "model", models, filters.modelSearch);
    }});
    document.getElementById("perCallOnly").addEventListener("click", event => {{
      filters.perCallOnly = !filters.perCallOnly;
      event.target.textContent = filters.perCallOnly ? "取消只看按次" : "只看按次";
      renderRows();
    }});
    document.getElementById("resetFilters").addEventListener("click", () => {{
      filters.status = new Set(["matched", "ambiguous", "unmatched"]);
      filters.host = new Set(hosts.map(([name]) => name));
      filters.channel = new Set(channels.map(([name]) => name));
      filters.model = new Set(models.map(([name]) => name));
      filters.search = "";
      filters.channelSearch = "";
      filters.modelSearch = "";
      filters.perCallOnly = false;
      document.getElementById("tableSearch").value = "";
      document.getElementById("channelSearch").value = "";
      document.getElementById("modelSearch").value = "";
      document.getElementById("perCallOnly").textContent = "只看按次";
      render();
    }});
    render();
  </script>
</body>
</html>
"""
    template = template.replace("{{", "{").replace("}}", "}")
    return template.replace("__REPORT_JSON__", json_payload)


def fmt_time(value: datetime) -> str:
    return value.strftime("%Y-%m-%d %H:%M:%S")


def print_summary(summary: dict[str, Any], report_dir: Path) -> None:
    print(f"Report: {report_dir}")
    print(
        "Total: "
        f"local=${money(summary['local_revenue'])}, "
        f"upstream=${money(summary['upstream_cost'])}, "
        f"gross=${money(summary['gross'])}"
    )
    print(
        "Match: "
        f"matched={summary['matched_rows']}, "
        f"ambiguous={summary['ambiguous_rows']}, "
        f"unmatched={summary['unmatched_rows']}, "
        f"unused_upstream={summary['unused_upstream_rows']}"
    )
    for host, item in summary["by_host"].items():
        print(
            f"- {host}: gross=${money(item['gross'])}, "
            f"matched={item['matched_rows']}/{item['local_rows']}, "
            f"per_call_break_even=${money(item['per_call_break_even_price'])}"
        )


def default_report_dir(log_dir: Path) -> Path:
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    return log_dir / "_match_reports" / f"newapi_log_match_{timestamp}"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("log_dir", nargs="?", default="logs", help="Directory containing exported CSV files.")
    parser.add_argument("--config", help="JSON rules file. Defaults to built-in rules.")
    parser.add_argument("--out-dir", help="Report output directory.")
    parser.add_argument("--local-host", help="Override local host from config.")
    parser.add_argument("--time-window", type=int, help="Override match time window in seconds.")
    args = parser.parse_args()

    log_dir = Path(args.log_dir).resolve()
    if not log_dir.exists():
        raise SystemExit(f"Log directory not found: {log_dir}")

    config_path = Path(args.config).resolve() if args.config else None
    config = load_config(config_path)
    if args.local_host:
        config["local_host"] = args.local_host
    if args.time_window:
        config["time_window_seconds"] = args.time_window

    rows_by_host = read_logs(log_dir, config)
    local_host = config["local_host"]
    if local_host not in rows_by_host:
        known = ", ".join(sorted(rows_by_host)) or "(none)"
        raise SystemExit(f"Local host `{local_host}` not found. Known hosts: {known}")

    assign_target_hosts(rows_by_host[local_host], config)
    upstream_rows_by_host = {
        host: rows for host, rows in rows_by_host.items() if host != local_host
    }
    results, used_upstream = match_rows(rows_by_host[local_host], upstream_rows_by_host, config)
    summary = summarize(rows_by_host, results, used_upstream, config)

    report_dir = Path(args.out_dir).resolve() if args.out_dir else default_report_dir(log_dir)
    write_reports(report_dir, rows_by_host, results, used_upstream, summary, config)
    print_summary(summary, report_dir)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
