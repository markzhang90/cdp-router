package data

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PlatformUserRepo struct {
	db *pgxpool.Pool
}

func NewPlatformUserRepo(db *pgxpool.Pool) *PlatformUserRepo {
	return &PlatformUserRepo{db: db}
}

func (r *PlatformUserRepo) SyncFromConfig(ctx context.Context, users map[string]config.PlatformUserConfig) error {
	for _, user := range users {
		callbackConfig := json.RawMessage(user.CallbackConfig)
		if len(callbackConfig) == 0 {
			callbackConfig = json.RawMessage(`{}`)
		}
		if !json.Valid(callbackConfig) {
			callbackConfig = json.RawMessage(`{}`)
		}
		_, err := r.db.Exec(ctx, `
insert into platform_users (
  platform_code,name,api_key,status,default_provider_code,callback_mode,callback_url,callback_config
) values ($1,$2,$3,$4,$5,$6,$7,$8)
on conflict (platform_code) do update set
  name=excluded.name,
  api_key=excluded.api_key,
  status=excluded.status,
  default_provider_code=excluded.default_provider_code,
  callback_mode=excluded.callback_mode,
  callback_url=excluded.callback_url,
  callback_config=excluded.callback_config,
  updated_at=now()
`, user.PlatformCode, user.Name, user.APIKey, user.Status, user.DefaultProviderCode, user.CallbackMode, user.CallbackURL, callbackConfig)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *PlatformUserRepo) GetByAPIKey(ctx context.Context, apiKey string) (biz.PlatformUser, error) {
	var user biz.PlatformUser
	err := r.db.QueryRow(ctx, `
select id,platform_code,name,api_key,status,default_provider_code,callback_mode,coalesce(callback_url,''),callback_config,created_at,updated_at
from platform_users
where api_key=$1
`, apiKey).Scan(&user.ID, &user.PlatformCode, &user.Name, &user.APIKey, &user.Status, &user.DefaultProviderCode,
		&user.CallbackMode, &user.CallbackURL, &user.CallbackConfig, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.PlatformUser{}, biz.ErrPlatformUserNotFound
	}
	if err != nil {
		return biz.PlatformUser{}, err
	}
	if user.Status != "ACTIVE" {
		return biz.PlatformUser{}, biz.ErrPlatformUserDisabled
	}
	return user, nil
}

var _ biz.PlatformUserRepo = (*PlatformUserRepo)(nil)
