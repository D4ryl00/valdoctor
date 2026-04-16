# Valdoctor Live Web UI

React frontend for `valdoctor live`.

It can run in two modes:

- Embedded: built into the Go API server and served from the same origin.
- Standalone: served by its own Node process and connected to a remote `valdoctor live` API.

## Features

- Live dashboard status
- Node table
- Active and resolved incident cards
- Incident search
- Severity filter
- Pause/resume UI updates
- Height detail view
- Consensus tab using backend-rendered height text
- Propagation tab using the live propagation API
- Live refresh over WebSocket

## Requirements

- Node.js 22+
- A running `valdoctor live` backend with `--api-addr`

## Install

```bash
cd web
npm install
```

## Backend

Start the backend first:

```bash
valdoctor live --api-addr :8080 --genesis /path/to/genesis.json --log /path/to/node.log
```

Replace the source flags with your real `live` inputs.

## Development

Run the React app only and point it at a remote API:

```bash
cd web
VITE_API_BASE_URL=http://remote-host:8080 npm run dev
```

Notes:

- `npm run dev` starts Vite with `--host`, so it can be opened from another machine if needed.
- `VITE_API_BASE_URL` must point to the `valdoctor live` API origin, for example `http://validator-monitor.example.com:8080`.

You can also create a local env file:

```bash
cp .env.example .env.local
```

Then set:

```bash
VITE_API_BASE_URL=http://localhost:8080
```

## Standalone Production Build

Build the frontend for standalone hosting:

```bash
cd web
npm run build:standalone
```

This writes static assets to `web/dist`.

Serve that build with the bundled Node server:

```bash
cd web
VALDOCTOR_API_BASE_URL=http://remote-host:8080 npm run serve
```

Environment variables:

- `VALDOCTOR_API_BASE_URL`
  The remote `valdoctor live` API base URL.
- `PORT`
  Port for the standalone UI server. Default: `4173`.
- `HOST`
  Listen host for the standalone UI server. Default: `0.0.0.0`.

Example:

```bash
HOST=0.0.0.0 PORT=4173 VALDOCTOR_API_BASE_URL=http://validator-monitor.example.com:8080 npm run serve
```

Then open:

```text
http://localhost:4173
```

## Embedded Build

Build the frontend for the Go server:

```bash
cd web
npm run build
```

This writes the compiled assets into:

- [internal/api/ui/static](/Users/remi/valdoctor/internal/api/ui/static/index.html)

When built this way, the Go API server serves the UI from `/`.

Example:

```bash
valdoctor live --api-addr :8080 --genesis /path/to/genesis.json --log /path/to/node.log
```

Then open:

```text
http://localhost:8080
```

## API Connectivity

The web UI uses:

- `GET /api/v1/status`
- `GET /api/v1/nodes`
- `GET /api/v1/heights?limit=32`
- `GET /api/v1/heights/{height}`
- `GET /api/v1/heights/{height}/consensus-text`
- `GET /api/v1/incidents?...`
- `GET /api/v1/ws`

The backend now enables cross-origin browser access, so the standalone frontend can call the API remotely.

## Scripts

- `npm run dev`
  Start Vite dev server.
- `npm run build`
  Build for embedded Go serving.
- `npm run build:standalone`
  Build for standalone hosting in `dist/`.
- `npm run preview`
  Preview the Vite build.
- `npm run serve`
  Serve the standalone build with `server.mjs`.

## Troubleshooting

If the page loads but no data appears:

- confirm `valdoctor live` is running with `--api-addr`
- confirm `VITE_API_BASE_URL` or `VALDOCTOR_API_BASE_URL` points to the correct host and port
- confirm the backend is reachable from the browser
- check that `/api/v1/status` responds in a browser or with `curl`
- check that the WebSocket endpoint `/api/v1/ws` is reachable

If you change frontend code, rebuild before using embedded or standalone production mode:

```bash
cd web
npm run build
```

or:

```bash
cd web
npm run build:standalone
```
