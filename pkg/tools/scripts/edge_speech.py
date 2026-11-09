import asyncio
import json
import os
import ssl
import sys
from urllib.request import getproxies, proxy_bypass

import certifi
import edge_tts
import edge_tts.communicate


def tls_context():
    context = ssl.create_default_context()
    context.load_verify_locations(cafile=certifi.where())
    for name in ("REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE"):
        path = os.environ.get(name)
        if path:
            context.load_verify_locations(cafile=path)
    return context


def proxy_url():
    if proxy_bypass("speech.platform.bing.com"):
        return None
    proxies = getproxies()
    return proxies.get("https") or proxies.get("http")


async def main():
    data = json.load(sys.stdin)
    edge_tts.communicate._SSL_CTX = tls_context()
    speech = edge_tts.Communicate(
        data["text"], data["voice"], rate=data["rate"], proxy=proxy_url()
    )
    await speech.save(data["filename"])


if __name__ == "__main__":
    asyncio.run(main())
