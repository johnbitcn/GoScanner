# eSCL、WSD 与设备首选协议

## 默认操作

用户选择设备，不需要在扫描页或手动添加页选择协议。

1. 并行发现 eSCL（Bonjour）和 WSD（WS-Discovery）。
2. 只有可靠身份信息确认一致时才合并。首次确认双协议设备时，首选 eSCL。
3. 后续操作默认使用设备列表保存的首选协议。连接或能力查询失败就报告错误，不自动切换协议。
4. 设备列表选中设备后按 `p`，可在该设备已确认支持的协议之间切换。设置持久保存，重新发现、能力刷新和重新启动都不会重置为 eSCL。
5. CLI 的 `--protocol wsd` / `--protocol escl` 是可选的本次覆盖，不修改设备首选协议。

设备列表和当前任务显示“**型号** + IP + 协议”，IP 和协议使用当前深色／浅色主题的信息色，不设置背景色。每行只显示当前选用的一个协议，不显示 `eSCL + WSD` 作为当前协议。

## 手动添加：只输入 IP

在 TUI 选择“手动添加”，输入 IP，选中“发现并使用设备”并按 Enter。程序按 IP 筛选发现结果，取得设备发布的实际扫描协议、端口和路径；再只读验证，成功后保存默认设备。验证不会创建扫描任务。

发现端口固定：mDNS 使用 UDP 5353，WS-Discovery 使用 UDP 3702。但扫描服务的 HTTP/HTTPS 端口和路径由设备发布，不能据此固定为 80、8018 或 `/eSCL`。如果发现被网络隔离、防火墙或本地网络权限阻断，程序会报告未取得服务地址，不会猜地址后试扫。

同一 IP 若对应多条无法确认同一身份的设备记录，TUI 保留各条记录并要求选择设备，不会仅凭 IP 合并。

CLI 也可以只给 IP：

```bash
goscanner info --host 192.168.1.8
goscanner scan --host 192.168.1.8 --dpi 300 --mode color -o scan.png
goscanner --agent info --host 192.168.1.8
```

已登记 IP 使用设备表首选地址；未登记 IP 按发现结果解析。Agent 模式允许用户明确给出 IP 后进行这种筛选发现，但不写配置或设备表；没有地址时仍不会自动发现或选择设备。

高级 CLI 保留 `--endpoint`、`--port`、`--path`、`--scheme` 兼容入口，不要求普通用户填写。未登记的旧式完整地址保留原有配置协议语义。低级地址无法从设备表确定协议时，可以显式指定 `--protocol`；不会为猜测协议提交扫描任务。

## 身份与持久化

相同有效 UUID 或有效序列号等身份信息确认一致时，才跨协议合并。相同 IP、相似型号、DNS-SD 实例名、hostname 和疑似 MAC 片段都不足以单独确认身份。

部分厂商为 WSD 与 DNS-SD 使用不同 UUID，双方有效序列号一致时仍可确认；有效序列号冲突时不合并。DNS-SD UUID 与 eSCL 能力 UUID 都有效但矛盾时，不使用该 UUID 合并，并分别保留证据。无法交叉验证时，保留为两台设备。

`~/.config/goscanner/drives.json` 是设备记录数组。例如，用户将双协议设备的首选协议改为 WSD 后：

```json
[
  {
    "record_id": "scanner_4e3bb8e859e34bd292ee7e65dd156413",
    "id": "urn:uuid:87654321-4321-5678-9012-123456789abc",
    "name": "Office Scanner",
    "model": "Example MFP",
    "protocol": "wsd",
    "preferred_protocol": "wsd",
    "endpoint": "http://192.168.1.8:8018/wsd/scan",
    "interface": "en0",
    "serial_number": "DEVICE-123",
    "endpoints": [
      {
        "protocol": "escl",
        "endpoint": "http://192.168.1.8:80/eSCL",
        "id": "12345678-1234-5678-9012-123456789abc",
        "capability_uuid": "urn:uuid:12345678-1234-5678-9012-123456789abc",
        "serial_number": "DEVICE-123"
      },
      {
        "protocol": "wsd",
        "endpoint": "http://192.168.1.8:8018/wsd/scan",
        "id": "urn:uuid:87654321-4321-5678-9012-123456789abc",
        "serial_number": "DEVICE-123"
      }
    ],
    "discovered": true,
    "verified": false
  }
]
```

- `record_id` 是本地稳定引用，配置中的 `device.record_id` 指向它；切换协议不改变此 ID。可靠合并前的本地 ID 保留在可选 `record_aliases` 中。
- `protocol`、`endpoint`、`id` 表示当前首选入口，不是另造的跨协议统一 UUID。
- `preferred_protocol` 记录用户主动设置的首选协议；尚未主动设置时可省略，首次双协议合并自动选 eSCL。
- `endpoints` 保存各协议的地址和身份；单协议记录可省略此字段。
- eSCL `id` 保留 DNS-SD 身份，`capability_uuid` 保留能力响应 UUID；WSD `id` 保留 WS-Discovery EndpointReference。
- 发现漏包不删除已确认的其他协议地址。保留地址不表示自动回退。
- 改变首选入口时清除不适用于新入口的能力快照。`verified` 只表示历史查询，不保证现在空闲。
- 默认配置通过稳定记录 ID 定位设备，以设备表首选协议为准。CLI 显式协议只覆盖本次操作。旧地址配置的解释和迁移仅见[旧配置兼容](configuration-compatibility.md)。
- 本地 ID 不是硬件认证。现有能力查询发现有效序列号或同来源能力 UUID 明确冲突时，拒绝登记默认引用或创建扫描任务；不会把不同来源 UUID 的差异直接当作硬件更换。

## 扫描与兼容限制

两个协议共用平板单页、手动连续换页、颜色、DPI、扫描区域、图像保存、PDF 合成和可选 AI 后处理。本次不增加 ADF、双面、USB 或厂商私有协议。eSCL `SettingProfile` 参数组合在创建任务前校验。

创建扫描任务只发送一次 POST；连接失败、HTTP 503、超时或结果不确定时都不会跨协议重扫。仅 `NextDocument` 的 HTTP 503 可以在有界时间内等候重取。成功取图后清理任务；清理失败时保留完整图像并停止连续扫描。

PNG 按设备能力选择 PNG、TIFF 或 JPEG 源；JPEG 转 PNG 不能恢复压缩损失。TIFF 要求设备提供受支持的 TIFF 格式。PDF 仍由本机图像合成。原图后缀跟随实际源格式。

macOS 使用系统 Bonjour，无需额外 Go 模块、Avahi 或 SANE。自动发现仅支持直连 IPv4 局域网；Linux 源码构建保留 WSD 发现及已知地址的手动 eSCL 入口，但无 Bonjour 自动发现。

端点使用数字单播 IP。请求禁用代理和重定向；任务 Location 只能指向同源 ScanJobs 下的单个任务。响应大小、XML 深度、图像像素和处理时间均有限制。HTTPS 正常验证系统信任链及 IP 身份，不跳过自签证书错误；只有 hostname 证书而没有对应 IP SAN 的设备仍受此限制。

自动化测试使用模拟 HTTP 设备、公开能力响应样本和 Bonjour callback stub，不代替真实设备和实际网络验收。
