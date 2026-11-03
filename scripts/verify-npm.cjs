'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const { execFileSync, spawn } = require('node:child_process');
const { checkRuntime } = require('../npm/check-runtime.cjs');

const root = path.resolve(__dirname, '..');
const dist = path.join(root, 'dist');
const prefix = path.join(root, '.build/opencv-install');
const hidden = prefix + '.verification-hidden';
const npm = fs.realpathSync(process.env.npm_execpath || execFileSync('which', ['npm'], { encoding: 'utf8' }).trim());

function command(file, args, options = {}) {
  return execFileSync(file, args, { cwd: root, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024, ...options });
}

async function smoke(cli, env, directory) {
  const help = command(cli, ['--help'], { env, cwd: directory });
  assert.match(help, /GoScanner/);
  let requests = 0;
  let requestError;
  const server = http.createServer((request, response) => {
    let body = '';
    request.on('data', (data) => { body += data; });
    request.on('end', () => {
      requests++;
      if (!body.includes('GetScannerElements')) requestError = 'Unexpected SOAP request';
      response.writeHead(500, { 'Content-Type': 'application/soap+xml' });
      response.end('loopback verification ends after AI preflight');
    });
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  try {
    const child = spawn(cli, ['scan', '--host', '127.0.0.1', '--port', String(server.address().port),
      '--deskew', '--autocrop', '--timeout', '10s', '-o', 'smoke.jpg'], { env, cwd: directory });
    let output = '';
    child.stdout.on('data', (data) => { output += data; });
    child.stderr.on('data', (data) => { output += data; });
    const code = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => { child.kill('SIGKILL'); reject(new Error('Isolated CLI timed out')); }, 30000);
      child.once('error', (error) => { clearTimeout(timer); reject(error); });
      child.once('exit', (status) => { clearTimeout(timer); resolve(status); });
    });
    assert.equal(code, 1, output);
    assert.equal(requests, 1, output);
    assert.equal(requestError, undefined);
    // scanPages constructs the processor and runs ONNX preflight before querying the scanner.
    assert.match(output, /检查能力并扫描/, output);
    assert.match(output, /500/, output);
    assert(!fs.existsSync(path.join(directory, 'smoke.jpg')), 'Failed scan must not publish an image');
    console.log('Isolated CLI help and packaged-model inference preflight passed.');
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
}

async function main() {
  assert.equal(process.platform, 'darwin');
  assert.equal(process.arch, 'arm64');
  checkRuntime(root);
  fs.mkdirSync(dist, { recursive: true });
  const json = JSON.parse(command(process.execPath, [npm, 'pack', '--json', '--pack-destination', dist]));
  const packed = Array.isArray(json) ? json[0] : Object.values(json)[0];
  for (const file of packed.files) {
    assert(!/\.(?:go|test\.cjs)$|^(?:testdata|scripts|\.build|verification)\//.test(file.path), `Unexpected packed file: ${file.path}`);
  }
  const archive = path.join(dist, packed.filename);
  const temporary = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'goscanner packed ')));
  let moved = false;
  try {
    const tools = path.join(temporary, 'tools');
    const home = path.join(temporary, 'home');
    fs.mkdirSync(tools);
    fs.mkdirSync(home);
    fs.symlinkSync(process.execPath, path.join(tools, 'node'));
    // Only Node is on PATH. No Go, compiler, Homebrew, model overrides or dynamic-library overrides are available.
    const env = { PATH: tools, HOME: home, TMPDIR: temporary, LANG: 'en_US.UTF-8',
      npm_config_cache: path.join(temporary, 'npm-cache'), npm_config_userconfig: path.join(home, '.npmrc'), npm_config_globalconfig: path.join(temporary, 'global.npmrc') };
    const destination = path.join(temporary, 'prefix');
    command(process.execPath, [npm, 'install', '--global', '--prefix', destination, '--ignore-scripts', '--offline', '--no-audit', '--no-fund', archive], { env, stdio: 'inherit' });
    const pkg = require('../package.json');
    const installed = path.join(destination, 'lib/node_modules', pkg.name);
    const cli = path.join(destination, 'bin/goscanner');
    checkRuntime(installed);
    assert.equal(JSON.parse(fs.readFileSync(path.join(installed, 'package.json'))).license, 'Apache-2.0');
    assert.deepEqual(fs.readFileSync(path.join(installed, 'LICENSE')), fs.readFileSync(path.join(root, 'LICENSE')));
    command('go', ['test', '-count=1', '-run', '^TestNpmBundleScan$', '-v', '.'], {
      stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '1', GOWORK: 'off',
        PKG_CONFIG_PATH: path.join(prefix, 'lib/pkgconfig'), GOSCANNER_TEST_CLI: cli },
    });
    assert(!fs.existsSync(hidden), `Restore or remove an interrupted verification directory first: ${hidden}`);
    fs.renameSync(prefix, hidden);
    moved = true;
    await smoke(cli, env, temporary);
    fs.writeFileSync(archive + '.sha256', crypto.createHash('sha256').update(fs.readFileSync(archive)).digest('hex') + '  ' + packed.filename + '\n');
    fs.writeFileSync(path.join(dist, 'verification.json'), JSON.stringify({
      package: pkg.name, version: pkg.version, platform: process.platform, arch: process.arch,
      testedKernel: os.release(), node: process.version, compressedBytes: packed.size, unpackedBytes: packed.unpackedSize,
      checks: ['runtime SHA256 inventory', 'Apache-2.0 metadata and complete project LICENSE', 'offline npm install with scripts disabled and Node-only PATH',
        'installed CLI ordinary and AI scans against loopback scanner', 'help and AI inference preflight with build libraries hidden'],
      limitations: ['No physical scanner test', 'Minimum macOS 14 compatibility needs a macOS 14 test host', 'No Developer ID signing or notarization'],
    }, null, 2) + '\n');
    console.log(`Verified npm artifact: ${archive} (${packed.size} compressed bytes)`);
  } finally {
    if (moved) fs.renameSync(hidden, prefix);
    fs.rmSync(temporary, { recursive: true, force: true });
  }
}

main().catch((error) => { console.error(error); process.exitCode = 1; });
