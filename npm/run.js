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

function needsReinstall() {
  if (!fs.existsSync(binaryPath)) return true;
  try {
    const out = execFileSync(binaryPath, ["--version"], { encoding: "utf8", timeout: 5000 });
    return !out.includes(EXPECTED_VER);
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
