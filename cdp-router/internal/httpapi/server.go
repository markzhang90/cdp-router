package httpapi

import (
	"net/http"

	"github.com/GitMarkZhang/cdp-router/internal/service"
	"github.com/gin-gonic/gin"
)

type Server struct {
	engine *gin.Engine
}

func New(taskService *service.TaskService, adminService *service.AdminService) *Server {
	engine := gin.New()
	engine.Use(gin.Recovery(), requestID(), accessLog())

	engine.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": 0, "data": gin.H{"ok": true}})
	})
	engine.GET("/ready", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": 0, "data": gin.H{"ok": true}})
	})
	engine.GET("/api/openapi.json", openAPIJSON("openapi/openapi.yaml"))
	serveAdminStatic(engine)

	engine.POST("/api/v1/tasks", taskService.CreateTask)
	engine.GET("/api/v1/external-tasks/:externalBatchId", taskService.GetExternalTask)
	engine.GET("/api/v1/external-tasks/:externalBatchId/result", taskService.GetExternalTaskResult)
	engine.GET("/api/v1/tasks/:id", taskService.GetTask)
	engine.GET("/api/v1/tasks/:id/provider-status", taskService.GetProviderStatus)
	engine.GET("/api/v1/tasks/:id/provider-result-files", taskService.GetProviderResultFiles)
	engine.POST("/api/v1/tasks/:id/refetch-result", taskService.RefetchTaskResult)
	engine.POST("/api/v1/tasks/:id/sync-provider-result", taskService.SyncProviderResult)
	engine.POST("/external/oss/:providerCode/upload-url", taskService.GetUploadURL)
	engine.POST("/external/callback/:providerCode", taskService.ProviderCallback)

	if adminService != nil {
		admin := engine.Group("/api/admin/v1")
		admin.POST("/login", adminService.Login)
		admin.GET("/me", adminService.RequireAuth(false), adminService.Me)
		admin.POST("/logout", adminService.RequireAuth(true), adminService.Logout)
		admin.PUT("/result-validation-rules/providers/:providerCode", adminService.RequireAuth(true), adminService.UpdateProviderValidationRule)
		admin.GET("/result-validation-rules", adminService.RequireAuth(false), adminService.GetResultValidationRules)
		admin.PUT("/result-validation-rules", adminService.RequireAuth(true), adminService.UpdateResultValidationRules)
		admin.GET("/tasks", adminService.RequireAuth(false), adminService.ListTasks)
		admin.GET("/tasks/:id", adminService.RequireAuth(false), adminService.GetTaskDetail)
		admin.POST("/tasks/:id/items-export", adminService.RequireAuth(true), adminService.ExportTaskItems)
		admin.POST("/tasks/:id/check-result", adminService.RequireAuth(true), adminService.CheckTaskResult)
		admin.POST("/tasks/:id/retry-callback", adminService.RequireAuth(true), adminService.RetryTaskCallback)
		admin.POST("/tasks/:id/force-import-callback", adminService.RequireAuth(true), adminService.ForceImportCallback)
		admin.POST("/tasks/reassign", adminService.RequireAuth(true), adminService.ReassignTask)
		admin.DELETE("/tasks/:id", adminService.RequireAuth(true), adminService.DeleteTask)
		admin.GET("/providers", adminService.RequireAuth(false), adminService.ListProviders)
		admin.GET("/dispatch-options", adminService.RequireAuth(false), adminService.GetDispatchOptions)
		admin.GET("/providers/:providerCode/metrics", adminService.RequireAuth(false), adminService.GetProviderMetrics)
		admin.GET("/platform-users", adminService.RequireAuth(false), adminService.ListPlatformUsers)
		admin.GET("/platform-users/:id", adminService.RequireAuth(false), adminService.GetPlatformUser)
		admin.GET("/platform-users/:id/metrics", adminService.RequireAuth(false), adminService.GetPlatformUserMetrics)
		admin.POST("/platform-users/:id/reconciliation-export", adminService.RequireAuth(true), adminService.ExportPlatformUserReconciliation)
		admin.POST("/platform-users", adminService.RequireAuth(true), adminService.CreatePlatformUser)
		admin.PUT("/platform-users/:id", adminService.RequireAuth(true), adminService.UpdatePlatformUser)
	}

	return &Server{engine: engine}
}

func (s *Server) Handler() http.Handler {
	return s.engine
}

func (s *Server) Engine() *gin.Engine {
	return s.engine
}
