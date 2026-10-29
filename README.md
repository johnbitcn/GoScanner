# GoScanner

面向 HP Laser MFP 1188w 的 macOS WSD 命令行扫描工具。扫描协议使用 Go 标准库；图像预处理使用 GoCV/OpenCV，需要 CGO 和 OpenCV 原生库。不需要 Python、HP 驱动、Avahi 或 SANE。

已实现 WSD 自动发现、能力查询、IP/端点地址配置、分辨率/色彩/区域选择、单页 JPEG、手动连续扫描、超时与取消、安全保存，以及可选的小角度纠偏和纸张边界裁切。当前仍只支持 WSD 扫描，不支持 eSCL-only 设备。

## 快速开始

在 `GoScanner/goscanner/` 仓库中执行：

```bash
# 先按下文安装 OpenCV；本机开发使用 Go 1.27.3，go.mod 声明最低 Go 1.22。
pkg-config --modversion opencv4
CGO_ENABLED=1 go build -trimpath -o bin/goscanner .

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

# 手动连续扫描：立即扫描第一页，换页后按任意键继续，单独按 Esc 结束。
# 依次保存 scans/document-001.jpg、scans/document-002.jpg 等。
./bin/goscanner scan --manual -o scans/document.jpg

./bin/goscanner --help
```

**行为变更：不再默认连接 `192.168.1.8`。** 未指定 `--host` 或 `--endpoint` 时先自动发现；只在发现一项扫描端点且没有发现警告时使用它。多项、零项或发现不完整时停止，不会退回旧 IP，也不会选择列表第一项。

显式 `--host IP` 保留旧用法：使用 `http://IP:8018/wsd/scan`。显式 `--endpoint URL` 支持发现得到的其他端口和路径。两者互斥，均跳过发现。地址必须使用 IP，不接受用户名、查询参数、片段或 DNS 主机名。

默认文件保存到**当前工作目录**的 `scans/scan-时间戳.jpg`，不是二进制所在目录。

本地可能仍保留主分支此前生成的 `bin/goscanner` 和 `bin/goscanner-darwin-amd64`。这些旧程序不包含本分支的预处理功能；此前的实机验证不能代表当前版本。本次离线验收构建另存为 `bin/goscanner-opencv`，没有覆盖旧程序，运行示例中的程序名可替换为它。

本分支构建需要 OpenCV 运行库，即使未开启预处理也需要它。不能再用 `CGO_ENABLED=0` 构建，交叉编译需要目标架构的 C/C++ 工具链和 OpenCV 库。尚未做运行库打包、Developer ID 分发签名或公证。

## 参数

