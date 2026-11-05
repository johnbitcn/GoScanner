# eSCL 测试资料

## `escl-brother-mfc-j985dw.xml`

来源：SANE backends 的公开问题 [Broken ESCL device Brother MFC-J985DW，#510](https://gitlab.com/sane-project/backends/-/issues/510)。该问题附有设备 `/eSCL/ScannerCapabilities` 响应，型号为 Brother MFC-J985DW，声明 eSCL 2.5；原报告未说明固件版本。

本地整理：只提取能力 XML；将序列号替换为 `REDACTED-SERIAL`；去除网页 Markdown 对 schema 文件名下划线的转义。没有将该序列号用于真实设备关联。测试不跟随 XML 的 `schemaLocation` 或任何设备 URI。

原报告指出设备虽然声明支持 PDF，平板 PDF 扫描却会返回 500。本项目测试因此检查使用 JPEG 图像格式；PDF 由本机合成。这里只验证能力解析和参数生成，不表示已对该型号进行实际扫描。

## `dnssd_lifecycle.c`

本项目编写的 Bonjour callback stub。它编译实际使用的 `dnssd_darwin.h`，替换 resolve/address/deallocate API，不发送网络请求。覆盖重复更新的查询数量、hostname 变更时的旧地址失效、旧 hostname callback、服务 Remove 后重新 Add，以及子查询释放。
