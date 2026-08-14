"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");

const { releasePolicyForVersion } = require("./check-release-version.js");

const workflow = fs.readFileSync(
  path.join(__dirname, "..", ".github", "workflows", "release.yml"),
  "utf8"
);

test("release policy selects GitHub and npm channels from the version", () => {
  assert.deepEqual(releasePolicyForVersion("0.1.0-beta.1"), {
    prerelease: true,
    npmTag: "beta",
  });
  assert.deepEqual(releasePolicyForVersion("0.1.0"), {
    prerelease: false,
    npmTag: "latest",
  });
});

test("release reruns reuse immutable published assets", () => {
  assert.doesNotMatch(workflow, /gh release upload/);
  assert.doesNotMatch(workflow, /--clobber/);
  assert.match(workflow, /gh release download "\$tag"/);
  assert.match(workflow, /check-release-assets\.js "\$published_dir"/);
});

test("release workflow applies the derived channels", () => {
  assert.match(workflow, /id: release_policy/);
  assert.match(workflow, /--github-output/);
  assert.match(workflow, /--prerelease="\$prerelease"/);
  assert.match(workflow, /--tag "\$npm_tag"/);
  assert.doesNotMatch(workflow, /--tag beta/);
});
