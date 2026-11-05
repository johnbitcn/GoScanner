'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const { spawn, spawnSync } = require('node:child_process');
const { once } = require('node:events');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { test } = require('node:test');
const { checkRuntime } = require('./check-runtime.cjs');

test('project declares Apache-2.0 and includes the complete license in its publish list', () => {
  const root = path.resolve(__dirname, '..');
  const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json')));
  assert.equal(pkg.name, '@codingbone/goscanner');
  assert.equal(pkg.license, 'Apache-2.0');
  assert(pkg.files.includes('LICENSE'));
  const license = fs.readFileSync(path.join(root, 'LICENSE'), 'utf8');
  assert.match(license, /Apache License\s+Version 2\.0, January 2004/);
  assert.match(license, /END OF TERMS AND CONDITIONS/);
});

test('README and configuration documentation links exist and are included in the package', () => {
  const root = path.resolve(__dirname, '..');
  const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json')));
  for (const document of ['README.md', 'docs/configuration.md', 'docs/configuration-compatibility.md', 'docs/escl.md', 'docs/interfaces.md']) {
    const markdown = fs.readFileSync(path.join(root, document), 'utf8');
    for (const [, link] of markdown.matchAll(/!?\[[^\]]*\]\(([^)\s]+)\)/g)) {
      if (/^https?:\/\//.test(link) || link.startsWith('#')) continue;
      const target = path.posix.normalize(path.posix.join(path.posix.dirname(document), link.split('#')[0]));
      assert(fs.existsSync(path.join(root, target)), `Missing ${document} target: ${target}`);
      assert(pkg.files.some((entry) => entry.endsWith('/') ? target.startsWith(entry) : entry === target),
        `${document} target is not shipped: ${target}`);
    }
  }
});

function fixture(t) {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'goscanner npm ')));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  for (const folder of ['npm', 'runtime/bin', 'runtime/lib', 'runtime/models', 'runtime/licenses']) {
    fs.mkdirSync(path.join(root, folder), { recursive: true });
  }
  for (const file of ['cli.cjs', 'dependencies.json']) {
    fs.copyFileSync(path.join(__dirname, file), path.join(root, 'npm', file));
  }
  fs.copyFileSync(path.join(__dirname, '..', 'package.json'), path.join(root, 'package.json'));
  return root;
}

function executable(root, source) {
  fs.writeFileSync(path.join(root, 'runtime/bin/goscanner'), `#!${process.execPath}\n${source}\n`, { mode: 0o755 });
}

function run(root, args = [], options = {}) {
  return spawnSync(process.execPath, [path.join(root, 'npm/cli.cjs'), ...args], {
    encoding: 'utf8', timeout: 10000, ...options,
  });
}

function manifestFixture(root) {
  const inputs = JSON.parse(fs.readFileSync(path.join(root, 'npm/dependencies.json')));
  const hash = (data) => crypto.createHash('sha256').update(data).digest('hex');
  inputs.model.sha256 = hash('model');
  inputs.modelLicense.sha256 = hash('license');
  fs.writeFileSync(path.join(root, 'npm/dependencies.json'), JSON.stringify(inputs));
  const contents = {
    'bin/goscanner': 'program', [`lib/${inputs.onnxruntime.library}`]: 'runtime',
    'lib/libopencv_core.413.dylib': 'opencv', [`models/${inputs.model.filename}`]: 'model',
    'licenses/DocAligner-LICENSE': 'license', 'licenses/OpenCV-LICENSE': 'license',
    'licenses/ONNXRuntime-LICENSE': 'license', 'licenses/ONNXRuntime-ThirdPartyNotices.txt': 'notices',
    'licenses/Go-LICENSE': 'license',
  };
  for (const [file, data] of Object.entries(contents)) {
    fs.writeFileSync(path.join(root, 'runtime', file), data);
  }
  fs.chmodSync(path.join(root, 'runtime/bin/goscanner'), 0o755);
  const manifest = { platform: 'darwin', arch: 'arm64', minimumMacOS: inputs.minimumMacOS,
    inputs, files: Object.fromEntries(Object.entries(contents).map(([file, data]) => [file, hash(data)])) };
  fs.writeFileSync(path.join(root, 'runtime/manifest.json'), JSON.stringify(manifest));
}

