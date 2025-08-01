OCI-Help - 甲骨文云管理助手
oci-help 是一个功能强大的命令行工具和 Telegram 机器人，旨在简化对 Oracle Cloud Infrastructure (OCI) 资源的管理。它提供了一个交互式界面，用于执行各种任务，包括实例管理、账单查询、用户管理等，并支持通过 Telegram 机器人进行远程操作。

🌟 功能特性
多账户管理: 在单个配置文件中轻松管理多个 OCI 账户。

实例管理:

基于预定义模板批量创建实例 (支持 ARM 和 AMD)。

查看、启动、停止、重启和终止实例。

动态调整实例配置（CPU、内存）。

更换实例的公共 IP 地址。

查看实例流量使用情况。

管理员管理:

查看、创建、更新和删除 OCI 账户中的管理员用户。

快速将新用户添加到管理员组以授予权限。

Telegram Bot 集成:

通过 Telegram 机器人远程管理 OCI 资源。

创建和监控“抢占式”实例创建任务。

交互式按钮菜单，方便操作。

租户管理:

一键检查所有已配置账户凭证的有效性。

查看租户和用户信息。

账单与用量:

查询指定时间范围内的成本和用量数据。

查看账户订阅详情。

网络与安全:

查看 VCN、子网和安全列表（防火墙规则）。

自动创建和配置网络基础设施（VCN、子网、互联网网关）。

跨平台构建: 使用 Makefile 轻松为多个平台（Linux, Windows, macOS）构建可执行文件。

⚙️ 配置文件 (oci-help.ini)
在使用前，您需要创建一个 oci-help.ini 文件来配置您的 OCI 账户和实例模板。

# 配置 socks5 或 http 代理 (可选)
# proxy=socks5://127.0.0.1:7890

# Telegram Bot 消息提醒 (可选)
token=YOUR_TELEGRAM_BOT_TOKEN
chat_id=YOUR_TELEGRAM_CHAT_ID

############################## 甲骨文账号配置 ##############################
# 可以配置多个账号，每个账号使用一个独立的区块
[账户别名1]
user=ocid1.user.oc1..your_user_ocid
fingerprint=your_api_key_fingerprint
tenancy=ocid1.tenancy.oc1..your_tenancy_ocid
region=us-ashburn-1
key_file=path/to/your/private_key.pem
# key_password=your_key_password (如果密钥有密码)

[账户别名2]
user=...
fingerprint=...
tenancy=...
region=ap-singapore-1
key_file=...

############################## 实例相关参数配置 ##############################
[INSTANCE]
# 通用实例配置
OperatingSystem=Canonical Ubuntu
OperatingSystemVersion=20.04
retry=3
minTime=5
maxTime=30
# cloud-init=... (Base64编码的初始化脚本)

[INSTANCE.ARM]
# ARM 实例模板
shape=VM.Standard.A1.Flex
cpus=4
memoryInGBs=24
bootVolumeSizeInGBs=50
sum=1
retry=-1 # -1 表示无限重试
availabilityDomain= # 可选，留空则随机选择
ssh_authorized_key=ssh-rsa ...

[INSTANCE.AMD]
# AMD 实例模板
shape=VM.Standard.E2.1.Micro
bootVolumeSizeInGBs=50
sum=1
retry=-1
availabilityDomain=
ssh_authorized_key=ssh-rsa ...

🚀 使用方法
命令行模式
直接运行程序以启动交互式命令行界面。

./oci-help -c oci-help.ini

程序启动后，您可以：

选择账户: 如果配置了多个 OCI 账户，程序会提示您选择一个进行操作。

选择操作: 进入主菜单后，根据提示输入数字序号来执行不同的管理任务，如查看实例、创建实例、管理用户等。

返回/退出: 在任何菜单中，输入 q 或直接按回车键可以返回上一级菜单或退出程序。

Telegram Bot 模式
要以 Telegram 机器人模式启动，请使用 -bot 标志。

./oci-help -c oci-help.ini -bot

启动后，机器人将开始监听消息。您可以向您的机器人发送以下命令：

/start 或 /menu: 显示主操作菜单。

/list_tasks: 查看当前正在运行的“抢机”任务及其状态。

通过交互式按钮，您可以选择租户、查看实例、创建实例、检查账单等。

🛠️ 构建
本项目包含一个 Makefile，可以方便地进行构建和打包。

构建当前平台的版本:

make build

构建所有主要平台的版本 (Linux, Windows, macOS):

make all-arch

构建并打包发布版本:
此命令会为不同平台构建程序，并将其与 oci-help.ini 文件一起打包成 .zip 压缩包。

make release

清理构建产物:

make clean

📦 依赖
本项目依赖以下主要的 Go 模块：

github.com/oracle/oci-go-sdk/v65: Oracle Cloud Infrastructure官方Go SDK。

gopkg.in/ini.v1: 用于解析 .ini 配置文件。

github.com/google/uuid: 用于生成唯一的任务 ID。

所有依赖项都已在 go.mod 文件中定义。
