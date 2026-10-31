# Generates trayicons/*.png: the tray icon shown while voice conversation runs,
# one pictogram per state (mic = listening, lines = transcribing, hourglass =
# waiting for the reply, speaker = reading it, ! = problem). Drawn at 4x and
# scaled down for smooth edges. Run from desktop/: py -3 build/gen_tray_state_icons.py
from PIL import Image, ImageDraw

S = 256  # drawing size; output is 64x64
BG = (15, 23, 42, 255)  # same navy as trayicon.png
FG = (255, 255, 255, 255)
W = 18  # stroke width


def base():
    im = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    d.rounded_rectangle((4, 4, S - 5, S - 5), radius=52, fill=BG)
    return im, d


def mic(d):
    d.rounded_rectangle((96, 36, 160, 152), radius=32, fill=FG)
    d.arc((64, 76, 192, 196), 0, 180, fill=FG, width=W)
    d.line((128, 196, 128, 222), fill=FG, width=W)
    d.line((88, 222, 168, 222), fill=FG, width=W)


def lines(d):
    for y, x2 in ((74, 204), (128, 204), (182, 150)):
        d.rounded_rectangle((52, y - 11, x2, y + 11), radius=11, fill=FG)


def hourglass(d):
    d.line((64, 40, 192, 40), fill=FG, width=W)
    d.line((64, 216, 192, 216), fill=FG, width=W)
    d.line((80, 40, 128, 128, 80, 216), fill=FG, width=W, joint="curve")
    d.line((176, 40, 128, 128, 176, 216), fill=FG, width=W, joint="curve")
    d.polygon(((96, 206), (160, 206), (128, 160)), fill=FG)  # sand


def speaker(d):
    d.polygon(((40, 100), (84, 100), (136, 52), (136, 204), (84, 156), (40, 156)), fill=FG)
    d.arc((104, 88, 184, 168), -50, 50, fill=FG, width=W)
    d.arc((84, 48, 224, 208), -50, 50, fill=FG, width=W)


def problem(d):
    d.line((128, 40, 32, 208, 224, 208, 128, 40), fill=FG, width=W, joint="curve")
    d.rounded_rectangle((118, 96, 138, 160), radius=10, fill=FG)
    d.ellipse((116, 172, 140, 196), fill=FG)


for name, draw in (("listening", mic), ("transcribing", lines), ("waiting", hourglass),
                   ("speaking", speaker), ("problem", problem)):
    im, d = base()
    draw(d)
    im.resize((64, 64), Image.LANCZOS).save(f"trayicons/{name}.png")


def record(d):
    d.ellipse((40, 40, 216, 216), outline=FG, width=W)
    d.ellipse((84, 84, 172, 172), fill=FG)


im, d = base()
record(d)
im.resize((64, 64), Image.LANCZOS).save("trayicons/recording.png")


def done(d):
    d.ellipse((36, 36, 220, 220), outline=FG, width=W)
    d.line((84, 132, 116, 164, 176, 96), fill=FG, width=W + 4, joint="curve")


im, d = base()
done(d)
im.resize((64, 64), Image.LANCZOS).save("trayicons/done.png")
