package parse

import (
	"testing"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/stretchr/testify/require"
)

func TestParseJSONLine(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator}
	line := `{"level":"info","ts":1774017464.5705216,"msg":"Finalizing commit of block","height":12}`

	event, warning := ParseLogLine(source, line, 1)

	require.Empty(t, warning)
	require.Equal(t, "json", event.Format)
	require.Equal(t, model.EventFinalizeCommit, event.Kind)
	require.True(t, event.HasTimestamp)
	require.EqualValues(t, 12, event.Height)
}

func TestParseConsoleLineWithANSIAndContainerPrefix(t *testing.T) {
	source := model.Source{Path: "/tmp/sentry.log", Node: "sentry_a", Role: model.RoleSentry}
	line := "gnoland-1  | 2026-03-20T14:37:08.485Z\t\x1b[34mINFO \x1b[0m\tAdded peer\t{\"module\":\"p2p\",\"peer\":\"Peer{abc}\"}"

	event, warning := ParseLogLine(source, line, 7)

	require.Empty(t, warning)
	require.Equal(t, "console", event.Format)
	require.Equal(t, model.EventAddedPeer, event.Kind)
	require.Equal(t, "info", event.Level)
	require.Equal(t, "Added peer", event.Message)
}

func TestParseVoteSetIncludesBitmap(t *testing.T) {
	recv, total, maj23, maj23Hash, bits := parseVoteSet("VoteSet{H:19497 R:0 T:2 +2/3:<nil>(0.571) BA{7:x__xx__} map[]}")

	require.Equal(t, 3, recv)
	require.Equal(t, 7, total)
	require.False(t, maj23)
	require.Equal(t, "", maj23Hash)
	require.Equal(t, "x__xx__", bits)
}

func TestParseJSONLineClassifiesVoteSigningError(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator}
	line := `{"level":"error","ts":1775590325.1933045,"msg":"Error signing vote","height":234888,"round":0,"err":"same HRS with conflicting data"}`

	event, warning := ParseLogLine(source, line, 87)

	require.Empty(t, warning)
	require.Equal(t, "json", event.Format)
	require.Equal(t, model.EventSignVoteError, event.Kind)
	require.EqualValues(t, 234888, event.Height)
	require.Equal(t, 0, event.Round)
}

func TestParseJSONLineClassifiesConsensusWALIssue(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator}
	line := `{"level":"error","ts":1775590325.1591926,"msg":"Error on catchup replay. Proceeding to start ConsensusState anyway","err":"cannot replay height 234888. WAL does not contain #ENDHEIGHT for 234887"}`

	event, warning := ParseLogLine(source, line, 55)

	require.Empty(t, warning)
	require.Equal(t, model.EventConsensusWALIssue, event.Kind)
}

func TestParseJSONLineClassifiesPubKeyRequestFailure(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator}
	line := `{"level":"error","ts":1775590325.1591926,"msg":"PubKey request failed","error":"failed to fetch public key"}`

	event, warning := ParseLogLine(source, line, 12)

	require.Empty(t, warning)
	require.Equal(t, model.EventRemoteSignerFailure, event.Kind)
}

func TestParseJSONLineClassifiesProposalSigningError(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator}
	line := `{"level":"error","ts":1775590325.1591926,"msg":"enterPropose: Error signing proposal","height":234888,"round":2,"err":"remote signer unavailable"}`

	event, warning := ParseLogLine(source, line, 99)

	require.Empty(t, warning)
	require.Equal(t, model.EventSignProposalError, event.Kind)
	require.EqualValues(t, 234888, event.Height)
	require.Equal(t, 2, event.Round)
}

func TestParseJSONLineClassifiesPeerConfigError(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator}
	line := `{"level":"error","ts":1775590325.1591926,"msg":"invalid persistent peer address","err":"invalid net address"}`

	event, warning := ParseLogLine(source, line, 3)

	require.Empty(t, warning)
	require.Equal(t, model.EventPeerConfigError, event.Kind)
}
