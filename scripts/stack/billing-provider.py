#!/usr/bin/env python3
"""Isolated Stripe, VIES and hosted-x402 wire simulator; never moves money."""

import hashlib
import hmac
import json
import re
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Provider:
    def __init__(self):
        self.lock = threading.RLock()
        self.objects = {}
        self.requests = []
        self.counter = 0
        self.receipts = {}

    def create(self, prefix, kind, values):
        self.counter += 1
        obj = {"id": f"{prefix}_stack{self.counter}", "object": kind, **values}
        self.objects[obj["id"]] = obj
        return obj

    @staticmethod
    def fields(form):
        out = {}
        for name, values in form.items():
            value = values[-1]
            match = re.fullmatch(r"([^\[]+)\[([^\]]+)\]", name)
            if match:
                out.setdefault(match[1], {})[match[2]] = value
            elif "[" not in name:
                out[name] = value
        for name in ("automatic_tax",):
            if name in out and "enabled" in out[name]:
                out[name]["enabled"] = out[name]["enabled"] == "true"
        for name in ("payment_method_types", "expand", "default_tax_rates"):
            if isinstance(out.get(name), dict) and all(key.isdigit() for key in out[name]):
                out[name] = [out[name][key] for key in sorted(out[name], key=int)]
        return out

    @staticmethod
    def listing(objects):
        return {"object": "list", "data": list(objects), "has_more": False}

    def original_pdf(self, path):
        obj = self.objects.get(path.rsplit("/", 1)[-1].removesuffix(".pdf"), {})
        lines = ["Simulated provider original - no real payment", obj.get("number", "STACK-INVOICE"), obj.get("account_name", "FrameWorks Stack"), "VAT NL000000000B01", "Customer: " + obj.get("customer_name", "Stack Buyer"), "Currency: " + obj.get("currency", "eur").upper(), "Net: %.2f" % (obj.get("total_excluding_tax", 0) / 100), "VAT: %.2f" % ((obj.get("total", 0) - obj.get("total_excluding_tax", 0)) / 100), "Gross: %.2f" % (obj.get("total", 0) / 100)]
        content = "BT /F1 12 Tf 50 780 Td " + " ".join(("0 -20 Td " if i else "") + "(" + text.replace("\\", "\\\\").replace("(", "\\(").replace(")", "\\)") + ") Tj" for i, text in enumerate(lines)) + " ET"
        stream = content.encode("latin1", errors="replace")
        objects = [b"<< /Type /Catalog /Pages 2 0 R >>", b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>", b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", b"<< /Length " + str(len(stream)).encode() + b" >>\nstream\n" + stream + b"\nendstream"]
        pdf = b"%PDF-1.4\n"
        offsets = [0]
        for i, value in enumerate(objects, 1):
            offsets.append(len(pdf))
            pdf += str(i).encode() + b" 0 obj\n" + value + b"\nendobj\n"
        start = len(pdf)
        pdf += b"xref\n0 6\n0000000000 65535 f \n" + b"".join(f"{offset:010d} 00000 n \n".encode() for offset in offsets[1:])
        return pdf + b"trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n" + str(start).encode() + b"\n%%EOF\n"

    def event(self, kind, obj):
        self.counter += 1
        timestamp = int(time.time())
        payload = json.dumps({"id": f"evt_stack{self.counter}", "object": "event", "type": kind, "created": timestamp, "api_version": "2026-05-27.dahlia", "livemode": False, "data": {"object": obj}}, separators=(",", ":"))
        digest = hmac.new(b"whsec_stack_fixture", f"{timestamp}.{payload}".encode(), hashlib.sha256).hexdigest()
        return {"body": payload, "signature": f"t={timestamp},v1={digest}"}

    def invoice(self, sub):
        customer = self.objects[sub["customer"]]
        price = self.objects[sub["items"]["data"][0]["price"]["id"]]
        net = price["unit_amount"]
        rates = sub.get("default_tax_rates", [])
        vat = round(net * sum(x["percentage"] for x in rates) / 100)
        now = int(time.time())
        inv = self.create("in", "invoice", {"customer": customer["id"], "subscription": sub["id"], "parent": {"type": "subscription_details", "subscription_details": {"subscription": sub["id"], "metadata": sub["metadata"]}}, "status": "paid", "currency": "eur", "subtotal": net, "total": net + vat, "amount_paid": net + vat, "amount_due": 0, "total_excluding_tax": net, "total_taxes": [{"amount": vat, "taxable_amount": net, "tax_behavior": "exclusive", "tax_rate_details": {"tax_rate": rates[0]["id"] if rates else "", "percentage_decimal": str(rates[0]["percentage"] if rates else 0)}}], "created": now, "status_transitions": {"finalized_at": now}, "account_name": "FrameWorks-Stack-Test", "account_country": "NL", "account_tax_ids": [{"type": "eu_vat", "value": "NL000000000B01"}], "customer_name": customer.get("name", "Stack Buyer"), "customer_email": customer.get("email", "stack@example.test"), "customer_address": customer.get("address", {}), "customer_tax_ids": customer.get("tax_ids", {}).get("data", []), "lines": self.listing([{"id": "il_stack", "object": "line_item", "amount": net, "currency": "eur", "period": {"start": now, "end": now + 2592000}, "price": price}]), "metadata": sub["metadata"]})
        inv["number"] = "STACK-" + inv["id"]
        inv["invoice_pdf"] = "https://invoice.stripe.com/" + inv["id"] + ".pdf"
        sub["latest_invoice"] = inv["id"]
        return inv

    def complete(self, session):
        now = int(time.time())
        session["status"] = "complete"
        session["payment_status"] = "paid"
        if session.get("mode") == "subscription":
            form = session["stack_form"]
            price = self.objects[form["line_items[0][price]"][0]]
            rate_ids = form.get("subscription_data[default_tax_rates][0]", [])
            metadata = {m[1]: v[-1] for k, v in form.items() if (m := re.fullmatch(r"subscription_data\[metadata\]\[([^\]]+)\]", k))}
            sub = self.create("sub", "subscription", {"customer": session["customer"], "status": "active", "metadata": metadata, "default_tax_rates": [self.objects[x] for x in rate_ids], "automatic_tax": {"enabled": False}, "items": self.listing([{"id": "si_stack", "object": "subscription_item", "price": price, "current_period_start": now, "current_period_end": now + 2592000}])})
            session["subscription"] = sub["id"]
            inv = self.invoice(sub)
            return [self.event("checkout.session.completed", session), self.event("customer.subscription.created", sub), self.event("invoice.finalized", inv), self.event("invoice.paid", inv)]
        if session.get("mode") == "setup":
            intent = self.create("seti", "setup_intent", {"status": "succeeded", "payment_method": "pm_stack", "customer": session["customer"]})
            session["setup_intent"] = intent["id"]
        elif session.get("mode") == "payment":
            form = session["stack_form"]
            amount = int(form["line_items[0][price_data][unit_amount]"][0])
            currency = form["line_items[0][price_data][currency]"][0]
            intent = self.create("pi", "payment_intent", {"status": "succeeded", "amount": amount, "amount_received": amount, "currency": currency, "metadata": session.get("metadata", {})})
            session.update({"payment_intent": intent["id"], "amount_total": amount, "currency": currency})
        return [self.event("checkout.session.completed", session)]

    def dispatch(self, method, path, query, body, host):
        with self.lock:
            self.requests.append({"method": method, "path": path, "host": host, "body": body.decode(errors="replace")})
            if path == "/health":
                return 200, {"ready": True, "simulated": True}
            if path == "/__test/requests":
                return 200, self.requests
            if path == "/__test/objects":
                return 200, self.objects
            if path == "/__test/complete":
                return 200, self.complete(self.objects[json.loads(body)["session_id"]])
            if path == "/__test/renew":
                sub = self.objects[json.loads(body)["subscription_id"]]
                inv = self.invoice(sub)
                return 200, [self.event("invoice.finalized", inv), self.event("invoice.paid", inv)]
            if path.startswith("/rpc/"):
                data = json.loads(body)
                method_name = data["method"]
                result = "0x" + "0" * 64
                if method_name == "eth_call" and data["params"][0]["data"].startswith("0x70a08231"):
                    result = "0x" + "f" * 64
                elif method_name == "eth_getTransactionReceipt":
                    result = self.receipts.get(data["params"][0])
                elif method_name == "eth_getBlockByNumber":
                    result = {"number": "0x100", "hash": "0x" + "1" * 64, "parentHash": "0x" + "2" * 64, "timestamp": hex(int(time.time()))}
                elif method_name == "eth_blockNumber":
                    result = "0x100"
                elif method_name == "eth_chainId":
                    result = "0x2105" if path.endswith("base") else "0xa4b1"
                elif method_name != "eth_call":
                    return 400, {"error": "unsupported simulated chain RPC " + method_name}
                return 200, {"jsonrpc": "2.0", "id": data.get("id"), "result": result}
            if path == "/supported":
                return 200, {"kinds": [{"x402Version": 2, "scheme": "exact", "network": n} for n in ("eip155:8453", "eip155:42161")], "extensions": [], "signers": {}}
            if path in ("/verify", "/settle"):
                data = json.loads(body)
                payment = data.get("paymentPayload", {})
                payer = payment.get("payload", {}).get("authorization", {}).get("from", "0x0000000000000000000000000000000000000003")
                if path == "/verify":
                    return 200, {"isValid": True, "payer": payer}
                tx_hash = "0x" + hashlib.sha256(body).hexdigest()
                auth = payment["payload"]["authorization"]
                req = data["paymentRequirements"]
                self.receipts[tx_hash] = {"transactionHash": tx_hash, "status": "0x1", "blockNumber": "0x100", "blockHash": "0x" + "1" * 64, "gasUsed": "0x5208", "logs": [{"address": req["asset"], "topics": ["0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", "0x" + auth["from"][2:].lower().zfill(64), "0x" + auth["to"][2:].lower().zfill(64)], "data": "0x" + hex(int(auth["value"]))[2:].zfill(64)}]}
                return 200, {"success": True, "payer": payer, "network": req["network"], "transaction": tx_hash}
            if path == "/vies":
                text = body.decode()
                country = re.search(r"<(?:\w+:)?countryCode>([^<]+)", text)
                number = re.search(r"<(?:\w+:)?vatNumber>([^<]+)", text)
                return 200, ('<?xml version="1.0"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><checkVatResponse xmlns="urn:ec.europa.eu:taxud:vies:services:checkVat:types"><countryCode>' + (country[1] if country else "AT") + '</countryCode><vatNumber>' + (number[1] if number else "U12345678") + '</vatNumber><requestDate>' + time.strftime("%Y-%m-%d") + '</requestDate><valid>true</valid><name>Stack Business</name><address>Vienna</address></checkVatResponse></soap:Body></soap:Envelope>').encode()
            if path.endswith(".pdf"):
                return 200, self.original_pdf(path)
            if path == "/v1/account":
                return 200, {"id": "acct_stack", "object": "account", "country": "NL", "business_profile": {"name": "FrameWorks Stack"}}
            form = urllib.parse.parse_qs(body.decode(), keep_blank_values=True)
            values = self.fields(form)
            parts = path.strip("/").split("/")
            if len(parts) > 2 and parts[1] in ("checkout", "billing_portal") and parts[2] == "sessions":
                parts.pop(2)
            collection = parts[1] if len(parts) > 1 else ""
            ident = parts[2] if len(parts) > 2 else ""
            if collection == "customers" and len(parts) > 3 and parts[3] == "tax_ids":
                customer = self.objects[ident]
                ids = customer.setdefault("tax_ids", self.listing([]))["data"]
                if method == "POST":
                    obj = self.create("txi", "tax_id", values)
                    ids.append(obj)
                    return 200, obj
                if method == "DELETE":
                    customer["tax_ids"]["data"] = [x for x in ids if x["id"] != parts[4]]
                    return 200, {"id": parts[4], "object": "tax_id", "deleted": True}
                return 200, self.listing(ids)
            if ident == "search":
                candidates = [x for x in self.objects.values() if x["object"] == collection.rstrip("s")]
                q = query.get("query", [""])[0]
                candidates = [x for x in candidates if any(str(v) in q for v in x.get("metadata", {}).values())]
                return 200, {"object": "search_result", "data": candidates, "has_more": False}
            if method == "GET" and ident:
                if ident in self.objects:
                    return 200, self.objects[ident]
                return 404, {"error": {"message": "unknown simulated object", "type": "invalid_request_error"}}
            kind = {"tax_rates": "tax_rate", "checkout": "checkout.session", "billing_portal": "billing_portal.session"}.get(collection, collection.rstrip("s"))
            if method == "GET":
                return 200, self.listing(x for x in self.objects.values() if x["object"] == kind)
            if method == "POST" and ident in self.objects:
                for field in ("metadata", "invoice_settings", "address", "automatic_tax"):
                    if isinstance(values.get(field), dict):
                        values[field] = {**self.objects[ident].get(field, {}), **values[field]}
                self.objects[ident].update(values)
                if "default_tax_rates[0]" in form:
                    self.objects[ident]["default_tax_rates"] = [self.objects[form["default_tax_rates[0]"][0]]]
                return 200, self.objects[ident]
            prefix = {"customers": "cus", "products": "prod", "prices": "price", "tax_rates": "txr", "checkout": "cs", "payment_intents": "pi", "setup_intents": "seti", "subscriptions": "sub"}.get(collection, "obj")
            if collection == "customers":
                values.setdefault("tax_ids", self.listing([]))
                values.setdefault("invoice_settings", {})
            if collection == "tax_rates":
                values["inclusive"] = values.get("inclusive") == "true"
                values["percentage"] = float(values.get("percentage", 0))
                values["active"] = True
            if collection == "prices":
                values["unit_amount"] = int(values.get("unit_amount", 0))
                values["active"] = True
            if collection == "payment_intents":
                amount = int(values.get("amount", 0))
                values.update({"amount": amount, "amount_received": amount, "status": "succeeded"})
                values["latest_charge"] = self.create("ch", "charge", {"amount": amount, "currency": values.get("currency", "eur"), "balance_transaction": self.create("txn", "balance_transaction", {"amount": amount, "fee": 0, "net": amount, "currency": values.get("currency", "eur"), "exchange_rate": 1, "created": int(time.time())})})
            if collection == "checkout":
                values.update({"url": "https://checkout.stripe.com/stack-simulated", "status": "open", "payment_status": "unpaid", "expires_at": int(time.time()) + 1800})
                values["stack_form"] = form
            return 200, self.create(prefix, kind, values)


