package service

import (
	"net/http"
	"time"

	adminv1 "github.com/GitMarkZhang/cdp-router/api/admin/v1"
	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/trace"
	"github.com/gin-gonic/gin"
)

func requireAdminWrite(c *gin.Context, adminOnly bool) bool {
	session, ok := adminSessionFromContext(c)
	if !ok {
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return false
	}
	if session.User.Role != "admin" && (adminOnly || session.User.Role != "operator") {
		errorJSON(c, http.StatusForbidden, biz.ErrAdminForbidden)
		return false
	}
	return true
}
func (s *AdminService) GetResultValidationRules(c *gin.Context) {
	rules, interval, err := s.tasks.GetValidationRules(c.Request.Context())
	if err != nil {
		writeBizError(c, err)
		return
	}
	protoJSON(c, http.StatusOK, &adminv1.ResultValidationRulesResponse{Data: validationRulesProto(rules, interval)})
}
func (s *AdminService) UpdateResultValidationRules(c *gin.Context) {
	if !requireAdminWrite(c, true) {
		return
	}
	var req adminv1.UpdateResultValidationRulesRequest
	if _, err := bindProto(c, &req); err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	session, _ := adminSessionFromContext(c)
	rules, err := s.tasks.SaveValidationRules(c.Request.Context(), req.ExpectedVersion, req.Keywords, biz.AdminAuditLog{
		UserID: session.User.UserID, Username: session.User.Username, Role: session.User.Role, RequestID: trace.RequestID(c.Request.Context()),
	})
	if err != nil {
		writeBizError(c, err)
		return
	}
	// Saving only persists the configuration. Each instance refreshes on its timer.
	protoJSON(c, http.StatusOK, &adminv1.ResultValidationRulesResponse{Message: "已保存，定时刷新后对新导入生效", Data: validationRulesProto(rules, s.tasks.ValidationRefreshInterval())})
}
func validationRulesProto(rules biz.ResultValidationRules, interval time.Duration) *adminv1.ResultValidationRulesData {
	return &adminv1.ResultValidationRulesData{Version: rules.Version, Keywords: rules.Keywords, RiskCallbackAllowedProviderCodes: rules.RiskCallbackAllowedProviderCodes, UpdatedAt: formatTime(rules.UpdatedAt), UpdatedBy: rules.UpdatedBy, RefreshIntervalSeconds: int32(interval / time.Second)}
}
func answerRisksProto(risks []biz.AnswerRisk) []*adminv1.AnswerRisk {
	out := make([]*adminv1.AnswerRisk, 0, len(risks))
	for _, risk := range risks {
		out = append(out, &adminv1.AnswerRisk{Code: risk.Code, Message: risk.Message, ActualLength: int32(risk.ActualLength), MinLength: int32(risk.MinLength), MatchedKeywords: risk.MatchedKeywords})
	}
	return out
}
func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatTime(*value)
}

func (s *AdminService) UpdateProviderValidationRule(c *gin.Context) {
	if !requireAdminWrite(c, true) {
		return
	}
	var req adminv1.UpdateProviderValidationRuleRequest
	if _, err := bindProto(c, &req); err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	if req.RiskInterceptionEnabled == nil {
		errorJSON(c, http.StatusBadRequest, biz.ErrInvalidTaskRequest)
		return
	}
	session, _ := adminSessionFromContext(c)
	rules, err := s.tasks.SaveProviderValidationRule(c.Request.Context(), req.ExpectedVersion, c.Param("providerCode"), *req.RiskInterceptionEnabled, biz.AdminAuditLog{
		UserID: session.User.UserID, Username: session.User.Username, Role: session.User.Role, RequestID: trace.RequestID(c.Request.Context()),
	})
	if err != nil {
		writeBizError(c, err)
		return
	}
	protoJSON(c, http.StatusOK, &adminv1.ResultValidationRulesResponse{Message: "已保存，定时刷新后对待回调任务生效", Data: validationRulesProto(rules, s.tasks.ValidationRefreshInterval())})
}
