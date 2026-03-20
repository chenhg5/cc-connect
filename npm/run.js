#!/usr/bin/env node

"use strict";

const { execFileSync, execSync } = require("child_process");
const path = require("path");
const fs = require("fs");

const PACKAGE = require("./package.json");
const EXPECTED_VER = PACKAGE.version; // e.g. "1.1.0-beta.4"
const NAME = "cc-connect";
const binDir = path.join(__dirname, "bin");
const ext = process.platform === "win32" ? ".exe" : "";
const binaryPath = path.join(binDir, NAME + ext);

// parseVersion splits "1.2.3-beta.1" into { nums: [1,2,3], preTag: "beta", preNum: 1 }
function parseVersion(v) {
  v = v.replace(/^v/, "").trim();
  const [base, ...rest] = v.split("-");
  const nums = base.split(".").map(Number);
  const pre = rest.join("-");
  const m = pre.match(/^([a-zA-Z]+)\.?(\d+)?$/);
  return { nums, preTag: m ? m[1] : pre, preNum: m && m[2] ? parseInt(m[2], 10) : 0, hasPre: pre !== "" };
}

// isNewerOrEqual returns true if installed >= expected
function isNewerOrEqual(installed, expected) {
  const a = parseVersion(installed);
  const b = parseVersion(expected);
  const len = Math.max(a.nums.length, b.nums.length);
  for (let i = 0; i < len; i++) {
    const av = a.nums[i] || 0;
    const bv = b.nums[i] || 0;
    if (av > bv) return true;
    if (av < bv) return false;
  }
  // Same base: no pre-release >= any pre-release (1.2.3 >= 1.2.3-beta.1)
  if (!a.hasPre && b.hasPre) return true;
  if (a.hasPre && !b.hasPre) return false;
  if (!a.hasPre && !b.hasPre) return true;
  // Both pre-release: compare tag then number (rc > beta, beta.10 > beta.9)
  if (a.preTag !== b.preTag) return a.preTag > b.preTag;
  return a.preNum >= b.preNum;
}

function needsReinstall() {
  if (!fs.existsSync(binaryPath)) return true;
  try {
    const out = execFileSync(binaryPath, ["--version"], { encoding: "utf8", timeout: 5000 });
    if (out.includes(EXPECTED_VER)) return false;
    // Extract version from output (e.g. "cc-connect 1.2.2-beta.1" or "1.2.2-beta.1")
    const match = out.match(/(\d+\.\d+\.\d+[^\s]*)/);
    if (match && isNewerOrEqual(match[1], EXPECTED_VER)) return false;
    return true;
  } catch {
    return true;
  }
}

if (needsReinstall()) {
  console.log(`[cc-connect] Binary missing or outdated, installing v${EXPECTED_VER}...`);
  try {
    execSync("node " + JSON.stringify(path.join(__dirname, "install.js")), {
      stdio: "inherit",
      cwd: __dirname,
    });
  } catch {
    console.error("[cc-connect] Auto-install failed. Run manually: npm uninstall -g cc-connect && npm install -g cc-connect@beta");
    process.exit(1);
  }
}

try {
  execFileSync(binaryPath, process.argv.slice(2), { stdio: "inherit" });
} catch (err) {
  process.exit(err.status || 1);
}
