#!/usr/bin/env node

"use strict";

const { execFileSync } = require("child_process");
const crypto = require("crypto");
const fs = require("fs");
const path = require("path");
const https = require("https");
const http = require("http");

const PACKAGE = require("./package.json");
const VERSION = `v${PACKAGE.version}`;
const NAME = "cc-connect-next";

const GITHUB_REPO = "timmyagentic/cc-connect-next";

const PLATFORM_MAP = {
  darwin: "darwin",
  linux: "linux",
  win32: "windows",
};

const ARCH_MAP = {
  x64: "amd64",
  arm64: "arm64",
};

function getPlatformInfo() {
  const platform = PLATFORM_MAP[process.platform];
  const arch = ARCH_MAP[process.arch];
  if (!platform || !arch) {
    throw new Error(
      `Unsupported platform: ${process.platform}/${process.arch}. ` +
        `Supported: linux/darwin/windows x64/arm64`
    );
  }
  const ext = platform === "windows" ? ".zip" : ".tar.gz";
  const filename = `${NAME}-${VERSION}-${platform}-${arch}${ext}`;
  return { platform, arch, ext, filename };
}

function getDownloadURLs(filename) {
  return [
    `https://github.com/${GITHUB_REPO}/releases/download/${VERSION}/${filename}`,
  ];
}

function getChecksumURLs() {
  return [
    `https://github.com/${GITHUB_REPO}/releases/download/${VERSION}/checksums.txt`,
  ];
}

function fetch(url, redirects = 5) {
  return new Promise((resolve, reject) => {
    if (redirects <= 0) return reject(new Error("Too many redirects"));
    const mod = url.startsWith("https") ? https : http;
    mod
      .get(url, { headers: { "User-Agent": "cc-connect-next-npm" } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          const redirect = new URL(res.headers.location, url).toString();
          return resolve(fetch(redirect, redirects - 1));
        }
        if (res.statusCode !== 200) {
          res.resume();
          return reject(new Error(`HTTP ${res.statusCode} for ${url}`));
        }
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => resolve(Buffer.concat(chunks)));
        res.on("error", reject);
      })
      .on("error", reject);
  });
}

function parseChecksumManifest(manifest, filename) {
  const matches = [];
  for (const line of String(manifest).split(/\r?\n/)) {
    const match = line.match(/^([0-9a-fA-F]{64})[ \t]+(?:\*)?(.+)$/);
    if (match && match[2] === filename) {
      matches.push(match[1].toLowerCase());
    }
  }
  if (matches.length === 0) {
    throw new Error(`[cc-connect-next] checksums.txt does not contain ${filename}`);
  }
  if (matches.length > 1) {
    throw new Error(`[cc-connect-next] checksums.txt contains more than one entry for ${filename}`);
  }
  return matches[0];
}

function verifyChecksum(data, expected, filename) {
  if (!/^[0-9a-fA-F]{64}$/.test(expected)) {
    throw new Error(`[cc-connect-next] invalid SHA-256 for ${filename}`);
  }
  const actual = crypto.createHash("sha256").update(data).digest("hex");
  const matches = crypto.timingSafeEqual(
    Buffer.from(actual, "hex"),
    Buffer.from(expected, "hex")
  );
  if (!matches) {
    throw new Error(
      `[cc-connect-next] checksum mismatch for ${filename}; refusing to extract the archive`
    );
  }
  return actual;
}

function replaceBinary(stagedPath, binaryPath) {
  const backupPath = `${binaryPath}.previous-${process.pid}`;
  const hadExisting = fs.existsSync(binaryPath);
  if (hadExisting) {
    fs.renameSync(binaryPath, backupPath);
  }
  try {
    fs.renameSync(stagedPath, binaryPath);
    if (hadExisting) {
      fs.rmSync(backupPath, { force: true });
    }
  } catch (err) {
    if (hadExisting && fs.existsSync(backupPath) && !fs.existsSync(binaryPath)) {
      fs.renameSync(backupPath, binaryPath);
    }
    throw err;
  }
}

function validateExtractedBinary(extractDir, archiveBinaryName) {
  const extractedPath = path.join(extractDir, archiveBinaryName);
  const info = fs.lstatSync(extractedPath);
  if (!info.isFile() || info.isSymbolicLink()) {
    throw new Error(`[cc-connect-next] archive entry is not a regular file: ${archiveBinaryName}`);
  }
  return extractedPath;
}

async function download(urls) {
  for (const url of urls) {
    try {
      console.log(`[cc-connect-next] Downloading from ${url}`);
      const data = await fetch(url);
      console.log(`[cc-connect-next] Downloaded ${(data.length / 1024 / 1024).toFixed(1)} MB`);
      return data;
    } catch (err) {
      console.warn(`[cc-connect-next] Failed: ${err.message}, trying next source...`);
    }
  }
  throw new Error(
    `[cc-connect-next] Could not download binary from any source.\n` +
      `  Tried: ${urls.join(", ")}\n` +
      `  You can download manually from https://github.com/${GITHUB_REPO}/releases`
  );
}

