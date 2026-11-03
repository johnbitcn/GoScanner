# npm 预编译包与 Actions

## 用户安装

仅支持 **macOS 14.0 或更新版本、Apple Silicon（arm64）**。需要 arm64 版 Node.js 22+ 和 npm，不支持 Intel Mac、Rosetta 下的 x64 Node.js、Linux 或 Windows。

发布到 npm 后：

```bash
npm install -g @johnbitcn/goscanner
goscanner --help
goscanner scan --deskew --autocrop
```

安装只下载 npm 包，不编译，不执行安装脚本，也不从 GitHub 或其他站点另外下载模型或运行库。使用 `--ignore-scripts` 安装也能工作。不需要 Go、Xcode、CMake、Homebrew、Python、单独的 OpenCV 或 ONNX Runtime。运行时仍需 macOS 自带的系统库与框架；不复制整个操作系统环境。

npm 包只提供命令行入口，不提供 JavaScript API。参数、当前工作目录、交互输入和退出码沿用原程序。配置仍位于 `~/.config/goscanner/config.json`。安装不扫描、不修改用户配置。

AI 默认关闭，用 `--deskew` 和 `--autocrop` 分别启用。npm 命令自动设置包内模型和 ONNX Runtime 路径；非空的 `GOSCANNER_MODEL_PATH`、`GOSCANNER_ONNXRUNTIME_PATH` 仍可显式覆盖。独立执行内部的 `runtime/bin/goscanner` 会绕过 npm 入口的路径设置，用户应使用 `goscanner` 命令。推理在本机 CPU 上执行，不上传图像。

## 包内容

```text
npm/
  cli.cjs                       命令入口
  check-runtime.cjs             发布完整性检查
  dependencies.json             下载来源、固定版本和 SHA256
runtime/
  bin/goscanner                 darwin/arm64 程序
  lib/*.dylib                   OpenCV 依赖闭包和 ONNX Runtime
  models/*.onnx                 固定版本 DocAligner 模型
  licenses/                     第三方许可证与声明
  manifest.json                 构建输入、Go 版本和文件 SHA256
```

不发布 Go 源码、Go 工具链、Python wheel、开发头文件、测试、扫描图像或验证数据。包内模型约 83 MB；最终压缩体积以构建输出为准。

OpenCV 的非系统依赖必须全部落在包内。构建脚本将 Mach-O 加载路径改为 `@loader_path` 相对路径，并检查整个依赖闭包；遇到 Homebrew 或其他未打包依赖时失败，不通过设置 `DYLD_LIBRARY_PATH` 掩盖问题。

## 固定构建输入与许可

`npm/dependencies.json` 固定以下输入的下载地址与 SHA256：

