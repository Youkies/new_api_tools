import { AlertTriangle, Bot, Clock, Loader2, RefreshCw, ShieldAlert, Users } from 'lucide-react'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '../ui/card'
import { cn } from '../../lib/utils'
import type { AltAccountAIResult, AltAccountCase } from './types'

interface AltAccountCasesPanelProps {
  cases: AltAccountCase[]
  loading: boolean
  refreshing: boolean
  assessingCaseId: string | null
  aiResult: AltAccountAIResult | null
  onRefresh: () => void
  onAssess: (item: AltAccountCase) => void
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

export function AltAccountCasesPanel({
  cases,
  loading,
  refreshing,
  assessingCaseId,
  aiResult,
  onRefresh,
  onAssess,
  formatNumber,
  formatTime,
}: AltAccountCasesPanelProps) {
  const topCase = cases[0]
  const assessment = aiResult?.assessment || null

  return (
    <Card className="rounded-xl border shadow-sm overflow-hidden mb-4">
      <CardHeader className="pb-3 border-b bg-muted/20">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <CardTitle className="text-base flex items-center gap-2">
              <ShieldAlert className="h-4 w-4 text-red-500" />
              小号风险案件
              <Badge variant="secondary" className="ml-1 bg-background font-mono">{cases.length}</Badge>
            </CardTitle>
            <div className="mt-1 text-xs text-muted-foreground">
              结合共享 IP、30 天轮换账号池、邀请链和 token 轮换，优先展示可复核案件。
            </div>
          </div>
          <div className="flex items-center gap-2">
            {topCase && (
              <Badge variant="outline" className={cn('h-7 px-2.5 text-xs', levelStyles[topCase.risk_level] || levelStyles.low)}>
                最高风险 {topCase.risk_score}
              </Badge>
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
            {cases.slice(0, 12).map((item) => (
              <div key={item.case_id} className="p-4 hover:bg-muted/20 transition-colors">
                <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant="outline" className={cn('h-6 px-2 text-xs', levelStyles[item.risk_level] || levelStyles.low)}>
                        {levelLabels[item.risk_level] || item.risk_level} · {item.risk_score}
                      </Badge>
                      <span className="font-semibold text-sm">{labelForCaseType(item.case_type, item.case_type_label)}</span>
                      <Badge variant="secondary" className="h-5 px-2 text-[10px]">{formatWindow(item.window)}</Badge>
                      {item.primary_ip_masked && <span className="text-xs font-mono text-muted-foreground">{item.primary_ip_masked}</span>}
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
                    disabled={assessingCaseId === item.case_id}
                  >
                    {assessingCaseId === item.case_id ? <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" /> : <Bot className="h-3.5 w-3.5 mr-1.5" />}
                    AI 研判
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}

        {assessment && (
          <div className="border-t bg-muted/10 p-4">
            <div className="flex flex-wrap items-center gap-2 mb-3">
              <Badge variant="outline" className="h-6 px-2 bg-background">AI 结果</Badge>
              <Badge variant="outline" className={cn('h-6 px-2', Number(assessment.risk_score || 0) >= 80 ? levelStyles.critical : levelStyles.medium)}>
                风险 {assessment.risk_score ?? '-'}
              </Badge>
              <Badge variant="outline" className="h-6 px-2">置信度 {Math.round(Number(assessment.confidence || 0) * 100)}%</Badge>
              <Badge variant="secondary" className="h-6 px-2">动作 {assessment.action || 'review'}</Badge>
              {aiResult?.model && <span className="text-xs text-muted-foreground">{aiResult.model}</span>}
            </div>
            <div className="text-sm font-medium">{assessment.reason || 'AI 未返回明确结论'}</div>
            <div className="mt-3 grid gap-3 lg:grid-cols-2">
              <div>
                <div className="text-xs font-semibold text-muted-foreground mb-1 flex items-center gap-1">
                  <AlertTriangle className="h-3.5 w-3.5" />
                  证据摘要
                </div>
                <ul className="space-y-1 text-xs text-muted-foreground">
                  {asList(assessment.evidence_summary).slice(0, 8).map((item, idx) => <li key={idx}>· {String(item)}</li>)}
                </ul>
              </div>
              <div>
                <div className="text-xs font-semibold text-muted-foreground mb-1 flex items-center gap-1">
                  <Users className="h-3.5 w-3.5" />
                  复核问题
                </div>
                <ul className="space-y-1 text-xs text-muted-foreground">
                  {asList(assessment.questions_for_admin).slice(0, 8).map((item, idx) => <li key={idx}>· {String(item)}</li>)}
                </ul>
              </div>
            </div>
            <div className="mt-3 text-[11px] text-muted-foreground">
              误报风险：{assessment.false_positive_risk || 'medium'} · Token 用量：{aiResult?.usage?.total_tokens || 0}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
