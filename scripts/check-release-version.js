#!/usr/bin/env node

"use strict";

const fs = require("node:fs");
const path = require("node:path");

const root = path.resolve(__dirname, "..");

function fail(message) {
  throw new Error(`[release-version] ${message}`);
}

function releasePolicyForVersion(npmVersion) {
  const prerelease = npmVersion.includes("-");
  return {
    prerelease,
    npmTag: prerelease ? "beta" : "latest",
  };
}

function readReleaseVersion() {
  const packageJSON = JSON.parse(
    fs.readFileSync(path.join(root, "npm", "package.json"), "utf8")
  );
  const makefile = fs.readFileSync(path.join(root, "Makefile"), "utf8");
  const makeMatch = makefile.match(/^VERSION\s*:?=\s*(v[^\s#]+)\s*$/m);
  if (!makeMatch) {
    fail("Makefile VERSION is missing or malformed");
  }

  const npmVersion = String(packageJSON.version || "").trim();
  if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(npmVersion)) {
    fail(`npm/package.json has an invalid version: ${npmVersion || "<empty>"}`);
  }
  const tag = `v${npmVersion}`;
  if (makeMatch[1] !== tag) {
    fail(`Makefile uses ${makeMatch[1]} but npm/package.json requires ${tag}`);
  }

  const notes = path.join(root, "changelogs", `${tag}.md`);
  if (!fs.existsSync(notes)) {
    fail(`release notes are missing: changelogs/${tag}.md`);
  }
  return {
    npmVersion,
    tag,
    notes,
    ...releasePolicyForVersion(npmVersion),
  };
}

function main() {
  const release = readReleaseVersion();
  // Callers that must bind metadata to a release tag pass it explicitly.
  // GITHUB_REF_NAME is a branch or values such as "3/merge" on pull requests,
  // so treating it as an implicit tag would make ordinary CI fail.
  const expectedTag = String(process.argv[2] || "").trim();
  if (expectedTag && expectedTag !== release.tag) {
    fail(`release tag ${expectedTag} does not match ${release.tag}`);
  }

  const format = String(process.argv[3] || "").trim();
  if (format === "--github-output") {
    process.stdout.write([
      `tag=${release.tag}`,
      `prerelease=${release.prerelease}`,
      `npm_tag=${release.npmTag}`,
      "",
    ].join("\n"));
    return;
  }
  if (format) {
    fail(`unknown output format: ${format}`);
  }
  process.stdout.write(`${release.tag}\n`);
}

if (require.main === module) {
  try {
    main();
  } catch (err) {
    console.error(err.message);
    process.exit(1);
  }
}

module.exports = { readReleaseVersion, releasePolicyForVersion };