- OpenCV 4.13.0 的固定 Git commit 源码。构建时使用内置 JPEG、PNG、zlib，不链接 Homebrew 图像库。
- ONNX Runtime 1.31.1 官方 PyPI macOS arm64 wheel。只提取原生动态库、MIT LICENSE 和 ThirdPartyNotices；发布物不需要 Python。
- DocAligner 1.1.1 的 FastViT SA24 heatmap 权重。官方 [模型目录](https://docsaid.org/en/models/docaligner) 将 `docaligner-1.1.1-fastvit-sa24-7f9f5a89` 明确标为 Apache-2.0。权重从官方 GitHub Release 下载，完整 SHA256 与程序中的固定校验值一致。
- 固定 commit 的 DocAligner Apache-2.0 LICENSE。

另外随包复制 OpenCV 许可证、其安装生成的第三方声明，以及实际链接的 Go 模块（GoCV、onnxruntime_go、x/image）和 Go 运行时许可证。修改 Mach-O 加载路径属于分发改动；权重不修改，许可证不删除。更新依赖时必须重新核对来源、版本、校验值、声明及使用条件，不把本项目的许可自动套用于第三方权重。

本次不替项目选择源码许可证。npm 包名暂定 `@johnbitcn/goscanner`，版本 `0.1.0`；GitHub 用户名不代表 npm scope 所有权。正式公开发布前，发布者应确认 scope 权限、分发许可及项目自身的授权声明。

## Actions：只构建和保存产物

工作流：`.github/workflows/npm-macos-arm64.yml`。

- 仅 `workflow_dispatch` 手动触发，不因 push、PR 或标签自动运行。
- 固定使用 `macos-15` runner，并断言 `uname -m` 为 `arm64`。虽然官方 runner 汇总表仍列出 `macos-14`，其[镜像公告](https://github.com/actions/runner-images/blob/main/images/macos/macos-14-arm64-Readme.md)已声明于 2026-11-02 结束支持，因此不再选择它。编译部署目标仍为 macOS 14.0；最低系统实机验收另行完成。
- 使用 `go.mod` 的 Go 版本和 Node.js 22。Python 3.11+、CMake、pkg-config 和 Xcode 工具仅在构建端需要。
- 第三方 Actions 固定 commit；下载输入逐一校验 SHA256。缓存只保存下载归档，不复用已编译运行库。
- 从源码构建 OpenCV，编译 Go 程序，检查所有二进制为 arm64 且部署目标不高于 macOS 14.0。
- 修改加载路径后执行 ad-hoc 签名，并验证签名；生成完整性清单。
- 执行 Go 测试、真实模型合成图像推理、`go vet`、npm 测试及打包安装验证。
- 验证后上传 tarball、SHA256、`verification.json` 和运行时 manifest，默认保留 14 天。
- 只有 `contents: read` 权限；不设置 npm 令牌，不上传 npm，不创建 GitHub Release。

工作流文件需先存在于远程默认分支，才能通过 GitHub 的手动触发入口发现；之后可以选择包含它的目标分支运行。本地新增 YAML 不等于工作流已在 GitHub 执行。提交、推送、合并和触发远程工作流应按仓库授权进行。

## 本地复现

在原生 Apple Silicon Mac 的仓库根目录执行。仅构建机器需要 Go 1.26+、Python 3.11+、CMake、pkg-config 和 Xcode Command Line Tools。首次构建需要网络；已缓存且校验通过的依赖可复用。

```bash
npm test
python3 scripts/build-npm.py
node scripts/verify-npm.cjs
```

下载归档、OpenCV 源码及安装目录位于 `.build/`；生成的运行包位于 `runtime/`；最终文件位于：

```text
dist/johnbitcn-goscanner-0.1.0.tgz
dist/johnbitcn-goscanner-0.1.0.tgz.sha256
dist/verification.json
```

这些生成目录均不进入 Git。升级 OpenCV commit 时使用独立的源码及 CMake 构建目录，避免复用旧源码树。

`verify-npm.cjs` 验证以下阶段：

1. 检查运行包文件清单、SHA256、平台信息和不存在安装钩子。
2. 使用 Node-only PATH、空 HOME、独立 npm 配置及缓存，离线安装 tarball，并禁用所有安装脚本。
3. 从安装后的 npm bin 链接调用程序，对环回地址的模拟 WSD 服务分别执行普通扫描和 AI 扫描，解码生成图像。测试图像是合成数据，不使用真实扫描仪。
4. 临时移走本次构建的 OpenCV 安装目录。在移位安装的包中测试帮助和真实模型推理预检；之后只让环回服务返回 HTTP 500，确认不发布图像。不把这一步称为完整扫描成功。
5. 在 `finally` 中恢复构建目录并删除临时测试安装，输出校验文件和验证记录。

如果强制终止验证进程，`.build/opencv-install.verification-hidden` 可能尚未恢复。确认没有验证进程运行且原路径不存在后，将其改回 `.build/opencv-install`。不要在验证过程中同时运行构建脚本。

`npm pack` 的 `prepack` 会拒绝没有运行包、文件缺失或文件被修改的仓库。不得以 `--ignore-scripts` 绕过发布端检查；这里与用户安装端支持 `--ignore-scripts` 是不同操作。

## 人工验收与 npm 发布

从 Actions 下载构建产物并解压 artifact 外层 ZIP。上传物保留目录结构：安装包、校验文件与验证记录位于 `dist/`，运行时清单位于 `runtime/manifest.json`。先进入解压后的 `dist/` 子目录执行：

```bash
shasum -a 256 -c johnbitcn-goscanner-0.1.0.tgz.sha256
npm install -g --ignore-scripts ./johnbitcn-goscanner-0.1.0.tgz
goscanner --help
```

然后在没有开发依赖的目标 Mac 上验收普通扫描、AI 处理、PDF、交互和取消。最低目标 macOS 14 与其他目标系统应有实际运行记录。真实设备测试仍需用户本机执行。

确认版本、许可和权限后，可登录 npm，并发布已经验收的原始 tarball，不重新打包：

```bash
npm login
npm publish ./johnbitcn-goscanner-0.1.0.tgz --access public --dry-run
npm publish ./johnbitcn-goscanner-0.1.0.tgz --access public
```

遵循账户的双因素认证要求；不在仓库或工作流中保存 npm 凭据。发布 tarball 不等于执行仓库的所有测试，应使用 Actions 已通过验证的产物与校验文件。npm 版本不能覆盖，后续发布需先修改 `package.json` 版本。

## 尚未覆盖的验证

部署目标检查不代替在旧系统上的实际运行。工作流执行前不能声称已通过 GitHub runner 验证。

ad-hoc 签名只保证重写后的 Mach-O 签名有效，不是 Apple Developer ID 签名或公证，也不保证外部分发后的 Gatekeeper 行为。首次公开发布前需在目标 Mac 上验证实际下载、安装和启动；如需要 Developer ID 与公证，应另外配置证书及凭据，不要求用户关闭系统安全功能。
