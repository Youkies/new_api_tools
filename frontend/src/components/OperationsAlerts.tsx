import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Activity,
  AlertTriangle,
  Bell,
  CheckCircle2,
  Clock,
  Copy,
  CreditCard,
  Eye,
  Loader2,
  Mail,
  RefreshCw,
  Search,
  User,
  XCircle,
} from 'lucide-react'
import { useAuth } from '../contexts/AuthContext'
import { useToast } from './Toast'
import { Badge } from './ui/badge'
import { Button } from './ui/button'
import { Card, CardContent, CardHeader, CardTitle } from './ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from './ui/dialog'
import { Select } from './ui/select'
import { StatCard } from './StatCard'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from './ui/table'
import { cn } from '../lib/utils'

type Severity = 'critical' | 'high' | 'medium' | 'low'
type AlertStatus = 'handled' | 'ignored'

interface OperationsAlert {
  id: string
  type: string
  category: string
  severity: Severity
  title: string
  user_id: number
  username: string
  triggered_at: number
  evidence: string[]
  suggested_action: string
  metrics: Record<string, unknown>
}
interface OperationsSummary {
  total_alerts: number
  affected_users: number
  needs_attention: number
  by_severity: Record<string, number>
  by_category: Record<string, number>
  revenue_alerts_off: boolean
}

interface OperationsPayload {
  items: OperationsAlert[]
  summary: OperationsSummary
  window: string
  generated_at: number
  cache_hit: boolean
  notes: string[]
}

interface UserDetail {
  user: {
    id: number
    username: string
    display_name: string
    email: string
    status: number
    role: number
    group: string
    quota: number
    used_quota: number
    request_count: number
    created_at: number
  }
  usage: {
    total_requests: number
    success_requests: number
    failure_requests: number
    quota_used: number
    avg_use_time: number
    last_request_time: number
    unique_models: number
    unique_channels: number
    unique_ips: number
    unique_tokens: number
    failure_rate: number
  }
  topups: {
    available: boolean
    total_count?: number
    success_count?: number
    success_amount?: number
    success_money?: number
    last_success_time?: number
  }
  recent_topups: Array<{
    id: number
    amount: number
    money: number
    trade_no: string
    payment_method: string
    create_time: number
    complete_time: number
    status: string
  }>
  recent_logs: Array<{
    created_at: number
    type: number
    model_name: string
    channel_id: number
    quota: number
    use_time: number
    ip: string
    request_id: string
  }>
  privacy_note: string
}

const typeOptions = [
  { value: 'all', label: '全部预警' },
  { value: 'retention', label: '用户流失' },
  { value: 'activation', label: '新充值激活' },
  { value: 'experience', label: '体验异常' },
  { value: 'payment', label: '支付状态' },
]

const severityOptions = [
  { value: 'all', label: '全部等级' },
  { value: 'critical', label: '严重' },
  { value: 'high', label: '高' },
  { value: 'medium', label: '中' },
  { value: 'low', label: '低' },
]

const windowOptions = [
  { value: '24h', label: '24 小时' },
  { value: '3d', label: '3 天' },
  { value: '7d', label: '7 天' },
  { value: '30d', label: '30 天' },
]

