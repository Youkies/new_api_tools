import { useCallback, useEffect, useMemo, useState } from 'react'
import { AlertTriangle, CheckCircle2, Copy, Database, FileSearch, FileText, Loader2, RefreshCw, Search, Trash2, Upload, XCircle } from 'lucide-react'
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

type MatchStatus = 'matched' | 'ambiguous' | 'unmatched'

interface ByHostSummary {
  local_rows: number
  upstream_rows: number
  matched_rows: number
  ambiguous_rows: number
  unmatched_rows: number
  local_revenue: number
  upstream_cost: number
  gross: number
  matched_local_revenue: number
  matched_upstream_cost: number
  matched_gross: number
  per_call_rows: number
  per_call_current_avg: number
  per_call_break_even_price: number
}

interface LogMatchSummary {
  local_host: string
  local_rows: number
  upstream_rows: number
  matched_rows: number
  ambiguous_rows: number
  unmatched_rows: number
  unused_upstream_rows: number
  local_revenue: number
  upstream_cost: number
  gross: number
  matched_local_revenue: number
  matched_upstream_cost: number
  matched_gross: number
  by_host: Record<string, ByHostSummary>
}

interface LogMatchRecord {
  status: MatchStatus
  reason: string
  candidate_count: number
  time_diff_seconds?: number
  target_host: string
  upstream_host?: string
  local_time: string
  upstream_time?: string
  local_request_id: string
  upstream_request_id?: string
  local_model: string
  normalized_model: string
  upstream_model?: string
  local_channel: string
  local_group: string
  local_username: string
  local_token_name: string
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  local_revenue: number
  upstream_cost: number
  gross?: number
  is_per_call: boolean
}

interface UploadedFileInfo {
  name: string
  host: string
  rows: number
}

interface StoredUpload {
  id: string
  name: string
  host: string
  source_url?: string
  source_name?: string
  start_time?: number
  end_time?: number
  rows: number
  size: number
  uploaded_at: number
}

interface UploadKeyConfig {
  configured: boolean
  key: string
  masked_key: string
  updated_at: number
  data_dir: string
}

interface LogMatchResult {
  summary: LogMatchSummary
  records: LogMatchRecord[]
  uploaded_files: UploadedFileInfo[]
  generated_at: number
  time_window_seconds: number
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
  const date = new Date()
  date.setHours(0, 0, 0, 0)
  return toLocalInputValue(date)
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

function uniqueSorted(values: string[]) {
  return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean))).sort((a, b) => a.localeCompare(b))
}

function statusLabel(status: MatchStatus) {
  if (status === 'matched') return '已匹配'
  if (status === 'ambiguous') return '多候选'
  return '未匹配'
}

function statusBadge(status: MatchStatus) {
  if (status === 'matched') return 'success'
  if (status === 'ambiguous') return 'warning'
  return 'destructive'
}

function errorMessage(payload: any, fallback: string) {
  return payload?.error?.message || payload?.message || fallback
}

interface CheckGroupProps {
  title: string
  options: string[]
  selected: string[]
  onChange: (next: string[]) => void
}

function CheckGroup({ title, options, selected, onChange }: CheckGroupProps) {
  const selectedCount = selected.length

  const toggle = (value: string) => {
    if (selected.includes(value)) {
      onChange(selected.filter((item) => item !== value))
      return
    }
    onChange([...selected, value])
  }

  return (
    <div className="rounded-md border border-border/60 bg-muted/20 p-3">
      <div className="mb-2 flex items-center justify-between gap-2">
        <div className="text-xs font-semibold text-muted-foreground">{title}</div>
        <div className="flex items-center gap-2">
          <span className="text-[11px] text-muted-foreground">{selectedCount}/{options.length}</span>
          <button type="button" className="text-[11px] text-primary hover:underline" onClick={() => onChange(options)}>
            全选
          </button>
          <button type="button" className="text-[11px] text-muted-foreground hover:text-foreground" onClick={() => onChange([])}>
            清空
          </button>
        </div>
      </div>
      <div className="max-h-44 space-y-1 overflow-y-auto pr-1 custom-scrollbar">
        {options.length === 0 ? (
          <div className="py-4 text-center text-xs text-muted-foreground">暂无选项</div>
        ) : options.map((option) => (
          <label key={option} className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-xs hover:bg-muted/60">
            <input
              type="checkbox"
              className="h-3.5 w-3.5 rounded border-border accent-primary"
              checked={selected.includes(option)}
              onChange={() => toggle(option)}
            />
            <span className="truncate" title={option}>{option}</span>
          </label>
        ))}
      </div>
    </div>
  )
}

