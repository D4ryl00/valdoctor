# valdoctor

Offline incident inspection for Gnoland and TM2 validators. Give it a `genesis.json`
and your logs; it tells you what went wrong.

## Install

Install to `$GOPATH/bin` (must be in your `PATH`):

```sh
make install
```

Or build a local binary in `build/valdoctor`:

```sh
make build
```

## Usage

```sh
valdoctor inspect \
  --genesis ./genesis.json \
  --validator-log ./logs/validator.log \
  --sentry-log ./logs/sentry-a.log
```

If you don't know the role of each file, use `--log` for everything:

```sh
valdoctor inspect --genesis ./genesis.json --log ./logs/*
```

For scripting or incident pipelines:

```sh
valdoctor inspect --genesis ./genesis.json --log ./logs/* --format json
```

## Sample output

```
=== valdoctor report ===

chain: test5  validators: 7  logs: 3 file(s)  window: 14:35:00Z → 14:55:46Z

Health summary
- validator-a  peer count max=6 current=0  stalled 10m20s
- sentry-a     peer count max=4 current=4
- validator-b  remote signer unstable: failures=3 reconnects=2

Consensus state (end of window)
- validator-a [validator] height=19497 round=0
  prevotes: 4/7  precommits: 4/7
- sentry-a    [sentry]    height=19498 round=3
  round escalation: max_round=3 at h19498

Findings

[critical] consensus-panic-validator-b: Consensus panic on validator-b
  A CONSENSUS FAILURE!!! panic was logged. The node process terminated.
  evidence: [validator-b] CONSENSUS FAILURE!!! err=runtime error: ...
  possible cause: node joined consensus mid-round via fast-sync without the proposal block
  suggested: restart the node after resolving the underlying issue

[high] stall-after-last-commit-validator-a: validator-a: no commits for 10m20s after height 19497
  Last commit at h19497; no further commits observed for 10m20s. Average block time was 2s.
  evidence: [validator-b] consensus panic at h19498
  possible cause: quorum loss caused by other validators also failing at h19498:
    1 crashed (validator-b) — if their combined voting power exceeds 1/3, consensus cannot proceed
  suggested: investigate why 1 other validator(s) also failed at h19498

[medium] round-escalation-sentry-a: sentry-a reached round 3 at height 19498
  Consensus at height 19498 required at least 3 round(s) before committing or stalling.
  suggested: examine logs from all validators around height 19498
```

## Live monitoring

`valdoctor live` ingests running node logs in real time, detects incidents as
they happen, and exposes a REST/WebSocket API and TUI for live inspection.

```sh
valdoctor live \
  --genesis ./genesis.json \
  --validator-log /var/log/gnoland/val1.log
```

### Log sources

Three source types can be mixed freely in a single invocation.
Each flag may be repeated.

#### Local files

```sh
valdoctor live --genesis ./genesis.json \
  --validator-log /var/log/gnoland/val1.log \
  --validator-log /var/log/gnoland/val2.log \
  --sentry-log    /var/log/gnoland/sentry.log
```

Use `--log` when the role (validator / sentry) is not yet known; valdoctor will
infer it from the log content.

The file source watches the file with `fsnotify` and falls back to polling.
Log rotation is detected automatically.
Gzip-compressed files (`.gz`) are read in one shot rather than followed.

#### Local Docker containers

```sh
valdoctor live --genesis ./genesis.json \
  --validator-docker val1-container \
  --validator-docker val2-container \
  --sentry-docker    sentry-container
```

Uses `docker logs --follow` under the hood.

#### External commands (`--validator-cmd` / `--sentry-cmd` / `--cmd`)

Any command whose **stdout** is a stream of gnoland log lines can be used as a
source. The flag format is `name=command arg1 arg2 ...`:

```sh
# SSH + journalctl (systemd)
valdoctor live --genesis ./genesis.json \
  --validator-cmd "val1=ssh user@server1 journalctl -f -u gnoland --output=short-iso"

# SSH + tail -f
valdoctor live --genesis ./genesis.json \
  --validator-cmd "val1=ssh user@server1 tail -f /var/log/gnoland/gnoland.log"
```

No shell expansion is applied to the command string — arguments are split on
whitespace. For pipelines or quoting, wrap the command in a script and pass the
script path as the single argument.

### Remote Docker containers

#### Single remote server

Point the local Docker client at the remote daemon over SSH; the existing
`--validator-docker` flag then works unchanged:

```sh
# One-off via env var
DOCKER_HOST="ssh://user@remote-server" valdoctor live \
  --genesis ./genesis.json \
  --validator-docker my-gnoland-container

# Permanent via Docker context
docker context create remote --docker "host=ssh://user@remote-server"
docker context use remote
valdoctor live --genesis ./genesis.json \
  --validator-docker my-gnoland-container
```

This uses `DockerSource`, which handles `--since` bootstrapping and Docker
timestamp stripping correctly.

