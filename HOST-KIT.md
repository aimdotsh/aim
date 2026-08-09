# AIM Host Kit

在管理电脑上解压工具包，然后执行：

```sh
./aim-copy-id root@192.168.1.100
```

首次运行会在 `~/.ssh/aim/aim_console_ed25519` 生成 AIM 专用密钥。工具通过 SSH 检测目标机架构，并复制对应执行器、公钥、MySQL 生命周期脚本、Router 脚本和受限安装脚本。

复制完成后，工具会输出一条需要在目标机执行的命令，例如：

```sh
sudo /tmp/aim-bootstrap-20260715120000-12345/install-staged-target.sh
```

如果当前 SSH 用户具备 sudo 权限，也可一步完成：

```sh
./aim-copy-id --install ops@192.168.1.100
```

常用参数：

```text
--key PATH    指定 AIM 私钥路径
--port PORT   指定目标 SSH 端口
--install     复制后立即通过 SSH 执行 sudo 安装
```

安装成功后，在 AIM 页面填写主机信息，并导入 `~/.ssh/aim/aim_console_ed25519`。不要导入 `.pub` 文件。

Host Kit 2.4.12 是控制台 1.0.31 的配套版本。它修复 MySQL Router 官方安装包下载函数在 Bash `nounset` 模式下错误引用未绑定局部变量的问题，并保留失败安装受限清理、OpenCloudOS、低内存、Ubuntu 24.04、systemd 诊断和 MySQL Router 支持；重复安装只会更新受限工具，不会删除现有 MySQL 数据。
