from pathlib import Path
import json
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parent
MARKS = [
    {
        "id": "01", "slug": "ledger-portal", "name": "门账", "idea": "门形入口 × 账本刻度", "color": "#245DE8", "ink": "#19345A", "dark": "#7FA9FF",
        "art": '''<path fill="INK" fill-rule="evenodd" d="M28 12H68Q80 12 80 24V82H58V36H38V82H16V24Q16 12 28 12Z M22 32H32V38H22Z M22 46H32V52H22Z M22 60H32V66H22Z"/><path fill="COLOR" d="M58 34 80 24V75L58 84Z"/>''',
    },
    {
        "id": "02", "slug": "woven-lg", "name": "交织 LG", "idea": "L 与 G 咬合，简洁的字母标记", "color": "#437BFF", "ink": "#172F52", "dark": "#8DB0FF",
        "art": '''<path fill="INK" d="M14 14H32V62H70V80H24Q14 80 14 70Z"/><path fill="COLOR" d="M58 14C41 14 29 26 29 43S41 72 58 72H82V40H56V54H66V58H58C49 58 43 52 43 43S49 28 58 28H82V14Z"/>''',
    },
    {
        "id": "03", "slug": "confluence", "name": "汇流", "idea": "多路上游，汇入同一出口", "color": "#13AFA9", "ink": "#285FDD", "dark": "#55D9CE",
        "art": '''<g fill="none" stroke-linecap="round" stroke-linejoin="round" stroke-width="11"><path stroke="INK" d="M12 20H27Q36 20 43 29L58 48H76"/><path stroke="COLOR" d="M12 76H27Q36 76 43 67L58 48"/><path stroke="INK" d="M12 48H76"/></g><path fill="INK" d="M70 31 89 48 70 65Z"/>''',
    },
    {
        "id": "04", "slug": "folded-ledger", "name": "折页门", "idea": "账本折页中留出一道门", "color": "#65C9A3", "ink": "#177A61", "dark": "#9AE4C6",
        "art": '''<path fill="INK" fill-rule="evenodd" d="M24 10H59L80 31V76Q80 86 70 86H24Q14 86 14 76V20Q14 10 24 10Z M38 86V62A10 10 0 0 1 58 62V86Z M25 30H48V36H25Z M25 42H55V48H25Z"/><path fill="COLOR" d="M59 10V25Q59 31 65 31H80Z"/>''',
    },
    {
        "id": "05", "slug": "ledger-ring", "name": "账环", "idea": "环形 G 与三道账目刻度", "color": "#36A8D0", "ink": "#3F5DD9", "dark": "#73D5F0",
        "art": '''<defs><mask id="ring-cut" maskUnits="userSpaceOnUse" x="0" y="0" width="96" height="96"><rect width="96" height="96" fill="white"/><path fill="black" d="M57 20H96V48H57Z M7 29H28V35H7Z M5 44H25V50H5Z M7 59H28V65H7Z"/></mask></defs><circle cx="48" cy="48" r="29" fill="none" stroke="INK" stroke-width="18" mask="url(#ring-cut)"/><path fill="COLOR" d="M47 44H86V58H47Z"/>''',
    },
    {
        "id": "06", "slug": "protocol-prism", "name": "协议棱镜", "idea": "多面接口，统一连接", "color": "#24ACC5", "ink": "#6552DA", "dark": "#78E0ED",
        "art": '''<path fill="INK" d="M48 8 12 28V68L48 88V48L30 38L48 28Z"/><path fill="COLOR" d="M48 8 84 28V68L48 88V48L66 38L48 28Z"/>''',
    },
    {
        "id": "07", "slug": "ledger-bridge", "name": "双页桥", "idea": "展开的账本，也是连接两端的桥", "color": "#27A99C", "ink": "#234C7D", "dark": "#78DBCE",
        "art": '''<defs><mask id="book-cut"><rect width="96" height="96" fill="white"/><path fill="black" d="M36 90V61A12 12 0 0 1 60 61V90Z"/><g stroke="black" stroke-width="5" stroke-linecap="round"><path d="M23 32Q32 31 39 35M23 43Q31 42 35 44M57 35Q64 31 73 32M61 44Q65 42 73 43"/></g></mask></defs><g mask="url(#book-cut)"><path fill="INK" d="M12 18C26 14 39 18 48 26V80C38 72 26 70 12 75Z"/><path fill="COLOR" d="M84 18C70 14 57 18 48 26V80C58 72 70 70 84 75Z"/></g>''',
    },
    {
        "id": "08", "slug": "operations-beacon", "name": "运维哨塔", "idea": "监控信号与稳定守望", "color": "#EDA247", "ink": "#234166", "dark": "#FFD08A",
        "art": '''<path fill="INK" fill-rule="evenodd" d="M38 29H58L72 84H24Z M42 84V67A6 6 0 0 1 54 67V84Z M42 42H54V49H42Z"/><circle cx="48" cy="15" r="8" fill="COLOR"/><path fill="COLOR" d="M33 15 12 6V24Z M63 15 84 6V24Z"/>''',
    },
    {
        "id": "09", "slug": "continuous-flow", "name": "回环", "idea": "连续流转与自动故障切换", "color": "#27B8AD", "ink": "#117C8A", "dark": "#7CE4D6",
        "art": '''<path d="M48 48C37 33 29 25 20 30 5 38 12 65 28 65 46 65 50 29 69 29 85 29 91 53 77 63 66 71 57 62 48 48Z" fill="none" stroke="INK" stroke-width="13" stroke-linecap="round" stroke-linejoin="round"/><path d="M48 48C55 36 61 29 69 29 85 29 91 53 77 63 71 67 65 66 59 61" fill="none" stroke="COLOR" stroke-width="13" stroke-linecap="round"/>''',
    },
    {
        "id": "10", "slug": "ledger-layers", "name": "层账", "idea": "账户、账目与请求逐层归集", "color": "#4280E7", "ink": "#6653D7", "dark": "#93BCFF",
        "art": '''<path fill="INK" d="M12 38H25V68H55V82H22Q12 82 12 72Z"/><path fill="COLOR" d="M27 24H40V53H70V67H37Q27 67 27 57Z"/><path fill="INK" fill-rule="evenodd" d="M51 10H75Q85 10 85 20V44Q85 54 75 54H51Q41 54 41 44V20Q41 10 51 10Z M51 23H75V29H51Z M51 35H68V41H51Z"/>''',
    },
]


