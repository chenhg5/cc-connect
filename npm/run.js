#!/usr/bin/env node

"use strict";

const { execFileSync } = require("child_process");
const path = require("path");
const fs = require("fs");

const NAME = "cc-connect";
const binDir = path.join(__dirname, "bin");
const ext = process.platform === "win32" ? ".exe" : "";
const binaryPath = path.join(binDir, NAME + ext);

if (!fs.existsSync(binaryPath)) {
  console.error(
    `[cc-connect] Binary not found at ${binaryPath}\n` +
      `  Run "node install.js" in ${__dirname} or reinstall:\n` +
      `  npm install -g cc-connect`
  );
  process.exit(1);
}

try {
  execFileSync(binaryPath, process.argv.slice(2), { stdio: "inherit" });
} catch (err) {
  process.exit(err.status || 1);
}
