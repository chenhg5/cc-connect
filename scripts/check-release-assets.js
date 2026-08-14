#!/usr/bin/env node

"use strict";

const crypto = require("node:crypto");
const { execFileSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const { parseChecksumManifest } = require("../npm/install.js");
const { tag } = require("./check-release-version.js").readReleaseVersion();

const name = "cc-connect-next";
const platforms = [
  ["linux", "amd64", ".tar.gz", ""],
  ["linux", "arm64", ".tar.gz", ""],
  ["darwin", "amd64", ".tar.gz", ""],
  ["darwin", "arm64", ".tar.gz", ""],
  ["windows", "amd64", ".zip", ".exe"],
  ["windows", "arm64", ".zip", ".exe"],
];

function fail(message) {
  throw new Error(`[release-assets] ${message}`);
}

function archiveEntries(file, extension) {
  const output = extension === ".zip"
    ? execFileSync("unzip", ["-Z1", file], { encoding: "utf8" })
    : execFileSync("tar", ["tzf", file], { encoding: "utf8" });
  return output.split(/\r?\n/).filter(Boolean);
}

function main() {
  const dist = path.resolve(process.argv[2] || path.join(__dirname, "..", "dist"));
  const manifestPath = path.join(dist, "checksums.txt");
  const manifest = fs.readFileSync(manifestPath, "utf8");
  const nonEmptyLines = manifest.split(/\r?\n/).filter(Boolean);
  if (nonEmptyLines.length !== platforms.length) {
    fail(`checksums.txt has ${nonEmptyLines.length} entries, want ${platforms.length}`);
  }

  const expectedArchives = [];
  for (const [platform, arch, extension, binaryExtension] of platforms) {
    const archive = `${name}-${tag}-${platform}-${arch}${extension}`;
    const binary = `${name}-${tag}-${platform}-${arch}${binaryExtension}`;
    const archivePath = path.join(dist, archive);
    expectedArchives.push(archive);
    if (!fs.statSync(archivePath).isFile()) {
      fail(`${archive} is not a regular file`);
    }

    const expectedHash = parseChecksumManifest(manifest, archive);
    const actualHash = crypto.createHash("sha256").update(fs.readFileSync(archivePath)).digest("hex");
    if (actualHash !== expectedHash) {
      fail(`${archive} does not match checksums.txt`);
    }

    const entries = archiveEntries(archivePath, extension);
    if (entries.length !== 1 || entries[0] !== binary) {
      fail(`${archive} must contain only ${binary}; found ${JSON.stringify(entries)}`);
    }
  }

  const actualArchives = fs.readdirSync(dist)
    .filter((file) => file.endsWith(".tar.gz") || file.endsWith(".zip"))
    .sort();
  const expectedSorted = expectedArchives.sort();
  if (JSON.stringify(actualArchives) !== JSON.stringify(expectedSorted)) {
    fail(`archive set differs: found ${JSON.stringify(actualArchives)}`);
  }

  process.stdout.write(`[release-assets] verified ${platforms.length} archives and checksums.txt\n`);
}

try {
  main();
} catch (err) {
  console.error(err.message);
  process.exit(1);
}
