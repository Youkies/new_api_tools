import { Fragment, useCallback, useEffect, useMemo, useRef, useState, type ElementType } from 'react'
import { Activity, AlertTriangle, Calculator, CheckCircle2, ChevronDown, ChevronRight, Copy, KeyRound, Loader2, Plus, RefreshCw, Save, Settings2, Trash2 } from 'lucide-react'
import { useAuth } from '../contexts/AuthContext'
import { useToast } from './Toast'
import { Badge } from './ui/badge'
import { Button } from './ui/button'
import { Card, CardContent, CardHeader, CardTitle } from './ui/card'
import { Input } from './ui/input'
import { Select } from './ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from './ui/table'
import { cn } from '../lib/utils'

type BillingMode = 'token' | 'request'

interface CostRule {
  id?: number
  channel_id: number
  model_name: string
  upstream_model: string
  billing_mode: BillingMode
  input_cost_per_million: number
  output_cost_per_million: number
  request_cost: number
  cost_multiplier: number
  enabled: boolean
  updated_at?: number
}

interface ChannelOption {
  id: number
  name: string
  type: number
  status: number
  priority?: number
}

interface CostModelRow {
  channel_id: number
  channel_name: string
  model_name: string
  upstream_model: string
  billing_mode: BillingMode
  request_count: number
  quota_used: number
  prompt_tokens: number
  completion_tokens: number
  billed_amount: number
  estimated_cost: number
  rule_estimated_cost?: number
  upstream_imported_cost?: number
  upstream_matched_requests?: number
  upstream_request_id_matches?: number
  upstream_tokens_time_matches?: number
  gross_margin: number
  margin_rate: number
  cost_multiplier: number
  configured: boolean
  rule_id: number
}

interface CostChannelRow {
  channel_id: number
  channel_name: string
  request_count: number
  quota_used: number
  prompt_tokens: number
  completion_tokens: number
  billed_amount: number
  estimated_cost: number
  rule_estimated_cost?: number
  upstream_imported_cost?: number
  upstream_matched_requests?: number
  upstream_request_id_matches?: number
  upstream_tokens_time_matches?: number
  gross_margin: number
  margin_rate: number
  configured_models: number
  unconfigured_models: number
  models: CostModelRow[]
}

interface CostSummary {
  request_count: number
  quota_used: number
  prompt_tokens: number
  completion_tokens: number
  billed_amount: number
  estimated_cost: number
  rule_estimated_cost?: number
  upstream_imported_cost?: number
  upstream_matched_requests?: number
  upstream_request_id_matches?: number
  upstream_tokens_time_matches?: number
  upstream_unmatched_cost?: number
  gross_margin: number
  margin_rate: number
  configured_models: number
  unconfigured_models: number
}

interface UpstreamImportSummary {
  available: boolean
  request_count: number
  matched_request_count: number
  unmatched_request_count: number
  request_id_matches: number
  tokens_time_matches: number
  quota_used: number
  cost: number
}

interface CostSummaryPayload {
  range: {
    start_time: number
    end_time: number
  }
  summary: CostSummary
  channels: CostChannelRow[]
  rules: CostRule[]
  upstream_import?: UpstreamImportSummary
}

interface UpstreamSyncConfig {
  enabled: boolean
  base_url: string
  endpoint: string
  auth_token: string
  auth_token_set: boolean
  clear_auth_token?: boolean
  user_id: string
  page_size: number
  request_delay_ms: number
  interval_minutes: number
  lookback_minutes: number
  overlap_minutes: number
  match_tolerance_seconds: number
  log_type: number
  max_pages_per_run: number
  last_sync_at: number
  last_success_at: number
  last_error: string
  total_imported: number
  updated_at: number
}

interface ToolsAccessConfig {
  tools_url: string
  api_key: string
  source: string
  config_path: string
  updated_at: number
}

const moneyFormatter = new Intl.NumberFormat('en-US', {
  style: 'currency',
  currency: 'USD',
  minimumFractionDigits: 2,
  maximumFractionDigits: 6,
})

const compactFormatter = new Intl.NumberFormat('zh-CN', {
  notation: 'compact',
  maximumFractionDigits: 2,
})

