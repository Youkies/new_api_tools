import { useMemo } from 'react'
import { AlertTriangle, ChevronDown, Loader2, RefreshCw, ShieldBan, ShieldCheck, Sparkles, Users } from 'lucide-react'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '../ui/card'
import { cn, isCloudflareIp } from '../../lib/utils'
import type { BulkBanRecord, SharedIPItem, SharedIPUser } from './types'

interface SharedIPAltAccountPanelProps {
  sharedIps: SharedIPItem[]
  page: number
  pageSize: number
  expandedIps: Set<string>
  bulkBanRecords: BulkBanRecord[]
  latestUndoableBulkBanRecord: BulkBanRecord | null
  bulkBanLoadingIp: string | null
  undoBulkBanLoading: string | null
  refreshing: boolean
  aiAssessingIp?: string | null
  onPageChange: (page: number) => void
  onToggleExpand: (ip: string) => void
  onRefresh: () => void
  onBulkBan: (item: SharedIPItem) => void
  onAssessWithAI?: (item: SharedIPItem) => void
  onUndoBulkBan: (record: BulkBanRecord | null) => void
  onOpenUserAnalysis: (userId: number, displayName: string) => void
  onOpenBanDialog: (user: SharedIPUser, displayName: string) => void
  formatNumber: (value: number) => string
  formatTime: (value: number) => string
}

type RiskLevel = 'low' | 'medium' | 'high' | 'critical'

interface AltAccountSignal {
  score: number
  level: RiskLevel
  label: string
  reasons: string[]
  unbannedCount: number
  suspiciousFreshCount: number
  noTopupCount: number
  knownTopupUsers: number
  activeUsers: number
  firstSeenSpreadSeconds: number | null
}

const levelStyles: Record<RiskLevel, string> = {
  low: 'bg-slate-50 text-slate-700 border-slate-200 dark:bg-slate-900/30 dark:text-slate-300',
  medium: 'bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-900/20 dark:text-amber-300',
  high: 'bg-orange-50 text-orange-700 border-orange-200 dark:bg-orange-900/20 dark:text-orange-300',
  critical: 'bg-red-50 text-red-700 border-red-200 dark:bg-red-900/20 dark:text-red-300',
}

const levelLabels: Record<RiskLevel, string> = {
  low: '低风险',
  medium: '需复核',
  high: '高风险',
  critical: '强疑似小号',
}

function normalizeLevel(level?: string): RiskLevel | null {
  if (!level) return null
  if (level === 'critical' || level === 'high' || level === 'medium' || level === 'low') {
    return level
  }
  return null
}

function getUserName(user: SharedIPUser) {
  return user.display_name || user.username || `User#${user.user_id}`
}

function hasTopupSignal(user: SharedIPUser) {
  return user.has_successful_topup !== undefined || user.topup_count !== undefined
}

function hasSuccessfulTopup(user: SharedIPUser) {
  if (typeof user.has_successful_topup === 'boolean') return user.has_successful_topup
  return Number(user.topup_count || 0) > 0
}

function renderUserStatusBadge(status: number) {
  if (status === 2) {
    return <Badge variant="destructive" className="h-5 px-2 text-[10px]">已封禁</Badge>
  }
  if (status === 1) {
    return <Badge variant="success" className="h-5 px-2 text-[10px]">正常</Badge>
  }
  return <Badge variant="outline" className="h-5 px-2 text-[10px]">未知</Badge>
}

