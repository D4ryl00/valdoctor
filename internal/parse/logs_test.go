package parse

import (
	"testing"
	"time"

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

func TestParseConsoleReceiveVoteUsesEmbeddedWireMessage(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "val3", Role: model.RoleValidator}
	line := "val3-1  | 2026-04-20T21:43:23.301Z\t\x1b[35mDEBUG\x1b[0m\tReceive\t{\"module\":\"consensus\",\"src\":\"Peer{MConn{172.20.0.7:52858} g1u9f8lwsqmjclrmmmluy4kalemkwgx2yvpjqgfc in}\",\"chId\":34,\"msg\":\"[Vote Vote{1:2E6BC11AECD0 160/00/1(Prevote) 3A2534EFFDE9 E554F77A28A6 @ 2026-04-20T21:43:23.198036843Z}]\"}"

	event, warning := ParseLogLine(source, line, 42)

	require.Empty(t, warning)
	require.Equal(t, "console", event.Format)
	require.Equal(t, model.EventAddedPrevote, event.Kind)
	require.EqualValues(t, 160, event.Height)
	require.Equal(t, 0, event.Round)
	require.Equal(t, "prevote", event.Fields["_vote_type"])
	require.Equal(t, 1, event.Fields["_vidx"])
	require.Equal(t, "2E6BC11AECD0", event.Fields["_vaddrprefix"])
	require.Equal(t, "3A2534EFFDE9", event.Fields["_vhash"])
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

func TestParseJSONLineExtractsSignVoteErrorDetails(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator}
	line := `{"level":"error","ts":1775590325.1933045,"msg":"Error signing vote","height":160,"round":0,"vote":"Vote{1:2E6BC11AECD0 160/00/2(Precommit) 3A2534EFFDE9 000000000000 @ 2026-04-20T21:43:23.21024801Z}","err":"response contains error: valsigner: signature dropped by control rule"}`

	event, warning := ParseLogLine(source, line, 88)

	require.Empty(t, warning)
	require.Equal(t, model.EventSignVoteError, event.Kind)
	require.Equal(t, "precommit", event.Fields["_vote_type"])
	require.Equal(t, 1, event.Fields["_vidx"])
	require.Equal(t, "2E6BC11AECD0", event.Fields["_vaddrprefix"])
	require.Equal(t, "3A2534EFFDE9", event.Fields["_vhash"])
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

func TestParseJSONLineClassifiesSignedVote(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator}
	line := `{"level":"info","ts":1775723448.1131523,"msg":"Signed and pushed vote","module":"consensus","height":236286,"round":0,"type":1,"timestamp":"2026-04-09 08:30:47.775636368 +0000 UTC","validator address":"g1qve0yt0vt4tskhxffv27p0fatsua2um6cqr5ve","validator index":1}`

	event, warning := ParseLogLine(source, line, 123)

	require.Empty(t, warning)
	require.Equal(t, model.EventSignedVote, event.Kind)
	require.EqualValues(t, 236286, event.Height)
	require.Equal(t, 0, event.Round)
	require.Equal(t, "prevote", event.Fields["_vote_type"])
	require.Equal(t, "QVE0YT0VT4TS", event.Fields["_vaddrprefix"])
	require.Equal(t, 1, event.Fields["_vidx"])
	require.Equal(t, "", event.Fields["_vhash"])
	_, ok := event.Fields["_cast_at"].(time.Time)
	require.True(t, ok)
}

func TestParseConsoleLineClassifiesLastPrecommitsAsPrecommitEvidence(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "val5", Role: model.RoleValidator}
	line := "val5-1  | 2026-04-21T00:02:23.627Z\t\x1b[34mINFO \x1b[0m\tAdded to lastPrecommits: VoteSet{H:1342 R:0 T:2 +2/3:6B8F4DF0A6060AFD632900CC998BBADA59D12BF215772AD137B909D75E3D6C14:1:6BD05228F3A2(1) BA{5:xxxxx} map[]}\t{\"module\":\"consensus\"}"

	event, warning := ParseLogLine(source, line, 201)

	require.Empty(t, warning)
	require.Equal(t, model.EventAddedPrecommit, event.Kind)
	require.EqualValues(t, 1342, event.Height)
	require.Equal(t, 0, event.Round)
	require.Equal(t, "precommit", event.Fields["_vote_type"])
	require.Equal(t, 5, event.Fields["_vrecv"])
	require.Equal(t, 5, event.Fields["_vtotal"])
	require.Equal(t, true, event.Fields["_vmaj23"])
	require.Equal(t, "xxxxx", event.Fields["_vbits"])
}

func TestParseConsoleLineClassifiesSetHasVoteAsObservedVoteEvidence(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "val2", Role: model.RoleValidator}
	line := "val2-1  | 2026-04-21T09:08:14.073Z\tDEBUG\tsetHasVote\t{\"module\":\"consensus\",\"peerH/R\":\"1118/0\",\"H/R\":\"1118/0\",\"type\":1,\"index\":4}"

	event, warning := ParseLogLine(source, line, 101)

	require.Empty(t, warning)
	require.Equal(t, model.EventObservedPrevote, event.Kind)
	require.EqualValues(t, 1118, event.Height)
	require.Equal(t, 0, event.Round)
	require.Equal(t, "prevote", event.Fields["_vote_type"])
	require.Equal(t, 4, event.Fields["_vidx"])
}

func TestParseConsoleLineClassifiesSendingVoteAsObservedVoteEvidence(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "val2", Role: model.RoleValidator}
	line := "val2-1  | 2026-04-21T09:08:14.151Z\tDEBUG\tSending vote message\t{\"module\":\"consensus\",\"vote\":\"Vote{4:C4F822B35F6C 1118/00/1(Prevote) 9B47FA716CC9 66B9929C9829 @ 2026-04-21T09:08:13.964700885Z}\"}"

	event, warning := ParseLogLine(source, line, 102)

	require.Empty(t, warning)
	require.Equal(t, model.EventObservedPrevote, event.Kind)
	require.EqualValues(t, 1118, event.Height)
	require.Equal(t, 0, event.Round)
	require.Equal(t, "prevote", event.Fields["_vote_type"])
	require.Equal(t, 4, event.Fields["_vidx"])
	require.Equal(t, "C4F822B35F6C", event.Fields["_vaddrprefix"])
	require.Equal(t, "9B47FA716CC9", event.Fields["_vhash"])
}

func TestParseJSONLineClassifiesPeerConfigError(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator}
	line := `{"level":"error","ts":1775590325.1591926,"msg":"invalid persistent peer address","err":"invalid net address"}`

	event, warning := ParseLogLine(source, line, 3)

	require.Empty(t, warning)
	require.Equal(t, model.EventPeerConfigError, event.Kind)
}

func TestParseJSONLineClassifiesNodeShutdown(t *testing.T) {
	source := model.Source{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator}
	line := `{"level":"info","ts":1775590325.1591926,"msg":"Stopping Node"}`

	event, warning := ParseLogLine(source, line, 200)

	require.Empty(t, warning)
	require.Equal(t, model.EventNodeShutdown, event.Kind)
}

func TestDefaultNodeNameExtractsComposeValidatorService(t *testing.T) {
	used := map[string]int{}

	require.Equal(t, "val1", DefaultNodeName("valcontrol-5-validators-val1-1", used))
	require.Equal(t, "val1_signer", DefaultNodeName("valcontrol-5-validators-val1-signer-1", used))
	require.Equal(t, "validator3", DefaultNodeName("project-validator3-1", used))
}
