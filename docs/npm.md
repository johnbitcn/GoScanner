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

另外随包复制 OpenCV 许可证、其安装生成的第三方声明，以及实际链接的 Go 模块（GoCV、onnxruntime_go、x/image）和 Go 运行时许可证。修改 Mach-O 加载路径属于分发改动；权重不修改，许可证不删除。更新依赖时必须重新核对来源、版本、校验值、声明及使用条件，不把本项目的许可自动套用于第三方权重。

本项目原创代码采用 [Apache License 2.0](../LICENSE)，`package.json` 声明 `Apache-2.0`，完整许可证随 npm 包放在根目录。第三方库和模型保留各自的许可证与声明，不统一改标为本项目许可证。npm 包名为 `@codingbone/goscanner`，版本 `0.1.0`；GitHub 用户名不代表 npm scope 所有权。正式公开发布前，发布者仍应确认 scope 权限及分发许可。添加许可证前生成的旧 tarball 不包含本项目许可文件，应重新运行 Actions，验收并发布新的产物。

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
dist/codingbone-goscanner-0.1.0.tgz
dist/codingbone-goscanner-0.1.0.tgz.sha256
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

## 发布状态与授权边界

- npm 包：`@codingbone/goscanner`；GitHub 仓库：`johnbitcn/GoScanner`。两者不是同一账户命名空间。
- 首个版本 `0.1.0` 已通过本机交互式认证发布，registry 文件完整性值已与 Actions 产物核对一致。不要重新发布或覆盖该版本。
- 当前 `.github/workflows/npm-macos-arm64.yml` 仍只在手动触发时构建、验证并上传 artifact，不执行 `npm publish`。普通 push、PR 和版本标签不会自动发布。
- 用户已确认在 npm 添加 GitHub Actions 可信发布连接；尚未通过该连接执行过成功的 OIDC 发布，不能把“配置已保存”当作“自动发布已验证”。
- npm 发布与 GitHub Release 独立。本项目不要求创建 GitHub Release，也不在仓库中保存 npm 令牌、验证码或认证回调链接。

## 人工验收与 npm 发布（当前可用）

### 1. 准备新版本并构建

确认功能已合入 `main`，工作区干净。先修改 `package.json` 版本，再测试、提交并推送。仅增加补丁版本时，可以在仓库根目录执行：

```bash
npm version patch --no-git-tag-version
npm test
```

这两条命令不会替你提交或推送。确认版本变更后，提交并推送到 `main`，再到 GitHub Actions 手动运行 `Build npm package (macOS arm64)`，选择 `main`。版本标签目前不是构建或发布触发器。

记录本次 run URL 和源码 commit。仅使用全部步骤成功、对应预期 commit 的运行产物；失败或取消的运行不能作为发布依据。

### 2. 下载并验收原始 tarball

从本次 Actions 下载 artifact 并解压外层 ZIP。安装包、SHA256 和验证记录位于 `dist/`，运行时清单位于 `runtime/manifest.json`。进入解压后的 `dist/`，将下例版本号改为本次实际版本：

```bash
version="0.1.1"  # 示例；必须与本次 package.json 和 tarball 一致
archive="codingbone-goscanner-${version}.tgz"
shasum -a 256 -c "${archive}.sha256"
npm install -g --ignore-scripts "./${archive}"
goscanner --help
```

不能通过重命名旧 tarball 来升级版本或改变 scope。检查 `verification.json`、根目录项目 `LICENSE` 和第三方声明；在没有开发依赖的目标 Mac 上验收普通扫描、AI、PDF、交互与取消。最低 macOS 14 兼容性仍需要对应系统的实际运行记录；模拟服务测试不等于真实扫描仪验收。

### 3. 确认账户并手动发布

后续命令沿用同一终端中的 `version` 和 `archive` 变量。确认当前账户拥有 `@codingbone` 的发布权限。未登录或账户不正确时，先执行登录命令，并在本机完成认证：

```bash
npm whoami --registry=https://registry.npmjs.org/
# 必要时执行，再重新核对 whoami：
# npm login --registry=https://registry.npmjs.org/

npm publish "./${archive}" --access public \
  --registry=https://registry.npmjs.org/ --dry-run
npm publish "./${archive}" --access public \
  --registry=https://registry.npmjs.org/
```

确认 dry-run 的包名、版本、公开访问级别和 registry 后才执行正式命令。`npm whoami`、dry-run 成功不代表已完成发布所需的 2FA。如果返回 `EOTP`，在本机交互式终端完成发布认证；不要把验证码、令牌或认证链接发给代理，也不要绕过2FA。认证错误后先查询 registry，避免在状态不明时重复发布同一版本。

必须发布已验收的原始 tarball，不在发布阶段重新打包。发布 tarball 不等于重新执行仓库测试，应保留对应 Actions run、commit、SHA256 和验证记录。

