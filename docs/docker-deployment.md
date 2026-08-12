# AIM Docker 一键部署手册

本文使用 Docker Hub 已发布镜像部署 aim.sh MySQL 控制台，不需要安装 Go、Node.js，也不需要在服务器上构建镜像。

- Docker Hub：[f00700f/aim-mysql-console](https://hub.docker.com/r/f00700f/aim-mysql-console)
- 当前固定版本：`1.0.41`
- 支持架构：`linux/amd64`

> AIM 控制台容器只负责管理平台本身。MySQL 实例安装在通过 SSH 纳管的目标 Linux 主机上，不会安装到控制台容器中。

## 一、部署要求

控制台服务器需要：

- x86_64/AMD64 Linux 主机。
- Docker Engine 24 或更高版本。
- Docker Compose v2。
- 至少 2 CPU、2 GiB 内存。
- 能通过 SSH 访问待管理的 MySQL 主机。
- 防火墙允许用户访问控制台 HTTPS 端口，默认 `8443/tcp`。

检查环境：

```bash
uname -m
docker version
docker compose version
```

`uname -m` 应输出 `x86_64`。ARM64 主机直接运行此镜像会出现 `exec format error`；不要依赖生产环境中的跨架构模拟。

## 二、推荐方式：Docker Compose 一键启动

### 1. 下载普通版部署文件

```bash
git clone --branch aim --single-branch https://github.com/aimdotsh/aim.git
cd aim
```

部署使用 `docker-compose.hub.yml`。该文件只从 Docker Hub 拉取镜像，不会读取 `Dockerfile` 或执行本地构建。

### 2. 创建配置

```bash
cp .env.sample .env
```

编辑 `.env`：

```dotenv
AIM_ADMIN_USER=admin
AIM_ADMIN_PASSWORD=请替换为至少12位的高强度密码
AIM_HTTPS_PORT=8443
AIM_TLS_HOSTS=localhost, 127.0.0.1, 192.168.31.10
AIM_MAX_UPLOAD_BYTES=2147483648
TZ=Asia/Shanghai
```

配置说明：

| 变量 | 说明 |
|---|---|
| `AIM_ADMIN_USER` | 空数据库首次启动时创建的管理员用户名 |
| `AIM_ADMIN_PASSWORD` | 初始管理员密码，至少 12 位 |
| `AIM_HTTPS_PORT` | 控制台在宿主机暴露的 HTTPS 端口 |
| `AIM_TLS_HOSTS` | 浏览器访问使用的域名或 IP，英文逗号分隔 |
| `AIM_MAX_UPLOAD_BYTES` | MySQL 安装介质最大上传字节数 |
| `AIM_IMAGE` | 可选，覆盖默认镜像标签或固定到摘要 |
| `TZ` | 控制台时区，也决定 Cron 备份计划的执行时区 |

`AIM_TLS_HOSTS` 必须包含实际访问地址。例如浏览器打开 `https://192.168.31.10:8443`，列表中就必须包含 `192.168.31.10`。

初始管理员只会在空数据库第一次启动时创建。后续修改 `.env` 中的密码不会重置数据库里的账号。

### 3. 生成加密主密钥

```bash
mkdir -p secrets
openssl rand -base64 32 > secrets/aim_master_key
chmod 600 secrets/aim_master_key
```

该密钥用于加密控制台保存的 SSH 私钥和 MySQL 密码。必须单独备份；密钥丢失后，原有加密数据无法恢复。

### 4. 拉取并启动

```bash
docker compose -f docker-compose.hub.yml pull
docker compose -f docker-compose.hub.yml up -d
```

这就是日常所说的“一键部署”：`up -d` 会创建持久卷、内部网络、AIM 控制台和 Caddy HTTPS 入口，并等待控制台健康后启动入口服务。

查看状态：

```bash
docker compose -f docker-compose.hub.yml ps
docker compose -f docker-compose.hub.yml logs -f console
```

健康的控制台应显示 `healthy`。浏览器访问：

```text
https://<控制台服务器IP>:8443
```

Caddy 默认使用内部 CA。首次访问可能出现证书不受信任提示；测试环境可手工确认，生产环境应导入 Caddy 根证书或替换为企业证书。

## 三、真正的单条 docker run

下面的方式不启动 Caddy，只提供 HTTP，适合本机临时体验，不建议直接暴露到公网：

```bash
docker volume create aim-data

docker run -d \
  --name aim-console \
  --restart unless-stopped \
  --platform linux/amd64 \
  -p 127.0.0.1:8080:8080 \
  -v aim-data:/var/lib/aim-console \
  -e AIM_LISTEN=:8080 \
  -e AIM_DATA_DIR=/var/lib/aim-console \
  -e AIM_BACKUP_ROOT=/var/lib/aim-console/backups \
  -e AIM_MASTER_KEY='请替换为随机长字符串' \
  -e AIM_ADMIN_USER=admin \
  -e AIM_ADMIN_PASSWORD='请替换为至少12位的高强度密码' \
  -e AIM_COOKIE_SECURE=false \
  f00700f/aim-mysql-console:1.0.41
```

访问 `http://127.0.0.1:8080`。如果需要让其他电脑访问，应通过 Nginx、Caddy 或 Traefik 提供 HTTPS，再把 `AIM_COOKIE_SECURE` 改为 `true`。

不要在共享服务器上直接把敏感值写入 Shell History。正式环境推荐使用前面的 Compose + Docker Secret 方式。

## 四、数据保存在哪里

Compose 默认创建 `aim-data` Docker volume：

```text
/var/lib/aim-console/aim.db       控制台 SQLite 数据库
/var/lib/aim-console/uploads      上传中的分片
/var/lib/aim-console/media        MySQL 安装介质
/var/lib/aim-console/backups      在线备份文件
```

实际卷名由 Compose 项目名决定，可先通过标签查找：

```bash
docker volume ls --filter label=com.docker.compose.volume=aim-data
```

例如项目目录名为 `aim` 时，默认卷名通常是 `aim_aim-data`。确认名称后可执行
`docker volume inspect aim_aim-data` 查看挂载点和元数据。

停止容器不会删除数据：

```bash
docker compose -f docker-compose.hub.yml down
```

不要运行 `docker compose -f docker-compose.hub.yml down -v`，除非明确要永久删除控制台数据库、安装介质和在线备份。

## 五、首次登录后的操作

1. 使用 `.env` 中的初始管理员账号登录。
2. 打开“主机资源”，下载 Host Kit 2.4.14。
3. 在管理电脑执行 `./aim-copy-id --install user@目标主机`。
4. 在控制台添加主机，SSH 用户填写 `aimops`。
5. 导入 `~/.ssh/aim/aim_console_ed25519` 私钥，不要导入 `.pub` 公钥。
6. 确认 SSH 指纹并执行主机探测。
7. 在“部署向导”选择单机、主从或三节点 MGR。

目标机初始化详见 [Host Kit 使用说明](../HOST-KIT.md)。

## 六、固定镜像版本和摘要

生产环境不建议使用 `latest`。默认 Compose 已固定到 `1.0.41`：

```text
f00700f/aim-mysql-console:1.0.41
```

对供应链可重复性要求更高时，可在 `.env` 固定摘要：

```dotenv
AIM_IMAGE=f00700f/aim-mysql-console@sha256:<从发布说明或 Docker Hub 获取的镜像摘要>
```

然后重新拉取并启动：

```bash
docker compose -f docker-compose.hub.yml pull
docker compose -f docker-compose.hub.yml up -d
```

## 七、升级和回滚

升级前备份 `aim-data` 卷和 `secrets/aim_master_key`。修改 `.env` 中的 `AIM_IMAGE` 后执行：

```bash
docker compose -f docker-compose.hub.yml pull
docker compose -f docker-compose.hub.yml up -d
docker compose -f docker-compose.hub.yml ps
docker compose -f docker-compose.hub.yml logs --tail=200 console
```

回滚时把 `AIM_IMAGE` 改回旧标签或旧摘要，再执行同样命令。不要在没有数据库备份的情况下跨版本反复切换。

控制台升级后，如果目标主机的备份或监控提示“不支持的 action”，需要在每台目标机重新运行配套 Host Kit。重复安装 Host Kit 是幂等的，不会删除 MySQL 数据。

## 八、备份控制台自身

以下示例把整个数据卷导出到当前目录：

```bash
mkdir -p controller-backup
# 按上一节 docker volume ls 的结果填写真实卷名
AIM_DATA_VOLUME=aim_aim-data

docker run --rm \
  -v "${AIM_DATA_VOLUME}:/source:ro" \
  -v "$PWD/controller-backup:/backup" \
  alpine:3.23 \
  tar -czf /backup/aim-data-$(date +%F).tar.gz -C /source .

cp secrets/aim_master_key controller-backup/
chmod 600 controller-backup/aim_master_key
```

如果变量中的卷名写错，Docker 会创建一个同名空卷，得到的备份也会是空的；因此执行前务必通过
`docker volume ls --filter label=com.docker.compose.volume=aim-data` 确认。备份文件和主密钥必须分开保护，并定期验证恢复。

## 九、常见问题

### `exec format error`

控制台主机不是 AMD64。当前镜像只提供 `linux/amd64`，应换用 x86_64 主机。

### 容器反复重启并提示管理员参数无效

空数据库首次启动必须设置 `AIM_ADMIN_USER` 和至少 12 位的 `AIM_ADMIN_PASSWORD`。检查 `.env` 后重新启动。

### 修改 `.env` 密码后仍无法登录

管理员只在数据库为空时创建，修改环境变量不会覆盖现有账号。请使用控制台用户管理功能，或按经过验证的数据恢复流程处理；不要直接删除数据卷。

### 浏览器证书报错

确认访问 IP/域名已经写入 `AIM_TLS_HOSTS`，然后执行：

```bash
docker compose -f docker-compose.hub.yml up -d --force-recreate caddy
```

### 控制台能打开但连接不到目标主机

在控制台服务器上确认能够访问目标机 SSH 端口，并检查云安全组、防火墙、路由、SSH 用户、专用私钥和已确认的主机指纹。

### 上传大文件出现 `413 Request Entity Too Large`

默认上限为 2 GiB。若需要调整，应同时修改 `.env` 的 `AIM_MAX_UPLOAD_BYTES` 和 `deploy/Caddyfile` 的 `request_body max_size`，然后重建 Caddy 容器。
