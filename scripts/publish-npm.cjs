'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const timers = require('node:timers/promises');
const pkg = require('../package.json');

const registry = 'https://registry.npmjs.org/';

function releaseVersion(env) {
  assert.equal(env.GITHUB_ACTIONS, 'true', 'Publishing is only supported in GitHub Actions');
  assert.equal(env.GITHUB_REPOSITORY, 'johnbitcn/GoScanner', 'Unexpected source repository');
  assert.equal(env.GITHUB_EVENT_NAME, 'push', 'Only pushed version tags may publish');
  assert.match(pkg.version, /^\d+\.\d+\.\d+$/, 'Use a stable release version');
  assert.equal(env.GITHUB_REF, `refs/tags/v${pkg.version}`, 'Tag must match package.json version');
  assert(!env.NPM_TOKEN && !env.NODE_AUTH_TOKEN, 'Use OIDC, not npm tokens');
  return pkg.version;
}

function checkArtifact(directory, version) {
  const archive = path.resolve(directory, `codingbone-goscanner-${version}.tgz`);
  assert(fs.lstatSync(archive).isFile(), 'Tarball must be a regular file');
  const bytes = fs.readFileSync(archive);
  const checksum = fs.readFileSync(archive + '.sha256', 'utf8').trim().split(/\s+/);
  assert.deepEqual(checksum, [crypto.createHash('sha256').update(bytes).digest('hex'), path.basename(archive)], 'Tarball SHA256 mismatch');
  const read = (name) => execFileSync('tar', ['-xOzf', archive, `package/${name}`], { maxBuffer: 4 * 1024 * 1024 });
  const packed = JSON.parse(read('package.json'));
  assert.equal(packed.name, '@codingbone/goscanner');
  assert.equal(packed.version, version);
  assert.equal(packed.version, pkg.version);
  assert.equal(packed.license, 'Apache-2.0');
  assert.equal(packed.repository.url, 'git+https://github.com/johnbitcn/GoScanner.git');
  assert.deepEqual(packed.os, ['darwin']);
  assert.deepEqual(packed.cpu, ['arm64']);
  for (const hook of ['preinstall', 'install', 'postinstall']) {
    assert(!Object.hasOwn(packed.scripts || {}, hook), `Unexpected install hook: ${hook}`);
  }
  assert.deepEqual(read('LICENSE'), fs.readFileSync(path.join(__dirname, '..', 'LICENSE')));
  assert.deepEqual(read('runtime/manifest.json'), fs.readFileSync(path.join(directory, '..', 'runtime/manifest.json')));
  const verification = JSON.parse(fs.readFileSync(path.join(directory, 'verification.json')));
  assert.equal(verification.package, packed.name);
  assert.equal(verification.version, version);
  assert.equal(verification.platform, 'darwin');
  assert.equal(verification.arch, 'arm64');
  for (const check of ['runtime SHA256 inventory', 'Apache-2.0 metadata and complete project LICENSE',
    'offline npm install with scripts disabled and Node-only PATH',
    'installed CLI ordinary and AI scans against loopback scanner',
    'help and AI inference preflight with build libraries hidden']) {
    assert(verification.checks.includes(check), `Missing build acceptance check: ${check}`);
  }
  return { archive, integrity: 'sha512-' + crypto.createHash('sha512').update(bytes).digest('base64'),
    shasum: crypto.createHash('sha1').update(bytes).digest('hex') };
}

async function registryVersion(version) {
  const response = await fetch(`${registry}${encodeURIComponent('@codingbone/goscanner')}/${version}`, { cache: 'no-store', signal: AbortSignal.timeout(30000) });
  if (response.status === 404) return null;
  assert.equal(response.status, 200, `Registry lookup failed: HTTP ${response.status}`);
  const metadata = await response.json();
  assert(metadata && metadata.name === '@codingbone/goscanner' && metadata.version === version, 'Registry returned unexpected package metadata');
  return metadata;
}

async function requireNewVersion(version) {
  assert.equal(await registryVersion(version), null, `Version ${version} already exists; never overwrite or retry a publication`);
}

function checkPublished(metadata, version, artifact) {
  assert.equal(metadata.name, '@codingbone/goscanner');
  assert.equal(metadata.version, version);
  assert.equal(metadata.dist.integrity, artifact.integrity, 'Published package integrity differs from the CI tarball');
  assert.equal(metadata.dist.shasum, artifact.shasum, 'Published package SHA1 differs from the CI tarball');
}

async function verifyPublication(version, artifact) {
  // Registry propagation can lag an accepted upload; retry reads, never npm publish.
  for (let attempt = 0; attempt < 30; attempt++) {
    const metadata = await registryVersion(version);
    if (metadata) {
      checkPublished(metadata, version, artifact);
      const response = await fetch(`${registry}${encodeURIComponent('@codingbone/goscanner')}`, { cache: 'no-store', signal: AbortSignal.timeout(30000) });
      assert.equal(response.status, 200, 'Cannot verify dist-tags');
      if ((await response.json())['dist-tags']?.latest === version) return;
    }
    if (attempt < 29) await timers.setTimeout(10000);
  }
  throw new Error('Registry version or latest is not visible after 30 checks; the package may already be published. Inspect before retrying; do not republish.');
}

async function main() {
  const version = releaseVersion(process.env);
  if (process.argv[2] === '--check-tag') {
    execFileSync('git', ['merge-base', '--is-ancestor', process.env.GITHUB_SHA, 'origin/main']);
    await requireNewVersion(version);
    console.log(`Release tag v${version} is on main and is not published.`);
    return;
  }
  assert.equal(process.argv.length, 3, 'Usage: node scripts/publish-npm.cjs <artifact dist directory>');
  assert(process.env.ACTIONS_ID_TOKEN_REQUEST_URL && process.env.ACTIONS_ID_TOKEN_REQUEST_TOKEN, 'The publish job requires id-token: write');
  const artifact = checkArtifact(process.argv[2], version);
  await requireNewVersion(version);
  // Publish the verified tarball exactly once; do not rebuild it on the Linux publisher.
  execFileSync('npm', ['publish', artifact.archive, '--access', 'public', '--provenance', `--registry=${registry}`],
    { cwd: path.dirname(artifact.archive), stdio: 'inherit' });
  console.log('Upload completed. Waiting for registry version and latest visibility; retrying reads only.');
  await verifyPublication(version, artifact);
  console.log(`Verified npm publication: @codingbone/goscanner@${version}; original CI integrity and latest match.`);
}

if (require.main === module) main().catch((error) => { console.error(error.message); process.exitCode = 1; });
module.exports = { releaseVersion, checkArtifact, registryVersion, requireNewVersion, checkPublished, verifyPublication };
