# Core ML 计时实验

仅用于 macOS Apple Silicon 适配调查。不修改正式扫描流程，不连接扫描仪，不需要扫描件。

## 环境

- Apple Silicon Mac 与 macOS SDK、Swift 编译器（`xcrun swiftc`）。
- Python 3.12；本次实际使用 ONNX Runtime `1.31.1`、NumPy `2.5.4`。
- 含 `CoreMLExecutionProvider` 的 ONNX Runtime wheel。脚本拒绝整个会话静默回退到 CPU。
- 正式 DocAligner ONNX 模型，默认位置和 SHA256 与 `docaligner.go` 一致。

Python 和 Swift 仅用于计时工具，不是新增的产品运行依赖。模型不自动下载或上传；运行前关闭 ORT 遥测。

## 运行

在仓库根目录执行。可以使用已有合适的 Python 环境，也可以单独创建：

```bash
mkdir -p verification
python3.12 -m venv verification/coreml-venv
verification/coreml-venv/bin/python -m pip install onnxruntime==1.31.1 numpy==2.5.4

verification/coreml-venv/bin/python tools/coreml_timing/benchmark.py
verification/coreml-venv/bin/python -m unittest discover \
  -s tools/coreml_timing -p 'test_*.py' -v
```

可选参数：

```bash
python tools/coreml_timing/benchmark.py \
  --model /absolute/path/fastvit_sa24_h_e_bifpn_256_fp32.onnx \
  --output verification/coreml-my-run \
  --runs 10
```

`--output` 必须是不存在的新目录，脚本拒绝覆盖。默认使用 `verification/coreml-timing-*` 随机新目录。目录继承私有权限；失败时保留日志和部分产物，不删除旧结果。单个子进程有超时限制。

成功时输出：

- `REPORT.md`、`summary.json`：计时、数值差异、缓存检查与限制。
- `cpu.json`、`cold.json`、`reuse-*.json`：各进程的原始时间样本与进程总耗时。
- `.log`、独立 profiling JSON：分区、CPU 回退及实际执行证据。
- `.npy`：可复现合成输入对应的热图，不含扫描件。
- `ort-cache/`：ORT 转换和编译后的 Core ML 缓存。
- `native-manifest.json`、`native-compiled/`、`native-*.json`：独立原生 API 实验。

`verification/` 已被 Git 忽略；模型、缓存、二进制和原始日志不提交。

## 计时口径

1. **CPU 基线**：独立进程，仅使用 ORT CPU 后端。
2. **新应用缓存**：独立进程、新缓存路径，记录会话创建、首次推理和后续推理。
3. **缓存复用**：三个新进程使用同一稳定缓存路径；验证缓存文件内容、大小和修改时间不变。
4. **实际后端验证**：单独 profiling，不混入计时表；确认 Core ML 确实执行，记录 CPU 回退。
5. **独立原生 API**：从 ORT 导出的 Core ML 子模型分别调用 `MLModel.compileModel`，再移动到稳定路径；随后用三个新进程调用 `MLModel(contentsOf:configuration:)`，验证编译文件未变。Swift 辅助程序构建时间不计入模型时间。

没有把会话创建总时间拆成虚假的精确阶段：

- ORT 会话创建同时包含 ONNX 图优化、ONNX→Core ML 转换、编译、缓存发布和加载。
- 原生 compile API 时间不等于 ONNX 转换时间。API 内可能还有实现相关优化。
- 设备专用优化可能发生在加载和首次推理中，公共 API 没有独立计时接口。
- 缓存发布仅测 move API，不包含强制刷盘；加载不代表模型已完成所有首次推理准备。
- 原生实验处理多个子模型，不是独立可执行的完整 DocAligner Core ML 模型，不能移除 ORT 后直接使用。
- 系统缓存没有清空，不能将新应用缓存结果称为全新设备冷启动。
- 使用固定合成输入，每个进程后续推理默认 10 次；不代替真实图像和整个扫描流程验收。

实测摘要见 [`../../COREML-TIMING.md`](../../COREML-TIMING.md)。
