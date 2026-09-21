"""Receive FrameWorks webhooks with the standard library's HTTP server."""

import os
from http.server import BaseHTTPRequestHandler, HTTPServer

from livepeer_frameworks import WebhookReceiver, WebhookVerificationError
from livepeer_frameworks.webhooks import PUBLIC_EVENT_MESSAGES

receiver = WebhookReceiver(os.environ["FRAMEWORKS_WEBHOOK_SECRET"])
ClipReady = PUBLIC_EVENT_MESSAGES["clip.ready"]


class Handler(BaseHTTPRequestHandler):
    def do_POST(self) -> None:  # noqa: N802
        # Verify the raw bytes: re-serialized JSON no longer matches the signature.
        body = self.rfile.read(int(self.headers["content-length"]))
        try:
            event = receiver.receive(body, dict(self.headers.items()))
        except WebhookVerificationError:
            self.send_response(401)
            self.end_headers()
            return
        if event.type == "clip.ready" and isinstance(event.data, ClipReady):
            print("clip ready", event.id, event.data.to_dict())
        elif not event.known:
            print("event type this SDK does not know yet:", event.type, event.raw_data)
        self.send_response(204)
        self.end_headers()


if __name__ == "__main__":
    HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
