# GoScanner

面向 HP Laser MFP 1188w 的 macOS WSD 命令行扫描工具。使用 Go 标准库，不需要 Python、HP 驱动、Avahi 或 SANE。

已实现 WSD 自动发现、能力查询、IP/端点地址配置、分辨率/色彩/区域选择、单页 JPEG、超时与取消、安全保存。当前仍只支持 WSD 扫描，不支持 eSCL-only 设备。

## 快速开始

在 `GoScanner/goscanner/` 仓库中执行：

```bash
# 构建。本机开发使用 Go 1.27.3，go.mod 声明最低 Go 1.22。
CGO_ENABLED=0 go build -trimpath -o bin/goscanner .

# 发现本机直连 IPv4 局域网中的 WSD 扫描仪，不扫描；输出 JSON。
./bin/goscanner discover

# 自动发现并查询唯一扫描仪的能力与状态，不扫描。
./bin/goscanner info

# 默认：自动选择唯一扫描仪，A4、300 DPI、彩色。运行后开始一次扫描。
./bin/goscanner scan

# 多设备时，显式复制 discover 返回的 endpoint，不猜测设备端口或路径。
./bin/goscanner info --endpoint http://192.168.1.8:8018/wsd/scan

# 兼容原用法：显式 IP，使用 HP 的固定端口与路径，不进行自动发现。
./bin/goscanner info --host 192.168.1.8

# 灰度，自定义区域：从左上角向右 10 mm、向下 10 mm，取 50 × 50 mm。
./bin/goscanner scan --dpi 150 --mode gray --area 10,10,50,50 -o scans/crop.jpg

# 保存到其他目录；已有文件不会被覆盖。
./bin/goscanner scan -o ~/Desktop/scan.jpg --timeout 2m

./bin/goscanner --help
```

**行为变更：不再默认连接 `192.168.1.8`。** 未指定 `--host` 或 `--endpoint` 时先自动发现；只在发现一项扫描端点且没有发现警告时使用它。多项、零项或发现不完整时停止，不会退回旧 IP，也不会选择列表第一项。

显式 `--host IP` 保留旧用法：使用 `http://IP:8018/wsd/scan`。显式 `--endpoint URL` 支持发现得到的其他端口和路径。两者互斥，均跳过发现。地址必须使用 IP，不接受用户名、查询参数、片段或 DNS 主机名。

默认文件保存到**当前工作目录**的 `scans/scan-时间戳.jpg`，不是二进制所在目录。

本机已生成：

- `bin/goscanner`：Darwin arm64，已在本机运行与扫描。
- `bin/goscanner-darwin-amd64`：Darwin amd64，已交叉编译，尚未在 Intel Mac 上运行验证。

这些构建不需要第三方运行库。尚未做 Developer ID 分发签名或公证。

## 参数

所有参数放在 `discover`、`info` 或 `scan` 子命令之后。

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--host` | 无，自动发现 | info/scan；显式 IP，固定端口 8018 和路径 `/wsd/scan` |
| `--endpoint` | 无，自动发现 | info/scan；完整 HTTP 扫描地址，与 --host 互斥 |
| `--timeout` | discover 为 `10s`；其他为 `2m` | 整体任务的 context 时限，包含发现；失败取消清理最多额外等待 10 秒 |
| `--dpi` | `300` | 仅 scan；必须同时被设备的横向和纵向能力列表支持 |
| `--mode` | `color` | 仅 scan；`color` 或 `gray` |
| `--area` | `a4` | 仅 scan；`a4` 或 `x,y,width,height`，单位毫米 |
| `-o` | 自动生成 | 仅 scan；新 `.jpg` 或 `.jpeg` 文件路径 |

区域坐标原点为平板左上角，毫米值会转换为 WSD 的 1/1000 英寸单位。宽高必须大于零，区域必须落在设备声明的范围内。`info` 的 `min_size` / `max_size` 使用 `size_unit` 指定的原始单位。

能力查询中的 `formats`、`colors` 是**设备原始声明**，不表示客户端已开放全部组合：

- `RGB24` 对应 `color`，`Grayscale8` 对应 `gray`。
- 设备声明 `BlackAndWhite1`，但本次原生黑白 + JPEG 实测返回 `ClientErrorNoImagesAvailable`。因此没有开放 `--mode bw`，也不会悄悄降级为灰度或再创建一个任务。
- TIFF、PNG、PDF、多页与图形界面不属于本阶段。

## 局域网自动发现

`discover` 使用 WS-Discovery 2005，通过活动、支持多播、非环回、非点对点接口上的 IPv4 地址，向 `239.255.255.250:3702` 发送通用与扫描器专用 Probe。通用和扫描器专用 Probe 间隔约 0.5 秒，各最多发送 3 次，监听窗口为 4 秒，然后只读获取设备元数据。该有限重发只用于只读发现，不用于扫描任务。每个元数据请求最多 2 秒，同时最多 4 个请求。不是遍历网段或扫描端口。

- 只处理与当前 Probe 的 MessageID 关联的回复。
- 按实际 QName 命名空间识别 `ScanDeviceType`，再从元数据验证 `ScannerServiceType`，不会把只有打印服务的设备当成扫描仪。
- 读取扫描服务真实地址，不假定端口 8018。结果按扫描端点去重和排序，同一设备若确有多个不同端点，可能显示多项；此时要求显式选择。
- 元数据地址与扫描地址都必须是响应源 IP 上的无凭据 HTTP 地址。不会访问设备顺带发布的其他 IP、外部 URL 或 DNS 名称。
- 有接口或元数据失败时，`discover` 在 stderr 输出警告，stdout 仍输出成功发现的 JSON 数组。自动 info/scan 遇到这些警告会拒绝自动选择，避免把不完整结果当成完整设备列表。
- 发现成功仅表示设备发布了 WSD 扫描服务；`info`/`scan` 仍要验证本工具所需的平板/JPEG 能力。
- 不支持 IPv6 多播发现、WS-Discovery 2009、eSCL-only、HTTPS-only、仅发布主机名的发现地址、仅提供 URN/引用参数而无绝对 HTTP 扫描端点的服务、跨子网发现或仅靠 USB 的设备。
- 设备休眠、Wi-Fi 客户端隔离、VPN、防火墙或系统局域网权限可能影响发现。可先唤醒设备，或用显式 `--host` / `--endpoint`。没有后台常驻服务或发现缓存。

本机已在 `en0` 发现 HP 1188w，并通过不带地址参数的 `info` 完成自动选择和能力读取。初版复核中曾有一次间歇性漏发现；加入有限重发后，连续五组 `discover` + 自动 `info` 均成功，但不能保证所有网络环境都不漏设备。本轮发现验证不创建扫描任务。发现验证记录见 [DISCOVERY-VERIFICATION.md](DISCOVERY-VERIFICATION.md)。

## 已完成的实机验证

打印机：HP Laser MFP 1188w，WSD 扫描地址 `http://192.168.1.8:8018/wsd/scan`。

