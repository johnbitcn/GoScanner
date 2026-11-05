# 源码构建与 AI 运行环境

本文面向从源码构建的开发者。通过 npm 安装的用户无需执行这些步骤；用户安装与扫描说明见 [中文 README](../README-CN.md)，分发包构建见 [npm 安装与发布](npm.md)。

## AI 预处理准备

**npm 用户不需要执行本节的依赖准备。** npm 命令自动定位包内运行库和模型；以下步骤仅用于源码构建。显式设置的 `GOSCANNER_MODEL_PATH`、`GOSCANNER_ONNXRUNTIME_PATH` 仍优先于包内路径。

源码最低 Go 版本为 1.26；构建需要 OpenCV 4.13、GoCV 0.43 和 CGO。设置 `PKG_CONFIG_PATH` 指向 OpenCV 的 `lib/pkgconfig`。本机路径见下方命令；其他平台须自行安装兼容的 OpenCV。

启用 `--deskew` 或 `--autocrop` 前，将 [DocsaidLab DocAligner](https://github.com/DocsaidLab/DocAligner) 官方 heatmap 模型（[下载来源](https://drive.google.com/file/d/14vUH77v6yGg7zFctUgcT6BzV5Iisg4Dl/view)）的 `fastvit_sa24_h_e_bifpn_256_fp32.onnx` 放到 `~/.local/share/goscanner/models/`，并将 ONNX Runtime 1.31.1 共享库放到 `~/.local/opt/goscanner/onnxruntime-1.31.1/lib/`（macOS 文件名 `libonnxruntime.1.31.1.dylib`，Linux 为 `libonnxruntime.so.1.31.1`）。模型 SHA256 必须为 `7f9f5a8935b2eb22b3ee0245d34996063f54562df390d34714af2d76928695bc`。可分别用 `GOSCANNER_MODEL_PATH` 和 `GOSCANNER_ONNXRUNTIME_PATH` 覆盖完整路径。程序不会下载模型、联网推理或上传图像；创建扫描任务前检查模型、库和会话。默认不启用 AI，查询或发现不加载模型。

固定 SHA256 用于确认模型图结构。启动时只建立一个正式推理会话，并用约定名称、类型和尺寸的张量执行一次暖机验证；输入输出契约不匹配或出现非有限热图时，在扫描前失败。连续页复用该会话，每页只推理一次。暖机不检查是否检测到纸张，也不另外建立元数据会话。

两个开关独立：纠偏只旋转，裁切只裁外接矩形；同时启用时先旋转再裁切。裁切在预测四角外保留检测对象最长边的 2% 作为安全边距，至少 3 像素；边距不随平板上未使用的区域变大。这不是实际纸边精度保证，相邻对象仍需核对。边界检测不可靠或处理错误时警告并输出原图。启用时始终额外保留设备原始图像：单页 `foo-original.jpg` / `foo-original.tiff`；连续扫描及 PDF 使用 `foo-original-001.jpg` 等页号（PNG 的源图是 TIFF）。原图放在最终输出目录，不覆盖已有文件；处理或 PDF 失败仍保留已保存原图。输出和原图均为 `0600`，并以硬链接原子发布。

纠偏使用预测四角的边线方向：相邻边按近似垂直、相对边按近似平行判断，两边关系的角度容差为 3°。只使用唯一的最大一致边集合（至少两条边），对边方向做等权圆周平均；不再让长边占更大权重。启用纠偏时，没有可信边对或最大集合不唯一就跳过旋转并警告；若边界本身有效且启用裁切，仍按未旋转的边界裁切。角度不可信时命令行显示 `Angle: 不可用`，不将其报告为 0°；小于 0.3° 时不旋转。3° 是初始容差，仍需用实际扫描样本评估。

JPEG 有实际变换时以质量 95 编码；TIFF 和 PNG 不经过 JPEG。TIFF 变换后不保证保留原标签或位深。小角度和不需裁切时原始 JPEG/TIFF 字节不变。AI 不识别文字方向、不做透视校正；模型误定位仍可能裁掉内容，请核对原图。

```bash
./bin/goscanner-ai scan --deskew --autocrop -o scans/document.png
```

AI 预处理使用 ONNX Runtime 的 CPU 推理流程。

## OpenCV 构建依赖

本节用于开发者源码构建。npm 用户无需安装这些工具或运行库；完整 npm 包的构建脚本会另外设置 macOS 14 部署目标并重写动态库路径，不能将本机开发库直接复制为分发包。

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
CGO_ENABLED=1 go build -trimpath -o bin/goscanner-ai .
```

这是当前图像处理功能使用的精简 OpenCV 构建，不包含贡献模块、FFmpeg 或 Protobuf 模型导入支持。后续功能需要这些能力时再调整依赖。上述安装名设置用于让 macOS 正确找到动态库，不依赖临时的 `DYLD_LIBRARY_PATH`。若此前已链接另一处 OpenCV，切换安装位置后用 `go test -a ./...` / `go build -a .` 强制重新编译，避免 Go 缓存保留旧动态库引用。新终端执行构建或测试前，需要重新设置 `PKG_CONFIG_PATH`；运行程序仍须保留安装目录，不能只复制可执行文件到另一台机器。

## 开发验证

```bash
go test ./...
go test -race -cover ./...
go vet ./...

CGO_ENABLED=1 go build -trimpath -o bin/goscanner-ai .
```

默认测试使用环回地址上的模拟 UDP/HTTP 服务和能力样本；macOS 下还使用本机伪终端，不向局域网发送多播、不访问真实打印机、不启动扫描。`-race` 属于开发验证，需相应编译工具；构建需要 CGO 和 OpenCV 开发库。

新增测试覆盖连续两页、每页超时与换页等待、文件冲突、后续页失败保留前页，以及 macOS 下的按键序列、终端恢复、CLI 的 Esc/Ctrl+C 退出码和无控制终端拒绝启动。Linux 按键行为尚未实测。

新增 `golang.org/x/image v0.47.0` 用于 TIFF 编解码；该安全版本要求 Go 1.26.0，因此同步提高源码构建要求。锁实现针对 Darwin/Linux；Linux 未做实机扫描验证，Windows 构建未实现。最低 Go 1.26 版本尚未单独测试。

源码按功能放在同一 `main` 包中，没有额外的服务层或插件框架。`config.go` 使用标准库读取用户 JSON 配置；`config.example.json` 仅为示例，不会自动加载。`testdata/scanner-elements.xml` 来自此前成功的只读能力查询，不含扫描任务令牌。

