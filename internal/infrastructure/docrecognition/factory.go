package docrecognition

import (
	"fmt"
	"strings"

	"paperMaster/internal/config"
	"paperMaster/internal/types/interfaces"
)

const (
	DriverDefault = "default"
	DriverBuiltin = "builtin"
)

// NewRecognizer selects the configured document-recognition adapter. The
// application receives only DocumentRecognizer and never imports this package.
func NewRecognizer(cfg config.Config) (interfaces.DocumentRecognizer, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.DocumentRecognitionDriver)) {
	case "", DriverDefault:
		return NewDefaultRecognizer(), nil
	case DriverBuiltin:
		return NewBuiltinRecognizer(cfg.ParserGRPCAddress, cfg.ParserGRPCMaxMessageSize)
	default:
		return nil, fmt.Errorf("unsupported DOCUMENT_RECOGNITION_DRIVER %q", cfg.DocumentRecognitionDriver)
	}
}
