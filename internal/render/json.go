package render

import (
	"encoding/json"

	"github.com/D4ryl00/valdoctor/internal/model"
)

func JSON(report model.Report) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}
