package parse

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
)

var (
	containerPrefixRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+\s+\|\s+`)
	ansiRE            = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	heightRoundRE     = regexp.MustCompile(`\((\d+)\/(-?\d+)\)`)
	voteSetRE         = regexp.MustCompile(`\+2/3:([^(]+)\([^)]+\) BA\{(\d+):([x_]+)\}`)
	digitSeqRE        = regexp.MustCompile(`\d+`)
	// hexSeqRE matches hex strings that contain at least one letter (a-f/A-F),
	// ensuring pure digit sequences always fall through to digitSeqRE instead.
	// The alternation covers letter-first and letter-last with 8+ total chars.
	hexSeqRE   = regexp.MustCompile(`[0-9A-Fa-f]*[A-Fa-f][0-9A-Fa-f]{7,}|[0-9A-Fa-f]{7,}[A-Fa-f][0-9A-Fa-f]*`)
	bitArrayRE = regexp.MustCompile(`BA\{[^}]*\}`)
	// timestampRE matches ISO 8601 timestamps so they are collapsed before hex/digit rules run.
	timestampRE  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)
	whitespaceRE = regexp.MustCompile(`\s+`)

	// Regexes for extracting peer gossip state (vote messages and round-step updates).
	// peerVoteRE extracts block height from [Vote Vote{VI:ADDR HEIGHT/ROUND/TYPE ...}].
	peerVoteRE = regexp.MustCompile(`\[Vote Vote\{\d+:[0-9A-Fa-f]+ (\d+)/`)
	// peerNRSRE extracts height, round, step from [NewRoundStep H:X R:Y S:Z ...].
	peerNRSRE = regexp.MustCompile(`\[NewRoundStep H:(\d+) R:(\d+) S:(\w+)`)
	// peerAddrRE extracts the bech32 peer address from "Peer{MConn{...} g1xxx in/out}".
	peerAddrRE = regexp.MustCompile(`\} (g1[a-z0-9]{38,}) `)
	// voteDetailRE extracts validator index, address fingerprint, and block hash
	// from "Added to prevote/precommit" messages (console/VoteSet inline format).
	// TM2 vote string format: Vote{IDX:ADDRSHORT HEIGHT/ROUND[/TYPE](TypeName) HASH}
	// ADDRSHORT is the first 6 bytes of the validator address (12 hex chars).
	// HASH is a hex string or "<nil>" for nil votes.
	// The optional /TYPE integer before (TypeName) is present in some gnoland versions.
	voteDetailRE = regexp.MustCompile(`Vote\{(\d+):([0-9A-Fa-f]+) \d+/\d+(?:/\d+)?\(\w+\) ([0-9A-Fa-f]+|<nil>)`)
	// voteReceiveRE extracts validator index, address fingerprint, height, round,
	// type name, and block hash from "Receive" (consensus) messages.
	// Format: [Vote Vote{IDX:ADDRSHORT HEIGHT/ROUND/TYPE(TypeName) BLOCKHASH SIG @ TS}]
	voteReceiveRE = regexp.MustCompile(`\[Vote Vote\{(\d+):([0-9A-Fa-f]+) (\d+)/(\d+)/\d+\((\w+)\) ([0-9A-Fa-f]+)`)
	// timeoutStepRE extracts the step name from "Timed out" messages when the
	// step is embedded in the message text rather than a structured field.
	timeoutStepRE = regexp.MustCompile(`(?i)step[=:\s]+(\w+)`)
)

// ParseStats holds aggregated peer-gossip data extracted during parsing.
// Vote and round-step messages are far too numerous to store individually;
// only summaries are tracked.
type ParseStats struct {
	// MaxPeerVoteHeight is the highest block height for which any vote was
	// received from a peer.  If this equals the node's last commit height at
	// the end of a stall, no validator cast any votes after that block.
	MaxPeerVoteHeight int64
	// PeerRoundStates maps each peer's p2p address (g1xxx…) to its last-known
	// consensus state, inferred from received [NewRoundStep …] gossip messages.
	PeerRoundStates map[string]model.PeerRoundState
	// StuckHeight is the highest rs.Height seen in "No votes to send, sleeping"
	// gossip logs.  When greater than the last observed FinalizeCommit height
	// it reveals that the chain actually committed more blocks than the parsed
	// events show — the missing commits are in a log window not provided.
	StuckHeight int64
}

func ParseLogFile(source model.Source, r io.Reader) ([]model.Event, []string, map[string]model.UnclassifiedEntry, ParseStats, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	events := make([]model.Event, 0)
	warnings := make([]string, 0)
	unclassified := map[string]model.UnclassifiedEntry{}
	stats := ParseStats{PeerRoundStates: map[string]model.PeerRoundState{}}
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		if raw == "" {
			continue
		}
		event, warning := ParseLogLine(source, raw, lineNo)
		collectPeerGossip(event, &stats)
		if warning != "" {
			if event.Kind == model.EventUnknown {
				key := NormalizeMessage(event.Message)
				entry := unclassified[key]
				entry.Count++
				if entry.Count == 1 {
					entry.Message = key
					entry.FirstPath = source.Path
					entry.FirstLine = lineNo
				}
				unclassified[key] = entry
			} else {
				warnings = append(warnings, warning)
			}
		}
		if event.Kind == model.EventUnknown || event.Kind == model.EventKnownNoise {
			continue
		}
		events = append(events, event)
	}

	return events, warnings, unclassified, stats, scanner.Err()
}

// collectPeerGossip extracts peer consensus state from p2p gossip messages that
// are otherwise dropped as known noise. Only [Vote ...], [NewRoundStep ...], and
// "No votes to send, sleeping" messages are inspected; everything else is skipped.
func collectPeerGossip(event model.Event, stats *ParseStats) {
	msg := event.Message
	if fieldsMsg, ok := event.Fields["msg"].(string); ok {
		if strings.HasPrefix(fieldsMsg, "[NewRoundStep ") || strings.HasPrefix(fieldsMsg, "[Vote ") {
			msg = fieldsMsg
		}
	}
	if strings.Contains(msg, "No votes to send") {
		// rs.Height reveals the height the node is currently stuck trying to commit.
		// JSON numbers decode as float64; convert carefully.
		if h, ok := jsonInt64Field(event.Fields, "rs.Height"); ok && h > stats.StuckHeight {
			stats.StuckHeight = h
		}
		return
	}
	if strings.HasPrefix(msg, "[Vote ") {
		if m := peerVoteRE.FindStringSubmatch(msg); m != nil {
			if h, err := strconv.ParseInt(m[1], 10, 64); err == nil && h > stats.MaxPeerVoteHeight {
				stats.MaxPeerVoteHeight = h
			}
		}
		return
	}
	if strings.HasPrefix(msg, "[NewRoundStep ") {
		m := peerNRSRE.FindStringSubmatch(msg)
		if m == nil {
			return
		}
		h, _ := strconv.ParseInt(m[1], 10, 64)
		r, _ := strconv.Atoi(m[2])
		step := m[3]
		peer := extractPeerAddr(event.Fields)
		if peer == "" || h == 0 {
			return
		}
		existing, ok := stats.PeerRoundStates[peer]
		if !ok || h > existing.Height || (h == existing.Height && r > existing.Round) {
			stats.PeerRoundStates[peer] = model.PeerRoundState{Peer: peer, Height: h, Round: r, Step: step}
		}
	}
}

// CollectPeerGossip updates ParseStats from a parsed event.
// It is used by live mode, where known-noise events are not stored but still
// need to contribute peer-gossip state.
func CollectPeerGossip(event model.Event, stats *ParseStats) {
	collectPeerGossip(event, stats)
}

// extractPeerAddr pulls the bech32 peer address (g1xxx…) from the "src" field
// that TM2 attaches to Receive/Send log entries.
// The field value looks like: "Peer{MConn{IP:PORT} g1xxx... in}" or similar.
func extractPeerAddr(fields map[string]any) string {
	if fields == nil {
		return ""
	}
	src, _ := fields["src"].(string)
	if src == "" {
		return ""
	}
	if m := peerAddrRE.FindStringSubmatch(src); m != nil {
		return m[1]
	}
	return ""
}

// jsonInt64Field reads a numeric field from a map[string]any (where JSON numbers
// decode as float64) and returns it as int64. Returns (0, false) when absent or
// not a number.
func jsonInt64Field(fields map[string]any, key string) (int64, bool) {
	if fields == nil {
		return 0, false
	}
	v, ok := fields[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}

func ParseLogLine(source model.Source, raw string, lineNo int) (model.Event, string) {
	clean := containerPrefixRE.ReplaceAllString(raw, "")
	clean = ansiRE.ReplaceAllString(clean, "")

	switch {
	case strings.HasPrefix(strings.TrimSpace(clean), "{"):
		return parseJSONLine(source, clean, lineNo)
	case looksLikeTimestamp(clean):
		return parseConsoleLine(source, clean, lineNo)
	default:
		event := baseEvent(source, lineNo)
		event.Format = "raw"
		event.Message = strings.TrimSpace(clean)
		event.Kind = classifyMessage(event.Message)
		enrichEvent(&event)
		if event.Kind == model.EventUnknown {
			return event, fmt.Sprintf("%s:%d: unclassified raw line", source.Path, lineNo)
		}
		return event, ""
	}
}

func parseJSONLine(source model.Source, clean string, lineNo int) (model.Event, string) {
	event := baseEvent(source, lineNo)
	event.Format = "json"

	var payload map[string]any
	if err := json.Unmarshal([]byte(clean), &payload); err != nil {
		event.Kind = model.EventParserWarning
		event.Message = strings.TrimSpace(clean)
		return event, fmt.Sprintf("%s:%d: invalid json log line: %v", source.Path, lineNo, err)
	}

	// Unwrap Docker/container log format: {"log":"...\n","stream":"...","time":"..."}
	if logField, ok := payload["log"].(string); ok {
		inner := strings.TrimSpace(logField)
		if strings.HasPrefix(inner, "{") {
			// Inner payload is also JSON — unwrap and continue.
			var innerPayload map[string]any
			if err := json.Unmarshal([]byte(inner), &innerPayload); err == nil {
				payload = innerPayload
			}
		} else if looksLikeTimestamp(inner) {
			// Inner payload is a console-format line — delegate entirely.
			return parseConsoleLine(source, inner, lineNo)
		}
	}

	if ts, ok := payload["ts"].(float64); ok {
		sec := int64(ts)
		nsec := int64((ts - float64(sec)) * float64(time.Second))
		event.Timestamp = time.Unix(sec, nsec).UTC()
		event.HasTimestamp = true
	}
	if level, ok := payload["level"].(string); ok {
		event.Level = strings.ToLower(level)
	}
	if msg, ok := payload["msg"].(string); ok {
		event.Message = msg
	}
	delete(payload, "ts")
	delete(payload, "level")
	delete(payload, "msg")
	if len(payload) > 0 {
		event.Fields = payload
	}
	event.Kind = classifyMessage(event.Message)
	enrichEvent(&event)
	if event.Kind == model.EventUnknown {
		return event, fmt.Sprintf("%s:%d: unclassified json message %q", source.Path, lineNo, event.Message)
	}
	return event, ""
}

func parseConsoleLine(source model.Source, clean string, lineNo int) (model.Event, string) {
	event := baseEvent(source, lineNo)
	event.Format = "console"

	tsToken, rest, ok := cutToken(clean)
	if !ok {
		event.Message = strings.TrimSpace(clean)
		event.Kind = model.EventParserWarning
		return event, fmt.Sprintf("%s:%d: unable to split console timestamp", source.Path, lineNo)
	}
	ts, err := time.Parse(time.RFC3339Nano, tsToken)
	if err != nil {
		event.Message = strings.TrimSpace(clean)
		event.Kind = model.EventParserWarning
		return event, fmt.Sprintf("%s:%d: invalid console timestamp %q", source.Path, lineNo, tsToken)
	}
	event.Timestamp = ts.UTC()
	event.HasTimestamp = true

	levelToken, rest, ok := cutToken(rest)
	if !ok {
		event.Message = strings.TrimSpace(rest)
		event.Kind = model.EventParserWarning
		return event, fmt.Sprintf("%s:%d: missing console level", source.Path, lineNo)
	}
	event.Level = strings.ToLower(strings.TrimSpace(levelToken))

	message, fields := splitConsoleMessageAndFields(rest)
	event.Message = message
	if len(fields) > 0 {
		event.Fields = fields
	}
	event.Kind = classifyMessage(event.Message)
	enrichEvent(&event)
	if event.Kind == model.EventUnknown {
		return event, fmt.Sprintf("%s:%d: unclassified console message %q", source.Path, lineNo, event.Message)
	}
	return event, ""
}

func baseEvent(source model.Source, lineNo int) model.Event {
	return model.Event{
		Node: source.Node,
		Role: source.Role,
		Path: source.Path,
		Line: lineNo,
		Kind: model.EventUnknown,
	}
}

func looksLikeTimestamp(line string) bool {
	line = strings.TrimSpace(line)
	if len(line) < len("2006-01-02T15:04:05Z") {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, strings.Fields(line)[0])
	return err == nil
}

func cutToken(input string) (string, string, bool) {
	input = strings.TrimLeft(input, " \t")
	if input == "" {
		return "", "", false
	}
	idx := strings.IndexAny(input, " \t")
	if idx < 0 {
		return input, "", true
	}
	return input[:idx], input[idx+1:], true
}

func splitConsoleMessageAndFields(rest string) (string, map[string]any) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", nil
	}

	parts := strings.Split(rest, "\t")
	trimmed := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			trimmed = append(trimmed, part)
		}
	}
	if len(trimmed) == 0 {
		return "", nil
	}

	last := trimmed[len(trimmed)-1]
	if strings.HasPrefix(last, "{") && strings.HasSuffix(last, "}") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(last), &payload); err == nil {
			if len(trimmed) == 1 {
				return "", payload
			}
			return trimmed[len(trimmed)-2], payload
		}
	}

	if idx := strings.LastIndex(rest, "{"); idx >= 0 && strings.HasSuffix(rest, "}") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(rest[idx:])), &payload); err == nil {
			return strings.TrimSpace(rest[:idx]), payload
		}
	}

	return trimmed[len(trimmed)-1], nil
}

func classifyMessage(msg string) model.EventKind {
	switch {
	case msg == "":
		return model.EventKnownNoise

	// ── Startup / configuration ────────────────────────────────────────────
	case strings.Contains(msg, "unable to update config field"):
		return model.EventConfigError
	case strings.Contains(msg, "invalid persistent peer address"):
		return model.EventPeerConfigError
	case strings.Contains(msg, "invalid private peer ID"):
		return model.EventPeerConfigError
	case strings.Contains(msg, "Error loading ConsensusState wal"):
		return model.EventConsensusWALIssue
	case strings.Contains(msg, "Encountered corrupt WAL file"):
		return model.EventConsensusWALIssue
	case strings.Contains(msg, "Please repair the WAL file before restarting"):
		return model.EventConsensusWALIssue
	case strings.Contains(msg, "Error on catchup replay. Proceeding to start ConsensusState anyway"):
		return model.EventConsensusWALIssue
	case strings.Contains(msg, "Failed to open WAL for consensus state"):
		return model.EventConsensusWALIssue
	case strings.Contains(msg, "Periodic WAL flush failed"):
		return model.EventConsensusWALIssue
	case strings.Contains(msg, "Error writing msg to consensus wal"):
		return model.EventConsensusWALIssue
	case strings.Contains(msg, "Error writing height to consensus wal"):
		return model.EventConsensusWALIssue
	case strings.Contains(msg, "WriteSync failed to flush consensus wal"):
		return model.EventConsensusWALIssue

	// ── Fast-sync (blockchain reactor) ────────────────────────────────────
	case strings.Contains(msg, "SwitchToConsensus"):
		return model.EventSwitchToConsensus
	case strings.Contains(msg, "Time to switch to consensus reactor!"):
		return model.EventSwitchToConsensus
	case strings.Contains(msg, "BlockchainReactor validation error"):
		return model.EventFastSyncBlockError
	case strings.Contains(msg, "Fast Sync Rate"):
		return model.EventFastSyncRate

	// ── P2P connectivity ───────────────────────────────────────────────────
	case strings.Contains(msg, "Added peer"):
		return model.EventAddedPeer
	case strings.Contains(msg, "Stopping peer for error"):
		return model.EventStoppedPeer
	case strings.Contains(msg, "unable to dial peer"):
		return model.EventDialFailure
	case strings.Contains(msg, "Failed to dial"):
		return model.EventDialFailure
	case strings.Contains(msg, "ignoring dial request: already have max outbound peers"):
		return model.EventMaxOutboundPeers
	case strings.Contains(msg, "no peers to share in discovery request"):
		return model.EventNoPeersToShare

	// ── Consensus — errors and anomalies (specific before generic) ─────────
	case strings.Contains(msg, "CONSENSUS FAILURE!!!"):
		return model.EventConsensusFailure
	case strings.Contains(msg, "Found conflicting vote from ourselves"):
		return model.EventConflictingVote
	case strings.Contains(msg, "Error signing vote"):
		return model.EventSignVoteError
	case strings.Contains(msg, "enterPropose: Error signing proposal"):
		return model.EventSignProposalError
	case strings.Contains(msg, "Error on ApplyBlock"):
		return model.EventApplyBlockError
	case strings.Contains(msg, "enterPrevote: ProposalBlock is nil"):
		return model.EventPrevoteProposalNil
	case strings.Contains(msg, "enterPrevote: ProposalBlock is invalid"):
		return model.EventPrevoteProposalInvalid
	case strings.Contains(msg, "enterPrecommit: No +2/3 prevotes during enterPrecommit"):
		return model.EventPrecommitNoMaj23
	case strings.Contains(msg, "Attempt to finalize failed. There was no +2/3 majority"):
		return model.EventFinalizeNoMaj23
	case strings.Contains(msg, "Attempt to finalize failed. We don't have the commit block."):
		return model.EventCommitBlockMissing
	case strings.Contains(msg, "Commit is for a block we don't know about"):
		return model.EventCommitUnknownBlock
	case strings.Contains(msg, "Received a block part when we're not expecting any"):
		return model.EventUnexpectedBlockPart
	case strings.Contains(msg, "Error attempting to add vote"):
		return model.EventAddVoteError

	// ── Consensus — meaningful progress events ─────────────────────────────
	case strings.Contains(msg, "Finalizing commit of block"):
		return model.EventFinalizeCommit
	case strings.Contains(msg, "Timed out"):
		return model.EventTimeout
	case strings.Contains(msg, "Added to prevote"):
		return model.EventAddedPrevote
	case strings.Contains(msg, "Added to precommit"):
		return model.EventAddedPrecommit
	case strings.Contains(msg, "Commit is for locked block"):
		return model.EventCommitLockedBlock
	case strings.Contains(msg, "Received complete proposal block"):
		return model.EventReceivedCompletePart
	case strings.Contains(msg, "Signed proposal"):
		return model.EventSignedProposal
	case strings.Contains(msg, "Signed and pushed vote"):
		return model.EventSignedVote
	case strings.Contains(msg, "enterPropose: Our turn to propose"),
		strings.Contains(msg, "enterPropose: Not our turn to propose"):
		return model.EventEnterPropose

	// ── Execution / application layer ─────────────────────────────────────
	// Source: tm2/pkg/bft/state/execution.go, tm2/pkg/sdk/baseapp.go
	case strings.Contains(msg, "Committed state"):
		return model.EventCommittedState
	case strings.Contains(msg, "Executed block"):
		return model.EventExecutedBlock
	case strings.Contains(msg, "Commit synced"):
		return model.EventCommitSynced

	// ── Validator identity ─────────────────────────────────────────────────
	case strings.Contains(msg, "This node is not a validator"):
		return model.EventNodeNotValidator
	case strings.Contains(msg, "This node is a validator"):
		return model.EventNodeIsValidator

	// ── Remote signer ─────────────────────────────────────────────────────
	case strings.Contains(msg, "Sign request failed"):
		return model.EventRemoteSignerFailure
	case strings.Contains(msg, "PubKey request failed"):
		return model.EventRemoteSignerFailure
	case strings.Contains(msg, "Connected to server"):
		return model.EventRemoteSignerConnect
	case strings.Contains(msg, "Sign request succeeded"):
		return model.EventRemoteSignerSuccess

	// ── Known noise — consensus state machine transitions ──────────────────
	// Source: tm2/pkg/bft/consensus/state.go — normal round-robin transitions.
	case strings.Contains(msg, "enterNewRound("):
		return model.EventKnownNoise
	case strings.Contains(msg, "enterPropose("):
		return model.EventKnownNoise
	case strings.Contains(msg, "enterPrevote: ProposalBlock is valid"):
		return model.EventKnownNoise
	case strings.Contains(msg, "enterPrevote("):
		return model.EventKnownNoise
	case strings.Contains(msg, "enterPrevoteWait("):
		return model.EventKnownNoise
	case strings.Contains(msg, "enterPrecommit("):
		return model.EventKnownNoise
	case strings.Contains(msg, "enterPrecommitWait("):
		return model.EventKnownNoise
	case strings.Contains(msg, "enterCommit("):
		return model.EventKnownNoise
	case strings.Contains(msg, "Resetting Proposal info"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Scheduled timeout"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Ignoring tock"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Received proposal"):
		return model.EventReceivedProposal
	case strings.Contains(msg, "Received tick"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Received tock"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Timer already stopped"):
		return model.EventKnownNoise

	// ── Known noise — voting bookkeeping ──────────────────────────────────
	// Source: tm2/pkg/bft/consensus/state.go, reactor.go
	case strings.Contains(msg, "Added to lastPrecommits:"):
		return model.EventKnownNoise
	case strings.Contains(msg, "setHasVote"):
		return model.EventKnownNoise
	case strings.Contains(msg, "addVote"):
		return model.EventKnownNoise
	case strings.Contains(msg, "No votes to send"):
		return model.EventKnownNoise

	// ── Known noise — consensus reactor gossip ────────────────────────────
	// Source: tm2/pkg/bft/consensus/reactor.go
	case strings.Contains(msg, "Sending proposal"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Sending block part"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Sending vote message"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Picked rs.LastCommit to send"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Picked rs.Prevotes"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Picked rs.Precommits"):
		return model.EventKnownNoise

	// ── Known noise — P2P message types (wire representation in logs) ──────
	// These are raw TM2 p2p message strings printed via their String() method.
	// "Receive" consensus messages carry individual vote details; classify by type.
	case strings.HasPrefix(msg, "[Vote Vote{") && strings.Contains(msg, "(Prevote)"):
		return model.EventAddedPrevote
	case strings.HasPrefix(msg, "[Vote Vote{") && strings.Contains(msg, "(Precommit)"):
		return model.EventAddedPrecommit
	case strings.HasPrefix(msg, "[Vote "):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "[Proposal "):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "[BlockPart "):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "[ValidBlock"):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "[NewRoundStep "):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "[HasVote "):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "[VoteSetMaj23"):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "[VSM23"): // VoteSetMaj23 abbreviated form
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "[VoteSetBits"):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "[VSB "): // VoteSetBits abbreviated form
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "[bc"): // blockchain reactor fast-sync messages
		return model.EventKnownNoise
	case strings.Contains(msg, "Blockpool has no peers"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Connection failed @ recvRoutine"):
		return model.EventKnownNoise
	case strings.Contains(msg, "unable to gracefully close"):
		return model.EventKnownNoise

	// ── Known noise — low-level P2P connection I/O ────────────────────────
	// Source: tm2/pkg/p2p/conn/connection.go (both current and older deployed versions).
	case strings.Contains(msg, "Read PacketMsg"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Received bytes"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Send Ping"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Send Pong"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Receive Ping"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Receive Pong"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Starting pong timer"):
		return model.EventKnownNoise
	case msg == "Flush":
		return model.EventKnownNoise
	case msg == "Send":
		return model.EventKnownNoise
	case msg == "TrySend":
		return model.EventKnownNoise

	// ── Known noise — peer dialing / connection lifecycle ─────────────────
	// Source: tm2/pkg/p2p/switch.go, tm2/pkg/p2p/dial.go
	case strings.Contains(msg, "dialing peer"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Dial succeeded"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Already connected to server"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Retrying to connect"):
		return model.EventKnownNoise

	// ── Known noise — peer discovery ──────────────────────────────────────
	// Source: tm2/pkg/p2p/discovery/discovery.go
	case strings.Contains(msg, "received message"):
		return model.EventKnownNoise
	case strings.Contains(msg, "running peer discovery"):
		return model.EventKnownNoise

	// ── Known noise — RPC / HTTP ──────────────────────────────────────────
	// Source: tm2/pkg/bft/rpc/lib/server/handlers.go
	case strings.Contains(msg, "HTTP HANDLER"):
		return model.EventKnownNoise
	case strings.Contains(msg, "HTTPRestRPC"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Served RPC HTTP response"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Panic in RPC HTTP handler"):
		return model.EventKnownNoise
	case strings.Contains(msg, "started Span"):
		return model.EventKnownNoise

	// ── Known noise — service lifecycle ───────────────────────────────────
	// Source: tm2/pkg/service/service.go — every service logs "Starting/Stopping X".
	case strings.HasPrefix(msg, "Starting "):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "Stopping "):
		return model.EventKnownNoise
	case strings.Contains(msg, "ConsensusReactor"):
		return model.EventKnownNoise
	case strings.Contains(msg, "InitChainer:"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Consensus ticker"):
		return model.EventKnownNoise
	case strings.Contains(msg, "P2P Node ID"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Version info"):
		return model.EventKnownNoise
	case strings.Contains(msg, "ABCI Handshake"):
		return model.EventKnownNoise
	case strings.Contains(msg, "ABCI Replay"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Completed ABCI Handshake"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Need to set a buffer"):
		return model.EventKnownNoise
	case strings.Contains(msg, "ignoring dial request for existing peer"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Updates to validators"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Default configuration initialized"):
		return model.EventKnownNoise
	case strings.Contains(msg, "Updated configuration saved"):
		return model.EventKnownNoise

	// ── Known noise — Go panic stack traces ────────────────────────────────
	// These appear when a goroutine panics; the actual panic is surfaced via
	// other findings (CONSENSUS FAILURE, ApplyBlock error, etc.).
	case strings.HasPrefix(msg, "github.com/"):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "runtime."):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "/gnoroot/"):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "/usr/local/go/"):
		return model.EventKnownNoise

	// ── Known noise — gnoland startup banner (ASCII art) ──────────────────
	case strings.Contains(msg, "_ `"):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "\\_,"):
		return model.EventKnownNoise
	case msg == "/___/":
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "___"):
		return model.EventKnownNoise
	case strings.HasPrefix(msg, "__ "):
		return model.EventKnownNoise

	default:
		return model.EventUnknown
	}
}

func enrichEvent(event *model.Event) {
	if event.Height == 0 {
		event.Height = extractHeight(event.Message, event.Fields)
	}
	if event.Round == 0 {
		event.Round = extractRound(event.Message, event.Fields)
	}

	if event.Kind == model.EventSignedVote {
		if event.Fields == nil {
			event.Fields = map[string]any{}
		}
		if rawType, ok := event.Fields["type"]; ok {
			if voteType := normalizeVoteType(rawType); voteType != "" {
				event.Fields["_vote_type"] = voteType
			}
		}
		if addr, ok := event.Fields["validator address"].(string); ok && addr != "" {
			event.Fields["_vaddrprefix"] = voteAddrPrefix(addr)
		}
		if hash, ok := event.Fields["block_hash"].(string); ok && hash != "" {
			event.Fields["_vhash"] = strings.ToUpper(hash)
		} else {
			event.Fields["_vhash"] = ""
		}
		if ts, ok := event.Fields["timestamp"].(string); ok && ts != "" {
			if parsed, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", ts); err == nil {
				event.Fields["_cast_at"] = parsed.UTC()
			}
		}
	}

	// For prevote/precommit events, parse the VoteSet string to extract vote counts,
	// and parse the individual vote to extract validator index + block hash.
	if event.Kind == model.EventAddedPrevote || event.Kind == model.EventAddedPrecommit {
		if event.Fields == nil {
			event.Fields = map[string]any{}
		}
		if event.Kind == model.EventAddedPrevote {
			event.Fields["_vote_type"] = "prevote"
		} else {
			event.Fields["_vote_type"] = "precommit"
		}
		fieldName := "prevotes"
		if event.Kind == model.EventAddedPrecommit {
			fieldName = "precommits"
		}
		if vs, ok := event.Fields[fieldName].(string); ok {
			recv, total, maj23, maj23Hash, bits := parseVoteSet(vs)
			if total > 0 {
				event.Fields["_vrecv"] = recv
				event.Fields["_vtotal"] = total
				event.Fields["_vmaj23"] = maj23
				event.Fields["_vmaj23hash"] = maj23Hash
				event.Fields["_vbits"] = bits
			}
		}
		// Extract per-validator detail: index, address fingerprint, and voted block hash.
		// "Receive" (consensus) messages: [Vote Vote{IDX:ADDRSHORT H/R/T(Type) HASH SIG @ TS}].
		// Groups: (1)=idx, (2)=addrShort, (3)=height, (4)=round, (5)=typeName, (6)=hash.
		if m := voteReceiveRE.FindStringSubmatch(event.Message); m != nil {
			idx, _ := strconv.Atoi(m[1])
			event.Fields["_vidx"] = idx
			event.Fields["_vaddrprefix"] = strings.ToUpper(m[2])
			hash := strings.ToUpper(m[6])
			// All-zero hash == nil vote (TM2 prints zero BlockID as "000000000000").
			if strings.Trim(hash, "0") == "" {
				event.Fields["_vhash"] = ""
			} else {
				event.Fields["_vhash"] = hash
			}
		} else {
			// Fall back: "vote" structured field (JSON) or raw message (console).
			// Groups: (1)=idx, (2)=addrShort, (3)=hash.
			voteStr, _ := event.Fields["vote"].(string)
			if voteStr == "" {
				voteStr = event.Message
			}
			if m := voteDetailRE.FindStringSubmatch(voteStr); m != nil {
				idx, _ := strconv.Atoi(m[1])
				event.Fields["_vidx"] = idx
				event.Fields["_vaddrprefix"] = strings.ToUpper(m[2])
				if m[3] == "<nil>" {
					event.Fields["_vhash"] = ""
				} else {
					event.Fields["_vhash"] = m[3]
				}
			}
		}
	}

	// For timeout events, extract the consensus step so callers can classify
	// votes that arrive after the step boundary as "late".
	if event.Kind == model.EventTimeout {
		if step, ok := event.Fields["step"].(string); ok && step != "" {
			event.Fields["_step"] = step
		} else if m := timeoutStepRE.FindStringSubmatch(event.Message); m != nil {
			event.Fields["_step"] = m[1]
		}
	}

	// For "Received complete proposal block" events, TM2 logs the block hash as
	// a base64 string in the "hash" field. Decode it to uppercase hex and store
	// it as "block_hash" so the rest of the analysis can use a uniform format.
	if event.Kind == model.EventReceivedCompletePart {
		if b64, ok := event.Fields["hash"].(string); ok && b64 != "" {
			if raw, err := base64.StdEncoding.DecodeString(b64); err == nil {
				event.Fields["block_hash"] = strings.ToUpper(hex.EncodeToString(raw))
			}
		}
	}

	// For "Received proposal" events, extract the full block hash from the
	// "proposal block ID" field (format: "HASH:PARTS_COUNT:PARTS_HASH").
	// This event carries the round correctly, unlike "Received complete proposal block".
	if event.Kind == model.EventReceivedProposal {
		if blockID, ok := event.Fields["proposal block ID"].(string); ok && blockID != "" {
			if colon := strings.IndexByte(blockID, ':'); colon > 0 {
				event.Fields["block_hash"] = strings.ToUpper(blockID[:colon])
			}
		}
	}

	// For peer add/drop events, extract the bech32 peer address for identity
	// resolution and (for drops) the error reason.
	if event.Kind == model.EventAddedPeer || event.Kind == model.EventStoppedPeer {
		peerStr, _ := event.Fields["peer"].(string)
		if peerStr == "" {
			peerStr, _ = event.Fields["src"].(string)
		}
		if peerStr != "" {
			if m := peerAddrRE.FindStringSubmatch(peerStr); m != nil {
				event.Fields["_paddr"] = m[1]
			}
		}
		if event.Kind == model.EventStoppedPeer {
			if errStr, ok := event.Fields["err"].(string); ok && errStr != "" {
				event.Fields["_perr"] = errStr
			}
		}
	}
}

// parseVoteSet extracts vote counts from a TM2 VoteSet string.
// Format: VoteSet{H:19497 R:0 T:2 +2/3:<nil>(0.571) BA{7:x______} map[]}
// Returns received (count of 'x'), total validators, whether +2/3 majority was reached,
// the block hash that achieved majority (empty string = nil majority), and the bit array.
//
// TM2 majority formats:
//
//	+2/3:<nil>             → no majority yet (maj23=false)
//	+2/3::0:000000000000   → nil majority (maj23=true, maj23Hash="")
//	+2/3:CF53223F...:1:... → block majority (maj23=true, maj23Hash=block hash)
func parseVoteSet(s string) (received, total int, maj23 bool, maj23Hash string, bits string) {
	m := voteSetRE.FindStringSubmatch(s)
	if m == nil {
		return
	}
	// m[1] = "<nil>" or ":0:000000000000" (nil majority) or "HASH:COUNT:PARTSHASH" (block majority)
	hashPart := strings.TrimSpace(m[1])
	switch {
	case hashPart == "<nil>":
		// No majority reached yet.
		maj23 = false
	case strings.HasPrefix(hashPart, ":"):
		// Nil majority: +2/3 voted nil. Block hash part is empty.
		maj23 = true
	default:
		// Block majority: the part before the first ':' is the block hash.
		maj23 = true
		if colon := strings.IndexByte(hashPart, ':'); colon > 0 {
			maj23Hash = strings.ToUpper(hashPart[:colon])
		} else {
			maj23Hash = strings.ToUpper(hashPart)
		}
	}
	total, _ = strconv.Atoi(m[2])
	bits = m[3]
	for _, c := range bits {
		if c == 'x' {
			received++
		}
	}
	return
}

func extractHeight(msg string, fields map[string]any) int64 {
	if value, ok := fields["height"]; ok {
		if parsed, ok := toInt64(value); ok {
			return parsed
		}
	}
	// "Added to prevote/precommit" uses "vote height" (with a space) instead of "height".
	if value, ok := fields["vote height"]; ok {
		if parsed, ok := toInt64(value); ok {
			return parsed
		}
	}
	// "[Vote Vote{IDX:ADDR H/R/T(Type) HASH}]" — "Receive" consensus messages.
	// Groups: (1)=idx, (2)=addrShort, (3)=height, (4)=round, (5)=typeName, (6)=hash.
	if m := voteReceiveRE.FindStringSubmatch(msg); m != nil {
		if parsed, err := strconv.ParseInt(m[3], 10, 64); err == nil {
			return parsed
		}
	}
	matches := heightRoundRE.FindStringSubmatch(msg)
	if len(matches) == 3 {
		if parsed, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
			return parsed
		}
	}
	return 0
}

