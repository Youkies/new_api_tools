import { AlertTriangle, Bot, CheckCircle2, ChevronDown, Clock, Loader2, RefreshCw, ShieldAlert, ShieldBan, ShieldCheck, Users } from 'lucide-react'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '../ui/card'
import { cn } from '../../lib/utils'
import type { AltAccountAIResult, AltAccountCase, BulkBanRecord, SharedIPItem, SharedIPUser } from './types'

interface AltAccountCasesPanelProps {
  cases: AltAccountCase[]
  loading: boolean
  refreshing: boolean
  assessingCaseId: string | null
  aiResult: AltAccountAIResult | null
  expandedCaseIds?: Set<string>
  latestUndoableBulkBanRecord?: BulkBanRecord | null
  bulkBanLoadingIp?: string | null
  undoBulkBanLoading?: string | null
  title?: string
  description?: string
  onRefresh: () => void
  onAssess: (item: AltAccountCase) => void
  onToggleExpand?: (caseId: string) => void
  onBulkBanSharedIP?: (item: SharedIPItem) => void
  onUndoBulkBan?: (record: BulkBanRecord | null) => void
  onOpenUserAnalysis?: (userId: number, displayName: string) => void
  onOpenBanDialog?: (user: SharedIPUser, displayName: string) => void
  formatNumber: (value: number) => string
  formatTime: (value: number) => string
}

const levelStyles: Record<string, string> = {
  critical: 'bg-red-50 text-red-700 border-red-200 dark:bg-red-900/20 dark:text-red-300',
  high: 'bg-orange-50 text-orange-700 border-orange-200 dark:bg-orange-900/20 dark:text-orange-300',
  medium: 'bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-900/20 dark:text-amber-300',
  low: 'bg-slate-50 text-slate-700 border-slate-200 dark:bg-slate-900/30 dark:text-slate-300',
}

const levelLabels: Record<string, string> = {
  critical: '极高风险',
  high: '高风险',
  medium: '需复核',
  low: '观察',
}

const actionLabels: Record<string, string> = {
  monitor: '继续观察',
  review: '人工复核',
  ban: '建议封禁',
}

function labelForCaseType(type: string, fallback?: string) {
  if (fallback) return fallback
  if (type === 'shared_ip') return '共享 IP 小号'
  if (type === 'rotating_pool') return '轮换小号池'
  if (type === 'invite_chain') return '邀请链小号'
  if (type === 'token_rotation') return 'Token 轮换'
  return type || '小号案件'
}

function formatWindow(window: string) {
  const labels: Record<string, string> = {
    '24h': '24小时',
    '3d': '3天',
    '7d': '7天',
    '30d': '30天',
  }
  return labels[window] || window
}

function asList(value: any): any[] {
  return Array.isArray(value) ? value : []
}

function isSharedIPCase(item: AltAccountCase) {
  return item.case_type === 'shared_ip'
}

function visibleCaseKey(item: AltAccountCase) {
  if (item.case_type === 'shared_ip' || item.case_type === 'rotating_pool') {
    return item.primary_ip || item.case_key || item.primary_ip_masked || ''
  }
  return item.case_key || item.primary_ip || item.primary_ip_masked || ''
}

function getCaseIP(item: AltAccountCase) {
  return item.primary_ip || item.case_key || ''
}

function toSharedIPUser(user: Record<string, any>): SharedIPUser {
  return {
    user_id: Number(user.user_id || user.id || 0),
    username: String(user.username || ''),
    display_name: user.display_name ? String(user.display_name) : undefined,
    status: Number(user.status || 0),
    token_count: Number(user.token_count || 0),
    request_count: Number(user.request_count || 0),
    first_seen: Number(user.first_seen || 0),
    last_seen: Number(user.last_seen || 0),
    used_quota: Number(user.used_quota || 0),
    total_request_count: Number(user.total_request_count || user.request_count || 0),
    topup_count: Number(user.topup_count || 0),
    has_successful_topup: Number(user.topup_count || 0) > 0,
  }
}