test('complete runtime passes; absent, changed, extra and symlinked files fail validation', (t) => {
  const root = fixture(t);
  assert.throws(() => checkRuntime(root), /ENOENT/);
  manifestFixture(root);
  assert.equal(checkRuntime(root).arch, 'arm64');
  const model = path.join(root, 'runtime/models', require('./dependencies.json').model.filename);
  fs.writeFileSync(model, 'changed');
  assert.throws(() => checkRuntime(root), /SHA256 mismatch/);
  fs.writeFileSync(model, 'model');
  const extra = path.join(root, 'runtime/private-data');
  fs.writeFileSync(extra, 'not allowed');
  assert.throws(() => checkRuntime(root), /inventory differs/);
  fs.unlinkSync(extra);
  fs.symlinkSync(model, extra);
  assert.throws(() => checkRuntime(root), /Symlink/);
});

test('runtime check rejects install hooks and wrong platform metadata', (t) => {
  const root = fixture(t);
  manifestFixture(root);
  const file = path.join(root, 'package.json');
  const pkg = JSON.parse(fs.readFileSync(file));
  pkg.scripts.install = 'go build';
  fs.writeFileSync(file, JSON.stringify(pkg));
  assert.throws(() => checkRuntime(root), /must not have a install hook/);
  delete pkg.scripts.install;
  pkg.cpu = ['x64'];
  fs.writeFileSync(file, JSON.stringify(pkg));
  assert.throws(() => checkRuntime(root));
});

test('launcher preserves arguments, stdin, cwd, output and exit code via npm-style symlink', (t) => {
  const root = fixture(t);
  executable(root, `
    const fs = require('node:fs');
    console.log(JSON.stringify({ args: process.argv.slice(2), cwd: process.cwd(), input: fs.readFileSync(0, 'utf8') }));
    console.error('native stderr');
    process.exit(7);
  `);
  const link = path.join(root, 'goscanner');
  fs.symlinkSync(path.join(root, 'npm/cli.cjs'), link);
  const result = spawnSync(process.execPath, [link, 'scan', '-o', 'a b.pdf', '--deskew=false'], {
    cwd: root, input: 'page\n', encoding: 'utf8', timeout: 10000,
  });
  assert.equal(result.status, 7, result.stderr);
  const output = JSON.parse(result.stdout);
  assert.deepEqual(output.args, ['scan', '-o', 'a b.pdf', '--deskew=false']);
  assert.equal(fs.realpathSync(output.cwd), root);
  assert.equal(output.input, 'page\n');
  assert.equal(result.stderr, 'native stderr\n');
});

test('launcher selects bundled assets and respects explicit environment overrides', (t) => {
  const root = fixture(t);
  const deps = require('./dependencies.json');
  executable(root, 'console.log(JSON.stringify([process.env.GOSCANNER_MODEL_PATH, process.env.GOSCANNER_ONNXRUNTIME_PATH]));');
  const result = run(root, [], { env: { ...process.env, GOSCANNER_MODEL_PATH: '', GOSCANNER_ONNXRUNTIME_PATH: '' } });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), [path.join(root, 'runtime/models', deps.model.filename),
    path.join(root, 'runtime/lib', deps.onnxruntime.library)]);
  const overrides = run(root, [], { env: { ...process.env, GOSCANNER_MODEL_PATH: '/custom model.onnx', GOSCANNER_ONNXRUNTIME_PATH: '/custom ort.dylib' } });
  assert.equal(overrides.status, 0, overrides.stderr);
  assert.deepEqual(JSON.parse(overrides.stdout), ['/custom model.onnx', '/custom ort.dylib']);
});

