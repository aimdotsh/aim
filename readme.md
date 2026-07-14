# AIM

`aim.sh` 使用 Oracle MySQL Community Server 官方通用二进制包，在一台 Linux 主机上安装相互隔离的 MySQL 实例。支持单机、主库和 GTID 从库。

## 支持范围

| MySQL | x86_64 | i686 | ARM64 | 初始化方式 | 复制命令 |
|---|---:|---:|---:|---|---|
| 5.6.x | 是 | 否 | 否 | `mysql_install_db` | `CHANGE MASTER` / `START SLAVE` |
| 5.7.x | 是 | 否 | 否 | 5.7.6 前使用 `mysql_install_db`，之后使用 `mysqld --initialize-insecure` | `CHANGE MASTER` / `START SLAVE` |
| 8.0.x | 是 | 是 | 是 | `mysqld --initialize-insecure` | 8.0.23 起使用 `CHANGE REPLICATION SOURCE` |
| 8.4.x | 是 | 是（以官网实际发布为准） | 是 | `mysqld --initialize-insecure` | `CHANGE REPLICATION SOURCE` / `START REPLICA` |

操作系统支持 RHEL/CentOS/Rocky/AlmaLinux/Oracle Linux、Debian/Ubuntu、SLES/openSUSE 等 glibc Linux。脚本自动识别 `dnf`、`yum`、`apt` 或 `zypper`，并识别 x86_64、i686 和 aarch64。Alpine 等 musl 系统不能直接运行 Oracle 通用二进制包，因此会在安装前明确退出。

在启用 SELinux 的 RHEL 系统上，脚本会为自定义数据、日志、临时目录和非默认 TCP 端口配置持久上下文；缺少管理工具时会安装发行版对应的 policycoreutils 包或给出明确错误。

MySQL 5.6/5.7 已停止官方维护。脚本仍支持安装归档版本，但生产环境应优先选择仍受支持的 8.0/8.4，并自行承担旧版本安全和系统动态库兼容风险。

## 快速开始

使用精确的三段版本号：

```bash
# 单机
sudo ./aim.sh -v 8.4.5 -p 3306 --role standalone

# 主库：创建只允许 10.0.0.12 使用的复制账号
sudo ./aim.sh -v 8.0.42 -p 3306 --role source \
  --replica-host 10.0.0.12 --repl-password 'replace-me'

# 从库：连接刚安装、尚无业务数据的主库
sudo ./aim.sh -v 8.0.42 -p 3306 --role replica \
  --source-host 10.0.0.11 --source-port 3306 \
  --source-user aim_repl --source-password 'replace-me'
```

没有传入 root 或复制密码时，脚本会生成高强度随机密码，只在安装结束时显示，不写入磁盘。自动化环境建议通过 `AIM_ROOT_PASSWORD`、`AIM_REPL_PASSWORD`、`AIM_SOURCE_PASSWORD` 环境变量从秘密管理系统注入；命令行密码参数可能被本机进程列表或 shell history 看见。优先级为命令行、环境变量、配置文件、内置默认值。

## 安装包

脚本先检测本机 glibc，再在 `media/` 中查找与版本、glibc 基线和 CPU 架构匹配的官方包，找不到时依次从 MySQL 当前下载区和官方 Archives 下载。对于 MySQL 8.x，本机 glibc 2.28 或更高时优先选择 `glibc2.28` 包，并自动回退到兼容的 `glibc2.17` 包；低于 2.28 时不会误选 2.28 包。

同时支持压缩的 `.tar.xz`、`.tar.gz`/`.tgz` 和未压缩的 `.tar`。例如 glibc 2.28 x86_64 主机会按以下顺序查找：

```text
mysql-8.0.46-linux-glibc2.28-x86_64.tar.xz
mysql-8.0.46-linux-glibc2.28-x86_64.tar
mysql-8.0.46-linux-glibc2.17-x86_64.tar.xz
mysql-8.0.46-linux-glibc2.17-x86_64.tar
```

在 glibc 2.17 x86_64 上，如果完整包不可用，还会继续识别：

```text
mysql-8.0.46-linux-glibc2.17-x86_64-minimal.tar.xz
mysql-8.0.46-linux-glibc2.17-x86_64-minimal.tar
```