function caseToSharedIPItem(item: AltAccountCase): SharedIPItem {
  const users = (item.users || []).map(toSharedIPUser).filter((user) => user.user_id > 0)
  return {
    ip: getCaseIP(item),
    token_count: Number(item.token_count || 0),
    user_count: Number(item.user_count || users.length || 0),
    banned_count: Number(item.case_stats?.banned_count || 0),
    request_count: Number(item.request_count || 0),
    unbanned_count: users.filter((user) => user.status !== 2).length,
    alt_account_score: Number(item.risk_score || 0),
    alt_account_level: item.risk_level,
    alt_account_reasons: item.risk_reasons || [],
    users,
  }
}

function getUserName(user: SharedIPUser) {
  return user.display_name || user.username || `User#${user.user_id}`
}

function renderUserStatusBadge(status: number) {
  if (status === 2) return <Badge variant="destructive" className="h-5 px-2 text-[10px]">已封禁</Badge>
  if (status === 1) return <Badge variant="success" className="h-5 px-2 text-[10px]">正常</Badge>
  return <Badge variant="outline" className="h-5 px-2 text-[10px]">未知</Badge>
}

function renderAIResult(
  aiResult: AltAccountAIResult | null,
  item: AltAccountCase,
  formatNumber: (value: number) => string
) {
  const assessment = aiResult?.assessment || null
  if (!assessment || aiResult?.case_id !== item.case_id) return null

  const score = Number(assessment.risk_score || 0)
  const confidence = Math.round(Number(assessment.confidence || 0) * 100)
  const action = String(assessment.action || 'review')
  const evidence = asList(assessment.evidence_summary)
  const questions = asList(assessment.questions_for_admin)
  const recommendedActions = asList(assessment.recommended_actions)
  const falsePositiveReasons = asList(assessment.false_positive_reasons)

  return (
    <div className="mt-4 rounded-lg border bg-background/80 p-4">
      <div className="flex flex-wrap items-center gap-2 mb-3">
        <Badge variant="outline" className="h-6 px-2 bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-900/20 dark:text-emerald-300">
          <CheckCircle2 className="h-3.5 w-3.5 mr-1" />
          AI 研判结果
        </Badge>
        <Badge variant="outline" className={cn('h-6 px-2', score >= 80 ? levelStyles.critical : score >= 60 ? levelStyles.high : levelStyles.medium)}>
          风险 {formatNumber(score)}
        </Badge>
        <Badge variant="outline" className="h-6 px-2">置信度 {confidence}%</Badge>
        <Badge variant="secondary" className="h-6 px-2">{actionLabels[action] || action}</Badge>
        {assessment.false_positive_risk && (
          <Badge variant="outline" className="h-6 px-2">误伤 {String(assessment.false_positive_risk)}</Badge>
        )}
        {aiResult?.model && <span className="text-xs text-muted-foreground">{aiResult.model}</span>}
      </div>

      <div className="text-sm font-medium leading-6">
        {assessment.reason || 'AI 未返回明确结论，建议人工复核案件证据。'}
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-2">
        <div>
          <div className="text-xs font-semibold text-muted-foreground mb-1.5 flex items-center gap-1">
            <AlertTriangle className="h-3.5 w-3.5" />
            关键证据
          </div>
          {evidence.length > 0 ? (
            <ul className="space-y-1 text-xs text-muted-foreground">
              {evidence.slice(0, 8).map((entry, idx) => <li key={idx}>· {String(entry)}</li>)}
            </ul>
          ) : (
            <div className="text-xs text-muted-foreground">暂无证据摘要。</div>
          )}
        </div>

        <div>
          <div className="text-xs font-semibold text-muted-foreground mb-1.5 flex items-center gap-1">
            <Users className="h-3.5 w-3.5" />
            复核重点
          </div>
          {questions.length > 0 ? (
            <ul className="space-y-1 text-xs text-muted-foreground">
              {questions.slice(0, 8).map((entry, idx) => <li key={idx}>· {String(entry)}</li>)}
            </ul>
          ) : (
            <div className="text-xs text-muted-foreground">暂无额外复核问题。</div>
          )}
        </div>
      </div>

      {(recommendedActions.length > 0 || falsePositiveReasons.length > 0) && (
        <div className="mt-4 grid gap-4 lg:grid-cols-2">
          {recommendedActions.length > 0 && (
            <div>
              <div className="text-xs font-semibold text-muted-foreground mb-1.5">建议动作</div>
              <div className="flex flex-wrap gap-1.5">
                {recommendedActions.slice(0, 6).map((entry, idx) => (
                  <Badge key={idx} variant="outline" className="h-6 px-2 bg-muted/30 text-[11px]">
                    {String(entry)}
                  </Badge>
                ))}
              </div>
            </div>
          )}
          {falsePositiveReasons.length > 0 && (
            <div>
              <div className="text-xs font-semibold text-muted-foreground mb-1.5">可能误伤原因</div>
              <div className="flex flex-wrap gap-1.5">
                {falsePositiveReasons.slice(0, 6).map((entry, idx) => (
                  <Badge key={idx} variant="outline" className="h-6 px-2 bg-muted/30 text-[11px]">
                    {String(entry)}
                  </Badge>
                ))}
              </div>
            </div>
          )}
        </div>
      )}

      <div className="mt-3 text-[11px] text-muted-foreground">
        Token 用量：{formatNumber(Number(aiResult?.usage?.total_tokens || 0))}
        {aiResult?.usage?.api_duration_ms ? ` · 耗时 ${formatNumber(Number(aiResult.usage.api_duration_ms))}ms` : ''}
      </div>
    </div>
  )
}