STATE = Provider()


class Handler(BaseHTTPRequestHandler):
    def request_body(self):
        if self.headers.get("Transfer-Encoding", "").lower() != "chunked":
            return self.rfile.read(int(self.headers.get("Content-Length", "0")))
        chunks = []
        while True:
            size = int(self.rfile.readline().split(b";", 1)[0].strip(), 16)
            if size == 0:
                while self.rfile.readline().strip():
                    pass
                return b"".join(chunks)
            chunks.append(self.rfile.read(size))
            if self.rfile.read(2) != b"\r\n":
                raise ValueError("invalid chunk delimiter")

    def handle_request(self):
        parsed = urllib.parse.urlsplit(self.path)
        try:
            body = self.request_body()
            status, result = STATE.dispatch(self.command, parsed.path, urllib.parse.parse_qs(parsed.query), body, self.headers.get("X-Stack-Provider-Host", "fixture-control"))
        except (ValueError, KeyError, TypeError) as exc:
            status, result = 400, {"error": {"message": str(exc), "type": "invalid_request_error"}}
        self.send_response(status)
        content_type = "application/json"
        if isinstance(result, bytes):
            content_type = "application/pdf" if result.startswith(b"%PDF") else "text/xml"
        else:
            result = json.dumps(result).encode()
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(result)))
        self.end_headers()
        self.wfile.write(result)

    do_GET = handle_request
    do_POST = handle_request
    do_DELETE = handle_request


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 18778), Handler).serve_forever()
