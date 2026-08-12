#!/bin/bash
# PostgreSQL 初始化时创建 cdp-router 所需数据库。
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    SELECT 'CREATE DATABASE "cdp-admin"'
    WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'cdp-admin')\gexec
EOSQL
