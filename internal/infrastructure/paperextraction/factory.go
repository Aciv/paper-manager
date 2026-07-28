package paperextraction

import (
	"paperMaster/internal/config"
	"paperMaster/internal/types/interfaces"
)

// NewExtractor creates the Python-backed metadata extractor from runtime
// configuration. Document recognition and paper extraction use separate
// clients even when they share the same gRPC server.
func NewExtractor(cfg config.Config) (interfaces.PaperMetadataExtractor, error) {
	return NewGRPCExtractor(
		cfg.ParserGRPCAddress,
		cfg.ParserGRPCMaxMessageSize,
	)
}
