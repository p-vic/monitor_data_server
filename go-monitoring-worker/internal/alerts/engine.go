package alerts

import (
	"github.com/monitoring-system/go-worker/internal/models"
)

// Engine es la Máquina de Estados que evalúa si un resultado dispara alertas
type Engine interface {
	ProcessResult(target models.TargetConfig, result models.PingResult)
	ResetTarget(targetID string)
}