function estimateAltAccountSignal(item: SharedIPItem): AltAccountSignal {
  const users = item.users || []
  const unbannedUsers = users.filter((user) => user.status !== 2)
  const activeUsers = users.filter((user) => Number(user.request_count || 0) > 0).length
  const suspiciousFreshCount = unbannedUsers.filter((user) => Number(user.request_count || 0) <= 3 && Number(user.token_count || 0) <= 1).length
  const topupKnownUsers = users.filter(hasTopupSignal)
  const noTopupCount = users.filter((user) => hasTopupSignal(user) && !hasSuccessfulTopup(user)).length
  const firstSeenValues = users.map((user) => Number(user.first_seen || 0)).filter(Boolean)
  const firstSeenSpreadSeconds = firstSeenValues.length >= 2
    ? Math.max(...firstSeenValues) - Math.min(...firstSeenValues)
    : null

  let score = Number(item.alt_account_score || 0)
  const reasons = new Set<string>(item.alt_account_reasons || [])

  if (item.user_count >= 8) {
    score += 26
    reasons.add(`同一 IP 聚集 ${item.user_count} 个用户`)
  } else if (item.user_count >= 5) {
    score += 20
    reasons.add(`同一 IP 聚集 ${item.user_count} 个用户`)
  } else if (item.user_count >= 3) {
    score += 12
    reasons.add(`同一 IP 聚集 ${item.user_count} 个用户`)
  }

  if (unbannedUsers.length >= 5) {
    score += 18
    reasons.add(`${unbannedUsers.length} 个未封禁账号仍可继续调用`)
  } else if (unbannedUsers.length >= 3) {
    score += 10
    reasons.add(`${unbannedUsers.length} 个未封禁账号仍需复核`)
  }

  if (item.token_count >= item.user_count * 2 && item.user_count >= 3) {
    score += 12
    reasons.add('令牌数明显高于用户数，可能存在批量开 token')
  } else if (item.token_count > item.user_count) {
    score += 6
    reasons.add('同一批账号存在多个令牌')
  }

  if (item.request_count >= 10000) {
    score += 16
    reasons.add('共享 IP 总请求量过高')
  } else if (item.request_count >= 1000) {
    score += 8
    reasons.add('共享 IP 有持续调用量')
  }

  if (firstSeenSpreadSeconds !== null && users.length >= 3) {
    if (firstSeenSpreadSeconds <= 3600) {
      score += 16
      reasons.add('多个账号首次出现时间集中在 1 小时内')
    } else if (firstSeenSpreadSeconds <= 86400) {
      score += 8
      reasons.add('多个账号首次出现时间集中在 24 小时内')
    }
  }

  if (suspiciousFreshCount >= 3) {
    score += 12
    reasons.add(`${suspiciousFreshCount} 个账号低请求低令牌，像试探性小号`)
  }

  if (topupKnownUsers.length > 0 && noTopupCount >= Math.max(2, Math.ceil(users.length * 0.5))) {
    score += 14
    reasons.add(`${noTopupCount} 个账号未发现成功充值记录`)
  }

  if ((item.banned_count || 0) > 0) {
    score += 8
    reasons.add('该 IP 已有关联账号被封禁')
  }

  if (isCloudflareIp(item.ip)) {
    reasons.add('Cloudflare 出口 IP 可能带来误伤，需结合账号和充值证据确认')
  }

  score = Math.min(100, Math.max(0, Math.round(score)))
  const serverLevel = normalizeLevel(item.alt_account_level)
  const level = serverLevel || (score >= 80 ? 'critical' : score >= 60 ? 'high' : score >= 35 ? 'medium' : 'low')

  return {
    score,
    level,
    label: levelLabels[level],
    reasons: Array.from(reasons).slice(0, 5),
    unbannedCount: item.unbanned_count ?? unbannedUsers.length,
    suspiciousFreshCount,
    noTopupCount,
    knownTopupUsers: topupKnownUsers.length,
    activeUsers,
    firstSeenSpreadSeconds,
  }
}

function formatSpread(seconds: number | null) {
  if (seconds === null) return '未知'
  if (seconds < 3600) return `${Math.max(1, Math.round(seconds / 60))} 分钟`
  if (seconds < 86400) return `${Math.round(seconds / 3600)} 小时`
  return `${Math.round(seconds / 86400)} 天`
}