function extractTarGz(buffer, destDir, binaryName, archiveBinaryName) {
  const extractDir = fs.mkdtempSync(path.join(destDir, ".extract-"));
  const tmpFile = path.join(extractDir, "archive.tar.gz");
  try {
    fs.writeFileSync(tmpFile, buffer);
    execFileSync("tar", ["xzf", tmpFile, "-C", extractDir], { stdio: "pipe" });
    const extractedPath = validateExtractedBinary(extractDir, archiveBinaryName);
    const stagedPath = path.join(destDir, `.${binaryName}.install-${process.pid}-${crypto.randomBytes(6).toString("hex")}`);
    fs.renameSync(extractedPath, stagedPath);
    replaceBinary(stagedPath, path.join(destDir, binaryName));
  } finally {
    fs.rmSync(extractDir, { recursive: true, force: true });
  }
}

function extractZip(buffer, destDir, binaryName, archiveBinaryName) {
  const extractDir = fs.mkdtempSync(path.join(destDir, ".extract-"));
  const tmpFile = path.join(extractDir, "archive.zip");
  try {
    fs.writeFileSync(tmpFile, buffer);
    try {
      execFileSync("unzip", ["-o", tmpFile, "-d", extractDir], { stdio: "pipe" });
    } catch {
      const quotePowerShell = (value) => value.replace(/'/g, "''");
      execFileSync("powershell", ["-NoProfile", "-NonInteractive", "-Command",
        `Expand-Archive -Force -LiteralPath '${quotePowerShell(tmpFile)}' -DestinationPath '${quotePowerShell(extractDir)}'`], {
        stdio: "pipe",
      });
    }
    const extractedPath = validateExtractedBinary(extractDir, archiveBinaryName);
    const stagedPath = path.join(destDir, `.${binaryName}.install-${process.pid}-${crypto.randomBytes(6).toString("hex")}`);
    fs.renameSync(extractedPath, stagedPath);
    replaceBinary(stagedPath, path.join(destDir, binaryName));
  } finally {
    fs.rmSync(extractDir, { recursive: true, force: true });
  }
}

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
  if (!a.hasPre && b.hasPre) return true;
  if (a.hasPre && !b.hasPre) return false;
  if (!a.hasPre && !b.hasPre) return true;
  // Both pre-release: compare tag then number (rc > beta, beta.10 > beta.9)
  if (a.preTag !== b.preTag) return a.preTag > b.preTag;
  return a.preNum >= b.preNum;
}

async function main() {
  const { platform, arch, ext, filename } = getPlatformInfo();
  console.log(`[cc-connect-next] Platform: ${platform}/${arch}`);

  const binDir = path.join(__dirname, "bin");
  fs.mkdirSync(binDir, { recursive: true });

  const binaryName = platform === "windows" ? `${NAME}.exe` : NAME;
  const binaryPath = path.join(binDir, binaryName);

  if (fs.existsSync(binaryPath)) {
    try {
      const out = execFileSync(binaryPath, ["--version"], { encoding: "utf8", timeout: 5000 });
      const expectedVer = VERSION.slice(1); // remove leading "v"
      if (out.includes(expectedVer)) {
        console.log(`[cc-connect-next] Binary ${VERSION} already installed, skipping.`);
        return;
      }
      // Don't downgrade: if existing binary is newer, keep it
      const match = out.match(/(\d+\.\d+\.\d+[^\s]*)/);
      if (match && isNewerOrEqual(match[1], expectedVer)) {
        console.log(`[cc-connect-next] Binary ${match[1]} is newer than ${VERSION}, skipping.`);
        return;
      }
      console.log(`[cc-connect-next] Existing binary is outdated, upgrading to ${VERSION}...`);
    } catch {
      console.log(`[cc-connect-next] Replacing existing binary with ${VERSION}...`);
    }
  }

  const checksumData = await download(getChecksumURLs());
  const expectedChecksum = parseChecksumManifest(checksumData.toString("utf8"), filename);
  const data = await download(getDownloadURLs(filename));
  verifyChecksum(data, expectedChecksum, filename);
  console.log(`[cc-connect-next] Verified SHA-256 for ${filename}`);

  const archiveBinaryName = `${NAME}-${VERSION}-${platform}-${arch}${platform === "windows" ? ".exe" : ""}`;

  if (ext === ".tar.gz") {
    extractTarGz(data, binDir, binaryName, archiveBinaryName);
  } else {
    extractZip(data, binDir, binaryName, archiveBinaryName);
  }

  if (platform !== "windows") {
    fs.chmodSync(binaryPath, 0o755);
  }

  if (platform === "darwin") {
    try {
      execFileSync("xattr", ["-d", "com.apple.quarantine", binaryPath], { stdio: "pipe" });
      console.log(`[cc-connect-next] Removed macOS quarantine attribute`);
    } catch {
      // xattr fails if the attribute doesn't exist, which is fine
    }
  }

  console.log(`[cc-connect-next] Installed to ${binaryPath}`);
}

if (require.main === module) {
  main().catch((err) => {
    console.error(err.message);
    console.error(
      "[cc-connect-next] Installation failed. You can install manually:\n" +
        `  https://github.com/${GITHUB_REPO}/releases/tag/${VERSION}`
    );
    process.exit(1);
  });
}

module.exports = {
  getPlatformInfo,
  isNewerOrEqual,
  parseChecksumManifest,
  verifyChecksum,
};
