export const dashboardPages = {
  overview: "overview", accounts: "accounts", analysis: "analytics/summary", alerts: "monitor/alerts",
  ranking: "analytics/summary", cache: "analytics/cache", stability: "analytics/stability", cost: "analytics/cost", requests: "requests",
} as const;
export type DashboardPage = typeof dashboardPages[keyof typeof dashboardPages];
