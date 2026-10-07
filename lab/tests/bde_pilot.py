#!/usr/bin/env python3
"""bde_pilot.py <outdir> - inputs for a small pilot of campaign idea 4 (docs/CAMPAIGN_IDEAS.md):
O-H bond dissociation enthalpies of para-substituted phenols, parent + phenoxyl radical
+ H atom, r2SCAN-3c Opt Freq (stdlib only; no RDKit needed for these planar molecules).

BDE(O-H) = H(phenoxyl) + H(H atom) - H(phenol), from "Total Enthalpy" of each output.
Substituent trend to expect: electron donors (NH2, OH, OMe, CH3) lower the BDE, acceptors
(CN, NO2) raise it (Wright et al., JACS 2001, doi:10.1021/ja002455u; ALFABET,
St. John et al., Nat. Commun. 2020, doi:10.1038/s41467-020-16201-z).

Geometries are idealized (regular hexagon, standard bond lengths); ORCA optimizes them.
"""
import math
import os
import sys

CC, CH, CO, OH = 1.39, 1.08, 1.36, 0.96
METHOD = "! r2SCAN-3c Opt Freq"

# substituent at the para carbon: list of (symbol, distance along the C-X axis,
# offset perpendicular in the ring plane) relative to the para carbon
SUBST = {
    "H": [("H", CH, 0.0)],
    "F": [("F", 1.35, 0.0)],
    "Cl": [("Cl", 1.74, 0.0)],
    "CH3": [("C", 1.51, 0.0), ("H", 1.51 + 0.36, 1.03), ("H", 1.51 + 0.36, -0.51), ("H", 1.51 + 0.36, -0.51)],
    # pyramidal start: a planar NH2 optimizes into the planar saddle point (1 imaginary
    # mode, seen in the first lab run)
    "NH2": [("N", 1.40, 0.0), ("H", 1.40 + 0.38, 0.82), ("H", 1.40 + 0.38, -0.82)],
    "OH": [("O", CO, 0.0), ("H", CO + 0.32, 0.91)],
    "CN": [("C", 1.44, 0.0), ("N", 1.44 + 1.16, 0.0)],
    "NO2": [("N", 1.47, 0.0), ("O", 1.47 + 0.62, 1.08), ("O", 1.47 + 0.62, -1.08)],
}


def phenol(subs, radical):
    """subs: {ring position 2..6: substituent}. Ring in the xy plane, C1 (bearing O) on
    +x; position p sits at angle 60*(p-1) degrees. Unlisted positions carry H."""
    atoms = []
    for k in range(6):
        a = math.radians(60 * k)
        u, v = (math.cos(a), math.sin(a)), (-math.sin(a), math.cos(a))
        cx, cy = CC * u[0], CC * u[1]
        atoms.append(("C", cx, cy, 0.0))
        if k == 0:
            continue
        sub = subs.get(k + 1, "H")
        for i, (sym, d, off) in enumerate(SUBST[sub]):
            z = 0.0
            if sub == "CH3" and i > 1:  # two out-of-plane methyl hydrogens
                z = 0.89 if i == 2 else -0.89
            if sub == "NH2" and i > 0:  # both amine hydrogens on the same side
                z = 0.35
            atoms.append((sym, cx + d * u[0] + off * v[0], cy + d * u[1] + off * v[1], z))
    atoms.append(("O", CC + CO, 0.0, 0.0))
    if not radical:
        atoms.append(("H", CC + CO + OH * math.cos(math.radians(70)), OH * math.sin(math.radians(70)), 0.0))
    return atoms


def label(subs):
    return "_".join(f"{p}-{x}" for p, x in sorted(subs.items())) or "H"


def write(path, title, charge, mult, atoms):
    with open(path, "w") as f:
        f.write(f"# {title}\n{METHOD}\n")
        if mult > 1:
            f.write("! UKS\n")
        f.write(f"* xyz {charge} {mult}\n")
        for s, x, y, z in atoms:
            f.write(f"  {s:<2} {x:10.5f} {y:10.5f} {z:10.5f}\n")
        f.write("*\n")


def main():
    """bde_pilot.py <outdir> [pilot|positions]
    pilot:     para-substituted phenols (first lab run)
    positions: ortho/meta/para series plus methylated (BHT-like) cores"""
    out = sys.argv[1] if len(sys.argv) > 1 else "bde_pilot"
    kind = sys.argv[2] if len(sys.argv) > 2 else "pilot"
    os.makedirs(out, exist_ok=True)
    sets = [{4: x} for x in SUBST]
    if kind == "positions":
        sets = [{p: x} for x in SUBST if x != "H" for p in (2, 3)]
        sets += [{2: "CH3", 6: "CH3"}, {2: "CH3", 4: "CH3", 6: "CH3"}, {3: "OH", 5: "OH"},
                 {2: "OCH3"} if "OCH3" in SUBST else {2: "F", 6: "F"}, {2: "NH2", 4: "CH3"}]
    n = 0
    for subs in sets:
        name = label(subs) if kind == "positions" or 4 not in subs or len(subs) > 1 else subs[4]
        write(os.path.join(out, f"phenol_{name}.inp"), f"phenol {label(subs)}", 0, 1, phenol(subs, False))
        write(os.path.join(out, f"phenoxyl_{name}.inp"), f"phenoxyl radical {label(subs)}", 0, 2, phenol(subs, True))
        n += 2
    if kind == "pilot":
        # one atom: nothing to optimize and no vibrations; H(298 K) = E + 5/2 RT (0.002360 Eh)
        with open(os.path.join(out, "h_atom.inp"), "w") as f:
            f.write("# hydrogen atom\n! r2SCAN-3c UKS\n* xyz 0 2\n  H 0.0 0.0 0.0\n*\n")
        n += 1
    print(f"{n} inputs in {out}/")


if __name__ == "__main__":
    main()
