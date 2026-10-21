// Package container registers application dependencies for runtime resolution.
package container

import (
	"fmt"

	"paperMaster/internal/application/repository"
	appservice "paperMaster/internal/application/service"
	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/config"
	"paperMaster/internal/handler"
	"paperMaster/internal/infrastructure/docrecognition"
	"paperMaster/internal/infrastructure/doicitation"
	"paperMaster/internal/infrastructure/papercatalog"
	"paperMaster/internal/infrastructure/paperextraction"
	"paperMaster/internal/infrastructure/paperurl"
	"paperMaster/internal/limit"
	"paperMaster/internal/router"
	"paperMaster/internal/types/interfaces"

	"go.uber.org/dig"
)

// Build registers providers only. NewDatabase applies configured migrations;
// the caller owns server startup and resource cleanup.
func Build() *dig.Container {
	dependencies := dig.New()
	must(dependencies.Provide(config.Load))
	must(dependencies.Provide(repository.NewDatabase))
	must(dependencies.Provide(repository.NewDocumentRepository))
	must(dependencies.Provide(repository.NewNoteReadingRepository))
	must(dependencies.Provide(repository.NewPaperMetadataRepository))
	must(dependencies.Provide(repository.NewPaperCitationRepository))
	must(dependencies.Provide(repository.NewDocumentBaseRepository))
	must(dependencies.Provide(func(repository interfaces.DocumentBaseRepository) interfaces.BibliographySnapshotRepository {
		return repository
	}))
	must(dependencies.Provide(doicitation.NewClient))
	must(dependencies.Provide(repository.NewDocumentProcessingSpanRepository))
	must(dependencies.Provide(appservice.NewSpanTracker))
	must(dependencies.Provide(appservice.NewSpanRecorder))
	must(dependencies.Provide(appservice.NewProcessingViewQuery))
	must(dependencies.Provide(router.NewAsyncqClient))
	must(dependencies.Provide(router.NewTaskEnqueuer))
	must(dependencies.Provide(router.NewAsynqServer))
	must(dependencies.Provide(router.NewSharedAsynqInspector))
	must(dependencies.Provide(router.NewTaskInspector))
	must(dependencies.Provide(router.NewTaskResultInspector))
	must(dependencies.Provide(docrecognition.NewRecognizer))
	must(dependencies.Provide(paperextraction.NewExtractor))
	must(dependencies.Provide(limit.NewEngine))
	must(dependencies.Provide(papercatalog.NewRegistryFromConfig))
	must(dependencies.Provide(appservice.NewCatalogDispatcher))
	must(dependencies.Provide(func(dispatcher *appservice.CatalogDispatcher) interfaces.CatalogDispatcher {
		return dispatcher
	}))
	must(dependencies.Provide(paperurl.NewResolver))
	must(dependencies.Provide(paperurl.NewDownloadLimiter))
	must(dependencies.Provide(appservice.NewDocumentServiceWithProcessingAndSpanRecorder))
	must(dependencies.Provide(appservice.NewDocumentStructureService))
	must(dependencies.Provide(appservice.NewDocumentReadingService))
	must(dependencies.Provide(func(storage interfaces.FileStorage) (interfaces.TextFileEditor, error) {
		editor, ok := storage.(interfaces.TextFileEditor)
		if !ok {
			return nil, fmt.Errorf("file storage driver does not support text editing")
		}
		return editor, nil
	}))
	must(dependencies.Provide(appservice.NewNoteReadingService))
	must(dependencies.Provide(appservice.NewDocumentSelectionProcessor))
	must(dependencies.Provide(appservice.NewPaperService))
	must(dependencies.Provide(appservice.NewPaperInfoServiceWithSpanRecorder))
	must(dependencies.Provide(appservice.NewDocumentBaseService))
	must(dependencies.Provide(appservice.NewBibliographyServiceWithTaskSystem))
	must(dependencies.Provide(appservice.NewDocumentHousekeepingService))
	must(dependencies.Provide(repository.NewTaskDeadLetterRepository))
	must(dependencies.Provide(fileservice.NewStorage))
	must(dependencies.Provide(handler.NewDocumentHandler))
	must(dependencies.Provide(handler.NewDocumentReadingHandler))
	must(dependencies.Provide(handler.NewNoteReadingHandler))
	must(dependencies.Provide(handler.NewDocumentProcessHandlerWithProcessingViewQuery))
	must(dependencies.Provide(handler.NewDocumentSelectionHandler))
	must(dependencies.Provide(handler.NewPaperMetadataHandler))
	must(dependencies.Provide(handler.NewPaperCitationHandler))
	must(dependencies.Provide(handler.NewPaperInfoHandler))
	must(dependencies.Provide(handler.NewDocumentBaseHandler))
	must(dependencies.Provide(handler.NewBibliographyHandler))
	must(dependencies.Provide(router.NewRouter))
	return dependencies
}

// must fails fast for invalid static DI registrations.
func must(err error) {
	if err != nil {
		panic(err)
	}
}
