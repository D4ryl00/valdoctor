import {
  startTransition,
  useDeferredValue,
  useEffect,
  useEffectEvent,
  useState,
} from 'react'
import type { ReactNode } from 'react'

import { loadHeightDetail, loadOverview, openEventsSocket } from './api'
import type {
  HeightEntry,
  IncidentCard,
  NodeState,
  Severity,
  StatusResponse,
  VoteReceipt,
  VotePropagationRow,
} from './types'

type DetailTab = 'consensus' | 'propagation'
type SocketState = 'connecting' | 'open' | 'closed'
type IncidentBucket = 'active' | 'resolved'

const severityOptions: Array<{ label: string; value: Severity | '' }> = [
  { label: 'All severities', value: '' },
  { label: 'Critical', value: 'critical' },
  { label: 'High', value: 'high' },
  { label: 'Medium', value: 'medium' },
  { label: 'Low', value: 'low' },
  { label: 'Info', value: 'info' },
]

export default function App() {
  const [status, setStatus] = useState<StatusResponse | null>(null)
  const [nodes, setNodes] = useState<NodeState[]>([])
  const [activeIncidents, setActiveIncidents] = useState<IncidentCard[]>([])
  const [resolvedIncidents, setResolvedIncidents] = useState<IncidentCard[]>([])
  const [recentHeights, setRecentHeights] = useState<HeightEntry[]>([])
  const [selectedHeight, setSelectedHeight] = useState<number | null>(null)
  const [selectedIncidentID, setSelectedIncidentID] = useState<string | null>(null)
  const [detailEntry, setDetailEntry] = useState<HeightEntry | null>(null)
  const [consensusText, setConsensusText] = useState('')
  const [detailTab, setDetailTab] = useState<DetailTab>('consensus')
  const [search, setSearch] = useState('')
  const [severityFilter, setSeverityFilter] = useState<Severity | ''>('')
  const [paused, setPaused] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [socketState, setSocketState] = useState<SocketState>('connecting')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const deferredSearch = useDeferredValue(search)

  const refreshDetail = useEffectEvent(async (height: number) => {
    try {
      const next = await loadHeightDetail(height)
      startTransition(() => {
        if (selectedHeight !== null && selectedHeight !== height) {
          return
        }
        setDetailEntry(next.entry)
        setConsensusText(next.consensusText)
        setError(null)
      })
    } catch (nextError) {
      setError(errorMessage(nextError))
    }
  })

  const refreshOverview = useEffectEvent(async () => {
    try {
      const snapshot = await loadOverview()
      startTransition(() => {
        setStatus(snapshot.status)
        setNodes(snapshot.nodes)
        setActiveIncidents(snapshot.activeIncidents)
        setResolvedIncidents(snapshot.resolvedIncidents)
        setRecentHeights(snapshot.recentHeights)
        setLoading(false)
        setError(null)

        const availableHeights = snapshot.recentHeights.map((entry) => entry.height)
        const preferredHeight =
          selectedHeight !== null && availableHeights.includes(selectedHeight)
            ? selectedHeight
            : availableHeights[0] ?? null

        if (preferredHeight !== selectedHeight) {
          setSelectedHeight(preferredHeight)
          if (preferredHeight === null) {
            setDetailEntry(null)
            setConsensusText('')
            setSelectedIncidentID(null)
          }
        }
      })
    } catch (nextError) {
      setLoading(false)
      setError(errorMessage(nextError))
    }
  })

  const handleRealtimeUpdate = useEffectEvent(() => {
    if (paused) {
      setDirty(true)
      return
    }

    void refreshOverview()
    if (selectedHeight !== null) {
      void refreshDetail(selectedHeight)
    }
  })

  useEffect(() => {
    void refreshOverview()
  }, [refreshOverview])

  useEffect(() => {
    if (selectedHeight === null) {
      setDetailEntry(null)
      setConsensusText('')
      return
    }
    void refreshDetail(selectedHeight)
  }, [refreshDetail, selectedHeight])

  useEffect(() => {
    if (paused || !dirty) {
      return
    }
    setDirty(false)
    void refreshOverview()
    if (selectedHeight !== null) {
      void refreshDetail(selectedHeight)
    }
  }, [dirty, paused, refreshDetail, refreshOverview, selectedHeight])

  useEffect(() => {
    let reconnectTimer = 0
    let closed = false
    let currentDispose = () => {}

    const connect = () => {
      currentDispose = openEventsSocket(
        () => handleRealtimeUpdate(),
        (nextState) => {
          setSocketState(nextState)
          if (nextState === 'closed' && !closed) {
            reconnectTimer = window.setTimeout(connect, 1500)
          }
        },
      )
    }

    connect()
    return () => {
      closed = true
      window.clearTimeout(reconnectTimer)
      currentDispose()
    }
  }, [handleRealtimeUpdate])

  const filteredActiveIncidents = activeIncidents.filter((card) =>
    matchesIncident(card, severityFilter, deferredSearch),
  )
  const filteredResolvedIncidents = resolvedIncidents.filter((card) =>
    matchesIncident(card, severityFilter, deferredSearch),
  )

  const openIncident = (card: IncidentCard) => {
    setSelectedIncidentID(card.id)
    setSelectedHeight(card.last_height)
    setDetailTab('consensus')
  }

  const openHeight = (height: number) => {
    setSelectedIncidentID(null)
    setSelectedHeight(height)
  }

  const navigateHeight = (direction: 1 | -1) => {
    if (recentHeights.length === 0 || selectedHeight === null) {
      return
    }
    const currentIndex = recentHeights.findIndex((entry) => entry.height === selectedHeight)
    const fallbackIndex = currentIndex === -1 ? 0 : currentIndex
    const nextIndex = Math.max(0, Math.min(recentHeights.length - 1, fallbackIndex + direction))
    openHeight(recentHeights[nextIndex].height)
  }

  return (
    <div className="app-shell">
      <header className="hero">
        <div className="hero__copy">
          <p className="eyebrow">Valdoctor Live</p>
          <h1>Live consensus diagnostics for running validators.</h1>
          <p className="hero__summary">
            Incident-driven monitoring with the same live state as the CLI, now backed by the
            `valdoctor live` REST and WebSocket API.
          </p>
        </div>

        <div className="hero__status">
          <StatusCard label="Chain" value={status?.chain_id ?? 'waiting'} accent="sand" />
          <StatusCard label="Tip" value={status ? `h${status.tip}` : '—'} accent="blue" />
          <StatusCard label="Nodes" value={String(status?.node_count ?? 0)} accent="teal" />
          <StatusCard
            label="Connection"
            value={paused ? 'paused' : socketState}
            accent={paused ? 'gold' : socketState === 'open' ? 'green' : 'rose'}
          />
        </div>
      </header>

      <section className="toolbar">
        <label className="search-field">
          <span>Search incidents</span>
          <input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="panic, validator-a, propagation..."
          />
        </label>

        <div className="toolbar__controls">
          <label className="select-field">
            <span>Severity</span>
            <select
              value={severityFilter}
              onChange={(event) => setSeverityFilter(event.target.value as Severity | '')}
            >
              {severityOptions.map((option) => (
                <option key={option.label} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
          </label>

          <button
            className={paused ? 'button button--primary' : 'button'}
            onClick={() => setPaused((current) => !current)}
            type="button"
          >
            {paused ? 'Resume live updates' : 'Pause updates'}
          </button>
        </div>
      </section>

      {(error !== null || dirty) && (
        <section className="notice-row">
          {error !== null && <div className="notice notice--error">{error}</div>}
          {dirty && paused && (
            <div className="notice notice--warning">New backend events arrived while updates were paused.</div>
          )}
        </section>
      )}

      <main className="layout">
        <section className="column column--dashboard">
          <Panel
            title="Node table"
            subtitle="Highest commit, peer health, round escalation, and signer instability."
          >
            <NodeTable nodes={nodes} tip={status?.tip ?? 0} />
          </Panel>

          <Panel
            title="Active incidents"
            subtitle={`${filteredActiveIncidents.length} visible incident card(s) in the current filter.`}
          >
            <IncidentList
              incidents={filteredActiveIncidents}
              bucket="active"
              selectedIncidentID={selectedIncidentID}
              onOpen={openIncident}
            />
          </Panel>

          <Panel
            title="Recent resolved"
            subtitle={`${filteredResolvedIncidents.length} incident card(s) recently cleared.`}
          >
            <IncidentList
              incidents={filteredResolvedIncidents}
              bucket="resolved"
              selectedIncidentID={selectedIncidentID}
              onOpen={openIncident}
            />
          </Panel>
        </section>

        <section className="column column--detail">
          <Panel
            title={selectedHeight !== null ? `Height h${selectedHeight}` : 'Height detail'}
            subtitle={
              detailEntry !== null
                ? `${statusLabel(detailEntry.status)} · updated ${formatTime(detailEntry.last_updated)}`
                : 'Open an incident card or recent height to inspect the live projection.'
            }
            headerAction={
              <div className="detail-actions">
                <button className="button" onClick={() => navigateHeight(-1)} type="button">
                  Previous height
                </button>
                <button className="button" onClick={() => navigateHeight(1)} type="button">
                  Next height
                </button>
              </div>
            }
          >
            <div className="height-strip">
              {recentHeights.length === 0 && <span className="chip chip--muted">No retained heights</span>}
              {recentHeights.map((entry) => (
                <button
                  key={entry.height}
                  className={entry.height === selectedHeight ? 'chip chip--selected' : 'chip'}
                  onClick={() => openHeight(entry.height)}
                  type="button"
                >
                  h{entry.height}
                </button>
              ))}
            </div>

            <div className="tab-row">
              <button
                className={detailTab === 'consensus' ? 'tab tab--active' : 'tab'}
                onClick={() => setDetailTab('consensus')}
                type="button"
              >
                Consensus
              </button>
              <button
                className={detailTab === 'propagation' ? 'tab tab--active' : 'tab'}
                onClick={() => setDetailTab('propagation')}
                type="button"
              >
                Propagation
              </button>
            </div>

            {loading && detailEntry === null && <div className="empty-state">Loading live snapshot…</div>}
            {!loading && detailEntry === null && (
              <div className="empty-state">
                No retained height is selected. Wait for a closed height or choose a recent incident once data
                arrives.
              </div>
            )}

            {detailEntry !== null && detailTab === 'consensus' && (
              <div className="detail-stack">
                <SummaryRibbon entry={detailEntry} />
                <pre className="consensus-text">{consensusText || 'Loading rendered consensus narrative…'}</pre>
              </div>
            )}

            {detailEntry !== null && detailTab === 'propagation' && (
              <PropagationTable votes={detailEntry.propagation.votes} />
            )}
          </Panel>
        </section>
      </main>
    </div>
  )
}

function StatusCard(props: { label: string; value: string; accent: string }) {
  return (
    <article className={`status-card status-card--${props.accent}`}>
      <span>{props.label}</span>
      <strong>{props.value}</strong>
    </article>
  )
}

function Panel(props: {
  title: string
  subtitle: string
  headerAction?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="panel">
      <header className="panel__header">
        <div>
          <h2>{props.title}</h2>
          <p>{props.subtitle}</p>
        </div>
        {props.headerAction}
      </header>
      {props.children}
    </section>
  )
}

function NodeTable(props: { nodes: NodeState[]; tip: number }) {
  if (props.nodes.length === 0) {
    return <div className="empty-state">No node state yet. Waiting for classified live events.</div>
  }

  return (
    <div className="table-wrap">
      <table className="node-table">
        <thead>
          <tr>
            <th>Node</th>
            <th>Role</th>
            <th>Commit</th>
            <th>Peers</th>
            <th>Round</th>
            <th>Signer</th>
            <th>Stall</th>
          </tr>
        </thead>
        <tbody>
          {props.nodes.map(({ summary }) => (
            <tr key={summary.name}>
              <td>{summary.name}</td>
              <td>{summary.role}</td>
              <td>h{summary.highest_commit}</td>
              <td>
                {summary.current_peers}/{summary.max_peers}
              </td>
              <td>
                r{summary.max_round_seen} @ h{summary.max_round_height}
              </td>
              <td>
                {summary.signer_failure_count}/{summary.signer_connect_count}
              </td>
              <td>
                {summary.stall_duration_ns && summary.highest_commit < props.tip
                  ? `${formatDuration(summary.stall_duration_ns)}`
                  : '—'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function IncidentList(props: {
  incidents: IncidentCard[]
  bucket: IncidentBucket
  selectedIncidentID: string | null
  onOpen: (card: IncidentCard) => void
}) {
  if (props.incidents.length === 0) {
    return <div className="empty-state">No incidents match the current filter.</div>
  }

  return (
    <div className="incident-list">
      {props.incidents.map((card) => (
        <button
          key={card.id}
          className={props.selectedIncidentID === card.id ? 'incident incident--selected' : 'incident'}
          onClick={() => props.onOpen(card)}
          type="button"
        >
          <div className="incident__meta">
            <span className={`pill pill--${props.bucket}`}>{props.bucket}</span>
            <span className={`pill pill--severity-${card.severity}`}>{card.severity}</span>
            <span className="pill pill--scope">{card.scope}</span>
          </div>
          <strong>{card.title}</strong>
          <p>{card.summary}</p>
          <div className="incident__footer">
            <span>
              h{card.first_height} to h{card.last_height}
            </span>
            <span>{formatTime(card.updated_at)}</span>
          </div>
        </button>
      ))}
    </div>
  )
}

function SummaryRibbon(props: { entry: HeightEntry }) {
  const rounds = props.entry.report.rounds ?? []
  const committedRounds = rounds.filter((round) => round.committed).length
  const warnings = props.entry.report.warnings ?? []

  return (
    <div className="summary-ribbon">
      <div>
        <span>Rounds observed</span>
        <strong>{rounds.length}</strong>
      </div>
      <div>
        <span>Committed rounds</span>
        <strong>{committedRounds}</strong>
      </div>
      <div>
        <span>Warnings</span>
        <strong>{warnings.length}</strong>
      </div>
      <div>
        <span>Propagation rows</span>
        <strong>{props.entry.propagation.votes.length}</strong>
      </div>
    </div>
  )
}

function PropagationTable(props: { votes: VotePropagationRow[] }) {
  if (props.votes.length === 0) {
    return <div className="empty-state">No propagation data for this height yet.</div>
  }

  const receivers = Array.from(
    new Set(props.votes.flatMap((vote) => vote.receipts.map((receipt) => receipt.receiver))),
  ).sort((left, right) => left.localeCompare(right))

  return (
    <div className="table-wrap">
      <table className="propagation-table">
        <thead>
          <tr>
            <th>Vote</th>
            <th>Origin</th>
            {receivers.map((receiver) => (
              <th key={receiver}>{receiver}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {props.votes.map((vote) => (
            <tr key={`${vote.height}-${vote.round}-${vote.vote_type}-${vote.origin_node}-${vote.origin_short_addr}`}>
              <td>
                r{vote.round} {shortVoteType(vote.vote_type)}
              </td>
              <td>{vote.origin_node}</td>
              {receivers.map((receiver) => (
                <td key={receiver}>
                  <ReceiptCell receipt={vote.receipts.find((candidate) => candidate.receiver === receiver)} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function ReceiptCell(props: { receipt?: VoteReceipt }) {
  if (!props.receipt || props.receipt.status === '') {
    return <span className="receipt receipt--pending">pending</span>
  }

  const tone = props.receipt.status === 'ok' ? 'ok' : props.receipt.status === 'late' ? 'late' : props.receipt.status === 'missing' ? 'missing' : 'unknown'
  const duration =
    props.receipt.latency_ns && props.receipt.latency_ns > 0 ? ` ${formatDuration(props.receipt.latency_ns)}` : ''

  return <span className={`receipt receipt--${tone}`}>{`${props.receipt.status}${duration}`}</span>
}

function matchesIncident(card: IncidentCard, severity: Severity | '', query: string): boolean {
  if (severity !== '' && card.severity !== severity) {
    return false
  }
  if (query.trim() === '') {
    return true
  }

  const haystack = [card.id, card.kind, card.scope, card.title, card.summary].join(' ').toLowerCase()
  return haystack.includes(query.trim().toLowerCase())
}

function formatDuration(ns: number): string {
  if (ns >= 1_000_000_000) {
    return `${(ns / 1_000_000_000).toFixed(2)}s`
  }
  if (ns >= 1_000_000) {
    return `${(ns / 1_000_000).toFixed(1)}ms`
  }
  if (ns >= 1_000) {
    return `${Math.round(ns / 1_000)}µs`
  }
  return `${ns}ns`
}

function formatTime(value: string | undefined): string {
  if (!value) {
    return 'unknown'
  }

  const parsed = new Date(value)
  if (Number.isNaN(parsed.valueOf())) {
    return value
  }

  return parsed.toLocaleString([], {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

function statusLabel(status: number): string {
  switch (status) {
    case 1:
      return 'closed'
    case 2:
      return 'evicted'
    default:
      return 'active'
  }
}

function shortVoteType(voteType: string): string {
  if (voteType === 'prevote') {
    return 'pv'
  }
  if (voteType === 'precommit') {
    return 'pc'
  }
  return voteType
}

function errorMessage(value: unknown): string {
  if (value instanceof Error) {
    return value.message
  }
  return 'Unexpected frontend error'
}
