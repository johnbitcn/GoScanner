# TIFF / PNG 联机验证

日期：2026-10-29。设备：HP Laser MFP 1188w，`192.168.1.8`。

基线为 `optimize/fix-edge-clipping` 的 `8a5f74b`，另包含本轮固定下载文档名的修复。测试构建使用 Go 1.27.3、`CGO_ENABLED=0` 和 `-trimpath`。

## 发现的问题与修复

基线程序的默认 A4 彩色 JPEG 扫描成功。原生 TIFF 的 CreateScanJob 成功，但 RetrieveImage 返回 `ClientErrorNoImagesAvailable`；程序没有重试扫描，错误退出后设备为 Idle。

通过临时环回诊断转发进行单变量对照：仅把 RetrieveImage 中的 `DocumentName` 从 `IMAGE000.TIF` 改回 `IMAGE000.JPG`，保持票据 `Format=tiff-single-uncompressed`。设备实际返回的 DocumentFinalParameters 仍为 TIFF、RGB24、300 DPI、A4，并成功返回有效的未压缩 RGB TIFF。

因此，HP 的下载对象名不能按图像扩展名推导。修复保留固定的 `IMAGE000.JPG`；最终 TIFF/PNG 仍来自原生 TIFF，不通过 JPEG 转码。回归测试已先在旧实现下失败，再在修复后通过。

## 实测结果

| 项目 | 结果 | 像素尺寸 | 文件字节数 | 耗时 |
| --- | --- | --- | --- | --- |
| 默认 A4、300 DPI、彩色 JPEG，基线直连 | 通过 | 2480 × 3512 | 1,324,826 | 11.94 秒 |
| 默认 A4、300 DPI、彩色原生 TIFF，诊断转发对照 | 通过，TIFF Compression=1 | 2480 × 3507 | 26,092,340 | 67.23 秒 |
| 默认 A4、300 DPI、彩色 PNG，修复后程序直连 | 通过 | 2480 × 3507 | 11,655,300 | 63.14 秒 |
| full、150 DPI、灰度原生 TIFF，修复后程序直连 | 通过，TIFF Compression=1 | 1275 × 1759 | 2,242,979 | 7.97 秒 |
| 指定已存在的 PNG 路径 | 正确拒绝，文件 SHA-256 不变 | — | — | 0.16 秒 |
| 自动发现 | 找到 en0 上的 HP WSD 扫描端点，无警告 | — | — | 4.16 秒 |
| 最后一次显式 IP 能力查询 | 成功，设备 Idle | — | — | 0.17 秒 |

- 所有成功图像均经 Pillow `verify` 和完整 `load` 检查，确认实际格式、RGB/灰度、尺寸和单页属性；TIFF 确认采用未压缩方式。
- 每次真实扫描前后均确认设备 Idle。没有自动重试创建/下载任务，没有强制结束成功扫描。
- 修复后 PNG 和 TIFF 的文件权限为 `0600`，没有遗留图像暂存文件。
- JPEG 与 TIFF/PNG 高度相差 5 像素是本次实际返回结果；客户端没有裁切或缩放。不能据此推断所有格式的固件像素对齐规则。
- 全量 Go 测试、竞态检查和 `go vet` 通过。PNG 与同源 TIFF 的逐像素一致性由模拟测试验证；没有将两次独立物理扫描当成像素完全一致。

## 文件与未验证项

- 测试图像仅保存在本机 `/tmp/goscanner-online-20261029-101504-lw3otems/`，未复制到 Google Drive 项目目录。
- 不含图像内容和任务令牌的 JSON 日志位于本地、被 Git 忽略的 `verification/online-20261029-101504/`。旧二进制备份在其中的 `before-document-name-fix/`。
- 本轮未实测新格式的手动连续换页、扫描中断、其他分辨率/区域组合或 Intel Mac/Linux 运行行为。
- 没有验证纸张内容、吃边、白边、颜色准确性或文字清晰度。
- 独立命名的 `goscanner-fixed` 测试程序曾发生能力查询超时；相同字节的原路径 `bin/goscanner` 则查询和扫描成功。尚未确定该进程路径差异的原因，未更改防火墙、VPN 或系统权限。
- 最初一次新测试构建的只读能力查询曾返回 HTTP 400，后续只读复查成功；未为此添加扫描自动重试。
