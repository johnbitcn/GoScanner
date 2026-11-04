# GoScanner

## 简介

**在 macOS 上使用局域网 WSD 扫描仪，无需厂商专用扫描驱动。**

GoScanner 通过 WSD 扫描服务与设备通信，提供交互式终端界面和面向 Agent 的 JSON 模式。它可以读取设备能力、逐页扫描、保存多页 PDF，并在本机完成可选的 AI 纠偏和裁切。

项目不限制设备品牌，但**设备支持 WSD 打印，不代表支持 WSD 扫描**。

<p align="center">
  <img
    src="https://github.com/user-attachments/assets/9178da8a-b5ae-407a-a454-afa0f5b3f633"
    alt="动画演示"
    width="800"
  />
</p>

## 安装与首次扫描

### 最简单的安装方法

需要 **Apple Silicon Mac、macOS 14 或更新版本，以及 Node.js 22 或更新版本**：

```bash
npm install -g @codingbone/goscanner
goscanner
```

## 命令详解

### 查看帮助

```bash
goscanner --help
goscanner scan --help
goscanner info --help
goscanner discover --help
```

顶层帮助只显示导航和常用示例；子命令帮助按用途分组。说明自动换行并对齐，窄终端改为上下排列。帮助不读取配置、不查询设备、不清屏，也不启用分页器。

在交互终端中，普通命令使用内联 TUI，不进入全屏界面。标准输入或输出不是终端，或 `TERM=dumb` 时，使用普通文本模式；**重定向不会自动启用 Agent 模式**。`NO_COLOR=1` 可关闭颜色。

### `goscanner`：交互扫描

无参数运行，在交互终端中相当于打开扫描界面：

```bash
goscanner
```

首页设备栏和设备列表显示“**型号** + 蓝色 IP”。没有发现元数据时显示“未知型号”，后续发现会补齐。已保存设备不代表当前就绪，选用后仍会重新验证。

| 按键   | 操作                                         |
| ------ | -------------------------------------------- |
| Enter  | 就绪时开始扫描；换页等待时继续下一页         |
| `r`    | 重新检查设备状态和参数                       |
| `s`    | 修改本次扫描设置                             |
| `d`    | 更换设备，选用已保存设备、手动添加或开启发现 |
| Esc    | 返回、结束换页或退出，按当前界面提示操作     |
| Ctrl+C | 取消当前操作，并等待任务清理                 |

设置页一次列出全部选项。用 ↑/↓ 选择，Enter 展开可选值，Space 切换开关。分辨率、颜色和格式按设备能力提供选项；仅输出路径和自定义区域需要输入文字。选择“应用设置”后返回扫描页并重新检查，Esc 放弃修改。设置仅影响本次运行；确认新设备会保存默认地址。

### `discover`：发现设备

```bash
goscanner discover
goscanner discover --timeout 5s
```

发现局域网中的 WSD 扫描设备，显示型号、名称和完整扫描端点等信息。默认超时为 **10 秒**；不会选择设备、修改默认设备或启动扫描。

普通模式会将发现信息记录到 `drives.json`。交互终端显示结果界面；普通文本模式向 stdout 输出 JSON 数组，警告写入 stderr。需要稳定的机器接口时，请使用 `--agent discover`。

找不到设备时，先检查设备是否唤醒、Wi-Fi 客户端隔离、VPN、防火墙和 macOS 本地网络权限。已知地址时可用 `info` 直接查询，但不能绕过网络或系统权限限制。

### `info`：读取能力与状态

```bash
goscanner info
goscanner info --host 192.168.1.8
goscanner info --host 192.168.1.8 --port 8018 --path /wsd/scan
```

只读查询设备状态、支持的格式、颜色、DPI 和扫描尺寸，不创建扫描任务。默认超时为 **2 分钟**。普通模式将成功查询结果记录到设备信息表；交互选择新设备并验证成功后，才保存默认设备。

`info` 和 `scan` 共用以下地址参数：

| 参数                 | 说明                                                        |
| -------------------- | ----------------------------------------------------------- |
| `--host IP`          | 扫描仪 IP，不接受 URL、主机名或凭据                         |
| `--port PORT`        | 扫描服务端口，默认 `8018`                                   |
| `--path PATH`        | 扫描服务路径，默认 `/wsd/scan`                              |
| `--endpoint URL`     | 兼容入口：完整 HTTP/IP 扫描端点，不能与上述三个参数同时使用 |
| `--timeout DURATION` | 本次命令超时，如 `10s`、`2m`                                |

未指定地址时使用配置中的默认设备。交互界面在没有默认设备时打开设备列表；自动发现仍须主动开启。设备连接失败时，不会自动切换到其他设备。

### `scan`：扫描并保存

```bash
# 连续扫描，结束后合并为 PDF。
goscanner scan -o 文档.pdf

# 只扫描一页，保存为 PNG。
goscanner scan --manual=false -o 单页.png

# 按设备能力选择灰度、DPI 和完整平板区域。
goscanner scan --dpi 300 --mode gray --area full -o 灰度.tiff

# 连续扫描，每页独立保存 PDF。
goscanner scan --single-page -o 分页.pdf
```

