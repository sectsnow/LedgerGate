export function requestTrendPath(points: { x: number; y: number }[]): string {
  if (!points.length) return "";
  const slopes = points.slice(1).map((point, i) => (point.y - points[i].y) / (point.x - points[i].x));
  const tangents = points.map((_, i) => {
    if (i === 0) return slopes[0] || 0;
    if (i === points.length - 1) return slopes[i - 1] || 0;
    const before = slopes[i - 1], after = slopes[i];
    // 峰谷及平坦处使用水平切线，避免曲线超出相邻数据的范围。
    return before * after > 0 ? 2 * before * after / (before + after) : 0;
  });
  let path = `M${points[0].x},${points[0].y}`;
  for (let i = 1; i < points.length; i++) {
    const start = points[i - 1], end = points[i], step = (end.x - start.x) / 3;
    path += ` C${start.x + step},${start.y + step * tangents[i - 1]} ${end.x - step},${end.y - step * tangents[i]} ${end.x},${end.y}`;
  }
  return path;
}

export function requestTrendSegments(points: { x: number; y: number | null }[]): { x: number; y: number }[][] {
  const segments: { x: number; y: number }[][] = [];
  let segment: { x: number; y: number }[] = [];
  for (const point of points) {
    if (point.y === null) segment = [];
    else {
      if (!segment.length) segments.push(segment);
      segment.push({ x: point.x, y: point.y });
    }
  }
  return segments;
}
