'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

function checkRuntime(root) {
  const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json')));
  assert.deepEqual(pkg.os, ['darwin']);
  assert.deepEqual(pkg.cpu, ['arm64']);
  for (const hook of ['preinstall', 'install', 'postinstall', 'prepare']) {
    assert.equal(pkg.scripts?.[hook], undefined, `Package must not have a ${hook} hook`);
  }
  const directory = path.join(root, 'runtime');
  const manifest = JSON.parse(fs.readFileSync(path.join(directory, 'manifest.json')));
  const inputs = JSON.parse(fs.readFileSync(path.join(root, 'npm', 'dependencies.json')));
  assert.equal(manifest.platform, 'darwin');
  assert.equal(manifest.arch, 'arm64');
  assert.equal(manifest.minimumMacOS, inputs.minimumMacOS);
  assert.deepEqual(manifest.inputs, inputs);
  const entries = [];
  function walk(folder) {
    for (const entry of fs.readdirSync(path.join(directory, folder), { withFileTypes: true })) {
      const relative = path.posix.join(folder, entry.name);
      assert(!entry.isSymbolicLink(), `Symlink in runtime: ${relative}`);
      if (entry.isDirectory()) walk(relative);
      else {
        assert(entry.isFile(), `Not a regular file: ${relative}`);
        if (relative !== 'manifest.json') entries.push(relative);
      }
    }
  }
  walk('');
  assert.deepEqual(entries.sort(), Object.keys(manifest.files).sort(), 'Runtime inventory differs from manifest');
  for (const file of entries) {
    const hash = crypto.createHash('sha256').update(fs.readFileSync(path.join(directory, file))).digest('hex');
    assert.equal(hash, manifest.files[file], `Runtime SHA256 mismatch: ${file}`);
  }
  for (const file of ['bin/goscanner', `lib/${inputs.onnxruntime.library}`,
    `models/${inputs.model.filename}`, 'licenses/DocAligner-LICENSE', 'licenses/OpenCV-LICENSE',
    'licenses/ONNXRuntime-LICENSE', 'licenses/ONNXRuntime-ThirdPartyNotices.txt', 'licenses/Go-LICENSE']) {
    assert(Object.hasOwn(manifest.files, file), `Required runtime file missing: ${file}`);
  }
  assert(entries.some((name) => name.startsWith('lib/libopencv_core.')), 'OpenCV is missing');
  assert.equal(manifest.files[`models/${inputs.model.filename}`], inputs.model.sha256);
  assert.equal(manifest.files['licenses/DocAligner-LICENSE'], inputs.modelLicense.sha256);
  assert(fs.statSync(path.join(directory, 'bin', 'goscanner')).mode & 0o111, 'CLI is not executable');
  return manifest;
}

if (require.main === module) {
  try {
    checkRuntime(path.resolve(__dirname, '..'));
    console.error('Precompiled runtime inventory and SHA256 checks passed.');
  } catch (error) {
    console.error(`Runtime check failed: ${error.message}. Build first with python3 scripts/build-npm.py.`);
    process.exitCode = 1;
  }
}
module.exports = { checkRuntime };
