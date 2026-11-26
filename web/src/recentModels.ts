export type RecentModelUsage = {
  name: string;
  requests: number;
  last_request_unix: number;
};

export function sortRecentModelGroups<T extends { id: number; name: string }>(
  groups: T[],
  usage: RecentModelUsage[],
): T[] {
  const byName = new Map(usage.map((row) => [row.name, row]));
  return groups
    .filter((group) => (byName.get(group.name)?.requests || 0) > 0)
    .sort((left, right) => {
      const a = byName.get(left.name)!;
      const b = byName.get(right.name)!;
      return b.requests - a.requests ||
        b.last_request_unix - a.last_request_unix ||
        left.id - right.id;
    })
    .slice(0, 5);
}
