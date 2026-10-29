# OpenCV 预处理离线验证

日期：2026-10-29。分支：`feat/custom-content-preprocessing`。

本次没有执行扫描仪发现、能力查询或实机扫描。下列结果来自合成图像、本机模拟 UDP/HTTP 服务和终端测试，不代表真实纸张检测已校准。

## 环境与构建

- macOS / Darwin arm64；Go 1.27.3。
- GoCV `v0.43.0`；OpenCV `4.13.0`，从固定标签源码编译。
- OpenCV 隔离安装：`~/.local/opt/goscanner/opencv-4.13.0`。当前精简构建范围见 README，不依赖临时源码目录运行。
- 本地验收程序：`bin/goscanner-opencv`。没有覆盖已有的 `bin/goscanner` 或 Intel 版本。
- 已检查动态库引用，程序指向用户目录中的 OpenCV，`--help` 实际运行成功。未设置 `DYLD_LIBRARY_PATH`。

仓库内执行：

```bash
export PKG_CONFIG_PATH="$HOME/.local/opt/goscanner/opencv-4.13.0/lib/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
CGO_ENABLED=1 go test -count=1 ./...
CGO_ENABLED=1 go test -race -count=1 -cover ./...
CGO_ENABLED=1 go vet ./...
CGO_ENABLED=1 go build -trimpath -o bin/goscanner-opencv .
./bin/goscanner-opencv --help
git diff --check
```

以上检查均通过。最终语句覆盖率为 **85.0%**，指整个 Go 包，不代表算法准确率，也不覆盖 OpenCV 内部 C++ 的全部行为。最低声明的 Go 1.22、Intel Mac、Linux、运行库打包与分发未验证。

## 已验证的关键阶段

1. **默认不变**：开关关闭时原始 JPEG 字节不变；启用但没有实际变换时也不重编码。非法 JPEG 与超大尺寸被拒绝。
2. **纸边估角与旋转**：正负 5° 的合成纸张纠偏后检查残余角度；画布扩展，不使用原尺寸直接切角。
3. **白底内容估角**：正负 4° 的分离 E 形字符多行页面检查输入角度和纠偏后残余角度；稀疏单行、混合方向、14° 超范围内容跳过。相关测试额外重复运行三次通过。该样本不是实际中文、英文文档。
4. **纸张裁切**：检查原分辨率、纸边安全余量及四角边缘标记；仅裁切时保留原倾斜，不隐式纠偏；纠偏后可继续裁切。
5. **误裁保护**：空白、白底内框跳过；贯穿纸张的居中竖条、偏心竖条和横条均逐字节保留原图并警告。后三项先在旧实现上实际失败，修复歧义判断后通过。
6. **编码与报告**：处理后的灰度 JPEG 保持灰度；CLI 显示处理后实际尺寸和字节数；不确定检测的警告到达 stderr。
7. **输出链路**：模拟服务验证 CLI → 下载 → 预处理 → JPEG / 单页 PDF，以及连续扫描 → 合并 PDF / 分单页 PDF。PDF 内嵌 JPEG 字节、图像尺寸、灰度颜色空间均与预处理结果一致，正常结束不留暂存文件。
8. **原有回归**：既有扫描生命周期、超时、取消、拒绝覆盖和 PDF 保留已完成页等测试继续通过。

独立只读审查发现了半页误裁问题；修复后的定向复查确认该阻塞问题解除。独立审查者未执行测试，上述命令由主 Agent 实际执行。处理函数返回错误时的原图回退有代码检查，但本次未通过故障注入独立覆盖该集成分支。

## 已知限制

- 裁切当前只接受四周有明显暗背景的浅色矩形纸张。白色背景、贴边纸张、窄外边框、多个显著亮区域或复杂背景通常会跳过。
- 浅色矩形照片与纸张可能无法区分；检测成功不等于内容完整。成功变换后不会额外保存原图。
- 纠偏需要可信纸边，或至少五行足够一致的内容方向。不做 OCR、90°/180°方向识别、透视矫正。
- JPEG 变换后以质量 95 重新编码。阴影、彩色纸张、淡字、印章、复杂图案和真实扫描噪声尚未验证。
- 本地 OpenCV 调用不支持 context 的硬实时抢占；超时/取消会在调用返回后继续检查。不能用 Go 的 race 结果保证 C++ 内部无竞争。

## 后续实机验收

先关闭预处理保存一份对照图，使用不同文件名，避免原图缺失。随后分别验证：

```bash
# 以下命令会启动真实扫描；本次未执行。
./bin/goscanner-opencv scan -o scans/reference.jpg
./bin/goscanner-opencv scan --deskew -o scans/deskew.jpg
./bin/goscanner-opencv scan --autocrop -o scans/paper-crop.jpg
./bin/goscanner-opencv scan --manual --deskew --autocrop -o scans/document.pdf
```

- 平正及正负倾斜纸张：检查文字方向、四角、页面内容是否完整。
- 纸边附近的页码、文字和印章：确认未被裁掉，纸内页边距仍保留。
- 白底/贴边/阴影/分隔条：确认不可靠时保留图像并提示，不能以“文件生成成功”代替内容检查。
- 彩色、灰度及两页以上 PDF：检查颜色、顺序、实际尺寸和逐页警告。

真实样本若触发保守跳过，先保留原图和警告用于调整阈值，不关闭歧义保护来追求裁切成功率。
