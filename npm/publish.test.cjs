'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { test } = require('node:test');
const { releaseVersion, checkArtifact, registryVersion, requireNewVersion, checkPublished } = require('../scripts/publish-npm.cjs');
const pkg = require('../package.json');

function artifactFixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'goscanner release '));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const directory = path.join(root, 'dist');
  const payload = path.join(root, 'payload');
  const packed = path.join(payload, 'package');
  for (const folder of [directory, path.join(root, 'runtime'), path.join(packed, 'runtime')]) fs.mkdirSync(folder, { recursive: true });
  const packageFile = path.join(packed, 'package.json');
  fs.writeFileSync(packageFile, JSON.stringify(pkg));
  fs.copyFileSync(path.join(__dirname, '..', 'LICENSE'), path.join(packed, 'LICENSE'));
  for (const file of [path.join(root, 'runtime/manifest.json'), path.join(packed, 'runtime/manifest.json')]) fs.writeFileSync(file, '{}\n');
  const verificationFile = path.join(directory, 'verification.json');
  fs.writeFileSync(verificationFile, JSON.stringify({ package: pkg.name, version: pkg.version, platform: 'darwin', arch: 'arm64', checks: [
    'runtime SHA256 inventory', 'Apache-2.0 metadata and complete project LICENSE',
    'offline npm install with scripts disabled and Node-only PATH',
    'installed CLI ordinary and AI scans against loopback scanner',
    'help and AI inference preflight with build libraries hidden',
  ] }));
  const archive = path.join(directory, `codingbone-goscanner-${pkg.version}.tgz`);
  const rebuild = () => {
    execFileSync('tar', ['-czf', archive, '-C', payload, 'package']);
    fs.writeFileSync(archive + '.sha256', crypto.createHash('sha256').update(fs.readFileSync(archive)).digest('hex') + '  ' + path.basename(archive) + '\n');
  };
  rebuild();
  return { directory, archive, packageFile, verificationFile, rebuild };
}

test('release requires a matching pushed tag in this repository, without npm tokens', () => {
  const env = { GITHUB_ACTIONS: 'true', GITHUB_REPOSITORY: 'johnbitcn/GoScanner', GITHUB_EVENT_NAME: 'push', GITHUB_REF: `refs/tags/v${pkg.version}` };
  assert.equal(releaseVersion(env), pkg.version);
  for (const change of [{ GITHUB_ACTIONS: '' }, { GITHUB_REPOSITORY: 'someone/fork' },
    { GITHUB_EVENT_NAME: 'pull_request' }, { GITHUB_EVENT_NAME: 'workflow_dispatch' },
    { GITHUB_REF: 'refs/heads/main' }, { GITHUB_REF: 'refs/tags/v999.0.0' },
    { NPM_TOKEN: 'test-only' }, { NODE_AUTH_TOKEN: 'test-only' }]) {
    assert.throws(() => releaseVersion({ ...env, ...change }));
  }
});

test('release accepts the original checksummed artifact and rejects corruption or absent acceptance checks', (t) => {
  const fixture = artifactFixture(t);
  const artifact = checkArtifact(fixture.directory, pkg.version);
  assert.equal(artifact.archive, fixture.archive);
  assert.match(artifact.integrity, /^sha512-/);
  fs.appendFileSync(fixture.archive, 'corrupt');
  assert.throws(() => checkArtifact(fixture.directory, pkg.version), /SHA256/);
  fixture.rebuild();
  const verification = JSON.parse(fs.readFileSync(fixture.verificationFile));
  verification.checks.pop();
  fs.writeFileSync(fixture.verificationFile, JSON.stringify(verification));
  assert.throws(() => checkArtifact(fixture.directory, pkg.version), /Missing build acceptance/);
});

test('release rejects wrong versions, repositories, platforms, licenses and install hooks', (t) => {
  const fixture = artifactFixture(t);
  for (const change of [{ version: '999.0.0' }, { repository: { url: 'git+https://github.com/someone/fork.git' } },
    { os: ['linux'] }, { cpu: ['x64'] }, { license: 'MIT' }, { scripts: { install: 'echo unexpected' } }]) {
    fs.writeFileSync(fixture.packageFile, JSON.stringify({ ...pkg, ...change }));
    fixture.rebuild();
    assert.throws(() => checkArtifact(fixture.directory, pkg.version));
  }
});

test('registry lookup permits only an explicit 404 as an unpublished version', async (t) => {
  let status = 404;
  let body = { name: pkg.name, version: pkg.version };
  t.mock.method(globalThis, 'fetch', async (url) => {
    assert(String(url).startsWith('https://registry.npmjs.org/'));
    return new Response(JSON.stringify(body), { status });
  });
  assert.equal(await registryVersion(pkg.version), null);
  await requireNewVersion(pkg.version);
  status = 200;
  await assert.rejects(requireNewVersion(pkg.version), /already exists/);
  for (status of [401, 403, 429, 500]) await assert.rejects(requireNewVersion(pkg.version), /Registry lookup failed/);
  status = 200;
  for (body of [null, {}, { name: pkg.name, version: '999.0.0' }]) await assert.rejects(requireNewVersion(pkg.version), /unexpected package metadata/);
});

test('publication must match the exact version and original tarball integrity', () => {
  const artifact = { integrity: 'sha512-test-only', shasum: 'test-only' };
  const metadata = { name: pkg.name, version: pkg.version, dist: { ...artifact } };
  checkPublished(metadata, pkg.version, artifact);
  for (const change of [{ name: 'other' }, { version: '999.0.0' }, { dist: { integrity: 'wrong', shasum: artifact.shasum } },
    { dist: { integrity: artifact.integrity, shasum: 'wrong' } }]) {
    assert.throws(() => checkPublished({ ...metadata, ...change }, pkg.version, artifact));
  }
});