def svg(mark, variant, instance="mark"):
    ink, color = mark["ink"], mark["color"]
    if variant == "dark":
        ink, color = "#DCE8FB", mark["dark"]
    elif variant == "mono":
        ink = color = "#1D293B"
    elif variant == "white":
        ink = color = "#FFFFFF"
    prefix = f'lg-{mark["id"]}-{variant}-{instance}'
    art = mark["art"].replace("INK", ink).replace("COLOR", color).replace('id="', f'id="{prefix}-').replace('url(#', f'url(#{prefix}-')
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 96 96" fill="none" role="img" aria-labelledby="{prefix}-title"><title id="{prefix}-title">LedgerGate {mark["id"]} {mark["name"]}</title>{art}</svg>\n'


def sheet(mode):
    dark = mode == "dark"
    cards = []
    for mark in MARKS:
        variant = "dark" if dark else "light"
        symbol = svg(mark, variant, "main")
        cards.append(f'''<article class="concept">
  <div class="card-top"><span class="number">{mark['id']}</span><div><h2>{mark['name']}</h2><p>{mark['idea']}</p></div></div>
  <div class="identity"><div class="big-mark">{symbol}</div><div class="wordmark">Ledger<span>Gate</span></div></div>
  <div class="applications"><span class="mini-label">小尺寸</span><div class="tiny tiny-16">{svg(mark, variant, '16')}</div><div class="tiny tiny-24">{svg(mark, variant, '24')}</div><div class="tiny tiny-32">{svg(mark, variant, '32')}</div><span class="mini-label mono-label">单色</span><div class="tiny tiny-32">{svg(mark, 'white' if dark else 'mono', 'sample')}</div></div>
</article>''')
    return '''<!doctype html>
<html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>LedgerGate · 十款 Logo 概念</title>
<style>
@font-face {font-family:Geist;src:url('../../web/static/app/assets/geist-latin-wght-normal-Dg_dQHbK.woff2') format('woff2');font-weight:100 900;font-display:swap}
* {box-sizing:border-box} body {margin:0;font-family:Geist,system-ui,-apple-system,'Noto Sans CJK SC',sans-serif;color:var(--ink);background:var(--paper);--paper:#f4f6fa;--card:#fff;--ink:#1c2e49;--muted:#61718a;--line:#e1e7f0;--accent:#245de8}
body.dark {--paper:#0e1726;--card:#172439;--ink:#e7eef9;--muted:#a7b7cd;--line:#2c3d56;--accent:#88afff}
.board {max-width:1600px;margin:0 auto;padding:64px 64px 42px}
.heading {display:flex;align-items:flex-end;justify-content:space-between;gap:30px;margin-bottom:40px}
.eyebrow {margin:0 0 12px;color:var(--accent);font-size:20px;font-weight:600;letter-spacing:4px}
h1 {font-size:64px;letter-spacing:-2.6px;font-weight:650;margin:0;line-height:1.05}.sub {font-size:25px;color:var(--muted);margin:16px 0 0;letter-spacing:1px}
.edition {text-align:right;color:var(--muted);font-size:19px;line-height:1.7;white-space:nowrap}.edition strong {font-weight:600;color:var(--ink)}
.grid {display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:24px}
.concept {background:var(--card);border:1px solid var(--line);border-radius:26px;padding:26px 28px 22px;min-width:0}
.card-top {display:flex;align-items:center;gap:18px}.number {color:var(--accent);font-size:26px;font-weight:600;letter-spacing:1px;font-variant-numeric:tabular-nums;align-self:flex-start;padding-top:1px}
h2 {margin:0;font-size:27px;font-weight:600;letter-spacing:.2px}.card-top p {font-size:19px;color:var(--muted);margin:7px 0 0;line-height:1.5}
.identity {display:flex;align-items:center;justify-content:center;gap:22px;padding:24px 0 25px;min-height:180px}.big-mark {width:128px;height:128px;flex:none}.big-mark svg,.tiny svg {display:block;width:100%;height:100%}
.wordmark {font-size:48px;font-weight:650;letter-spacing:-2px;color:var(--ink);white-space:nowrap}.wordmark span {font-weight:500}
.applications {border-top:1px solid var(--line);padding-top:18px;display:flex;align-items:center;gap:22px;min-height:54px}.mini-label {font-size:17px;color:var(--muted)}.mono-label {margin-left:auto}
.tiny {display:flex;align-items:center;justify-content:center;flex:none}.tiny-16 {width:16px;height:16px}.tiny-24 {width:24px;height:24px}.tiny-32 {width:32px;height:32px}
.footer {margin-top:30px;display:flex;justify-content:space-between;gap:20px;font-size:18px;color:var(--muted);letter-spacing:.3px}
@media(max-width:1000px){.board{padding:32px}.heading{align-items:flex-start}.identity{gap:10px;flex-direction:column}.big-mark{width:96px;height:96px}.wordmark{font-size:36px}.card-top p{font-size:16px}.concept{padding:22px}.applications{gap:16px}}
@media(max-width:640px){.board{padding:20px}.grid{grid-template-columns:1fr}.heading{flex-direction:column;gap:18px}h1{font-size:46px}.sub{font-size:19px}.edition{text-align:left;font-size:15px}.eyebrow{font-size:14px}.card-top p{font-size:15px}.footer{font-size:13px;flex-direction:column}.number{font-size:23px}h2{font-size:23px}.wordmark{font-size:38px}.identity{gap:16px}.applications{gap:22px}}
</style></head>''' + f'<body class="{mode}"><main class="board"><header class="heading"><div><p class="eyebrow">IDENTITY EXPLORATION</p><h1>LedgerGate</h1><p class="sub">上游账户运维 · LLM API 网关</p></div><div class="edition"><strong>10 款 Logo 概念</strong><br>{"深色背景" if dark else "浅色背景"} · 图标 / 字标 / 小尺寸</div></header><div class="grid">' + "\n".join(cards) + '</div><footer class="footer"><span>10 个不同图形方向 · 编号 01—10</span><span>LedgerGate / Concept Collection 01</span></footer></main></body></html>\n'


