"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");

const { releasePolicyForVersion } = require("./check-release-version.js");
const {
  compareReleaseVersions,
  newestReleaseVersion,
  releaseChannelRelation,
} = require("./release-channel-policy.js");

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
  assert.match(workflow, /publish_tag="\$npm_tag"/);
  assert.match(workflow, /--tag "\$publish_tag"/);
  assert.doesNotMatch(workflow, /--tag beta/);
});

test("release channel ordering follows SemVer precedence", () => {
  assert.equal(compareReleaseVersions("0.1.0-beta.2", "0.1.0-beta.1"), 1);
  assert.equal(compareReleaseVersions("0.1.0", "0.1.0-beta.9"), 1);
  assert.equal(compareReleaseVersions("v1.2.3", "1.2.3"), 0);
  assert.equal(releaseChannelRelation("0.1.0-beta.1", "0.1.0-beta.2"), "older");
  assert.equal(releaseChannelRelation("0.1.0-beta.2", "0.1.0-beta.1"), "newer");
  assert.equal(releaseChannelRelation("0.1.0", ""), "missing");
  assert.equal(
    newestReleaseVersion(["v0.1.0", "v0.2.0-beta.3", "v0.1.1"]),
    "0.2.0-beta.3"
  );
});

test("release reruns preserve newer npm tags and GitHub Latest", () => {
  assert.match(
    workflow,
    /\n  publish:\n[\s\S]*?\n    concurrency:\n      group: release-channel-publication\n      cancel-in-progress: false/
  );
  assert.match(workflow, /release-channel-policy\.js --relation/);
  assert.match(workflow, /release-channel-policy\.js --max/);
  assert.match(
    workflow,
    /case "\$tag_relation" in[\s\S]*newer\|missing\)[\s\S]*npm dist-tag add/
  );
  assert.match(
    workflow,
    /if \[ "\$tag_relation" = "older" \]; then[\s\S]*publish_tag="release-/
  );
  assert.match(
    workflow,
    /case "\$latest_relation" in[\s\S]*older\)[\s\S]*latest_flag="--latest=false"/
  );
});
