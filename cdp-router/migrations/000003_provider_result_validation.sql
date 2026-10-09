alter table cdp_result_validation_rules add column if not exists disabled_provider_codes jsonb not null default '[]'::jsonb;
