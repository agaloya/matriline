from rdkit import Chem
from rdkit.Chem import AllChem
import sys, os
M = {
 # drugs and natural products everyone has heard of
 "caffeine":"Cn1cnc2c1c(=O)n(C)c(=O)n2C", "aspirin":"CC(=O)Oc1ccccc1C(=O)O",
 "paracetamol":"CC(=O)Nc1ccc(O)cc1", "ibuprofen":"CC(C)Cc1ccc(cc1)C(C)C(=O)O",
 "nicotine":"CN1CCCC1c1cccnc1", "serotonin":"NCCc1c[nH]c2ccc(O)cc12",
 "dopamine":"NCCc1ccc(O)c(O)c1", "adrenaline":"CNCC(O)c1ccc(O)c(O)c1",
 "melatonin":"COc1ccc2[nH]cc(CCNC(C)=O)c2c1", "theobromine":"Cn1cnc2c1c(=O)[nH]c(=O)n2C",
 "histamine":"NCCc1c[nH]cn1", "lidocaine":"CCN(CC)CC(=O)Nc1c(C)cccc1C",
 "salicylic_acid":"OC(=O)c1ccccc1O", "benzocaine":"CCOC(=O)c1ccc(N)cc1",
 # flavours and fragrances
 "vanillin":"COc1cc(C=O)ccc1O", "menthol":"CC(C)C1CCC(C)CC1O", "eugenol":"COc1cc(CC=C)ccc1O",
 "thymol":"Cc1ccc(C(C)C)c(O)c1", "carvone":"CC1=CCC(CC1=O)C(C)=C", "limonene":"CC1=CCC(CC1)C(C)=C",
 "cinnamaldehyde":"O=CC=Cc1ccccc1", "coumarin":"O=c1ccc2ccccc2o1", "camphor":"CC1(C)C2CCC1(C)C(=O)C2",
 "citral":"CC(C)=CCCC(C)=CC=O", "anethole":"COc1ccc(C=CC)cc1",
 # small building blocks
 "phenol":"Oc1ccccc1", "benzene":"c1ccccc1", "naphthalene":"c1ccc2ccccc2c1", "pyridine":"c1ccncc1",
 "indole":"c1ccc2[nH]ccc2c1", "ethanol":"CCO", "acetone":"CC(C)=O", "urea":"NC(N)=O",
 "glycine":"NCC(=O)O", "alanine":"CC(N)C(=O)O", "serine":"NC(CO)C(=O)O", "tryptophan":"NC(Cc1c[nH]c2ccccc12)C(=O)O",
 "glucose":"OCC1OC(O)C(O)C(O)C1O", "acetic_acid":"CC(=O)O", "benzoic_acid":"OC(=O)c1ccccc1",
}
out = sys.argv[1]
for name, smi in M.items():
    m = Chem.AddHs(Chem.MolFromSmiles(smi))
    for seed in range(3):  # three conformers each: variety for the queue
        mm = Chem.Mol(m)
        if AllChem.EmbedMolecule(mm, randomSeed=11 + seed) != 0:
            continue
        AllChem.MMFFOptimizeMolecule(mm)
        c = mm.GetConformer()
        lines = [f"{a.GetSymbol():2s} {c.GetAtomPosition(i).x:10.5f} {c.GetAtomPosition(i).y:10.5f} {c.GetAtomPosition(i).z:10.5f}" for i, a in enumerate(mm.GetAtoms())]
        open(os.path.join(out, f"{name}_c{seed+1}.xyz"), "w").write("\n".join(lines) + "\n")
print(len(os.listdir(out)), "geometries")
