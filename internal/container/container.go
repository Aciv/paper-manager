// Package container registers application dependencies for runtime resolution.
package container

import (
	"paperMaster/internal/application/repository"
	appservice "paperMaster/internal/application/service"
	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/config"
	"paperMaster/internal/handler"
	"paperMaster/internal/infrastructure/docrecognition"
	"paperMaster/internal/router"

	"go.uber.org/dig"
)

// Build registers providers only. NewDatabase applies configured migrations;
// the caller owns server startup and resource cleanup.
func Build() *dig.Container {
	dependencies := dig.New()
	must(dependencies.Provide(config.Load))
	must(dependencies.Provide(repository.NewDatabase))
	must(dependencies.Provide(repository.NewDocumentRepository))
	must(dependencies.Provide(repository.NewPaperMetadataRepository))
	must(dependencies.Provide(repository.NewDocumentBaseRepository))
	must(dependencies.Provide(router.NewAsyncqClient))
	must(dependencies.Provide(router.NewDocumentTaskEnqueuer))
	must(dependencies.Provide(router.NewAsynqServer))
	must(dependencies.Provide(router.NewTaskInspector))
	must(dependencies.Provide(docrecognition.NewRecognizer))
	must(dependencies.Provide(appservice.NewDocumentServiceWithPaperMetadata))
	must(dependencies.Provide(appservice.NewDocumentBaseService))
	must(dependencies.Provide(appservice.NewDocumentHousekeepingService))
	must(dependencies.Provide(repository.NewTaskDeadLetterRepository))
	must(dependencies.Provide(fileservice.NewStorage))
	must(dependencies.Provide(handler.NewDocumentHandler))
	must(dependencies.Provide(handler.NewDocumentProcessHandler))
	must(dependencies.Provide(handler.NewPaperMetadataHandler))
	must(dependencies.Provide(handler.NewDocumentBaseHandler))
	must(dependencies.Provide(router.NewRouter))
	return dependencies
}

// must fails fast for invalid static DI registrations.
func must(err error) {
	if err != nil {
		panic(err)
	}
}
