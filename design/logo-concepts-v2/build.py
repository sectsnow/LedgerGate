from pathlib import Path
import importlib.util
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parent
PREVIOUS = ROOT.parent / "logo-concepts-v1"
spec = importlib.util.spec_from_file_location("ledgergate_logo_base", PREVIOUS / "build.py")
base = importlib.util.module_from_spec(spec)
spec.loader.exec_module(base)


def pixels():
    rows = ["PP...AAAAAA.", "PP..AA....AA", "PP..AA......", "PP..AA......", "PP..AA..AAAA", "PP..AA....AA", "PP..AA....AA", "PP..AA....AA", "PP..AA....AA", "PP...AAAAAA.", "PPPPPPP.....", "PPPPPPP...AA"]
    return '<g shape-rendering="crispEdges">' + "".join(f'<rect x="{12 + x * 6}" y="{12 + y * 6}" width="6" height="6" fill="{"INK" if cell == "P" else "COLOR"}"/>' for y, row in enumerate(rows) for x, cell in enumerate(row) if cell != ".") + '</g>'


MARKS = [
    {
        "id": "11", "slug": "signature", "name": "手写签名", "idea": "自由笔画 · 有个人作品的温度", "color": "#CC8B67", "ink": "#364542", "dark": "#E6B296", "dark_ink": "#E6EEE6",
        "art": '''<g fill="none" stroke-linecap="round" stroke-linejoin="round"><path stroke="INK" stroke-width="5.8" d="M15 73C27 57 46 21 36 14 25 7 15 29 20 42 25 56 43 50 48 57 53 65 40 76 23 77L12 77"/><path stroke="INK" stroke-width="5.4" d="M79 32C69 17 51 23 49 39 47 52 59 58 69 49L81 36C75 53 71 72 60 81 51 88 42 82 47 76 53 69 70 68 85 71"/><path stroke="COLOR" stroke-width="3.2" d="M12 86C31 81 56 83 78 87"/></g>''',
    },
    {
        "id": "12", "slug": "chinese-seal", "name": "中式方印", "idea": "门与账的篆刻构图 · 温润朱砂红", "color": "#B75C4B", "ink": "#B75C4B", "dark": "#F0AA98", "dark_ink": "#F0AA98",
        "art": '''<defs><mask id="seal" maskUnits="userSpaceOnUse" x="0" y="0" width="96" height="96"><rect width="96" height="96" fill="white"/><g fill="none" stroke="black" stroke-width="4.6" stroke-linecap="square" stroke-linejoin="miter"><path d="M21 25 24 29M20 35V72M29 26H43V72H36M53 29H63V57M54 29V57M54 64 50 72M61 64 66 71M78 26 69 37M68 44H82M72 31V70L79 67M73 50 83 66"/></g><path fill="black" d="M7 61H12V66H7Z M84 23H90V29H84Z M31 7H36V11H31Z"/></mask></defs><rect x="8" y="8" width="80" height="80" rx="5" fill="INK" mask="url(#seal)"/>''',
    },
    {
        "id": "13", "slug": "pixel-terminal", "name": "像素终端", "idea": "8-bit 字母块 · 复古开发者气质", "color": "#35B66C", "ink": "#236A45", "dark": "#B3ED9A", "dark_ink": "#65C28B",
        "art": pixels(),
    },
    {
        "id": "14", "slug": "wireframe", "name": "极简线框", "idea": "细线 LG 与接口括号 · 轻巧克制", "color": "#419DB0", "ink": "#345B68", "dark": "#83D3DD", "dark_ink": "#CADFE7",
        "art": '''<g fill="none" stroke-linecap="round" stroke-linejoin="round" stroke-width="3.8"><path stroke="INK" d="M24 13H13V83H24M72 13H83V83H72M29 27V69H47M68 33C59 23 47 28 47 42V56C47 70 68 73 68 58V49H57"/><path stroke="COLOR" d="M28 83H41M55 13H68"/></g>''',
    },
    {
        "id": "15", "slug": "gateway-badge", "name": "网关徽章", "idea": "盾形章徽 · 稳定、守护与秩序", "color": "#C69C4A", "ink": "#30465F", "dark": "#E9C574", "dark_ink": "#DAE2EC",
        "art": '''<path fill="INK" fill-rule="evenodd" d="M48 7 83 20V47C83 68 67 83 48 91 29 83 13 68 13 47V20Z M48 15 21 26V47C21 64 33 76 48 83 63 76 75 64 75 47V26Z"/><path fill="COLOR" d="M48 24 51 31 58 34 51 37 48 44 45 37 38 34 45 31Z"/><path fill="INK" d="M32 69V46H64V69H54V56H42V69Z"/>''',
    },
    {
        "id": "16", "slug": "soft-bloom", "name": "柔和花瓣", "idea": "有机曲线汇聚 · 轻盈、亲和", "color": "#9B89D8", "ink": "#E1877C", "dark": "#CCBCEB", "dark_ink": "#F3B6A7",
        "art": '''<path fill="INK" d="M48 46C22 40 18 19 34 14 51 10 59 29 48 46Z"/><path fill="COLOR" d="M50 47C56 21 78 18 83 34 88 51 67 59 50 47Z"/><path fill="INK" d="M48 50C74 56 78 77 62 82 45 86 37 67 48 50Z"/><path fill="COLOR" d="M46 49C40 75 18 78 13 62 8 45 29 37 46 49Z"/>''',
    },
    {
        "id": "17", "slug": "vintage-ticket", "name": "复古票据", "idea": "票据缺口与 LG 暗记 · 老派账房感", "color": "#C29D6C", "ink": "#765B45", "dark": "#E1BC86", "dark_ink": "#DDC6A8",
        "art": '''<defs><mask id="ticket" maskUnits="userSpaceOnUse" x="0" y="0" width="96" height="96"><rect width="96" height="96" fill="white"/><circle cx="10" cy="48" r="8" fill="black"/><circle cx="86" cy="48" r="8" fill="black"/><path fill="black" d="M23 25H26V30H23Z M23 37H26V42H23Z M23 49H26V54H23Z M23 61H26V66H23Z"/><g fill="none" stroke="black" stroke-width="4.8" stroke-linecap="square"><path d="M35 32V64H47M70 34C59 27 52 34 52 48S59 69 71 61V48H63"/></g></mask></defs><rect x="10" y="18" width="76" height="60" rx="5" fill="INK" mask="url(#ticket)"/><path stroke="COLOR" stroke-width="2" d="M31 24H76M31 72H76"/>''',
    },
    {
        "id": "18", "slug": "isometric-ledger", "name": "立体纸片", "idea": "等距视角与柔和渐变 · 桌面应用感", "color": "#64C5D2", "ink": "#6A5BDA", "dark": "#91DDE5", "dark_ink": "#C1B7F0",
        "art": '''<defs><linearGradient id="paper" x1="15" y1="15" x2="84" y2="60" gradientUnits="userSpaceOnUse"><stop stop-color="COLOR"/><stop offset="1" stop-color="INK"/></linearGradient></defs><path fill="INK" opacity=".12" d="M12 66 48 87 87 64 51 43Z"/><path fill="INK" d="M14 38V57L48 78V58Z"/><path fill="COLOR" d="M48 58 84 34V54L48 78Z"/><path fill="url(#paper)" fill-rule="evenodd" d="M14 38 50 14 84 34 48 58Z M32 35 52 22 58 26 38 39Z M43 42 63 29 69 33 49 46Z"/>''',
    },
    {
        "id": "19", "slug": "ledger-cat", "name": "账房猫", "idea": "吉祥物路线 · 轻松、有记忆点", "color": "#526B7D", "ink": "#D69B62", "dark": "#C8DBE5", "dark_ink": "#E9BD8F",
        "art": '''<defs><mask id="cat-face" maskUnits="userSpaceOnUse" x="0" y="0" width="96" height="96"><rect width="96" height="96" fill="white"/><ellipse cx="33" cy="52" rx="4" ry="6" fill="black"/><ellipse cx="63" cy="52" rx="4" ry="6" fill="black"/><path fill="black" d="M43 63H53L48 69Z"/></mask></defs><path fill="INK" d="M18 39 14 12 37 24Q48 20 59 24L82 12 78 39C91 67 72 84 48 84S5 67 18 39Z" mask="url(#cat-face)"/><path stroke="COLOR" stroke-width="4" stroke-linecap="round" d="M40 32H56M40 40H51"/>''',
    },
    {
        "id": "20", "slug": "stencil-wordmark", "name": "纯字标", "idea": "定制字母结构 · 品牌名直接成为标志", "color": "#5B7399", "ink": "#253243", "dark": "#91B5EC", "dark_ink": "#E0E8F3",
        "art": '''<path fill="INK" d="M13 15H28V67H50V82H13Z"/><path fill="COLOR" d="M82 15H59Q40 15 40 34V63Q40 82 59 82H82V46H60V59H68V68H59Q54 68 54 62V35Q54 29 60 29H82Z"/>''',
    },
]

