# Demo for the screenshots

`demo.sh` builds a realistic project on one computer, for the pictures in the README
(docs/images/): a separate server on port 44450 in ~/matriline/demo (the real server is
never touched), `fast` (about 1200 quick GFN2-xTB results, 10 inputs with a typo that
end in errors/, and a lab-only cheating client whose forged results are held in weird/),
then `slow` (12 named computers, 30 slots, about 1700 PBEh-3c Opt Freq inputs queued).

The molecules are well-known public ones (caffeine, aspirin, vanillin ...), never anyone's
research. Their geometries come from RDKit (ETKDG + MMFF, three conformers each), made
once outside the repo:

    uv venv venv && uv pip install --python venv/bin/python rdkit
    venv/bin/python lab/demo/mkmol.py <folder>

Screenshots: headless Chromium on `matriline-server web` (`--timeout=7000`; the live pages
refresh by themselves) and the terminal views rendered from their real output.
