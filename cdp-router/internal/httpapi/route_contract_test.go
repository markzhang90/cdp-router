package httpapi

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/GitMarkZhang/cdp-router/internal/service"
	"gopkg.in/yaml.v3"
)

// Route inventory only: HTTP behavior is covered by real-persistence E2E tests.
func TestRoutesMatchGeneratedOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../openapi/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{}
	for path, operations := range doc.Paths {
		for method := range operations {
			switch method {
			case "get", "post", "put", "delete", "patch", "head", "options":
				expected[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	param := regexp.MustCompile(`:([^/]+)`)
	for _, route := range New(&service.TaskService{}, &service.AdminService{}).Engine().Routes() {
		if !strings.HasPrefix(route.Path, "/api/admin/v1/") && !strings.HasPrefix(route.Path, "/api/v1/") && !strings.HasPrefix(route.Path, "/external/") {
			continue
		}
		key := route.Method + " " + param.ReplaceAllString(route.Path, `{$1}`)
		if !expected[key] {
			t.Errorf("undocumented route: %s", key)
		}
		delete(expected, key)
	}
	for key := range expected {
		t.Errorf("unregistered contract: %s", key)
	}
}
