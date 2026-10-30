# 第一阶段验证记录

> 本文保留第一阶段结果。后续已增加并验证 WSD 自动发现，见 [DISCOVERY-VERIFICATION.md](DISCOVERY-VERIFICATION.md)。当前 GoCV/CGO 预处理的离线验证见 [PREPROCESSING-VERIFICATION.md](PREPROCESSING-VERIFICATION.md)。下文“无第三方模块”“CGO_ENABLED=0”“未验证自动发现”等描述仅指第一阶段，不代表当前分支。

日期：2026-10-28。

## 环境

- macOS 27.0.1，arm64。
- Go 1.27.3；最低声明版本 Go 1.22 尚未单独测试。
- HP Laser MFP 1188w，`192.168.1.8:8018/wsd/scan`。
- 无第三方 Go 模块，发布构建使用 `CGO_ENABLED=0`。
- 未安装系统驱动，未修改打印机配置。

## 自动化验证

- `go test -race -count=1 -json -cover ./...`：通过。
- 16 个顶层测试，连同子测试共 58 项通过。
- 语句覆盖率：86.1%；这不表示全部硬件行为已覆盖。
- `go vet ./...`：通过。
- Darwin arm64 构建与运行：通过。
- Darwin amd64 交叉编译：通过；没有 Intel Mac 运行结果。

覆盖范围包括：

- 能力 XML 解析及缺失/无效数据拒绝。
- SOAP 命名空间、故障、DOCTYPE、尾随数据和大小限制。
- 请求 QName 命名空间、HTTP 超时、截断和超限。
- 不使用 HTTP 代理，不跟随重定向。
- 标准 MTOM、部件乱序、base64、HP 缺少 CRLF 的边界格式。
- XOP 引用缺失、外部 URL、重复 Content-ID、截断和无效图像。
- 扫描参数、区域边界、非有限数值、忙碌状态和不支持的分辨率。
- 已存在文件、悬空符号链接和扫描期间被其他进程创建的目标路径。
- 成功保存、创建结果未知、下载失败、任务取消、独立清理 context。
- 原子发布失败时保留完整暂存图像，其他正常失败清理暂存文件。
- 同一端点锁互斥及释放。
- CLI 帮助、错误参数和中断退出码。

默认测试不连接真实打印机。测试锁与临时文件位于测试临时目录。

## 实机验证

### 能力查询

成功返回 Idle、原生格式与色彩、分辨率及平板尺寸。

接口报告：75、100、150、200、300 DPI。本次成功扫描覆盖 150 和 300 DPI，其他分辨率没有逐一实测。

首次 Go 能力查询曾出现 HTTP 400。相同请求后续成功；XML 空白及 Accept-Encoding 对照未复现错误。原因未确定，没有添加自动重试或推测性的兼容修复。

### 彩色整页

```bash
./bin/goscanner scan --dpi 300 --mode color --area a4 \
  -o verification/color-a4-300.jpg --timeout 2m
```

结果：成功。2480 × 3512 像素，RGB JPEG，242,410 字节。

SHA-256：`0302341cd3986b99d8211ed2145d658cacf9ece20f9c8190eec4b3355b82bc09`。

### 灰度与偏移区域

```bash
./bin/goscanner scan --dpi 150 --mode gray --area 10,10,50,50 \
  -o verification/gray-crop-150.jpg --timeout 90s
```

结果：成功。295 × 296 像素，Gray JPEG，2,111 字节。

SHA-256：`b2a882eb26b118513b342a2ec81f42210eca9558dcb8cbbf01574131e7a7c6d8`。

两张文件均通过 Go JPEG 完整解码。另使用 macOS `sips` / ImageIO 完整解码并转为临时 PNG，再验证 PNG 签名。临时 PNG 已清理。文件权限均为 `0600`。

未判断扫描内容、实际偏移位置的视觉精度、文字清晰度或色彩准确性。

### 中断取消

对另一次彩色 A4、300 DPI 扫描，在进程启动约 2 秒后发送 SIGINT。

结果：

- 中断发生在 RetrieveImage 阶段，已获得任务编号。
- 进程退出码 130。
- 独立 context 的 CancelJob 清理流程未报错；该流程要求有效的 CancelJobResponse。
- 最终输出不存在，暂存文件没有残留。
- 随后查询设备恢复 Idle。

当前 Go 版本的取消错误文本为 `interrupt signal received`，不是固定的 `context canceled`。验证不应只依赖一个人类可读错误字符串。

### 禁止覆盖

把成功的彩色图像重新作为输出路径执行扫描命令。程序拒绝，原文件 SHA-256 不变。离线测试另覆盖了扫描期间目标路径被创建的竞争情形。

### 原生黑白组合

曾请求原生 `BlackAndWhite1`、75 DPI、JPEG、50 × 50 mm 区域。创建请求通过，但下载返回 `ClientErrorNoImagesAvailable`；取消流程无报错，没有留下输出文件。

这证明该次组合失败，不证明设备在任何情况下都不支持黑白扫描。为避免暴露未验证功能，最终 CLI 与参数验证仅开放 `color`、`gray`。没有自动降级或创建第二个任务。

## 本地证据与未验证项

`verification/` 保存原始测试输出、构建元数据、能力 JSON、扫描日志、取消结果、图像哈希及成功图像，已从 Git 排除。生成的 JPEG 和暂存文件也被忽略，以免误提交扫描内容。

未验证：所有参数组合、长期稳定性、多客户端跨机器竞争、其他机型/固件、IPv6 实机、Intel Mac 运行、Linux 实机、Windows、GUI、Image Capture、自动发现、TIFF、PNG/PDF、多页、分发签名与公证。
