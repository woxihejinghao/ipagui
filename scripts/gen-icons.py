#!/usr/bin/env python3
"""从一张方形源图生成 IPA GUI 的应用图标。

为什么要单独一个脚本：
  * Wails 构建 macOS 版时会拿 build/appicon.png 现场编码成 iconfile.icns
    （见 wails v2 pkg/commands/build/packager.go 的 processDarwinIcon），
    所以 macOS 的图标只需维护 build/appicon.png 这一张母版；
  * Windows 的 build/windows/icon.ico 只在**缺失时**才会由 Wails 从 appicon.png
    生成（generateIcoFile 会先判断文件存在），旧 ico 不删就永远不更新；
    这里直接按 Windows 习惯出多尺寸 ico，避免下次构建还是旧图标；
  * macOS 图标按系统规范做超椭圆（squircle）裁切 + 留白 + 柔和投影，
    直接用方形原图会在 Dock/Finder 里显示成一个硬边方块。

用法：
  python3 scripts/gen-icons.py [源图]

依赖：只需 Pillow（系统的 python3 就够，不用装 numpy）。
"""

from __future__ import annotations

import math
import sys
from pathlib import Path

from PIL import Image, ImageDraw, ImageFilter

ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / "build" / "appicon-source.jpg"

CANVAS = 1024          # macOS 图标画布
CONTENT = 824          # Big Sur 之后的规范：四周各留 100px
SQUIRCLE_N = 5.0       # 超椭圆指数，n=5 在 45° 方向与半径 185 的圆角矩形吻合
SUPERSAMPLE = 4        # 掩罩先放大再缩回，用面积平均拿干净的抗锯齿
SHADOW_BLUR = 22
SHADOW_OFFSET = 12
SHADOW_ALPHA = 0.28

WIN_SIZES = [16, 24, 32, 48, 64, 128, 256]


def squircle_mask(size: int, content: int) -> Image.Image:
    """生成居中的超椭圆遮罩（L 模式，0~255）。

    超椭圆 |x/a|^n + |y/a|^n = 1 的参数方程是
        x = a·sgn(cos t)·|cos t|^(2/n)
        y = a·sgn(sin t)·|sin t|^(2/n)
    放大 4 倍画成多边形再 BOX 缩回，等于做面积平均，边缘比直接
    drawing 圆角矩形更接近 macOS 的 squircle。
    """
    big = size * SUPERSAMPLE
    a = content * SUPERSAMPLE / 2.0
    c = big / 2.0
    steps = 4096
    pts = []
    for i in range(steps):
        t = 2 * math.pi * i / steps
        ct, st = math.cos(t), math.sin(t)
        pts.append((
            c + a * math.copysign(abs(ct) ** (2.0 / SQUIRCLE_N), ct),
            c + a * math.copysign(abs(st) ** (2.0 / SQUIRCLE_N), st),
        ))
    mask = Image.new("L", (big, big), 0)
    ImageDraw.Draw(mask).polygon(pts, fill=255)
    # BOX = 面积平均，比 LANCZOS 更适合二值掩罩（不会出现振铃黑边）
    return mask.resize((size, size), Image.BOX)


def build_macos_icon(src: Image.Image, out: Path) -> None:
    content = src.convert("RGBA").resize((CONTENT, CONTENT), Image.LANCZOS)
    mask = squircle_mask(CONTENT, CONTENT)

    layer = Image.new("RGBA", (CANVAS, CANVAS), (0, 0, 0, 0))
    offset = (CANVAS - CONTENT) // 2
    layer.paste(content, (offset, offset), mask)

    # 投影：用图形自身的 alpha 糊一层黑色，整体下移，模拟系统图标的落地感
    alpha = layer.getchannel("A").filter(ImageFilter.GaussianBlur(SHADOW_BLUR))
    alpha = alpha.point(lambda v: int(v * SHADOW_ALPHA))
    shadow = Image.new("RGBA", (CANVAS, CANVAS), (0, 0, 0, 0))
    shadow.putalpha(alpha)

    canvas = Image.new("RGBA", (CANVAS, CANVAS), (0, 0, 0, 0))
    canvas.alpha_composite(shadow, (0, SHADOW_OFFSET))
    canvas.alpha_composite(layer)
    canvas.save(out, optimize=True)
    print(f"macOS  {out.relative_to(ROOT)}  {CANVAS}x{CANVAS}")


def build_windows_icon(src: Image.Image, out: Path) -> None:
    # Windows 任务栏/资源管理器习惯满幅，不裁超椭圆也不留白
    win = src.convert("RGBA")
    out.parent.mkdir(parents=True, exist_ok=True)
    win.save(out, format="ICO", sizes=[(s, s) for s in WIN_SIZES])
    print(f"Windows {out.relative_to(ROOT)}  {'/'.join(map(str, WIN_SIZES))}")


def main() -> int:
    src_path = Path(sys.argv[1]) if len(sys.argv) > 1 else SOURCE
    if not src_path.exists():
        print(f"找不到源图：{src_path}", file=sys.stderr)
        return 1

    # 源图统一存成 960 的 PNG 母本，方便以后只换图重跑本脚本
    src = Image.open(src_path).convert("RGB")
    if src.width != src.height:
        print(f"源图必须是正方形，现在是 {src.width}x{src.height}", file=sys.stderr)
        return 1
    if src_path.resolve() != SOURCE.resolve():
        SOURCE.parent.mkdir(parents=True, exist_ok=True)
        src.save(SOURCE)
        print(f"source  {SOURCE.relative_to(ROOT)}  {src.width}x{src.height}")

    build_macos_icon(src, ROOT / "build" / "appicon.png")
    build_windows_icon(src, ROOT / "build" / "windows" / "icon.ico")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
