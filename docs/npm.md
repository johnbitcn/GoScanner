# npm 预编译包与 Actions

## 用户安装

仅支持 **macOS 14.0 或更新版本、Apple Silicon（arm64）**。需要 arm64 版 Node.js 22+ 和 npm，不支持 Intel Mac、Rosetta 下的 x64 Node.js、Linux 或 Windows。

发布到 npm 后：

```bash
npm install -g @codingbone/goscanner
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

另外随包复制 OpenCV 许可证、其安装生成的第三方声明，以及所有实际链接的 Go 模块和 Go 运行时许可证。修改 Mach-O 加载路径属于分发改动；权重不修改，许可证不删除。更新依赖时必须重新核对来源、版本、校验值、声明及使用条件，不把本项目的许可自动套用于第三方权重。

本项目原创代码采用 [Apache License 2.0](../LICENSE)，`package.json` 声明 `Apache-2.0`，完整许可证随 npm 包放在根目录。第三方库和模型保留各自的许可证与声明，不统一改标为本项目许可证。npm 包名为 `@codingbone/goscanner`，当前源码版本为 `0.1.1`；GitHub 用户名不代表 npm scope 所有权。正式公开发布前，发布者仍应确认 scope 权限及分发许可。添加许可证前生成的旧 tarball 不包含本项目许可文件，应重新运行 Actions，验收并发布新的产物。

## Actions：构建、验收与可信发布

工作流：`.github/workflows/npm-macos-arm64.yml`。

- `workflow_dispatch` 手动运行只构建验收；推送匹配源码版本的 `v*` 标签时，额外执行 OIDC 发布。普通 push 和 PR 不触发发布。
- 固定使用 `macos-15` runner，并断言 `uname -m` 为 `arm64`。虽然官方 runner 汇总表仍列出 `macos-14`，其[镜像公告](https://github.com/actions/runner-images/blob/main/images/macos/macos-14-arm64-Readme.md)已声明于 2026-11-02 结束支持，因此不再选择它。编译部署目标仍为 macOS 14.0；最低系统实机验收另行完成。
- 使用 `go.mod` 的 Go 版本和 Node.js 22。Python 3.11+、CMake、pkg-config 和 Xcode 工具仅在构建端需要。
- 第三方 Actions 固定 commit；下载输入逐一校验 SHA256。缓存只保存下载归档，不复用已编译运行库。
- 从源码构建 OpenCV，编译 Go 程序，检查所有二进制为 arm64 且部署目标不高于 macOS 14.0。
- 修改加载路径后执行 ad-hoc 签名，并验证签名；生成完整性清单。
- 执行 Go 测试、真实模型合成图像推理、`go vet`、npm 测试及打包安装验证。
- 验证后上传 tarball、SHA256、`verification.json` 和运行时 manifest，默认保留 14 天。
- 构建 job 只有 `contents: read`；发布 job 单独获得 `id-token: write`。不设置 npm 令牌，不创建 GitHub Release。

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
dist/codingbone-goscanner-0.1.1.tgz
dist/codingbone-goscanner-0.1.1.tgz.sha256
dist/verification.json
```

这些生成目录均不进入 Git。升级 OpenCV commit 时使用独立的源码及 CMake 构建目录，避免复用旧源码树。

`verify-npm.cjs` 验证以下阶段：

1. 检查运行包文件清单、SHA256、平台信息和不存在安装钩子。
2. 使用 Node-only PATH、空 HOME、独立 npm 配置及缓存，离线安装 tarball，并禁用所有安装脚本。确认安装后的包声明 `Apache-2.0`，且根目录 `LICENSE` 与源码仓库中的完整许可证一致。
3. 从安装后的 npm bin 链接调用程序，对环回地址的模拟 WSD 服务分别执行普通扫描和 AI 扫描，解码生成图像。测试图像是合成数据，不使用真实扫描仪。
4. 临时移走本次构建的 OpenCV 安装目录。在移位安装的包中测试帮助和真实模型推理预检；之后只让环回服务返回 HTTP 500，确认不发布图像。不把这一步称为完整扫描成功。
5. 在 `finally` 中恢复构建目录并删除临时测试安装，输出校验文件和验证记录。

如果强制终止验证进程，`.build/opencv-install.verification-hidden` 可能尚未恢复。确认没有验证进程运行且原路径不存在后，将其改回 `.build/opencv-install`。不要在验证过程中同时运行构建脚本。

`npm pack` 的 `prepack` 会拒绝没有运行包、文件缺失或文件被修改的仓库。不得以 `--ignore-scripts` 绕过发布端检查；这里与用户安装端支持 `--ignore-scripts` 是不同操作。

## 发布方式与授权边界