func extractRound(msg string, fields map[string]any) int {
	if value, ok := fields["round"]; ok {
		if parsed, ok := toInt64(value); ok {
			return int(parsed)
		}
	}
	// "Added to prevote/precommit" uses "vote round" (with a space) instead of "round".
	if value, ok := fields["vote round"]; ok {
		if parsed, ok := toInt64(value); ok {
			return int(parsed)
		}
	}
	// "[Vote Vote{IDX:ADDR H/R/T(Type) HASH}]" — "Receive" consensus messages.
	// Groups: (1)=idx, (2)=addrShort, (3)=height, (4)=round, (5)=typeName, (6)=hash.
	if m := voteReceiveRE.FindStringSubmatch(msg); m != nil {
		if parsed, err := strconv.Atoi(m[4]); err == nil {
			return parsed
		}
	}
	matches := heightRoundRE.FindStringSubmatch(msg)
	if len(matches) == 3 {
		if parsed, err := strconv.Atoi(matches[2]); err == nil {
			return parsed
		}
	}
	return 0
}

func toInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func normalizeVoteType(value any) string {
	switch typed := value.(type) {
	case int:
		switch typed {
		case 1:
			return "prevote"
		case 2:
			return "precommit"
		}
	case int64:
		return normalizeVoteType(int(typed))
	case float64:
		return normalizeVoteType(int(typed))
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "prevote":
			return "prevote"
		case "2", "precommit":
			return "precommit"
		}
	}
	return ""
}

