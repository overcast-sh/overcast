// Overcast Lambda Web Functions bootstrap.
//
// The Docker provider (provider_docker.go) runs this as the container's
// entrypoint in place of the Lambda init: there is no Runtime API, the user's
// package is a plain Node.js HTTP server. The file does four things and has no
// dependencies, so it works on any Lambda nodejs base image:
//
//   1. turns console output into the application-log JSON shape, filtered by
//      the revision's applicationLogLevel, with the request id of the request
//      that produced it;
//   2. imports the package's entry file (CJS or ESM);
//   3. reports a failed import with a marker line and a distinctive exit code
//      the provider maps to an init error type;
//   4. forwards SIGTERM so the application's own handler runs.
//
// Private contract with the provider: OVERCAST_WEB_APP_LOG_LEVEL carries the
// log level; exit codes 87 and 88 below.

import { AsyncLocalStorage } from 'node:async_hooks';
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import util from 'node:util';

// The entry file could not be loaded: missing file or dependency, syntax error.
const EXIT_IMPORT_ERROR = 87;
// The entry file loaded but threw while it ran.
const EXIT_INIT_ERROR = 88;

const IMPORT_MARKER = 'OVERCAST_WEB_IMPORT_ERROR';
const INIT_MARKER = 'OVERCAST_WEB_INIT_ERROR';

// Node has no FATAL; a FATAL threshold admits what ERROR does.
const LEVELS = { TRACE: 0, DEBUG: 1, INFO: 2, WARN: 3, ERROR: 4, FATAL: 4 };

const env = process.env;
const threshold = LEVELS[String(env.OVERCAST_WEB_APP_LOG_LEVEL || 'INFO').toUpperCase()] ?? LEVELS.INFO;
const region = env.AWS_REGION || '';

const requestContext = new AsyncLocalStorage();

// The streams' own write methods. Everything this file prints goes through
// them, so the wrappers installed below never see (and never re-wrap) it.
const writeStdout = process.stdout.write.bind(process.stdout);
const writeStderr = process.stderr.write.bind(process.stderr);

function stringify(record) {
  try {
    return JSON.stringify(record);
  } catch {
    // BigInt, cycles through toJSON, and the like: fall back to the inspected form.
    record.message = util.inspect(record.message, { depth: 4, breakLength: Infinity });
    return JSON.stringify(record);
  }
}

// emit writes one application-log line. fd is 1 (stdout) or 2 (stderr).
function emit(level, message, fd) {
  const record = { timestamp: new Date().toISOString(), level, message };
  const ctx = requestContext.getStore();
  if (ctx) {
    if (ctx.requestId) record.requestId = ctx.requestId;
    if (ctx.endpointName) record.endpointName = ctx.endpointName;
  }
  if (region) record.region = region;
  (fd === 2 ? writeStderr : writeStdout)(stringify(record) + '\n');
}

function isPlainObject(value) {
  if (value === null || typeof value !== 'object') return false;
  if (Array.isArray(value)) return true;
  const proto = Object.getPrototypeOf(value);
  return proto === Object.prototype || proto === null;
}

// consoleMessage is the "message" of a console call: a lone string or plain
// object is kept as it is (so an object stays an object in the JSON), anything
// else is formatted the way console would have printed it.
function consoleMessage(args) {
  if (args.length === 1) {
    const only = args[0];
    if (typeof only === 'string' || isPlainObject(only)) return only;
  }
  return util.format(...args);
}

const consoleLevels = {
  log: ['INFO', 1],
  info: ['INFO', 1],
  debug: ['DEBUG', 1],
  warn: ['WARN', 2],
  error: ['ERROR', 2],
  trace: ['TRACE', 2],
};