- **发布只在 GitHub Actions 中执行。** 使用 npm Trusted Publishing（OIDC），不要求本机 `npm login`，不使用长期 `NPM_TOKEN` 或 `NODE_AUTH_TOKEN`。
- npm 包为 `@codingbone/goscanner`，GitHub 仓库为 `johnbitcn/GoScanner`。两者不是同一账户命名空间。
- 手动运行工作流只构建与验收，不发布。只有推送与 `package.json` 完全匹配的稳定版本标签（如 `v0.1.1`）才进入发布流程。
- 标签提交必须已进入远程 `main`。工作流会拒绝其他仓库、错误事件、版本不匹配、无法确认 registry 状态或已经发布的版本。
- 构建和发布分为两个 job。只有发布 job 获得 `id-token: write`；它下载同一次运行的原始 tarball，重新检查 SHA256、包元数据、项目 LICENSE、manifest 与验收记录，不能重新打包后发布。
- 发布使用 Node.js 24、固定的 npm 11.21.1 和 provenance。结束后从 registry 核对精确版本、原始 tarball 的 integrity／SHA1 及 `latest`。
- 首个版本 `0.1.0` 曾通过本机交互认证发布；这是历史记录，不是后续发布方式。已发布版本不可覆盖。
- 不自动创建 GitHub Release。版本标签与 npm 发布有关，但不等于创建 GitHub Release。

## npm 可信发布配置

配置由 npm 包维护者在包的 Settings → Trusted Publisher 中管理。当前工作流对应：

- Provider：GitHub Actions
- Organization or user：`johnbitcn`
- Repository：`GoScanner`
- Workflow filename：`npm-macos-arm64.yml`，只填写文件名，不含目录
- Environment name：留空；当前工作流没有指定 GitHub Environment
- 允许此连接执行 `npm publish`。独立管理 dist-tag 的额外权限不是正常发布的必要条件。

字段区分大小写。保存配置不代表 OIDC 已验证，只有实际 Actions 发布成功且 registry 内容一致才能确认链路。

npm 官方要求 CLI 11.5.1+、Node.js 22.14.0+，并使用受支持的托管 runner。当前发布 job 使用 GitHub 托管的 Ubuntu 24.04、Node.js 24 和 npm 11.21.1；构建仍在 macOS 15 arm64 运行。详见 [npm Trusted Publishing](https://docs.npmjs.com/trusted-publishers)。

**不要用 `npm whoami` 判断 OIDC 能否发布。** 它检查传统认证；本机返回 E401，不代表 GitHub Actions 的可信发布连接不可用。

## 发布新版本

### 1. 准备版本并合入 main

先修改 `package.json` 版本，更新相关文档，运行测试，再提交并推送到 `main`。仅增加补丁版本时，可使用：

```bash
npm version patch --no-git-tag-version
npm test
```

这不会替你提交、推送或发布。不要把旧 tarball 改名作为新版本使用。工作区应干净，标签对应的提交必须已经在远程 `main` 中。

### 2. 推送明确版本标签

在已同步的 `main` 上，确认版本后执行：

```bash
version="$(node -p "require('./package.json').version")"
git tag -a "v${version}" -m "Release ${version}"
git push origin "v${version}"
```

不要强制覆盖既有标签。标签会启动 `Build and publish npm package (macOS arm64)`：先检查版本及 main 归属，再构建、测试和验收，最后由独立 job 使用 OIDC 发布。

普通 push、PR 和 `workflow_dispatch` 不发布。手动选择标签运行也只做 CI，不发布。

### 3. 检查 Actions 与 registry

记录本次 run URL、源码 commit 和标签。只有构建、安装验收、发布及 registry 核对都通过，才能报告发布成功。不能把 workflow 已启动、artifact 已上传或 publish 命令已执行当作全部成功。

可通过公共 registry 复核，不需要本机登录：

```bash
version="$(node -p "require('./package.json').version")"
npm view "@codingbone/goscanner@${version}" version dist.integrity dist.attestations --json
npm view @codingbone/goscanner dist-tags --json
```

核对 `latest` 指向新版本，且 registry integrity 与 Actions 原始 tarball 的 SHA512 一致。OIDC provenance 将包与公开仓库、工作流及源码提交关联。

### 4. 可选的本机产物验收

从对应 Actions run 下载 artifact 并解压。文件位于 `dist/`，运行时清单位于 `runtime/manifest.json`。进入 `dist/` 后核对 SHA256：

```bash
version="0.1.1"  # 必须与本次源码和 tarball 一致
archive="codingbone-goscanner-${version}.tgz"
shasum -a 256 -c "${archive}.sha256"
destination="$(mktemp -d)"
npm install --global --prefix "$destination" --ignore-scripts --offline "./${archive}"
"$destination/bin/goscanner" --help
```

临时 prefix 不替换已安装程序；用户安装端允许禁用脚本，发布端则必须使用已通过完整构建与验收的原始包。不要在本地执行 `npm publish`，也不要向对话或仓库提供令牌、验证码、Cookie 或认证回调地址。

本机能运行不代表完成 macOS 14 实机、真实扫描仪或 Gatekeeper 公开分发验收。普通用户更新仍使用 `npm install -g @codingbone/goscanner`。

## 发布失败时

- **版本或 main 校验失败：** 修正版本与提交归属；不会因此执行发布。
- **构建或验收失败：** 不发布，不使用失败运行中的产物。
- **OIDC 认证失败：** 核对 npm 可信发布中的仓库、工作流文件名、Environment、允许发布权限，以及连接是否仍有效。检查发布 job 的 `id-token: write` 与 npm 版本；不要改用本地登录或长期令牌绕过。
- **publish 后核对失败：** 先查询 registry。版本可能已经发布，不要自动重复发布；比较实际 integrity，明确是发布失败还是可见性／核对失败。
- **首次可信连接过期：** 由维护者按 npm 界面要求重新建立连接，再考虑重跑失败 job。重跑前仍须查询 registry，避免重复发布。
