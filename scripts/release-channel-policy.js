#!/usr/bin/env node

"use strict";

const fs = require("node:fs");

function parseReleaseVersion(value) {
  const original = String(value || "").trim();
  const normalized = original.startsWith("v") ? original.slice(1) : original;
  const match = normalized.match(
    /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/
  );
  if (!match) {
    throw new Error(`invalid release version: ${original || "<empty>"}`);
  }
  return {
    normalized,
    core: [BigInt(match[1]), BigInt(match[2]), BigInt(match[3])],
    prerelease: match[4] ? match[4].split(".") : [],
  };
}

function comparePrereleaseIdentifiers(left, right) {
  const leftNumeric = /^\d+$/.test(left);
  const rightNumeric = /^\d+$/.test(right);
  if (leftNumeric && rightNumeric) {
    const leftNumber = BigInt(left);
    const rightNumber = BigInt(right);
    return leftNumber < rightNumber ? -1 : leftNumber > rightNumber ? 1 : 0;
  }
  if (leftNumeric !== rightNumeric) {
    return leftNumeric ? -1 : 1;
  }
  return left < right ? -1 : left > right ? 1 : 0;
}

function compareReleaseVersions(leftValue, rightValue) {
  const left = parseReleaseVersion(leftValue);
  const right = parseReleaseVersion(rightValue);
  for (let i = 0; i < left.core.length; i += 1) {
    if (left.core[i] < right.core[i]) return -1;
    if (left.core[i] > right.core[i]) return 1;
  }
  if (left.prerelease.length === 0 || right.prerelease.length === 0) {
    if (left.prerelease.length === right.prerelease.length) return 0;
    return left.prerelease.length === 0 ? 1 : -1;
  }
  const sharedLength = Math.min(left.prerelease.length, right.prerelease.length);
  for (let i = 0; i < sharedLength; i += 1) {
    const compared = comparePrereleaseIdentifiers(
      left.prerelease[i],
      right.prerelease[i]
    );
    if (compared !== 0) return compared;
  }
  return left.prerelease.length < right.prerelease.length
    ? -1
    : left.prerelease.length > right.prerelease.length
      ? 1
      : 0;
}

function releaseChannelRelation(candidate, current) {
  if (!String(current || "").trim()) return "missing";
  const compared = compareReleaseVersions(candidate, current);
  return compared > 0 ? "newer" : compared < 0 ? "older" : "equal";
}

function newestReleaseVersion(values) {
  let newest = "";
  for (const value of values) {
    const trimmed = String(value || "").trim();
    if (!trimmed) continue;
    const parsed = parseReleaseVersion(trimmed);
    if (!newest || compareReleaseVersions(parsed.normalized, newest) > 0) {
      newest = parsed.normalized;
    }
  }
  return newest;
}

function main() {
  const [mode, ...args] = process.argv.slice(2);
  if (mode === "--relation" && (args.length === 1 || args.length === 2)) {
    process.stdout.write(`${releaseChannelRelation(args[0], args[1] || "")}\n`);
    return;
  }
  if (mode === "--max" && args.length === 0) {
    const versions = fs.readFileSync(0, "utf8").split(/\s+/);
    process.stdout.write(`${newestReleaseVersion(versions)}\n`);
    return;
  }
  throw new Error(
    "usage: release-channel-policy.js --relation <candidate> [current] | --max"
  );
}

if (require.main === module) {
  try {
    main();
  } catch (err) {
    console.error(`[release-channel] ${err.message}`);
    process.exit(1);
  }
}

module.exports = {
  compareReleaseVersions,
  newestReleaseVersion,
  releaseChannelRelation,
};
