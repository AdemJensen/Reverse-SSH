# Reverse SSH · SSH 反向隧道

Reverse SSH 是 macOS 和 Windows 上的 SSH 反向转发工具，**仅支持 TCP 端口转发**。用一台有公网地址、允许 SSH 远程转发的服务器，即可为本机服务提供访问入口。应用支持多端口、登录后自动启动和连接，以及断线重连。

[项目主页](https://ademjensen.github.io/Reverse-SSH/) · [下载最新版](https://github.com/AdemJensen/Reverse-SSH/releases/latest)

[创建专用 SSH 转发用户](https://ademjensen.github.io/Reverse-SSH/ssh-tunnel-user.html) · [用 Caddy 为转发服务添加 HTTPS](https://ademjensen.github.io/Reverse-SSH/https.html)

![Reverse SSH 的 macOS 主窗口，显示示例配置和转发路径](https://ademjensen.github.io/Reverse-SSH/assets/reverse-ssh-macos.png)

## 安装

- **macOS 13 或更新版本**：解压 macOS 包，把 `Reverse SSH.app` 拖到「应用程序」，再打开。通用版同时包含 Apple 芯片和 Intel 版本。应用使用临时签名，未提交 Apple 公证。
- **Windows 10/11**：下载与电脑架构匹配的压缩包，全部解压后运行 `Reverse SSH.exe`。内嵌窗口使用 Microsoft Edge WebView2 Runtime。WebView2 无法启动时，可通过默认浏览器访问应用界面，托盘和隧道由桌面程序继续管理。此版本未做 Authenticode 签名。

建议先将应用放到长期使用的位置，再打开「开机启动」。如果移动了应用，重新打开并检查开关。

升级前先退出旧版应用，再替换原位置的应用。旧版已保存的单端口配置会出现在新版的转发规则里。

Windows 启动诊断日志位于 `%AppData%\Reverse SSH\startup.log`。配置目录不可写时，程序会尝试将日志写到 `%TEMP%\Reverse SSH\startup.log`。

## 建立隧道

窗口顶栏始终显示连接状态和「连接隧道」按钮。错误和连接事件可在「连接记录」标签页查看。「关于」标签页包含项目说明、官网和 GitHub 仓库入口。

1. 确认本机服务已经运行。以端口 `3000` 的 HTTP 服务为例，将本机服务填为 `127.0.0.1:3000`。
2. 填写 SSH 服务器地址、SSH 端口和用户名，选择认证方式。私钥路径支持 `~/.ssh/id_ed25519` 等简写，也可以从文件选择器浏览。
3. 将远端监听填为 `127.0.0.1:8080`。点击「添加端口」可设置其他端口，例如把远端 `127.0.0.1:8081` 转发到本机 `127.0.0.1:3001`。填写后点击「连接隧道」。首次连接时，从可信渠道核对服务器指纹，再确认并连接。
4. 状态显示已连接后，在 SSH 服务器上访问 `http://127.0.0.1:8080`。到达此端口的连接会转发到本机端口 `3000`。

服务器须允许远程 TCP 转发，且监听端口须可用。如果其他主机也需要访问，远端管理员可按需指定绑定地址，并配置 `GatewayPorts` 与防火墙规则。实际绑定范围由 SSH 服务器决定。

## 导入 SSH 命令

点击「导入 SSH 命令」，粘贴一条 OpenSSH 命令并填入配置。应用会读取服务器、用户名、SSH 端口和每条 TCP `-R` 规则。命令可通过反斜杠换行，`-R` 中明确填写的监听地址会保留，例如 `0.0.0.0:10001:127.0.0.1:8080`。

填入后可以核对和修改，再保存或连接。应用只解析命令文本，连接时使用界面中的认证设置。无法导入的 OpenSSH 选项会显示提示。

## 保存 SSH 密码

密码认证时可勾选「保存密码（加密）」，输入密码后保存配置或连接。应用将密码用 AES-256-GCM 加密后写入本机的 `profile.json`，密钥固定内置于应用。同一服务器、端口和用户名可沿用已保存的密码。

- macOS 位于 `~/Library/Application Support/Reverse SSH/profile.json`。
- Windows 位于 `%AppData%\Reverse SSH\profile.json`。

内置密钥随应用源码公开，拿到 JSON 文件的人可以解密其中的密码。请像保管明文凭据一样保管包含密码的 JSON 文件。取消勾选并保存即可从配置文件移除密码。

## HTTP 测试、连接检测与快速打开

1. 在「连接配置」的端口转发列表中，点击对应本地端口旁的「启动 HTTP 测试」。每个本地地址和端口可单独启动。若端口已被你的服务占用，可直接打开该服务。
2. 点击该规则的「检测连接」。应用向本地和远端的 `/` 发出 HTTP GET 请求，分别显示响应状态码和耗时，或显示请求失败的原因。HTTP 2xx 表示测试通过，跳转显示原始状态码。检测使用该端口当前正在运行的服务。
3. 点击对应端口的「停止 HTTP 测试」即可释放该端口，其他端口可继续测试。测试服务在退出应用时停止，关闭窗口后仍可继续测试。

在「转发路径」中，也可点击本地或远端地址旁的「打开」，用默认浏览器直接查看。远端回环地址（例如 `127.0.0.1`）的检测和浏览器预览会通过当前 SSH 连接访问。这需要 SSH 服务端允许客户端访问对应远端端口。远端监听所有网卡时，检测和浏览器使用 SSH 服务器地址。测试服务遇到本地通配地址时只绑定本机回环。

## 后台运行与开机启动

- 关闭主窗口后，应用会在后台继续运行，已连接的隧道保持打开。在 macOS 菜单栏或 Windows 系统托盘中点击应用图标，可重新打开窗口或退出。退出会断开所有转发。
- 开启「开机启动」后，应用会在用户登录系统时在后台启动，只显示菜单栏或系统托盘图标。手动打开应用时会显示主窗口。应用已在后台运行时，手动打开会唤回同一个窗口。
- 同时启用「登录后自动连接」并保存配置后，应用会在后台连接已保存的端口。需要核对服务器指纹或手动输入凭据时，可打开主窗口查看连接状态。
- 自动连接可使用无口令的私钥、登录时已载入密钥的 OpenSSH Agent，或在本机配置中保存的 SSH 密码。私钥口令需手动输入。

## 导入和导出配置

- 「导出 JSON」将界面上的连接设置和转发规则写入文件。启用密码保存且当前 SSH 服务器、端口和用户名与已保存的密码匹配时，密码密文会一起导出。
- 在另一台电脑使用 1.4.1 或更新的兼容版本，选择「导入 JSON」即可恢复已保存的密码。导入会替换当前配置，导入前先断开隧道。首次连接时请核对服务器指纹。
- 导出的 JSON 包含私钥路径。私钥文件、私钥口令和已信任的服务器指纹需在各自设备上管理。

## 从源码构建

使用 Go 1.27 或更新版本。macOS 构建还需要 Xcode 命令行工具。

```sh
# macOS 通用应用，写入当前目录下的 release/
./scripts/build-macos.sh ./release

# Windows，命令可在 macOS 等支持 Go 交叉编译的环境中执行
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w -H windowsgui' -o 'Reverse SSH.exe' .
# Windows ARM64 原生版使用 GOARCH=arm64

# 测试在本机回环地址上启动临时 SSH 服务
go test -race ./...
```

Windows 构建会自动嵌入源码目录中的图标资源。若修改图标源文件，可安装 `llvm-rc` 和 `llvm-cvtres` 后运行 `python3 scripts/build_windows_icon.py` 重新生成资源。

## 发布新版本

将待发布的提交推送后，创建并推送新的 `v主版本.次版本.修订号` 标签，例如：

```sh
git tag -a v1.5.2 -m "Reverse SSH v1.5.2"
git push origin v1.5.2
```

GitHub Actions 的 [Build and release](https://github.com/AdemJensen/Reverse-SSH/actions/workflows/release.yml) 会构建 macOS 通用版、Windows x64 和 Windows ARM64，并在三个 ZIP 全部通过检查后发布到 Releases。Pages 通过固定文件名下载 GitHub 标记的最新正式版，无需为每个版本更新网页。只想验证打包时，可在 Actions 里手动运行该工作流，ZIP 将作为本次运行的产物提供。