function formatTime(timestamp?: number) {
  if (!timestamp) return '-'
  return new Date(timestamp * 1000).toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

function formatQuota(value?: number) {
  return `$${((Number(value || 0)) / 500000).toFixed(2)}`
}

function formatPercent(value?: number) {
  return `${(Number(value || 0) * 100).toFixed(1)}%`
}

function severityLabel(severity: string) {
  const labels: Record<string, string> = {
    critical: '严重',
    high: '高',
    medium: '中',
    low: '低',
  }
  return labels[severity] || severity
}

function severityBadgeVariant(severity: string) {
  if (severity === 'critical') return 'destructive'
  if (severity === 'high') return 'warning'
  if (severity === 'medium') return 'secondary'
  return 'outline'
}

function categoryLabel(category: string) {
  const labels: Record<string, string> = {
    retention: '用户流失',
    activation: '新充值激活',
    experience: '体验异常',
    payment: '支付状态',
  }
  return labels[category] || category
}

function statusLabel(status?: AlertStatus) {
  if (status === 'handled') return '已处理'
  if (status === 'ignored') return '已忽略'
  return '待处理'
}

function statusClass(status?: AlertStatus) {
  if (status === 'handled') return 'text-emerald-700 bg-emerald-50 border-emerald-200 dark:text-emerald-300 dark:bg-emerald-950/30 dark:border-emerald-900'
  if (status === 'ignored') return 'text-muted-foreground bg-muted border-border'
  return 'text-amber-700 bg-amber-50 border-amber-200 dark:text-amber-300 dark:bg-amber-950/30 dark:border-amber-900'
}

export function OperationsAlerts() {
  const { token } = useAuth()
  const { showToast } = useToast()
  const apiUrl = import.meta.env.VITE_API_URL || ''

  const [windowFilter, setWindowFilter] = useState('30d')
  const [typeFilter, setTypeFilter] = useState('all')
  const [severityFilter, setSeverityFilter] = useState('all')
  const [payload, setPayload] = useState<OperationsPayload | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [selectedUserId, setSelectedUserId] = useState<number | null>(null)
  const [selectedUserDetail, setSelectedUserDetail] = useState<UserDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [localStatuses, setLocalStatuses] = useState<Record<string, AlertStatus>>(() => {
    try {
      return JSON.parse(localStorage.getItem('operations_alert_statuses') || '{}')
    } catch {
      return {}
    }
  })

  const getAuthHeaders = useCallback(() => ({
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${token}`,
  }), [token])

  const saveStatus = useCallback((id: string, status: AlertStatus) => {
    setLocalStatuses((prev) => {
      const next = { ...prev, [id]: status }
      localStorage.setItem('operations_alert_statuses', JSON.stringify(next))
      return next
    })
  }, [])

  const fetchAlerts = useCallback(async (force = false) => {
    if (force) setRefreshing(true)
    else setLoading(true)

    try {
      const params = new URLSearchParams({
        window: windowFilter,
        type: typeFilter,
        severity: severityFilter,
        limit: '120',
      })
      if (force) params.set('no_cache', 'true')
      const response = await fetch(`${apiUrl}/api/operations/alerts?${params.toString()}`, {
        headers: getAuthHeaders(),
      })
      const data = await response.json()
      if (!response.ok || !data.success) {
        throw new Error(data.error?.message || data.message || '加载运营预警失败')
      }
      setPayload(data.data)
      if (force) showToast('success', '运营预警已刷新')
    } catch (error) {
      console.error('Failed to fetch operations alerts:', error)
      showToast('error', error instanceof Error ? error.message : '加载运营预警失败')
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }, [apiUrl, getAuthHeaders, severityFilter, showToast, typeFilter, windowFilter])

  useEffect(() => {
    fetchAlerts(false)
  }, [fetchAlerts])

  useEffect(() => {
    if (!selectedUserId) {
      setSelectedUserDetail(null)
      return
    }
    const fetchDetail = async () => {
      setDetailLoading(true)
      try {
        const params = new URLSearchParams({ window: windowFilter })
        const response = await fetch(`${apiUrl}/api/operations/users/${selectedUserId}/detail?${params.toString()}`, {
          headers: getAuthHeaders(),
        })
        const data = await response.json()
        if (!response.ok || !data.success) {
          throw new Error(data.error?.message || data.message || '加载用户详情失败')
        }
        setSelectedUserDetail(data.data)
      } catch (error) {
        console.error('Failed to fetch operations user detail:', error)
        showToast('error', error instanceof Error ? error.message : '加载用户详情失败')
      } finally {
        setDetailLoading(false)
      }
    }
    fetchDetail()
  }, [apiUrl, getAuthHeaders, selectedUserId, showToast, windowFilter])

  const summary = payload?.summary
  const visibleItems = payload?.items || []

  const activeItems = useMemo(
    () => visibleItems.filter((item) => !localStatuses[item.id]),
    [localStatuses, visibleItems],
  )

  const openAnalytics = useCallback((alert: OperationsAlert) => {
    const params = new URLSearchParams({
      source: 'operations',
      alert_type: alert.type,
      user_id: String(alert.user_id),
    })
    window.history.pushState(null, '', `/analytics?${params.toString()}`)
    window.dispatchEvent(new PopStateEvent('popstate'))
  }, [])

  const copyEmail = async (email: string) => {
    if (!email) {
      showToast('info', '该用户没有注册邮箱')
      return
    }
    await navigator.clipboard.writeText(email)
    showToast('success', '邮箱已复制')
  }

  return (
    <div className="space-y-6 animate-in fade-in duration-500">
      <div className="flex flex-col lg:flex-row justify-between items-start lg:items-center gap-4">
        <div>
          <h2 className="text-3xl font-bold tracking-tight">运营预警</h2>
          <p className="text-muted-foreground mt-1">跟踪高价值用户停用、充值断档、体验异常和支付状态</p>
        </div>
        <div className="flex flex-wrap items-center gap-3 w-full lg:w-auto">
          <Select value={windowFilter} onChange={(event) => setWindowFilter(event.target.value)} className="w-full sm:w-32">
            {windowOptions.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
          </Select>
          <Select value={typeFilter} onChange={(event) => setTypeFilter(event.target.value)} className="w-full sm:w-36">
            {typeOptions.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
          </Select>
          <Select value={severityFilter} onChange={(event) => setSeverityFilter(event.target.value)} className="w-full sm:w-32">
            {severityOptions.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
          </Select>
          <Button variant="outline" size="sm" onClick={() => fetchAlerts(true)} disabled={refreshing || loading} className="h-9">
            <RefreshCw className={cn('h-4 w-4 mr-2', refreshing && 'animate-spin')} />
            刷新
          </Button>
        </div>
      </div>

      <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
        <StatCard
          title="待处理预警"
          value={loading ? '-' : activeItems.length}
          subValue={`全部 ${summary?.total_alerts || 0} 条`}
          icon={Bell}
          color="blue"
        />
        <StatCard
          title="高优先级"
          value={loading ? '-' : summary?.needs_attention || 0}
          subValue={`严重 ${summary?.by_severity?.critical || 0} / 高 ${summary?.by_severity?.high || 0}`}
          icon={AlertTriangle}
          color="red"
        />
        <StatCard
          title="影响用户"
          value={loading ? '-' : summary?.affected_users || 0}
          subValue="可打开详情联系"
          icon={User}
          color="emerald"
        />
        <StatCard
          title="收入异常"
          value="未启用"
          subValue="等待上游价格系统"
          icon={CreditCard}
          color="gray"
        />
      </div>

      <Card className="border-dashed bg-muted/20">
        <CardContent className="p-4 flex flex-wrap gap-x-6 gap-y-2 text-sm text-muted-foreground">
          <div className="flex items-center gap-2">
            <Activity className="w-4 h-4 text-primary" />
            <span>数据时间：{formatTime(payload?.generated_at)}</span>
          </div>
          <div className="flex items-center gap-2">
            <Search className="w-4 h-4 text-primary" />
            <span>{payload?.cache_hit ? '命中缓存' : '实时刷新'}，注册邮箱仅在详情里展示</span>
          </div>
          <div className="flex items-center gap-2">
            <XCircle className="w-4 h-4 text-muted-foreground" />
            <span>收入/毛利异常暂不做，避免没有上游价格时误判</span>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base font-medium flex items-center gap-2">
            <Bell className="w-4 h-4" />
            预警列表
          </CardTitle>
        </CardHeader>
        <CardContent>
          {loading ? (
            <div className="h-64 flex items-center justify-center text-muted-foreground">
              <Loader2 className="w-5 h-5 mr-2 animate-spin" />
              正在加载运营预警...
            </div>
          ) : visibleItems.length === 0 ? (
            <div className="h-64 flex flex-col items-center justify-center text-muted-foreground gap-2">
              <CheckCircle2 className="w-9 h-9 text-emerald-500" />
              <div className="font-medium text-foreground">当前筛选下没有预警</div>
              <div className="text-sm">可以切换窗口或强制刷新再看一次</div>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-28">等级</TableHead>
                  <TableHead className="min-w-[220px]">预警</TableHead>
                  <TableHead className="min-w-[180px]">用户</TableHead>
                  <TableHead className="min-w-[320px]">证据摘要</TableHead>
                  <TableHead className="min-w-[220px]">建议动作</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-56 text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visibleItems.map((alert) => {
                  const localStatus = localStatuses[alert.id]
                  return (
                    <TableRow key={alert.id} className={cn(localStatus && 'opacity-70')}>
                      <TableCell>
                        <Badge variant={severityBadgeVariant(alert.severity)}>
                          {severityLabel(alert.severity)}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <div className="space-y-1">
                          <div className="font-medium">{alert.title}</div>
                          <div className="text-xs text-muted-foreground">{categoryLabel(alert.category)} · {formatTime(alert.triggered_at)}</div>
                        </div>
                      </TableCell>
                      <TableCell>
                        <button
                          className="text-left hover:text-primary transition-colors"
                          onClick={() => setSelectedUserId(alert.user_id)}
                        >
                          <div className="font-medium">{alert.username || `用户 ${alert.user_id}`}</div>
                          <div className="text-xs text-muted-foreground">ID {alert.user_id}</div>
                        </button>
                      </TableCell>
                      <TableCell>
                        <div className="space-y-1 text-sm">
                          {alert.evidence.slice(0, 3).map((item) => (
                            <div key={item} className="leading-relaxed">{item}</div>
                          ))}
                        </div>
                      </TableCell>
                      <TableCell>
                        <div className="text-sm leading-relaxed text-muted-foreground">{alert.suggested_action}</div>
                      </TableCell>
                      <TableCell>
                        <span className={cn('inline-flex rounded-full border px-2 py-0.5 text-xs font-medium', statusClass(localStatus))}>
                          {statusLabel(localStatus)}
                        </span>
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex items-center justify-end gap-2">
                          <Button variant="outline" size="sm" onClick={() => setSelectedUserId(alert.user_id)}>
                            <Eye className="w-4 h-4" />
                          </Button>
                          <Button variant="outline" size="sm" onClick={() => openAnalytics(alert)}>
                            <Search className="w-4 h-4" />
                          </Button>
                          <Button variant="outline" size="sm" onClick={() => saveStatus(alert.id, 'handled')}>
                            <CheckCircle2 className="w-4 h-4" />
                          </Button>
                          <Button variant="ghost" size="sm" onClick={() => saveStatus(alert.id, 'ignored')}>
                            <XCircle className="w-4 h-4" />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Dialog open={selectedUserId !== null} onOpenChange={(open) => !open && setSelectedUserId(null)}>
        <DialogContent className="max-w-5xl">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <User className="w-5 h-5 text-primary" />
              运营用户详情
            </DialogTitle>
            <DialogDescription>注册邮箱只用于管理员人工联系，不发送给外部 AI 或默认导出。</DialogDescription>
          </DialogHeader>

          {detailLoading ? (
            <div className="h-72 flex items-center justify-center text-muted-foreground">
              <Loader2 className="w-5 h-5 mr-2 animate-spin" />
              正在加载用户详情...
            </div>
          ) : selectedUserDetail ? (
            <div className="space-y-5">
              <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-3">
                <div className="rounded-lg border bg-muted/20 p-3">
                  <div className="text-xs text-muted-foreground mb-1">用户</div>
                  <div className="font-semibold truncate">{selectedUserDetail.user.display_name || selectedUserDetail.user.username || selectedUserDetail.user.id}</div>
                  <div className="text-xs text-muted-foreground">ID {selectedUserDetail.user.id} · {selectedUserDetail.user.group || '-'}</div>
                </div>
                <div className="rounded-lg border bg-muted/20 p-3">
                  <div className="text-xs text-muted-foreground mb-1">注册邮箱</div>
                  <div className="flex items-center gap-2 min-w-0">
                    <Mail className="w-4 h-4 text-primary shrink-0" />
                    <span className="font-semibold truncate">{selectedUserDetail.user.email || '-'}</span>
                    <Button variant="ghost" size="sm" onClick={() => copyEmail(selectedUserDetail.user.email)} className="h-7 w-7 p-0 shrink-0">
                      <Copy className="w-3.5 h-3.5" />
                    </Button>
                  </div>
                </div>
                <div className="rounded-lg border bg-muted/20 p-3">
                  <div className="text-xs text-muted-foreground mb-1">余额 / 已用</div>
                  <div className="font-semibold">{formatQuota(selectedUserDetail.user.quota)} / {formatQuota(selectedUserDetail.user.used_quota)}</div>
                  <div className="text-xs text-muted-foreground">累计请求 {Number(selectedUserDetail.user.request_count || 0).toLocaleString('zh-CN')}</div>
                </div>
                <div className="rounded-lg border bg-muted/20 p-3">
                  <div className="text-xs text-muted-foreground mb-1">最近调用</div>
                  <div className="font-semibold">{formatTime(selectedUserDetail.usage.last_request_time)}</div>
                  <div className="text-xs text-muted-foreground">失败率 {formatPercent(selectedUserDetail.usage.failure_rate)}</div>
                </div>
              </div>

              <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
                <Card>
                  <CardHeader className="pb-2">
                    <CardTitle className="text-sm flex items-center gap-2">
                      <Activity className="w-4 h-4" />
                      调用概况
                    </CardTitle>
                  </CardHeader>
                  <CardContent className="grid grid-cols-2 gap-3 text-sm">
                    <div><span className="text-muted-foreground">窗口请求</span><div className="font-semibold">{Number(selectedUserDetail.usage.total_requests || 0).toLocaleString('zh-CN')}</div></div>
                    <div><span className="text-muted-foreground">窗口消耗</span><div className="font-semibold">{formatQuota(selectedUserDetail.usage.quota_used)}</div></div>
                    <div><span className="text-muted-foreground">平均响应</span><div className="font-semibold">{Number(selectedUserDetail.usage.avg_use_time || 0).toFixed(2)}ms</div></div>
                    <div><span className="text-muted-foreground">IP / Token</span><div className="font-semibold">{selectedUserDetail.usage.unique_ips || 0} / {selectedUserDetail.usage.unique_tokens || 0}</div></div>
                  </CardContent>
                </Card>
                <Card>
                  <CardHeader className="pb-2">
                    <CardTitle className="text-sm flex items-center gap-2">
                      <CreditCard className="w-4 h-4" />
                      充值概况
                    </CardTitle>
                  </CardHeader>
                  <CardContent className="grid grid-cols-2 gap-3 text-sm">
                    <div><span className="text-muted-foreground">成功次数</span><div className="font-semibold">{selectedUserDetail.topups.available ? selectedUserDetail.topups.success_count || 0 : '-'}</div></div>
                    <div><span className="text-muted-foreground">成功金额</span><div className="font-semibold">{selectedUserDetail.topups.available ? `¥${Number(selectedUserDetail.topups.success_money || 0).toFixed(2)}` : '-'}</div></div>
                    <div><span className="text-muted-foreground">成功额度</span><div className="font-semibold">{selectedUserDetail.topups.available ? formatQuota(selectedUserDetail.topups.success_amount || 0) : '-'}</div></div>
                    <div><span className="text-muted-foreground">最近充值</span><div className="font-semibold">{selectedUserDetail.topups.available ? formatTime(selectedUserDetail.topups.last_success_time) : '-'}</div></div>
                  </CardContent>
                </Card>
              </div>

              <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
                <div className="space-y-2">
                  <h4 className="text-sm font-medium flex items-center gap-2">
                    <Clock className="w-4 h-4" />
                    最近充值
                  </h4>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>时间</TableHead>
                        <TableHead>金额</TableHead>
                        <TableHead>状态</TableHead>
                        <TableHead>订单</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {selectedUserDetail.recent_topups.length === 0 ? (
                        <TableRow><TableCell colSpan={4} className="text-center text-muted-foreground">暂无充值记录</TableCell></TableRow>
                      ) : selectedUserDetail.recent_topups.map((item) => (
                        <TableRow key={item.id}>
                          <TableCell>{formatTime(item.create_time)}</TableCell>
                          <TableCell>¥{Number(item.money || 0).toFixed(2)}</TableCell>
                          <TableCell>{item.status || '-'}</TableCell>
                          <TableCell className="max-w-[160px] truncate">{item.trade_no || '-'}</TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>

                <div className="space-y-2">
                  <h4 className="text-sm font-medium flex items-center gap-2">
                    <Activity className="w-4 h-4" />
                    最近日志
                  </h4>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>时间</TableHead>
                        <TableHead>模型</TableHead>
                        <TableHead>状态</TableHead>
                        <TableHead>耗时</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {selectedUserDetail.recent_logs.length === 0 ? (
                        <TableRow><TableCell colSpan={4} className="text-center text-muted-foreground">暂无调用日志</TableCell></TableRow>
                      ) : selectedUserDetail.recent_logs.map((item, index) => (
                        <TableRow key={`${item.request_id || index}-${item.created_at}`}>
                          <TableCell>{formatTime(item.created_at)}</TableCell>
                          <TableCell className="max-w-[180px] truncate">{item.model_name || '-'}</TableCell>
                          <TableCell>{item.type === 2 ? '成功' : '失败'}</TableCell>
                          <TableCell>{Number(item.use_time || 0).toFixed(2)}ms</TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              </div>
            </div>
          ) : (
            <div className="h-40 flex items-center justify-center text-muted-foreground">未选择用户</div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}
