export type WindowKey = '1h' | '3h' | '6h' | '12h' | '24h' | '3d' | '7d'

export interface IPStats {
  total_users: number
  enabled_count: number
  disabled_count: number
  enabled_percentage: number
  unique_ips_24h: number
}

export interface SharedIPUser {
  user_id: number
  username: string
  display_name?: string
  status: number
  token_count: number
  request_count: number
  first_seen: number
  last_seen: number
  used_quota?: number
  total_request_count?: number
  topup_count?: number
  has_successful_topup?: boolean
}

export interface SharedIPItem {
  ip: string
  token_count: number
  user_count: number
  banned_count?: number
  request_count: number
  unbanned_count?: number
  alt_account_score?: number
  alt_account_level?: 'low' | 'medium' | 'high' | 'critical' | string
  alt_account_reasons?: string[]
  users: SharedIPUser[]
  tokens?: Array<{
    token_id: number
    token_name: string
    user_id: number
    username: string
    request_count: number
  }>
}

export interface BulkBanRecord {
  id: string
  ip: string
  reason: string
  created_at: number
  user_count: number
  success_count: number
  failed_count: number
  users: Array<{
    user_id: number
    username: string
    display_name?: string
  }>
  undone?: boolean
  undone_at?: number
  undo_failed_count?: number
}

export interface MultiIPTokenItem {
  token_id: number
  token_name: string
  user_id: number
  username: string
  ip_count: number
  request_count: number
  ips: Array<{ ip: string; request_count: number }>
}

export interface MultiIPUserItem {
  user_id: number
  username: string
  ip_count: number
  request_count: number
  top_ips: Array<{ ip: string; request_count: number }>
}

export interface AltAccountCase {
  case_id: string
  case_type: 'shared_ip' | 'rotating_pool' | 'invite_chain' | 'token_rotation' | string
  case_type_label?: string
  case_key?: string
  primary_ip?: string
  primary_ip_masked?: string
  primary_user_id?: number
  primary_inviter_id?: number
  window: string
  risk_score: number
  risk_level: 'low' | 'medium' | 'high' | 'critical' | string
  risk_labels?: string[]
  risk_reasons?: string[]
  prompt_version?: string
  user_count: number
  request_count?: number
  token_count?: number
  no_topup_count?: number
  first_seen?: number
  last_seen?: number
  case_stats?: Record<string, any>
  users?: Array<Record<string, any>>
  timeline?: Array<Record<string, any>>
  source?: string
}

export interface AltAccountAIResult {
  case_id?: string
  window?: string
  case?: AltAccountCase
  assessment?: Record<string, any>
  model?: string
  usage?: Record<string, any>
  assessed_at?: number
}
