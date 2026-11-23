import assert from 'node:assert/strict';
import test from 'node:test';
import { requestEndLabel, attemptStatusLabel } from '../src/requestEnd.ts';

test('terminal reasons distinguish limits, filtering and unknown incompletion', () => {
  assert.equal(requestEndLabel('length'), '达到 Token 上限');
  assert.equal(requestEndLabel('content_filter'), '内容过滤或拒绝');
  assert.equal(requestEndLabel('incomplete', 'vendor_reason'), '输出未完成（vendor_reason）');
  assert.equal(requestEndLabel('incomplete'), '输出未完成，原因未提供');
  assert.equal(requestEndLabel(undefined), '');
  assert.equal(requestEndLabel('tool_calls'), '已请求工具调用');
});

test('upstream HTTP success is distinguishable from gateway delivery failure', () => {
  assert.equal(attemptStatusLabel(502, 200), '上游 HTTP 200 · 转发结果 502');
  assert.equal(attemptStatusLabel(200, 200), '上游 HTTP 200');
  assert.equal(attemptStatusLabel(502, 0), '未收到 HTTP 响应 · 转发结果 502');
  assert.equal(attemptStatusLabel(502), '结果 HTTP 502');
  assert.equal(attemptStatusLabel(0), '进行中');
});