`mysql-test-*` 是测试套件而不是数据库服务器安装包，AIM 不会把它作为安装介质；显式传入时会在解压前直接拒绝。

离线安装也可以显式指定：

```bash
sudo ./aim.sh -v 5.7.44 -p 3307 \
  --archive /mnt/packages/mysql-5.7.44-linux-glibc2.12-x86_64.tar.gz \
  --no-download
```

也可以用 `--download-url URL` 指定企业镜像。脚本解压后会调用 `mysqld --version` 校验包内版本，避免装错软件包。

## 目录与服务

默认目录如下，可用同名参数或 `etc/config` 修改：

```text
/opt/mysql/<version>       软件目录
/data/mysql/<port>/data    数据目录
/data/mysql/<port>/my.cnf  实例配置
/var/log/mysql/<port>      日志、binlog、relay log
/var/tmp/mysql/<port>      临时目录
```

systemd 环境会创建 `aim-mysql-<port>.service` 并立即启用。非 systemd 环境会用 `mysqld_safe` 启动，同时总会在 `/opt/mysql/` 生成 `start-<port>.sh` 和 `stop-<port>.sh`。停止脚本要求先导出密码：

```bash
export MYSQL_ROOT_PASSWORD='your-password'
sudo -E /opt/mysql/stop-3306.sh
```

卸载前先预览，再明确确认。卸载器只删除该端口的实例数据和服务，保留同版本可共享的软件目录及 `media/` 安装包：

```bash
sudo ./unaim.sh -v 8.4.5 -p 3306 --dry-run
sudo AIM_ROOT_PASSWORD='your-password' ./unaim.sh -v 8.4.5 -p 3306 --yes
```

## 配置和检查

查看所有参数：

```bash
./aim.sh --help
```

在目标 Linux 上仅检查版本、端口、系统、架构、下载包选择和将执行的动作：

```bash
./aim.sh -v 8.4.5 -p 3306 --dry-run --skip-deps
```

配置文件是受信任的 Bash 配置，会被 `source`。不要使用来源不明的配置文件。命令行参数优先于配置文件。

配置优先级为：命令行参数 > `AIM_*` 密码环境变量 > 配置文件 > 内置默认值。推荐先执行 `--dry-run`，确认系统识别、安装包名称和目录规划符合预期后再正式安装。

如果旧版 AIM 首次安装时出现 `Failed to set datadir ... errno: 13 - Permission denied`，先清理失败实例并修复默认根目录的穿越权限，再重试：

```bash
sudo ./unaim.sh -v 8.0.46 -p 8046 --yes
sudo chmod 0755 /data /data/mysql /opt/mysql \
  /var/log/mysql /var/tmp/mysql
namei -l /data/mysql/8046/data
sudo ./aim.sh -v 8.0.46 -p 8046
```

新版 AIM 会以可穿越权限创建缺失的安装根目录，并在初始化前以 `mysql` 用户实际写入数据、日志和临时目录；权限或 SELinux 仍不兼容时，会输出具体目录层级后退出。

## 主从约束

自动主从流程面向“一主一从均为新建空实例”的场景，默认启用 GTID，不再配置 SSH 免密或远程使用 root。步骤是：

1. 先用 `--role source` 安装主库并创建复制账号。
2. 再用 `--role replica` 安装从库并连接主库。
3. 从库安装结束时会输出 `SHOW SLAVE STATUS\G` 或 `SHOW REPLICA STATUS\G`。

如果主库已经包含业务数据，必须先使用经过验证的物理备份、Clone Plugin 或逻辑备份建立一致性基线，再配置复制；本脚本不会冒险自动搬迁已有数据。

## 设计上的安全改进

- 不覆盖 `/etc/my.cnf`，不同端口实例互不影响。
- 不修改整个 `/etc/security/limits.conf`，只写独立 drop-in。
- 安装前检查 root、系统、glibc、架构、端口、目录和包内版本。
- 配置按 5.6/5.7 与 8.x 分支生成，避免向 8.x 写入已删除参数。
- 不再把操作系统 SSH 密码或数据库密码写进仓库配置。

仓库中的 `auto_ssh*`、`ssh-copy-id`、`my56.cnf`、`my57.cnf`、旧 init 脚本和 `tool/` RPM 仅为 1.x 历史材料；2.x 安装和主从流程不会调用它们。
