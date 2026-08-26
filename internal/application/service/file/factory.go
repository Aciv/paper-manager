package file

import (
	"context"
	"fmt"
	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types/interfaces"
)

// NewStorage selects the durable file-storage implementation from runtime
// configuration. Add future storage backends to this package and this switch.
func NewStorage(
	cfg config.Config,
	downloads interfaces.RemoteDownloadLimiter,
) (interfaces.FileStorage, error) {
	switch cfg.FileStorageDriver {
	case "local":
		storage, err := NewLocalStorage(cfg.UploadDirectory, cfg.MaxUploadSize)
		if err != nil {
			return nil, err
		}
		storage.ConfigureRemoteClient(
			cfg.RemotePDFDownloadTimeout,
			cfg.RemotePDFMaxRedirects,
			downloads,
		)
		return storage, nil
	default:
		err := fmt.Errorf("unsupported FILE_STORAGE_DRIVER %q", cfg.FileStorageDriver)
		logger.Error(context.Background(), "select file storage driver failed", "error", err)
		return nil, err
	}
}
