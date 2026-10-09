package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/jackc/pgx/v5"
)

func (r *TaskRepo) GetValidationRuleVersion(ctx context.Context) (string, error) {
	var version string
	err := r.db.QueryRow(ctx, `select version::text from cdp_result_validation_rules where id=1`).Scan(&version)
	if err != nil {
		return "", fmt.Errorf("read validation rule version: %w", err)
	}
	return version, nil
}

const validationRulesColumns = `version::text,keywords,risk_callback_allowed_provider_codes,updated_at,updated_by`

func scanValidationRules(row pgx.Row) (biz.ResultValidationRules, error) {
	var rules biz.ResultValidationRules
	var keywords, allowed []byte
	if err := row.Scan(&rules.Version, &keywords, &allowed, &rules.UpdatedAt, &rules.UpdatedBy); err != nil {
		return rules, err
	}
	if err := json.Unmarshal(keywords, &rules.Keywords); err != nil {
		return rules, fmt.Errorf("decode validation keywords: %w", err)
	}
	if err := json.Unmarshal(allowed, &rules.RiskCallbackAllowedProviderCodes); err != nil {
		return rules, fmt.Errorf("decode risk callback policy: %w", err)
	}
	return rules, nil
}

func (r *TaskRepo) GetValidationRules(ctx context.Context) (biz.ResultValidationRules, error) {
	rules, err := scanValidationRules(r.db.QueryRow(ctx, `select `+validationRulesColumns+` from cdp_result_validation_rules where id=1`))
	if err != nil {
		return rules, fmt.Errorf("read validation rules: %w", err)
	}
	return rules, nil
}

func (r *TaskRepo) SaveValidationRules(ctx context.Context, expected string, keywords []string, audit biz.AdminAuditLog) (biz.ResultValidationRules, error) {
	raw, err := json.Marshal(keywords)
	if err != nil {
		return biz.ResultValidationRules{}, err
	}
	return r.updateValidationRules(ctx, expected, audit, "UPDATE_RESULT_VALIDATION_RULES", "RESULT_VALIDATION_RULES", "1",
		map[string]any{"keywords": keywords},
		`keywords=$3`, raw)
}

func (r *TaskRepo) SaveProviderValidationRule(ctx context.Context, expected, providerCode string, enabled bool, audit biz.AdminAuditLog) (biz.ResultValidationRules, error) {
	return r.updateValidationRules(ctx, expected, audit, "UPDATE_PROVIDER_RISK_INTERCEPTION", "PROVIDER", providerCode,
		map[string]any{"providerCode": providerCode, "riskInterceptionEnabled": enabled},
		`risk_callback_allowed_provider_codes=case when $4::boolean then risk_callback_allowed_provider_codes - $3::text else (risk_callback_allowed_provider_codes - $3::text) || jsonb_build_array($3::text) end`, providerCode, enabled)
}

// assignment is an internal SQL fragment; request values are always bound parameters.
func (r *TaskRepo) updateValidationRules(ctx context.Context, expected string, audit biz.AdminAuditLog, action, resourceType, resourceID string, payload map[string]any, assignment string, values ...any) (biz.ResultValidationRules, error) {
	var result biz.ResultValidationRules
	version, err := strconv.ParseInt(expected, 10, 64)
	if err != nil || version < 1 {
		return result, biz.ErrInvalidTaskRequest
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	args := append([]any{version, audit.Username}, values...)
	result, err = scanValidationRules(tx.QueryRow(ctx, `update cdp_result_validation_rules set `+assignment+`,version=version+1,updated_by=$2,updated_at=now() where id=1 and version=$1 returning `+validationRulesColumns, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return result, biz.ErrValidationRulesConflict
	}
	if err != nil {
		return result, fmt.Errorf("save validation rules: %w", err)
	}
	payload["previousVersion"], payload["version"] = expected, result.Version
	raw, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	_, err = tx.Exec(ctx, `insert into cdp_admin_audit_logs (user_id,username,role,action,resource_type,resource_id,request_id,payload) values ($1,$2,$3,$4,$5,$6,$7,$8)`, audit.UserID, audit.Username, audit.Role, action, resourceType, resourceID, audit.RequestID, raw)
	if err != nil {
		return result, fmt.Errorf("audit validation rules: %w", err)
	}
	return result, tx.Commit(ctx)
}
