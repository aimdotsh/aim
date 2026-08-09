# AIM 懒猫微服版

本移植版基于 AIM 2.3.0（上游提交 `cd7277b54010e5e07f4714531ff167721a23023a`）并增加：

- 懒猫微服 OIDC 单点登录。`ADMIN` 组映射为 AIM `admin`，其他账号默认为只读 `viewer`。
- 通过懒猫官方 browser inject 自动接管原生文件输入框，提供本地文件系统与懒猫网盘两种来源。选中的 MySQL Generic 安装包继续使用 AIM 原有的 16 MiB 分片、断点续传和 SHA-256 校验链路。
- CSP 按当前微服的 `file.<BoxDomain>` 动态放行网盘 iframe 和官方选择器样式；注入脚本的自定义元素注册已做幂等兼容，支持懒猫运行时重复触发 inject。
- LPK v2 使用经过 AMD64 校验的懒猫官方仓库不可变镜像、`/lzcapp/var` 持久化、局域网访问和文稿读取权限声明。
- 懒猫入口直接终止 HTTPS，不再需要原项目 Compose 中的 Caddy 容器。
- 部署向导可以生成不含明文密码的目标机执行脚本；单机、复制和 MGR 调用 `aim.sh`，可选 Router 作为独立阶段调用 `router.sh`。

## 构建

要求 Docker 与 `lzc-cli >= 2.0`：

```sh
lzc-cli project lint .
lzc-cli project release .
```

生成的 LPK 位于 `dist/`。

## 登录与权限

- 懒猫管理员组 `ADMIN`：AIM 管理员。
- 其他懒猫用户：AIM 只读用户。
- 如需允许普通用户执行部署，可将 `AIM_OIDC_NORMAL_ROLE` 改为 `operator`。
- LPK 默认关闭 AIM 本地密码登录，避免维护第二套凭据。

## 文件上传

进入“安装介质”后点击“选择软件包”，系统会提供“从本地打开 / 从懒猫打开”。网盘文件不会绕过 AIM 校验；浏览器将所选文件按现有 API 分片上传到应用私有持久目录。

## 简化目标机纳管

2.4.5 提供类似 `ssh-copy-id` 的 `aim-copy-id`。它只依赖管理电脑到目标机的 SSH/SCP，不需要公开 bootstrap URL，并会安装 MySQL Router 与失败任务安全续跑所需的受限执行脚本。

懒猫 1.0.31 可以在“主机资源 → 添加受管主机”页面直接下载 Host Kit 2.4.12，也可以访问 `/downloads/aim-host-kit-2.4.12.tar.gz`。该下载仍受懒猫应用登录保护，压缩包内不包含用户私钥或服务器凭据。升级后应在每台目标机幂等重装一次 Host Kit，使远端 `/opt/aim/router.sh` 获得 Router 下载作用域修复；该升级不会删除 MySQL 数据。

```sh
./scripts/aim-copy-id root@192.168.1.100
```

工具会自动生成 AIM 专用密钥、探测目标架构并复制初始化文件，随后打印目标机唯一需要执行的 `sudo install-staged-target.sh` 命令。也可使用 `--install` 直接完成远端安装：

```sh
./scripts/aim-copy-id --install root@192.168.1.100
```

完成后在 AIM“主机资源”页面导入工具生成的私钥文件并保存主机。

## 生成目标机执行脚本

在“部署向导”配置完拓扑后，可以先点击“生成执行脚本”，预览、复制或下载 `.sh`，不必创建 Web 部署任务。导出的脚本依赖 Host Kit 2.4.12 已安装的 `/opt/aim/aim.sh` 和 `/opt/aim/router.sh`，不包含数据库密码；密码只在目标机运行时静默输入。

在线备份、监控历史、懒猫网盘归档、OIDC 权限和审计仍由 Web 控制台与受限 `aim-executor` 提供，不属于 `aim.sh` 单文件能力。
