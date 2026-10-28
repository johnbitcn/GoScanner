# WSD 自动发现验证

日期：2026-10-28。环境：macOS 27.0.1、arm64、Go 1.27.3。

## Git 基线

实施前，已将第一阶段代码提交为 `d17083f`：`feat: add native Go WSD scanner CLI`。

本轮增加 `discover`、默认自动选择、显式 `--endpoint`，保留显式 `--host IP` 的原行为。没有增加第三方模块。

## 实机结果

在本机 `en0` 上连续执行三次：

```bash
./bin/goscanner discover --timeout 10s
```

初版三次均发现一项，没有警告。后续复核及重发增强的结果见下文“可靠性补充”：

```text
model:     HP Laser MFP 1188w
protocol:  wsd
endpoint:  http://192.168.1.8:8018/wsd/scan
interface: en0
```

随后分别验证：

```bash
# 未指定地址，自动发现并选择唯一端点。
./bin/goscanner info --timeout 20s

# 显式完整端点，跳过发现。
./bin/goscanner info --endpoint http://192.168.1.8:8018/wsd/scan --timeout 20s

# 兼容原显式 IP 用法，跳过发现。
./bin/goscanner info --host 192.168.1.8 --timeout 20s
```

均成功读取能力。自动选择日志指向发现得到的扫描端点，状态为 Idle。

本轮只进行 UDP Probe、SOAP 元数据 Get 和 GetScannerElements，没有创建硬件扫描任务。此前成功扫描的结果仍见 [VERIFICATION.md](VERIFICATION.md)。

真实局域网只有这一台设备。本轮没有用两台物理扫描仪验证多设备场景；该选择规则和重复回复使用本地模拟测试验证。

## 自动化结果

- `go test -race -count=1 -json -cover ./...`：通过。
- 28 个顶层测试，连同子测试共 93 项。
- 语句覆盖率：81.8%。
- `go vet ./...`：通过。
- Darwin arm64 构建和运行：通过。
- Darwin amd64 交叉编译：通过，未实机运行。

新增测试覆盖：

1. 通用和扫描器专用 Probe、独立随机 MessageID、明确的 XML 声明。
2. ProbeMatches 请求关联、Action、设备类型及 QName 命名空间继承、重绑定和别名。
3. 排除仅打印设备、错误命名空间、不匹配的请求及错误地址。
4. 元数据中的 ScannerServiceType、不同端口和路径、默认 QName 命名空间。
5. 拒绝外部/不同源 IP、DNS 名称、凭据、查询参数、片段、非 HTTP、无效端口和超长端点。
6. 环回 UDP 探测到 HTTP 元数据的完整流水线，不向局域网发送测试多播。
7. 重复 Probe 回复和重复扫描端点去重。
8. 零设备、多设备或有发现警告时拒绝自动选择。
9. 网络和元数据超时、候选设备数量限制、取消时停止监听；模拟前两条回复丢失后，通过有限 UDP 重发恢复发现。
10. 显式端点 CLI 的能力读取和模拟扫描，旧扫描测试继续通过。

限制值为：最多 32 个活动 IPv4 源地址、每接口 256 个接收包、64 个唯一元数据候选、4 个同时进行的元数据请求。响应仍受现有 SOAP 大小与 HTTP 边界限制。

## 可靠性补充

首次自动发现功能提交后，最后复核中发现一次间歇性空结果：独立 discover 找到 HP，但紧接着的自动 info 未发现设备；显式 IP 的 info 仍然成功。该轮失败输出保存在 `verification/discovery-final-info.log`。根因未定位，不能直接认定设备离线、XML 声明或某个确定的网络原因。

随后将只读 UDP Probe 改为每种最多 3 次、间隔约 0.5 秒，保持 MessageID 关联，监听窗口从 3 秒增至 4 秒。没有重试 CreateScanJob，也没有添加默认 IP 回退。新增测试用本地模拟服务丢弃前两条回复，验证后续重发能够恢复发现。

增强后在本机连续执行五组 discover + 不带地址参数的 info，共 10 次进程调用，全部成功，info 均返回 Idle。结果在 `verification/discovery-repeat-summary.json`。这说明有限重发在本次复核有效，不证明已经定位先前失败的真实原因，也不保证任何网络条件下都不漏发现。

## 排查记录

首次 Go 探测曾得到空数组。加入明确 XML 声明后发现成功，但随后用 Python 对同一设备进行四轮有/无 XML 声明的交替对照，**两种格式都收到回复**。因此不能认定 XML 声明缺失是空结果的确定原因。当前实现保留明确声明，并错开发送两个只读 Probe；未宣称已经验证所有休眠唤醒情形。

候选上限测试最初把 65 个匹配项放进一个 UDP 包，在本机发送时报 `sendto: message too long`，未真正到达解析器。已改为多个小数据包模拟回复洪泛，上限测试随后通过。没有修改系统网络参数。

## 输出与范围

- `discover` 输出 JSON 数组，警告写到 stderr。
- 空结果且搜索正常完成时输出 `[]`；所有接口失败、整体超时或中断时返回非零。
- 部分失败时列出已经验证元数据的结果并给出警告；默认 info/scan 拒绝使用这种不完整发现结果。
- 按扫描端点去重，不保证多个端点一定属于不同物理设备。多个结果一律要求显式选择。
- 发现确认的是发布的 WSD 服务，不是设备身份认证，也不保证该设备支持本工具的平板/JPEG 功能。

本地 `verification/discovery-*.json`、`discovery-*.log` 和 `discovery-tests.jsonl` 保存具体输出，已被 Git 忽略。源码、测试和本记录纳入 Git。

未验证：其他真实机型、IPv6/WS-Discovery 2009/eSCL 发现、跨子网、HTTPS-only、主机名端点、URN-only 服务绑定、受限 Wi-Fi/防火墙环境、长期休眠稳定性、Intel Mac 运行。没有安装常驻服务或系统驱动，没有推送远程仓库。
