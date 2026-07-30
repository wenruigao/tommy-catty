#!/usr/bin/env python3
"""Remove the 'AI 生成' watermark from the top-left corner of the
Tommy-Cat icon by filling with sampled background colors."""
from pathlib import Path
from PIL import Image

SRC = Path("/Users/wenruigao/.qclaw/workspace/generated-images/img_813343917eb839c1.png")
DST = Path("/Users/wenruigao/.qclaw/workspace/tommy-cat-icon-1024.png")

# Watermark is at x=8-111, y=8-36 (104x29 px)
# Sample row y=6 (just above watermark) as source fill
SAMPLE_Y = 6
WM_X1, WM_Y1 = 6, 8   # margin included
WM_X2, WM_Y2 = 113, 36

im = Image.open(SRC).convert("RGB")
w, h = im.size
print(f"Image size: {w}x{h}")

# Crop a strip just above the watermark area
strip = im.crop((WM_X1, SAMPLE_Y, WM_X2, SAMPLE_Y + 1))  # 1px tall
print(f"Strip size: {strip.size}")

# Vertically stretch to fill the watermark area
overlay_height = WM_Y2 - WM_Y1 + 1
overlay = strip.resize((strip.width, overlay_height), Image.NEAREST)
im.paste(overlay, (WM_X1, WM_Y1))

im.save(DST, "PNG", optimize=True)
print(f"Saved clean icon -> {DST}")
