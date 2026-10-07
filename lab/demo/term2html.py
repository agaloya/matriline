# term2html.py title < ansi-text > page.html: a terminal window as an HTML page (for screenshots)
import sys, re, html
bg = {"40":"#1e1e1e","41":"#e34935","42":"#22a06b","43":"#e2b203","44":"#3b82f6","45":"#a855f7","46":"#06b6d4","47":"#9aa5b1","100":"#6b7280","104":"#ec4899"}
fg = {"31":"#ff6b6b","32":"#4ade80","33":"#facc15","34":"#60a5fa","35":"#c084fc","36":"#22d3ee","90":"#9ca3af"}
title = sys.argv[1] if len(sys.argv) > 1 else "terminal"
text = sys.stdin.read().replace("\r", "")
out, style = [], {}
for part in re.split(r"(\x1b\[[0-9;]*m)", text):
    m = re.match(r"\x1b\[([0-9;]*)m", part)
    if m:
        for c in (m.group(1) or "0").split(";"):
            if c in ("0", ""): style = {}
            elif c == "1": style["font-weight"] = "bold"
            elif c in bg: style["background"] = bg[c]
            elif c in fg: style["color"] = fg[c]
        continue
    if not part: continue
    s = html.escape(part)
    out.append(f'<span style="{";".join(k+":"+v for k,v in style.items())}">{s}</span>' if style else s)
print(f"""<!doctype html><meta charset=utf-8><style>
body{{margin:0;background:#e9eef0;font-family:system-ui;padding:24px}}
.win{{background:#1e1e1e;border-radius:10px;box-shadow:0 8px 30px #0003;display:inline-block;min-width:900px}}
.bar{{background:#2d2d2d;border-radius:10px 10px 0 0;padding:8px 14px;color:#bbb;font-size:13px}}
.bar i{{display:inline-block;width:12px;height:12px;border-radius:6px;margin-right:6px;vertical-align:middle}}
pre{{margin:0;padding:14px 18px;color:#e6e6e6;font:14px/1.35 'DejaVu Sans Mono',monospace}}
</style><div class=win><div class=bar><i style=background:#ff5f56></i><i style=background:#ffbd2e></i><i style=background:#27c93f></i>&nbsp; {html.escape(title)}</div><pre>{"".join(out)}</pre></div>""")