def main():
    for mark in MARKS:
        folder = ROOT / "options" / (mark["id"] + "-" + mark["slug"])
        folder.mkdir(parents=True, exist_ok=True)
        for variant in ["light", "dark", "mono", "white"]:
            raw = svg(mark, variant)
            ET.fromstring(raw)
            (folder / (variant + ".svg")).write_text(raw, encoding="utf8")
    (ROOT / "overview-light.html").write_text(sheet("light"), encoding="utf8")
    (ROOT / "overview-dark.html").write_text(sheet("dark"), encoding="utf8")
    (ROOT / "concepts.json").write_text(json.dumps([{key: value for key, value in mark.items() if key != "art"} for mark in MARKS], ensure_ascii=False, indent=2) + "\n", encoding="utf8")
    lines = ["# LedgerGate Logo 概念", "", "十款独立设计的矢量概念，供选择。", "", "| 编号 | 名称 | 图形构思 |", "| --- | --- | --- |"]
    lines += [f'| {m["id"]} | {m["name"]} | {m["idea"]} |' for m in MARKS]
    lines += ["", "overview-light.png / overview-dark.png：编号对比总览。", "", "options/：各款独立图标，提供浅色配色、深色配色、单色与反白 SVG，以及透明 PNG。", "", "SVG 为自行编写的图形路径；PNG 由同一套图形渲染。此目录用于设计选择，应用界面尚未接入。", ""]
    (ROOT / "README.md").write_text("\n".join(lines), encoding="utf8")
    print(json.dumps({"options": len(MARKS), "svg_files": len(list((ROOT / "options").rglob("*.svg"))), "directory": str(ROOT)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
