import type {
  ConsensusTextResponse,
  HeightEntry,
  IncidentCard,
  NodeState,
  StatusResponse,
  StoreEvent,
} from './types'

const runtimeBase = (window.__VALDOCTOR_CONFIG__?.apiBaseURL ?? '').trim().replace(/\/$/, '')
const configuredBase = (import.meta.env.VITE_API_BASE_URL ?? '').trim().replace(/\/$/, '')
const apiBase = runtimeBase !== '' ? runtimeBase : configuredBase

function apiURL(path: string): string {
  if (apiBase !== '') {
    return `${apiBase}${path}`
  }
  return path
}

export async function getJSON<T>(path: string): Promise<T> {
  const response = await fetch(apiURL(path), {
    headers: { Accept: 'application/json' },
  })
  if (!response.ok) {
    throw new Error(`Request failed for ${path}: ${response.status}`)
  }
  return response.json() as Promise<T>
}

export function openEventsSocket(onMessage: (event: StoreEvent) => void, onState: (state: 'connecting' | 'open' | 'closed') => void): () => void {
  const wsURL = new URL(apiURL('/api/v1/ws'), window.location.origin)
  wsURL.protocol = wsURL.protocol === 'https:' ? 'wss:' : 'ws:'

  onState('connecting')
  const socket = new WebSocket(wsURL.toString())

  socket.addEventListener('open', () => onState('open'))
  socket.addEventListener('close', () => onState('closed'))
  socket.addEventListener('error', () => onState('closed'))
  socket.addEventListener('message', (message) => {
    try {
      onMessage(JSON.parse(message.data) as StoreEvent)
    } catch {
      onState('closed')
    }
  })

  return () => socket.close()
}

export async function loadOverview() {
  const [status, nodes, activeIncidents, resolvedIncidents, recentHeights] = await Promise.all([
    getJSON<StatusResponse>('/api/v1/status'),
    getJSON<NodeState[]>('/api/v1/nodes'),
    getJSON<IncidentCard[]>('/api/v1/incidents?status=active'),
    getJSON<IncidentCard[]>('/api/v1/incidents?status=resolved&limit=8'),
    getJSON<HeightEntry[]>('/api/v1/heights?limit=32'),
  ])

  return { status, nodes, activeIncidents, resolvedIncidents, recentHeights }
}

export async function loadHeightDetail(height: number) {
  const [entry, consensus] = await Promise.all([
    getJSON<HeightEntry>(`/api/v1/heights/${height}`),
    getJSON<ConsensusTextResponse>(`/api/v1/heights/${height}/consensus-text`),
  ])
  return { entry, consensusText: consensus.text }
}
