package httpapi

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

func openAPIJSON(path string) gin.HandlerFunc {
	return func(c *gin.Context) {
		content, err := os.ReadFile(path)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"status": http.StatusNotFound, "message": err.Error(), "data": nil})
			return
		}
		var doc any
		if err := yaml.Unmarshal(content, &doc); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": http.StatusInternalServerError, "message": err.Error(), "data": nil})
			return
		}
		body, err := json.Marshal(doc)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": http.StatusInternalServerError, "message": err.Error(), "data": nil})
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", body)
	}
}
