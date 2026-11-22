import test from "node:test";
import assert from "node:assert/strict";
import { dataTones, getTheme, normalizeTheme, themeModes, themes } from "../src/themes.ts";

function luminance(hex) {
  const values = hex.slice(1).match(/../g).map(value => {
    const channel = parseInt(value, 16) / 255;
    return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
  });
  return values[0] * 0.2126 + values[1] * 0.7152 + values[2] * 0.0722;
}

test("every theme has readable text, controls, statuses and code colors", () => {
  const pairs = [
    ["--text", "--bg"], ["--text", "--surface"], ["--text", "--surface-alt"],
    ["--text-muted", "--surface"], ["--text-muted", "--surface-alt"], ["--text-muted", "--bg"],
    ["--accent", "--surface"], ["--accent", "--accent-soft"], ["--on-accent", "--accent"],
    ["--success", "--success-soft"], ["--warning", "--warning-soft"], ["--danger", "--danger-soft"],
    ["--info", "--info-soft"], ["--purple", "--purple-soft"],
    ...dataTones.flatMap(tone => [
      [`--data-${tone}`, `--data-${tone}-soft`], [`--data-${tone}`, "--surface"],
      ["--text", `--data-${tone}-soft`], ["--text-muted", `--data-${tone}-soft`],
    ]),
    ["--sidebar-text", "--sidebar-bg"], ["--sidebar-muted", "--sidebar-bg"], ["--sidebar-active-text", "--sidebar-active"],
    ...["--code-key", "--code-string", "--code-number", "--code-literal"].map(color => [color, "--surface-alt"]),
  ];
  for (const theme of themes) for (const [foreground, background] of pairs) {
    const a = luminance(theme.colors[foreground]);
    const b = luminance(theme.colors[background]);
    const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
    assert(ratio >= 4.5, `${theme.id}: ${foreground} against ${background} is ${ratio.toFixed(2)}:1`);
  }
});

test("data categories retain distinct colors with matching tinted surfaces", () => {
  for (const theme of themes) {
    assert.equal(new Set(dataTones.map(tone => theme.colors[`--data-${tone}`])).size, 4, theme.id);
    assert.equal(new Set(dataTones.map(tone => theme.colors[`--data-${tone}-soft`])).size, 4, theme.id);
  }
});

test("display modes use two palettes and system preference without overriding explicit choices", () => {
  assert.deepEqual(themeModes.map(mode => mode.id), ["light", "dark", "system"]);
  assert.deepEqual(themes.map(theme => theme.id), ["light", "dark"]);
  for (const systemDark of [false, true]) {
    assert.equal(getTheme("light", systemDark).id, "light");
    assert.equal(getTheme("dark", systemDark).id, "dark");
    assert.equal(getTheme("system", systemDark).id, systemDark ? "dark" : "light");
  }
});

test("old saved palettes migrate and missing preferences follow the system", () => {
  for (const value of ["blue", "green", "amber"]) assert.equal(normalizeTheme(value), "light");
  assert.equal(normalizeTheme("night"), "dark");
  for (const value of [null, "", "missing", "constructor", "__proto__"]) {
    assert.equal(normalizeTheme(value), "system");
    assert.equal(getTheme(value, true).id, "dark");
    assert.equal(getTheme(value, false).id, "light");
  }
});

test("form borders and sidebar focus remain distinguishable", () => {
  for (const theme of themes) for (const [foreground, background] of [["--border-strong", "--surface"], ["--sidebar-accent", "--sidebar-bg"]]) {
    const a = luminance(theme.colors[foreground]);
    const b = luminance(theme.colors[background]);
    assert((Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05) >= 3, `${theme.id}: ${foreground}`);
  }
});
