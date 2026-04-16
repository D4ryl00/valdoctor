package source

import (
	"context"

	"github.com/D4ryl00/valdoctor/internal/model"
)

type Line struct {
	Raw    string
	Path   string
	Node   string
	Role   model.Role
	LineNo int
	SeqNo  uint64
}

type LogSource interface {
	Name() string
	Stream(ctx context.Context) (<-chan Line, <-chan error)
}