所有参数放在 `discover`、`info` 或 `scan` 子命令之后。

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--host` | 无，自动发现 | info/scan；显式 IP，固定端口 8018 和路径 `/wsd/scan` |
| `--endpoint` | 无，自动发现 | info/scan；完整 HTTP 扫描地址，与 --host 互斥 |
| `--timeout` | discover 为 `10s`；其他为 `2m` | 默认限制整体任务，包含发现；手动模式分别限制发现和每页扫描，不包含换页等待；失败取消清理最多额外等待 10 秒 |
| `--dpi` | `300` | 仅 scan；必须同时被设备的横向和纵向能力列表支持 |
| `--mode` | `color` | 仅 scan；`color` 或 `gray` |
| `--area` | `a4` | 仅 scan；`a4` 或 `x,y,width,height`，单位毫米 |
| `-o` | 自动生成 JPEG 路径 | 仅 scan；新 `.jpg`、`.jpeg` 或 `.pdf` 文件路径，扩展名不区分大小写 |
| `--manual` | `false` | 仅 scan；手动换页，JPEG 按页保存，PDF 默认合并为一个文档 |
| `--single-page` | `false` | 仅用于 PDF；每页独立保存，连续扫描时文件名带序号 |
| `--deskew` | `false` | 仅 scan；智能小角度纠偏，不判断 90°/180°文字方向 |
| `--autocrop` | `false` | 仅 scan；按纸张边界裁切，不按文字或图片内容裁切 |

区域坐标原点为平板左上角，毫米值会转换为 WSD 的 1/1000 英寸单位。宽高必须大于零，区域必须落在设备声明的范围内。`info` 的 `min_size` / `max_size` 使用 `size_unit` 指定的原始单位。

能力查询中的 `formats`、`colors` 是**设备原始声明**，不表示客户端已开放全部组合：

- `RGB24` 对应 `color`，`Grayscale8` 对应 `gray`。
- 设备声明 `BlackAndWhite1`，但本次原生黑白 + JPEG 实测返回 `ClientErrorNoImagesAvailable`。因此没有开放 `--mode bw`，也不会悄悄降级为灰度或再创建一个任务。
- TIFF、PNG、自动进纸与图形界面不属于本阶段。

## 图像预处理

默认关闭。两个开关可独立使用，也可用于手动连续扫描及所有 PDF 输出方式：

```bash
# 小角度纠偏，再按纸张边界裁切。
./bin/goscanner scan --deskew --autocrop -o scans/document.jpg
./bin/goscanner scan --manual --deskew --autocrop -o scans/document.pdf
```

- 在下载图像并验证 JPEG 后处理，在保存 JPEG 或生成 PDF 前完成。检测图像最长边不超过 1600 像素，变换作用于原分辨率，不缩小输出图像。
- 纠偏优先使用可靠纸边；纸边不可见时，要求至少五行一致方向的内容。仅处理不超过 10° 的倾斜；不足 0.3° 不旋转。不做 OCR、90°/180°方向识别或透视校正。旋转扩展画布并用白色填充，不切掉原图四角。
- 裁切只接受四周有明显暗背景的浅色矩形纸张，并向外保留少量安全边距。白色背景、贴边纸张、多个明显亮区域或边界不可靠时跳过，不用文字框替代纸边。不改变纸内原有页边距；仅开启裁切时不旋转。
- 无法可靠判断时输出逐页警告；没有实际变换时逐字节保留原始 JPEG。处理错误也会回退保存原图并警告，不会重新扫描。
- 有实际变换时 JPEG 以质量 95 重新编码；灰度 JPEG 保持灰度。PDF 嵌入处理后的 JPEG，页面物理尺寸按处理后像素和扫描 DPI 计算，不额外缩放到 A4。
- 自动检测不是内容完整性保证。浅色矩形照片与纸张可能无法区分，复杂背景、彩色纸张、阴影及混合方向内容尚需实机样本验证。成功变换时不会另存原图；重要文档请先保留一次未开启预处理的扫描结果。

## 手动连续扫描

```bash
./bin/goscanner scan --manual --host 192.168.1.8 --mode gray -o ~/Desktop/document.jpg
```

1. 在平板上放好第一页，运行命令。程序立即扫描。
2. 完整保存为 `document-001.jpg` 后，程序提示换页并等待按键。
3. 更换扫描页，按任意键继续，无需回车。下一页保存为 `document-002.jpg`，序号可超过三位。方向键也可继续。
4. 扫描完成后，在等待提示处单独按 `Esc`。程序显示已保存页数，并以退出码 0 结束。

手动模式需要可访问 `/dev/tty` 的交互终端，不能在没有控制终端的后台任务中启动。`Ctrl+C` 仍表示取消，退出码为 130。程序在等待期间临时关闭逐行输入和按键回显，退出等待时恢复终端设置。扫描期间的输入会在下次等待前丢弃，不能提前排队扫描；`Esc` 仅在等待换页时用于正常结束。为区分方向键序列，单独 `Esc` 的识别可能延迟约 0.1 秒。

`--timeout` 分别限制发现和每页扫描，不限制换页时间。每页都重新检查设备能力与状态。整个会话持有扫描器锁，避免本工具的其他进程在换页时插入任务。任何一页失败都会结束会话，不自动重试；之前完整保存的页面不删除。

`-o document.jpg` 是文件名基准，不生成无序号的 `document.jpg`。如果某个编号文件已存在，程序拒绝覆盖并停止，不跳过编号；重新扫描时请使用新的文件名基准。默认基准仍包含时间戳。

新增模式已通过本机 macOS 伪终端按键测试和环回模拟扫描测试；尚未进行真实扫描仪连续换页验证。

## 保存为 PDF

用 `-o` 指定 `.pdf` 扩展名即可选择 PDF。不指定 `-o` 时仍默认保存 JPEG。

```bash
# 单次扫描：保存一页 PDF。
./bin/goscanner scan -o scans/page.pdf

