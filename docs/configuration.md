# 配置

## 1. 位置与示例

可选配置文件位于 `~/.config/goscanner/config.json`。macOS 和 Linux 使用同一路径，不读取 `XDG_CONFIG_HOME`。配置可以省略；首次运行 `goscanner info`，在 TUI 选择设备或只输入 IP 添加，程序验证成功后保存默认设备。

需要手动配置时，可参考 [config.example.json](../config.example.json)。不要覆盖已有文件；只修改需要的字段。完整初始示例：

```json
{
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
| `device.record_id` | 无 | 引用设备表中的稳定记录。由程序登记，不需要手工生成；省略或 `device: {}` 表示没有默认设备 |
| `scan.dpi` | `150` | 正整数，设备必须支持横向和纵向分辨率 |
| `scan.mode` | `"color"` | `color` 或 `gray`，设备必须支持 |
| `scan.area` | `"a4"` | `a4`、`full` 或 `x,y,width,height`，单位为毫米 |
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

CLI 参数和 TUI 设置页只影响本次运行。设备列表按 `p` 修改的首选协议会持久保存。连接失败不自动换设备、换协议或重新提交扫描任务。

例如，临时覆盖 DPI 和输出位置：

```bash
goscanner scan --dpi 300 -o ~/Desktop/once.pdf
```

`-o` 同时覆盖默认输出目录和格式；`--deskew=false`、`--autocrop=false` 可临时关闭处理。布尔参数使用等号。

TUI 中确认设备后，程序先只读验证，再保存设备记录和默认引用。有效但忙碌的设备可以登记；扫描仍需设备空闲且参数有效。普通 `info` 不因此重写默认选择，Agent 不写配置或设备表。

## 4. 错误与安全保存

- 引用已失效时返回 `device_record_not_found`。TUI 可主动重新选择；不会按 IP、型号或列表顺序改绑。显式 `--host` 或 `--endpoint` 可覆盖一个已失效但格式合法的默认引用。
- 查询发现可比较的设备身份明确冲突时返回 `device_identity_conflict`，不保存默认选择、不创建扫描任务。
- 记录引用不能与旧地址字段混用。高级地址参数须提供明确目标，不能只用端口或路径修改默认引用。
- 损坏的配置和设备表不会被自动修复或覆盖。自动保存拒绝替换符号链接；设备表读取也拒绝符号链接。
- 写入采用同目录临时文件、同步和原子替换，文件权限为 `0600`。默认选择保存失败时不继续扫描。两个文件不是原子事务：可能留下已验证设备记录，但原默认配置保持不变。

旧地址配置仍可读取；兼容字段、迁移和回退方法仅见[旧配置兼容](configuration-compatibility.md)。

相关专题：[界面与 Agent](interfaces.md) · [eSCL 与 WSD](escl.md) · [源码构建与 AI 环境](build.md) · [npm 安装与发布](npm.md)。
