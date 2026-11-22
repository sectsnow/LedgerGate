const baseColors = {
  "--bg": "#f5f7fa",
  "--surface": "#ffffff",
  "--surface-alt": "#f5f7fa",
  "--surface-hover": "#eef3f8",
  "--text": "#1f242b",
  "--text-muted": "#59636f",
  "--border": "#e1e7ef",
  "--border-strong": "#8290a1",
  "--accent": "#1f60c4",
  "--accent-hover": "#164d9f",
  "--accent-soft": "#e5edfb",
  "--accent-mid": "#91add8",
  "--on-accent": "#ffffff",
  "--data-balance": "#347b3a", "--data-balance-soft": "#e8f4e9",
  "--data-usage": "#8650a6", "--data-usage-soft": "#f1eaf7",
  "--data-traffic": "#1f60c4", "--data-traffic-soft": "#e5edfb",
  "--data-cost": "#ab5800", "--data-cost-soft": "#fff1df",
  "--success": "#347b3a",
  "--success-soft": "#e8f4e9",
  "--warning": "#ab5800",
  "--warning-soft": "#fff1df",
  "--on-warning": "#ffffff",
  "--danger": "#b22e3e",
  "--danger-hover": "#962032",
  "--danger-soft": "#fcebee",
  "--on-danger": "#ffffff",
  "--info": "#1f60c4",
  "--info-soft": "#e5edfb",
  "--purple": "#8650a6",
  "--purple-soft": "#f1eaf7",
  "--code-key": "#1f60c4",
  "--code-string": "#347b3a",
  "--code-number": "#8650a6",
  "--code-literal": "#ab5800",
  "--sidebar-bg": "#f8f9fa",
  "--sidebar-text": "#3d4651",
  "--sidebar-muted": "#59636f",
  "--sidebar-active": "#e5edfb",
  "--sidebar-active-text": "#1f60c4",
  "--sidebar-hover": "#e9edf2",
  "--sidebar-accent": "#1f60c4",
  "--sidebar-border": "#e1e7ef",
  "--sidebar-online": "#347b3a",
  "--control-off": "#7b8795",
  "--shadow-color": "#21324b0d",
  "--overlay": "#10121799",
  "--heatmap-0": "#e4e8ed",
  "--heatmap-1": "#cbdcf6",
  "--heatmap-2": "#9cbaea",
  "--heatmap-3": "#5c8bd3",
  "--heatmap-4": "#1f60c4",
};

export const themes = [
  {
    id: "light", scheme: "light",
    colors: baseColors,
  },
  {
    id: "dark", scheme: "dark",
    colors: {
      ...baseColors,
      "--bg": "#0b0c0e", "--surface": "#181b1f", "--surface-alt": "#22262d", "--surface-hover": "#2a303a",
      "--text": "#f3f4f6", "--text-muted": "#b0bac6", "--border": "#3d4550", "--border-strong": "#707c8c",
      "--accent": "#6e9fff", "--accent-hover": "#99bbff", "--accent-soft": "#1e304d", "--accent-mid": "#3e669f", "--on-accent": "#101217",
      "--data-balance": "#73bf69", "--data-balance-soft": "#173322",
      "--data-usage": "#c595e5", "--data-usage-soft": "#33233f",
      "--data-traffic": "#6e9fff", "--data-traffic-soft": "#1e304d",
      "--data-cost": "#ff9830", "--data-cost-soft": "#412b18",
      "--success": "#73bf69", "--success-soft": "#173322", "--warning": "#ff9830", "--warning-soft": "#412b18", "--on-warning": "#101217",
      "--danger": "#ff98a5", "--danger-hover": "#ffbbc4", "--danger-soft": "#43212b", "--on-danger": "#281016",
      "--info": "#6e9fff", "--info-soft": "#1e304d", "--purple": "#c595e5", "--purple-soft": "#33233f",
      "--code-key": "#6e9fff", "--code-string": "#73bf69", "--code-number": "#c595e5", "--code-literal": "#ff9830",
      "--sidebar-bg": "#111217", "--sidebar-text": "#bdc5cf", "--sidebar-muted": "#a5aeba",
      "--sidebar-active": "#263040", "--sidebar-active-text": "#91b7ff", "--sidebar-hover": "#20242d", "--sidebar-accent": "#91b7ff", "--sidebar-border": "#343b46",
      "--sidebar-online": "#73bf69", "--control-off": "#526070", "--shadow-color": "#0000004d", "--overlay": "#000000b3",
      "--heatmap-0": "#22262d", "--heatmap-1": "#253957", "--heatmap-2": "#355888", "--heatmap-3": "#4c7bc0", "--heatmap-4": "#8ab0ff",
    },
  },
] as const;

export const dataTones = ["balance", "usage", "traffic", "cost"] as const;
export type DataTone = typeof dataTones[number];
export const themeModes = [
  { id: "light", name: "浅色", description: "始终使用浅色界面。" },
  { id: "dark", name: "深色", description: "始终使用深色界面。" },
  { id: "system", name: "跟随系统", description: "随系统外观自动切换浅色与深色。" },
] as const;
export type ThemeMode = typeof themeModes[number]["id"];
export const themeStorageKey = "ledgergate-theme";

export function normalizeTheme(value: string | null): ThemeMode {
  if (value === "light" || value === "dark" || value === "system") return value;
  if (value === "night") return "dark";
  if (value === "blue" || value === "green" || value === "amber") return "light";
  return "system";
}

export function getTheme(mode: string | null, systemDark = false) {
  const normalized = normalizeTheme(mode);
  return themes[normalized === "dark" || normalized === "system" && systemDark ? 1 : 0];
}

export function readTheme(): ThemeMode {
  try { return normalizeTheme(localStorage.getItem(themeStorageKey)); }
  catch { return "system"; }
}

export function applyTheme(mode: ThemeMode) {
  const media = window.matchMedia("(prefers-color-scheme: dark)");
  const update = () => {
    const theme = getTheme(mode, media.matches);
    const root = document.documentElement;
    for (const [key, value] of Object.entries(theme.colors)) root.style.setProperty(key, value);
    root.dataset.theme = theme.id;
    root.dataset.themeMode = mode;
    root.style.colorScheme = theme.scheme;
    document.querySelector('meta[name="theme-color"]')?.setAttribute("content", theme.colors["--sidebar-bg"]);
  };
  update();
  try { localStorage.setItem(themeStorageKey, mode); } catch { /* 浏览器禁用存储时仍可切换当前页面。 */ }
  if (mode === "system") {
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }
}
