import test from "node:test";
import assert from "node:assert/strict";
import { channelModelChanges, channelModelSyncSelection } from "../src/channelModels.ts";

const channel = { models: '["kept"]', discovered_models: '["kept","unselected"]', models_baseline: '["kept","unselected"]', models_sync_error: "" };

test("a curated subset and reordered upstream lists do not count as new models", () => {
  assert.deepEqual(channelModelChanges(channel), { added: [], removed: [], missing: [] });
  assert.deepEqual(channelModelChanges({ ...channel, discovered_models: '["unselected","kept"]' }), { added: [], removed: [], missing: [] });
  assert.deepEqual(channelModelChanges({ ...channel, models_baseline: "" }), { added: [], removed: [], missing: [] });
});

test("compares against the confirmed upstream list and identifies affected selections", () => {
  assert.deepEqual(channelModelChanges({ ...channel, discovered_models: '["unselected","new"]' }), { added: ["new"], removed: ["kept"], missing: ["kept"] });
});

test("confirmation clears the change while retaining information about missing selected models", () => {
  assert.deepEqual(channelModelChanges({ ...channel, discovered_models: '["unselected","new"]', models_baseline: '["unselected","new"]' }), { added: [], removed: [], missing: ["kept"] });
});

test("empty lists are real snapshots and failed or unperformed discovery shows no stale changes", () => {
  assert.deepEqual(channelModelChanges({ ...channel, models_baseline: "[]" }), { added: ["kept", "unselected"], removed: [], missing: [] });
  assert.deepEqual(channelModelChanges({ ...channel, discovered_models: "[]" }), { added: [], removed: ["kept", "unselected"], missing: ["kept"] });
  assert.deepEqual(channelModelChanges({ ...channel, models_baseline: "[]", models_sync_error: "failed" }), { added: [], removed: [], missing: [] });
  assert.deepEqual(channelModelChanges({ ...channel, discovered_models: "" }), { added: [], removed: [], missing: [] });
});

test("sync keeps all selected models including upstream omissions and leaves new models unselected", () => {
  const result = channelModelSyncSelection(["kept", "new"], ["kept", "missing"]);
  assert.deepEqual(result.models, ["kept", "new", "missing"]);
  assert.deepEqual([...result.selected], ["kept", "missing"]);
  assert.deepEqual([...channelModelSyncSelection(["new"], []).selected], []);
});