# 连续扫描：按 Esc 结束后，按扫描顺序合并为一个 document.pdf。
./bin/goscanner scan --manual -o scans/document.pdf

# 单页模式：每页立即保存为独立 PDF，文件名为 page-001.pdf、page-002.pdf……
./bin/goscanner scan --manual --single-page -o scans/page.pdf
```

- `--single-page` 仅用于 `.pdf` 输出；不启用 `--manual` 时，仍只扫描一次，使用指定的原文件名。
- 合并 PDF 在结束时发布，不生成带序号的 PDF。Ctrl+C、换页等待失败或后续页扫描失败时，也会保存已经完成的页面；失败退出码不变。第一页未完成时不生成空 PDF。
- PDF 直接嵌入扫描器返回的 JPEG，不重复压缩。每页纸张尺寸按实际像素尺寸和 `--dpi` 计算，支持彩色和灰度；不包含 OCR 文本层。
- 合并前，完整 JPEG 保存在输出目录内的私有 `.goscanner-pdf-*` 暂存目录。PDF 保存成功后清理这些 JPEG；保存失败时保留并报告恢复路径。
- 已有 PDF 不会被覆盖。文件名在扫描期间被其他程序占用时，保留完整 `.pdf.part` 和原始 JPEG，不覆盖新出现的文件。
- 强制终止、进程崩溃或断电不会执行合并与清理，可能留下暂存图像；不能保证尚未完成的页面可恢复。

PDF 已通过模拟扫描测试、文档结构检查，并在本机使用 `qpdf`、`pdfinfo` 和 `pdftoppm` 检查和渲染。用户已反馈 PDF 功能实测通过；具体测试模式和异常场景未单独记录。

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

请求尺寸与返回像素可能因固件对齐而略有差异。未开启预处理时，程序原样保存设备返回的图像，不裁切、缩放。程序会使用 Go JPEG 解码器完整验证图像；独立的 macOS ImageIO 解码检查也已通过。没有判断纸张内容、文字清晰度或色彩准确性。

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
- 下载响应最多 64 MiB，SOAP XML 最多 1 MiB，JPEG 最多 4000 万像素。超时不是对本地解码、OpenCV 处理、PDF 合并和文件系统调用的硬实时抢占保证。
- PDF 同样先写盘、关闭，再用硬链接独占发布。PDF 和图像文件权限为 `0600`，新建输出目录和图像暂存目录权限为 `0700`。合并期间需要容纳原始 JPEG 和完整 PDF 的磁盘空间。

## 固件兼容性与网络边界

这台 HP 固件会在 MIME 分隔符前漏写 CRLF。程序只对已观察到的 `_DPWS_v1.0_MimeBoundary_WSD` 边界做修复，然后用标准库解析 MIME，按 `start` 和 XOP `cid:` 找到图像，不假定附件顺序。测试覆盖修复前后图像字节一致及重复修复不改变数据。

HTTP 客户端不使用代理环境变量，不跟随重定向，不访问 XOP 中的外部 URL。任务令牌仅在内存中用于下载，不写入日志。

WSD 使用明文 HTTP，只在可信局域网运行，不要把端口暴露到互联网。当前项目位于 Google Drive 同步路径内，扫描文件可能自动上传；敏感文档请用 `-o` 指定非同步目录。

## OpenCV 构建依赖

需要 C/C++ 编译工具、CMake、`pkg-config`，以及与 GoCV 匹配的 OpenCV。本分支固定 GoCV `v0.43.0`，使用 OpenCV `4.13.0` 验证。不要直接安装当前 Homebrew 默认的 OpenCV 5；`opencv@4` 也不保证仍为匹配的小版本。

下面是 macOS 本地隔离安装示例，不写入系统库目录，不升级已有 Homebrew 依赖。先安装 Xcode Command Line Tools，并确保 `cmake` 和 `pkg-config` 可用。首次克隆源码、下载 Go 模块需要网络；准备完成后，构建和合成图像测试不需要扫描仪。

```bash
prefix="$HOME/.local/opt/goscanner/opencv-4.13.0"
# 如目标已存在，先核对版本，不覆盖其他安装。
git clone --depth 1 --branch 4.13.0 https://github.com/opencv/opencv.git /tmp/goscanner-opencv-source
cmake -S /tmp/goscanner-opencv-source -B /tmp/goscanner-opencv-build \
  -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$prefix" \
  -DCMAKE_INSTALL_NAME_DIR="$prefix/lib" -DCMAKE_INSTALL_RPATH="$prefix/lib" \
  -DBUILD_LIST=core,imgproc,imgcodecs,calib3d,dnn,features2d,highgui,objdetect,photo,video,videoio \
  -DOPENCV_GENERATE_PKGCONFIG=ON -DBUILD_TESTS=OFF -DBUILD_PERF_TESTS=OFF \
  -DBUILD_EXAMPLES=OFF -DBUILD_opencv_apps=OFF -DBUILD_JAVA=OFF \
  -DBUILD_opencv_python2=OFF -DBUILD_opencv_python3=OFF \
  -DWITH_FFMPEG=OFF -DWITH_GSTREAMER=OFF -DWITH_QT=OFF -DWITH_VTK=OFF \
  -DWITH_OPENEXR=OFF -DWITH_AVIF=OFF -DWITH_TIFF=OFF -DWITH_WEBP=OFF \
  -DWITH_JASPER=OFF -DWITH_OPENJPEG=OFF -DWITH_PROTOBUF=OFF \
  -DWITH_IPP=OFF -DWITH_OPENCL=OFF -DWITH_LAPACK=OFF -DWITH_EIGEN=OFF \
  -DWITH_KLEIDICV=OFF -DWITH_ADE=OFF
