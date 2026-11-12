#!/usr/bin/env python3
"""Isolated billing wire fixture; never moves money.

Server contracts:
https://docs.stripe.com/api/idempotent_requests
https://docs.stripe.com/billing/subscriptions/pause-payment
https://docs.stripe.com/invoicing/integration/automatic-advancement-collection
Collection and settlement advance only through explicit fixture clock controls.
"""

import hashlib
import copy
import hmac
import json
import os
import re
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from decimal import Decimal, ROUND_HALF_UP


class Provider:
    def __init__(self):
        self.lock = threading.RLock()
        self.objects = {}
        self.requests = []
        self.counter = 0
        self.receipts = {}
        self.idempotency = {}
        self.vies_validity = {}
        self.payment_outcomes = {}
        self.invoice_outcomes = {}
        self.faults = {}

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
        for name in ("auto_advance", "cancel_at_period_end", "discountable", "active"):
            if name in out:
                out[name] = out[name] == "true"
        for name in ("payment_method_types", "expand", "default_tax_rates", "tax_rates"):
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

    def invoice(self, sub, draft=False):
        customer = self.objects[sub["customer"]]
        price = self.objects[sub["items"]["data"][0]["price"]["id"]]
        net = price["unit_amount"]
        rates = sub.get("default_tax_rates", [])
        taxes = [{"amount": int((Decimal(net) * Decimal(str(rate["percentage"])) / 100).quantize(Decimal("1"), rounding=ROUND_HALF_UP)), "taxable_amount": net, "tax_behavior": "exclusive", "tax_rate_details": {"tax_rate": rate["id"], "percentage_decimal": str(rate["percentage"])}} for rate in rates]
        vat = sum(tax["amount"] for tax in taxes)
        now = int(time.time())
        inv = self.create("in", "invoice", {"customer": customer["id"], "subscription": sub["id"], "parent": {"type": "subscription_details", "subscription_details": {"subscription": sub["id"], "metadata": sub["metadata"]}}, "status": "paid", "currency": "eur", "subtotal": net, "total": net + vat, "amount_paid": net + vat, "amount_due": 0, "total_excluding_tax": net, "total_taxes": taxes, "created": now, "status_transitions": {"finalized_at": now}, "account_name": "FrameWorks-Stack-Test", "account_country": "NL", "account_tax_ids": [{"type": "eu_vat", "value": "NL000000000B01"}], "customer_name": customer.get("name", "Stack Buyer"), "customer_email": customer.get("email", "stack@example.test"), "customer_address": customer.get("address", {}), "customer_tax_ids": customer.get("tax_ids", {}).get("data", []), "lines": self.listing([{"id": "il_stack", "object": "line_item", "amount": net, "currency": "eur", "taxes": taxes, "period": {"start": now, "end": now + 2592000}, "price": price}]), "metadata": sub["metadata"]})
        inv["number"] = "STACK-" + inv["id"]
        inv["invoice_pdf"] = "https://invoice.stripe.com/" + inv["id"] + ".pdf"
        inv["default_tax_rates"] = rates
        inv["footer"] = customer.get("invoice_settings", {}).get("footer", "")
        self.snapshot_invoice_buyer(inv)
        item = sub["items"]["data"][0]
        inv["lines"]["data"][0].update({"id": "il_" + inv["id"], "quantity": 1, "pricing": {"type": "price_details", "price_details": {"price": price["id"], "product": price.get("product")}}, "parent": {"type": "subscription_item_details", "subscription_item_details": {"subscription": sub["id"], "subscription_item": item["id"], "proration": False}}, "period": {"start": item["current_period_start"], "end": item["current_period_end"]}})
        inv["metadata"] = dict(sub["metadata"])
        inv["parent"]["subscription_details"]["metadata"] = dict(sub["metadata"])
        inv["billing_reason"] = "subscription_cycle"
        inv["collection_method"] = "charge_automatically"
        if draft:
            inv.update({"status": "draft", "amount_paid": 0, "amount_due": inv["total"], "status_transitions": {"finalized_at": None}, "auto_advance": False})
        else:
            self.paid_invoice(inv)
        sub["latest_invoice"] = inv["id"]
        return inv

    def snapshot_invoice_buyer(self, invoice):
        customer = self.objects[invoice["customer"]]
        invoice.update({"customer_name": customer.get("name", "Stack Buyer"), "customer_email": customer.get("email", "stack@example.test"), "customer_address": copy.deepcopy(customer.get("address", {})), "customer_tax_ids": copy.deepcopy(customer.get("tax_ids", {}).get("data", []))})

    def refresh_draft_buyer(self, invoice):
        # Stripe's customer_* fields follow the customer until finalization;
        # finalized documents retain their own deep-copied buyer snapshot.
        if invoice.get("object") == "invoice" and invoice.get("status") == "draft":
            self.snapshot_invoice_buyer(invoice)

    def recalculate(self, invoice):
        groups = {}
        for line in invoice["lines"]["data"]:
            rates = line.get("tax_rates", invoice.get("default_tax_rates", []))
            line["taxes"] = []
            for rate in rates:
                amount = int((Decimal(line["amount"]) * Decimal(str(rate["percentage"])) / 100).quantize(Decimal("1"), rounding=ROUND_HALF_UP))
                tax = {"amount": amount, "taxable_amount": line["amount"], "tax_behavior": "exclusive", "tax_rate_details": {"tax_rate": rate["id"], "percentage_decimal": str(rate["percentage"])}}
                line["taxes"].append(tax)
                group = groups.setdefault(rate["id"], {**tax, "amount": 0, "taxable_amount": 0})
                group["amount"] += amount
                group["taxable_amount"] += line["amount"]
        invoice["total_taxes"] = list(groups.values())
        invoice["subtotal"] = invoice["total_excluding_tax"] = sum(line["amount"] for line in invoice["lines"]["data"])
        invoice["total"] = invoice["subtotal"] + sum(tax["amount"] for tax in invoice["total_taxes"])
        invoice["amount_due"] = max(0, invoice["total"] - invoice["amount_paid"])

    def paid_invoice(self, invoice):
        invoice.update({"status": "paid", "amount_paid": invoice["total"], "amount_due": 0, "amount_remaining": 0, "auto_advance": False, "next_payment_attempt": None})
        sub = self.objects.get(invoice.get("subscription"))
        if sub and sub.get("latest_invoice") == invoice["id"]:
            sub["status"] = "active"
        if invoice.get("payment_intent"):
            self.objects[invoice["payment_intent"]]["status"] = "succeeded"
        if any(obj["object"] == "invoice_payment" and obj.get("invoice") == invoice["id"] and obj.get("status") == "paid" for obj in self.objects.values()):
            return
        charge = self.create("ch", "charge", {"amount": invoice["total"], "currency": invoice["currency"], "refunded": False, "amount_refunded": 0, "disputed": False})
        self.create("inpay", "invoice_payment", {"invoice": invoice["id"], "status": "paid", "amount_paid": invoice["total"], "payment": {"type": "charge", "charge": charge["id"]}})

    def attempt_payment(self, invoice):
        invoice["attempt_count"] = invoice.get("attempt_count", 0) + 1
        outcome = self.invoice_outcomes.get(invoice["id"], self.payment_outcomes.get(invoice["customer"], "succeeded"))
        if outcome == "succeeded":
            self.paid_invoice(invoice)
            return 200, invoice
        state = "requires_payment_method" if outcome == "declined" else outcome
        intent = self.objects.get(invoice.get("payment_intent"))
        if intent is None:
            intent = self.create("pi", "payment_intent", {"customer": invoice["customer"], "amount": invoice["total"], "currency": invoice["currency"], "metadata": invoice.get("metadata", {})})
            invoice["payment_intent"] = intent["id"]
        intent["status"] = state
        # The fixture uses a deterministic retry deadline; real schedules are
        # account settings. Clock controls must advance past it to retry.
        invoice.update({"status": "open", "amount_paid": 0, "amount_remaining": invoice["total"], "next_payment_attempt": int(time.time()) + 86400 if outcome == "declined" and invoice.get("auto_advance") else None})
        if outcome == "processing":
            return 200, invoice
        sub = self.objects.get(invoice.get("subscription"))
        if sub:
            sub["status"] = "past_due"
        return 402, {"error": {"type": "card_error", "code": "card_declined" if outcome == "declined" else "authentication_required", "message": "Simulated payment requires a new payment method or customer authentication", "payment_intent": intent}}

    @staticmethod
    def invalid(message):
        return 400, {"error": {"message": message, "type": "invalid_request_error"}}

    def complete(self, session):
        now = int(time.time())
        session["status"] = "complete"
        session["payment_status"] = "paid"
        if session.get("mode") == "subscription":
            form = session["stack_form"]
            price = self.objects[form["line_items[0][price]"][0]]
            rate_ids = form.get("subscription_data[default_tax_rates][0]", [])
            metadata = {m[1]: v[-1] for k, v in form.items() if (m := re.fullmatch(r"subscription_data\[metadata\]\[([^\]]+)\]", k))}
            sub = self.create("sub", "subscription", {"customer": session["customer"], "status": "active", "metadata": metadata, "default_tax_rates": [self.objects[x] for x in rate_ids], "automatic_tax": {"enabled": False}, "items": self.listing([{"id": "si_stack", "object": "subscription_item", "quantity": 1, "price": price, "current_period_start": now, "current_period_end": now + 2592000}])})
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

    def dispatch(self, method, path, query, body, host, headers=None):
        with self.lock:
            key = (headers or {}).get("Idempotency-Key", "") if method == "POST" else ""
            self.requests.append({"method": method, "path": path, "host": host, "body": body.decode(errors="replace"), "idempotency_key": key})
            identity = (method, path, tuple((name, tuple(values)) for name, values in sorted(urllib.parse.parse_qs(body.decode(), keep_blank_values=True).items())))
            if key in self.idempotency:
                prior, response = self.idempotency[key]
                if prior != identity:
                    return self.invalid("idempotency key reused with different parameters")
                return copy.deepcopy(response)
            fault = self.faults.get((method, path))
            if fault:
                fault["remaining"] = fault.get("remaining", 1) - 1
                if fault["remaining"] <= 0:
                    del self.faults[(method, path)]
            if fault and not fault["after_execution"]:
                started = fault.get("execution_started", False)
                response = fault["status"], {"error": {"type": "api_error" if started else "invalid_request_error", "message": "Simulated endpoint failure before state changes" if started else "Simulated conflict before endpoint execution"}}
                if key and started:
                    self.idempotency[key] = (identity, copy.deepcopy(response))
                return response
            response = self._dispatch(method, path, query, body, host)
            executed = 200 <= response[0] < 300 or response[0] == 402
            if fault and executed:
                response = fault["status"], {"error": {"type": "api_error", "message": "Simulated failure after endpoint execution"}}
            # API v1 retains the first executed result, including errors.
            # Validation/conflict before execution does not reserve a key.
            if key and executed:
                self.idempotency[key] = (identity, copy.deepcopy(response))
            return response

    def _dispatch(self, method, path, query, body, host):
        with self.lock:
            if path == "/health":
                return 200, {"ready": True, "simulated": True}
            if path == "/__test/requests":
                return 200, self.requests
            if path == "/__test/objects":
                return 200, self.objects
            if path == "/__test/native_settlement":
                data = json.loads(body)
                payment = self.objects.get(data.get("payment_id"), {})
                currency = data.get("currency", "").lower()
                amount, fee = data.get("amount"), data.get("fee")
                if payment.get("object") != "payment_intent" or payment.get("status") != "succeeded" or payment.get("latest_charge"):
                    return self.invalid("native settlement requires an unsettled paid PaymentIntent")
                if not re.fullmatch(r"[a-z]{3}", currency) or not isinstance(amount, int) or not isinstance(fee, int) or fee < 0:
                    return self.invalid("native settlement requires currency and integer amounts")
                rate = data.get("exchange_rate")
                if currency != payment["currency"] and (rate is None or Decimal(str(rate)) <= 0):
                    return self.invalid("cross-currency provider settlement requires its exchange rate")
                transaction = self.create("txn", "balance_transaction", {"amount": amount, "fee": fee, "net": amount-fee, "currency": currency, "exchange_rate": rate, "created": int(time.time())})
                charge = self.create("ch", "charge", {"amount": payment["amount"], "currency": payment["currency"], "payment_intent": payment["id"], "balance_transaction": transaction["id"], "metadata": dict(payment.get("metadata", {}))})
                payment["latest_charge"] = charge
                return 200, self.event("charge.updated", charge)
            if path == "/__test/vies":
                data = json.loads(body)
                self.vies_validity[data["vat_number"]] = bool(data["valid"])
                return 200, {"configured": True}
            if path == "/__test/payment_behavior":
                data = json.loads(body)
                if data["outcome"] not in ("succeeded", "declined", "requires_action", "processing"):
                    return self.invalid("unsupported simulated payment outcome")
                if data.get("invoice_id"):
                    self.invoice_outcomes[data["invoice_id"]] = data["outcome"]
                else:
                    self.payment_outcomes[data["customer_id"]] = data["outcome"]
                return 200, {"configured": True}
            if path == "/__test/fault":
                data = json.loads(body)
                if data.get("remaining", 1) == 0:
                    self.faults.pop((data["method"], data["path"]), None)
                else:
                    self.faults[(data["method"], data["path"])] = data
                return 200, {"configured": True}
            if path == "/__test/collect":
                data = json.loads(body)
                invoice = self.objects[data["invoice_id"]]
                if not invoice.get("auto_advance") or invoice["status"] not in ("draft", "open"):
                    return 200, []
                sub = self.objects.get(invoice.get("subscription"))
                if sub and sub.get("pause_collection"):
                    return 200, []
                if invoice.get("next_payment_attempt") and invoice["next_payment_attempt"] > data.get("at", int(time.time())):
                    return 200, []
                events = []
                if invoice["status"] == "draft":
                    self.refresh_draft_buyer(invoice)
                    invoice.update({"status": "open", "status_transitions": {"finalized_at": int(time.time())}})
                    events.append(self.event("invoice.finalized", invoice))
                intent = self.objects.get(invoice.get("payment_intent"), {})
                if intent.get("status") in ("processing", "requires_action"):
                    return 200, events
                status, _ = self.attempt_payment(invoice)
                intent = self.objects.get(invoice.get("payment_intent"), {})
                event = "invoice.paid" if invoice["status"] == "paid" else "invoice.payment_action_required" if intent.get("status") == "requires_action" else "invoice.payment_failed" if status == 402 else None
                if event:
                    events.append(self.event(event, invoice))
                return 200, events
            if path == "/__test/settle":
                invoice = self.objects[json.loads(body)["invoice_id"]]
                intent = self.objects.get(invoice.get("payment_intent"), {})
                if intent.get("status") not in ("processing", "requires_action"):
                    return self.invalid("only an existing pending payment can settle")
                self.paid_invoice(invoice)
                return 200, [self.event("invoice.paid", invoice)]
            if path == "/__test/unrelated_invoice":
                sub = self.objects[json.loads(body)["subscription_id"]]
                latest = sub["latest_invoice"]
                manual = self.invoice(sub, draft=True)
                manual.update({"billing_reason": "manual", "parent": None, "metadata": {"operator_manual": "true"}, "status": "open", "collection_method": "send_invoice", "due_date": int(time.time()) - 86400, "auto_advance": False, "status_transitions": {"finalized_at": int(time.time())}})
                sub["latest_invoice"] = latest
                return 200, manual
            if path in ("/__test/held_adjustment", "/__test/operator_paused_invoice"):
                sub = self.objects[json.loads(body)["subscription_id"]]
                latest = sub["latest_invoice"]
                paid = [invoice for invoice in self.objects.values() if invoice["object"] == "invoice" and invoice.get("subscription") == sub["id"] and invoice["status"] == "paid"]
                original = paid[-1] if paid else None
                if original is None:
                    return self.invalid("fixture requires an actual paid source period")
                extra = self.invoice(sub, draft=True)
                if path.endswith("held_adjustment"):
                    extra["billing_reason"] = "subscription_update"
                else:
                    extra.update({"status": "open", "default_tax_rates": copy.deepcopy(original["default_tax_rates"]), "status_transitions": {"finalized_at": int(time.time())}})
                    extra["metadata"]["frameworks_tax_profile"] = original["metadata"]["frameworks_tax_profile"]
                    self.recalculate(extra)
                sub["latest_invoice"] = latest
                return 200, extra
            if path == "/__test/invoice_events":
                data = json.loads(body)
                invoice = self.objects[data["invoice_id"]] if data.get("invoice_id") else self.objects[self.objects[data["subscription_id"]]["latest_invoice"]]
                if invoice["status"] not in ("open", "paid"):
                    return self.invalid("invoice has not actually been finalized")
                events = [self.event("invoice.finalized", invoice)]
                if invoice["status"] == "paid":
                    events.append(self.event("invoice.paid", invoice))
                return 200, events
            if path == "/__test/complete":
                return 200, self.complete(self.objects[json.loads(body)["session_id"]])
            if path == "/__test/renew":
                sub = self.objects[json.loads(body)["subscription_id"]]
                for item in sub["items"]["data"]:
                    duration = item["current_period_end"] - item["current_period_start"]
                    item["current_period_start"] = item["current_period_end"]
                    item["current_period_end"] += duration
                paused = sub.get("pause_collection", {}).get("behavior") == "keep_as_draft"
                inv = self.invoice(sub, draft=True)
                inv["auto_advance"] = not paused
                return 200, [self.event("invoice.created", inv)]
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
                valid = self.vies_validity.get(number[1] if number else "", True)
                return 200, ('<?xml version="1.0"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><checkVatResponse xmlns="urn:ec.europa.eu:taxud:vies:services:checkVat:types"><countryCode>' + (country[1] if country else "AT") + '</countryCode><vatNumber>' + (number[1] if number else "U12345678") + '</vatNumber><requestDate>' + time.strftime("%Y-%m-%d", time.gmtime()) + '+01:00</requestDate><valid>' + str(valid).lower() + '</valid><name>Stack Business</name><address>Vienna</address></checkVatResponse></soap:Body></soap:Envelope>').encode()
            if path.endswith(".pdf"):
                return 200, self.original_pdf(path)
            if path == "/v1/account":
                return 200, {"id": "acct_stack", "object": "account", "country": "NL", "business_profile": {"name": "FrameWorks Stack"}}
            if path == "/v1/webhook_endpoints" and method == "GET":
                gateway = os.environ.get("GATEWAY_PUBLIC_URL", "").rstrip("/")
                events = ["checkout.session.completed", "checkout.session.async_payment_succeeded", "checkout.session.async_payment_failed", "checkout.session.expired", "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted", "customer.subscription.paused", "customer.subscription.resumed", "invoice.finalized", "invoice.paid", "invoice.payment_failed", "invoice.payment_action_required", "credit_note.created", "credit_note.updated", "credit_note.voided", "payment_intent.succeeded", "payment_intent.payment_failed", "charge.refunded", "charge.updated", "charge.dispute.created", "charge.dispute.closed", "charge.dispute.funds_withdrawn", "charge.dispute.funds_reinstated"]
                endpoints = [{"id": "we_stack", "object": "webhook_endpoint", "status": "enabled", "url": gateway + "/webhooks/billing/stripe", "enabled_events": events}] if gateway else []
                return 200, self.listing(endpoints)
            form = urllib.parse.parse_qs(body.decode(), keep_blank_values=True)
            values = self.fields(form)
            parts = path.strip("/").split("/")
            if len(parts) > 2 and parts[1] in ("checkout", "billing_portal") and parts[2] == "sessions":
                parts.pop(2)
            collection = parts[1] if len(parts) > 1 else ""
            ident = parts[2] if len(parts) > 2 else ""
            for name, entries in form.items():
                if name.endswith("[unit_amount]") or name == "unit_amount":
                    inline_subscription_credit = collection == "subscriptions" and name.startswith("add_invoice_items[")
                    if int(entries[-1]) < 0 and not inline_subscription_credit:
                        return self.invalid("ordinary price unit_amount must be nonnegative")
            if method == "POST" and collection == "invoices" and ident in self.objects and len(parts) > 3:
                invoice = self.objects[ident]
                action = parts[3]
                if action == "update_lines":
                    if invoice["status"] != "draft":
                        return self.invalid("lines can only be changed on draft invoices")
                    indexes = sorted({int(m[1]) for key in form if (m := re.match(r"lines\[(\d+)\]", key))})
                    pending = []
                    for index in indexes:
                        # Strip only the outer line index; nested Stripe field names remain intact.
                        line_form = {re.sub(r"^lines\[\d+\]\[([^\]]+)\]", r"\1", key): entries for key, entries in form.items() if key.startswith(f"lines[{index}][")}
                        fields = self.fields(line_form)
                        rates = [self.objects[r] for r in fields.get("tax_rates", [])]
                        source = next((line for line in invoice["lines"]["data"] if line["id"] == fields.get("id")), None)
                        if source is None:
                            return self.invalid("unknown invoice line")
                        line = {**source, "tax_rates": rates}
                        pending.append(line)
                    for line in pending:
                        invoice["lines"]["data"] = [line if previous["id"] == line["id"] else previous for previous in invoice["lines"]["data"]]
                    self.recalculate(invoice)
                elif action == "finalize":
                    if invoice["status"] != "draft":
                        return self.invalid("only draft invoices can be finalized")
                    self.refresh_draft_buyer(invoice)
                    invoice.update({"status": "open", "auto_advance": values.get("auto_advance", False), "status_transitions": {"finalized_at": int(time.time())}})
                elif action == "pay":
                    if invoice["status"] != "open":
                        return self.invalid("only open invoices can be paid")
                    return self.attempt_payment(invoice)
                elif action == "void":
                    if invoice["status"] not in ("open", "draft"):
                        return self.invalid("invoice cannot be voided")
                    invoice.update({"status": "void", "amount_due": 0})
                else:
                    return self.invalid("unsupported invoice action")
                return 200, invoice
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
                    self.refresh_draft_buyer(self.objects[ident])
                    obj = copy.deepcopy(self.objects[ident])
                    if len(parts) > 3 and parts[3] == "lines":
                        return 200, obj["lines"]
                    if "latest_invoice" in query.get("expand[]", []) + query.get("expand[0]", []):
                        obj["latest_invoice"] = copy.deepcopy(self.objects.get(obj.get("latest_invoice")))
                    return 200, obj
                return 404, {"error": {"message": "unknown simulated object", "type": "invalid_request_error"}}
            kind = {"tax_rates": "tax_rate", "invoice_payments": "invoice_payment", "checkout": "checkout.session", "billing_portal": "billing_portal.session"}.get(collection, collection.rstrip("s"))
            if method == "GET":
                candidates = [x for x in self.objects.values() if x["object"] == kind]
                for candidate in candidates:
                    self.refresh_draft_buyer(candidate)
                for key in ("customer", "subscription", "status", "invoice"):
                    if key in query:
                        candidates = [x for x in candidates if x.get(key) == query[key][-1]]
                return 200, self.listing(candidates)
            if method == "POST" and ident in self.objects:
                if collection == "invoices" and self.objects[ident]["status"] != "draft" and "default_tax_rates" in values:
                    return self.invalid("finalized invoice tax rates are immutable")
                if collection == "subscriptions":
                    if form.get("billing_cycle_anchor", [""])[-1] == "now" and values.get("payment_behavior") == "error_if_incomplete" and self.payment_outcomes.get(self.objects[ident]["customer"], "succeeded") in ("declined", "requires_action"):
                        return 402, {"error": {"type": "card_error", "code": "card_declined", "message": "Simulated required payment failed; subscription update was not applied"}}
                    # The update's item instructions are not the response's
                    # existing SubscriptionItemList, including an empty update.
                    values.pop("items", None)
                    previous_subscription = copy.deepcopy(self.objects[ident])
                if isinstance(values.get("metadata"), dict):
                    merged = dict(self.objects[ident].get("metadata", {}))
                    for name, value in values["metadata"].items():
                        if len(name) > 40 or "[" in name or "]" in name or len(str(value)) > 500:
                            return self.invalid("invalid metadata key or value length")
                        if value == "":
                            merged.pop(name, None)
                        else:
                            merged[name] = value
                    if len(merged) > 50:
                        return self.invalid("metadata has more than 50 keys")
                    values["metadata"] = merged
                for field in ("invoice_settings", "address", "automatic_tax"):
                    if isinstance(values.get(field), dict):
                        values[field] = {**self.objects[ident].get(field, {}), **values[field]}
                self.objects[ident].update(values)
                if "default_tax_rates[0]" in form:
                    self.objects[ident]["default_tax_rates"] = [self.objects[form["default_tax_rates[0]"][0]]]
                if collection == "subscriptions":
                    if values.get("pause_collection") == "":
                        self.objects[ident].pop("pause_collection", None)
                    price_id = form.get("items[0][price]", [None])[-1]
                    if price_id:
                        self.objects[ident]["items"]["data"][0]["price"] = self.objects[price_id]
                    for index, item in enumerate(self.objects[ident]["items"]["data"]):
                        prefix = f"items[{index}][tax_rates]"
                        rate_keys = sorted(key for key in form if key.startswith(prefix + "["))
                        if rate_keys:
                            item["tax_rates"] = [self.objects[form[key][-1]] for key in rate_keys]
                        elif prefix in form and form[prefix][-1] == "":
                            item["tax_rates"] = []
                    if values.get("default_tax_rates") == "":
                        self.objects[ident]["default_tax_rates"] = []
                    if form.get("billing_cycle_anchor", [""])[-1] == "now":
                        now = int(time.time())
                        self.objects[ident]["billing_cycle_anchor"] = now
                        self.objects[ident]["items"]["data"][0].update({"current_period_start": now, "current_period_end": now + 2592000})
                        paused = self.objects[ident].get("pause_collection", {}).get("behavior") == "keep_as_draft"
                        invoice = self.invoice(self.objects[ident], draft=True)
                        if values.get("proration_behavior", "create_prorations") != "none":
                            old_item = previous_subscription["items"]["data"][0]
                            whole = old_item["current_period_end"] - old_item["current_period_start"]
                            remaining = max(0, min(old_item["current_period_end"] - now, whole))
                            credit = int((Decimal(old_item["price"]["unit_amount"]) * Decimal(remaining) / Decimal(whole)).quantize(Decimal("1"), rounding=ROUND_HALF_UP)) if whole > 0 else 0
                            if credit:
                                invoice["lines"]["data"].append({"id": "il_proration_" + invoice["id"], "object": "line_item", "amount": -credit, "currency": invoice["currency"], "quantity": 1, "tax_rates": old_item.get("tax_rates") or previous_subscription.get("default_tax_rates", []), "metadata": {}, "period": {"start": now, "end": old_item["current_period_end"]}, "parent": {"type": "subscription_item_details", "subscription_item_details": {"subscription": ident, "subscription_item": old_item["id"], "proration": True}}})
                        indexes = sorted({int(m[1]) for key in form if (m := re.match(r"add_invoice_items\[(\d+)\]", key))})
                        for index in indexes:
                            credit_form = {re.sub(r"^add_invoice_items\[\d+\]\[([^\]]+)\]", r"\1", key): entries for key, entries in form.items() if key.startswith(f"add_invoice_items[{index}][")}
                            credit = self.fields(credit_form)
                            amount = int(credit.get("price_data", {}).get("unit_amount", 0)) * int(credit.get("quantity", 1))
                            rates = [self.objects[r] for r in credit.get("tax_rates", [])]
                            line = {"id": "il_" + str(self.counter + 1), "object": "line_item", "amount": amount, "currency": credit.get("price_data", {}).get("currency", "eur"), "quantity": int(credit.get("quantity", 1)), "tax_rates": rates, "metadata": credit.get("metadata", {}), "period": {"start": int(credit_form.get("period[start][timestamp]", [str(now)])[-1]), "end": int(credit_form.get("period[end][timestamp]", [str(now + 2592000)])[-1])}}
                            self.counter += 1
                            invoice["lines"]["data"].append(line)
                        self.recalculate(invoice)
                        if not paused:
                            invoice["status_transitions"] = {"finalized_at": now}
                            invoice["auto_advance"] = True
                            self.attempt_payment(invoice)
                        invoice["billing_reason"] = "subscription_update"
                if collection == "invoices":
                    self.recalculate(self.objects[ident])
                return 200, self.objects[ident]
            if ident:
                return 404, {"error": {"message": "unknown simulated object", "type": "invalid_request_error"}}
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
                if "recurring" in values:
                    values["recurring"]["interval_count"] = int(values["recurring"].get("interval_count", 1))
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
            status, result = STATE.dispatch(self.command, parsed.path, urllib.parse.parse_qs(parsed.query), body, self.headers.get("X-Stack-Provider-Host", "fixture-control"), self.headers)
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
