#!/usr/bin/env python3
"""Find a verification message in the operator's private IMAP test inbox."""

import email
import imaplib
import os
import re
import sys
from email.utils import getaddresses
from urllib.parse import unquote


def message_text(message):
    for part in message.walk():
        if part.get_content_type() not in ("text/plain", "text/html"):
            continue
        payload = part.get_payload(decode=True)
        if payload:
            yield payload.decode(part.get_content_charset() or "utf-8", "replace")


def main():
    if len(sys.argv) != 3 or sys.argv[1] not in ("exists", "token"):
        return 2
    mode, address = sys.argv[1:]
    host = os.environ["STACK_MAIL_IMAP_HOST"]
    user = os.environ["STACK_MAIL_IMAP_USER"]
    password = os.environ["STACK_MAIL_IMAP_PASSWORD"]
    mailbox = os.environ.get("STACK_MAIL_IMAP_FOLDER", "INBOX")
    with imaplib.IMAP4_SSL(host, int(os.environ.get("STACK_MAIL_IMAP_PORT", "993"))) as client:
        client.login(user, password)
        status, _ = client.select(mailbox, readonly=True)
        if status != "OK":
            return 1
        status, result = client.search(None, "TO", f'"{address}"')
        if status != "OK":
            return 1
        for message_id in reversed(result[0].split()[-30:]):
            status, parts = client.fetch(message_id, "(BODY.PEEK[])")
            if status != "OK":
                continue
            raw = next((part[1] for part in parts if isinstance(part, tuple)), None)
            if not raw:
                continue
            message = email.message_from_bytes(raw)
            recipients = {value.lower() for _, value in getaddresses(message.get_all("To", []))}
            if address.lower() not in recipients:
                continue
            if mode == "exists":
                return 0
            for body in message_text(message):
                match = re.search(r"verify-email#token=([A-Za-z0-9%_.~-]+)", body)
                if match:
                    print(unquote(match.group(1)))
                    return 0
    return 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, imaplib.IMAP4.error) as exc:
        print(f"mailbox lookup failed: {exc}", file=sys.stderr)
        sys.exit(1)