export function SharedIPAltAccountPanel({
  sharedIps,
  page,
  pageSize,
  expandedIps,
  bulkBanRecords,
  latestUndoableBulkBanRecord,
  bulkBanLoadingIp,
  undoBulkBanLoading,
  refreshing,
  aiAssessingIp,
  onPageChange,
  onToggleExpand,
  onRefresh,
  onBulkBan,
  onAssessWithAI,
  onUndoBulkBan,
  onOpenUserAnalysis,
  onOpenBanDialog,
  formatNumber,
  formatTime,
}: SharedIPAltAccountPanelProps) {
  const startIndex = (page - 1) * pageSize
  const pageItems = useMemo(
    () => sharedIps.slice(startIndex, page * pageSize),
    [sharedIps, startIndex, page, pageSize]
  )

  const topSignal = useMemo(() => {
    if (sharedIps.length === 0) return null
    return sharedIps
      .map((item) => ({ item, signal: estimateAltAccountSignal(item) }))
      .sort((a, b) => b.signal.score - a.signal.score)[0]
  }, [sharedIps])

  return (
    <Card className="rounded-xl border shadow-sm overflow-hidden">
      <CardHeader className="pb-3 border-b bg-muted/20">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <CardTitle className="text-base flex items-center gap-2">
              <AlertTriangle className="h-4 w-4 text-orange-500" />
              多用户共享 IP 小号研判
              <Badge variant="secondary" className="ml-1 bg-background font-mono">{sharedIps.length}</Badge>
            </CardTitle>
            <div className="mt-1 text-xs text-muted-foreground">
              按用户聚集度、请求量、令牌分布、首次出现时间和充值线索识别批量注册/开小号。
            </div>
          </div>
          <div className="flex items-center gap-2">
            {topSignal && (
              <Badge variant="outline" className={cn('h-7 px-2.5 text-xs', levelStyles[topSignal.signal.level])}>
                最高风险 {topSignal.signal.score}
              </Badge>
            )}
            <Button
              variant="outline"
              size="sm"
              className="h-7 px-2 text-xs"
              onClick={() => onUndoBulkBan(latestUndoableBulkBanRecord)}
              disabled={!latestUndoableBulkBanRecord || !!undoBulkBanLoading}
              title="撤销最近一次批量封禁"
            >
              {undoBulkBanLoading ? <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" /> : <ShieldCheck className="h-3.5 w-3.5 mr-1.5" />}
              撤销上一次
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7"
              onClick={onRefresh}
              disabled={refreshing}
              title="刷新"
            >
              <RefreshCw className={cn('h-3.5 w-3.5', refreshing && 'animate-spin')} />
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className="p-0">
        {sharedIps.length > 0 ? (
          <>
            <div className="divide-y">
              {pageItems.map((item) => {
                const signal = estimateAltAccountSignal(item)
                const isBulkBanning = bulkBanLoadingIp === item.ip
                const isAIAssessing = aiAssessingIp === item.ip
                const expanded = expandedIps.has(item.ip)

                return (
                  <div key={item.ip} className="px-4 py-3 transition-colors hover:bg-muted/30">
                    <div
                      className="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between cursor-pointer"
                      onClick={() => onToggleExpand(item.ip)}
                    >
                      <div className="min-w-0 space-y-2">
                        <div className="flex flex-wrap items-center gap-2">
                          <code className="text-sm bg-muted px-2 py-1 rounded font-mono text-foreground border border-border/50">{item.ip}</code>
                          {isCloudflareIp(item.ip) && (
                            <Badge className="bg-orange-100 text-orange-700 border-orange-200 hover:bg-orange-100 px-1.5 py-0 text-[10px] font-bold">CF</Badge>
                          )}
                          <Badge variant="outline" className={cn('font-semibold', levelStyles[signal.level])}>
                            {signal.label} · {signal.score}
                          </Badge>
                          {(item.banned_count || 0) > 0 && (
                            <Badge variant="destructive" className="font-normal">{item.banned_count} 已封禁</Badge>
                          )}
                        </div>
                        <div className="flex flex-wrap gap-2 text-xs">
                          <Badge variant="outline" className="font-normal bg-background">{item.user_count} 用户</Badge>
                          <Badge variant="outline" className="font-normal bg-background">{item.token_count} 令牌</Badge>
                          <Badge variant="outline" className="font-normal bg-background">{signal.unbannedCount} 未封禁</Badge>
                          <Badge variant="outline" className="font-normal bg-background">{formatNumber(item.request_count)} 请求</Badge>
                          {signal.knownTopupUsers > 0 && (
                            <Badge variant="outline" className="font-normal bg-background">{signal.noTopupCount} 未充值</Badge>
                          )}
                          <Badge variant="outline" className="font-normal bg-background">首次集中 {formatSpread(signal.firstSeenSpreadSeconds)}</Badge>
                        </div>
                        {signal.reasons.length > 0 && (
                          <div className="flex flex-wrap gap-1.5">
                            {signal.reasons.slice(0, 3).map((reason) => (
                              <span key={reason} className="rounded-full bg-orange-50 px-2 py-0.5 text-[11px] text-orange-700 dark:bg-orange-900/20 dark:text-orange-300">
                                {reason}
                              </span>
                            ))}
                          </div>
                        )}
                      </div>
                      <div className="flex items-center justify-between gap-3 xl:justify-end">
                        {onAssessWithAI && (
                          <Button
                            variant="outline"
                            size="sm"
                            className="h-8 px-2 text-xs text-blue-600 border-blue-200 hover:bg-blue-50 hover:text-blue-700"
                            disabled={!!aiAssessingIp}
                            title="使用 AI 研判该共享 IP 是否疑似批量小号"
                            onClick={(e) => {
                              e.stopPropagation()
                              onAssessWithAI(item)
                            }}
                          >
                            {isAIAssessing ? <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" /> : <Sparkles className="h-3.5 w-3.5 mr-1.5" />}
                            AI研判
                          </Button>
                        )}
                        <Button
                          variant="outline"
                          size="sm"
                          className="h-8 px-2 text-xs text-red-600 border-red-200 hover:bg-red-50 hover:text-red-700"
                          disabled={signal.unbannedCount === 0 || !!bulkBanLoadingIp}
                          title={signal.unbannedCount > 0 ? `封禁该 IP 下 ${signal.unbannedCount} 个未封禁用户` : '该 IP 下用户均已封禁'}
                          onClick={(e) => {
                            e.stopPropagation()
                            onBulkBan(item)
                          }}
                        >
                          {isBulkBanning ? <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" /> : <ShieldBan className="h-3.5 w-3.5 mr-1.5" />}
                          封禁全部
                        </Button>
                        <div className="flex flex-col items-end">
                          <span className="text-sm font-bold tabular-nums font-mono text-foreground">
                            {formatNumber(item.request_count)}
                          </span>
                          <span className="text-[9px] text-muted-foreground uppercase font-bold tracking-tight opacity-50">Requests</span>
                        </div>
                        <div className={cn('transition-transform duration-200 p-1 rounded hover:bg-muted', expanded && 'rotate-180')}>
                          <ChevronDown className="h-4 w-4 text-muted-foreground" />
                        </div>
                      </div>
                    </div>

                    {expanded && (
                      <div className="mt-3 space-y-3 animate-in slide-in-from-top-1 duration-200">
                        <div className="grid gap-2 rounded-lg border bg-muted/20 p-3 text-xs md:grid-cols-4">
                          <div>
                            <div className="text-muted-foreground">活跃账号</div>
                            <div className="mt-1 font-semibold">{signal.activeUsers}/{item.user_count}</div>
                          </div>
                          <div>
                            <div className="text-muted-foreground">低请求小号</div>
                            <div className="mt-1 font-semibold">{signal.suspiciousFreshCount}</div>
                          </div>
                          <div>
                            <div className="text-muted-foreground">未充值线索</div>
                            <div className="mt-1 font-semibold">{signal.knownTopupUsers > 0 ? `${signal.noTopupCount}/${signal.knownTopupUsers}` : '后端未返回'}</div>
                          </div>
                          <div>
                            <div className="text-muted-foreground">首次出现跨度</div>
                            <div className="mt-1 font-semibold">{formatSpread(signal.firstSeenSpreadSeconds)}</div>
                          </div>
                        </div>

                        {(item.users || []).map((user) => {
                          const displayName = getUserName(user)
                          const isBanned = user.status === 2
                          const topupKnown = hasTopupSignal(user)
                          const noTopup = topupKnown && !hasSuccessfulTopup(user)

                          return (
                            <div key={`${item.ip}-${user.user_id}`} className="flex items-center justify-between text-sm bg-muted/40 rounded-lg px-3 py-2 border border-border/40 group/user-row">
                              <div className="flex items-center gap-2 min-w-0">
                                <div
                                  className="flex items-center gap-2 px-2 py-1 rounded-full bg-muted/50 hover:bg-primary/10 hover:text-primary transition-all cursor-pointer border border-transparent hover:border-primary/20 w-fit group/user"
                                  onClick={(e) => {
                                    e.stopPropagation()
                                    onOpenUserAnalysis(user.user_id, displayName)
                                  }}
                                >
                                  <div className="w-5 h-5 rounded-full bg-blue-500/10 text-blue-600 flex items-center justify-center font-bold text-[10px] border border-blue-500/20 group-hover/user:bg-blue-500/20 shrink-0">
                                    {displayName[0]?.toUpperCase()}
                                  </div>
                                  <div className="flex flex-col leading-tight min-w-0">
                                    <span className="text-xs font-semibold truncate max-w-[150px]">{displayName}</span>
                                    <span className="text-[9px] text-muted-foreground font-mono">ID: {user.user_id}</span>
                                  </div>
                                </div>
                                {renderUserStatusBadge(user.status)}
                                <Badge variant="outline" className="h-5 px-2 text-[10px] bg-background">
                                  {user.token_count} 令牌
                                </Badge>
                                {topupKnown && (
                                  <Badge variant="outline" className={cn('h-5 px-2 text-[10px]', noTopup ? 'border-orange-200 bg-orange-50 text-orange-700 dark:bg-orange-900/20 dark:text-orange-300' : 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300')}>
                                    {noTopup ? '未充值' : '已充值'}
                                  </Badge>
                                )}
                              </div>
                              <div className="flex items-center gap-2">
                                <div className="hidden items-center gap-1.5 opacity-80 sm:flex">
                                  <span className="text-[9px] text-muted-foreground">首次</span>
                                  <span className="text-[10px] text-foreground font-mono">{formatTime(user.first_seen)}</span>
                                </div>
                                <div className="flex items-center gap-1.5 opacity-80">
                                  <span className="text-foreground font-bold tabular-nums font-mono text-xs">{formatNumber(user.request_count)}</span>
                                  <span className="text-[9px] text-muted-foreground uppercase font-bold tracking-tighter opacity-60">reqs</span>
                                </div>
                                {!isBanned && (
                                  <Button
                                    variant="ghost"
                                    size="icon"
                                    className="h-7 w-7 text-red-500 hover:text-red-600 hover:bg-red-500/10"
                                    title="一键封禁"
                                    onClick={(e) => {
                                      e.stopPropagation()
                                      onOpenBanDialog(user, displayName)
                                    }}
                                  >
                                    <ShieldBan className="h-3.5 w-3.5" />
                                  </Button>
                                )}
                              </div>
                            </div>
                          )
                        })}
                        {(!item.users || item.users.length === 0) && (
                          <div className="text-xs text-muted-foreground bg-muted/30 rounded-lg px-3 py-2">
                            暂无用户详情
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
            {sharedIps.length > pageSize && (
              <div className="flex items-center justify-between p-3 border-t bg-muted/5">
                <div className="text-[11px] text-muted-foreground">
                  显示 {Math.min(sharedIps.length, startIndex + 1)} - {Math.min(sharedIps.length, page * pageSize)}，共 {sharedIps.length} 条
                </div>
                <div className="flex gap-1">
                  <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>上一页</Button>
                  <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" disabled={page * pageSize >= sharedIps.length} onClick={() => onPageChange(page + 1)}>下一页</Button>
                </div>
              </div>
            )}
          </>
        ) : (
          <div className="h-40 flex flex-col items-center justify-center text-muted-foreground text-sm">
            <ShieldCheck className="h-8 w-8 mb-2 opacity-20" />
            暂无异常共用 IP
          </div>
        )}

        {bulkBanRecords.length > 0 && (
          <div className="border-t bg-muted/10 px-4 py-3">
            <div className="flex items-center justify-between mb-2">
              <div className="flex items-center gap-2">
                <Users className="h-4 w-4 text-red-500" />
                <span className="text-sm font-semibold">共享 IP 批量处置记录</span>
                <Badge variant="outline" className="h-5 px-2 text-[10px]">{bulkBanRecords.length}</Badge>
              </div>
              <Button
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs"
                disabled={!latestUndoableBulkBanRecord || !!undoBulkBanLoading}
                onClick={() => onUndoBulkBan(latestUndoableBulkBanRecord)}
              >
                {undoBulkBanLoading ? <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" /> : <ShieldCheck className="h-3.5 w-3.5 mr-1.5" />}
                撤销上一次封禁
              </Button>
            </div>
            <div className="space-y-1.5">
              {bulkBanRecords.slice(0, 5).map((record) => (
                <div key={record.id} className="flex items-center justify-between gap-3 rounded-lg border bg-background/80 px-3 py-2 text-xs">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2 min-w-0">
                      <code className="font-mono text-foreground truncate">{record.ip}</code>
                      <Badge variant={record.undone ? 'secondary' : 'destructive'} className="h-5 px-2 text-[10px]">
                        {record.undone ? '已撤销' : '已封禁'}
                      </Badge>
                      {record.failed_count > 0 && (
                        <Badge variant="outline" className="h-5 px-2 text-[10px]">{record.failed_count} 失败</Badge>
                      )}
                    </div>
                    <div className="mt-1 text-muted-foreground">
                      {formatTime(record.created_at)} · 成功 {record.success_count}/{record.user_count} · {record.reason}
                    </div>
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-7 px-2 text-xs shrink-0"
                    disabled={record.undone || undoBulkBanLoading === record.id}
                    onClick={() => onUndoBulkBan(record)}
                  >
                    {undoBulkBanLoading === record.id ? <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" /> : <ShieldCheck className="h-3.5 w-3.5 mr-1.5" />}
                    撤销
                  </Button>
                </div>
              ))}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