for (const [method, [level, fd]] of Object.entries(consoleLevels)) {
  const rank = LEVELS[level];
  console[method] = (...args) => {
    if (rank < threshold) return;
    let message = consoleMessage(args);
    if (method === 'trace') {
      const where = new Error().stack.split('\n').slice(2).join('\n');
      message = (typeof message === 'string' ? message : util.format(message)) + '\n' + where;
    }
    emit(level, message, fd);
  };
}

// Writes that are not console calls (process.stdout.write, a logging library
// that owns its stream) are wrapped line by line: stdout is INFO, stderr ERROR.
function wrapStream(stream, level, fd) {
  const rank = LEVELS[level];
  stream.write = function write(chunk, encoding, callback) {
    if (typeof encoding === 'function') {
      callback = encoding;
      encoding = undefined;
    }
    if (rank >= threshold) {
      const text = typeof chunk === 'string'
        ? chunk
        : Buffer.from(chunk.buffer ?? chunk, chunk.byteOffset, chunk.byteLength).toString();
      for (const line of text.split('\n')) {
        const trimmed = line.endsWith('\r') ? line.slice(0, -1) : line;
        if (trimmed.length > 0) emit(level, trimmed, fd);
      }
    }
    if (typeof callback === 'function') process.nextTick(callback);
    return true;
  };
}
wrapStream(process.stdout, 'INFO', 1);
wrapStream(process.stderr, 'ERROR', 2);

// Every request handler runs inside the context of the request it serves, so a
// console call anywhere below it - however many awaits down - carries the id.
const baseEmit = http.Server.prototype.emit;
http.Server.prototype.emit = function emitWithRequestContext(type, ...args) {
  if (type === 'request' && args[0] && args[0].headers) {
    const headers = args[0].headers;
    const ctx = {
      requestId: headers['lambda-web-request-id'],
      endpointName: headers['lambda-web-endpoint-name'],
    };
    return requestContext.run(ctx, () => baseEmit.call(this, type, ...args));
  }
  return baseEmit.call(this, type, ...args);
};

// SIGTERM: if the application installed a handler of its own it owns the
// shutdown (the platform kills it after the grace period); if not, exit 0.
for (const signal of ['SIGTERM', 'SIGINT']) {
  process.on(signal, () => {
    if (process.listenerCount(signal) > 1) return;
    process.exit(0);
  });
}

const IMPORT_ERROR_CODES = new Set([
  'ERR_MODULE_NOT_FOUND',
  'MODULE_NOT_FOUND',
  'ERR_REQUIRE_ESM',
  'ERR_REQUIRE_ASYNC_MODULE',
  'ERR_UNKNOWN_FILE_EXTENSION',
  'ERR_PACKAGE_PATH_NOT_EXPORTED',
  'ERR_PACKAGE_IMPORT_NOT_DEFINED',
  'ERR_INVALID_PACKAGE_CONFIG',
  'ERR_INVALID_PACKAGE_TARGET',
  'ERR_INVALID_MODULE_SPECIFIER',
  'ERR_UNSUPPORTED_DIR_IMPORT',
]);

function failImport(error) {
  const err = error instanceof Error ? error : new Error(String(error));
  const isImportError = err.name === 'SyntaxError' || IMPORT_ERROR_CODES.has(err.code);
  const marker = isImportError ? IMPORT_MARKER : INIT_MARKER;
  const first = String(err.message || err).split('\n')[0];
  // The marker is one raw line, written before anything else and not as JSON:
  // it is what the provider looks for in the container's output.
  fs.writeSync(2, `${marker}: ${err.name}: ${first}\n`);
  emit('ERROR', err.stack || String(err), 2);
  process.exit(isImportError ? EXIT_IMPORT_ERROR : EXIT_INIT_ERROR);
}

const taskRoot = env.LAMBDA_TASK_ROOT || '/var/task';
const entry = env.AWS_LAMBDA_NODEJS_ENTRYPOINT || 'index.js';

try {
  await import(pathToFileURL(path.resolve(taskRoot, entry)).href);
} catch (error) {
  failImport(error);
}
