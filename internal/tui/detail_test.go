package tui

import (
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/stretchr/testify/require"
)

func TestReceiptCellRendersQuorumSatisfied(t *testing.T) {
	require.Equal(t, "after+2/3", receiptCell(&model.VoteReceipt{
		Status:     "quorum-satisfied",
		Latency:    250 * time.Millisecond,
		CastAt:     time.Unix(1, 0),
		ReceivedAt: time.Unix(1, 0).Add(250 * time.Millisecond),
	}))
}