### 4. 核对 registry 和实际安装

```bash
npm view "@codingbone/goscanner@${version}" \
  version license dist.integrity --json --registry=https://registry.npmjs.org/
npm view @codingbone/goscanner dist-tags --json \
  --registry=https://registry.npmjs.org/
node -e 'const fs=require("node:fs"),c=require("node:crypto"); console.log("sha512-"+c.createHash("sha512").update(fs.readFileSync(process.argv[1])).digest("base64"))' "${archive}"
```

核对版本、`Apache-2.0`、`latest` 指向及 registry 的 `dist.integrity`；后者应与上述原始 tarball 计算出的 SHA512 SRI 一致。再从 registry 安装本次精确版本，执行帮助命令和必要的扫描回归，不把本地 tarball 安装当作 registry 安装验证。

## OIDC 可信发布配置（已保存，待实际验证）

npm 包 Settings → Trusted Publisher 的配置约定如下。保存状态来自用户确认，不代表已完成首次 OIDC 发布：

| 字段 | 值 |
|---|---|
| Publisher | GitHub Actions |
| Label | `GoScanner GitHub Actions`，仅用于识别，可选 |
| Organization or user | `johnbitcn`，这里是 GitHub 仓库所有者，不是 npm 账户 |
| Repository | `GoScanner`，不填 URL |
| Workflow filename | `npm-macos-arm64.yml`，不带 `.github/workflows/` 前缀 |
| Environment name | 留空；当前工作流未绑定发布环境 |
| Allow npm publish | 勾选，允许直接发布 |
| Allow npm dist-tag | 暂不勾选，不授权独立管理 npm 标签 |

`npm stage publish` 总是允许，但本项目计划使用直接发布。`npm publish --tag latest` 或 `--tag beta` 属于发布操作；独立执行 `npm dist-tag add/rm` 才需要额外的 dist-tag 权限。npm 的 `latest`、`beta` 标签与 Git 的版本标签不是同一概念。

若以后增加 GitHub Environment 审批，需要同时修改发布 job 的 environment 和 npm 连接的 Environment name，使二者完全一致。工作流文件改名或仓库迁移时，也要同步核对可信发布配置。

按 npm 当前规则，新连接必须在创建后 **2 天内完成首次成功发布**，才能验证并绑定仓库身份。超时后连接会过期，不能继续使用或编辑；需删除并重新创建，再完成首次发布。若自动发布实现来不及完成，应届时重建连接，不能为赶期限跳过验证。

## 自动发布实施清单（尚未实现）

目标流程：更新版本并合入 `main` → 推送对应版本标签（如 `v0.1.1`）→ 构建和验收 → 使用 OIDC 发布同一个 tarball → 核对 registry。

实现前，推送标签不会自动发布。后续改造需满足：

1. 只允许 `main` 上的明确版本标签触发发布；校验标签与 `package.json` 版本一致。普通提交和 PR 不发布，已发布版本不得覆盖。
2. 保留当前 macOS arm64 构建、原生库审计、模型推理及安装验证。构建失败时禁止发布。
3. 增加独立发布 job，只下载和发布本次已验证的 tarball，不重新编译或打包。只有该 job 获得 `id-token: write`；构建 job 保持最小权限。
4. 发布端使用支持 OIDC 的固定 npm CLI 版本：**npm 11.5.1+、Node.js 22.14.0+**。当前构建使用的 Node.js 22 自带 npm 10，不能直接依赖它完成可信发布；可另用 Node.js 24 配合固定 npm 版本。用户安装端的 Node.js 22+ 要求不变。
5. 不配置长期 `NPM_TOKEN` 或 `NODE_AUTH_TOKEN`；由 npm CLI 使用 GitHub OIDC 取得短期凭据。不得在 OIDC 失败后自动改用令牌或绕过2FA。
6. 可按需增加 GitHub Environment 人工审批；这与 npm 2FA 不同。环境名必须和 npm 连接一致。
7. 用一个未发布的新版本完成首次真实 OIDC 发布。记录 run、commit、版本和文件完整性值；确认 registry 元数据、精确版本安装及运行通过后，才能将可信发布标为“已验证”。

官方依据：[npm Trusted publishing](https://docs.npmjs.com/trusted-publishers)。首次验证通过前保留上述手动发布流程。任何公开发布都需要明确的版本发布授权；不要把测试或配置验证当成发布许可。

## 尚未覆盖的验证

部署目标检查不代替在旧系统上的实际运行。工作流执行前不能声称已通过 GitHub runner 验证。

ad-hoc 签名只保证重写后的 Mach-O 签名有效，不是 Apple Developer ID 签名或公证，也不保证外部分发后的 Gatekeeper 行为。首次公开发布前需在目标 Mac 上验证实际下载、安装和启动；如需要 Developer ID 与公证，应另外配置证书及凭据，不要求用户关闭系统安全功能。