original_svg = base.svg


def svg(mark, variant, instance="mark"):
    raw = original_svg(mark, variant, instance)
    return raw.replace("#DCE8FB", mark["dark_ink"]) if variant == "dark" else raw


def wordmark(variant, instance="wordmark"):
    mark = MARKS[-1]
    ink, color = mark["ink"], mark["color"]
    if variant == "dark":
        ink, color = mark["dark_ink"], mark["dark"]
    elif variant == "mono":
        ink = color = "#253243"
    elif variant == "white":
        ink = color = "#FFFFFF"
    alphabet = {
        "L": "M0 0H10V54H38V64H0Z",
        "E": "M0 0H38V10H10V27H32V37H10V54H38V64H0Z",
        "D": "M0 0H22Q40 0 40 20V44Q40 64 22 64H0Z M10 10V54H20Q30 54 30 44V20Q30 10 20 10Z",
        "G": "M40 0H18Q0 0 0 18V46Q0 64 18 64H40V29H22V39H30V54H18Q10 54 10 46V18Q10 10 18 10H40Z",
        "R": "M0 0H24Q40 0 40 16V22Q40 33 28 34L42 64H30L17 35H10V64H0Z M10 10V25H22Q30 25 30 20V16Q30 10 22 10Z",
        "A": "M0 64 15 0H29L44 64H33L29 46H15L11 64Z M17 36H27L22 13Z",
        "T": "M0 0H42V10H26V64H16V10H0Z",
    }
    letters = "".join(f'<path transform="translate({index * 49 + 8} 12)" fill="{ink if index < 6 else color}" fill-rule="evenodd" d="{alphabet[letter]}"/>' for index, letter in enumerate("LEDGERGATE"))
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 505 88" role="img" aria-labelledby="lg20-{instance}-{variant}"><title id="lg20-{instance}-{variant}">LedgerGate 定制字标</title>{letters}</svg>'


