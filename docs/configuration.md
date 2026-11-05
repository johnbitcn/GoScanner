# 配置

## 1. 位置与示例

可选配置文件位于 `~/.config/goscanner/config.json`。macOS 和 Linux 使用同一路径，不读取 `XDG_CONFIG_HOME`。配置可以省略；首次运行 `goscanner info`，在 TUI 选择设备或只输入 IP 添加，程序验证成功后保存默认设备。

需要手动配置时，可参考 [config.example.json](../config.example.json)。不要覆盖已有文件；只修改需要的字段。完整初始示例：

```json
{
  "language": "auto",
  "device": {},
  "scan": {
    "dpi": 150,
    "mode": "color",
    "area": "a4",
    "timeout": "2m0s"
  },
  "output": {
    "directory": ".",
    "format": "jpg"
  },
  "processing": {
    "deskew": false,
    "autocrop": false
  }
}
```

省略的设置使用内置默认值。必须使用 JSON，不支持注释、尾随逗号或未知字段。

## 2. 配置项

| 字段 | 默认值 | 用途与限制 |
| --- | --- | --- |
| `language` | `"auto"` | 人类界面语言：`auto`、`en` 或 `zh-CN`；Agent 的项目说明始终为英文 |
| `device.record_id` | 无 | 引用设备表中的稳定记录。由程序登记，不需要手工生成；省略或 `device: {}` 表示没有默认设备 |
| `scan.dpi` | `150` | 正整数，设备必须支持横向和纵向分辨率 |
| `scan.mode` | `"color"` | `color` 或 `gray`，设备必须支持 |
| `scan.area` | `"a4"` | `a4`、`full` 或 `x,y,width,height`，单位为毫米 |
| `scan.manual` | 未配置时 TUI 开启，文本模式关闭 | 连续扫描开关；显式 CLI `--manual` 可覆盖。Agent 拒绝开启状态，需使用 `--manual=false` |
| `scan.single_page` | `false` | PDF 每页独立保存；只适用于 PDF，显式 `--single-page` 可覆盖 |
| `scan.timeout` | `"2m0s"` | 正的 Go 时长，例如 `30s`；只影响 `scan` |
| `output.directory` | `"."` | 非空目录；不存在时尝试创建。相对路径基于命令运行目录；支持 `~` 和 `~/...`，不展开环境变量或 `~用户名` |
| `output.format` | `"jpg"` | `jpg`、`tiff`、`png` 或 `pdf` |
| `processing.deskew` | `false` | 本地 AI 纠偏，只旋转 |
| `processing.autocrop` | `false` | 本地 AI 裁切，只裁切 |

设备表位于同目录的 `drives.json`。它维护设备身份、各协议地址、用户首选协议和历史能力快照。配置只引用默认设备，不重复保存其协议和地址。

选择设备后，`device` 类似如下。ID 仅为示例，必须对应本机已登记的记录：

```json
{
  "device": {
    "record_id": "scanner_4e3bb8e859e34bd292ee7e65dd156413"
  }
}
```

`record_id` 在切换协议和更新地址时保持稳定。确认合并的旧记录 ID 会作为别名保留。记录 ID 不是协议 UUID，也不是硬件认证。设备表通常由程序维护，不建议手动编辑。

## 3. 优先级与临时覆盖

设备选择和协议选择是两个步骤：

- **设备：** 显式 CLI 地址 > 默认设备引用 > 无默认设备。无默认设备时，TUI 打开设备列表；Agent 返回 `device_required`。
- **协议：** 显式 `--protocol` > 设备列表首选协议 > 初始协议选择。首次确认双协议设备时首选 eSCL。
- **扫描设置：** 显式 CLI 参数 > 配置文件 > 内置默认值。
- **人类界面语言：** 显式 `--lang` > 配置 `language` > 系统语言自动检测。

CLI 参数只影响本次运行。TUI 设置页中的选择先保留为草稿，选择“保存并应用”并确认后，才写入配置；Esc 放弃，不写配置。设备列表按 `p` 修改的首选协议也会持久保存。连接失败不自动换设备、换协议或重新提交扫描任务。

