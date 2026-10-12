#!/usr/bin/env python3
"""Stack Livepeer chain: the Ethereum JSON-RPC calls the network observer's
catalog discovery makes (api_network_observer/internal/providers/livepeer),
answered from a fixed orchestrator pool.

POST /                 JSON-RPC: eth_chainId, eth_blockNumber, and eth_call on
                       Controller.getContract, BondingManager's transcoder pool
                       and ServiceRegistry.getServiceURI.
GET  /orchestrators    the pool as JSON, for scenario assertions.

The service URIs carry documentation-range addresses: the scenario's prober
reports checks instead of dialing, and a real prober refuses non-public
destinations before dialing anyway.
"""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CHAIN_ID = 42161
BLOCK = 0x100


def address(suffix):
    return "0x5eed" + suffix.rjust(36, "0")


CONTROLLER = address("c0de")
BONDING_MANAGER = address("b0")
SERVICE_REGISTRY = address("5e")
# keccak256 of the contract names Controller.getContract is asked for.
CONTRACT_IDS = {
    "2517d59a36a86548e38734e8ab416f42afff4bca78706a66ad65750dae7f9e37": BONDING_MANAGER,
    "79c5d2a4a07754f4bacb0ffba18ac516030ee589ebc89db8627680c4d4cdb230": SERVICE_REGISTRY,
}
# The first entry has an IP-literal URI, so the coordinator creates its
# endpoint from the catalog; the second needs a prober's DNS answer.
ORCHESTRATORS = [
    {"address": address("a1"), "uri": "https://203.0.113.10:8935", "active": True, "ip": "203.0.113.10"},
    {"address": address("b2"), "uri": "https://orch-b.stack.invalid:8935", "active": True, "ip": "198.51.100.20"},
]

GET_CONTRACT = "e16c7d98"
FIRST_IN_POOL = "88a6c749"
NEXT_IN_POOL = "235c9603"
IS_ACTIVE = "08802374"
SERVICE_URI = "214c2a4b"


def word(value):
    return format(value, "064x")


def encode_address(addr):
    return word(int(addr, 16))


def encode_string(text):
    raw = text.encode()
    padded = raw.hex().ljust(((len(raw) + 31) // 32) * 64, "0")
    return word(32) + word(len(raw)) + padded


def orchestrator(arg):
    wanted = int(arg, 16)
    for index, orch in enumerate(ORCHESTRATORS):
        if int(orch["address"], 16) == wanted:
            return index, orch
    return None, None


def call(to, data):
    data = data[2:] if data.startswith("0x") else data
    selector, arg = data[:8], data[8:72]
    to = int(to, 16)
    if to == int(CONTROLLER, 16) and selector == GET_CONTRACT:
        return encode_address(CONTRACT_IDS.get(arg.lower(), "0x0"))
    if to == int(BONDING_MANAGER, 16):
        if selector == FIRST_IN_POOL:
            return encode_address(ORCHESTRATORS[0]["address"])
        index, orch = orchestrator(arg)
        if orch is not None and selector == NEXT_IN_POOL:
            following = ORCHESTRATORS[index + 1]["address"] if index + 1 < len(ORCHESTRATORS) else "0x0"
            return encode_address(following)
        if orch is not None and selector == IS_ACTIVE:
            return word(1 if orch["active"] else 0)
    if to == int(SERVICE_REGISTRY, 16) and selector == SERVICE_URI:
        _, orch = orchestrator(arg)
        if orch is not None:
            return encode_string(orch["uri"])
    raise ValueError("execution reverted")


def answer(request):
    method, params, rid = request.get("method"), request.get("params") or [], request.get("id")
    try:
        if method == "eth_chainId":
            result = hex(CHAIN_ID)
        elif method == "eth_blockNumber":
            result = hex(BLOCK)
        elif method == "eth_call":
            target = params[0]
            result = "0x" + call(target.get("to", "0x0"), target.get("input") or target.get("data") or "")
        else:
            return {"jsonrpc": "2.0", "id": rid, "error": {"code": -32601, "message": "method not found"}}
    except (ValueError, IndexError, AttributeError) as err:
        return {"jsonrpc": "2.0", "id": rid, "error": {"code": 3, "message": str(err)}}
    return {"jsonrpc": "2.0", "id": rid, "result": result}


class Handler(BaseHTTPRequestHandler):
    def _send(self, code, payload):
        body = json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/orchestrators":
            self._send(200, {"controller": CONTROLLER, "chain_id": CHAIN_ID, "orchestrators": ORCHESTRATORS})
        else:
            self._send(404, {"error": "not found"})

    def do_POST(self):
        try:
            request = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0) or 0)))
        except ValueError:
            self._send(400, {"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "parse error"}})
            return
        if isinstance(request, list):
            self._send(200, [answer(r) for r in request])
        else:
            self._send(200, answer(request))

    def log_message(self, *_):
        pass


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8545), Handler).serve_forever()