| 项目 | 结果 |
| --- | --- |
| 能力与状态查询 | 成功，返回设备分辨率、原生格式、色彩、平板范围 |
| 彩色 A4、300 DPI | 成功；2480 × 3512 像素，RGB JPEG |
| 灰度 150 DPI、偏移区域 50 × 50 mm | 成功；295 × 296 像素，Gray JPEG |
| 下载阶段按 Ctrl+C | 退出码 130，取消流程无报错，未留下最终图像或暂存图像，设备恢复 Idle |
| 指定已存在的成功图像作为输出 | 拒绝，原文件哈希未变，没有创建新扫描任务 |
| 原生黑白 75 DPI + JPEG | 失败；已取消任务，本阶段不开放该模式 |

请求尺寸与返回像素可能因固件对齐而略有差异。程序保存设备返回的图像，不擅自裁切、缩放。程序会使用 Go JPEG 解码器完整验证图像；独立的 macOS ImageIO 解码检查也已通过。没有判断纸张内容、文字清晰度或色彩准确性。

实测期间首次 Go 能力查询曾返回 HTTP 400。随后原请求成功，多种请求格式对照没有复现该错误，因此未给它编造原因或添加未经证实的修复。程序不会自动重试创建任务。

详细结果见 [VERIFICATION.md](VERIFICATION.md)。原始日志和扫描图像在本地 `verification/` 中，该目录已被 Git 忽略。

## 取消、超时和文件安全

- Ctrl+C 或 SIGTERM 会取消当前操作。已获得任务编号时，使用独立的、最多 10 秒的 context 取消**本客户端的任务**。
- 若创建请求断线、任务编号尚未返回，不能可靠取消未知任务。先查询状态，不要立即反复扫描。
- 不自动重试，避免重复创建任务。
- 同一用户下，本机多个进程通过端点锁避免同时扫描同一地址。锁位于 macOS 用户缓存目录 `~/Library/Caches/goscanner/`，不包含图像或任务令牌。锁只约束本工具，不能阻止 Windows 或其他客户端同时扫描。
- 图像先写入输出目录中的私有 `.goscanner-*.jpg.part` 文件，完整解码、写盘并关闭后，通过硬链接原子发布最终路径。创建目标路径时不会覆盖已有文件，即使该文件是扫描期间才被其他进程创建的。
- 发生正常错误或中断时清理未完成暂存文件；强制杀死进程或断电可能留下 `.part`，但不会发布半张 JPEG。
- 若最终文件发布失败，已经完成的图像会保留在错误信息指出的暂存路径，便于恢复。输出文件系统须支持硬链接；当前项目所在文件系统已验证支持。
- 下载响应最多 64 MiB，SOAP XML 最多 1 MiB，JPEG 最多 4000 万像素。超时不是对本地解码和文件系统调用的硬实时抢占保证。

## 固件兼容性与网络边界

这台 HP 固件会在 MIME 分隔符前漏写 CRLF。程序只对已观察到的 `_DPWS_v1.0_MimeBoundary_WSD` 边界做修复，然后用标准库解析 MIME，按 `start` 和 XOP `cid:` 找到图像，不假定附件顺序。测试覆盖修复前后图像字节一致及重复修复不改变数据。

HTTP 客户端不使用代理环境变量，不跟随重定向，不访问 XOP 中的外部 URL。任务令牌仅在内存中用于下载，不写入日志。

WSD 使用明文 HTTP，只在可信局域网运行，不要把端口暴露到互联网。当前项目位于 Google Drive 同步路径内，扫描文件可能自动上传；敏感文档请用 `-o` 指定非同步目录。

## 开发验证

```bash
go test ./...
go test -race -cover ./...
go vet ./...

CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -o bin/goscanner-darwin-amd64 .
```

默认测试只使用环回地址上的模拟 UDP/HTTP 服务和能力样本，不向局域网发送多播、不访问真实打印机、不启动扫描。`-race` 属于开发验证，需相应编译工具；发布的 `CGO_ENABLED=0` 二进制不需要这些工具。

当前只有标准库依赖。锁实现针对 Darwin/Linux；Linux 未做实机扫描验证，Windows 构建未实现。最低 Go 1.22 版本尚未单独测试。

源码按功能放在同一 `main` 包中，没有额外的服务层、插件框架或配置文件。`testdata/scanner-elements.xml` 来自此前成功的只读能力查询，不含扫描任务令牌。