func voteAddrPrefix(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if !looksLikeHex(addr) {
		if sep := strings.LastIndexByte(addr, '1'); sep >= 1 && sep+1 < len(addr) {
			addr = addr[sep+1:]
		}
	}
	addr = strings.ToUpper(addr)
	if len(addr) > 12 {
		return addr[:12]
	}
	return addr
}

func looksLikeHex(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// NormalizeMessage collapses variable runtime data (heights, hashes, bit
// arrays, whitespace) so that structurally identical log messages share the
// same key regardless of the block height or round they were emitted at.
func NormalizeMessage(msg string) string {
	key := timestampRE.ReplaceAllString(msg, "T")
	key = bitArrayRE.ReplaceAllString(key, "BA{...}")
	key = hexSeqRE.ReplaceAllString(key, "X")
	key = digitSeqRE.ReplaceAllString(key, "N")
	key = whitespaceRE.ReplaceAllString(key, " ")
	return strings.TrimSpace(key)
}

// StreamCategoryLines re-reads r and writes every raw log line whose
// normalised message matches targetKey to w.
func StreamCategoryLines(source model.Source, r io.Reader, targetKey string, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		if raw == "" {
			continue
		}
		event, _ := ParseLogLine(source, raw, lineNo)
		if event.Kind != model.EventUnknown {
			continue
		}
		if NormalizeMessage(event.Message) == targetKey {
			fmt.Fprintf(w, "%s\n", raw)
		}
	}
	return scanner.Err()
}

