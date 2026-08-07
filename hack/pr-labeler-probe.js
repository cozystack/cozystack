// Evaluates the github-script body of .github/workflows/pr-labeler.yaml and
// reports what it would actually do, as JSON on stdout:
//
//   {"labels": [...], "scopeToArea": {...}, "typeToKind": {...}}
//
// `labels` is the union of everything handed to issues.addLabels across a title
// per (type, scope) pair plus the breaking-change and backport paths, so it is
// the set the labeler can apply rather than the set spelled out as literals.
//
// This exists because a pattern over the workflow source answers "is this
// spelling present", and the same break spelled differently walks past it: a
// double-quoted key, a space before the colon, a value built by concatenation,
// a key repeated lower down that wins at runtime, an object parked where
// nothing consults it. Each of those was demonstrated against a regex-based
// version of the tests. Asking the engine that runs in CI removes the class
// rather than the instances.
//
// Usage: node hack/pr-labeler-probe.js <extracted-script.js>

'use strict';

const fs = require('fs');
const vm = require('vm');

const src = fs.readFileSync(process.argv[2], 'utf8');
const applied = new Set();

// The step is a bare script that github-script wraps in an async function, so
// it is run the same way here. The epilogue publishes the lookup tables; it is
// appended rather than parsed out, so no assumption about their spelling or
// position survives into this file.
const wrapped = '(async () => {' + src + '\n;globalThis.__tables = {scopeToArea, typeToKind};})()';

async function run(title, body, existing) {
  const ctx = {
    context: {
      payload: {
        pull_request: {
          title: title,
          body: body || '',
          labels: (existing || []).map(function (n) { return {name: n}; }),
          number: 1,
        },
      },
      repo: {owner: 'owner', repo: 'repo'},
    },
    core: {warning: function () {}, info: function () {}, notice: function () {}, setFailed: function () {}},
    github: {
      rest: {
        issues: {
          addLabels: async function (args) {
            (args.labels || []).forEach(function (l) { applied.add(l); });
          },
        },
      },
    },
    console: {log: function () {}, error: function () {}},
  };
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  await vm.runInContext(wrapped, ctx);
  return ctx.__tables;
}

async function main() {
  const tables = await run('fix(api): probe');
  const scopes = Object.keys(tables.scopeToArea);
  const types = Object.keys(tables.typeToKind);

  for (const t of types) {
    await run(t + ': probe');
    for (const s of scopes) {
      await run(t + '(' + s + '): probe');
    }
  }
  await run('fix(api)!: probe');
  await run('fix(api): probe', 'BREAKING CHANGE: probe');
  await run('[Backport release-1.0] fix(api): probe');
  // A scope that is deliberately absent, so the fallback label is reachable too.
  await run('fix(no-such-scope-probe): probe');

  process.stdout.write(JSON.stringify({
    labels: Array.from(applied).sort(),
    scopeToArea: tables.scopeToArea,
    typeToKind: tables.typeToKind,
  }, null, 1) + '\n');
}

main().catch(function (e) {
  process.stderr.write('probe failed: ' + (e && e.stack ? e.stack : e) + '\n');
  process.exit(1);
});