cmake --build /tmp/goscanner-opencv-build --parallel 6
cmake --install /tmp/goscanner-opencv-build
export PKG_CONFIG_PATH="$prefix/lib/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
pkg-config --modversion opencv4  # 应为 4.13.0
CGO_ENABLED=1 go build -trimpath -o bin/goscanner .
```

这是当前图像处理功能使用的精简 OpenCV 构建，不包含贡献模块、FFmpeg 或 Protobuf 模型导入支持。后续功能需要这些能力时再调整依赖。上述安装名设置用于让 macOS 正确找到动态库，不依赖临时的 `DYLD_LIBRARY_PATH`。若此前已链接另一处 OpenCV，切换安装位置后用 `go test -a ./...` / `go build -a .` 强制重新编译，避免 Go 缓存保留旧动态库引用。新终端执行构建或测试前，需要重新设置 `PKG_CONFIG_PATH`；运行程序仍须保留安装目录，不能只复制可执行文件到另一台机器。

## 开发验证

```bash
go test ./...
go test -race -cover ./...
go vet ./...

CGO_ENABLED=1 go build -trimpath -o bin/goscanner .
```

默认测试使用环回地址上的模拟 UDP/HTTP 服务和能力样本；macOS 下还使用本机伪终端，不向局域网发送多播、不访问真实打印机、不启动扫描。所有构建和测试都需要 CGO、C/C++ 编译工具和匹配版本的 OpenCV；`-race` 不能检查 OpenCV 内部的 C++ 数据竞争。

测试覆盖连续两页、每页超时与换页等待、文件冲突、后续页失败保留前页，以及 macOS 下的按键序列、终端恢复、CLI 的 Esc/Ctrl+C 退出码和无控制终端拒绝启动。预处理另有合成纸张、字符、多方向拒绝、分隔条误裁回归和 JPEG/PDF 输出链测试。当前离线验证结果和实机验收清单见 [PREPROCESSING-VERIFICATION.md](PREPROCESSING-VERIFICATION.md)。Linux 按键行为尚未实测。

依赖固定为 GoCV `v0.43.0`，本分支匹配 OpenCV `4.13.0`。锁实现针对 Darwin/Linux；Linux 未做本分支构建或实机扫描验证，Windows 构建未实现。最低 Go 1.22 版本尚未单独测试。

源码按功能放在同一 `main` 包中，没有额外的服务层、插件框架或配置文件。`testdata/scanner-elements.xml` 来自此前成功的只读能力查询，不含扫描任务令牌。