EXTRA_CSS = '''
.concept[data-design="11"] .wordmark {font-family:'DejaVu Serif',Georgia,serif;font-style:italic;font-size:43px;font-weight:400;letter-spacing:-2px;color:#364542}
.concept[data-design="12"] .wordmark {font-family:'DejaVu Serif',Georgia,serif;font-size:36px;letter-spacing:1px;font-weight:400;color:#B75C4B;text-transform:uppercase}
.concept[data-design="13"] .wordmark {font-family:'DejaVu Sans Mono',monospace;font-size:35px;font-weight:700;letter-spacing:-1.5px;text-transform:uppercase;color:#236A45}
.concept[data-design="14"] .wordmark {font-size:37px;font-weight:350;letter-spacing:.5px;text-transform:lowercase;color:#345B68}
.concept[data-design="15"] .wordmark {font-family:'DejaVu Serif',Georgia,serif;font-size:36px;font-weight:700;letter-spacing:.6px;text-transform:uppercase;color:#30465F}
.concept[data-design="16"] .wordmark {font-size:45px;font-weight:500;letter-spacing:-1.8px;text-transform:lowercase;color:#806C95}
.concept[data-design="17"] .wordmark {font-family:'DejaVu Serif',Georgia,serif;font-size:40px;font-weight:700;letter-spacing:-1.7px;color:#765B45}
.concept[data-design="18"] .wordmark {font-size:45px;font-style:italic;font-weight:500;letter-spacing:-1.7px;color:#6A5BDA}
.concept[data-design="19"] .wordmark {font-size:44px;font-weight:500;letter-spacing:-1.7px;color:#526B7D}
.custom-wordmark {width:505px;max-width:100%}.custom-wordmark svg{display:block;width:100%;height:auto}
body.dark .concept .wordmark {color:var(--ink)}
@media(max-width:1000px){.concept .identity .wordmark{font-size:32px}.custom-wordmark{width:410px}}
@media(max-width:640px){.concept .identity .wordmark{font-size:28px}.concept[data-design="20"] .identity{min-height:150px}}
'''


