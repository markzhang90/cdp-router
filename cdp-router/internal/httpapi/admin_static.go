package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

func serveAdminStatic(engine *gin.Engine) {
	distDir, ok := findAdminDist()
	if !ok {
		return
	}
	engine.GET("/admin", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/admin/tasks")
	})
	engine.GET("/admin/*path", func(c *gin.Context) {
		requestPath := strings.TrimPrefix(c.Param("path"), "/")
		if requestPath != "" {
			filePath := filepath.Join(distDir, requestPath)
			if isFile(filePath) {
				c.File(filePath)
				return
			}
		}
		c.File(filepath.Join(distDir, "index.html"))
	})
}

func findAdminDist() (string, bool) {
	for _, path := range []string{
		"web/admin/dist",
		"../web/admin/dist",
		"/app/web/admin/dist",
	} {
		if isFile(filepath.Join(path, "index.html")) {
			return path, true
		}
	}
	return "", false
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
