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
child.on('error', (error) => {
  cleanup();
  console.error(`Cannot start GoScanner. External error: ${JSON.stringify(error.message)}. Reinstall the complete macOS arm64 npm package; no compilation or native dependencies are required.`);
  process.exitCode = 1;
});
child.on('exit', (code, signal) => {
  cleanup();
  process.exitCode = code ?? (128 + constants.signals[signal]);
});