// FilterEventsByHeight scans r and returns the events relevant to analysing a
// specific block height H:
//   - all classified events where event.Height == H
//   - EventFinalizeCommit at H-1 (used to determine the block-period window start)
//   - EventAddedPeer / EventStoppedPeer with timestamps (filtered to the window later)
func FilterEventsByHeight(source model.Source, r io.Reader, height int64) ([]model.Event, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var events []model.Event
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		if raw == "" {
			continue
		}
		event, _ := ParseLogLine(source, raw, lineNo)
		if event.Kind == model.EventUnknown || event.Kind == model.EventKnownNoise {
			continue
		}

		keep := false
		switch {
		case event.Height == height:
			// All classified events at the target height.
			keep = true
		case event.Kind == model.EventFinalizeCommit && event.Height == height-1:
			// Needed to establish the block-period window start.
			keep = true
		case (event.Kind == model.EventAddedPeer || event.Kind == model.EventStoppedPeer) && event.HasTimestamp:
			// Peer connection changes anywhere in the log; windowed later.
			keep = true
		}
		if keep {
			events = append(events, event)
		}
	}
	return events, scanner.Err()
}

func DefaultNodeName(path string, used map[string]int) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".gz")
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.ReplaceAll(base, " ", "_")
	base = strings.ReplaceAll(base, "-", "_")
	if base == "" {
		base = "node"
	}
	count := used[base]
	used[base]++
	if count == 0 {
		return base
	}
	return fmt.Sprintf("%s_%d", base, count+1)
}
