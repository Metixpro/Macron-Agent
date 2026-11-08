import json
import sys

import qrcode

d = json.load(sys.stdin)
qr = qrcode.QRCode(error_correction=qrcode.constants.ERROR_CORRECT_M, box_size=12, border=4)
qr.add_data(d["data"])
qr.make(fit=True)

qr.make_image(fill_color="#0f172a", back_color="white").save(d["out"])
print("OK")