test('launcher reports an English startup failure and preserves the external path regardless of locale', (t) => {
  const original = fixture(t);
  const root = path.join(original, '中文安装目录');
  fs.mkdirSync(root);
  fs.cpSync(path.join(original, 'npm'), path.join(root, 'npm'), { recursive: true });
  let baseline;
  for (const locale of ['C', 'en_US.UTF-8', 'zh_CN.UTF-8']) {
    const result = run(root, ['--agent', '--help'], { env: { ...process.env, LANG: locale, LC_ALL: locale } });
    assert.equal(result.status, 1);
    assert.equal(result.stdout, '');
    const diagnostic = result.stderr.match(/^Cannot start GoScanner\. External error: (".*")\. Reinstall the complete macOS arm64 npm package/);
    assert.ok(diagnostic, result.stderr);
    assert.ok(JSON.parse(diagnostic[1]).includes(root));
    baseline ??= result.stderr;
    assert.equal(result.stderr, baseline);
  }
});

test('launcher startup diagnostics follow human language options and locale precedence', (t) => {
  const root = fixture(t);
  for (const { args = [], all = '', messages = '', lang = 'en_US.UTF-8', chinese } of [
    { chinese: false },
    { all: 'zh_CN.UTF-8', chinese: true },
    { all: 'C', messages: 'zh_CN', chinese: false },
    { messages: 'zh_CN.UTF-8', chinese: true },
    { lang: 'zh_SG.UTF-8', chinese: true },
    { lang: 'zh_TW.UTF-8', chinese: false },
    { args: ['--lang', 'zh-CN', 'scan'], chinese: true },
    { args: ['scan', '--lang=zh-CN'], chinese: true },
    { args: ['--lang=zh-CN', 'scan', '--lang=en'], chinese: false },
    { args: ['scan', '--lang=en'], all: 'zh_CN', chinese: false },
    { args: ['--lang=zh-CN', 'scan', '--lang=auto'], chinese: false },
    { args: ['scan', '-o', '--lang', '--help'], chinese: false },
    { args: ['--help', '--lang=zh-CN'], chinese: false },
    { args: ['--agent', '--lang=zh-CN', 'scan'], all: 'zh_CN', chinese: false },
  ]) {
    const result = run(root, args, { env: { ...process.env, LC_ALL: all, LC_MESSAGES: messages, LANG: lang } });
    assert.equal(result.status, 1, result.stderr);
    assert.equal(result.stdout, '');
    assert.ok(result.stderr.startsWith(chinese ? '无法启动 GoScanner。外部错误：' : 'Cannot start GoScanner. External error: '), JSON.stringify({ args, all, messages, lang, stderr: result.stderr }));
  }
});

for (const [signal, status] of [['SIGINT', 130], ['SIGTERM', 143]]) {
  test(`launcher forwards ${signal} and preserves cancellation status`, { timeout: 10000 }, async (t) => {
    const root = fixture(t);
    executable(root, `
      process.on('${signal}', () => process.exit(${status}));
      console.log('ready');
      setInterval(() => {}, 1000);
    `);
    const child = spawn(process.execPath, [path.join(root, 'npm/cli.cjs')], { stdio: ['ignore', 'pipe', 'pipe'] });
    t.after(() => child.kill());
    const exited = once(child, 'exit');
    await once(child.stdout, 'data');
    child.kill(signal);
    const [code, receivedSignal] = await exited;
    assert.equal(code, status);
    assert.equal(receivedSignal, null);
  });
}

test('launcher maps native signal termination to conventional exit code', (t) => {
  const root = fixture(t);
  executable(root, "process.kill(process.pid, 'SIGTERM');");
  const result = run(root);
  assert.equal(result.status, 143, result.stderr);
});
