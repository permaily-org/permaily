# Makes the font that the drawings on Home set their bars in.
#
# It has two shapes, each a block as wide as one character of the mono face. A row of covered mail is written
# with one of them in place of every character, its spaces kept, so each word comes out as one black bar. The
# mail itself is never in that row, so a browser that fails to load this font shows x's, not the mail.
#
#   x  the bar of the hero, which lies on paper
#   X  the bar of How it works, which lies over the mail it covers, so it is tall enough to hide a whole word
#
# The widths are read from the mono face, so a bar is exactly one character wide. Run it again if that face
# changes:
#
#   uv run --with fonttools --with brotli site/scripts/bars-font.py

from pathlib import Path

from fontTools.fontBuilder import FontBuilder
from fontTools.pens.ttGlyphPen import TTGlyphPen
from fontTools.ttLib import TTFont

SITE = Path(__file__).resolve().parent.parent
MONO = SITE / "node_modules/@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-normal.woff2"
OUT = SITE / "src/assets/fonts/permaily-bars.woff2"

mono = TTFont(MONO)
upm = mono["head"].unitsPerEm
advance = mono["hmtx"]["x"][0]
hhea, os2 = mono["hhea"], mono["OS/2"]

# Each bar is drawn for the size its drawing sets its mail at, in whole pixels above and below the baseline,
# so its top and bottom land on whole pixels there and stay sharp.
#   x: 10.5px type in a 15px row. 8 above and 1 below, which centres the bar on the row.
#   X: 12px type. 10 above and 3 below, which covers the tallest letter and the deepest tail.
BARS = {"x": (10.5, 8, 1), "X": (12, 10, 3)}
# Each block reaches a little past its own cell, so no seam shows between two of them.
over = round(0.05 * upm)


def block(size, above, below):
    low, high = round(-below / size * upm), round(above / size * upm)
    pen = TTGlyphPen(None)
    pen.moveTo((-over, low))
    pen.lineTo((-over, high))
    pen.lineTo((advance + over, high))
    pen.lineTo((advance + over, low))
    pen.closePath()
    return pen.glyph()


def empty():
    return TTGlyphPen(None).glyph()


font = FontBuilder(upm, isTTF=True)
font.setupGlyphOrder([".notdef", "space", *BARS])
font.setupCharacterMap({0x20: "space", 0xA0: "space", **{ord(name): name for name in BARS}})
font.setupGlyf({".notdef": empty(), "space": empty(), **{name: block(*BARS[name]) for name in BARS}})
font.setupHorizontalMetrics(
    {".notdef": (advance, 0), "space": (advance, 0), **{name: (advance, -over) for name in BARS}}
)
font.setupHorizontalHeader(ascent=hhea.ascent, descent=hhea.descent, lineGap=hhea.lineGap)
font.setupNameTable({"familyName": "Permaily Bars", "styleName": "Regular"})
font.setupOS2(
    version=4,
    sTypoAscender=os2.sTypoAscender,
    sTypoDescender=os2.sTypoDescender,
    sTypoLineGap=os2.sTypoLineGap,
    usWinAscent=os2.usWinAscent,
    usWinDescent=os2.usWinDescent,
    fsSelection=os2.fsSelection,
    xAvgCharWidth=advance,
    panose=os2.panose,
)
font.setupPost(isFixedPitch=1)

OUT.parent.mkdir(parents=True, exist_ok=True)
font.font.flavor = "woff2"
font.save(OUT)
print(f"{OUT.relative_to(SITE)}: {OUT.stat().st_size} bytes, bars {advance}/{upm} wide")