| 参数                 | 说明与内置默认值                                                                      |
| -------------------- | ------------------------------------------------------------------------------------- |
| `--dpi DPI`          | 分辨率，默认 `150`；必须为设备支持的值                                                |
| `--mode MODE`        | `color`（彩色）或 `gray`（灰度），默认 `color`                                        |
| `--area AREA`        | `a4`、`full` 或 `x,y,width,height`，默认 `a4`；自定义区域单位为毫米，原点是平板左上角 |
| `-o FILE`            | 输出路径；支持 `.jpg`、`.jpeg`、`.tif`、`.tiff`、`.png`、`.pdf`                       |
| `--manual[=BOOL]`    | 连续换页；TUI 默认开启，普通文本模式默认关闭。用 `--manual=false` 明确关闭            |
| `--single-page`      | 仅适用于 PDF，每页独立保存；默认关闭                                                  |
| `--deskew`           | AI 纠偏，默认关闭，启用后额外保留设备原图                                             |
| `--autocrop`         | AI 裁切，默认关闭，启用后额外保留设备原图                                             |
| `--timeout DURATION` | 每页扫描超时，默认 `2m0s`；不含设备选择或换页等待                                     |

TUI 扫描前会检查 `Idle` 状态及当前参数，通过后仍须按 Enter 才开始。就绪状态带有检查时间，不是设备预约；真正扫描前会再次读取能力并校验。有效但忙碌的设备可保存地址，但不能开始扫描。程序不会自动重试扫描任务。

普通文本模式需要 `--manual` 才会等待换页，按任意键继续、Esc 结束；此模式需要交互终端。它不会因输出进入管道而变成 Agent 模式。

#### 默认文件名

未指定 `-o` 时，默认在运行命令的当前目录保存，文件名主体为 **`scan-YYYYMMDD-HHMMSS`**，精确到秒。同一次连续扫描共用文件名主体：

| 输出方式               | 文件名示例                                  |
| ---------------------- | ------------------------------------------- |
| 单页图片               | `scan-20261104-143025.jpg`                  |
| 连续图片               | `scan-20261104-143025-001.jpg`、`…-002.jpg` |
| 合并 PDF               | `scan-20261104-143025.pdf`                  |
| 连续扫描，每页独立 PDF | `scan-20261104-143025-001.pdf`、`…-002.pdf` |

指定 `-o 文档.pdf` 时使用自定义主体；连续独立页为 `文档-001.pdf`、`文档-002.pdf`。AI 原图使用 `-original` 标记，连续扫描原图例如 `文档-original-001.jpg`；PNG 输出对应的原图为 TIFF，PDF 对应的原图为 JPEG。

目录和格式可由配置修改。同秒重复运行且输出路径相同时可能重名；程序会报错，不覆盖已有文件，也不会自动跳过已占用页码。

### `--agent`：供脚本与 Agent 调用

**将 `--agent` 放在子命令前**：

```bash
goscanner --agent discover
goscanner --agent info --host 192.168.1.8
goscanner --agent scan --host 192.168.1.8 -o 单页.pdf
goscanner --agent scan --help
```

- 不读键盘、不启动交互界面，不修改配置或设备信息表。
- `info`、`scan` 不自动发现或选择设备。没有明确地址，也没有默认设备时，返回 `device_required`。
- 不支持交互式连续换页；每次 `scan` 执行一次扫描。
- stdout 只返回一个紧凑 JSON 对象，不混入进度日志或颜色。
- `status` 为 `ok`、`error` 或 `partial`。失败和部分完成返回非零退出码；根据 `files`、`originals`、`retained` 等字段核对实际保留的文件。

成功结果示例（节选）：

```json
{ "status": "ok", "files": ["/absolute/path/document.pdf"], "pages": 1 }
```

字段、错误码、退出与部分完成约定见[界面与 Agent 输出](docs/interfaces.md)。

### 文件安全与故障处理

文件先在输出目录暂存，完整校验后再发布，不将未完成图像当作最终文件。输出文件系统必须支持硬链接。图像、PDF 和原图权限为 `0600`，新建输出目录与图像暂存目录权限为 `0700`。

| 情况                     | 处理方式                                                                         |
| ------------------------ | -------------------------------------------------------------------------------- |
| 参数或格式不受支持       | 运行 `info` 核对能力，调整 DPI、区域、颜色或源格式；能力列表不保证任意组合均可用 |
| 扫描超时                 | 查看设备状态，必要时调整 `--timeout`，不要反复启动任务                           |
| 图像发布或 PDF 保存失败  | 按错误信息查找保留的暂存图像、`.part` 或 PDF 暂存目录，先备份再处理              |
| 纠偏不正确或裁切丢失内容 | 核对额外原图，使用 `--deskew=false --autocrop=false` 关闭处理                    |
| 更换设备                 | 在 TUI 按 `d`；取消或验证失败不会替换原设备                                      |

