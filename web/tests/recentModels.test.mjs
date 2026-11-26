import assert from "node:assert/strict";
import test from "node:test";
import { sortRecentModelGroups } from "../src/recentModels.ts";

test("最近使用模型按请求次数、最后使用时间和模型 ID 排序并限制数量", () => {
  const groups = [
    { id: 3, name: "old" },
    { id: 2, name: "same-time-later-id" },
    { id: 1, name: "same-time-earlier-id" },
    { id: 4, name: "most-used" },
    { id: 5, name: "unused" },
    { id: 6, name: "extra" },
    { id: 7, name: "more-recent" },
    { id: 8, name: "zero" },
  ];
  const usage = [
    { name: "old", requests: 2, last_request_unix: 200 },
    { name: "same-time-later-id", requests: 4, last_request_unix: 300 },
    { name: "same-time-earlier-id", requests: 4, last_request_unix: 300 },
    { name: "most-used", requests: 8, last_request_unix: 100 },
    { name: "extra", requests: 1, last_request_unix: 500 },
    { name: "more-recent", requests: 4, last_request_unix: 400 },
    { name: "zero", requests: 0, last_request_unix: 1000 },
  ];
  assert.deepEqual(
    sortRecentModelGroups(groups, usage).map((group) => group.name),
    ["most-used", "more-recent", "same-time-earlier-id", "same-time-later-id", "old"],
  );
  assert.deepEqual(groups.map((group) => group.name), [
    "old", "same-time-later-id", "same-time-earlier-id", "most-used", "unused", "extra", "more-recent", "zero",
  ]);
  assert.deepEqual(sortRecentModelGroups(groups, []), []);
});
