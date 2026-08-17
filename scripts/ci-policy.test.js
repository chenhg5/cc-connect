const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");

const root = path.resolve(__dirname, "..");
const workflow = fs.readFileSync(
  path.join(root, ".github", "workflows", "ci.yml"),
  "utf8",
);

function triggerBlock() {
  const match = workflow.match(/^on:\n([\s\S]*?)^concurrency:/m);
  assert.ok(match, "CI workflow must declare an on block before concurrency");
  return match[1];
}

test("CI runs only for version tag pushes", () => {
  const triggers = triggerBlock();

  assert.match(triggers, /push:\n\s+tags:\n\s+- ['"]v\*['"]/);
  assert.doesNotMatch(triggers, /^\s+branches:/m);
  assert.doesNotMatch(triggers, /^\s+pull_request:/m);
  assert.doesNotMatch(triggers, /^\s+release:/m);
  assert.doesNotMatch(triggers, /^\s+workflow_dispatch:/m);
  assert.doesNotMatch(triggers, /^\s+schedule:/m);
});