export function LogMatcher() {
  const { token } = useAuth()
  const { showToast } = useToast()
  const apiUrl = import.meta.env.VITE_API_URL || ''
  const toolsBaseUrl = useMemo(() => {
    const base = apiUrl || window.location.origin
    return base.replace(/\/+$/, '')
  }, [apiUrl])
  const uploadEndpoint = `${toolsBaseUrl}/api/log-match/uploads`

  const [startTime, setStartTime] = useState(defaultStartValue)
  const [endTime, setEndTime] = useState(defaultEndValue)
  const [timeWindow, setTimeWindow] = useState('120')
  const [maxRows, setMaxRows] = useState('50000')
  const [files, setFiles] = useState<File[]>([])
  const [storedUploads, setStoredUploads] = useState<StoredUpload[]>([])
  const [selectedUploadIds, setSelectedUploadIds] = useState<string[]>([])
  const [uploadsLoading, setUploadsLoading] = useState(false)
  const [result, setResult] = useState<LogMatchResult | null>(null)
  const [loading, setLoading] = useState(false)
  const [statusFilter, setStatusFilter] = useState<Record<MatchStatus, boolean>>({
    matched: true,
    ambiguous: true,
    unmatched: true,
  })
  const [hostFilter, setHostFilter] = useState('all')
  const [onlyPerCall, setOnlyPerCall] = useState(false)
  const [search, setSearch] = useState('')
  const [selectedChannels, setSelectedChannels] = useState<string[]>([])
  const [selectedModels, setSelectedModels] = useState<string[]>([])
  const [uploadKey, setUploadKey] = useState('')
  const [uploadKeyConfigured, setUploadKeyConfigured] = useState(false)
  const [uploadKeyUpdatedAt, setUploadKeyUpdatedAt] = useState(0)
  const [uploadKeyDataDir, setUploadKeyDataDir] = useState('')
  const [uploadKeyLoading, setUploadKeyLoading] = useState(false)
  const [uploadKeySaving, setUploadKeySaving] = useState(false)

  const channelOptions = useMemo(() => uniqueSorted(result?.records.map((record) => record.local_channel) || []), [result])
  const modelOptions = useMemo(() => uniqueSorted(result?.records.map((record) => record.local_model) || []), [result])
  const hostOptions = useMemo(() => Object.keys(result?.summary.by_host || {}).sort((a, b) => a.localeCompare(b)), [result])
  const selectedUploadCount = selectedUploadIds.length

  const filteredRecords = useMemo(() => {
    if (!result) return []
    const keyword = search.trim().toLowerCase()
    return result.records.filter((record) => {
      if (!statusFilter[record.status]) return false
      if (hostFilter !== 'all' && record.target_host !== hostFilter && record.upstream_host !== hostFilter) return false
      if (onlyPerCall && !record.is_per_call) return false
      if (selectedChannels.length > 0 && !selectedChannels.includes(record.local_channel)) return false
      if (selectedModels.length > 0 && !selectedModels.includes(record.local_model)) return false
      if (!keyword) return true
      return [
        record.local_request_id,
        record.upstream_request_id,
        record.local_model,
        record.upstream_model,
        record.normalized_model,
        record.local_channel,
        record.target_host,
        record.upstream_host,
        record.local_username,
        record.local_token_name,
        record.reason,
      ].some((value) => String(value || '').toLowerCase().includes(keyword))
    })
  }, [hostFilter, onlyPerCall, result, search, selectedChannels, selectedModels, statusFilter])

  const visibleRecords = filteredRecords.slice(0, 1000)
  const matchedRate = result && result.summary.local_rows > 0
    ? Math.round((result.summary.matched_rows / result.summary.local_rows) * 100)
    : 0

  const copyText = useCallback(async (value: string, label: string) => {
    if (!value) {
      showToast('error', `${label}为空`)
      return
    }
    try {
      await navigator.clipboard.writeText(value)
      showToast('success', `已复制${label}`)
    } catch (_) {
      showToast('error', `复制${label}失败`)
    }
  }, [showToast])

  const handleFileChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    setFiles(Array.from(event.target.files || []))
  }

  const applyUploadKeyConfig = (data: UploadKeyConfig) => {
    setUploadKey(data.key || '')
    setUploadKeyConfigured(Boolean(data.configured))
    setUploadKeyUpdatedAt(Number(data.updated_at || 0))
    setUploadKeyDataDir(data.data_dir || '')
  }

  const fetchUploadKey = useCallback(async () => {
    setUploadKeyLoading(true)
    try {
      const response = await fetch(`${apiUrl}/api/log-match/upload-key`, {
        headers: {
          Authorization: `Bearer ${token}`,
        },
      })
      const payload = await response.json().catch(() => null)
      if (!response.ok || !payload?.success) {
        throw new Error(errorMessage(payload, '加载上传专用 Key 失败'))
      }
      applyUploadKeyConfig(payload.data as UploadKeyConfig)
    } catch (error) {
      showToast('error', error instanceof Error ? error.message : '加载上传专用 Key 失败')
    } finally {
      setUploadKeyLoading(false)
    }
  }, [apiUrl, showToast, token])

  useEffect(() => {
    fetchUploadKey()
  }, [fetchUploadKey])

  const saveUploadKey = useCallback(async (generate = false) => {
    setUploadKeySaving(true)
    try {
      const response = await fetch(`${apiUrl}/api/log-match/upload-key`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify(generate ? { generate: true } : { key: uploadKey.trim() }),
      })
      const payload = await response.json().catch(() => null)
      if (!response.ok || !payload?.success) {
        throw new Error(errorMessage(payload, '保存上传专用 Key 失败'))
      }
      applyUploadKeyConfig(payload.data as UploadKeyConfig)
      showToast('success', generate ? '已生成上传专用 Key' : uploadKey.trim() ? '上传专用 Key 已保存' : '已关闭上传专用 Key')
    } catch (error) {
      showToast('error', error instanceof Error ? error.message : '保存上传专用 Key 失败')
    } finally {
      setUploadKeySaving(false)
    }
  }, [apiUrl, showToast, token, uploadKey])

  const fetchStoredUploads = useCallback(async () => {
    setUploadsLoading(true)
    try {
      const response = await fetch(`${apiUrl}/api/log-match/uploads`, {
        headers: {
          Authorization: `Bearer ${token}`,
        },
      })
      const payload = await response.json()
      if (!response.ok || !payload?.success) {
        throw new Error(errorMessage(payload, '加载已上传日志失败'))
      }
      const uploads = (payload.data?.uploads || []) as StoredUpload[]
      setStoredUploads(uploads)
      setSelectedUploadIds((current) => current.filter((id) => uploads.some((item) => item.id === id)))
    } catch (error) {
      showToast('error', error instanceof Error ? error.message : '加载已上传日志失败')
    } finally {
      setUploadsLoading(false)
    }
  }, [apiUrl, showToast, token])

  useEffect(() => {
    fetchStoredUploads()
  }, [fetchStoredUploads])

  const toggleStoredUpload = (id: string) => {
    setSelectedUploadIds((current) => {
      if (current.includes(id)) {
        return current.filter((item) => item !== id)
      }
      return [...current, id]
    })
  }

  const deleteStoredUpload = async (id: string) => {
    try {
      const response = await fetch(`${apiUrl}/api/log-match/uploads/${encodeURIComponent(id)}`, {
        method: 'DELETE',
        headers: {
          Authorization: `Bearer ${token}`,
        },
      })
      const payload = await response.json().catch(() => null)
      if (!response.ok || payload?.success === false) {
        throw new Error(errorMessage(payload, '删除已上传日志失败'))
      }
      setStoredUploads((current) => current.filter((item) => item.id !== id))
      setSelectedUploadIds((current) => current.filter((item) => item !== id))
      showToast('success', '已删除上传日志')
    } catch (error) {
      showToast('error', error instanceof Error ? error.message : '删除已上传日志失败')
    }
  }

  const handleAnalyze = async () => {
    const start = toUnixSeconds(startTime)
    const end = toUnixSeconds(endTime)
    if (!start || !end || end < start) {
      showToast('error', '时间范围不正确')
      return
    }
    if (files.length === 0 && selectedUploadIds.length === 0) {
      showToast('error', '请上传或选择至少一个上游 CSV')
      return
    }

    const form = new FormData()
    for (const file of files) {
      form.append('files', file)
    }
    for (const id of selectedUploadIds) {
      form.append('uploaded_ids', id)
    }
    form.append('start_time', String(start))
    form.append('end_time', String(end))
    form.append('time_window_seconds', timeWindow || '120')
    form.append('max_rows', maxRows || '50000')

    setLoading(true)
    try {
      const response = await fetch(`${apiUrl}/api/log-match/analyze`, {
        method: 'POST',
        headers: {
          Authorization: `Bearer ${token}`,
        },
        body: form,
      })
      const payload = await response.json().catch(() => null)
      if (!response.ok || !payload?.success) {
        throw new Error(errorMessage(payload, '日志对账失败'))
      }
      const nextResult = payload.data as LogMatchResult
      setResult(nextResult)
      setSelectedChannels(uniqueSorted(nextResult.records.map((record) => record.local_channel)))
      setSelectedModels(uniqueSorted(nextResult.records.map((record) => record.local_model)))
      setHostFilter('all')
      setStatusFilter({ matched: true, ambiguous: true, unmatched: true })
      setOnlyPerCall(false)
      showToast('success', `已分析 ${formatNumber(nextResult.summary.local_rows)} 条本站日志`)
    } catch (error) {
      showToast('error', error instanceof Error ? error.message : '日志对账失败')
    } finally {
      setLoading(false)
    }
  }

  const toggleStatus = (status: MatchStatus) => {
    setStatusFilter((current) => ({ ...current, [status]: !current[status] }))
  }

  return (
    <div className="space-y-5">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h2 className="text-2xl font-bold tracking-tight">日志对账</h2>
          <div className="mt-1 flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
            <span className="inline-flex items-center gap-1"><Database className="h-3.5 w-3.5" /> 本站日志来自数据库</span>
            <span className="inline-flex items-center gap-1"><Upload className="h-3.5 w-3.5" /> 上游日志手动上传或脚本上传</span>
          </div>
        </div>
        <Button onClick={handleAnalyze} disabled={loading || (files.length === 0 && selectedUploadIds.length === 0)} className="gap-2">
          {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : <FileSearch className="h-4 w-4" />}
          开始分析
        </Button>
      </div>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base">脚本上传接入</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="grid gap-3 lg:grid-cols-2">
            <div className="rounded-md border border-border/60 bg-muted/20 p-3">
              <div className="mb-2 text-xs font-medium text-muted-foreground">NewAPI Tools 地址</div>
              <div className="flex items-center gap-2">
                <code className="min-w-0 flex-1 break-all rounded bg-background px-2 py-1.5 text-xs">{toolsBaseUrl}</code>
                <Button variant="outline" size="icon" className="h-8 w-8 shrink-0" title="复制 Tools 地址" onClick={() => copyText(toolsBaseUrl, 'Tools 地址')}>
                  <Copy className="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
            <div className="rounded-md border border-border/60 bg-muted/20 p-3">
              <div className="mb-2 text-xs font-medium text-muted-foreground">上传接口</div>
              <div className="flex items-center gap-2">
                <code className="min-w-0 flex-1 break-all rounded bg-background px-2 py-1.5 text-xs">{uploadEndpoint}</code>
                <Button variant="outline" size="icon" className="h-8 w-8 shrink-0" title="复制上传接口" onClick={() => copyText(uploadEndpoint, '上传接口')}>
                  <Copy className="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
          </div>
          <div className="rounded-md border border-border/60 bg-muted/20 p-3">
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div>
                <div className="text-xs font-medium text-muted-foreground">上传专用 Key</div>
                <div className="mt-1 text-[11px] text-muted-foreground">
                  {uploadKeyConfigured ? `已配置${uploadKeyUpdatedAt ? `，更新于 ${new Date(uploadKeyUpdatedAt * 1000).toLocaleString('zh-CN')}` : ''}` : '未配置'}
                </div>
              </div>
              <Button variant="outline" size="sm" className="gap-2" onClick={fetchUploadKey} disabled={uploadKeyLoading || uploadKeySaving}>
                {uploadKeyLoading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
                刷新
              </Button>
            </div>
            <div className="grid gap-2 lg:grid-cols-[minmax(0,1fr)_auto]">
              <Input
                value={uploadKey}
                onChange={(event) => setUploadKey(event.target.value)}
                placeholder="点击生成或输入自定义上传 Key"
                autoComplete="off"
                spellCheck={false}
                disabled={uploadKeyLoading || uploadKeySaving}
              />
              <div className="flex flex-wrap gap-2">
                <Button variant="outline" size="sm" onClick={() => saveUploadKey(true)} disabled={uploadKeySaving}>
                  生成
                </Button>
                <Button variant="outline" size="sm" onClick={() => saveUploadKey(false)} disabled={uploadKeySaving}>
                  保存
                </Button>
                <Button variant="outline" size="sm" className="gap-2" onClick={() => copyText(uploadKey.trim(), '上传专用 Key')} disabled={!uploadKey.trim()}>
                  <Copy className="h-3.5 w-3.5" />
                  复制
                </Button>
              </div>
            </div>
            <div className="mt-2 text-xs text-muted-foreground">
              脚本里的 Tools API Key / Bearer JWT 填这个上传专用 Key。它只允许上传 CSV 到日志对账，持久化在后端 DATA_DIR{uploadKeyDataDir ? `（当前 ${uploadKeyDataDir}）` : ''}。
            </div>
          </div>
          <div className="text-xs text-muted-foreground">
            Zeabur 挂载目录使用 `/app/data` 时，将后端环境变量 `DATA_DIR` 设置为 `/app/data`，上传专用 Key 和脚本上传的 CSV 都会随挂载目录保留。
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base">分析输入</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4 xl:grid-cols-[1fr_1fr]">
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">开始时间</span>
              <Input type="datetime-local" value={startTime} onChange={(event) => setStartTime(event.target.value)} />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">结束时间</span>
              <Input type="datetime-local" value={endTime} onChange={(event) => setEndTime(event.target.value)} />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">时间窗口(秒)</span>
              <Input type="number" min="1" value={timeWindow} onChange={(event) => setTimeWindow(event.target.value)} />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">本站最大行数</span>
              <Input type="number" min="1" value={maxRows} onChange={(event) => setMaxRows(event.target.value)} />
            </label>
          </div>
          <div className="rounded-md border border-dashed border-border bg-muted/20 p-3">
            <label className="flex cursor-pointer flex-col items-center justify-center gap-2 rounded-md px-3 py-4 text-center hover:bg-muted/40">
              <Upload className="h-5 w-5 text-muted-foreground" />
              <span className="text-sm font-medium">选择上游 CSV 文件</span>
              <span className="text-xs text-muted-foreground">{files.length > 0 ? `${files.length} 个文件已选择` : '支持多选'}</span>
              <Input type="file" accept=".csv,text/csv" multiple className="hidden" onChange={handleFileChange} />
            </label>
            {files.length > 0 && (
              <div className="mt-3 flex flex-wrap gap-2">
                {files.map((file) => (
                  <Badge key={`${file.name}-${file.size}`} variant="secondary" className="max-w-full gap-1">
                    <FileText className="h-3 w-3 shrink-0" />
                    <span className="truncate">{file.name}</span>
                  </Badge>
                ))}
              </div>
            )}
          </div>
          <div className="rounded-md border border-border/60 bg-muted/20 p-3 xl:col-span-2">
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div>
                <div className="text-sm font-medium">已上传到 Tools 的上游日志</div>
                <div className="text-xs text-muted-foreground">可与本次手动选择的 CSV 一起分析</div>
              </div>
              <div className="flex items-center gap-2">
                <Button variant="outline" size="sm" className="gap-2" onClick={() => setSelectedUploadIds(storedUploads.map((item) => item.id))} disabled={storedUploads.length === 0}>
                  全选
                </Button>
                <Button variant="outline" size="sm" className="gap-2" onClick={() => setSelectedUploadIds([])} disabled={selectedUploadCount === 0}>
                  清空
                </Button>
                <Button variant="outline" size="sm" className="gap-2" onClick={fetchStoredUploads} disabled={uploadsLoading}>
                  {uploadsLoading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
                  刷新
                </Button>
              </div>
            </div>
            {storedUploads.length === 0 ? (
              <div className="rounded-md border border-dashed border-border/70 py-8 text-center text-sm text-muted-foreground">
                暂无脚本上传的上游日志
              </div>
            ) : (
              <div className="grid max-h-72 gap-2 overflow-y-auto pr-1 custom-scrollbar lg:grid-cols-2">
                {storedUploads.map((item) => (
                  <div key={item.id} className="flex items-start gap-3 rounded-md border border-border/60 bg-background/70 p-3">
                    <input
                      type="checkbox"
                      className="mt-1 h-4 w-4 accent-primary"
                      checked={selectedUploadIds.includes(item.id)}
                      onChange={() => toggleStoredUpload(item.id)}
                    />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <Badge variant="secondary" className="shrink-0">{item.host || 'unknown'}</Badge>
                        <div className="truncate text-sm font-medium" title={item.name}>{item.name}</div>
                      </div>
                      <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
                        <span>{formatNumber(item.rows)} 行</span>
                        <span>{new Date(item.uploaded_at * 1000).toLocaleString('zh-CN')}</span>
                        {item.source_url && <span className="truncate">{item.source_url}</span>}
                      </div>
                    </div>
                    <Button variant="ghost" size="icon" className="h-8 w-8 shrink-0 text-muted-foreground hover:text-destructive" onClick={() => deleteStoredUpload(item.id)}>
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </div>
        </CardContent>
      </Card>

      {result && (
        <>
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-5">
            <Card>
              <CardContent className="p-4">
                <div className="text-xs text-muted-foreground">匹配率</div>
                <div className="mt-2 flex items-end justify-between">
                  <div className="text-2xl font-bold">{matchedRate}%</div>
                  <Badge variant="success">{formatNumber(result.summary.matched_rows)}/{formatNumber(result.summary.local_rows)}</Badge>
                </div>
              </CardContent>
            </Card>
            <Card>
              <CardContent className="p-4">
                <div className="text-xs text-muted-foreground">本站收入</div>
                <div className="mt-2 text-2xl font-bold">{formatMoney(result.summary.local_revenue)}</div>
              </CardContent>
            </Card>
            <Card>
              <CardContent className="p-4">
                <div className="text-xs text-muted-foreground">上游成本</div>
                <div className="mt-2 text-2xl font-bold">{formatMoney(result.summary.upstream_cost)}</div>
              </CardContent>
            </Card>
            <Card>
              <CardContent className="p-4">
                <div className="text-xs text-muted-foreground">毛利</div>
                <div className={cn('mt-2 text-2xl font-bold', result.summary.gross < 0 ? 'text-red-600 dark:text-red-400' : 'text-emerald-600 dark:text-emerald-400')}>
                  {formatMoney(result.summary.gross)}
                </div>
              </CardContent>
            </Card>
            <Card>
              <CardContent className="p-4">
                <div className="text-xs text-muted-foreground">未用上游</div>
                <div className="mt-2 text-2xl font-bold">{formatNumber(result.summary.unused_upstream_rows)}</div>
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader className="pb-3">
              <CardTitle className="text-base">上游站点</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
                {Object.entries(result.summary.by_host).map(([host, item]) => (
                  <div key={host} className="rounded-md border border-border/60 p-3">
                    <div className="flex items-center justify-between gap-2">
                      <div className="truncate text-sm font-semibold" title={host}>{host}</div>
                      <Badge variant={item.gross < 0 ? 'destructive' : 'success'}>{formatMoney(item.gross)}</Badge>
                    </div>
                    <div className="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
                      <div className="text-muted-foreground">本站</div>
                      <div className="text-right">{formatNumber(item.local_rows)}</div>
                      <div className="text-muted-foreground">上游</div>
                      <div className="text-right">{formatNumber(item.upstream_rows)}</div>
                      <div className="text-muted-foreground">已匹配</div>
                      <div className="text-right">{formatNumber(item.matched_rows)}</div>
                      <div className="text-muted-foreground">按次不亏价</div>
                      <div className="text-right font-medium">{item.per_call_rows > 0 ? formatMoney(item.per_call_break_even_price) : '-'}</div>
                    </div>
                  </div>
                ))}
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="pb-3">
              <CardTitle className="text-base">筛选</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="grid gap-3 lg:grid-cols-[1fr_180px_170px_auto]">
                <div className="relative">
                  <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                  <Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索 Request ID、模型、渠道、用户" className="pl-9" />
                </div>
                <Select value={hostFilter} onChange={(event) => setHostFilter(event.target.value)}>
                  <option value="all">全部上游</option>
                  {hostOptions.map((host) => (
                    <option key={host} value={host}>{host}</option>
                  ))}
                </Select>
                <label className="flex h-10 items-center gap-2 rounded-md border border-input px-3 text-sm">
                  <input type="checkbox" className="h-4 w-4 accent-primary" checked={onlyPerCall} onChange={(event) => setOnlyPerCall(event.target.checked)} />
                  只看按次
                </label>
                <Button
                  variant="outline"
                  className="gap-2"
                  onClick={() => {
                    setSearch('')
                    setHostFilter('all')
                    setOnlyPerCall(false)
                    setStatusFilter({ matched: true, ambiguous: true, unmatched: true })
                    setSelectedChannels(channelOptions)
                    setSelectedModels(modelOptions)
                  }}
                >
                  <RefreshCw className="h-4 w-4" />
                  重置
                </Button>
              </div>

              <div className="flex flex-wrap gap-2">
                {(['matched', 'ambiguous', 'unmatched'] as MatchStatus[]).map((status) => (
                  <label key={status} className="inline-flex h-8 cursor-pointer items-center gap-2 rounded-md border border-border px-3 text-sm hover:bg-muted/60">
                    <input
                      type="checkbox"
                      className="h-4 w-4 accent-primary"
                      checked={statusFilter[status]}
                      onChange={() => toggleStatus(status)}
                    />
                    {status === 'matched' && <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" />}
                    {status === 'ambiguous' && <AlertTriangle className="h-3.5 w-3.5 text-amber-500" />}
                    {status === 'unmatched' && <XCircle className="h-3.5 w-3.5 text-red-500" />}
                    {statusLabel(status)}
                  </label>
                ))}
              </div>

              <div className="grid gap-3 lg:grid-cols-2">
                <CheckGroup title="本站渠道" options={channelOptions} selected={selectedChannels} onChange={setSelectedChannels} />
                <CheckGroup title="本站模型" options={modelOptions} selected={selectedModels} onChange={setSelectedModels} />
              </div>
            </CardContent>
          </Card>

          <div className="flex flex-wrap items-center justify-between gap-3 text-sm text-muted-foreground">
            <div>
              当前显示 {formatNumber(visibleRecords.length)} / {formatNumber(filteredRecords.length)} 条，全部记录 {formatNumber(result.records.length)} 条
            </div>
            <div>
              上传 {result.uploaded_files.map((file) => `${file.host}: ${formatNumber(file.rows)}`).join('，')}
            </div>
          </div>

          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-[92px]">状态</TableHead>
                <TableHead>上游</TableHead>
                <TableHead>时间</TableHead>
                <TableHead>本站渠道</TableHead>
                <TableHead>本站模型</TableHead>
                <TableHead className="text-right">Tokens</TableHead>
                <TableHead className="text-right">收入</TableHead>
                <TableHead className="text-right">成本</TableHead>
                <TableHead className="text-right">毛利</TableHead>
                <TableHead>Request ID</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {visibleRecords.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={10} className="h-24 text-center text-muted-foreground">没有符合条件的记录</TableCell>
                </TableRow>
              ) : visibleRecords.map((record, index) => (
                <TableRow key={`${record.local_request_id}-${record.local_time}-${index}`}>
                  <TableCell>
                    <Badge variant={statusBadge(record.status) as any}>{statusLabel(record.status)}</Badge>
                  </TableCell>
                  <TableCell>
                    <div className="max-w-[170px] truncate font-medium" title={record.upstream_host || record.target_host || '-'}>
                      {record.upstream_host || record.target_host || '-'}
                    </div>
                    <div className="mt-1 text-xs text-muted-foreground">{record.reason}</div>
                  </TableCell>
                  <TableCell>
                    <div className="whitespace-nowrap">{record.local_time}</div>
                    <div className="mt-1 text-xs text-muted-foreground">
                      {record.time_diff_seconds !== undefined ? `偏差 ${record.time_diff_seconds}s` : record.upstream_time || '-'}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="max-w-[170px] truncate" title={record.local_channel}>{record.local_channel || '-'}</div>
                    <div className="mt-1 text-xs text-muted-foreground">{record.local_group || '-'}</div>
                  </TableCell>
                  <TableCell>
                    <div className="max-w-[220px] truncate font-medium" title={record.local_model}>{record.local_model || '-'}</div>
                    <div className="mt-1 text-xs text-muted-foreground">{record.normalized_model || '-'}</div>
                  </TableCell>
                  <TableCell className="text-right">
                    <div>{formatTokens(record.total_tokens)}</div>
                    <div className="mt-1 text-xs text-muted-foreground">{formatTokens(record.prompt_tokens)} / {formatTokens(record.completion_tokens)}</div>
                  </TableCell>
                  <TableCell className="text-right">{formatMoney(record.local_revenue)}</TableCell>
                  <TableCell className="text-right">{record.status === 'matched' ? formatMoney(record.upstream_cost) : '-'}</TableCell>
                  <TableCell className={cn('text-right font-medium', (record.gross || 0) < 0 ? 'text-red-600 dark:text-red-400' : record.gross !== undefined ? 'text-emerald-600 dark:text-emerald-400' : '')}>
                    {record.gross !== undefined ? formatMoney(record.gross) : '-'}
                  </TableCell>
                  <TableCell>
                    <div className="max-w-[210px] truncate text-xs" title={record.local_request_id}>{record.local_request_id || '-'}</div>
                    {record.upstream_request_id && (
                      <div className="mt-1 max-w-[210px] truncate text-xs text-muted-foreground" title={record.upstream_request_id}>{record.upstream_request_id}</div>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </>
      )}
    </div>
  )
}
