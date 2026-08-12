# cdp-router 部署说明

## 目录结构

```text
deploy/
  build.sh                     构建并推送 cdp-router 镜像
  deploy.sh                    上传部署文件并可选重启服务
  docker-compose.infra.yml     基础组件：Nginx / PostgreSQL / Redis
  docker-compose.services.yml  业务服务：cdp-router
  init-extra-dbs.sh            PostgreSQL 初始化数据库脚本
  nginx.conf                   反向代理配置，支持 IP HTTP 和域名 HTTPS 访问
```

## 构建

```bash
cd /Users/Mark/Documents/Developer/GitMarkZhang/cdp-router/cdp-router

./deploy/build.sh
```

默认构建流程：

```text
../web/admin npm build -> cdp-router/web/admin/dist
make proto openapi
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -> bin/cdp-router-linux-amd64
docker build -f deploy/Dockerfile.runtime
docker push
```

这种方式复用本机 Go 和前端构建缓存，Docker 只负责打包运行镜像，通常比每次在 Docker 里编译快很多。

后台前端源码与 Go 服务目录同级维护：

```text
/Users/Mark/Documents/Developer/GitMarkZhang/cdp-router/web/admin
```

构建产物会同步到 Go 服务目录，仅用于镜像打包：

```text
/Users/Mark/Documents/Developer/GitMarkZhang/cdp-router/cdp-router/web/admin/dist
```

也可以单独执行后台页面构建和同步：

```bash
cd /Users/Mark/Documents/Developer/GitMarkZhang/cdp-router/cdp-router

make admin-sync
```

如果需要恢复 Docker 内完整编译：

```bash
BUILD_IN_DOCKER=y ./deploy/build.sh
```

默认镜像：

```text
crpi-u6rv1mtsub1wnpu2.cn-shanghai.personal.cr.aliyuncs.com/fuyu-webapi/cdp-router:<tag>
```

`build.sh` 会同步更新 `deploy/docker-compose.services.yml` 中的镜像 tag。

## 部署

```bash
cd /Users/Mark/Documents/Developer/GitMarkZhang/cdp-router/cdp-router

./deploy/deploy.sh <server_ip>
```

如果需要密码登录，可以临时通过环境变量传入：

```bash
SERVER_PASS='<password>' ./deploy/deploy.sh <server_ip>
```

推荐服务器提前配置 SSH key。

如果镜像仓库是私有仓库，远端服务器拉取业务镜像前也需要登录 registry。需要部署后自动重启 services 和 Nginx 时执行：

```bash
RESTART=y \
NGINX=y \
./deploy/deploy.sh <server_ip>
```

脚本内置了默认 SSH 密码和 registry 账号密码，会先在远端执行 `docker login`，再执行 services compose 的 `pull && up -d`。如果需要临时覆盖，可以继续通过 `SERVER_PASS`、`DOCKER_USERNAME`、`DOCKER_PASSWORD` 环境变量传入。

自动重启 services 时，脚本会先执行：

```bash
ssh root@<server_ip> 'cd /root && docker compose -f docker-compose.infra.yml up -d redis postgres'
```

这一步会创建 `root_default-network`，并保证业务服务依赖的 PostgreSQL、Redis 已启动。

## 远端文件位置

```text
/root/docker-compose.infra.yml                 infra compose
/opt/cdp-router/docker-compose.services.yml    services compose
/opt/cdp-router/config/cdp-router.conf         服务配置
/opt/cdp-router/logs/                          服务日志预留目录
/opt/cdp-router/init-extra-dbs.sh              PostgreSQL 初始化脚本
/opt/cdp-router/.env                           services 环境变量文件，默认空文件
/home/user/nginx/conf/nginx.conf               Nginx 配置
/home/user/nginx/conf/certs/                   SSL 证书目录
/home/user/nginx/conf/acme/                    ACME HTTP 验证目录
/home/user/redis/                              Redis 数据目录
```

## Docker Compose 拆分

基础组件：

```bash
ssh root@<server_ip> 'cd /root && docker compose -f docker-compose.infra.yml up -d'
```

需要先启动基础组件，业务服务依赖 infra compose 创建的 `root_default-network`。

业务服务：

```bash
ssh root@<server_ip> 'cd /opt/cdp-router && docker compose -f docker-compose.services.yml pull && docker compose -f docker-compose.services.yml up -d'
```

Nginx 配置更新后用 compose 重新拉起，不依赖固定容器名：

```bash
ssh root@<server_ip> 'cd /root && docker compose -f docker-compose.infra.yml up -d my-nginx'
```

## 域名和证书

IP HTTP 访问：

```text
http://8.149.131.7
```

域名 HTTPS 访问：

```text
https://openapi.mainland.vip
```

后台页面入口：

```text
https://openapi.mainland.vip/admin
```

后台登录页：

```text
https://openapi.mainland.vip/admin/login
```

`/admin`、`/admin/login`、`/admin/tasks`、`/admin/tasks/{id}` 都由 cdp-router 服务托管的 SPA 处理；Nginx 只需要把请求代理到 cdp-router，不需要为后台页面单独配置静态文件目录。

域名 HTTP 访问也会转发到服务，便于临时验证：

```text
http://openapi.mainland.vip
```

服务配置中的 `public_base_url` 当前使用 `https://openapi.mainland.vip`，生成给供应商的回调地址会使用域名。如果需要临时切回 IP，再把 `/opt/cdp-router/config/cdp-router.conf` 中的 `public_base_url` 改为 `http://8.149.131.7` 并重启 cdp-router。

证书文件放在宿主机：

```text
/home/user/nginx/conf/certs/openapi.mainland.vip.pem
/home/user/nginx/conf/certs/openapi.mainland.vip.key
```

容器内对应路径：

```text
/etc/data/certs/openapi.mainland.vip.pem
/etc/data/certs/openapi.mainland.vip.key
```

证书未放置前，不要启用 443 server，否则 Nginx 会因为证书文件缺失而无法启动。

ACME HTTP 验证目录：

```text
/home/user/nginx/conf/acme/
```

## 服务配置

线上配置文件位置：

```text
/opt/cdp-router/config/cdp-router.conf
```

部署脚本默认上传 `deploy/cdp-router.conf`。该文件使用 compose 网络内的 `postgres:5432`，本地开发配置仍保留在 `configs/cdp-router.conf`。

provider 当前通过配置文件维护，启动时加载到内存常驻。修改 provider token、接口地址或 `bucket_path_prefix` 后，需要重启 `cdp-router` 容器生效。
