import assert from "node:assert/strict";
import test from "node:test";
import { requestTrendPath, requestTrendSegments } from "../src/requestTrendPath.ts";

test("request trend handles empty and isolated observations", () => {
  assert.equal(requestTrendPath([]), "");
  assert.equal(requestTrendPath([{ x: 7, y: 0 }]), "M7,0");
});

test("unknown trend values break curves without discarding observed zero or isolated values", () => {
  assert.deepEqual(requestTrendSegments([]), []);
  assert.deepEqual(requestTrendSegments([{ x: 0, y: null }]), []);
  const segments = requestTrendSegments([null, 0, 2, null, null, 4, null, 6, 0, null].map((y, x) => ({ x, y })));
  assert.deepEqual(segments, [[{ x: 1, y: 0 }, { x: 2, y: 2 }], [{ x: 5, y: 4 }], [{ x: 7, y: 6 }, { x: 8, y: 0 }]]);
  assert.deepEqual(segments.map(requestTrendPath).map(path => (path.match(/ C/g) || []).length), [1, 0, 1]);
});

test("smooth request curves pass through observations without inventing peaks or negative counts", () => {
  for (const values of [[0, 0, 0], [0, 100, 0], [100, 0, 100], [0, 10, 11, 300, 300, 0], [0, 16, 279, 122, 128, 199, 114, 317, 190, 44, 0, 56, 54, 138, 82, 45]]) {
    const points = values.map((y, i) => ({ x: i * 13, y }));
    const path = requestTrendPath(points);
    const segments = path.split(" C").slice(1);
    assert.equal(segments.length, points.length - 1);
    for (const [i, segment] of segments.entries()) {
      const [x1, y1, x2, y2, x3, y3] = segment.split(/[ ,]+/).map(Number);
      const start = points[i], end = points[i + 1];
      assert.equal(x3, end.x); assert.equal(y3, end.y);
      for (let sample = 0; sample <= 100; sample++) {
        const t = sample / 100, q = 1 - t;
        const y = q ** 3 * start.y + 3 * q ** 2 * t * y1 + 3 * q * t ** 2 * y2 + t ** 3 * y3;
        const x = q ** 3 * start.x + 3 * q ** 2 * t * x1 + 3 * q * t ** 2 * x2 + t ** 3 * x3;
        assert.ok(y >= Math.min(start.y, end.y) - 1e-8 && y <= Math.max(start.y, end.y) + 1e-8);
        assert.ok(x >= start.x - 1e-8 && x <= end.x + 1e-8);
      }
    }
  }
});