function toLocalInputValue(date: Date) {
  const pad = (value: number) => value.toString().padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function defaultStartValue() {
  const start = new Date()
  start.setHours(0, 0, 0, 0)
  return toLocalInputValue(start)
}

function defaultEndValue() {
  return toLocalInputValue(new Date())
}

function toUnixSeconds(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return 0
  return Math.floor(date.getTime() / 1000)
}

function formatMoney(value: number) {
  return moneyFormatter.format(Number(value || 0))
}

function formatNumber(value: number) {
  return Number(value || 0).toLocaleString('zh-CN')
}

function formatTokens(value: number) {
  return compactFormatter.format(Number(value || 0))
}

function formatUnixTime(value: number) {
  if (!value) return '-'
  return new Date(Number(value) * 1000).toLocaleString('zh-CN')
}

const defaultUpstreamSyncConfig: UpstreamSyncConfig = {
  enabled: false,
  base_url: '',
  endpoint: 'auto',
  auth_token: '',
  auth_token_set: false,
  clear_auth_token: false,
  user_id: '',
  page_size: 100,
  request_delay_ms: 80,
  interval_minutes: 0,
  lookback_minutes: 60,
  overlap_minutes: 10,
  match_tolerance_seconds: 60,
  log_type: 2,
  max_pages_per_run: 1000,
  last_sync_at: 0,
  last_success_at: 0,
  last_error: '',
  total_imported: 0,
  updated_at: 0,
}

const defaultToolsAccessConfig: ToolsAccessConfig = {
  tools_url: '',
  api_key: '',
  source: 'missing',
  config_path: '',
  updated_at: 0,
}

function createEmptyRule(channelId: number): CostRule {
  return {
    channel_id: channelId,
    model_name: '*',
    upstream_model: '*',
    billing_mode: 'token',
    input_cost_per_million: 0,
    output_cost_per_million: 0,
    request_cost: 0,
    cost_multiplier: 1,
    enabled: true,
  }
}

export function CostAccounting() {
  const { token } = useAuth()
  const { showToast } = useToast()
  const apiUrl = import.meta.env.VITE_API_URL || ''

  const [startTime, setStartTime] = useState(defaultStartValue)
  const [endTime, setEndTime] = useState(defaultEndValue)
  const [channelFilter, setChannelFilter] = useState('all')
  const [summary, setSummary] = useState<CostSummaryPayload | null>(null)
  const [channels, setChannels] = useState<ChannelOption[]>([])
  const [rules, setRules] = useState<CostRule[]>([])
  const [draftRules, setDraftRules] = useState<CostRule[]>([])
  const [expandedChannels, setExpandedChannels] = useState<Record<number, boolean>>({})
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [saving, setSaving] = useState(false)
  const [rulesDirty, setRulesDirty] = useState(false)
  const [upstreamConfig, setUpstreamConfig] = useState<UpstreamSyncConfig>(defaultUpstreamSyncConfig)
  const [toolsAccess, setToolsAccess] = useState<ToolsAccessConfig>(defaultToolsAccessConfig)
  const [savingUpstream, setSavingUpstream] = useState(false)
  const [savingToolsAccess, setSavingToolsAccess] = useState(false)
  const [syncingUpstream, setSyncingUpstream] = useState(false)
  const rulesDirtyRef = useRef(false)

  const browserToolsUrl = useMemo(() => window.location.origin.replace(/\/+$/, ''), [])

  const getAuthHeaders = useCallback(() => ({
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${token}`,
  }), [token])

  useEffect(() => {
    rulesDirtyRef.current = rulesDirty
  }, [rulesDirty])

  const fetchRules = useCallback(async () => {
    const response = await fetch(`${apiUrl}/api/cost/rules`, { headers: getAuthHeaders() })
    const data = await response.json()
    if (!data.success) throw new Error(data.error?.message || '加载成本规则失败')

    const nextRules = data.data?.rules || []
    setRules(nextRules)
    setChannels(data.data?.channels || [])
    if (!rulesDirtyRef.current) {
      setDraftRules(nextRules)
    }
  }, [apiUrl, getAuthHeaders])

  const fetchSummary = useCallback(async () => {
    const start = toUnixSeconds(startTime)
    const end = toUnixSeconds(endTime)
    if (!start || !end || end < start) {
      showToast('error', '时间范围不正确')
      return
    }

    const params = new URLSearchParams({
      start_time: String(start),
      end_time: String(end),
    })
    if (channelFilter !== 'all') {
      params.set('channel_id', channelFilter)
    }

    const response = await fetch(`${apiUrl}/api/cost/summary?${params.toString()}`, { headers: getAuthHeaders() })
    const data = await response.json()
    if (!data.success) throw new Error(data.error?.message || '加载成本核算失败')
    setSummary(data.data)
  }, [apiUrl, channelFilter, endTime, getAuthHeaders, showToast, startTime])

  const fetchUpstreamConfig = useCallback(async () => {
    const response = await fetch(`${apiUrl}/api/cost/upstream-sync/config`, { headers: getAuthHeaders() })
    const data = await response.json()
    if (!data.success) throw new Error(data.error?.message || '加载上游同步配置失败')
    setUpstreamConfig({ ...defaultUpstreamSyncConfig, ...(data.data || {}), auth_token: '', clear_auth_token: false })
  }, [apiUrl, getAuthHeaders])

  const fetchToolsAccess = useCallback(async () => {
    const response = await fetch(`${apiUrl}/api/cost/tools-access`, { headers: getAuthHeaders() })
    const data = await response.json()
    if (!data.success) throw new Error(data.error?.message || '加载脚本接入信息失败')
    setToolsAccess({ ...defaultToolsAccessConfig, ...(data.data || {}), tools_url: browserToolsUrl || data.data?.tools_url || '' })
  }, [apiUrl, browserToolsUrl, getAuthHeaders])

  const loadAll = useCallback(async (isRefresh = false) => {
    if (isRefresh) setRefreshing(true)
    else setLoading(true)

    try {
      await fetchRules()
      await fetchSummary()
      await fetchUpstreamConfig()
      await fetchToolsAccess()
    } catch (error) {
      console.error('Failed to load cost accounting:', error)
      showToast('error', error instanceof Error ? error.message : '加载成本核算失败')
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }, [fetchRules, fetchSummary, fetchToolsAccess, fetchUpstreamConfig, showToast])

  useEffect(() => {
    loadAll(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const firstChannelId = useMemo(() => {
    if (channelFilter !== 'all') return Number(channelFilter)
    return Number(channels[0]?.id || 0)
  }, [channelFilter, channels])

  const updateDraftRule = (index: number, patch: Partial<CostRule>) => {
    setDraftRules(prev => prev.map((rule, i) => i === index ? { ...rule, ...patch } : rule))
    setRulesDirty(true)
  }

  const addRule = (rule?: Partial<CostRule>) => {
    setDraftRules(prev => [
      ...prev,
      { ...createEmptyRule(firstChannelId), ...rule },
    ])
    setRulesDirty(true)
  }

  const removeRule = (index: number) => {
    setDraftRules(prev => prev.filter((_, i) => i !== index))
    setRulesDirty(true)
  }

  const createRuleFromModel = (model: CostModelRow) => {
    const exists = draftRules.some(rule =>
      Number(rule.channel_id) === Number(model.channel_id) && rule.model_name === model.model_name
    )
    if (exists) {
      showToast('info', '这条模型规则已经在草稿里')
      return
    }

    const related = draftRules.find(rule =>
      Number(rule.channel_id) === Number(model.channel_id)
      && rule.upstream_model
      && rule.upstream_model === model.upstream_model
      && (rule.input_cost_per_million > 0 || rule.output_cost_per_million > 0 || rule.request_cost > 0)
    )

    addRule({
      channel_id: model.channel_id,
      model_name: model.model_name,
      upstream_model: model.upstream_model || model.model_name,
      billing_mode: related?.billing_mode || 'token',
      input_cost_per_million: related?.input_cost_per_million || 0,
      output_cost_per_million: related?.output_cost_per_million || 0,
      request_cost: related?.request_cost || 0,
      cost_multiplier: related?.cost_multiplier || model.cost_multiplier || 1,
      enabled: true,
    })
    showToast('success', '已添加到成本规则草稿')
  }

  const saveRules = async () => {
    setSaving(true)
    try {
      const cleaned = draftRules
        .filter(rule => Number(rule.channel_id) > 0)
        .map(rule => ({
          ...rule,
          channel_id: Number(rule.channel_id),
          model_name: (rule.model_name || '*').trim() || '*',
          upstream_model: (rule.upstream_model || rule.model_name || '*').trim(),
          input_cost_per_million: Number(rule.input_cost_per_million || 0),
          output_cost_per_million: Number(rule.output_cost_per_million || 0),
          request_cost: Number(rule.request_cost || 0),
          cost_multiplier: Number(rule.cost_multiplier || 1),
        }))

      const response = await fetch(`${apiUrl}/api/cost/rules`, {
        method: 'POST',
        headers: getAuthHeaders(),
        body: JSON.stringify({ rules: cleaned }),
      })
      const data = await response.json()
      if (!data.success) throw new Error(data.error?.message || '保存成本规则失败')

      setRules(data.data?.rules || [])
      setDraftRules(data.data?.rules || [])
      setChannels(data.data?.channels || channels)
      setRulesDirty(false)
      showToast('success', '成本规则已保存')
      await fetchSummary()
    } catch (error) {
      console.error('Failed to save cost rules:', error)
      showToast('error', error instanceof Error ? error.message : '保存成本规则失败')
    } finally {
      setSaving(false)
    }
  }

  const updateUpstreamConfig = (patch: Partial<UpstreamSyncConfig>) => {
    setUpstreamConfig(prev => ({ ...prev, ...patch }))
  }

  const saveUpstreamConfig = async () => {
    setSavingUpstream(true)
    try {
      const payload = {
        ...upstreamConfig,
        page_size: Number(upstreamConfig.page_size || 100),
        request_delay_ms: Number(upstreamConfig.request_delay_ms || 0),
        interval_minutes: Number(upstreamConfig.interval_minutes || 0),
        lookback_minutes: Number(upstreamConfig.lookback_minutes || 60),
        overlap_minutes: Number(upstreamConfig.overlap_minutes || 0),
        match_tolerance_seconds: Number(upstreamConfig.match_tolerance_seconds || 60),
        log_type: Number(upstreamConfig.log_type || 2),
        max_pages_per_run: Number(upstreamConfig.max_pages_per_run || 1000),
      }

      const response = await fetch(`${apiUrl}/api/cost/upstream-sync/config`, {
        method: 'POST',
        headers: getAuthHeaders(),
        body: JSON.stringify(payload),
      })
      const data = await response.json()
      if (!data.success) throw new Error(data.error?.message || '保存上游同步配置失败')

      setUpstreamConfig({ ...defaultUpstreamSyncConfig, ...(data.data || {}), auth_token: '', clear_auth_token: false })
      showToast('success', '上游同步配置已保存')
    } catch (error) {
      console.error('Failed to save upstream sync config:', error)
      showToast('error', error instanceof Error ? error.message : '保存上游同步配置失败')
    } finally {
      setSavingUpstream(false)
    }
  }

  const runUpstreamSync = async () => {
    const start = toUnixSeconds(startTime)
    const end = toUnixSeconds(endTime)
    if (!start || !end || end < start) {
      showToast('error', '时间范围不正确')
      return
    }

    setSyncingUpstream(true)
    try {
      const response = await fetch(`${apiUrl}/api/cost/upstream-sync/run`, {
        method: 'POST',
        headers: getAuthHeaders(),
        body: JSON.stringify({ start_time: start, end_time: end, type: Number(upstreamConfig.log_type || 2) }),
      })
      const data = await response.json()
      if (!data.success) throw new Error(data.error?.message || '上游日志同步失败')

      const match = data.data?.match || {}
      showToast('success', `上游日志已同步，匹配 ${formatNumber(Number(match.matched_count || 0))} 条`)
      await fetchUpstreamConfig()
      await fetchSummary()
    } catch (error) {
      console.error('Failed to sync upstream logs:', error)
      showToast('error', error instanceof Error ? error.message : '上游日志同步失败')
      await fetchUpstreamConfig().catch(() => undefined)
    } finally {
      setSyncingUpstream(false)
    }
  }

  const saveToolsAccess = async () => {
    const apiKey = toolsAccess.api_key.trim()
    if (apiKey.length < 8) {
      showToast('error', 'API Key 至少需要 8 个字符')
      return
    }

    setSavingToolsAccess(true)
    try {
      const response = await fetch(`${apiUrl}/api/cost/tools-access`, {
        method: 'POST',
        headers: getAuthHeaders(),
        body: JSON.stringify({ api_key: apiKey }),
      })
      const data = await response.json()
      if (!data.success) throw new Error(data.error?.message || '保存脚本 API Key 失败')

      setToolsAccess({ ...defaultToolsAccessConfig, ...(data.data || {}), tools_url: browserToolsUrl || data.data?.tools_url || '' })
      showToast('success', '脚本 API Key 已保存')
    } catch (error) {
      console.error('Failed to save tools access config:', error)
      showToast('error', error instanceof Error ? error.message : '保存脚本 API Key 失败')
    } finally {
      setSavingToolsAccess(false)
    }
  }

  const copyText = async (text: string, label: string) => {
    if (!text) {
      showToast('error', `${label} 为空`)
      return
    }
    try {
      await navigator.clipboard.writeText(text)
      showToast('success', `${label} 已复制`)
    } catch {
      showToast('error', `${label} 复制失败`)
    }
  }

  const resetDraftRules = () => {
    setDraftRules(rules)
    setRulesDirty(false)
  }

  const toggleChannel = (channelId: number) => {
    setExpandedChannels(prev => ({ ...prev, [channelId]: !prev[channelId] }))
  }

  if (loading) {
    return (
      <div className="flex justify-center items-center py-40">
        <Loader2 className="h-12 w-12 animate-spin text-primary" />
      </div>
    )
  }

  const totals = summary?.summary
  const channelRows = summary?.channels || []
  const upstreamImport = summary?.upstream_import
  const upstreamMatchRate = upstreamImport?.request_count
    ? (Number(upstreamImport.matched_request_count || 0) / Number(upstreamImport.request_count || 1)) * 100
    : 0
  const toolsAccessSourceLabel = toolsAccess.source === 'file'
    ? '持久化配置'
    : toolsAccess.source === 'env'
      ? '环境变量'
      : '未配置'

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
        <div>
          <h2 className="text-3xl font-bold tracking-tight flex items-center gap-2">
            <Calculator className="h-7 w-7 text-primary" />
            成本核算
          </h2>
          <p className="text-sm text-muted-foreground mt-1">
            {new Date(toUnixSeconds(startTime) * 1000).toLocaleString('zh-CN')} - {new Date(toUnixSeconds(endTime) * 1000).toLocaleString('zh-CN')}
          </p>
        </div>

        <div className="flex flex-wrap items-end gap-3">
          <div className="w-full sm:w-52">
            <label className="text-xs text-muted-foreground">开始时间</label>
            <Input type="datetime-local" value={startTime} onChange={e => setStartTime(e.target.value)} className="mt-1" />
          </div>
          <div className="w-full sm:w-52">
            <label className="text-xs text-muted-foreground">结束时间</label>
            <Input type="datetime-local" value={endTime} onChange={e => setEndTime(e.target.value)} className="mt-1" />
          </div>
          <div className="w-full sm:w-56">
            <label className="text-xs text-muted-foreground">渠道</label>
            <Select value={channelFilter} onChange={e => setChannelFilter(e.target.value)} className="mt-1">
              <option value="all">全部渠道</option>
              {channels.map(channel => (
                <option key={channel.id} value={String(channel.id)}>
                  {channel.name}
                </option>
              ))}
            </Select>
          </div>
          <Button onClick={() => loadAll(true)} disabled={refreshing}>
            {refreshing ? <Loader2 className="h-4 w-4 mr-2 animate-spin" /> : <RefreshCw className="h-4 w-4 mr-2" />}
            查询
          </Button>
        </div>
      </div>

      {totals && (
        <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
          <MetricCard title="上游成本" value={formatMoney(totals.estimated_cost)} icon={Calculator} color="text-rose-600 bg-rose-50 dark:bg-rose-950 dark:text-rose-300" />
          <MetricCard title="站内计费" value={formatMoney(totals.billed_amount)} icon={Activity} color="text-emerald-600 bg-emerald-50 dark:bg-emerald-950 dark:text-emerald-300" />
          <MetricCard title="毛利估算" value={formatMoney(totals.gross_margin)} subValue={`${totals.margin_rate.toFixed(2)}%`} icon={CheckCircle2} color="text-blue-600 bg-blue-50 dark:bg-blue-950 dark:text-blue-300" />
          <MetricCard title="请求数量" value={formatNumber(totals.request_count)} subValue={`${formatTokens(totals.prompt_tokens + totals.completion_tokens)} tokens`} icon={Activity} color="text-violet-600 bg-violet-50 dark:bg-violet-950 dark:text-violet-300" />
        </div>
      )}

      {totals && totals.unconfigured_models > 0 && (
        <div className="rounded-md border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200 flex items-center gap-2">
          <AlertTriangle className="h-4 w-4 shrink-0" />
          <span>{totals.unconfigured_models} 个渠道模型还没有成本规则，相关上游成本会按 0 计算。</span>
        </div>
      )}

      {totals && upstreamImport?.available && (
        <div className="rounded-md border bg-muted/40 px-4 py-3 text-sm flex flex-col gap-1 md:flex-row md:items-center md:justify-between">
          <div>
            <span className="font-medium">上游导入：</span>
            {formatNumber(upstreamImport.request_count)} 条 / {formatMoney(upstreamImport.cost)}
          </div>
          <div className="text-muted-foreground">
            已匹配 {formatNumber(upstreamImport.matched_request_count || 0)} 条（{upstreamMatchRate.toFixed(2)}%），
            Request ID {formatNumber(totals.upstream_request_id_matches || 0)} 条，
            Token+时间 {formatNumber(totals.upstream_tokens_time_matches || 0)} 条，
            未匹配 {formatMoney(totals.upstream_unmatched_cost || 0)}
          </div>
        </div>
      )}

      <Card>
        <CardHeader className="pb-3">
          <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <CardTitle className="text-lg flex items-center gap-2">
              <KeyRound className="h-5 w-5 text-primary" />
              日志助手接入
            </CardTitle>
            <Button size="sm" onClick={saveToolsAccess} disabled={savingToolsAccess}>
              {savingToolsAccess ? <Loader2 className="h-4 w-4 mr-2 animate-spin" /> : <Save className="h-4 w-4 mr-2" />}
              保存 API Key
            </Button>
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
            <div>
              <label className="text-xs text-muted-foreground">NewAPI Tools 地址</label>
              <div className="mt-1 flex gap-2">
                <Input value={toolsAccess.tools_url || browserToolsUrl} readOnly />
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  onClick={() => copyText(toolsAccess.tools_url || browserToolsUrl, 'Tools 地址')}
                  title="复制 Tools 地址"
                >
                  <Copy className="h-4 w-4" />
                </Button>
              </div>
            </div>
            <div>
              <label className="text-xs text-muted-foreground">Tools API Key</label>
              <div className="mt-1 flex gap-2">
                <Input
                  value={toolsAccess.api_key}
                  onChange={e => setToolsAccess(prev => ({ ...prev, api_key: e.target.value }))}
                  placeholder="填写脚本使用的 X-API-Key"
                />
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  onClick={() => copyText(toolsAccess.api_key, 'API Key')}
                  title="复制 API Key"
                >
                  <Copy className="h-4 w-4" />
                </Button>
              </div>
            </div>
          </div>
          <div className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
            <span>来源：{toolsAccessSourceLabel}</span>
            <span>配置文件：{toolsAccess.config_path || '/app/data/tools_auth.json'}</span>
            {toolsAccess.updated_at > 0 && <span>更新时间：{formatUnixTime(toolsAccess.updated_at)}</span>}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-3">
          <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <CardTitle className="text-lg flex items-center gap-2">
              <RefreshCw className="h-5 w-5 text-primary" />
              上游日志同步
            </CardTitle>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" size="sm" onClick={saveUpstreamConfig} disabled={savingUpstream || syncingUpstream}>
                {savingUpstream ? <Loader2 className="h-4 w-4 mr-2 animate-spin" /> : <Save className="h-4 w-4 mr-2" />}
                保存配置
              </Button>
              <Button size="sm" onClick={runUpstreamSync} disabled={syncingUpstream || savingUpstream}>
                {syncingUpstream ? <Loader2 className="h-4 w-4 mr-2 animate-spin" /> : <RefreshCw className="h-4 w-4 mr-2" />}
                手动同步
              </Button>
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
            <label className="flex h-10 items-center gap-2 rounded-md border px-3 text-sm">
              <input
                type="checkbox"
                checked={upstreamConfig.enabled}
                onChange={e => updateUpstreamConfig({ enabled: e.target.checked })}
                className="h-4 w-4 accent-primary"
              />
              启用定时同步
            </label>
            <div>
              <label className="text-xs text-muted-foreground">上游地址</label>
              <Input
                value={upstreamConfig.base_url}
                onChange={e => updateUpstreamConfig({ base_url: e.target.value })}
                placeholder="https://upstream.example.com"
                className="mt-1"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground">Endpoint</label>
              <Select
                value={upstreamConfig.endpoint}
                onChange={e => updateUpstreamConfig({ endpoint: e.target.value })}
                className="mt-1"
              >
                <option value="auto">auto</option>
                <option value="/api/log/">/api/log/</option>
                <option value="/api/log/self/">/api/log/self/</option>
              </Select>
            </div>
            <div>
              <label className="text-xs text-muted-foreground">Token</label>
              <Input
                type="password"
                value={upstreamConfig.auth_token}
                onChange={e => updateUpstreamConfig({ auth_token: e.target.value, clear_auth_token: false })}
                placeholder={upstreamConfig.auth_token_set ? '已保存，留空不变' : 'Bearer token'}
                className="mt-1"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground">User ID</label>
              <Input
                value={upstreamConfig.user_id}
                onChange={e => updateUpstreamConfig({ user_id: e.target.value })}
                placeholder="可选"
                className="mt-1"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground">同步间隔(分钟)</label>
              <Input
                type="number"
                min="0"
                value={upstreamConfig.interval_minutes}
                onChange={e => updateUpstreamConfig({ interval_minutes: Number(e.target.value) })}
                className="mt-1"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground">回看(分钟)</label>
              <Input
                type="number"
                min="1"
                value={upstreamConfig.lookback_minutes}
                onChange={e => updateUpstreamConfig({ lookback_minutes: Number(e.target.value) })}
                className="mt-1"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground">重叠(分钟)</label>
              <Input
                type="number"
                min="0"
                value={upstreamConfig.overlap_minutes}
                onChange={e => updateUpstreamConfig({ overlap_minutes: Number(e.target.value) })}
                className="mt-1"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground">匹配窗口(秒)</label>
              <Input
                type="number"
                min="1"
                max="3600"
                value={upstreamConfig.match_tolerance_seconds}
                onChange={e => updateUpstreamConfig({ match_tolerance_seconds: Number(e.target.value) })}
                className="mt-1"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground">Page Size</label>
              <Input
                type="number"
                min="1"
                max="1000"
                value={upstreamConfig.page_size}
                onChange={e => updateUpstreamConfig({ page_size: Number(e.target.value) })}
                className="mt-1"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground">请求延迟(ms)</label>
              <Input
                type="number"
                min="0"
                value={upstreamConfig.request_delay_ms}
                onChange={e => updateUpstreamConfig({ request_delay_ms: Number(e.target.value) })}
                className="mt-1"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground">最大页数</label>
              <Input
                type="number"
                min="1"
                value={upstreamConfig.max_pages_per_run}
                onChange={e => updateUpstreamConfig({ max_pages_per_run: Number(e.target.value) })}
                className="mt-1"
              />
            </div>
          </div>
          <div className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
            <span>最近同步：{formatUnixTime(upstreamConfig.last_sync_at)}</span>
            <span>最近成功：{formatUnixTime(upstreamConfig.last_success_at)}</span>
            <span>累计导入：{formatNumber(upstreamConfig.total_imported)}</span>
            {upstreamConfig.last_error && <span className="text-destructive">错误：{upstreamConfig.last_error}</span>}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-lg">渠道消耗</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-10" />
                  <TableHead>渠道</TableHead>
                  <TableHead className="text-right">请求</TableHead>
                  <TableHead className="text-right">站内计费</TableHead>
                  <TableHead className="text-right">上游成本</TableHead>
                  <TableHead className="text-right">毛利</TableHead>
                  <TableHead className="text-right">模型配置</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {channelRows.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={7} className="text-center py-12 text-muted-foreground">暂无数据</TableCell>
                  </TableRow>
                ) : channelRows.map(channel => (
                  <Fragment key={channel.channel_id}>
                    <TableRow key={channel.channel_id} className="cursor-pointer" onClick={() => toggleChannel(channel.channel_id)}>
                      <TableCell>
                        {expandedChannels[channel.channel_id] ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
                      </TableCell>
                      <TableCell>
                        <div className="font-medium">{channel.channel_name}</div>
                        <div className="text-xs text-muted-foreground">ID: {channel.channel_id}</div>
                      </TableCell>
                      <TableCell className="text-right">{formatNumber(channel.request_count)}</TableCell>
                      <TableCell className="text-right">{formatMoney(channel.billed_amount)}</TableCell>
                      <TableCell className="text-right font-medium">
                        {formatMoney(channel.estimated_cost)}
                        {(channel.upstream_imported_cost || channel.rule_estimated_cost) ? (
                          <div className="text-xs font-normal text-muted-foreground">
                            导入 {formatMoney(channel.upstream_imported_cost || 0)} / 规则 {formatMoney(channel.rule_estimated_cost || 0)}
                          </div>
                        ) : null}
                      </TableCell>
                      <TableCell className={cn("text-right", channel.gross_margin >= 0 ? "text-emerald-600" : "text-destructive")}>
                        {formatMoney(channel.gross_margin)}
                        <div className="text-xs opacity-75">{channel.margin_rate.toFixed(2)}%</div>
                      </TableCell>
                      <TableCell className="text-right">
                        <Badge variant={channel.unconfigured_models > 0 ? 'warning' : 'success'}>
                          {channel.configured_models}/{channel.configured_models + channel.unconfigured_models}
                        </Badge>
                      </TableCell>
                    </TableRow>
                    {expandedChannels[channel.channel_id] && (
                      <TableRow>
                        <TableCell colSpan={7} className="bg-muted/30 p-0">
                          <div className="overflow-x-auto">
                            <table className="w-full text-sm">
                              <thead>
                                <tr className="border-b">
                                  <th className="text-left p-3 font-medium">模型</th>
                                  <th className="text-left p-3 font-medium">上游模型</th>
                                  <th className="text-right p-3 font-medium">请求</th>
                                  <th className="text-right p-3 font-medium">Token</th>
                                  <th className="text-right p-3 font-medium">倍率</th>
                                  <th className="text-right p-3 font-medium">成本</th>
                                  <th className="text-right p-3 font-medium">操作</th>
                                </tr>
                              </thead>
                              <tbody>
                                {channel.models.map(model => (
                                  <tr key={`${model.channel_id}-${model.model_name}`} className="border-b last:border-0">
                                    <td className="p-3 max-w-[260px] truncate" title={model.model_name}>{model.model_name}</td>
                                    <td className="p-3 max-w-[220px] truncate" title={model.upstream_model}>
                                      {model.upstream_model}
                                      <Badge variant={model.configured ? 'success' : 'warning'} className="ml-2">
                                        {model.configured ? '已配置' : '未配置'}
                                      </Badge>
                                    </td>
                                    <td className="p-3 text-right">{formatNumber(model.request_count)}</td>
                                    <td className="p-3 text-right">{formatTokens(model.prompt_tokens + model.completion_tokens)}</td>
                                    <td className="p-3 text-right">{model.configured ? `${Number(model.cost_multiplier || 1).toFixed(4)}x` : '-'}</td>
                                    <td className="p-3 text-right font-medium">
                                      {formatMoney(model.estimated_cost)}
                                      {(model.upstream_imported_cost || model.rule_estimated_cost) ? (
                                        <div className="text-xs font-normal text-muted-foreground">
                                          导入 {formatMoney(model.upstream_imported_cost || 0)} / 规则 {formatMoney(model.rule_estimated_cost || 0)}
                                        </div>
                                      ) : null}
                                    </td>
                                    <td className="p-3 text-right">
                                      <Button variant="outline" size="sm" onClick={(event) => { event.stopPropagation(); createRuleFromModel(model) }}>
                                        <Plus className="h-3.5 w-3.5 mr-1" />
                                        规则
                                      </Button>
                                    </td>
                                  </tr>
                                ))}
                              </tbody>
                            </table>
                          </div>
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                ))}
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-3">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <CardTitle className="text-lg flex items-center gap-2">
              <Settings2 className="h-5 w-5 text-primary" />
              成本规则
            </CardTitle>
            <div className="flex gap-2">
              <Button variant="outline" size="sm" onClick={() => addRule()} disabled={firstChannelId <= 0}>
                <Plus className="h-4 w-4 mr-2" />
                新增
              </Button>
              <Button variant="outline" size="sm" onClick={resetDraftRules} disabled={!rulesDirty || saving}>
                重置
              </Button>
              <Button size="sm" onClick={saveRules} disabled={saving}>
                {saving ? <Loader2 className="h-4 w-4 mr-2 animate-spin" /> : <Save className="h-4 w-4 mr-2" />}
                保存
              </Button>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="min-w-[180px]">渠道</TableHead>
                  <TableHead className="min-w-[180px]">站内模型</TableHead>
                  <TableHead className="min-w-[180px]">上游模型</TableHead>
                  <TableHead className="min-w-[120px]">计费</TableHead>
                  <TableHead className="min-w-[110px] text-right">倍率</TableHead>
                  <TableHead className="min-w-[120px] text-right">基础输入 $/1M</TableHead>
                  <TableHead className="min-w-[120px] text-right">基础输出 $/1M</TableHead>
                  <TableHead className="min-w-[120px] text-right">基础每次 $</TableHead>
                  <TableHead className="w-24 text-center">启用</TableHead>
                  <TableHead className="w-20" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {draftRules.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={10} className="text-center py-12 text-muted-foreground">暂无规则</TableCell>
                  </TableRow>
                ) : draftRules.map((rule, index) => (
                  <TableRow key={`${rule.channel_id}-${rule.model_name}-${index}`}>
                    <TableCell>
                      <Select
                        value={String(rule.channel_id)}
                        onChange={e => updateDraftRule(index, { channel_id: Number(e.target.value) })}
                      >
                        {channels.map(channel => (
                          <option key={channel.id} value={String(channel.id)}>{channel.name}</option>
                        ))}
                      </Select>
                    </TableCell>
                    <TableCell>
                      <Input
                        value={rule.model_name}
                        onChange={e => updateDraftRule(index, { model_name: e.target.value })}
                        placeholder="*"
                      />
                    </TableCell>
                    <TableCell>
                      <Input
                        value={rule.upstream_model}
                        onChange={e => updateDraftRule(index, { upstream_model: e.target.value })}
                        placeholder={rule.model_name || '*'}
                      />
                    </TableCell>
                    <TableCell>
                      <Select
                        value={rule.billing_mode}
                        onChange={e => updateDraftRule(index, { billing_mode: e.target.value as BillingMode })}
                      >
                        <option value="token">按量</option>
                        <option value="request">按次</option>
                      </Select>
                    </TableCell>
                    <TableCell>
                      <Input
                        type="number"
                        min="0"
                        step="0.000001"
                        value={rule.cost_multiplier ?? 1}
                        onChange={e => updateDraftRule(index, { cost_multiplier: Number(e.target.value) })}
                        className="text-right"
                      />
                    </TableCell>
                    <TableCell>
                      <Input
                        type="number"
                        min="0"
                        step="0.000001"
                        value={rule.input_cost_per_million}
                        onChange={e => updateDraftRule(index, { input_cost_per_million: Number(e.target.value) })}
                        className="text-right"
                        disabled={rule.billing_mode === 'request'}
                      />
                    </TableCell>
                    <TableCell>
                      <Input
                        type="number"
                        min="0"
                        step="0.000001"
                        value={rule.output_cost_per_million}
                        onChange={e => updateDraftRule(index, { output_cost_per_million: Number(e.target.value) })}
                        className="text-right"
                        disabled={rule.billing_mode === 'request'}
                      />
                    </TableCell>
                    <TableCell>
                      <Input
                        type="number"
                        min="0"
                        step="0.000001"
                        value={rule.request_cost}
                        onChange={e => updateDraftRule(index, { request_cost: Number(e.target.value) })}
                        className="text-right"
                        disabled={rule.billing_mode === 'token'}
                      />
                    </TableCell>
                    <TableCell className="text-center">
                      <input
                        type="checkbox"
                        checked={rule.enabled}
                        onChange={e => updateDraftRule(index, { enabled: e.target.checked })}
                        className="h-4 w-4 accent-primary"
                      />
                    </TableCell>
                    <TableCell className="text-right">
                      <Button variant="ghost" size="icon" onClick={() => removeRule(index)}>
                        <Trash2 className="h-4 w-4 text-destructive" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          <div className="mt-3 text-xs text-muted-foreground">
            `*` 表示该渠道的默认规则；实际成本按基础价格乘倍率计算，例如输入 5 $/1M、倍率 0.35 会按 1.75 $/1M 计入成本。
          </div>
        </CardContent>
      </Card>
    </div>
  )
}

interface MetricCardProps {
  title: string
  value: string
  subValue?: string
  icon: ElementType
  color: string
}

function MetricCard({ title, value, subValue, icon: Icon, color }: MetricCardProps) {
  return (
    <Card className="overflow-hidden">
      <CardContent className="p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="text-xs text-muted-foreground">{title}</p>
            <div className="mt-1 text-xl font-semibold tracking-tight truncate" title={value}>
              {value}
            </div>
            {subValue && <p className="mt-1 text-xs text-muted-foreground">{subValue}</p>}
          </div>
          <div className={cn("h-9 w-9 rounded-md flex items-center justify-center shrink-0", color)}>
            <Icon className="h-4 w-4" />
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
