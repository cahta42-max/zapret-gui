# Генератор логотипа Zapret GUI v3.
# Знак: «Z» из шрифта Bahnschrift (технический гротеск), разрезанная
# тонкой горизонтальной линией — метафора split-стратегий DPI-обхода.
# Никакой ручной геометрии: только настоящая типографика.

from PIL import Image, ImageDraw, ImageFont

S = 512
BG = (14, 17, 22, 255)     # #0e1116 — фон приложения
FG = (231, 235, 242, 255)  # #e7ebf2 — почти белый

FONT = r"C:\Windows\Fonts\bahnschrift.ttf"
FONT_SIZE = 460
GAP = 16                   # расхождение половин знака (половина разреза)

img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
d = ImageDraw.Draw(img)
d.rounded_rectangle([24, 24, S - 24, S - 24], radius=104, fill=BG)

font = ImageFont.truetype(FONT, FONT_SIZE)
for name in ("SemiBold", "Bold"):
    try:
        font.set_variation_by_name(name)
        break
    except Exception:
        pass

# маска знака, отцентрованная по фактическим границам глифа
mask = Image.new("L", (S, S), 0)
md = ImageDraw.Draw(mask)
bbox = md.textbbox((0, 0), "Z", font=font)
x = (S - (bbox[2] - bbox[0])) // 2 - bbox[0]
y = (S - (bbox[3] - bbox[1])) // 2 - bbox[1]
md.text((x, y), "Z", font=font, fill=255)

# разрез: верхняя половина уходит вверх, нижняя — вниз.
# ВАЖНО: кадр нижней половины начинается с cy, поэтому вставлять
# её нужно в y = cy + GAP, а не просто GAP (иначе наложится на верх).
cy = S // 2
top = mask.crop((0, 0, S, cy))
bot = mask.crop((0, cy, S, S))
split = Image.new("L", (S, S), 0)
split.paste(top, (0, -GAP), top)
split.paste(bot, (0, cy + GAP), bot)

img.paste(Image.new("RGBA", (S, S), FG), (0, 0), split)

img.save("build/appicon.png")
img.save("build/windows/icon.ico",
         sizes=[(256, 256), (128, 128), (64, 64), (48, 48), (32, 32), (16, 16)])
print("готово: appicon.png + icon.ico")