export function AltAccountCasesPanel({
  cases,
  loading,
  refreshing,
  assessingCaseId,
  aiResult,
  expandedCaseIds,
  latestUndoableBulkBanRecord,
  bulkBanLoadingIp,
  undoBulkBanLoading,
  title = '小号风险案件',
  description = '结合共享 IP、30 天轮换账号池、邀请链和 token 轮换，优先展示可复核案件。',
  onRefresh,
  onAssess,
  onToggleExpand,
  onBulkBanSharedIP,
  onUndoBulkBan,
  onOpenUserAnalysis,
  onOpenBanDialog,
  formatNumber,
  formatTime,
}: AltAccountCasesPanelProps) {
  const topCase = cases[0]

  return (
    <Card className="rounded-xl border shadow-sm overflow-hidden mb-4">
      <CardHeader className="pb-3 border-b bg-muted/20">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <CardTitle className="text-base flex items-center gap-2">
              <ShieldAlert className="h-4 w-4 text-red-500" />
              {title}
              <Badge variant="secondary" className="ml-1 bg-background font-mono">{cases.length}</Badge>
            </CardTitle>
            <div className="mt-1 text-xs text-muted-foreground">
              {description}
            </div>
          </div>
          <div className="flex items-center gap-2">
            {topCase && (
              <Badge variant="outline" className={cn('h-7 px-2.5 text-xs', levelStyles[topCase.risk_level] || levelStyles.low)}>
                最高风险 {topCase.risk_score}
              </Badge>
            )}
            {onUndoBulkBan && (
              <Button
                variant="outline"
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={() => onUndoBulkBan(latestUndoableBulkBanRecord || null)}
                disabled={!latestUndoableBulkBanRecord || !!undoBulkBanLoading}
                title="撤销最近一次批量封禁"
              >
                {undoBulkBanLoading ? <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" /> : <ShieldCheck className="h-3.5 w-3.5 mr-1.5" />}
                撤销上一次
              </Button>
            )}
            <Button variant="ghost" size="icon" className="h-7 w-7" onClick={onRefresh} disabled={refreshing || loading} title="刷新小号案件">
              <RefreshCw className={cn('h-3.5 w-3.5', (refreshing || loading) && 'animate-spin')} />
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className="p-0">
        {loading ? (
          <div className="h-40 flex items-center justify-center text-sm text-muted-foreground">
            <Loader2 className="h-5 w-5 mr-2 animate-spin" />
            正在生成小号风险案件...
          </div>
        ) : cases.length === 0 ? (
          <div className="p-8 text-center text-sm text-muted-foreground">
            暂未发现达到阈值的小号风险案件。
          </div>
        ) : (
          <div className="divide-y">
            {cases.slice(0, 12).map((item) => {
              const hasAIResult = aiResult?.case_id === item.case_id && Boolean(aiResult?.assessment)
              const isAssessing = assessingCaseId === item.case_id
              const isExpanded = expandedCaseIds?.has(item.case_id) || false
              const sharedIPItem = isSharedIPCase(item) ? caseToSharedIPItem(item) : null
              const caseIp = sharedIPItem?.ip || ''
              const canBulkBan = Boolean(sharedIPItem && onBulkBanSharedIP && sharedIPItem.users.some((user) => user.status !== 2))
              return (
              <div key={item.case_id} className="p-4 hover:bg-muted/20 transition-colors">
                <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant="outline" className={cn('h-6 px-2 text-xs', levelStyles[item.risk_level] || levelStyles.low)}>
                        {levelLabels[item.risk_level] || item.risk_level} · {item.risk_score}
                      </Badge>
                      <span className="font-semibold text-sm">{labelForCaseType(item.case_type, item.case_type_label)}</span>
                      <Badge variant="secondary" className="h-5 px-2 text-[10px]">{formatWindow(item.window)}</Badge>
                      {hasAIResult && <Badge variant="outline" className="h-5 px-2 text-[10px] bg-emerald-50 text-emerald-700 border-emerald-200">已研判</Badge>}
                      {visibleCaseKey(item) && <span className="text-xs font-mono text-muted-foreground">{visibleCaseKey(item)}</span>}
                      {item.primary_inviter_id && <span className="text-xs font-mono text-muted-foreground">inviter #{item.primary_inviter_id}</span>}
                    </div>
                    <div className="mt-2 grid grid-cols-2 sm:grid-cols-4 gap-2 text-xs">
                      <div className="rounded-md bg-muted/40 p-2">
                        <div className="text-muted-foreground">用户</div>
                        <div className="font-semibold tabular-nums">{formatNumber(Number(item.user_count || 0))}</div>
                      </div>
                      <div className="rounded-md bg-muted/40 p-2">
                        <div className="text-muted-foreground">未充值</div>
                        <div className="font-semibold tabular-nums">{formatNumber(Number(item.no_topup_count || 0))}</div>
                      </div>
                      <div className="rounded-md bg-muted/40 p-2">
                        <div className="text-muted-foreground">请求</div>
                        <div className="font-semibold tabular-nums">{formatNumber(Number(item.request_count || 0))}</div>
                      </div>
                      <div className="rounded-md bg-muted/40 p-2">
                        <div className="text-muted-foreground">Token</div>
                        <div className="font-semibold tabular-nums">{formatNumber(Number(item.token_count || 0))}</div>
                      </div>
                    </div>
                    <div className="mt-2 flex flex-wrap gap-1.5">
                      {(item.risk_reasons || []).slice(0, 4).map((reason, idx) => (
                        <Badge key={idx} variant="outline" className="h-5 px-2 text-[10px] bg-background">
                          {reason}
                        </Badge>
                      ))}
                    </div>
                    <div className="mt-2 flex flex-wrap items-center gap-3 text-[11px] text-muted-foreground">
                      {item.first_seen ? <span><Clock className="inline h-3 w-3 mr-1" />首次 {formatTime(item.first_seen)}</span> : null}
                      {item.last_seen ? <span>最近 {formatTime(item.last_seen)}</span> : null}
                      {item.prompt_version ? <span>{item.prompt_version}</span> : null}
                    </div>
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-8 px-3 text-xs shrink-0"
                    onClick={() => onAssess(item)}
                    disabled={isAssessing}
                  >
                    {isAssessing ? <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" /> : <Bot className="h-3.5 w-3.5 mr-1.5" />}
                    {hasAIResult ? '重新研判' : 'AI 研判'}
                  </Button>
                  {sharedIPItem && onBulkBanSharedIP && (
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-8 px-3 text-xs shrink-0 border-red-200 text-red-600 hover:bg-red-50"
                      onClick={() => onBulkBanSharedIP(sharedIPItem)}
                      disabled={!canBulkBan || bulkBanLoadingIp === caseIp}
                    >
                      {bulkBanLoadingIp === caseIp ? <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" /> : <ShieldBan className="h-3.5 w-3.5 mr-1.5" />}
                      封禁全部
                    </Button>
                  )}
                  {onToggleExpand && (
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 shrink-0"
                      onClick={() => onToggleExpand(item.case_id)}
                      title={isExpanded ? '收起用户明细' : '展开用户明细'}
                    >
                      <ChevronDown className={cn('h-4 w-4 transition-transform', isExpanded && 'rotate-180')} />
                    </Button>
                  )}
                </div>
                {isExpanded && (
                  <div className="mt-4 rounded-lg border bg-background overflow-hidden">
                    <div className="px-3 py-2 border-b bg-muted/20 text-xs font-semibold text-muted-foreground">
                      用户明细 {formatNumber(Number(item.users?.length || 0))}
                    </div>
                    <div className="divide-y">
                      {(sharedIPItem?.users || (item.users || []).map(toSharedIPUser)).slice(0, 80).map((user) => {
                        const displayName = getUserName(user)
                        return (
                          <div key={user.user_id} className="px-3 py-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between text-xs">
                            <div className="min-w-0">
                              <div className="flex flex-wrap items-center gap-2">
                                <span className="font-semibold">#{user.user_id}</span>
                                <span className="truncate max-w-[180px]">{displayName}</span>
                                {renderUserStatusBadge(user.status)}
                                {Number(user.topup_count || 0) > 0 ? (
                                  <Badge variant="outline" className="h-5 px-2 text-[10px]">已充值 {user.topup_count}</Badge>
                                ) : (
                                  <Badge variant="outline" className="h-5 px-2 text-[10px] bg-red-50 text-red-700 border-red-200">未充值</Badge>
                                )}
                              </div>
                              <div className="mt-1 text-[11px] text-muted-foreground">
                                请求 {formatNumber(Number(user.request_count || 0))}
                                {' · '}Token {formatNumber(Number(user.token_count || 0))}
                                {' · '}首次 {user.first_seen ? formatTime(user.first_seen) : '-'}
                                {' · '}最近 {user.last_seen ? formatTime(user.last_seen) : '-'}
                              </div>
                            </div>
                            <div className="flex items-center gap-2">
                              {onOpenUserAnalysis && (
                                <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={() => onOpenUserAnalysis(user.user_id, displayName)}>
                                  分析
                                </Button>
                              )}
                              {onOpenBanDialog && user.status !== 2 && (
                                <Button variant="ghost" size="sm" className="h-7 px-2 text-xs text-red-600 hover:bg-red-50" onClick={() => onOpenBanDialog(user, displayName)}>
                                  封禁
                                </Button>
                              )}
                            </div>
                          </div>
                        )
                      })}
                    </div>
                  </div>
                )}
                {isAssessing && (
                  <div className="mt-4 rounded-lg border bg-muted/20 p-4 text-sm text-muted-foreground flex items-center">
                    <Loader2 className="h-4 w-4 mr-2 animate-spin" />
                    AI 正在研判这个案件，完成后结果会显示在这里...
                  </div>
                )}
                {renderAIResult(aiResult, item, formatNumber)}
              </div>
              )
            })}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
