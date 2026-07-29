package router

import (
	"strings"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/handler"
	"paperMaster/internal/middleware"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"go.uber.org/dig"
)

// RouterParams 路由参数
type RouterParams struct {
	dig.In

	Config                 config.Config
	FileService            interfaces.FileStorage
	DocumentHandler        *handler.DocumentHandler
	DocumentProcessHandler *handler.DocumentProcessHandler
	PaperMetadataHandler   *handler.PaperMetadataHandler
	PaperCitationHandler   *handler.PaperCitationHandler
	PaperInfoHandler       *handler.PaperInfoHandler
	DocumentBaseHandler    *handler.DocumentBaseHandler
}

// NewRouter 创建新的路由
func NewRouter(params RouterParams) *gin.Engine {
	r := gin.New()
	r.ContextWithFallback = true

	// origin: protocol, domain, port
	// CORS
	r.Use(cors.New(cors.Config{
		AllowOrigins:     corsOrigins(params.Config.CORSOrigin),
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-API-Key", "X-Request-ID", "X-Tenant-ID", "X-Embed-Session", "X-External-User-ID", "X-External-User-Token"},
		ExposeHeaders:    []string{"Content-Length", "Access-Control-Allow-Origin"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	// err free
	r.Use(middleware.RequestID())
	//r.Use(middleware.Language())
	r.Use(middleware.Logger())
	// begin Recovery
	r.Use(middleware.ErrorHandler())
	r.Use(middleware.Recovery())

	// healthy check
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})
	ServerFile(r, params.DocumentHandler)
	ServerProcess(r, params.DocumentProcessHandler)
	ServerPaperMetadata(r, params.PaperMetadataHandler, params.PaperCitationHandler, params.PaperInfoHandler)
	ServerDocumentBase(r, params.DocumentBaseHandler)

	return r
}

func corsOrigins(value string) []string {
	var origins []string
	for _, origin := range strings.Split(value, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			origins = append(origins, origin)
		}
	}
	if len(origins) == 0 {
		return []string{"http://localhost:5173"}
	}
	return origins
}

// ServerFile registers source-file and document-record endpoints.
func ServerFile(router gin.IRoutes, documentHandler *handler.DocumentHandler) {
	if documentHandler == nil {
		return
	}
	router.POST("/api/document/upload", documentHandler.Upload)
	router.GET("/api/document/search", documentHandler.Search)
	router.GET("/api/document/:id/content", documentHandler.Content)
	router.GET("/api/document/:id/blocks", documentHandler.ListBlocks)
	router.GET("/api/document/:id/blocks/:blockID/image", documentHandler.BlockImage)
	router.PATCH("/api/document/:id", documentHandler.UpdateDocument)
	router.DELETE("/api/document/:id", documentHandler.DeleteFile)
}

// ServerProcess registers asynchronous document-processing controls.
func ServerProcess(router gin.IRoutes, processHandler *handler.DocumentProcessHandler) {
	if processHandler == nil {
		return
	}
	router.POST("/api/document/:id/cancel", processHandler.CancelDocumentParse)
}

// ServerPaperMetadata registers the bibliography and paper-search API.
func ServerPaperMetadata(
	router gin.IRoutes,
	metadataHandler *handler.PaperMetadataHandler,
	citationHandler *handler.PaperCitationHandler,
	infoHandler *handler.PaperInfoHandler,
) {
	if metadataHandler != nil {
		router.GET("/api/paper/search", metadataHandler.Search)
		router.GET("/api/paper/:documentID", metadataHandler.Get)
		router.PATCH("/api/paper/:documentID", metadataHandler.Update)
	}
	if citationHandler != nil {
		router.GET("/api/paper/:documentID/citations", citationHandler.List)
		router.POST("/api/paper/:documentID/citations", citationHandler.Add)
		router.PUT("/api/paper/:documentID/citations", citationHandler.Replace)
		router.POST("/api/paper/:documentID/citations/resolve", citationHandler.Resolve)
		router.DELETE("/api/paper/:documentID/citations/:citedDocumentID", citationHandler.Remove)
		router.GET("/api/paper/:documentID/cited-by", citationHandler.ListCitedBy)
	}
	if infoHandler != nil {
		router.GET("/api/paper/:documentID/info", infoHandler.Status)
		router.POST("/api/paper/:documentID/info/extraction/retry", infoHandler.RetryExtraction)
		router.POST("/api/paper/:documentID/info/catalog/retry", infoHandler.RetryCatalog)
	}
}

// ServerDocumentBase registers collection and collection-membership API.
func ServerDocumentBase(router gin.IRoutes, baseHandler *handler.DocumentBaseHandler) {
	if baseHandler == nil {
		return
	}
	router.POST("/api/document/bases", baseHandler.Create)
	router.GET("/api/document/bases", baseHandler.List)
	router.GET("/api/document/bases/:baseID", baseHandler.Get)
	router.PATCH("/api/document/bases/:baseID", baseHandler.Update)
	router.DELETE("/api/document/bases/:baseID", baseHandler.Delete)
	router.GET("/api/document/bases/:baseID/documents", baseHandler.ListDocuments)
	router.PUT("/api/document/bases/:baseID/documents/:documentID", baseHandler.AddDocument)
	router.DELETE("/api/document/bases/:baseID/documents/:documentID", baseHandler.RemoveDocument)
}
