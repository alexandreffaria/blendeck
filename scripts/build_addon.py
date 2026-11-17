"""Packs blender/consort into dist/consort-addon.zip, ready for Blender's
"Install from Disk"."""

import os
import sys
import zipfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "blender", "consort")
OUT = os.path.join(ROOT, "dist", "consort-addon.zip")


def main():
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with zipfile.ZipFile(OUT, "w", zipfile.ZIP_DEFLATED) as z:
        for name in sorted(os.listdir(SRC)):
            path = os.path.join(SRC, name)
            if os.path.isfile(path) and (name.endswith(".py") or name.endswith(".toml")):
                z.write(path, name)
    print(OUT)


if __name__ == "__main__":
    sys.exit(main())
