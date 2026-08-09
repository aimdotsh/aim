# AIM Host Kit 2.4.13（部署、失败清理、断点恢复、备份、监控与 Router）

控制台会通过 SSH 连接目标机的 `aimops` 用户。Host Kit 2.4.13 安装 MySQL 生命周期脚本、MySQL Router 脚本、受限执行器，以及失败安装清理、安全续跑、在线备份和健康监控所需的协议动作。

```sh
# 解压 host kit 后，在管理电脑执行；--install 会自动在目标机执行一次 sudo 安装
./aim-copy-id --install root@192.168.1.100
```

工具会根据 `uname -m` 选择 amd64/arm64 执行器，生成或复用专用密钥，将公钥、`aim.sh`、引导脚本和执行器复制到目标机，然后创建：

- `aimops` 专用 SSH 账号和受限 `authorized_keys`；
- root-owned `/usr/local/sbin/aim-executor`、`/opt/aim/aim.sh` 和 `/opt/aim/router.sh`；
- `/var/lib/aim-staging` 以及每个任务独立的暂存目录；
- 仅允许 `aimops` 无密码运行指定执行器的 sudo 规则。

重新执行新版 Host Kit 是幂等的，不会删除 MySQL 数据。升级到控制台 1.0.34 及以上版本后，应在每台目标机重新执行一次 2.4.13。该版本会修正已解压的 Router 目录权限，避免 `mysqlrouter` 服务用户触发 `203/EXEC Permission denied`，并在 Router 端口未就绪时输出 systemd 与 journal 诊断。

对于内存不超过 3 GiB 的测试主机，新建实例默认使用 `innodb_buffer_pool_size = 128M`。如果首次启动因主机负载过高而超时，但 MySQL 随后已经正常监听，控制台会核验 AIM 配置、版本、server_id、MGR 拓扑和保存的 root 凭据，全部一致后才继续部署；不会接管不属于 AIM 的实例，也不会覆盖用户已经修改的 Buffer Pool。

网页“主机资源”中填写：

- SSH 用户：`aimops`；
- 私钥：管理电脑上的 `~/.ssh/aim/aim_console_ed25519` 全文（不要选择同目录的 `.pub` 文件）；
- 首次使用先核对并固定页面显示的 SSH SHA-256 指纹。

备份输出文件归属 `aimops:aimops` 且为 `0600`，控制台下载并校验后会通过 SFTP 删除目标机暂存文件。MySQL root 密码不会写入命令行，也不会打印到任务日志。

启用 Router 时，目标机会从 Oracle 官方 CDN 下载 MySQL Shell/Router 8.0.46，并按照 Oracle 发布的校验值验证文件。MGR 会先被 MySQL Shell 以 `adoptFromGR` 接管为 InnoDB Cluster，然后分别创建 Classic 读写、Classic 只读、X 读写和 X 只读四个连续端口。