def main():
    base.ROOT, base.MARKS, base.svg = ROOT, MARKS, svg
    base.main()
    for mode in ["light", "dark"]:
        raw = base.sheet(mode)
        for mark in MARKS:
            raw = raw.replace('<article class="concept">', f'<article class="concept" data-design="{mark["id"]}">', 1)
        variant = "dark" if mode == "dark" else "light"
        old = '<div class="identity"><div class="big-mark">' + svg(MARKS[-1], variant, "main") + '</div><div class="wordmark">Ledger<span>Gate</span></div></div>'
        replacement = '<div class="identity"><div class="custom-wordmark">' + wordmark(variant, "preview") + '</div></div>'
        assert old in raw
        raw = raw.replace(old, replacement)
        raw = raw.replace('IDENTITY EXPLORATION', 'STYLE EXPLORATION').replace('10 款 Logo 概念', '10 种不同风格').replace('10 个不同图形方向 · 编号 01—10', '第二组风格探索 · 编号 11—20').replace('Concept Collection 01', 'Concept Collection 02')
        raw = raw.replace('</style>', EXTRA_CSS + '</style>')
        (ROOT / f"overview-{mode}.html").write_text(raw, encoding="utf8")
    target = ROOT / "options/20-stencil-wordmark"
    for variant in ["light", "dark", "mono", "white"]:
        raw = wordmark(variant)
        ET.fromstring(raw)
        (target / f"wordmark-{variant}.svg").write_text(raw + "\n", encoding="utf8")
    readme = (ROOT / "README.md").read_text()
    readme = readme.replace('十款独立设计的矢量概念，供选择。', '第二组：十种不同风格的矢量方案，编号 11—20，供选择。')
    readme += '\n20 为纯字标方案，wordmark-*.svg 是完整品牌字标；light.svg / dark.svg 是小尺寸 LG 备用标。完整字标的字母由独立路径构成，不依赖字体。\n'
    (ROOT / "README.md").write_text(readme, encoding="utf8")
    renderer = (PREVIOUS / "render.cjs").read_text().replace("page.locator('.big-mark svg').count()", "page.locator('.identity > div > svg').count()")
    extra_exports = '''
    for (const theme of ['light', 'dark']) {
      const folder = path.join(root, 'options/20-stencil-wordmark');
      const raw = fs.readFileSync(path.join(folder, 'wordmark-' + theme + '.svg'));
      const png = await page.evaluate(async encoded => {
        const image = new Image(); image.src = 'data:image/svg+xml;base64,' + encoded; await image.decode();
        const canvas = document.createElement('canvas'); canvas.width = 1024; canvas.height = 178;
        canvas.getContext('2d').drawImage(image, 0, 0, 1024, 178);
        return canvas.toDataURL('image/png').split(',')[1];
      }, raw.toString('base64'));
      fs.writeFileSync(path.join(folder, 'wordmark-' + theme + '.png'), Buffer.from(png, 'base64'));
    }
'''
    renderer = renderer.replace("    assert.deepEqual(errors, []);", extra_exports + "    assert.deepEqual(errors, []);")
    renderer = renderer.replace("svg_files: 40, transparent_pngs: renderChecks.length", "svg_files: 44, transparent_pngs: renderChecks.length + 2")
    (ROOT / "render.cjs").write_text(renderer, encoding="utf8")


if __name__ == "__main__":
    main()
