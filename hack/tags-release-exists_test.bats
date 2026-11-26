#!/usr/bin/env bats

load test_helper

@test "release checks resume prerelease drafts and preserve promoted stable drafts" {
  for job in build-amd64 build-arm64; do
    local script
    script=$(yq --exit-status --unwrapScalar ".jobs.\"$job\".steps[] | select(.id == \"check_release\") | .with.script" "$BATS_TEST_DIRNAME/../.github/workflows/tags.yaml")
    node - "$job" "$script" <<'JS'
const assert = require('node:assert/strict');
const [job, script] = process.argv.slice(2);
const AsyncFunction = (async () => {}).constructor;
const check = new AsyncFunction('github', 'context', 'core', script);
async function verify(tag, drafts, expected) {
  const releases = drafts.map(draft => ({tag_name: tag, draft}));
  releases.push({tag_name: 'v0.1.0', draft: true});
  const github = {rest: {repos: {}}, paginate: async () => releases};
  const outputs = {};
  await check(github, {ref: 'refs/tags/' + tag, repo: {}}, {
    setOutput: (key, value) => { outputs[key] = value; }
  });
  assert.equal(outputs.release_exists, expected, JSON.stringify({job, tag, drafts}));
}
(async () => {
  for (const kind of ['rc', 'beta', 'alpha']) {
    await verify('v1.7.0-' + kind + '.1', [true], false);
  }
  await verify('v1.7.0-rc.1', [false], true);
  await verify('v1.7.0-rc.1', [], false);
  await verify('v1.7.0-rc.1', [true, false], true);
  if (job === 'build-amd64') {
    await verify('v1.7.0', [true], true);
    await verify('v1.7.0', [false], true);
    await verify('v1.7.0', [], false);
  }
})();
JS
  done
}
