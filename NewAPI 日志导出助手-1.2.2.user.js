// ==UserScript==
// @name         NewAPI 日志导出助手
// @namespace    https://newapi.youkies.space/
// @version      1.2.4
// @description  兼容 NewAPI 经典版 /console/log 与 /legacy/console/log 的使用日志导出工具，支持 CSV/JSON 导出、上传和注册 NewAPI Tools 后台同步
// @author       Youkies
// @match        *://*/*
// @grant        none
// @run-at       document-start
// ==/UserScript==

(function () {
  "use strict";

  const CONFIG = {
    QUOTA_PER_UNIT: 500000,
    PAGE_SIZE: 100,
    REQUEST_DELAY: 80,
    TOOLS_CONFIG_KEY: "newapi_tools_upload_config_v1",
  };

  let lastCapturedParams = {};
  let isExporting = false;
  let shouldAbort = false;
  let routeCheckTimer = null;
  let networkInterceptorInstalled = false;

  let _origFetch = window.fetch ? window.fetch.bind(window) : null;

  function normalizePath(path) {
    const normalized = String(path || "/").replace(/\/+$/, "");
    return normalized || "/";
  }

  function isClassicLogRoute() {
    const path = normalizePath(location.pathname);
    if (path === "/console/log") return true;
    if (path.endsWith("/legacy/console/log")) return true;

    const hashPath = normalizePath(
      (location.hash || "").replace(/^#/, "").split("?")[0]
    );
    return hashPath === "/console/log" || hashPath.endsWith("/console/log");
  }

  function isLogListPath(pathname) {
    const path = normalizePath(pathname);
    return path === "/api/log" || path === "/api/log/self";
  }

  function captureLogParams(rawUrl) {
    try {
      const url = typeof rawUrl === "string" ? rawUrl : rawUrl?.url || "";
      if (!url || !url.includes("/api/log")) return;

      const parsed = new URL(url, location.origin);
      if (!isLogListPath(parsed.pathname)) return;

      lastCapturedParams = Object.fromEntries(parsed.searchParams);
      console.log("[LogExporter] Captured params:", lastCapturedParams);
    } catch (_) {
      // Ignore non-standard URLs.
    }
  }

  function isLikelyNewApiPage() {
    const path = normalizePath(location.pathname);
    const hashPath = normalizePath(
      (location.hash || "").replace(/^#/, "").split("?")[0]
    );

    return (
      path === "/" ||
      path.startsWith("/console") ||
      path.startsWith("/legacy") ||
      path.startsWith("/login") ||
      path.startsWith("/register") ||
      hashPath.startsWith("/console")
    );
  }

  function installNetworkInterceptor() {
    if (networkInterceptorInstalled || !isLikelyNewApiPage()) return;

    networkInterceptorInstalled = true;

    if (_origFetch) {
      window.fetch = function (...args) {
        captureLogParams(args[0]);
        return _origFetch(...args);
      };
    }

    const origXHROpen = XMLHttpRequest.prototype.open;
    XMLHttpRequest.prototype.open = function (method, url, ...rest) {
      this.__logExporterUrl = url;
      captureLogParams(url);
      return origXHROpen.call(this, method, url, ...rest);
    };
  }

  installNetworkInterceptor();

  function getAuthHeaders() {
    const headers = {};

    try {
      const user = JSON.parse(localStorage.getItem("user") || "{}");
      if (user.id) headers["New-API-User"] = String(user.id);
      if (user.token) headers.Authorization = `Bearer ${user.token}`;
    } catch (_) {
      // User info is optional; cookie auth may still work.
    }

    const session = localStorage.getItem("session");
    if (!headers.Authorization && session) {
      headers.Authorization = `Bearer ${session}`;
    }

    return headers;
  }

  function getStoredToolsConfig() {
    try {
      return {
        toolsUrl: "",
        authToken: "",
        uploadEnabled: false,
        registerEnabled: false,
        matchToleranceSeconds: 60,
        syncIntervalMinutes: 1,
        ...(JSON.parse(localStorage.getItem(CONFIG.TOOLS_CONFIG_KEY) || "{}") || {}),
      };
    } catch (_) {
      return {
        toolsUrl: "",
        authToken: "",
        uploadEnabled: false,
        registerEnabled: false,
        matchToleranceSeconds: 60,
        syncIntervalMinutes: 1,
      };
    }
  }

  function saveToolsConfig(config) {
    localStorage.setItem(CONFIG.TOOLS_CONFIG_KEY, JSON.stringify(config));
  }

  function getToolsAuthHeaders(authToken) {
    const token = String(authToken || "").trim();
    const headers = { "Content-Type": "application/json" };
    if (!token) return headers;
    if (/^bearer\s+/i.test(token)) {
      headers.Authorization = token;
    } else {
      headers["X-API-Key"] = token;
    }
    return headers;
  }

  function getToolsOptionsFromForm() {
    const toolsUrl = $id("lex-tools-url").value.trim().replace(/\/+$/, "");
    const authToken = $id("lex-tools-token").value.trim();
    const uploadEnabled = Boolean($id("lex-upload-tools").checked);
    const registerEnabled = Boolean($id("lex-register-tools").checked);
    const matchToleranceSeconds =
      Number.parseInt($id("lex-match-window").value, 10) || 60;
    const syncIntervalMinutes =
      Number.parseInt($id("lex-sync-interval").value, 10) || 1;

    return {
      toolsUrl,
      authToken,
      uploadEnabled,
      registerEnabled,
      matchToleranceSeconds,
      syncIntervalMinutes,
    };
  }

  function getUpstreamLoginState() {
    const headers = getAuthHeaders();
    const auth = String(headers.Authorization || "");
    return {
      token: auth.replace(/^bearer\s+/i, "").trim(),
      userId: String(headers["New-API-User"] || "").trim(),
    };
  }

  async function registerUpstreamToTools(toolsOptions) {
    saveToolsConfig(toolsOptions);

    if (!toolsOptions.registerEnabled) {
      return null;
    }
    if (!toolsOptions.toolsUrl) {
      throw new Error("已启用后台同步注册，但未填写 NewAPI Tools 地址");
    }
    if (!toolsOptions.authToken) {
      throw new Error("已启用后台同步注册，但未填写 NewAPI Tools API Key/JWT");
    }

    const upstream = getUpstreamLoginState();
    if (!upstream.token) {
      throw new Error("未找到当前上游登录 token，请先登录 NewAPI 后刷新页面");
    }

    const registerUrl = `${toolsOptions.toolsUrl}/api/cost/upstream-sync/register`;
    const resp = await fetch(registerUrl, {
      method: "POST",
      headers: getToolsAuthHeaders(toolsOptions.authToken),
      body: JSON.stringify({
        enabled: true,
        source_name: document.title || location.host,
        base_url: location.origin,
        endpoint: "auto",
        auth_token: upstream.token,
        user_id: upstream.userId,
        page_size: CONFIG.PAGE_SIZE,
        request_delay_ms: CONFIG.REQUEST_DELAY,
        interval_minutes: Math.max(1, toolsOptions.syncIntervalMinutes || 1),
        lookback_minutes: 60,
        overlap_minutes: 10,
        match_tolerance_seconds: toolsOptions.matchToleranceSeconds || 60,
        log_type: Number.parseInt($id("lex-type").value, 10) || 2,
        max_pages_per_run: 1000,
      }),
    });

    const data = await resp.json().catch(() => ({}));
    if (!resp.ok || data.success === false) {
      const message =
        data?.error?.message || data?.message || `HTTP ${resp.status}`;
      throw new Error(`注册后台同步失败：${message}`);
    }
    return data.data || data;
  }

  async function uploadLogsToTools(logs, options, toolsOptions) {
    saveToolsConfig(toolsOptions);

    if (!toolsOptions.uploadEnabled) {
      return null;
    }
    if (!toolsOptions.toolsUrl) {
      throw new Error("已启用上传，但未填写 NewAPI Tools 地址");
    }
    if (!toolsOptions.authToken) {
      throw new Error("已启用上传，但未填写 NewAPI Tools API Key/JWT");
    }

    const uploadUrl = `${toolsOptions.toolsUrl}/api/cost/upstream-sync/upload`;
    const resp = await fetch(uploadUrl, {
      method: "POST",
      headers: getToolsAuthHeaders(toolsOptions.authToken),
      body: JSON.stringify({
        source_url: location.origin,
        source_name: document.title || location.host,
        start_time: options.startTs,
        end_time: options.endTs,
        match_tolerance_seconds: toolsOptions.matchToleranceSeconds,
        logs,
      }),
    });

    const data = await resp.json().catch(() => ({}));
    if (!resp.ok || data.success === false) {
      const message =
        data?.error?.message || data?.message || `HTTP ${resp.status}`;
      throw new Error(`上传 NewAPI Tools 失败：${message}`);
    }
    return data.data || data;
  }

  async function apiFetch(path, params = {}) {
    if (!_origFetch) {
      throw new Error("当前浏览器不支持 fetch");
    }

    const url = new URL(`${location.origin}${path}`);
    for (const [key, value] of Object.entries(params)) {
      if (value !== "" && value !== null && value !== undefined) {
        url.searchParams.set(key, String(value));
      }
    }

    const resp = await _origFetch(url.toString(), {
      headers: getAuthHeaders(),
      credentials: "same-origin",
    });

    if (!resp.ok) {
      const text = await resp.text().catch(() => "");
      throw new Error(`HTTP ${resp.status}: ${text.slice(0, 200)}`);
    }

    const json = await resp.json();
    if (json.success === false) {
      throw new Error(json.message || "API returned success=false");
    }
    return json;
  }

  function extractLogs(responseData) {
    if (!responseData) return [];
    const inner = responseData.data;
    if (!inner) return [];
    if (Array.isArray(inner.items)) return inner.items;
    if (Array.isArray(inner.data)) return inner.data;
    if (Array.isArray(inner)) return inner;
    return [];
  }

  function extractTotal(responseData) {
    return Number(responseData?.data?.total || 0);
  }

  async function detectApiPath() {
    try {
      const resp = await apiFetch("/api/log/", { p: 1, page_size: 1 });
      if (resp.success !== false) {
        console.log("[LogExporter] Using admin endpoint: /api/log/");
        return "/api/log/";
      }
    } catch (err) {
      console.log("[LogExporter] Admin endpoint failed:", err.message);
    }

    console.log("[LogExporter] Falling back to user endpoint: /api/log/self/");
    return "/api/log/self/";
  }

  async function exportLogs(options, onProgress, onStatus) {
    const {
      startTs,
      endTs,
      logType,
      modelName,
      username,
      tokenName,
      channel,
      group,
      requestId,
    } = options;

    const baseParams = {};
    if (startTs) baseParams.start_timestamp = startTs;
    if (endTs) baseParams.end_timestamp = endTs;
    if (logType !== "") baseParams.type = logType;
    if (modelName) baseParams.model_name = modelName;
    if (username) baseParams.username = username;
    if (tokenName) baseParams.token_name = tokenName;
    if (channel) baseParams.channel = channel;
    if (group) baseParams.group = group;
    if (requestId) baseParams.request_id = requestId;

    onStatus("正在检测权限...");
    const apiPath = await detectApiPath();
    onStatus(apiPath === "/api/log/" ? "管理员模式" : "用户模式");

    const firstResp = await apiFetch(apiPath, {
      ...baseParams,
      p: 1,
      page_size: CONFIG.PAGE_SIZE,
    });

    let allLogs = extractLogs(firstResp);
    const total = extractTotal(firstResp);
    const totalPages = Math.ceil(total / CONFIG.PAGE_SIZE);
    onProgress(total > 0 ? 1 : 0, totalPages, allLogs.length, total);

    for (let page = 2; page <= totalPages; page++) {
      if (shouldAbort) throw new Error("用户取消导出");

      const resp = await apiFetch(apiPath, {
        ...baseParams,
        p: page,
        page_size: CONFIG.PAGE_SIZE,
      });
      const logs = extractLogs(resp);
      allLogs = allLogs.concat(logs);

      onProgress(page, totalPages, allLogs.length, total);
      if (logs.length === 0) break;

      await sleep(CONFIG.REQUEST_DELAY);
    }

    return allLogs;
  }

  function parseOther(otherStr) {
    if (!otherStr) return {};
    if (typeof otherStr === "object") return otherStr;

    try {
      return JSON.parse(otherStr);
    } catch (_) {
      return {};
    }
  }

  function formatTimestamp(ts) {
    if (!ts) return "";
    const date = new Date(Number(ts) * 1000);
    const pad = (n) => String(n).padStart(2, "0");
    return (
      `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ` +
      `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
    );
  }

  function logsToCSV(logs, quotaPerUnit) {
    const headers = [
      "时间",
      "用户名",
      "令牌名称",
      "分组",
      "模型名称",
      "渠道ID",
      "渠道名称",
      "类型",
      "输入Tokens",
      "输出Tokens",
      "总Tokens",
      "Quota",
      "费用(USD)",
      "耗时(s)",
      "是否流式",
      "模型倍率",
      "分组倍率",
      "补全倍率",
      "Request ID",
      "详情",
    ];

    const typeMap = {
      0: "未知",
      1: "充值",
      2: "消费",
      3: "管理",
      4: "系统",
      5: "错误",
      6: "退款",
    };

    const rows = logs.map((log) => {
      const other = parseOther(log.other);
      const inputTokens = Number(log.prompt_tokens || 0);
      const outputTokens = Number(log.completion_tokens || 0);
      const quota = Number(log.quota || 0);

      return [
        formatTimestamp(log.created_at),
        log.username || "",
        log.token_name || "",
        log.group || "",
        log.model_name || "",
        log.channel || "",
        log.channel_name || "",
        typeMap[log.type] || String(log.type ?? ""),
        inputTokens,
        outputTokens,
        inputTokens + outputTokens,
        quota,
        (quota / quotaPerUnit).toFixed(6),
        log.use_time || 0,
        log.is_stream ? "是" : "否",
        other.model_ratio ?? "",
        other.group_ratio ?? "",
        other.completion_ratio ?? "",
        log.request_id || "",
        log.content || "",
      ];
    });

    const esc = (value) => {
      const text = String(value ?? "");
      if (/[",\r\n]/.test(text)) {
        return `"${text.replace(/"/g, '""')}"`;
      }
      return text;
    };

    const lines = [headers.map(esc).join(",")];
    for (const row of rows) {
      lines.push(row.map(esc).join(","));
    }

    const totalQuota = logs.reduce((sum, log) => sum + Number(log.quota || 0), 0);
    const totalInput = logs.reduce(
      (sum, log) => sum + Number(log.prompt_tokens || 0),
      0
    );
    const totalOutput = logs.reduce(
      (sum, log) => sum + Number(log.completion_tokens || 0),
      0
    );

    lines.push("");
    lines.push(
      [
        "汇总",
        "",
        "",
        "",
        "",
        "",
        "合计",
        "",
        totalInput,
        totalOutput,
        totalInput + totalOutput,
        totalQuota,
        (totalQuota / quotaPerUnit).toFixed(6),
        "",
        "",
        "",
        "",
        "",
        "",
        "",
      ]
        .map(esc)
        .join(",")
    );

    return lines.join("\r\n");
  }

  function logsToJSON(logs, quotaPerUnit) {
    const enriched = logs.map((log) => {
      const quota = Number(log.quota || 0);
      return {
        ...log,
        _formatted_time: formatTimestamp(log.created_at),
        _cost_usd: (quota / quotaPerUnit).toFixed(6),
        _total_tokens:
          Number(log.prompt_tokens || 0) + Number(log.completion_tokens || 0),
        _other_parsed: parseOther(log.other),
      };
    });
    return JSON.stringify(enriched, null, 2);
  }

  function sleep(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  function downloadFile(content, filename, mime = "text/csv") {
    const prefix = mime === "text/csv" ? "\ufeff" : "";
    const blob = new Blob([prefix + content], {
      type: `${mime};charset=utf-8`,
    });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = filename;
    document.body.appendChild(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }

  function nowDateStr() {
    const date = new Date();
    const pad = (n) => String(n).padStart(2, "0");
    return (
      `${date.getFullYear()}${pad(date.getMonth() + 1)}${pad(date.getDate())}_` +
      `${pad(date.getHours())}${pad(date.getMinutes())}${pad(date.getSeconds())}`
    );
  }

  function todayStart() {
    const date = new Date();
    date.setHours(0, 0, 0, 0);
    return date;
  }

  function toLocalDatetimeStr(date) {
    const pad = (n) => String(n).padStart(2, "0");
    return (
      `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T` +
      `${pad(date.getHours())}:${pad(date.getMinutes())}`
    );
  }

  function escapeAttr(value) {
    return String(value ?? "")
      .replace(/&/g, "&amp;")
      .replace(/"/g, "&quot;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;");
  }

  function injectStyles() {
    if (document.getElementById("log-export-style")) return;

    const css = `
      #log-export-btn {
        position: fixed;
        right: 28px;
        bottom: 28px;
        z-index: 99999;
        display: none;
        align-items: center;
        gap: 8px;
        padding: 11px 18px;
        border: none;
        border-radius: 999px;
        background: #4f46e5;
        color: #fff;
        font: 600 14px/1.2 -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans SC", sans-serif;
        cursor: pointer;
        box-shadow: 0 8px 24px rgba(79, 70, 229, 0.35);
      }
      #log-export-btn:hover { background: #4338ca; transform: translateY(-1px); }
      #log-export-btn:active { transform: translateY(0); }

      #log-export-overlay {
        position: fixed;
        inset: 0;
        z-index: 100000;
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 18px;
        background: rgba(15, 23, 42, 0.48);
        opacity: 0;
        pointer-events: none;
        transition: opacity 0.18s ease;
      }
      #log-export-overlay.visible {
        opacity: 1;
        pointer-events: auto;
      }

      #log-export-modal {
        width: 560px;
        max-width: calc(100vw - 32px);
        max-height: 88vh;
        overflow: auto;
        border-radius: 12px;
        background: #fff;
        color: #111827;
        box-shadow: 0 22px 70px rgba(15, 23, 42, 0.28);
        font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans SC", sans-serif;
      }
      .lex-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 16px;
        padding: 20px 24px 14px;
        border-bottom: 1px solid #eef2f7;
      }
      .lex-header h2 {
        margin: 0;
        font-size: 18px;
        line-height: 1.3;
        font-weight: 700;
      }
      .lex-close {
        width: 32px;
        height: 32px;
        border: none;
        border-radius: 8px;
        background: #f3f4f6;
        color: #4b5563;
        cursor: pointer;
        font-size: 18px;
      }
      .lex-close:hover { background: #e5e7eb; }
      .lex-body { padding: 20px 24px 24px; }
      .lex-row {
        display: grid;
        grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
        gap: 14px;
        margin-bottom: 14px;
      }
      .lex-row.full { grid-template-columns: minmax(0, 1fr); }
      .lex-field {
        display: flex;
        flex-direction: column;
        gap: 6px;
      }
      .lex-field label {
        color: #6b7280;
        font-size: 12px;
        font-weight: 600;
      }
      .lex-field input,
      .lex-field select {
        width: 100%;
        box-sizing: border-box;
        border: 1px solid #d9dee8;
        border-radius: 8px;
        background: #fff;
        color: #111827;
        padding: 8px 10px;
        font-size: 13px;
        outline: none;
      }
      .lex-field input:focus,
      .lex-field select:focus {
        border-color: #4f46e5;
        box-shadow: 0 0 0 3px rgba(79, 70, 229, 0.12);
      }
      .lex-hint {
        margin: -5px 0 12px;
        color: #9ca3af;
        font-size: 12px;
      }
      .lex-check {
        display: flex;
        align-items: center;
        gap: 8px;
        color: #374151;
        font-size: 13px;
        font-weight: 600;
      }
      .lex-check input {
        width: 16px;
        height: 16px;
        accent-color: #4f46e5;
      }
      .lex-actions {
        display: flex;
        gap: 10px;
        margin-top: 18px;
      }
      .lex-btn {
        flex: 1;
        border: none;
        border-radius: 8px;
        padding: 10px 12px;
        font-size: 14px;
        font-weight: 600;
        cursor: pointer;
      }
      .lex-btn-primary { background: #4f46e5; color: #fff; }
      .lex-btn-primary:hover:not(:disabled) { background: #4338ca; }
      .lex-btn-primary:disabled { cursor: not-allowed; opacity: 0.62; }
      .lex-btn-secondary { background: #f3f4f6; color: #374151; }
      .lex-btn-secondary:hover { background: #e5e7eb; }
      .lex-btn-danger { background: #fee2e2; color: #b91c1c; }
      .lex-btn-danger:hover { background: #fecaca; }
      .lex-progress-wrap {
        display: none;
        margin-top: 18px;
      }
      .lex-progress-wrap.active { display: block; }
      .lex-progress-bar-bg {
        height: 8px;
        overflow: hidden;
        border-radius: 999px;
        background: #eef2f7;
      }
      .lex-progress-bar {
        width: 0%;
        height: 100%;
        border-radius: inherit;
        background: #4f46e5;
        transition: width 0.22s ease;
      }
      .lex-progress-text {
        margin-top: 8px;
        color: #6b7280;
        text-align: center;
        font-size: 12px;
      }
      .lex-progress-text .count {
        color: #4f46e5;
        font-weight: 700;
      }
      .lex-result {
        display: none;
        margin-top: 16px;
        border: 1px solid #bbf7d0;
        border-radius: 8px;
        background: #f0fdf4;
        color: #166534;
        padding: 12px 14px;
        font-size: 13px;
        line-height: 1.65;
      }
      .lex-result.active { display: block; }
      .lex-result.error {
        border-color: #fecaca;
        background: #fef2f2;
        color: #991b1b;
      }
      @media (max-width: 640px) {
        #log-export-btn {
          right: 16px;
          bottom: 16px;
        }
        .lex-row { grid-template-columns: minmax(0, 1fr); }
        .lex-actions { flex-direction: column; }
      }
      @media (prefers-color-scheme: dark) {
        #log-export-modal {
          background: #18181b;
          color: #f4f4f5;
        }
        .lex-header { border-color: #27272a; }
        .lex-close,
        .lex-btn-secondary {
          background: #27272a;
          color: #d4d4d8;
        }
        .lex-close:hover,
        .lex-btn-secondary:hover { background: #3f3f46; }
        .lex-field label,
        .lex-progress-text { color: #a1a1aa; }
        .lex-field input,
        .lex-field select {
          background: #09090b;
          border-color: #3f3f46;
          color: #f4f4f5;
        }
        .lex-hint { color: #71717a; }
        .lex-progress-bar-bg { background: #27272a; }
        .lex-result {
          background: #052e16;
          border-color: #166534;
          color: #bbf7d0;
        }
        .lex-result.error {
          background: #450a0a;
          border-color: #991b1b;
          color: #fecaca;
        }
      }
    `;

    const style = document.createElement("style");
    style.id = "log-export-style";
    style.textContent = css;
    document.head.appendChild(style);
  }

  function injectUI() {
    if (!document.body || !document.head) {
      setTimeout(injectUI, 100);
      return;
    }
    if (document.getElementById("log-export-btn")) return;

    injectStyles();

    const button = document.createElement("button");
    button.id = "log-export-btn";
    button.type = "button";
    button.textContent = "导出日志";
    button.style.display = isClassicLogRoute() ? "flex" : "none";
    button.addEventListener("click", openModal);
    document.body.appendChild(button);

    const overlay = document.createElement("div");
    overlay.id = "log-export-overlay";
    overlay.innerHTML = buildModalHTML();
    document.body.appendChild(overlay);

    overlay.addEventListener("click", (event) => {
      if (event.target === overlay && !isExporting) closeModal();
    });

    $id("lex-btn-export").addEventListener("click", handleExport);
    $id("lex-btn-close").addEventListener("click", () => {
      if (!isExporting) closeModal();
    });
    $id("lex-btn-cancel").addEventListener("click", () => {
      shouldAbort = true;
    });
    $id("lex-btn-sync").addEventListener("click", syncFromPage);
  }

  function buildModalHTML() {
    const start = toLocalDatetimeStr(todayStart());
    const end = toLocalDatetimeStr(new Date());
    const tools = getStoredToolsConfig();

    return `
      <div id="log-export-modal">
        <div class="lex-header">
          <h2>NewAPI 日志导出助手</h2>
          <button class="lex-close" id="lex-btn-close" type="button" aria-label="关闭">&times;</button>
        </div>
        <div class="lex-body">
          <div class="lex-row">
            <div class="lex-field">
              <label for="lex-start">开始时间</label>
              <input type="datetime-local" id="lex-start" value="${start}" step="1">
            </div>
            <div class="lex-field">
              <label for="lex-end">结束时间</label>
              <input type="datetime-local" id="lex-end" value="${end}" step="1">
            </div>
          </div>

          <div class="lex-row">
            <div class="lex-field">
              <label for="lex-type">日志类型</label>
              <select id="lex-type">
                <option value="0">全部</option>
                <option value="2" selected>消费</option>
                <option value="1">充值</option>
                <option value="3">管理</option>
                <option value="4">系统</option>
                <option value="5">错误</option>
                <option value="6">退款</option>
              </select>
            </div>
            <div class="lex-field">
              <label for="lex-format">导出格式</label>
              <select id="lex-format">
                <option value="csv" selected>CSV (Excel)</option>
                <option value="json">JSON</option>
              </select>
            </div>
          </div>

          <div class="lex-row">
            <div class="lex-field">
              <label for="lex-model">模型名称</label>
              <input type="text" id="lex-model" placeholder="留空表示全部">
            </div>
            <div class="lex-field">
              <label for="lex-username">用户名</label>
              <input type="text" id="lex-username" placeholder="管理员可用">
            </div>
          </div>

          <div class="lex-row">
            <div class="lex-field">
              <label for="lex-token">令牌名称</label>
              <input type="text" id="lex-token" placeholder="留空表示全部">
            </div>
            <div class="lex-field">
              <label for="lex-channel">渠道 ID</label>
              <input type="text" id="lex-channel" placeholder="管理员可用">
            </div>
          </div>

          <div class="lex-row">
            <div class="lex-field">
              <label for="lex-group">分组</label>
              <input type="text" id="lex-group" placeholder="留空表示全部">
            </div>
            <div class="lex-field">
              <label for="lex-request-id">Request ID</label>
              <input type="text" id="lex-request-id" placeholder="留空表示全部">
            </div>
          </div>

          <div class="lex-row full">
            <div class="lex-field">
              <label for="lex-quota-unit">额度单位 (Quota/USD)</label>
              <input type="number" id="lex-quota-unit" value="${CONFIG.QUOTA_PER_UNIT}">
            </div>
          </div>
          <div class="lex-hint">默认：500000 quota = 1 USD。</div>

          <div class="lex-row full">
            <label class="lex-check">
              <input type="checkbox" id="lex-upload-tools" ${tools.uploadEnabled ? "checked" : ""}>
              导出后上传到 NewAPI Tools 自动匹配成本
            </label>
          </div>

          <div class="lex-row full">
            <label class="lex-check">
              <input type="checkbox" id="lex-register-tools" ${tools.registerEnabled ? "checked" : ""}>
              保存当前上游登录态供 Tools 后台定时拉取
            </label>
          </div>

          <div class="lex-row">
            <div class="lex-field">
              <label for="lex-tools-url">NewAPI Tools 地址</label>
              <input type="text" id="lex-tools-url" value="${escapeAttr(tools.toolsUrl)}" placeholder="https://tools.example.com">
            </div>
            <div class="lex-field">
              <label for="lex-tools-token">Tools API Key / Bearer JWT</label>
              <input type="password" id="lex-tools-token" value="${escapeAttr(tools.authToken)}" placeholder="推荐填写 tools 的 API_KEY">
            </div>
          </div>

          <div class="lex-row">
            <div class="lex-field">
              <label for="lex-match-window">上传匹配窗口（秒）</label>
              <input type="number" id="lex-match-window" min="1" max="3600" value="${Number(tools.matchToleranceSeconds || 60)}">
            </div>
            <div class="lex-field">
              <label for="lex-sync-interval">后台拉取间隔（分钟）</label>
              <input type="number" id="lex-sync-interval" min="1" max="1440" value="${Number(tools.syncIntervalMinutes || 1)}">
            </div>
          </div>
          <div class="lex-hint">勾选后台拉取会把当前上游网址和登录 token 保存到 Tools：${escapeAttr(location.origin)}。</div>

          <div class="lex-actions">
            <button class="lex-btn lex-btn-secondary" id="lex-btn-sync" type="button" title="从页面当前筛选条件同步">同步页面筛选</button>
            <button class="lex-btn lex-btn-primary" id="lex-btn-export" type="button">开始导出</button>
          </div>

          <div class="lex-progress-wrap" id="lex-progress">
            <div class="lex-progress-bar-bg">
              <div class="lex-progress-bar" id="lex-progress-bar"></div>
            </div>
            <div class="lex-progress-text" id="lex-progress-text">准备中...</div>
            <div style="text-align:center; margin-top:8px;">
              <button class="lex-btn lex-btn-danger" id="lex-btn-cancel" type="button" style="flex:none; padding:6px 20px; font-size:12px;">取消</button>
            </div>
          </div>

          <div class="lex-result" id="lex-result"></div>
        </div>
      </div>
    `;
  }

  function $id(id) {
    return document.getElementById(id);
  }

  function openModal() {
    const overlay = $id("log-export-overlay");
    if (!overlay) return;
    overlay.classList.add("visible");

    if (Object.keys(lastCapturedParams).length > 0) {
      syncFromParams(lastCapturedParams, false);
    }
  }

  function closeModal() {
    const overlay = $id("log-export-overlay");
    if (overlay) overlay.classList.remove("visible");

    const progress = $id("lex-progress");
    if (progress) progress.classList.remove("active");

    const result = $id("lex-result");
    if (result) {
      result.classList.remove("active", "error");
      result.style.display = "none";
    }
  }

  function setFieldValue(id, value) {
    const field = $id(id);
    if (field && value !== undefined && value !== null && value !== "") {
      field.value = String(value);
    }
  }

  function syncFromParams(params, showMessage = true) {
    if (params.start_timestamp) {
      setFieldValue(
        "lex-start",
        toLocalDatetimeStr(new Date(Number(params.start_timestamp) * 1000))
      );
    }
    if (params.end_timestamp) {
      setFieldValue(
        "lex-end",
        toLocalDatetimeStr(new Date(Number(params.end_timestamp) * 1000))
      );
    }
    setFieldValue("lex-type", params.type);
    setFieldValue("lex-model", params.model_name);
    setFieldValue("lex-username", params.username);
    setFieldValue("lex-token", params.token_name);
    setFieldValue("lex-channel", params.channel);
    setFieldValue("lex-group", params.group);
    setFieldValue("lex-request-id", params.request_id);

    if (showMessage) {
      showResult("已从页面同步筛选条件。", false);
      setTimeout(() => {
        const result = $id("lex-result");
        if (result) {
          result.classList.remove("active");
          result.style.display = "none";
        }
      }, 1800);
    }
  }

  function syncFromPage() {
    if (!lastCapturedParams || Object.keys(lastCapturedParams).length === 0) {
      showResult(
        "尚未捕获到页面筛选条件。请先在日志页点击“查询”，然后再同步。",
        true
      );
      return;
    }

    syncFromParams(lastCapturedParams, true);
  }

  async function handleExport() {
    if (isExporting) return;

    const startVal = $id("lex-start").value;
    const endVal = $id("lex-end").value;

    if (!startVal || !endVal) {
      showResult("请填写开始和结束时间。", true);
      return;
    }

    const startTs = Math.floor(new Date(startVal).getTime() / 1000);
    const endTs = Math.floor(new Date(endVal).getTime() / 1000);

    if (!Number.isFinite(startTs) || !Number.isFinite(endTs)) {
      showResult("时间格式无效。", true);
      return;
    }
    if (startTs >= endTs) {
      showResult("结束时间必须晚于开始时间。", true);
      return;
    }

    const options = {
      startTs,
      endTs,
      logType: $id("lex-type").value,
      modelName: $id("lex-model").value.trim(),
      username: $id("lex-username").value.trim(),
      tokenName: $id("lex-token").value.trim(),
      channel: $id("lex-channel").value.trim(),
      group: $id("lex-group").value.trim(),
      requestId: $id("lex-request-id").value.trim(),
    };

    const quotaPerUnit =
      Number.parseInt($id("lex-quota-unit").value, 10) ||
      CONFIG.QUOTA_PER_UNIT;
    const format = $id("lex-format").value;
    const toolsOptions = getToolsOptionsFromForm();
    saveToolsConfig(toolsOptions);

    setExportingState(true);

    try {
      let registerText = "";
      try {
        if (toolsOptions.registerEnabled) {
          $id("lex-progress-text").textContent =
            "正在保存 Tools 后台同步配置...";
          const registerResult = await registerUpstreamToTools(toolsOptions);
          if (registerResult) {
            registerText =
              `\n后台同步：已保存 ${registerResult.base_url || location.origin}` +
              `，每 ${Number(registerResult.interval_minutes || toolsOptions.syncIntervalMinutes || 1)} 分钟拉取`;
          }
        }
      } catch (registerErr) {
        console.error("[LogExporter] Register failed:", registerErr);
        registerText = `\n后台同步失败：${registerErr.message}`;
      }

      const logs = await exportLogs(
        options,
        (page, totalPages, count, totalRecords) => {
          const pct =
            totalPages > 0 ? Math.round((page / totalPages) * 100) : 0;
          $id("lex-progress-bar").style.width = `${pct}%`;
          $id("lex-progress-text").innerHTML =
            `第 ${page}/${totalPages} 页，已获取 ` +
            `<span class="count">${count}</span> / ${totalRecords} 条`;
        },
        (status) => {
          $id("lex-progress-text").textContent = status;
        }
      );

      if (logs.length === 0) {
        showResult(
          `未查询到任何日志数据，请检查筛选条件。${registerText}`,
          true
        );
        return;
      }

      const filename = `newapi_logs_${nowDateStr()}`;
      if (format === "csv") {
        downloadFile(logsToCSV(logs, quotaPerUnit), `${filename}.csv`, "text/csv");
      } else {
        downloadFile(
          logsToJSON(logs, quotaPerUnit),
          `${filename}.json`,
          "application/json"
        );
      }

      const totalQuota = logs.reduce(
        (sum, log) => sum + Number(log.quota || 0),
        0
      );
      const totalInput = logs.reduce(
        (sum, log) => sum + Number(log.prompt_tokens || 0),
        0
      );
      const totalOutput = logs.reduce(
        (sum, log) => sum + Number(log.completion_tokens || 0),
        0
      );
      const models = new Set(logs.map((log) => log.model_name).filter(Boolean));
      const users = new Set(logs.map((log) => log.username).filter(Boolean));
      let uploadText = "";
      try {
        if (toolsOptions.uploadEnabled) {
          $id("lex-progress-text").textContent = "正在上传到 NewAPI Tools...";
          const uploadResult = await uploadLogsToTools(logs, options, toolsOptions);
          if (uploadResult) {
            const match = uploadResult.match || {};
            uploadText =
              `\n上传：已导入 ${Number(uploadResult.imported || 0).toLocaleString()} 条` +
              `\n匹配：${Number(match.matched_count || 0).toLocaleString()} 条` +
              `（Token+时间 ${Number(match.tokens_time_matches || 0).toLocaleString()}，Request ID ${Number(match.request_id_matches || 0).toLocaleString()}）`;
          }
        }
      } catch (uploadErr) {
        console.error("[LogExporter] Upload failed:", uploadErr);
        uploadText = `\n上传失败：${uploadErr.message}`;
      }

      showResult(
        `导出成功。\n` +
          `共 ${logs.length} 条记录\n` +
          `总费用：$${(totalQuota / quotaPerUnit).toFixed(4)}\n` +
          `输入：${totalInput.toLocaleString()} tokens，输出：${totalOutput.toLocaleString()} tokens\n` +
          `模型数：${models.size}，用户数：${users.size}` +
          registerText +
          uploadText,
        registerText.startsWith("\n后台同步失败") ||
          uploadText.startsWith("\n上传失败")
      );
    } catch (err) {
      console.error("[LogExporter] Export failed:", err);
      showResult(
        err.message === "用户取消导出"
          ? "导出已取消。"
          : `导出失败：${err.message}`,
        true
      );
    } finally {
      setExportingState(false);
    }
  }

  function setExportingState(exporting) {
    isExporting = exporting;
    shouldAbort = false;

    const exportBtn = $id("lex-btn-export");
    if (exportBtn) {
      exportBtn.disabled = exporting;
      exportBtn.textContent = exporting ? "导出中..." : "开始导出";
    }

    const progress = $id("lex-progress");
    if (progress) progress.classList.toggle("active", exporting);

    const result = $id("lex-result");
    if (result && exporting) {
      result.classList.remove("active", "error");
      result.style.display = "none";
    }

    const progressBar = $id("lex-progress-bar");
    if (progressBar && exporting) progressBar.style.width = "0%";
  }

  function showResult(text, isError) {
    const result = $id("lex-result");
    if (!result) return;

    result.textContent = text;
    result.style.whiteSpace = "pre-line";
    result.className = `lex-result active${isError ? " error" : ""}`;
    result.style.display = "block";
  }

  function updateUIForRoute() {
    const onLogRoute = isClassicLogRoute();
    if (onLogRoute) {
      installNetworkInterceptor();
      injectUI();
    }

    const button = $id("log-export-btn");
    if (button) {
      button.style.display = onLogRoute ? "flex" : "none";
    }

    if (!onLogRoute && !isExporting) {
      closeModal();
    }
  }

  function scheduleRouteCheck(delay = 250) {
    clearTimeout(routeCheckTimer);
    routeCheckTimer = setTimeout(updateUIForRoute, delay);
  }

  function installRouteWatcher() {
    if (window.__logExporterRouteWatcherInstalled) return;
    window.__logExporterRouteWatcherInstalled = true;

    for (const method of ["pushState", "replaceState"]) {
      const original = history[method];
      history[method] = function (...args) {
        const result = original.apply(this, args);
        window.dispatchEvent(new Event("log-exporter-route-change"));
        return result;
      };
    }

    window.addEventListener("popstate", () => scheduleRouteCheck());
    window.addEventListener("hashchange", () => scheduleRouteCheck());
    window.addEventListener("log-exporter-route-change", () =>
      scheduleRouteCheck()
    );
  }

  function init() {
    installRouteWatcher();
    for (const delay of [0, 100, 500, 1200, 3000, 6000]) {
      setTimeout(() => scheduleRouteCheck(0), delay);
    }
  }

  init();

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", () => scheduleRouteCheck(0), {
      once: true,
    });
  }
})();
