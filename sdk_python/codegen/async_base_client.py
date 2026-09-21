"""Base class of the generated async client.

ariadne-codegen copies this file into the generated package. The transport
itself (auth, retries, the serverInfo gate, typed errors, and WebSocket
subscriptions) lives in livepeer_frameworks._transport.
"""

from livepeer_frameworks._transport import AsyncTransport


class AsyncBaseClient(AsyncTransport):
    """Runs the generated async operations through the SDK transport."""