`language` 可以设为 `"en"`、`"zh-CN"` 或 `"auto"`。省略字段或设为 `"auto"` 时，按 `LC_ALL` > `LC_MESSAGES` > `LANG` 使用第一个非空环境变量；缺少或不支持时回退英文。显式 `--lang auto` 跳过配置中的固定语言，直接使用此自动检测规则。TUI 可选择自动、英文或简体中文，并在“保存并应用”时写入 `language`。没有显式 `--lang` 时，保存后立即切换界面语言；存在该参数时，保留本次参数指定的语言，新偏好供后续未指定参数的运行使用。`--lang` 本身不写回语言偏好；保存默认设备时保留已有 `language`。Agent 输出始终使用英文。

人类帮助也使用此优先级，只提取语言字段，不应用或校验扫描设置。配置无法读取、JSON 损坏或语言值无效时，帮助回退到系统语言，显式 `--lang` 仍有效。正常执行命令仍严格校验配置；无效的 `language` 返回 `invalid_config`，不会静默执行扫描。Agent 帮助不读取配置。完整规则见[界面与 Agent 输出](interfaces.md)。

例如，临时覆盖 DPI 和输出位置：

```bash
goscanner scan --dpi 300 -o ~/Desktop/once.pdf
```

`-o` 同时覆盖默认输出目录和格式；`--deskew=false`、`--autocrop=false` 可临时关闭处理。布尔参数使用等号。

TUI 中确认设备后，程序先只读验证，再保存设备记录和默认引用。有效但忙碌的设备可以登记；扫描仍需设备空闲且参数有效。普通 `info` 不因此重写默认选择，Agent 不写配置或设备表。

### TUI 设置保存范围

“保存并应用”会保存界面中的全部可见值，包括由本次 CLI 参数带入的扫描参数，而不只保存本次改动的单项：

- 语言：`language`；保存的是菜单偏好，不把显式 `--lang` 自动写回。
- 扫描参数：`scan.dpi`、`scan.mode`、`scan.area`、`scan.manual`、`scan.single_page`。
- 输出：`output.directory`、`output.format`。当前文件路径只用于本次扫描，不保存文件名；后续仍自动生成时间戳文件名。
- 图像处理：`processing.deskew`、`processing.autocrop`。

保存时重新读取配置，合并上述字段，保留设备引用、超时等其他设置。保存失败时保留草稿和原配置，不应用修改，可修复配置后重试。成功后重新检查设备，不自动启动扫描。

保存连续扫描为开启后，普通文本模式也使用此偏好；Agent 会返回 `unsupported_interaction`，除非显式传入 `--manual=false`。未配置 `scan.manual` 时，保留原来的 TUI 开启／文本模式关闭默认行为。

## 4. 错误与安全保存

旧程序可能不识别 `language`、`scan.manual` 或 `scan.single_page`，并因未知字段返回 `invalid_config`。保存新设置后，请使用支持这些字段的程序。若需回退，先备份配置，再移除旧版不支持的字段；不要用旧版示例覆盖整个文件。

- 引用已失效时返回 `device_record_not_found`。TUI 可主动重新选择；不会按 IP、型号或列表顺序改绑。显式 `--host` 或 `--endpoint` 可覆盖一个已失效但格式合法的默认引用。
- 查询发现可比较的设备身份明确冲突时返回 `device_identity_conflict`，不保存默认选择、不创建扫描任务。
- 记录引用不能与旧地址字段混用。高级地址参数须提供明确目标，不能只用端口或路径修改默认引用。
- 损坏的配置和设备表不会被自动修复或覆盖。自动保存拒绝替换符号链接；设备表读取也拒绝符号链接。
- 写入采用同目录临时文件、同步和原子替换，文件权限为 `0600`。默认选择保存失败时不继续扫描。两个文件不是原子事务：可能留下已验证设备记录，但原默认配置保持不变。

旧地址配置仍可读取；兼容字段、迁移和回退方法仅见[旧配置兼容](configuration-compatibility.md)。

相关专题：[界面与 Agent](interfaces.md) · [eSCL 与 WSD](escl.md) · [源码构建与 AI 环境](build.md) · [npm 安装与发布](npm.md)。
