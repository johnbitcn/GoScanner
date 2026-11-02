#!/usr/bin/env python3
"""Measure DocAligner Core ML preparation and persistent cache reuse, without a scanner."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import statistics
import subprocess
import sys
import tempfile
import time

MODEL_SHA256 = "7f9f5a8935b2eb22b3ee0245d34996063f54562df390d34714af2d76928695bc"
REPO = Path(__file__).resolve().parents[2]


def write_json(path, value):
    with Path(path).open("x") as stream:
        json.dump(value, stream, indent=2, ensure_ascii=False)
        stream.write("\n")


def cache_snapshot(root):
    return {
        str(p.relative_to(root)): [p.stat().st_size, p.stat().st_mtime_ns,
                                  hashlib.sha256(p.read_bytes()).hexdigest()]
        for p in sorted(root.rglob("*")) if p.is_file()
    }


def compare_outputs(reference, candidate):
    import numpy as np
    if candidate.shape != (1, 4, 128, 128) or not np.isfinite(candidate).all():
        raise ValueError("invalid DocAligner heatmap shape or non-finite values")
    if candidate.min() < 0 or candidate.max() > 1:
        raise ValueError("DocAligner heatmap is outside [0, 1]")
    delta = np.abs(reference - candidate)
    return {"max_absolute_error": float(delta.max()),
            "mean_absolute_error": float(delta.mean()),
            "allclose_rtol_1e-3_atol_1e-4": bool(np.allclose(
                reference, candidate, rtol=1e-3, atol=1e-4))}


def worker(args):
    import numpy as np
    import onnxruntime as ort
    if ort.__version__ != "1.31.1":
        raise ValueError("this experiment requires onnxruntime==1.31.1")
    ort.disable_telemetry_events()
    options = ort.SessionOptions()
    options.intra_op_num_threads = 4
    options.log_severity_level = 2
    if args.worker == "profile":
        options.enable_profiling = True
        options.profile_file_prefix = str(args.result.with_suffix(""))
    providers = ["CPUExecutionProvider"]
    if args.worker != "cpu":
        if "CoreMLExecutionProvider" not in ort.get_available_providers():
            raise ValueError("ONNX Runtime does not include CoreMLExecutionProvider")
        providers.insert(0, ("CoreMLExecutionProvider", {
            "ModelFormat": "MLProgram", "MLComputeUnits": "ALL",
            "ModelCacheDirectory": str(args.cache),
        }))
    x = np.random.default_rng(0).random((1, 3, 256, 256), dtype=np.float32)
    start = time.perf_counter()
    session = ort.InferenceSession(str(args.model), sess_options=options, providers=providers)
    create_seconds = time.perf_counter() - start
    if args.worker != "cpu" and "CoreMLExecutionProvider" not in session.get_providers():
        raise ValueError("Core ML session fell back entirely to CPU")
    session.disable_fallback()
    start = time.perf_counter()
    output = session.run(["heatmap"], {"img": x})[0]
    first_seconds = time.perf_counter() - start
    runs = []
    for _ in range(args.runs):
        start = time.perf_counter()
        session.run(["heatmap"], {"img": x})
        runs.append(time.perf_counter() - start)
    profile_counts = {}
    if args.worker == "profile":
        for event in json.loads(Path(session.end_profiling()).read_text()):
            provider = event.get("args", {}).get("provider")
            if provider:
                profile_counts[provider] = profile_counts.get(provider, 0) + 1
        if not profile_counts.get("CoreMLExecutionProvider"):
            raise ValueError("profile contains no actual Core ML execution")
    np.save(args.result.with_suffix(".npy"), output, allow_pickle=False)
    write_json(args.result, {
        "session_seconds": create_seconds, "first_inference_seconds": first_seconds,
        "ready_after_first_inference_seconds": create_seconds + first_seconds,
        "inference_seconds": runs, "inference_median_seconds": statistics.median(runs),
        "providers": session.get_providers(), "profile_provider_events": profile_counts,
        "onnxruntime": ort.__version__, "numpy": np.__version__,
    })


def child(args, root, name, mode):
    result = root / (name + ".json")
    command = [sys.executable, str(Path(__file__).resolve()), "--worker", mode,
               "--model", str(args.model), "--cache", str(root / "ort-cache"),
               "--result", str(result), "--runs", str(args.runs)]
    start = time.perf_counter()
    with (root / (name + ".log")).open("x") as log:
        subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=300)
    row = json.loads(result.read_text())
    row["process_wall_seconds"] = time.perf_counter() - start
    return row


def native_phases(root):
    models = sorted(p.parent for p in (root / "ort-cache").rglob("Manifest.json"))
    if not models:
        raise ValueError("Core ML produced no cached model packages")
    output = root / "native-compiled"
    output.mkdir()
    manifest = [dict(source=str(p), compiled=str(output / f"{i:03}.mlmodelc"))
                for i, p in enumerate(models)]
    manifest_path = root / "native-manifest.json"
    write_json(manifest_path, manifest)
    helper = root / "coreml_phases"
    with (root / "swift-build.log").open("x") as log:
        subprocess.run(["xcrun", "swiftc", "-O", "-module-cache-path",
                        str(root / "swift-module-cache"),
                        str(Path(__file__).with_name("coreml_phases.swift")), "-o", str(helper)],
                       check=True, stdout=log, stderr=subprocess.STDOUT, timeout=180)
    results = {}
    compiled_snapshot = None
    for name, mode in [("native-compile", "compile"), ("native-load-first", "load"),
                       ("native-load-reuse-1", "load"), ("native-load-reuse-2", "load")]:
        with (root / (name + ".json")).open("x") as out, (root / (name + ".log")).open("x") as log:
            subprocess.run([str(helper), mode, str(manifest_path)], check=True,
                           stdout=out, stderr=log, timeout=300)
        if mode == "compile":
            compiled_snapshot = cache_snapshot(output)
        elif cache_snapshot(output) != compiled_snapshot:
            raise ValueError("native compiled model files changed during loading")
        value = json.loads((root / (name + ".json")).read_text())
        results[name] = {
            key: sum(m.get(key, 0) for m in value["models"])
            for key in ["compile_seconds", "cache_publish_seconds", "load_seconds"]
        }
    results["partition_count"] = len(models)
    results["compiled_cache_unchanged_on_load"] = True
    results["compiled_bytes"] = sum(p.stat().st_size for p in output.rglob("*") if p.is_file())
    return results


def report(summary):
    cpu, cold = summary["cpu"], summary["cold"]
    warm = summary["reuse"]
    native = summary["native"]
    fmt = lambda n: f"{n:.6f}"
    lines = ["# Core ML 分阶段计时", "", "## 环境", "",
             f"- 芯片：{summary['machine']['chip']}",
             f"- macOS：{summary['machine']['macos']}",
             "- ONNX Runtime 1.31.1；Core ML MLProgram / ALL；ORT CPU 线程数 4。",
             "- 输入为固定种子的合成 float32 张量 1×3×256×256；没有访问扫描仪或扫描件。",
             "- 每个场景使用独立进程；所有缓存保留在本次结果目录，不清理系统缓存。",
             "", "## ONNX Runtime 完整模型路径", "",
             "| 场景 | 会话创建（秒） | 首次推理（秒） | 创建到首次输出（秒） | 后续推理中位数（秒） |",
             "|---|---:|---:|---:|---:|"]
    for label, row in [("CPU 基线", cpu), ("新应用缓存", cold)] + [
            (f"缓存复用，新进程 {i+1}", row) for i, row in enumerate(warm)]:
        lines.append("| " + " | ".join([label] + [fmt(row[k]) for k in [
            "session_seconds", "first_inference_seconds", "ready_after_first_inference_seconds",
            "inference_median_seconds"]]) + " |")
    lines += ["", "## 已导出 Core ML 子模型的独立 API 计时", "",
              f"- 子模型数量：{native['partition_count']}。不是一个完整的原生 Core ML 模型。",
              f"- `MLModel.compileModel` 总时间：{fmt(native['native-compile']['compile_seconds'])} 秒。",
              f"- 移动到稳定缓存路径：{fmt(native['native-compile']['cache_publish_seconds'])} 秒。",
              f"- 稳定路径首次 `MLModel` 加载总时间：{fmt(native['native-load-first']['load_seconds'])} 秒。",
              f"- 新进程复用加载 1：{fmt(native['native-load-reuse-1']['load_seconds'])} 秒。",
              f"- 新进程复用加载 2：{fmt(native['native-load-reuse-2']['load_seconds'])} 秒。",
              f"- 独立编译产物：{native['compiled_bytes']} 字节。",
              "", "## 验证", "",
              f"- 复用期间 ORT 缓存内容与文件修改时间均不变：{summary['cache_unchanged_on_reuse']}。",
              f"- 原生编译产物在加载期间不变：{native['compiled_cache_unchanged_on_load']}。",
              f"- ORT 缓存文件总大小：{summary['ort_cache_bytes']} 字节。",
              f"- Core ML 分区日志：{summary['partition_log']}。",
              f"- 独立 profiling 的实际执行事件：{summary['profile']['profile_provider_events']}。",
              f"- 合成输入对 CPU 的数值差异：{summary['comparison']}。",
              "", "## 边界与未完成项", "",
              "- 新缓存会话创建包含 ONNX 图优化、ONNX→Core ML 转换、Core ML 编译、缓存发布和模型加载；没有把它误报为纯转换时间。",
              "- 设备专用优化可能发生在加载及首次推理中；公共 API 没有独立阶段计时，不能用两次时间相减当作精确优化成本。",
              "- 独立 Swift 实验测量导出子模型的编译/缓存发布/加载，不执行完整 ONNX 图，不能与 ORT 各阶段直接相加。",
              "- 缓存发布时间只测量 move API，不代表强制刷盘耗时。",
              "- profiling 单独运行，未混入表内时间。Core ML 执行事件不能证明具体使用了 GPU 或 ANE。",
              "- 新应用缓存不等于全新 Mac：没有清除 OS 缓存、重启或控制温度/其他进程负载。首次场景只有一次样本。",
              "- 仅验证一个合成输入的有限性与数值差异；未验证真实图像的四角、裁切结果、其他芯片、原生单模型转换或扫描全链路。",
              "- 这只是适配实验；正式 Go 扫描流程和依赖未改变。", ""]
    return "\n".join(lines)


def main(args):
    if platform.system() != "Darwin" or platform.machine() != "arm64":
        raise ValueError("this experiment targets macOS Apple Silicon only")
    args.model = args.model.expanduser().resolve(strict=True)
    if hashlib.sha256(args.model.read_bytes()).hexdigest() != MODEL_SHA256:
        raise ValueError("model SHA256 mismatch")
    os.umask(0o077)
    if args.output:
        root = args.output.expanduser().resolve()
        root.mkdir(parents=True, exist_ok=False)
    else:
        (REPO / "verification").mkdir(exist_ok=True)
        root = Path(tempfile.mkdtemp(prefix="coreml-timing-", dir=REPO / "verification"))
    print(f"结果目录：{root}", flush=True)
    cpu = child(args, root, "cpu", "cpu")
    cold = child(args, root, "cold", "coreml")
    snapshot = cache_snapshot(root / "ort-cache")
    if not any(".mlmodelc/" in path for path in snapshot):
        raise ValueError("no compiled Core ML cache was produced")
    reuse = [child(args, root, f"reuse-{i+1}", "coreml") for i in range(3)]
    unchanged = snapshot == cache_snapshot(root / "ort-cache")
    profile = child(args, root, "profile", "profile")
    unchanged = unchanged and snapshot == cache_snapshot(root / "ort-cache")
    if not unchanged:
        raise ValueError("Core ML cache was changed during reuse; inspect files before claiming a hit")
    import numpy as np
    reference = np.load(root / "cpu.npy", allow_pickle=False)
    comparison = {name: compare_outputs(reference, np.load(root / f"{name}.npy", allow_pickle=False))
                  for name in ["cold", "reuse-1", "reuse-2", "reuse-3"]}
    native = native_phases(root)
    partition_log = re.findall(r"number of partitions supported by CoreML:.*", (root / "cold.log").read_text())
    summary = {
        "machine": {"chip": subprocess.check_output(["sysctl", "-n", "machdep.cpu.brand_string"], text=True).strip(),
                    "macos": platform.mac_ver()[0], "python": platform.python_version()},
        "model_sha256": MODEL_SHA256, "cpu": cpu, "cold": cold, "reuse": reuse,
        "profile": profile, "native": native, "partition_log": partition_log,
        "cache_unchanged_on_reuse": unchanged,
        "ort_cache_bytes": sum(row[0] for row in snapshot.values()), "comparison": comparison,
    }
    write_json(root / "summary.json", summary)
    (root / "REPORT.md").write_text(report(summary))
    print(report(summary), flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", type=Path, default=Path.home() / ".local/share/goscanner/models/fastvit_sa24_h_e_bifpn_256_fp32.onnx")
    parser.add_argument("--output", type=Path, help="new result directory; refuses to overwrite")
    parser.add_argument("--runs", type=int, default=10)
    parser.add_argument("--worker", choices=["cpu", "coreml", "profile"], help=argparse.SUPPRESS)
    parser.add_argument("--cache", type=Path, help=argparse.SUPPRESS)
    parser.add_argument("--result", type=Path, help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.runs <= 0:
        parser.error("--runs must be positive")
    if args.worker and (args.cache is None or args.result is None):
        parser.error("worker requires --cache and --result")
    if args.worker:
        worker(args)
    else:
        main(args)
