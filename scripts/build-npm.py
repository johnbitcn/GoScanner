#!/usr/bin/env python3
"""Build the self-contained macOS arm64 runtime. Only build hosts need Python."""
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import zipfile

ROOT = Path(__file__).resolve().parent.parent
WORK = ROOT / ".build"
RUNTIME = ROOT / "runtime"
LOCK = json.loads((ROOT / "npm/dependencies.json").read_text())
PREFIX = WORK / "opencv-install"
SOURCE = WORK / ("opencv-source-" + LOCK["opencv"]["commit"][:12])
BUILD = WORK / ("opencv-build-" + LOCK["opencv"]["commit"][:12])


def run(*args, **kwargs):
    print("+", " ".join(map(str, args)), flush=True)
    return subprocess.run(list(map(str, args)), check=True, **kwargs)


def output(*args):
    return subprocess.check_output(list(map(str, args)), text=True).strip()


def digest(file):
    with open(file, "rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def download(spec):
    dest = WORK / "downloads" / spec["filename"]
    dest.parent.mkdir(parents=True, exist_ok=True)
    if not dest.exists():
        partial = dest.with_name(dest.name + ".part")
        run("curl", "--fail", "--location", "--retry", "3", "--proto", "=https",
            "--proto-redir", "=https", spec["url"], "-o", partial)
        if digest(partial) != spec["sha256"]:
            raise RuntimeError(f"SHA256 mismatch: {partial}")
        partial.replace(dest)
    if digest(dest) != spec["sha256"]:
        raise RuntimeError(f"SHA256 mismatch: {dest}; remove the corrupt cache and retry")
    return dest


def dependencies(file):
    lines = output("otool", "-L", file).splitlines()[1:]
    deps = [line.strip().split(" (compatibility version", 1)[0] for line in lines]
    if file.suffix == ".dylib":
        identity = output("otool", "-D", file).splitlines()[-1].strip()
        deps = [dep for dep in deps if dep != identity]
    return deps


def system_library(dep):
    return dep.startswith(("/usr/lib/", "/System/Library/"))


def relocate():
    # GoCV links multiple OpenCV modules; follow the actual dependency closure, not a hand-maintained list.
    pending = list((RUNTIME / "bin").iterdir()) + list((RUNTIME / "lib").iterdir())
    visited = set()
    while pending:
        file = pending.pop()
        if file in visited:
            continue
        visited.add(file)
        for dep in dependencies(file):
            if system_library(dep):
                continue
            name = Path(dep).name
            source = PREFIX / "lib" / name
            if not name.startswith("libopencv_") or not source.is_file():
                raise RuntimeError(f"Unexpected non-system dependency: {file}: {dep}")
            dest = RUNTIME / "lib" / name
            if not dest.exists():
                shutil.copyfile(source, dest)
                dest.chmod(0o755)
                pending.append(dest)
            relative = "../lib/" if file.parent.name == "bin" else ""
            run("install_name_tool", "-change", dep, "@loader_path/" + relative + name, file)
        if file.suffix == ".dylib":
            run("install_name_tool", "-id", "@rpath/" + file.name, file)
        # Rewriting Mach-O invalidates its original signature, including Go's linker signature.
        run("codesign", "--force", "--sign", "-", "--timestamp=none", file)
    check_native()


def version(text):
    return tuple(map(int, text.split("."))) + (0,) * (3 - len(text.split(".")))


def check_native():
    for file in sorted(list((RUNTIME / "bin").iterdir()) + list((RUNTIME / "lib").iterdir())):
        if output("lipo", "-archs", file) != "arm64":
            raise RuntimeError(f"Not arm64-only: {file}")
        build = output("vtool", "-show-build", file)
        targets = re.findall(r"\bminos\s+(\d+(?:\.\d+)*)", build)
        if not targets or any(version(v) > version(LOCK["minimumMacOS"]) for v in targets):
            raise RuntimeError(f"Unsupported deployment target: {file}\n{build}")
        for dep in dependencies(file):
            if system_library(dep):
                continue
            if not dep.startswith("@loader_path/"):
                raise RuntimeError(f"Non-relocatable dependency: {file}: {dep}")
            resolved = (file.parent / dep.removeprefix("@loader_path/")).resolve()
            if not resolved.is_relative_to(RUNTIME.resolve()) or not resolved.is_file():
                raise RuntimeError(f"Missing or escaping dependency: {file}: {dep}")
        run("codesign", "--verify", "--strict", file)


def build():
    if platform.system() != "Darwin" or platform.machine() != "arm64":
        raise RuntimeError("Build on a native macOS Apple Silicon host")
    os.chdir(ROOT)
    downloads = {name: download(LOCK[name]) for name in ("opencv", "onnxruntime", "model", "modelLicense")}
    if not SOURCE.exists():
        SOURCE.mkdir(parents=True)
        # The source archive is verified above before extraction.
        run("tar", "-xzf", downloads["opencv"], "--strip-components=1", "-C", SOURCE)
    env = {**os.environ, "MACOSX_DEPLOYMENT_TARGET": LOCK["minimumMacOS"],
           "CGO_ENABLED": "1", "GOWORK": "off", "GOOS": "darwin", "GOARCH": "arm64",
           "CGO_CFLAGS": "-mmacosx-version-min=" + LOCK["minimumMacOS"],
           "CGO_CXXFLAGS": "-mmacosx-version-min=" + LOCK["minimumMacOS"],
           "CGO_LDFLAGS": "-Wl,-headerpad_max_install_names -mmacosx-version-min=" + LOCK["minimumMacOS"],
           "PKG_CONFIG_PATH": str(PREFIX / "lib/pkgconfig")}
    cmake = ["cmake", "-S", SOURCE, "-B", BUILD,
             "-DCMAKE_BUILD_TYPE=Release", f"-DCMAKE_INSTALL_PREFIX={PREFIX}",
             "-DCMAKE_OSX_ARCHITECTURES=arm64", f"-DCMAKE_OSX_DEPLOYMENT_TARGET={LOCK['minimumMacOS']}",
             f"-DCMAKE_INSTALL_NAME_DIR={PREFIX / 'lib'}", "-DCMAKE_SHARED_LINKER_FLAGS=-Wl,-headerpad_max_install_names",
             "-DBUILD_LIST=core,imgproc,imgcodecs,calib3d,dnn,features2d,highgui,objdetect,photo,video,videoio",
             "-DOPENCV_GENERATE_PKGCONFIG=ON", "-DBUILD_SHARED_LIBS=ON",
             "-DBUILD_ZLIB=ON", "-DBUILD_PNG=ON", "-DBUILD_JPEG=ON"]
    disabled = ["BUILD_TESTS", "BUILD_PERF_TESTS", "BUILD_EXAMPLES", "BUILD_opencv_apps", "BUILD_JAVA",
                "BUILD_opencv_python2", "BUILD_opencv_python3", "WITH_FFMPEG", "WITH_GSTREAMER", "WITH_QT",
                "WITH_VTK", "WITH_OPENEXR", "WITH_AVIF", "WITH_TIFF", "WITH_WEBP", "WITH_JASPER", "WITH_OPENJPEG",
                "WITH_PROTOBUF", "WITH_IPP", "WITH_OPENCL", "WITH_LAPACK", "WITH_EIGEN", "WITH_KLEIDICV", "WITH_ADE", "WITH_ITT"]
    run(*cmake, *(f"-D{flag}=OFF" for flag in disabled), env=env)
    run("cmake", "--build", BUILD, "--parallel", min(os.cpu_count() or 2, 4), env=env)
    run("cmake", "--install", BUILD, env=env)
    actual_opencv = subprocess.check_output(["pkg-config", "--modversion", "opencv4"], env=env, text=True).strip()
    if actual_opencv != LOCK["opencv"]["version"]:
        raise RuntimeError(f"Unexpected OpenCV version: {actual_opencv}")
    if RUNTIME.exists():
        shutil.rmtree(RUNTIME)
    for folder in ("bin", "lib", "models", "licenses"):
        (RUNTIME / folder).mkdir(parents=True)
    shutil.copyfile(downloads["model"], RUNTIME / "models" / LOCK["model"]["filename"])
    shutil.copyfile(downloads["modelLicense"], RUNTIME / "licenses/DocAligner-LICENSE")
    with zipfile.ZipFile(downloads["onnxruntime"]) as archive:
        library = RUNTIME / "lib" / LOCK["onnxruntime"]["library"]
        library.write_bytes(archive.read("onnxruntime/capi/" + library.name))
        if digest(library) != LOCK["onnxruntime"]["librarySha256"]:
            raise RuntimeError("Unexpected ONNX Runtime library in pinned wheel")
        for name in ("LICENSE", "ThirdPartyNotices.txt"):
            (RUNTIME / "licenses" / ("ONNXRuntime-" + name)).write_bytes(archive.read("onnxruntime/" + name))
    shutil.copyfile(SOURCE / "LICENSE", RUNTIME / "licenses/OpenCV-LICENSE")
    shutil.copytree(PREFIX / "share/licenses/opencv4", RUNTIME / "licenses/OpenCV-third-party")
    run("go", "build", "-trimpath", "-o", RUNTIME / "bin/goscanner", ".", env=env)
    modules = subprocess.check_output(["go", "list", "-deps", "-f", "{{if .Module}}{{.Module.Path}} {{.Module.Dir}}{{end}}", "."], env=env, text=True)
    for line in sorted(set(modules.splitlines())):
        if not line.strip():
            continue
        name, directory = line.split(" ", 1)
        if name == "goscanner":
            continue
        dest = RUNTIME / "licenses" / name.replace("/", "_")
        dest.mkdir()
        for pattern in ("LICENSE*", "NOTICE*", "COPYING*", "PATENTS*"):
            for file in Path(directory).glob(pattern):
                if file.is_file():
                    shutil.copyfile(file, dest / file.name)
        if not list(dest.iterdir()):
            raise RuntimeError(f"Missing module license: {name}")
    shutil.copyfile(Path(output("go", "env", "GOROOT")) / "LICENSE", RUNTIME / "licenses/Go-LICENSE")
    for file in RUNTIME.rglob("*"):
        if file.is_file():
            file.chmod(0o755 if file.parent.name in ("bin", "lib") else 0o644)
    relocate()
    manifest = {"platform": "darwin", "arch": "arm64", "minimumMacOS": LOCK["minimumMacOS"],
                "goVersion": output("go", "version"), "inputs": LOCK,
                "files": {str(file.relative_to(RUNTIME)): digest(file)
                          for file in sorted(RUNTIME.rglob("*")) if file.is_file()}}
    (RUNTIME / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    # Exercise the shipped ORT/model, not the developer's home-directory copies.
    env.update({"GOSCANNER_TEST_MODEL": "1", "GOSCANNER_MODEL_PATH": str(RUNTIME / "models" / LOCK["model"]["filename"]),
                "GOSCANNER_ONNXRUNTIME_PATH": str(library)})
    run("go", "test", "./...", env=env)
    run("go", "vet", "./...", env=env)
    run("node", "npm/check-runtime.cjs")
    print("Runtime ready. Run npm test and node scripts/verify-npm.cjs to pack and test an isolated installation.")


if __name__ == "__main__":
    try:
        if sys.argv[1:] == ["--check"]:
            check_native()
        elif not sys.argv[1:]:
            build()
        else:
            raise RuntimeError("Usage: python3 scripts/build-npm.py [--check]")
    except (RuntimeError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))
