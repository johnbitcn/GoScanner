#!/usr/bin/env node
'use strict';

const path = require('node:path');
const { spawn } = require('node:child_process');
const { constants } = require('node:os');

const runtime = path.resolve(__dirname, '..', 'runtime');
const { model, onnxruntime } = require('./dependencies.json');
const binary = path.join(runtime, 'bin', 'goscanner');
const child = spawn(binary, process.argv.slice(2), {
  stdio: 'inherit',
  env: {
    ...process.env,
    GOSCANNER_MODEL_PATH: process.env.GOSCANNER_MODEL_PATH || path.join(runtime, 'models', model.filename),
    GOSCANNER_ONNXRUNTIME_PATH: process.env.GOSCANNER_ONNXRUNTIME_PATH || path.join(runtime, 'lib', onnxruntime.library),
  },
});
const handlers = new Map();
for (const signal of ['SIGINT', 'SIGTERM']) {
  const handler = () => child.kill(signal);
  handlers.set(signal, handler);
  process.on(signal, handler);
}
function cleanup() {
  for (const [signal, handler] of handlers) process.off(signal, handler);
}
// The binary cannot select a language when startup itself fails.
function startupUsesChinese() {
  const args = process.argv.slice(2);
  if (args[0] === '--agent') return false;
  let language = 'auto';
  const valueOptions = new Set(['--host', '--protocol', '--scheme', '--port', '--path', '--endpoint', '--dpi', '--mode', '--area', '-o', '--timeout']);
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (['--', '-h', '--help', 'help'].includes(arg)) break;
    if (arg === '--lang' || arg.startsWith('--lang=')) {
      const value = arg === '--lang' ? args[++i] : arg.slice('--lang='.length);
      if (!['auto', 'en', 'zh-CN'].includes(value)) break;
      language = value;
    } else if (valueOptions.has(arg)) {
      i++;
    }
  }
  if (language !== 'auto') return language === 'zh-CN';
  const locale = [process.env.LC_ALL, process.env.LC_MESSAGES, process.env.LANG].find(Boolean) || '';
  const normalized = locale.toLowerCase().replaceAll('_', '-').split(/[.@]/, 1)[0];
  return ['zh', 'zh-cn', 'zh-sg', 'zh-hans'].includes(normalized) || normalized.startsWith('zh-hans-');
}

child.on('error', (error) => {
  cleanup();
  const original = JSON.stringify(error.message);
  console.error(startupUsesChinese()
    ? `无法启动 GoScanner。外部错误：${original}。请重新安装完整的 macOS arm64 npm 包；无需编译或安装原生依赖。`
    : `Cannot start GoScanner. External error: ${original}. Reinstall the complete macOS arm64 npm package; no compilation or native dependencies are required.`);
  process.exitCode = 1;
});
child.on('exit', (code, signal) => {
  cleanup();
  process.exitCode = code ?? (128 + constants.signals[signal]);
});
