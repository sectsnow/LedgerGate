import { Monitor, Moon, Palette, Sun } from "lucide-react";
import { themeModes, type ThemeMode } from "./themes";

const modeIcons = { light: Sun, dark: Moon, system: Monitor };

export default function ThemePicker({ value, change }: { value: ThemeMode; change: (theme: ThemeMode) => void }) {
  return <section className="panel appearance-panel">
    <div className="panel-heading"><div><h2>主题与外观</h2><p>选择浅色、深色或跟随系统，即刻生效并保存在当前浏览器。</p></div><Palette size={19} aria-hidden="true" /></div>
    <fieldset className="theme-grid"><legend className="theme-legend">选择显示模式</legend>{themeModes.map(mode => {
      const Icon = modeIcons[mode.id];
      return <label className="theme-card" key={mode.id}>
        <Icon className="theme-mode-icon" size={22} aria-hidden="true" />
        <span className="theme-card-title"><strong>{mode.name}</strong><input type="radio" name="interface-theme" aria-label={mode.name} value={mode.id} checked={value === mode.id} onChange={() => change(mode.id)} /></span><p>{mode.description}</p>
      </label>;
    })}</fieldset>
  </section>;
}