Ctrl+C 或 SIGTERM 会尝试取消本客户端已知编号的任务；若创建任务时连接中断且没有获得编号，程序无法可靠取消未知任务。合并 PDF 会在正常结束、中断或后续页失败时尝试保存已完成页面；强制终止或断电不保证执行保存。清理最多可能额外等待 10 秒，文件系统和本地图像处理不是硬实时操作。

扫描图像和 AI 推理均在本机处理，不上传到云端推理服务。WSD 使用明文 HTTP，仅应在可信局域网使用，不要向互联网开放扫描端口。iCloud、Google Drive 等同步目录仍可能上传输出文件，敏感文档应保存到非同步目录。

遇到设备差异时，可通过 [GitHub Issues](https://github.com/johnbitcn/GoScanner/issues) 提供设备型号、系统与包版本、执行命令、错误信息及脱敏后的 `discover`／`info` 输出。不要提交任务令牌、敏感文档或未脱敏的网络信息。

## 配置文件

### 配置位置与覆盖顺序

| 文件                              | 用途                                               |
| --------------------------------- | -------------------------------------------------- |
| `~/.config/goscanner/config.json` | 默认设备、扫描参数、输出目录与格式、AI 开关        |
| `~/.config/goscanner/drives.json` | 发现信息及成功验证的能力、状态快照，不代表当前就绪 |

配置优先级为 **显式命令行参数 > 配置文件 > 内置默认值**。没有配置文件时使用内置值。TUI 确认新设备后只更新 `config.json` 的 `device`，不会把设置页中的本次参数全部写回配置。`drives.json` 在首次记录时创建；Agent 模式不写这两个文件。

### 配置示例

可以参考 [`config.example.json`](config.example.json) 手动创建配置；示例文件不会自动加载：

```json
{
  "device": {
    "host": "192.168.1.8",
    "port": 8018,
    "path": "/wsd/scan"
  },
  "scan": {
    "dpi": 150,
    "mode": "color",
    "area": "a4",
    "timeout": "2m0s"
  },
  "output": {
    "directory": "~/Documents/Scans",
    "format": "pdf"
  },
  "processing": {
    "deskew": false,
    "autocrop": false
  }
}
```

### 配置项说明

| 配置项                | 内置默认值    | 说明                                                   |
| --------------------- | ------------- | ------------------------------------------------------ |
| `device.host`         | `""`          | 扫描仪 IP；空值在 TUI 中打开设备列表，不主动搜索网络   |
| `device.port`         | `8018`        | 扫描服务端口                                           |
| `device.path`         | `"/wsd/scan"` | 扫描服务路径                                           |
| `scan.dpi`            | `150`         | 默认分辨率                                             |
| `scan.mode`           | `"color"`     | `color` 或 `gray`                                      |
| `scan.area`           | `"a4"`        | `a4`、`full` 或毫米区域字符串，如 `10,10,50,50`        |
| `scan.timeout`        | `"2m0s"`      | 仅影响 `scan`；`discover`、`info` 用各自的 `--timeout` |
| `output.directory`    | `"."`         | 输出目录，不存在时尝试创建                             |
| `output.format`       | `"jpg"`       | `jpg`、`tiff`、`png` 或 `pdf`                          |
| `processing.deskew`   | `false`       | 默认是否启用纠偏                                       |
| `processing.autocrop` | `false`       | 默认是否启用裁切                                       |

- 相对目录按**运行命令时的工作目录**解析，不相对于配置文件或可执行文件。支持 `~`、`~/...`，不展开 `~用户名`、`$HOME` 或 `${HOME}`；目录不能为空。
- `-o` 指定完整输出路径，并以扩展名覆盖配置中的输出目录和格式。例如 `goscanner scan -o ~/Desktop/once.jpg`。
- `--manual`、`--single-page`、`--endpoint` 不是配置项。`--deskew=false`、`--autocrop=false` 可临时关闭配置中启用的 AI 处理。
- JSON 不支持注释、尾随逗号或未知字段。损坏或为符号链接的配置、设备表不会被自动覆盖，请先修复。

[配置与首次使用](docs/configuration.md)详细说明了首次设备登记、手动地址与发现地址的区别、`drives.json` 字段、旧 `device.endpoint` 迁移、路径解析、超时和高级环境变量。普通 npm 用户通常无需设置 `GOSCANNER_MODEL_PATH` 或 `GOSCANNER_ONNXRUNTIME_PATH`；源码与 AI 运行环境见[构建文档](docs/build.md)。

## 开源协议

本项目原创代码采用 [Apache License 2.0](LICENSE)。第三方库和 AI 模型保留各自许可证；npm 包中的许可证与声明位于 `runtime/licenses/`，来源与分发说明见 [npm 文档](docs/npm.md#固定构建输入与许可)。
