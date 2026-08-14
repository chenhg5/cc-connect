"use strict";

const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const test = require("node:test");

const {
  parseChecksumManifest,
  verifyChecksum,
} = require("./install.js");

const filename = "cc-connect-next-v0.1.0-beta.1-darwin-arm64.tar.gz";
const payload = Buffer.from("verified archive bytes");
const digest = crypto.createHash("sha256").update(payload).digest("hex");

test("parseChecksumManifest selects one exact asset entry", () => {
  const manifest = [
    `${"1".repeat(64)}  cc-connect-next-v0.1.0-beta.1-linux-amd64.tar.gz`,
    `${digest}  ${filename}`,
    `${"2".repeat(64)} *cc-connect-next-v0.1.0-beta.1-windows-amd64.zip`,
  ].join("\n");

  assert.equal(parseChecksumManifest(manifest, filename), digest);
});

test("parseChecksumManifest fails closed for missing, duplicate, or malformed entries", () => {
  assert.throws(
    () => parseChecksumManifest(`${digest}  another-file.tar.gz\n`, filename),
    /does not contain/
  );
  assert.throws(
    () => parseChecksumManifest(`${digest}  ${filename}\n${digest} *${filename}\n`, filename),
    /more than one/
  );
  assert.throws(
    () => parseChecksumManifest(`not-a-sha256  ${filename}\n`, filename),
    /does not contain/
  );
});

test("verifyChecksum accepts matching bytes and rejects a mismatch", () => {
  assert.equal(verifyChecksum(payload, digest, filename), digest);
  assert.throws(
    () => verifyChecksum(Buffer.from("tampered"), digest, filename),
    /checksum mismatch/
  );
  assert.throws(
    () => verifyChecksum(payload, "abc", filename),
    /invalid SHA-256/
  );
});
