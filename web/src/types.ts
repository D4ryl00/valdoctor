export type Severity = 'info' | 'low' | 'medium' | 'high' | 'critical'

export type StoreEvent = {
  kind: 'height_updated' | 'node_updated' | 'incident_updated'
  height?: number
  node?: string
  incident_id?: string
}

export type StatusResponse = {
  chain_id: string
  status: string
  tip: number
  node_count: number
  active_incident_count: number
  recent_height_count: number
}

export type NodeSummary = {
  name: string
  role: string
  highest_commit: number
  current_peers: number
  max_peers: number
  max_round_seen: number
  max_round_height: number
  signer_failure_count: number
  signer_connect_count: number
  stall_duration_ns?: number
  last_commit_time?: string
}

export type NodeState = {
  summary: NodeSummary
  updated_at: string
}

export type IncidentCard = {
  id: string
  kind: string
  severity: Severity
  status: 'active' | 'resolved'
  scope: string
  title: string
  summary: string
  first_height: number
  last_height: number
  updated_at: string
}

export type RoundSummary = {
  round: number
  proposal_seen: boolean
  proposal_hash?: string
  proposal_from_round?: number
  proposal_valid: boolean
  proposal_received_late: boolean
  proposal_late_time_str?: string
  proposer_addr?: string
  prevote_narrative: string
  precommit_narrative: string
  committed: boolean
  timed_out: boolean
}

export type HeightReport = {
  height: number
  chain_id: string
  rounds: RoundSummary[]
  warnings?: string[]
  committed_in_log?: boolean
  double_sign_detected?: boolean
}

export type VoteReceipt = {
  receiver: string
  cast_at?: string
  received_at?: string
  latency_ns?: number
  status?: string
}

export type VotePropagationRow = {
  height: number
  round: number
  vote_type: string
  origin_node: string
  origin_short_addr?: string
  receipts: VoteReceipt[]
}

export type VotePropagation = {
  height: number
  votes: VotePropagationRow[]
}

export type HeightEntry = {
  height: number
  status: number
  report: HeightReport
  propagation: VotePropagation
  last_updated: string
}

export type ConsensusTextResponse = {
  text: string
}
