import assert from "node:assert/strict";
import test from "node:test";
import { backupPasswordError } from "../src/backupPasswordError.ts";

test("encrypted backup and transfer passwords use UTF-8 bytes without trimming", () => {
  for (const password of ["a".repeat(12), "a".repeat(256), "中文密码", "😀".repeat(3), " ".repeat(12)]) {
    assert.equal(backupPasswordError(password), "", password);
  }
  for (const password of ["", "a".repeat(11), "a".repeat(257), "中文密", "😀".repeat(2), "中".repeat(86)]) {
    assert.equal(backupPasswordError(password), "加密密码须为 12–256 字节。", password);
  }
});
