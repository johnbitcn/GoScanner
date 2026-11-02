# Apple Silicon AI 适配：Core ML 时间成本基线

日期：2026-11-02。分支：`feat/coreml-adaptation`，从 `main` 的 `98836b2` 创建。

本轮只增加离线实验工具，不修改正式 Go 扫描流程、模型或 Go 依赖。

## 结论

- Core ML 模型缓存可跨进程复用，复用期间缓存内容及修改时间未变。
- **缓存命中不等于启动几乎无开销。** 本模型经 ORT 转换后被分成 32 个 Core ML 子模型，另有 CPU 执行部分。会话创建和首次推理仍有明显成本。
- 两次正式实验中，Core ML 后续推理中位数约 42–57 ms，CPU 约 52–60 ms；不足以认定改用此后端一定更快。
- 已独立测量 Core ML compile API、缓存发布、稳定路径加载；**纯 ONNX 转换和纯设备专用优化尚未独立分离**。
- 尚未完成单个完整原生 Core ML 模型转换。本结果不能用于宣称已移除 ORT、已适配全部 M 系列或已使用 ANE。

## 环境和方法

- Apple M1，macOS `27.0.1`，Python `3.12.16`。
- ONNX Runtime `1.31.1`，NumPy `2.5.4`。
- Core ML 后端参数：`ModelFormat=MLProgram`、`MLComputeUnits=ALL`、持久化 `ModelCacheDirectory`。
- ORT CPU 线程数固定为 4；这与正式 Go 代码使用默认会话选项不同。
- 模型：`fastvit_sa24_h_e_bifpn_256_fp32.onnx`。
- 模型 SHA256：`7f9f5a8935b2eb22b3ee0245d34996063f54562df390d34714af2d76928695bc`。
- 固定随机种子的 float32 输入，尺寸 `1×3×256×256`，没有扫描仪访问或真实扫描件。
- 首次推理单独计时，后续 10 次记录原始样本并取中位数。加载复用使用新进程，不是同一进程内反复调用。
- profiling 在额外进程执行，不混入时间表。

下表采用第二次正式实验 `verification/coreml-timing-cq79cz7z/summary.json`。第一次正式实验为 `verification/coreml-timing-ebaaecjn/`；更早探针为 `verification/coreml-timing-probe/`，仅用于确认后端与导出物，不纳入表内。

## 1. 完整模型：首次准备与缓存复用

| 场景 | 会话创建 | 首次推理 | 创建到首次输出 | 后续推理中位数 |
|---|---:|---:|---:|---:|
| CPU 基线 | 0.096 s | 0.067 s | 0.163 s | 60.4 ms |
| Core ML，新应用缓存 | 4.055 s | 0.784 s | 4.839 s | 46.2 ms |
| Core ML，缓存复用，新进程 1 | 2.905 s | 0.744 s | 3.648 s | 43.6 ms |
| Core ML，缓存复用，新进程 2 | 2.753 s | 0.718 s | 3.471 s | 48.5 ms |
| Core ML，缓存复用，新进程 3 | 2.902 s | 0.957 s | 3.859 s | 49.9 ms |

“创建到首次输出”只包含会话创建及首次推理，不包含 Python 启动、模块导入、输入构造或图像预处理。各进程总耗时另存于 `summary.json` 的 `process_wall_seconds`。

新缓存会话创建包含图优化、ONNX→Core ML 转换、Core ML 编译、缓存发布和模型加载；**4.055 s 不是纯模型转换耗时**。

设备专用优化可能在加载或第一次预测中发生，不能把 0.784 s 全部归为优化，也不能用冷暖场景相减得到精确的转换或优化时间。

## 2. 已导出子模型：原生 API 分段计时

为分离可测量的 API 边界，将 ORT 已导出的 32 个 Core ML 子模型编译到另一组稳定路径。每个数值是 32 个子模型 API 调用时间之和，不包含 Swift 程序构建和进程启动。

| 阶段 | 耗时 | 能说明什么 |
|---|---:|---|
| `MLModel.compileModel` | 1.020 s | 已转换 Core ML 子模型的编译成本，不包含 ONNX 转换 |
| 将编译产物移动到稳定缓存路径 | 0.006 s | 本机 move API 成本，不代表全部序列化写入或强制刷盘成本 |
| 稳定路径首次加载 | 2.377 s | 模型加载及 API 内的相关准备；不保证涵盖首次推理全部设备优化 |
| 新进程复用加载 1 | 2.368 s | 保留编译产物后的加载成本 |
| 新进程复用加载 2 | 2.384 s | 再次独立进程验证 |

该实验没有执行完整 ONNX 图。其时间与上一节不是可直接相加的同一条流水线，也不是完整原生模型替代方案的最终成绩。

## 3. 缓存与执行验证

- ORT 日志：优化后的图共 577 个节点；Core ML 支持其中 542 个节点，组合成 32 个分区。节点数量不是耗时占比。
- 独立 profiling 的 11 次推理共记录 352 个 Core ML 执行事件和 385 个 CPU 执行事件；确认并非整个会话静默退回 CPU。
- 配置 `ALL` 不能证明具体执行于 GPU 或 ANE，本轮未做硬件级归属分析。
- ORT 缓存约 **159.0 MiB**，包括转换产物与编译产物；跨进程复用时 SHA256、大小和修改时间均不变。
- 独立原生编译产物约 **79.5 MiB**；首次加载及随后两次加载均未改变这些文件。
- 合成输入的 Core ML 输出与 CPU 输出最大绝对差约 `3.80e-7`，平均绝对差约 `2.93e-8`，通过 `rtol=1e-3, atol=1e-4` 对比，输出有限且在 `[0, 1]` 内。
- 这只验证一个合成输入的数值差异，不证明真实文档定位和裁切正确。

## 4. 验证与下一步

已完成：

- 两次完整正式计时运行，分别保留日志、JSON、热图和缓存。
- 新增 Python 计时工具的 4 项单元测试通过。
- Swift 辅助程序实际构建及 compile/load 路径执行通过；对已有编译目标的拒绝覆盖检查通过。
- `PKG_CONFIG_PATH="$HOME/.local/opt/goscanner/opencv-4.13.0/lib/pkgconfig" CGO_ENABLED=1 go test ./...` 通过。
- Git 空白检查通过。

下一步先验证完整原生 Core ML 单模型的转换可行性与数值一致性，再在相同口径下比较启动和推理。当前 32 个分区的加载成本值得关注，但本轮没有隔离实验证明它是唯一瓶颈。

未完成：纯 ONNX 转换/纯设备优化的独立时间、原生单模型、真实图像四角与裁切验收、其他 M 系列、系统更新后的缓存失效、干净系统启动、完整扫描任务耗时。未清理系统缓存、重启或控制温度和其他进程负载；每次运行只有一个新应用缓存样本，不报告统计置信区间。

## 复现与资料

- [计时工具与命令](tools/coreml_timing/README.md)
- [ONNX Runtime Core ML：模型缓存配置](https://onnxruntime.ai/docs/execution-providers/CoreML-ExecutionProvider.html)
- [Apple：模型编译、设备专用优化与路径缓存](https://apple.github.io/coremltools/docs-guides/source/model-prediction.html#using-compiled-python-models-for-prediction)

模型、缓存、日志和生成的二进制保存在已忽略的 `verification/`，不纳入版本控制。