#### Multiple remote servers

`DOCKER_HOST` is a global env var, so you cannot point different sources at
different hosts in a single invocation. Use `--validator-cmd` instead:

```sh
valdoctor live --genesis ./genesis.json \
  --validator-cmd "val1=ssh user@server1 docker logs --follow gnoland-val1" \
  --validator-cmd "val2=ssh user@server2 docker logs --follow gnoland-val2" \
  --validator-cmd "val3=ssh user@server3 docker logs --follow gnoland-val3"
```

> Use `docker logs --follow` **without** `--timestamps` here. The `DockerSource`
> adds `--timestamps` itself so it can strip the extra prefix; `CmdSource` passes
> lines straight to the parser, which expects the raw gnoland log format.

Alternatively use Docker's per-command `--host` flag to avoid an SSH subprocess:

```sh
--validator-cmd "val1=docker --host ssh://user@server1 logs --follow gnoland-val1"
```

#### SSH options

Extra SSH flags go directly in the command string:

```sh
--validator-cmd "val1=ssh -i /home/ops/.ssh/id_ed25519 -p 2222 user@server1 docker logs --follow gnoland-val1"
```

### Naming and roles

For `--validator-cmd` and `--sentry-cmd` the node name is the part before `=`
in the flag value, so no additional naming is needed.

For file and Docker sources, use `--node` to assign a human-readable name:

```sh
--node "paris-validator=/var/log/gnoland/val.log"
--node "paris-validator=docker:val-container"
```

Override the role inferred from the flag type with `--role`:

```sh
--role "paris-validator=validator"
```

Or use a TOML metadata file (see **Metadata** below) to persist names, roles,
and topology across runs.

### Options reference

| Flag | Default | Description |
|------|---------|-------------|
| `--genesis` | *(required)* | Path to `genesis.json` |
| `--since` | tail from now | Bootstrap from this RFC3339 timestamp |
| `--max-history` | `500` | Number of recent heights kept in memory |
| `--closure-policy` | `single_validator_commit` | When a height is considered complete (see below) |
| `--propagation-grace` | `5s` | Extra window after height closes to collect propagation data |
| `--db` | *(in-memory)* | SQLite path for persistent live state across restarts |
| `--api-addr` | *(disabled)* | HTTP address for the live REST/WebSocket API, e.g. `:8080` |
| `--no-tui` | `false` | Headless mode; prints events to stdout |
| `--metadata` | | TOML metadata file (may be repeated) |

### Closure policy

The closure policy controls when valdoctor considers a block height "done"
and runs propagation analysis on it.

| Policy | Closes when… | Use for… |
|--------|-------------|----------|
| `single_validator_commit` | any one validator reports `FinalizeCommit` | default; works with any number of log sources, including a single one or unequal voting power |
| `observed_validator_majority` | ⌊N×2/3⌋+1 validators (by count) report it | equal-power networks where you have logs from all validators |
| `observed_all_validator_sources` | every configured validator source reports it | tight propagation analysis with full visibility |

If one validator holds a dominant share of voting power (e.g. scenario-11 style),
use `single_validator_commit` — the count-based majority policy will never close
heights when only that one validator's logs are available.

## Recommendations

**Use JSON logs.** Pass `--log-format json` to `gnoland start`.
Console logs are supported but field extraction is best-effort.

**Enable debug-level logging.** Pass `--log-level debug` to `gnoland start` to get VoteSet quorum
tracking (how many prevotes and precommits were received per round) and richer
consensus state in the report. Without it, quorum analysis falls back to
coarser event counts.

**Provide logs from all validators when possible.** Many findings are downgraded
to lower confidence when only one node's logs are available. Cross-node correlation
(stall root cause, quorum loss attribution) requires at least two nodes.

**Use a metadata file for recurring incidents.** The first time, let `valdoctor`
generate one from your logs:

```sh
valdoctor inspect \
  --genesis ./genesis.json \
  --log ./logs/* \
  --generate-metadata ./valdoctor-meta.toml
```

Edit it to add topology (which sentries serve which validators) and re-use it:

```sh
valdoctor inspect \
  --genesis ./genesis.json \
  --metadata ./valdoctor-meta.toml \
  --log ./logs/*
```

This unlocks topology-aware findings (e.g. "validator-a lost its connection to
sentry-b while sentry-b remained reachable").

**Narrow the window when logs are large.** Use `--since` and `--until` to focus
on the incident:

```sh
valdoctor inspect \
  --genesis ./genesis.json \
  --log ./logs/* \
  --since 2026-03-20T14:00:00Z \
  --until 2026-03-20T15:00:00Z
```

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | No critical issue |
| 1 | At least one critical finding |
| 2 | Input error |
| 3 | Too few classifiable events to draw conclusions |

## Config

```sh
valdoctor config init            # write default config
valdoctor config set format json # persist a flag default
```

See `docs/resources/doctor-cli-spec.md` for the full specification.
