# AIM Web 控制台部署指南

## 架构与安全边界

AIM Web 控制台是单实例内网运维平台：

```text
浏览器 -> Caddy HTTPS -> aim-console -> SQLite / 安装包仓库
                                      |
                                      +-- SSH/SFTP -> aimops -> sudo aim-executor -> aim.sh
```

`aim-executor` 不是常驻 Agent，不监听网络端口。它只在控制台通过 SSH 提交一个符合严格 schema 的 JSON 任务时启动，且：

- 拒绝未知 JSON 字段、任意命令、任意根目录和 `aim.sh -c` 配置文件。
- 在目标机重新校验安装包大小、SHA-256、版本、glibc、架构和压缩格式魔数，并拒绝符号链接越界。
- 只以参数数组调用 root-owned `aim.sh`，不使用 shell 拼接命令。
- 通过环境变量传递 MySQL 密码，启用 `--no-print-secrets`，并对远程输出再次脱敏。

控制台将 MySQL 密码和 SSH 私钥使用 AES-256-GCM 加密后写入 SQLite。主密钥不存在数据库中，由 Docker secret 单独挂载。

## 启动控制台

要求：Docker Engine 24+ 与 Docker Compose v2。

```bash
git clone https://github.com/aimdotsh/aim.git
cd aim
cp .env.sample .env
mkdir -p secrets
openssl rand -base64 32 > secrets/aim_master_key
chmod 600 secrets/aim_master_key
```

编辑 `.env`，至少替换 `AIM_ADMIN_PASSWORD`。密码不得少于 12 个字符。初始管理员只会在空数据库首次启动时创建，以后修改 `.env` 不会覆盖已有用户。

```bash
docker compose up -d --build
docker compose ps
docker compose logs -f console
```

默认 HTTPS 端口是 `8443`。首次使用 Caddy 内部 CA 时，可导出根证书后交由内网管理员分发信任：

```bash
docker compose cp \
  caddy:/data/caddy/pki/authorities/local/root.crt \
  ./aim-caddy-root.crt
```

如已有企业 CA 或统一反向代理，请替换 `deploy/Caddyfile` 的 `tls internal`，不要在生产内网绕过 HTTPS。

SQLite、上传中的分块和安装包保存在 Docker volume `aim-data`。备份时应同时保存该 volume 和 `secrets/aim_master_key`；丢失主密钥后无法恢复已加密的 SSH 私钥及 MySQL 密码。

## 准备控制台 SSH 密钥

建议为 AIM 单独生成一对密钥，不要复用 root 或个人运维密钥：

```bash
ssh-keygen -t ed25519 -a 100 -f ./secrets/aim_console_ed25519
chmod 600 ./secrets/aim_console_ed25519
```

私钥在网页添加主机时录入，随即被加密保存。公钥通过下一节的初始化脚本安装到目标机。

## 准备每台 MySQL 目标机

目标机需要 `aim.sh`、与目标 CPU 匹配的 Linux `aim-executor` 以及初始化脚本。例如 x86_64：

```bash
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o dist/aim-executor ./cmd/aim-executor

scp aim.sh scripts/bootstrap-target.sh dist/aim-executor \
  root@172.20.23.90:/root/
scp secrets/aim_console_ed25519.pub \
  root@172.20.23.90:/root/
```

在目标机执行：

```bash
cd /root
chmod +x aim.sh aim-executor bootstrap-target.sh
sudo ./bootstrap-target.sh \
  --public-key ./aim_console_ed25519.pub \
  --aim-script ./aim.sh \
  --executor ./aim-executor
```

该脚本会：

- 创建专用 `aimops` 账号和安装包暂存目录。
- 把 `aim.sh` 安装为 root-owned `/opt/aim/aim.sh`。
- 把执行器安装为 root-owned `/usr/local/sbin/aim-executor`。
- 创建 `/etc/aim/executor.json` 安全边界配置。
- 使 `aimops` 只能无密码 sudo 运行无命令行参数的 `aim-executor`。执行器自身也会拒绝任何命令行参数。

ARM64 目标机把 `GOARCH=amd64` 替换为 `GOARCH=arm64`。三台 MGR 主机都需执行相同初始化。

## 纳管主机与部署

1. 使用管理员登录，在“主机资源”录入主机、`aimops` 和专用 SSH 私钥。
2. 点击“确认指纹”，在目标机或可信 CMDB 中核对 SSH SHA-256 指纹后确认。之后指纹变更会被直接拒绝。
3. 执行主机探测。只有探测成功的主机才会出现在部署向导中。
4. 如需离线安装，在“安装介质”上传官方 MySQL Generic 包。上传可断点续传，完成后才会进入介质库；部署页会按已选主机的架构和 glibc 自动推荐兼容包。
5. 在“部署向导”选择单机、主库、从库、一主一从或三节点 MGR。

MGR 会自动生成组 UUID、三节点 seeds，以及默认只包含三个成员精确 IP 的 allowlist，并按页面节点顺序执行 bootstrap、join、join。第一个成员没有进入 `ONLINE` 时不会继续第二台；任意 join 失败时不会自动删除前面已完成的数据。需要放宽网段时可以在部署页显式覆盖 allowlist。

多主机任务失败后，可在任务中心“从失败节点重试”。控制台会跳过状态已记录为完成且 SQL/MGR 端口仍在线的节点，只传输介质并执行未完成节点。控制台重启时，进行中的任务会变成“待核实”；先点击“核实远端状态”，确认未完成节点没有监听目标端口后，才能进入可重试状态，避免盲目重复远端操作。

## 权限和破坏性操作

| 角色 | 权限 |
|---|---|
| 管理员 | 用户、主机、部署、密码查看、重新初始化、卸载和审计 |
| 操作员 | 上传介质、部署、启动、停止、状态检查和任务日志 |
| 只读用户 | 主机、介质、实例、集群和任务查看 |

重新初始化和卸载只允许管理员执行。控制台先创建 `dry-run` 任务，预览成功后要求输入“主机地址:端口”。预览只在 15 分钟内有效，不会被后端自动重放。

## 开发与验证

```bash
make test
make build
docker compose config
docker compose build
```

`make test` 会运行 Bash 语法与行为测试、Go 单元测试与静态检查，并重新生成 Vue 生产资源。
