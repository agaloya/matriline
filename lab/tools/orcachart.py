#!/usr/bin/env python3
"""orcachart.py OUT.svg - the "which ORCA 6.1.1 file to download" chart of INSTALL.md
section 2, as a fixed image (user: no zooming like the mermaid chart, and every build shown).
Standard library only; the layout is by hand: questions on the left, files on the right."""
import sys
from xml.sax.saxutils import escape

COL = [30, 330, 720, 1100]          # x of each column
W = [150, 220, 230, 500]            # box widths
ROW0, STEP, H = 110, 66, 48         # first row's centre, row spacing, box height
FILE_FILL, FILE_LINE = "#e9f7ef", "#3c8d5a"
Q_FILL, Q_LINE = "#e8f0fe", "#4a6fa5"


def y(row):
    return ROW0 + row * STEP


out = []


def box(col, cy, title, sub, fill, line, mono_title=False, note=""):
    x, w = COL[col], W[col]
    out.append(f'<rect x="{x}" y="{cy - H / 2}" width="{w}" height="{H}" rx="8" fill="{fill}" stroke="{line}" stroke-width="1.5"/>')
    fam = 'font-family="DejaVu Sans Mono, Menlo, Consolas, monospace"' if mono_title else ""
    ty = cy - 4 if sub else cy + 5
    out.append(f'<text x="{x + 12}" y="{ty}" font-size="14" font-weight="bold" {fam}>{escape(title)}</text>')
    if sub:
        out.append(f'<text x="{x + 12}" y="{cy + 14}" font-size="12" fill="#444">{escape(sub)}</text>')
    if note:
        out.append(f'<text x="{x + w - 10}" y="{cy - 4}" font-size="11" fill="{line}" text-anchor="end" font-weight="bold">{escape(note)}</text>')


def edge(c1, y1, c2, y2, label):
    x1, x2 = COL[c1] + W[c1], COL[c2]
    xm = x1 + 18
    out.append(f'<path d="M{x1},{y1} H{xm} V{y2} H{x2 - 2}" fill="none" stroke="#666" stroke-width="1.4" marker-end="url(#a)"/>')
    out.append(f'<text x="{xm + 8}" y="{y2 - 6}" font-size="12" fill="#222">{escape(label)}</text>')


def chart():
    rows = 10
    height = y(rows - 1) + 70
    width = COL[3] + W[3] + 30
    out.append(f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}" font-family="DejaVu Sans, Helvetica, Arial, sans-serif">')
    out.append('<defs><marker id="a" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0,0 L10,5 L0,10 z" fill="#666"/></marker></defs>')
    out.append(f'<rect width="{width}" height="{height}" fill="#ffffff"/>')
    out.append('<text x="30" y="36" font-size="20" font-weight="bold">Which ORCA 6.1.1 file to download</text>')
    out.append('<text x="30" y="60" font-size="13" fill="#444">ORCA forum (orcaforum.kofo.mpg.de) &gt; Filebase &gt; ORCA 6.1.1. Every file name starts with orca_6_1_1_ (Windows: Orca.6.1.1.). Matriline accepts all of them.</text>')
    # files (right column), one per row
    files = [
        ("linux_x86-64_shared_openmpi418_avx2.tar.xz", "16-24 % faster than the one below, same results", "best"),
        ("linux_x86-64_shared_openmpi418.tar.xz", "for processors without AVX2 (some old or cheap ones)", ""),
        ("linux_arm64_shared_openmpi418.tar.xz", "ARM computers (e.g. Ampere servers, Raspberry Pi 5)", ""),
        ("linux_riscv_shared_openmpi418.tar.xz", "RISC-V computers", ""),
        ("macosx_arm64_openmpi411.tar.bz2", "if unsure, this one: about the same in most cases, and smaller (1.7 GB)", "recommended"),
        ("macosx_arm64_openblas_openmpi411.tar.bz2", "the same program with the OpenBLAS maths library (8.5 GB)", ""),
        ("macosx_intel_openmpi411.tar.bz2", "Intel Macs", ""),
        ("Win64_msmpi.zip", "an installer: Typical or Full give the same ORCA", "most people"),
        ("Win64_msmpi.zip + Win64_autoci.zip", "AUTOCI unzipped into the ORCA folder", ""),
        ("Win64_msmpi.zip + Win64_autoci_msmpi.zip", "the same for multi-core jobs (needs MS-MPI); both AUTOCI zips also work", ""),
    ]
    for i, (f, sub, note) in enumerate(files):
        box(3, y(i), f, sub, FILE_FILL, FILE_LINE, mono_title=True, note=note)
    # questions
    box(0, y(4), "Your system?", "", Q_FILL, Q_LINE)
    box(1, y(1.5), "Processor?", "uname -m", Q_FILL, Q_LINE)
    box(2, y(0.5), "AVX2?", "grep -o -m1 avx2 /proc/cpuinfo", Q_FILL, Q_LINE)
    box(1, y(5), "Chip?", "Apple menu > About This Mac", Q_FILL, Q_LINE)
    box(2, y(4.5), "Which build?", "both are accepted", Q_FILL, Q_LINE)
    box(1, y(7.5), "AUTOCI methods?", "only if your inputs use them", Q_FILL, Q_LINE)
    box(2, y(8.5), "On several cores?", "max_cores_per_job > 1", Q_FILL, Q_LINE)
    # edges
    edge(0, y(4), 1, y(1.5), "Linux")
    edge(0, y(4), 1, y(5), "macOS")
    edge(0, y(4), 1, y(7.5), "Windows")
    edge(1, y(1.5), 2, y(0.5), "x86_64 (Intel, AMD)")
    edge(1, y(1.5), 3, y(2), "aarch64 (ARM)")
    edge(1, y(1.5), 3, y(3), "riscv64 (RISC-V)")
    edge(2, y(0.5), 3, y(0), "yes (prints avx2)")
    edge(2, y(0.5), 3, y(1), "no (prints nothing)")
    edge(1, y(5), 2, y(4.5), "Apple M1, M2, ...")
    edge(1, y(5), 3, y(6), "Intel")
    edge(2, y(4.5), 3, y(4), "plain")
    edge(2, y(4.5), 3, y(5), "OpenBLAS")
    edge(1, y(7.5), 3, y(7), "no (DFT, Opt, Freq...)")
    edge(1, y(7.5), 2, y(8.5), "yes")
    edge(2, y(8.5), 3, y(8), "no (one core)")
    edge(2, y(8.5), 3, y(9), "yes")
    out.append("</svg>")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    chart()
    with open(sys.argv[1], "w") as f:
        f.write("\n".join(out) + "\n")
